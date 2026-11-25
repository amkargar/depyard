package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amkargar/mirror-registry/backend/internal/audit"
	"github.com/amkargar/mirror-registry/backend/internal/auth"
	"github.com/amkargar/mirror-registry/backend/internal/cache"
	"github.com/amkargar/mirror-registry/backend/internal/metrics"
	"github.com/amkargar/mirror-registry/backend/internal/registry"
	"github.com/amkargar/mirror-registry/backend/internal/storage"
	"github.com/amkargar/mirror-registry/backend/internal/upstream"
)

// Handler serves the registry-agnostic Admin REST API and public registry directory.
type Handler struct {
	manager       *registry.Manager
	auth          *auth.Service
	storage       *storage.Service
	cache         *cache.Coordinator
	metrics       *metrics.Collector
	audit         *audit.Service
	upstream      *upstream.Client
	settingsStore SettingsStore
	settingsMu    sync.RWMutex
	defaultConfig *PlatformSettings
	mux           *http.ServeMux
}

func NewHandler(
	manager *registry.Manager,
	authSvc *auth.Service,
	storageSvc *storage.Service,
	cacheCoord *cache.Coordinator,
	metricsCol *metrics.Collector,
	auditSvc *audit.Service,
	settingsStore SettingsStore,
	defaults *PlatformSettings,
) *Handler {
	if defaults == nil {
		defaults = DefaultSettings("", true)
	}
	defaults.Normalize()
	h := &Handler{
		manager:       manager,
		auth:          authSvc,
		storage:       storageSvc,
		cache:         cacheCoord,
		metrics:       metricsCol,
		audit:         auditSvc,
		settingsStore: settingsStore,
		defaultConfig: defaults,
		mux:           http.NewServeMux(),
	}
	h.registerRoutes()
	return h
}

// SetUpstreamClient attaches the shared upstream HTTP client so live setting edits and connection tests apply immediately.
func (h *Handler) SetUpstreamClient(c *upstream.Client) {
	h.upstream = c
}

// CurrentSettings returns a thread-safe snapshot of the active global platform settings.
func (h *Handler) CurrentSettings() PlatformSettings {
	h.settingsMu.RLock()
	defer h.settingsMu.RUnlock()
	cp := *h.defaultConfig
	return cp
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) registerRoutes() {
	// Public directory for the package explorer
	h.mux.HandleFunc("GET /api/public/registries", h.handlePublicListRegistries)
	h.mux.HandleFunc("GET /api/public/registries/{slug}", h.handlePublicGetRegistry)

	// Authentication
	h.mux.HandleFunc("POST /api/admin/auth/login", h.handleLogin)
	h.mux.HandleFunc("POST /api/admin/auth/logout", h.handleLogout)
	h.mux.HandleFunc("GET /api/admin/auth/me", h.handleMe)

	// Installed Registry Types
	h.mux.HandleFunc("GET /api/admin/registry-types", h.requireAdmin(h.handleListRegistryTypes))

	// Registry Instances
	h.mux.HandleFunc("GET /api/admin/registries", h.requireAdmin(h.handleListRegistries))
	h.mux.HandleFunc("POST /api/admin/registries", h.requireAdmin(h.handleCreateRegistry))
	h.mux.HandleFunc("GET /api/admin/registries/{id}", h.requireAdmin(h.handleGetRegistry))
	h.mux.HandleFunc("PUT /api/admin/registries/{id}", h.requireAdmin(h.handleUpdateRegistry))
	h.mux.HandleFunc("PATCH /api/admin/registries/{id}/enabled", h.requireAdmin(h.handleToggleRegistry))
	h.mux.HandleFunc("DELETE /api/admin/registries/{id}", h.requireAdmin(h.handleDeleteRegistry))
	h.mux.HandleFunc("POST /api/admin/registries/{id}/health-check", h.requireAdmin(h.handleHealthCheck))

	// Ecosystem version registry (e.g. Flutter SDK versions & storage settings)
	h.mux.HandleFunc("GET /api/admin/registries/{id}/version-registry", h.requireAdmin(h.handleVersionRegistry))

	// Capability-gated Artifact & Cache management
	h.mux.HandleFunc("GET /api/admin/registries/{id}/artifacts", h.requireAdmin(h.handleListArtifacts))
	h.mux.HandleFunc("DELETE /api/admin/registries/{id}/artifacts/{name}", h.requireAdmin(h.handleDeleteArtifact))
	h.mux.HandleFunc("POST /api/admin/registries/{id}/cache/purge", h.requireAdmin(h.handlePurgeCache))
	h.mux.HandleFunc("POST /api/admin/registries/{id}/cache/warm", h.requireAdmin(h.handleWarmCache))

	// Access Tokens
	h.mux.HandleFunc("GET /api/admin/tokens", h.requireAdmin(h.handleListTokens))
	h.mux.HandleFunc("POST /api/admin/tokens", h.requireAdmin(h.handleCreateToken))
	h.mux.HandleFunc("POST /api/admin/tokens/{id}/revoke", h.requireAdmin(h.handleRevokeToken))
	h.mux.HandleFunc("DELETE /api/admin/tokens/{id}", h.requireAdmin(h.handleDeleteToken))

	// Users
	h.mux.HandleFunc("GET /api/admin/users", h.requireAdmin(h.handleListUsers))
	h.mux.HandleFunc("POST /api/admin/users", h.requireAdmin(h.handleCreateUser))
	h.mux.HandleFunc("PUT /api/admin/users/{id}", h.requireAdmin(h.handleUpdateUser))
	h.mux.HandleFunc("DELETE /api/admin/users/{id}", h.requireAdmin(h.handleDeleteUser))

	// Unified Monitoring & Stats
	h.mux.HandleFunc("GET /api/admin/stats/overview", h.requireAdmin(h.handleStatsOverview))
	h.mux.HandleFunc("GET /api/admin/stats/registries/{id}", h.requireAdmin(h.handleRegistryStats))

	// Audit Logs, Settings & Upstream Diagnostics
	h.mux.HandleFunc("GET /api/admin/audit-logs", h.requireAdmin(h.handleAuditLogs))
	h.mux.HandleFunc("GET /api/admin/settings", h.requireAdmin(h.handleGetSettings))
	h.mux.HandleFunc("PUT /api/admin/settings", h.requireAdmin(h.handleUpdateSettings))
	// Platform-wide base storage settings: preview the impact and push it to existing registries.
	h.mux.HandleFunc("GET /api/admin/settings/base-config/{type}/impact", h.requireAdmin(h.handleBaseConfigImpact))
	h.mux.HandleFunc("POST /api/admin/settings/base-config/{type}/apply", h.requireAdmin(h.handleBaseConfigApply))
	h.mux.HandleFunc("GET /api/admin/settings/base-config", h.requireAdmin(h.handleListBaseConfigs))
	h.mux.HandleFunc("POST /api/admin/upstream/test", h.requireAdmin(h.handleTestUpstream))
}

type adminHandlerFunc func(w http.ResponseWriter, r *http.Request, principal *auth.Principal)

func (h *Handler) requireAdmin(fn adminHandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, err := h.auth.AuthenticateRequest(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if !principal.Authenticated {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if !principal.IsAdmin() {
			writeError(w, http.StatusForbidden, "admin privileges required")
			return
		}
		fn(w, r, principal)
	}
}

// --- Public Directory ---

type PublicRegistryView struct {
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	Slug          string                `json:"slug"`
	Type          string                `json:"type"`
	Description   string                `json:"description"`
	Mode          registry.RepositoryMode `json:"mode"`
	Visibility    registry.Visibility   `json:"visibility"`
	IsDefault     bool                  `json:"is_default"`
	BasePath      string                `json:"base_path"`
	Capabilities  registry.Capabilities `json:"capabilities"`
	ArtifactCount int64                 `json:"artifact_count"`
	Downloads     int64                 `json:"downloads"`
}

func (h *Handler) handlePublicListRegistries(w http.ResponseWriter, r *http.Request) {
	principal, _ := h.auth.AuthenticateRequest(r.Context(), r)
	instances, err := h.manager.ListInstances(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := make([]PublicRegistryView, 0, len(instances))
	for _, inst := range instances {
		if !inst.Enabled {
			continue
		}
		// Public registries are visible to everyone; private registries only if caller is authorized to read.
		if inst.Visibility != registry.VisibilityPublic {
			if err := h.auth.Authorize(principal, auth.AuthRequest{
				Policy:     inst.AccessPolicy(),
				Permission: auth.PermissionRead,
			}); err != nil {
				continue
			}
		}
		out = append(out, h.buildPublicView(r.Context(), inst))
	}

	settings, _ := h.getSettings(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"registries":               out,
		"site_title":               settings.SiteTitle,
		"enable_root_compat_route": settings.EnableRootCompatRoute,
	})
}

func (h *Handler) handlePublicGetRegistry(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	inst, err := h.manager.GetInstanceBySlug(r.Context(), slug)
	if err != nil {
		if errors.Is(err, registry.ErrInstanceNotFound) {
			writeError(w, http.StatusNotFound, "registry not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !inst.Enabled {
		writeError(w, http.StatusServiceUnavailable, "registry is disabled")
		return
	}
	principal, authErr := h.auth.AuthenticateRequest(r.Context(), r)
	if authErr != nil {
		writeError(w, http.StatusUnauthorized, authErr.Error())
		return
	}
	if err := h.auth.Authorize(principal, auth.AuthRequest{
		Policy:     inst.AccessPolicy(),
		Permission: auth.PermissionRead,
	}); err != nil {
		if errors.Is(err, auth.ErrUnauthenticated) {
			writeError(w, http.StatusUnauthorized, "authentication required")
		} else {
			writeError(w, http.StatusForbidden, "permission denied")
		}
		return
	}
	writeJSON(w, http.StatusOK, h.buildPublicView(r.Context(), inst))
}

func (h *Handler) buildPublicView(ctx context.Context, inst *registry.Instance) PublicRegistryView {
	var caps registry.Capabilities
	var artifactCount int64
	if rt, ok := h.manager.GetRuntimeByID(inst.ID); ok {
		caps = rt.Registry.Capabilities().EffectiveForMode(inst.Mode)
		if browser, ok := rt.Registry.(registry.ArtifactBrowser); ok {
			if page, err := browser.ListArtifacts(ctx, registry.ArtifactQuery{Page: 1, Limit: 1}); err == nil {
				artifactCount = page.Total
			}
		}
	}
	var downloads int64
	if h.metrics != nil {
		if snap, err := h.metrics.GetRegistrySnapshot(ctx, inst.ID); err == nil {
			downloads = snap.Downloads
		}
	}
	return PublicRegistryView{
		ID:            inst.ID,
		Name:          inst.Name,
		Slug:          inst.Slug,
		Type:          inst.Type,
		Description:   inst.Description,
		Mode:          inst.Mode,
		Visibility:    inst.Visibility,
		IsDefault:     inst.IsDefault,
		BasePath:      "/r/" + inst.Slug,
		Capabilities:  caps,
		ArtifactCount: artifactCount,
		Downloads:     downloads,
	}
}

// --- Auth ---

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	user, rawToken, sess, err := h.auth.Login(r.Context(), body.Username, body.Password, r.UserAgent(), clientIP(r))
	if err != nil {
		h.audit.Record(r.Context(), audit.Entry{
			ActorName: body.Username,
			ActorType: "user",
			Action:    "auth.login",
			IPAddress: clientIP(r),
			Success:   false,
		})
		status := http.StatusUnauthorized
		if errors.Is(err, auth.ErrUserDisabled) {
			status = http.StatusForbidden
		}
		writeError(w, status, err.Error())
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    rawToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  sess.ExpiresAt,
	})
	h.audit.Record(r.Context(), audit.Entry{
		ActorID:   user.ID,
		ActorName: user.Username,
		ActorType: "user",
		Action:    "auth.login",
		IPAddress: clientIP(r),
		Success:   true,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"user":       user,
		"token":      rawToken,
		"expires_at": sess.ExpiresAt,
	})
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil && cookie.Value != "" {
		_ = h.auth.Logout(r.Context(), cookie.Value)
	}
	if ah := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(ah), "bearer ") {
		raw := strings.TrimSpace(ah[7:])
		if strings.HasPrefix(raw, "mrs_") {
			_ = h.auth.Logout(r.Context(), raw)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
	principal, err := h.auth.AuthenticateRequest(r.Context(), r)
	if err != nil || !principal.Authenticated {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"kind":          principal.Kind,
		"is_admin":      principal.IsAdmin(),
		"user":          principal.User,
	})
}

// --- Installed Registry Types ---

func (h *Handler) handleListRegistryTypes(w http.ResponseWriter, _ *http.Request, _ *auth.Principal) {
	writeJSON(w, http.StatusOK, map[string]any{
		"types": h.manager.ListTypes(),
	})
}

// --- Registry Instances ---

type EnrichedRegistry struct {
	*registry.Instance
	Installed             bool                         `json:"installed"`
	BasePath              string                       `json:"base_path"`
	Health                string                       `json:"health"` // "healthy", "degraded", "unhealthy", "disabled"
	TypeCapabilities      registry.Capabilities        `json:"type_capabilities"`
	EffectiveCapabilities registry.Capabilities        `json:"effective_capabilities"`
	Storage               storage.RegistryStorageStats `json:"storage"`
	QuotaUtilization      float64                      `json:"quota_utilization"`
	Metrics               metrics.RegistrySnapshot     `json:"metrics"`
	ArtifactCount         int64                        `json:"artifact_count"`
}

func (h *Handler) enrichRegistry(ctx context.Context, inst *registry.Instance) EnrichedRegistry {
	res := EnrichedRegistry{
		Instance: inst.SanitizedClone(),
		BasePath: "/r/" + inst.Slug,
		Health:   "healthy",
	}
	if regType, ok := h.manager.GetType(inst.Type); ok {
		res.Installed = true
		res.TypeCapabilities = regType.Capabilities
		res.EffectiveCapabilities = regType.Capabilities.EffectiveForMode(inst.Mode)
	}
	if h.storage != nil {
		if st, err := h.storage.GetRegistryStats(ctx, inst.ID); err == nil {
			res.Storage = st
			if inst.StorageQuotaBytes > 0 {
				res.QuotaUtilization = float64(st.LogicalBytes) / float64(inst.StorageQuotaBytes)
			}
		}
	}
	if h.metrics != nil {
		if snap, err := h.metrics.GetRegistrySnapshot(ctx, inst.ID); err == nil {
			res.Metrics = snap
		}
	}
	if rt, ok := h.manager.GetRuntimeByID(inst.ID); ok {
		if browser, ok := rt.Registry.(registry.ArtifactBrowser); ok {
			if page, err := browser.ListArtifacts(ctx, registry.ArtifactQuery{Page: 1, Limit: 1}); err == nil {
				res.ArtifactCount = page.Total
			}
		}
	}

	// Derive health status cleanly
	if !inst.Enabled {
		res.Health = "disabled"
	} else if !res.Installed {
		res.Health = "unhealthy"
	} else if inst.Mode == registry.ModeProxy || inst.Mode == registry.ModeHybrid {
		if res.Metrics.Upstream.Checked && !res.Metrics.Upstream.Available {
			res.Health = "degraded"
		}
	}
	return res
}

func (h *Handler) handleListRegistries(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	instances, err := h.manager.ListInstances(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]EnrichedRegistry, 0, len(instances))
	for _, inst := range instances {
		out = append(out, h.enrichRegistry(r.Context(), inst))
	}
	writeJSON(w, http.StatusOK, map[string]any{"registries": out})
}

func (h *Handler) handleGetRegistry(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	id := r.PathValue("id")
	inst, err := h.manager.GetInstance(r.Context(), id)
	if err != nil {
		if errors.Is(err, registry.ErrInstanceNotFound) {
			writeError(w, http.StatusNotFound, "registry not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h.enrichRegistry(r.Context(), inst))
}

func (h *Handler) handleCreateRegistry(w http.ResponseWriter, r *http.Request, principal *auth.Principal) {
	var body struct {
		Name              string                  `json:"name"`
		Slug              string                  `json:"slug"`
		Type              string                  `json:"type"`
		Description       string                  `json:"description"`
		Enabled           *bool                    `json:"enabled"`
		Mode              registry.RepositoryMode  `json:"mode"`
		UpstreamURL       string                   `json:"upstream_url"`
		UpstreamConfig    *registry.UpstreamConfig `json:"upstream_config"`
		Visibility        registry.Visibility      `json:"visibility"`
		AuthPolicy        *registry.AuthPolicy    `json:"auth_policy"`
		StorageQuotaBytes int64                   `json:"storage_quota_bytes"`
		CachePolicy       *registry.CachePolicy   `json:"cache_policy"`
		Config            map[string]any          `json:"config"`
		IsDefault         bool                    `json:"is_default"`
		Hostname          string                  `json:"hostname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	authPol := registry.AuthPolicy{
		AllowAnonymousRead: body.Visibility != registry.VisibilityPrivate,
		RequireAuthPublish: true,
	}
	if body.AuthPolicy != nil {
		authPol = *body.AuthPolicy
	}
	cachePol := registry.DefaultCachePolicy()
	if body.CachePolicy != nil {
		cachePol = *body.CachePolicy
	}

	inst, err := h.manager.CreateInstance(r.Context(), &registry.Instance{
		Name:              body.Name,
		Slug:              body.Slug,
		Type:              body.Type,
		Description:       body.Description,
		Enabled:           enabled,
		Mode:              body.Mode,
		UpstreamURL:       body.UpstreamURL,
		UpstreamConfig:    body.UpstreamConfig,
		Visibility:        body.Visibility,
		AuthPolicy:        authPol,
		StorageQuotaBytes: body.StorageQuotaBytes,
		CachePolicy:       cachePol,
		Config:            body.Config,
		IsDefault:         body.IsDefault,
		Hostname:          body.Hostname,
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, registry.ErrSlugExists) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}

	h.audit.Record(r.Context(), audit.Entry{
		ActorID:    principal.ActorID(),
		ActorName:  principal.ActorName(),
		ActorType:  principal.Kind,
		Action:     "registry.create",
		RegistryID: inst.ID,
		Target:     inst.Slug,
		IPAddress:  clientIP(r),
		Success:    true,
		Details:    map[string]any{"type": inst.Type, "mode": inst.Mode},
	})
	writeJSON(w, http.StatusCreated, h.enrichRegistry(r.Context(), inst))
}

func (h *Handler) handleUpdateRegistry(w http.ResponseWriter, r *http.Request, principal *auth.Principal) {
	id := r.PathValue("id")
	var body registry.Instance
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	updated, err := h.manager.UpdateInstance(r.Context(), id, &body)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, registry.ErrInstanceNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}

	h.audit.Record(r.Context(), audit.Entry{
		ActorID:    principal.ActorID(),
		ActorName:  principal.ActorName(),
		ActorType:  principal.Kind,
		Action:     "registry.update",
		RegistryID: updated.ID,
		Target:     updated.Slug,
		IPAddress:  clientIP(r),
		Success:    true,
	})
	writeJSON(w, http.StatusOK, h.enrichRegistry(r.Context(), updated))
}

func (h *Handler) handleToggleRegistry(w http.ResponseWriter, r *http.Request, principal *auth.Principal) {
	id := r.PathValue("id")
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	updated, err := h.manager.SetEnabled(r.Context(), id, body.Enabled)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, registry.ErrInstanceNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}

	h.audit.Record(r.Context(), audit.Entry{
		ActorID:    principal.ActorID(),
		ActorName:  principal.ActorName(),
		ActorType:  principal.Kind,
		Action:     "registry.toggle",
		RegistryID: updated.ID,
		Target:     updated.Slug,
		IPAddress:  clientIP(r),
		Success:    true,
		Details:    map[string]any{"enabled": updated.Enabled},
	})
	writeJSON(w, http.StatusOK, h.enrichRegistry(r.Context(), updated))
}

func (h *Handler) handleDeleteRegistry(w http.ResponseWriter, r *http.Request, principal *auth.Principal) {
	id := r.PathValue("id")
	inst, err := h.manager.GetInstance(r.Context(), id)
	if err != nil {
		if errors.Is(err, registry.ErrInstanceNotFound) {
			writeError(w, http.StatusNotFound, "registry not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.manager.DeleteInstance(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Record(r.Context(), audit.Entry{
		ActorID:    principal.ActorID(),
		ActorName:  principal.ActorName(),
		ActorType:  principal.Kind,
		Action:     "registry.delete",
		RegistryID: id,
		Target:     inst.Slug,
		IPAddress:  clientIP(r),
		Success:    true,
	})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

func (h *Handler) handleHealthCheck(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	id := r.PathValue("id")
	rt, ok := h.manager.GetRuntimeByID(id)
	if !ok {
		writeError(w, http.StatusNotFound, "registry runtime not found")
		return
	}
	checker, ok := rt.Registry.(registry.HealthChecker)
	if !ok {
		writeError(w, http.StatusNotImplemented, "registry type does not support upstream health checks")
		return
	}
	res := checker.CheckHealth(r.Context())
	now := res.CheckedAt
	h.metrics.SetUpstreamHealth(id, metrics.UpstreamHealth{
		Checked:    true,
		Available:  res.Available,
		StatusCode: res.StatusCode,
		LatencyMs:  res.LatencyMs,
		Error:      res.Error,
		CheckedAt:  &now,
	})
	writeJSON(w, http.StatusOK, res)
}

// --- Capability-Gated Artifact & Cache Operations ---

func (h *Handler) handleListArtifacts(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	id := r.PathValue("id")
	rt, ok := h.manager.GetRuntimeByID(id)
	if !ok {
		writeError(w, http.StatusNotFound, "registry not found")
		return
	}
	browser, ok := rt.Registry.(registry.ArtifactBrowser)
	if !ok {
		writeError(w, http.StatusNotImplemented, "registry does not support artifact browsing")
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if page <= 0 {
		page = 1
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	res, err := browser.ListArtifacts(r.Context(), registry.ArtifactQuery{
		Search: r.URL.Query().Get("search"),
		Origin: r.URL.Query().Get("origin"),
		Page:   page,
		Limit:  limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleVersionRegistry proxies an ecosystem's per-version registry snapshot (e.g. every Flutter
// SDK release with its FLUTTER_STORAGE_BASE_URL settings) through the registry-agnostic Admin API.
func (h *Handler) handleVersionRegistry(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	id := r.PathValue("id")
	rt, ok := h.manager.GetRuntimeByID(id)
	if !ok {
		writeError(w, http.StatusNotFound, "registry not found")
		return
	}
	provider, ok := rt.Registry.(registry.VersionRegistryProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "registry does not expose a version registry")
		return
	}
	ctx := registry.WithBaseURL(r.Context(), h.resolveBaseURL(r, rt.Instance.Slug))
	snapshot, err := provider.VersionRegistry(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"registry_id":   rt.Instance.ID,
		"registry_slug": rt.Instance.Slug,
		"registry_type": rt.Instance.Type,
		"registry":      snapshot,
	})
}

// resolveBaseURL builds the externally visible base URL for a registry instance, honoring the
// platform public_base_url setting and reverse-proxy headers before falling back to the request host.
func (h *Handler) resolveBaseURL(r *http.Request, slug string) string {
	h.settingsMu.RLock()
	publicBase := ""
	if h.defaultConfig != nil {
		publicBase = h.defaultConfig.PublicBaseURL
	}
	h.settingsMu.RUnlock()
	if publicBase != "" {
		return strings.TrimRight(publicBase, "/") + "/r/" + slug
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		scheme = strings.Split(proto, ",")[0]
	}
	host := r.Host
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); fwd != "" {
		host = strings.Split(fwd, ",")[0]
	}
	if host == "" {
		return "/r/" + slug
	}
	return scheme + "://" + strings.TrimSpace(host) + "/r/" + slug
}

func (h *Handler) handleDeleteArtifact(w http.ResponseWriter, r *http.Request, principal *auth.Principal) {
	id := r.PathValue("id")
	name := r.PathValue("name")
	version := r.URL.Query().Get("version")

	rt, ok := h.manager.GetRuntimeByID(id)
	if !ok {
		writeError(w, http.StatusNotFound, "registry not found")
		return
	}
	caps := rt.Registry.Capabilities().EffectiveForMode(rt.Instance.Mode)
	if !caps.ArtifactDeletion {
		writeError(w, http.StatusNotImplemented, "artifact deletion is not supported by this registry")
		return
	}
	deleter, ok := rt.Registry.(registry.ArtifactDeleter)
	if !ok {
		writeError(w, http.StatusNotImplemented, "artifact deletion is not implemented by this registry")
		return
	}
	if err := deleter.DeleteArtifact(r.Context(), name, version); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	target := name
	if version != "" {
		target = name + "@" + version
	}
	h.audit.Record(r.Context(), audit.Entry{
		ActorID:    principal.ActorID(),
		ActorName:  principal.ActorName(),
		ActorType:  principal.Kind,
		Action:     "artifact.delete",
		RegistryID: id,
		Target:     target,
		IPAddress:  clientIP(r),
		Success:    true,
	})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "target": target})
}

func (h *Handler) handlePurgeCache(w http.ResponseWriter, r *http.Request, principal *auth.Principal) {
	id := r.PathValue("id")
	rt, ok := h.manager.GetRuntimeByID(id)
	if !ok {
		writeError(w, http.StatusNotFound, "registry not found")
		return
	}
	caps := rt.Registry.Capabilities().EffectiveForMode(rt.Instance.Mode)
	if !caps.Proxy {
		writeError(w, http.StatusNotImplemented, "cache purge is only available for proxy or hybrid registries")
		return
	}

	var res storage.EvictResult
	var err error
	if purger, ok := rt.Registry.(registry.CachePurger); ok {
		res, err = purger.PurgeCache(r.Context())
	} else {
		res, err = h.cache.PurgeRegistry(r.Context(), id)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Record(r.Context(), audit.Entry{
		ActorID:    principal.ActorID(),
		ActorName:  principal.ActorName(),
		ActorType:  principal.Kind,
		Action:     "cache.purge",
		RegistryID: id,
		Target:     rt.Instance.Slug,
		IPAddress:  clientIP(r),
		Success:    true,
		Details:    map[string]any{"removed_refs": res.RemovedRefs, "freed_bytes": res.FreedLogical},
	})
	writeJSON(w, http.StatusOK, res)
}

func (h *Handler) handleWarmCache(w http.ResponseWriter, r *http.Request, principal *auth.Principal) {
	id := r.PathValue("id")
	rt, ok := h.manager.GetRuntimeByID(id)
	if !ok {
		writeError(w, http.StatusNotFound, "registry not found")
		return
	}
	caps := rt.Registry.Capabilities().EffectiveForMode(rt.Instance.Mode)
	if !caps.CacheWarm {
		writeError(w, http.StatusNotImplemented, "cache warming is not supported by this registry")
		return
	}
	warmer, ok := rt.Registry.(registry.CacheWarmer)
	if !ok {
		writeError(w, http.StatusNotImplemented, "cache warming is not implemented by this registry")
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "artifact name is required")
		return
	}
	if err := warmer.WarmArtifact(r.Context(), strings.TrimSpace(body.Name)); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	h.audit.Record(r.Context(), audit.Entry{
		ActorID:    principal.ActorID(),
		ActorName:  principal.ActorName(),
		ActorType:  principal.Kind,
		Action:     "cache.warm",
		RegistryID: id,
		Target:     body.Name,
		IPAddress:  clientIP(r),
		Success:    true,
	})
	writeJSON(w, http.StatusOK, map[string]any{"warmed": true, "name": body.Name})
}

// --- Access Tokens ---

func (h *Handler) handleListTokens(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	registryID := r.URL.Query().Get("registry_id")
	tokens, err := h.auth.ListTokens(r.Context(), registryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tokens == nil {
		tokens = []*auth.AccessToken{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

func (h *Handler) handleCreateToken(w http.ResponseWriter, r *http.Request, principal *auth.Principal) {
	var body struct {
		Name         string            `json:"name"`
		RegistryID   string            `json:"registry_id"`
		RegistryType string            `json:"registry_type"`
		Scopes       []auth.Permission `json:"scopes"`
		Namespaces   []string          `json:"namespaces"`
		ExpiresDays  int               `json:"expires_days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	var slug string
	if body.RegistryID != "" {
		inst, err := h.manager.GetInstance(r.Context(), body.RegistryID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "selected registry does not exist")
			return
		}
		slug = inst.Slug
		if body.RegistryType == "" {
			body.RegistryType = inst.Type
		}
	}

	var expiresIn *time.Duration
	if body.ExpiresDays > 0 {
		d := time.Duration(body.ExpiresDays) * 24 * time.Hour
		expiresIn = &d
	}

	userID := ""
	if principal.User != nil {
		userID = principal.User.ID
	}

	tok, plaintext, err := h.auth.CreateToken(r.Context(), auth.CreateTokenInput{
		Name:         body.Name,
		UserID:       userID,
		RegistryID:   body.RegistryID,
		RegistrySlug: slug,
		RegistryType: body.RegistryType,
		Scopes:       body.Scopes,
		Namespaces:   body.Namespaces,
		ExpiresIn:    expiresIn,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.audit.Record(r.Context(), audit.Entry{
		ActorID:    principal.ActorID(),
		ActorName:  principal.ActorName(),
		ActorType:  principal.Kind,
		Action:     "token.create",
		RegistryID: tok.RegistryID,
		Target:     tok.Name,
		IPAddress:  clientIP(r),
		Success:    true,
		Details:    map[string]any{"scopes": tok.Scopes, "namespaces": tok.Namespaces},
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":     tok,
		"plaintext": plaintext, // Shown once!
	})
}

func (h *Handler) handleRevokeToken(w http.ResponseWriter, r *http.Request, principal *auth.Principal) {
	id := r.PathValue("id")
	if err := h.auth.RevokeToken(r.Context(), id); err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			writeError(w, http.StatusNotFound, "token not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Record(r.Context(), audit.Entry{
		ActorID:   principal.ActorID(),
		ActorName: principal.ActorName(),
		ActorType: principal.Kind,
		Action:    "token.revoke",
		Target:    id,
		IPAddress: clientIP(r),
		Success:   true,
	})
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
}

func (h *Handler) handleDeleteToken(w http.ResponseWriter, r *http.Request, principal *auth.Principal) {
	id := r.PathValue("id")
	if err := h.auth.DeleteToken(r.Context(), id); err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			writeError(w, http.StatusNotFound, "token not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Record(r.Context(), audit.Entry{
		ActorID:   principal.ActorID(),
		ActorName: principal.ActorName(),
		ActorType: principal.Kind,
		Action:    "token.delete",
		Target:    id,
		IPAddress: clientIP(r),
		Success:   true,
	})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// --- Users ---

func (h *Handler) handleListUsers(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	users, err := h.auth.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (h *Handler) handleCreateUser(w http.ResponseWriter, r *http.Request, principal *auth.Principal) {
	var body struct {
		Username string    `json:"username"`
		Email    string    `json:"email"`
		Password string    `json:"password"`
		Role     auth.Role `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	u, err := h.auth.CreateUser(r.Context(), auth.CreateUserInput{
		Username: body.Username,
		Email:    body.Email,
		Password: body.Password,
		Role:     body.Role,
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, auth.ErrAlreadyExists) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	h.audit.Record(r.Context(), audit.Entry{
		ActorID:   principal.ActorID(),
		ActorName: principal.ActorName(),
		ActorType: principal.Kind,
		Action:    "user.create",
		Target:    u.Username,
		IPAddress: clientIP(r),
		Success:   true,
		Details:   map[string]any{"role": u.Role},
	})
	writeJSON(w, http.StatusCreated, u)
}

func (h *Handler) handleUpdateUser(w http.ResponseWriter, r *http.Request, principal *auth.Principal) {
	id := r.PathValue("id")
	var body struct {
		Email    *string    `json:"email"`
		Role     *auth.Role `json:"role"`
		Disabled *bool      `json:"disabled"`
		Password *string    `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	u, err := h.auth.UpdateUser(r.Context(), id, auth.UpdateUserInput{
		Email:    body.Email,
		Role:     body.Role,
		Disabled: body.Disabled,
		Password: body.Password,
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, auth.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	h.audit.Record(r.Context(), audit.Entry{
		ActorID:   principal.ActorID(),
		ActorName: principal.ActorName(),
		ActorType: principal.Kind,
		Action:    "user.update",
		Target:    u.Username,
		IPAddress: clientIP(r),
		Success:   true,
	})
	writeJSON(w, http.StatusOK, u)
}

func (h *Handler) handleDeleteUser(w http.ResponseWriter, r *http.Request, principal *auth.Principal) {
	id := r.PathValue("id")
	if principal.User != nil && principal.User.ID == id {
		writeError(w, http.StatusBadRequest, "cannot delete your own active admin account")
		return
	}
	if err := h.auth.DeleteUser(r.Context(), id); err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Record(r.Context(), audit.Entry{
		ActorID:   principal.ActorID(),
		ActorName: principal.ActorName(),
		ActorType: principal.Kind,
		Action:    "user.delete",
		Target:    id,
		IPAddress: clientIP(r),
		Success:   true,
	})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// --- Unified Monitoring & Stats ---

func (h *Handler) handleStatsOverview(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	ctx := r.Context()
	globalStorage, err := h.storage.GetGlobalStats(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	instances, err := h.manager.ListInstances(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	ids := make([]string, 0, len(instances))
	for _, inst := range instances {
		ids = append(ids, inst.ID)
	}
	snapshots, err := h.metrics.GetAllSnapshots(ctx, ids)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var totalDownloads, totalUploads, totalServed, totalReceived, totalHits, totalMisses int64
	enriched := make([]EnrichedRegistry, 0, len(instances))
	for _, inst := range instances {
		snap := snapshots[inst.ID]
		totalDownloads += snap.Downloads
		totalUploads += snap.Uploads
		totalServed += snap.BytesServed
		totalReceived += snap.BytesReceived
		totalHits += snap.CacheHits
		totalMisses += snap.CacheMisses
		enriched = append(enriched, h.enrichRegistry(ctx, inst))
	}

	var globalHitRatio float64
	if totalHits+totalMisses > 0 {
		globalHitRatio = float64(totalHits) / float64(totalHits+totalMisses)
	}

	buckets, _ := h.metrics.ListRecentBuckets(ctx, "", 14)
	if buckets == nil {
		buckets = []*metrics.BucketRecord{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"storage":          globalStorage,
		"downloads":        totalDownloads,
		"uploads":          totalUploads,
		"bytes_served":     totalServed,
		"bytes_received":   totalReceived,
		"cache_hits":       totalHits,
		"cache_misses":     totalMisses,
		"cache_hit_ratio":  globalHitRatio,
		"active_transfers": h.metrics.ActiveTransfersGlobal(),
		"registry_count":   len(instances),
		"installed_types":  h.manager.ListTypes(),
		"registries":       enriched,
		"history":          buckets,
	})
}

func (h *Handler) handleRegistryStats(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	id := r.PathValue("id")
	inst, err := h.manager.GetInstance(r.Context(), id)
	if err != nil {
		if errors.Is(err, registry.ErrInstanceNotFound) {
			writeError(w, http.StatusNotFound, "registry not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 90 {
		days = 14
	}
	buckets, _ := h.metrics.ListRecentBuckets(r.Context(), id, days)
	if buckets == nil {
		buckets = []*metrics.BucketRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"registry": h.enrichRegistry(r.Context(), inst),
		"history":  buckets,
	})
}

// --- Audit Logs & Settings ---

func (h *Handler) handleAuditLogs(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	entries, total, err := h.audit.List(r.Context(), audit.Query{
		RegistryID: r.URL.Query().Get("registry_id"),
		Action:     r.URL.Query().Get("action"),
		ActorName:  r.URL.Query().Get("actor"),
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entries == nil {
		entries = []*audit.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"total":   total,
	})
}

func (h *Handler) getSettings(ctx context.Context) (*PlatformSettings, error) {
	if h.settingsStore != nil {
		st, err := h.settingsStore.GetSettings(ctx)
		if err == nil && st != nil {
			st.Normalize()
			return st, nil
		}
	}
	cp := h.CurrentSettings()
	cp.Normalize()
	return &cp, nil
}

// GetSettings exposes the current platform settings to other packages (e.g. the composition root
// wiring live base-config inheritance) without re-reading the store on every call.
func (h *Handler) GetSettings(ctx context.Context) (*PlatformSettings, error) {
	return h.getSettings(ctx)
}

func (h *Handler) handleGetSettings(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	st, err := h.getSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func validateOptionalHTTPURL(field, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New(field + " must be a valid http or https URL")
	}
	return nil
}

func (h *Handler) handleUpdateSettings(w http.ResponseWriter, r *http.Request, principal *auth.Principal) {
	var body PlatformSettings
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	if err := validateOptionalHTTPURL("public_base_url", body.PublicBaseURL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateOptionalHTTPURL("default_pub_upstream_url", body.DefaultPubUpstreamURL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateOptionalHTTPURL("default_flutter_storage_url", body.DefaultFlutterStorageURL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := upstream.ValidateProxyURL(body.UpstreamProxyURL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	body.Normalize()
	var flutterSpec *registry.BaseConfigSpec
	if reg, ok := h.manager.GetType(flutterBaseRegistryType); ok {
		flutterSpec = reg.BaseConfig
	}
	if err := body.NormalizeFlutterBase(flutterSpec); err != nil {
		writeError(w, http.StatusBadRequest, "default_flutter_storage_config: "+err.Error())
		return
	}
	body.UpdatedAt = time.Now().UTC()

	if h.upstream != nil {
		if err := h.upstream.ApplyConfig(body.ToUpstreamConfig()); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	if h.settingsStore != nil {
		if err := h.settingsStore.SaveSettings(r.Context(), &body); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	h.settingsMu.Lock()
	*h.defaultConfig = body
	h.settingsMu.Unlock()

	h.audit.Record(r.Context(), audit.Entry{
		ActorID:   principal.ActorID(),
		ActorName: principal.ActorName(),
		ActorType: principal.Kind,
		Action:    "settings.update",
		IPAddress: clientIP(r),
		Success:   true,
	})
	writeJSON(w, http.StatusOK, &body)
}

func (h *Handler) handleTestUpstream(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	var body struct {
		URL                string                   `json:"url"`
		RegistryID         string                   `json:"registry_id"`
		UpstreamConfig     *registry.UpstreamConfig `json:"upstream_config"`
		ProxyURL           string                   `json:"proxy_url"`
		InsecureSkipVerify bool                     `json:"insecure_skip_verify"`
		TimeoutSeconds     int                      `json:"timeout_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	targetURL := strings.TrimSpace(body.URL)
	if targetURL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	if err := validateOptionalHTTPURL("url", targetURL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	uc := body.UpstreamConfig.Clone()
	// If testing an existing registry without re-entering its saved auth secret, reuse stored secret safely.
	if body.RegistryID != "" {
		if existing, err := h.manager.GetInstance(r.Context(), body.RegistryID); err == nil && existing.UpstreamConfig != nil {
			if uc == nil {
				uc = existing.UpstreamConfig.Clone()
			} else if uc.AuthToken == "" {
				authType := strings.ToLower(strings.TrimSpace(uc.AuthType))
				if authType != "" && authType != "none" {
					uc.AuthToken = existing.UpstreamConfig.AuthToken
				}
			}
		}
	}
	if uc != nil {
		if err := uc.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	timeout := 10 * time.Second
	if body.TimeoutSeconds > 0 {
		timeout = time.Duration(body.TimeoutSeconds) * time.Second
	}
	opts := uc.BuildRequestOptions(nil, timeout)
	if strings.TrimSpace(body.ProxyURL) != "" {
		if _, err := upstream.ValidateProxyURL(body.ProxyURL); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		opts.ProxyURL = strings.TrimSpace(body.ProxyURL)
	}
	if body.InsecureSkipVerify {
		opts.InsecureSkipVerify = true
	}

	client := h.upstream
	if client == nil {
		client = upstream.NewClient(timeout)
	}
	res := client.CheckHealthWithOptions(r.Context(), targetURL, opts)
	writeJSON(w, http.StatusOK, res)
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{
		"error": msg,
	})
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	return r.RemoteAddr
}
