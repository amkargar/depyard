package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$|^[a-z0-9]$`)

// RuntimeInstance pairs a persisted Instance configuration with its active Registry implementation.
type RuntimeInstance struct {
	Instance *Instance
	Registry Registry
}

// Manager coordinates installed registry types and live registry instances.
// Instance changes take effect immediately without restarting the application.
type Manager struct {
	store InstanceStore
	deps  ServiceDeps

	mu             sync.RWMutex
	types          map[string]Registration
	byID           map[string]*RuntimeInstance
	bySlug         map[string]*RuntimeInstance
	byHost         map[string]*RuntimeInstance
	defaultsByType map[string]string
}

func NewManager(store InstanceStore, deps ServiceDeps) *Manager {
	return &Manager{
		store:          store,
		deps:           deps,
		types:          make(map[string]Registration),
		byID:           make(map[string]*RuntimeInstance),
		bySlug:         make(map[string]*RuntimeInstance),
		byHost:         make(map[string]*RuntimeInstance),
		defaultsByType: make(map[string]string),
	}
}

// RegisterType installs a compiled registry type into the manager.
func (m *Manager) RegisterType(reg Registration) error {
	t := strings.ToLower(strings.TrimSpace(reg.Type))
	if t == "" {
		return fmt.Errorf("registry type name cannot be empty")
	}
	if reg.Create == nil {
		return fmt.Errorf("registry type %q must provide a Create factory", t)
	}
	reg.Type = t

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.types[t]; exists {
		return fmt.Errorf("registry type %q is already registered", t)
	}
	m.types[t] = reg
	return nil
}

// ListTypes returns metadata for all compiled & installed registry types only.
func (m *Manager) ListTypes() []TypeInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]TypeInfo, 0, len(m.types))
	for _, r := range m.types {
		out = append(out, TypeInfo{
			Type:               r.Type,
			DisplayName:        r.DisplayName,
			Description:        r.Description,
			DefaultUpstreamURL: r.DefaultUpstreamURL,
			Capabilities:       r.Capabilities,
			SupportedModes:     r.Capabilities.SupportedModes(),
			ConfigHints:        r.ConfigHints,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// GetType returns registration metadata for a single installed registry type.
func (m *Manager) GetType(regType string) (Registration, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.types[strings.ToLower(strings.TrimSpace(regType))]
	return r, ok
}

// ValidateTypeConfig validates a config map for a registry type using that type's own validator.
// It lets core/admin code validate ecosystem configs without importing concrete implementations.
func (m *Manager) ValidateTypeConfig(regType string, cfg map[string]any) error {
	reg, ok := m.GetType(regType)
	if !ok {
		return fmt.Errorf("%w: %s", ErrTypeNotRegistered, regType)
	}
	if reg.ValidateConfig == nil {
		return nil
	}
	if err := reg.ValidateConfig(cfg); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	return nil
}

// LoadAll hydrates all persisted registry instances at startup.
// Instances whose type is not compiled into this binary are kept persisted in disabled/inactive state.
func (m *Manager) LoadAll(ctx context.Context) error {
	instances, err := m.store.ListInstances(ctx)
	if err != nil {
		return fmt.Errorf("list persisted registries: %w", err)
	}

	for _, inst := range instances {
		reg, ok := m.GetType(inst.Type)
		if !ok {
			// Skip activating unknown/uninstalled types; never fail server startup.
			continue
		}
		if err := m.activateRuntime(ctx, reg, inst); err != nil {
			return fmt.Errorf("activate registry %q: %w", inst.Slug, err)
		}
	}
	return nil
}

// CreateInstance validates, persists, and immediately activates a new registry instance.
func (m *Manager) CreateInstance(ctx context.Context, inst *Instance) (*Instance, error) {
	if inst == nil {
		return nil, fmt.Errorf("%w: instance is nil", ErrInvalidConfig)
	}
	normalized, reg, err := m.validateAndNormalize(inst, true)
	if err != nil {
		return nil, err
	}

	// Ensure slug uniqueness.
	if _, err := m.store.GetInstanceBySlug(ctx, normalized.Slug); err == nil {
		return nil, ErrSlugExists
	} else if !errors.Is(err, ErrInstanceNotFound) {
		return nil, err
	}

	// Build runtime instance BEFORE persisting so factory errors fail cleanly.
	rt, err := reg.Create(ctx, normalized.Clone(), m.deps)
	if err != nil {
		return nil, fmt.Errorf("initialize registry %q: %w", normalized.Slug, err)
	}

	// If this instance is marked default, clear previous default flag for the same registry type.
	if normalized.IsDefault {
		m.clearOtherDefaults(ctx, normalized.Type, "")
	}

	if err := m.store.CreateInstance(ctx, normalized); err != nil {
		closeRegistry(rt)
		return nil, err
	}

	m.installRuntime(&RuntimeInstance{
		Instance: normalized.Clone(),
		Registry: rt,
	})
	return normalized.Clone(), nil
}

// UpdateInstance validates, persists, and hot-reloads an existing registry instance without restart.
func (m *Manager) UpdateInstance(ctx context.Context, id string, patch *Instance) (*Instance, error) {
	existing, err := m.store.GetInstanceByID(ctx, id)
	if err != nil {
		return nil, err
	}

	updated := existing.Clone()
	if patch.Name != "" {
		updated.Name = patch.Name
	}
	updated.Description = patch.Description
	updated.Enabled = patch.Enabled
	if patch.Mode != "" {
		updated.Mode = patch.Mode
	}
	updated.UpstreamURL = patch.UpstreamURL
	if patch.UpstreamConfig != nil {
		uc := patch.UpstreamConfig.Clone()
		// Preserve existing upstream auth token if admin left the secret input blank without clearing auth_type.
		authType := strings.ToLower(strings.TrimSpace(uc.AuthType))
		if uc.AuthToken == "" && authType != "" && authType != "none" && existing.UpstreamConfig != nil {
			uc.AuthToken = existing.UpstreamConfig.AuthToken
		}
		updated.UpstreamConfig = uc
	}
	if patch.Visibility != "" {
		updated.Visibility = patch.Visibility
	}
	updated.AuthPolicy = patch.AuthPolicy
	updated.StorageQuotaBytes = patch.StorageQuotaBytes
	updated.CachePolicy = patch.CachePolicy
	if patch.Config != nil {
		updated.Config = patch.Config
	}
	updated.IsDefault = patch.IsDefault
	updated.Hostname = patch.Hostname

	// Registry type and slug are immutable once created to keep URLs & metadata stable.
	updated.Type = existing.Type
	updated.Slug = existing.Slug
	updated.ID = existing.ID
	updated.CreatedAt = existing.CreatedAt

	normalized, reg, err := m.validateAndNormalize(updated, false)
	if err != nil {
		return nil, err
	}

	rt, err := reg.Create(ctx, normalized.Clone(), m.deps)
	if err != nil {
		return nil, fmt.Errorf("reload registry %q: %w", normalized.Slug, err)
	}

	if normalized.IsDefault && !existing.IsDefault {
		m.clearOtherDefaults(ctx, normalized.Type, normalized.ID)
	}

	if err := m.store.UpdateInstance(ctx, normalized); err != nil {
		closeRegistry(rt)
		return nil, err
	}

	old := m.installRuntime(&RuntimeInstance{
		Instance: normalized.Clone(),
		Registry: rt,
	})
	if old != nil {
		closeRegistry(old.Registry)
	}
	return normalized.Clone(), nil
}

// SetEnabled enables or disables a registry instance immediately without restart.
func (m *Manager) SetEnabled(ctx context.Context, id string, enabled bool) (*Instance, error) {
	inst, err := m.store.GetInstanceByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if enabled {
		if _, ok := m.GetType(inst.Type); !ok {
			return nil, fmt.Errorf("%w: %s", ErrTypeNotRegistered, inst.Type)
		}
	}
	inst.Enabled = enabled
	inst.UpdatedAt = time.Now().UTC()
	if err := m.store.UpdateInstance(ctx, inst); err != nil {
		return nil, err
	}

	m.mu.Lock()
	if rt, ok := m.byID[id]; ok {
		rt.Instance = inst.Clone()
	}
	m.mu.Unlock()
	return inst.Clone(), nil
}

// DeleteInstance safely removes a registry instance, cleans up its metadata,
// removes its artifact references (safely preserving shared blobs), and stops serving its routes.
func (m *Manager) DeleteInstance(ctx context.Context, id string) error {
	inst, err := m.store.GetInstanceByID(ctx, id)
	if err != nil {
		return err
	}

	// Remove from active routing table first so no new requests enter.
	m.mu.Lock()
	rt := m.byID[id]
	delete(m.byID, id)
	delete(m.bySlug, inst.Slug)
	if inst.Hostname != "" {
		delete(m.byHost, strings.ToLower(inst.Hostname))
	}
	if m.defaultsByType[inst.Type] == id {
		delete(m.defaultsByType, inst.Type)
	}
	m.mu.Unlock()

	if rt != nil {
		if cleaner, ok := rt.Registry.(RegistryCleaner); ok {
			_ = cleaner.CleanupRegistry(ctx)
		}
		closeRegistry(rt.Registry)
	}

	if m.deps.Storage != nil {
		_, _ = m.deps.Storage.DeleteRegistryArtifacts(ctx, id)
	}
	if m.deps.Metrics != nil {
		m.deps.Metrics.DeleteRegistry(ctx, id)
	}

	return m.store.DeleteInstance(ctx, id)
}

func (m *Manager) ListInstances(ctx context.Context) ([]*Instance, error) {
	instances, err := m.store.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(instances, func(i, j int) bool {
		if instances[i].IsDefault != instances[j].IsDefault {
			return instances[i].IsDefault
		}
		return instances[i].Slug < instances[j].Slug
	})
	return instances, nil
}

func (m *Manager) GetInstance(ctx context.Context, id string) (*Instance, error) {
	return m.store.GetInstanceByID(ctx, id)
}

func (m *Manager) GetInstanceBySlug(ctx context.Context, slug string) (*Instance, error) {
	return m.store.GetInstanceBySlug(ctx, slug)
}

func (m *Manager) GetRuntimeByID(id string) (*RuntimeInstance, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rt, ok := m.byID[id]
	return rt, ok
}

func (m *Manager) GetRuntimeBySlug(slug string) (*RuntimeInstance, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rt, ok := m.bySlug[strings.ToLower(strings.TrimSpace(slug))]
	return rt, ok
}

func (m *Manager) GetRuntimeByHostname(host string) (*RuntimeInstance, bool) {
	h := strings.ToLower(strings.TrimSpace(host))
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	m.mu.RLock()
	defer m.mu.RUnlock()
	rt, ok := m.byHost[h]
	return rt, ok
}

func (m *Manager) GetDefaultRuntime() (*RuntimeInstance, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.defaultsByType) == 0 {
		return nil, false
	}
	types := make([]string, 0, len(m.defaultsByType))
	for t := range m.defaultsByType {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		if rt, ok := m.byID[m.defaultsByType[t]]; ok {
			return rt, true
		}
	}
	return nil, false
}

// GetDefaultRuntimeForType returns the default compatibility runtime for a specific registry type.
func (m *Manager) GetDefaultRuntimeForType(regType string) (*RuntimeInstance, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.defaultsByType[strings.ToLower(strings.TrimSpace(regType))]
	if !ok || id == "" {
		return nil, false
	}
	rt, ok := m.byID[id]
	return rt, ok
}

// GetCompatRuntimes returns all enabled default runtimes (one per registry type) for root compatibility routing.
func (m *Manager) GetCompatRuntimes() []*RuntimeInstance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.defaultsByType) == 0 {
		return nil
	}
	types := make([]string, 0, len(m.defaultsByType))
	for t := range m.defaultsByType {
		types = append(types, t)
	}
	sort.Strings(types)
	out := make([]*RuntimeInstance, 0, len(types))
	for _, t := range types {
		if rt, ok := m.byID[m.defaultsByType[t]]; ok && rt.Instance.Enabled {
			out = append(out, rt)
		}
	}
	return out
}

func (m *Manager) AllRuntimes() []*RuntimeInstance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*RuntimeInstance, 0, len(m.byID))
	for _, rt := range m.byID {
		out = append(out, rt)
	}
	return out
}

func (m *Manager) validateAndNormalize(inst *Instance, isCreate bool) (*Instance, Registration, error) {
	cp := inst.Clone()
	cp.Type = strings.ToLower(strings.TrimSpace(cp.Type))
	cp.Slug = strings.ToLower(strings.TrimSpace(cp.Slug))
	cp.Name = strings.TrimSpace(cp.Name)
	cp.Description = strings.TrimSpace(cp.Description)
	cp.UpstreamURL = strings.TrimRight(strings.TrimSpace(cp.UpstreamURL), "/")
	cp.Hostname = strings.ToLower(strings.TrimSpace(cp.Hostname))

	reg, ok := m.GetType(cp.Type)
	if !ok {
		return nil, Registration{}, fmt.Errorf("%w: %q", ErrTypeNotRegistered, cp.Type)
	}

	if cp.Name == "" {
		return nil, Registration{}, fmt.Errorf("%w: name is required", ErrInvalidConfig)
	}
	if !slugPattern.MatchString(cp.Slug) {
		return nil, Registration{}, fmt.Errorf("%w: slug %q must be lowercase alphanumeric with hyphens", ErrInvalidConfig, cp.Slug)
	}

	// Validate repository mode against installed registry type capabilities.
	if err := reg.Capabilities.SupportsMode(cp.Mode); err != nil {
		return nil, Registration{}, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}

	// Upstream URL is required for proxy and hybrid modes.
	if cp.Mode == ModeProxy || cp.Mode == ModeHybrid {
		if cp.UpstreamURL == "" {
			return nil, Registration{}, fmt.Errorf("%w: upstream_url is required for %s mode", ErrInvalidConfig, cp.Mode)
		}
		u, err := url.Parse(cp.UpstreamURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, Registration{}, fmt.Errorf("%w: upstream_url %q must be a valid http or https URL", ErrInvalidConfig, cp.UpstreamURL)
		}
	}

	if cp.UpstreamConfig != nil {
		if err := cp.UpstreamConfig.Validate(); err != nil {
			return nil, Registration{}, err
		}
	}

	if cp.Visibility == "" {
		cp.Visibility = VisibilityPublic
	}
	if cp.Visibility != VisibilityPublic && cp.Visibility != VisibilityPrivate {
		return nil, Registration{}, fmt.Errorf("%w: invalid visibility %q", ErrInvalidConfig, cp.Visibility)
	}
	if cp.Visibility == VisibilityPrivate {
		cp.AuthPolicy.AllowAnonymousRead = false
	}

	if cp.StorageQuotaBytes < 0 {
		return nil, Registration{}, fmt.Errorf("%w: storage_quota_bytes cannot be negative", ErrInvalidConfig)
	}

	if cp.CachePolicy.EvictionPolicy == "" {
		cp.CachePolicy.EvictionPolicy = "lru"
	}
	if cp.CachePolicy.MetadataTTLSeconds < 0 || cp.CachePolicy.ArchiveTTLSeconds < 0 || cp.CachePolicy.MaxSizeBytes < 0 {
		return nil, Registration{}, fmt.Errorf("%w: cache policy values cannot be negative", ErrInvalidConfig)
	}

	if cp.Config == nil {
		cp.Config = map[string]any{}
	}
	if reg.ValidateConfig != nil {
		if err := reg.ValidateConfig(cp.Config); err != nil {
			return nil, Registration{}, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
	}

	now := time.Now().UTC()
	if isCreate {
		if cp.ID == "" {
			cp.ID = newID()
		}
		cp.CreatedAt = now
	}
	cp.UpdatedAt = now
	return cp, reg, nil
}

func (m *Manager) activateRuntime(ctx context.Context, reg Registration, inst *Instance) error {
	rt, err := reg.Create(ctx, inst.Clone(), m.deps)
	if err != nil {
		return err
	}
	m.installRuntime(&RuntimeInstance{
		Instance: inst.Clone(),
		Registry: rt,
	})
	return nil
}

func (m *Manager) installRuntime(rt *RuntimeInstance) *RuntimeInstance {
	m.mu.Lock()
	defer m.mu.Unlock()

	old := m.byID[rt.Instance.ID]
	if old != nil && old.Instance.Hostname != "" {
		delete(m.byHost, strings.ToLower(old.Instance.Hostname))
	}

	m.byID[rt.Instance.ID] = rt
	m.bySlug[rt.Instance.Slug] = rt
	if rt.Instance.Hostname != "" {
		m.byHost[strings.ToLower(rt.Instance.Hostname)] = rt
	}
	if rt.Instance.IsDefault {
		m.defaultsByType[rt.Instance.Type] = rt.Instance.ID
	} else if m.defaultsByType[rt.Instance.Type] == rt.Instance.ID {
		delete(m.defaultsByType, rt.Instance.Type)
	}
	return old
}

func (m *Manager) clearOtherDefaults(ctx context.Context, regType, exceptID string) {
	instances, err := m.store.ListInstances(ctx)
	if err != nil {
		return
	}
	for _, inst := range instances {
		if inst.ID == exceptID || !inst.IsDefault || inst.Type != regType {
			continue
		}
		inst.IsDefault = false
		inst.UpdatedAt = time.Now().UTC()
		_ = m.store.UpdateInstance(ctx, inst)
		m.mu.Lock()
		if rt, ok := m.byID[inst.ID]; ok {
			rt.Instance.IsDefault = false
		}
		m.mu.Unlock()
	}
}

func closeRegistry(r Registry) {
	if c, ok := r.(Closer); ok {
		_ = c.Close()
	}
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
