package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/amkargar/depyard/backend/internal/audit"
	"github.com/amkargar/depyard/backend/internal/auth"
	"github.com/amkargar/depyard/backend/internal/registry"
)

// Apply modes for pushing base storage settings into existing registries.
const (
	FlutterApplyFillMissing = "fill-missing"
	FlutterApplyOverwrite   = "overwrite"
)

// BaseConfigApplyRequest is the body of POST /api/admin/settings/base-config/{type}/apply.
type BaseConfigApplyRequest struct {
	// RegistryIDs limits the apply to specific instances. Empty means "every registry of this type".
	RegistryIDs []string `json:"registry_ids"`
	// Fields limits which base fields are pushed. Empty means "every field present in the base".
	Fields []string `json:"fields"`
	// Mode is "fill-missing" (default) or "overwrite".
	Mode string `json:"mode"`
	// DryRun reports the changes without persisting anything.
	DryRun bool `json:"dry_run"`
}

// FieldChange describes one field modified by an apply.
type FieldChange struct {
	Key  string `json:"key"`
	From any    `json:"from,omitempty"`
	To   any    `json:"to"`
}

// RegistryBaseImpact is the per-registry preview/result of a base-config apply.
type RegistryBaseImpact struct {
	ID      string        `json:"id"`
	Slug    string        `json:"slug"`
	Name    string        `json:"name"`
	Changes []FieldChange `json:"changes"`
	Skipped []string      `json:"skipped,omitempty"`
	Applied bool          `json:"applied"`
	Error   string        `json:"error,omitempty"`
	// FieldSources is the ecosystem-defined per-field resolution report (see BaseConfigSpec.Diff).
	FieldSources any `json:"field_sources,omitempty"`
}

// BaseConfigImpact is the response for both the impact preview and the apply endpoint.
type BaseConfigImpact struct {
	Type       string               `json:"type"`
	Mode       string               `json:"mode"`
	DryRun     bool                 `json:"dry_run"`
	Registries []RegistryBaseImpact `json:"registries"`
	Applied    int                  `json:"applied"`
	Failed     int                  `json:"failed"`
}

// baseConfigSpec resolves the BaseConfigSpec for a registry type.
func (h *Handler) baseConfigSpec(registryType string) (*registry.BaseConfigSpec, bool) {
	reg, ok := h.manager.GetType(registryType)
	if !ok || reg.BaseConfig == nil {
		return nil, false
	}
	return reg.BaseConfig, true
}

// baseConfigSpecs reports which registry types support platform-wide base settings.
func (h *Handler) baseConfigSpecs() map[string]*registry.BaseConfigSpec {
	out := map[string]*registry.BaseConfigSpec{}
	for _, info := range h.manager.ListTypes() {
		if reg, ok := h.manager.GetType(info.Type); ok && reg.BaseConfig != nil {
			out[info.Type] = reg.BaseConfig
		}
	}
	return out
}

// registriesOfType returns every registry instance of a type (optionally filtered by ID or slug).
func (h *Handler) registriesOfType(ctx context.Context, registryType string, ids []string) ([]*registry.Instance, error) {
	instances, err := h.manager.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[strings.TrimSpace(id)] = true
	}
	out := make([]*registry.Instance, 0, len(instances))
	for _, inst := range instances {
		if inst.Type != registryType {
			continue
		}
		if len(want) > 0 && !want[inst.ID] && !want[inst.Slug] {
			continue
		}
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

// handleBaseConfigImpact reports, per registry, which fields would change if the current base
// settings were pushed. It never writes.
func (h *Handler) handleBaseConfigImpact(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	registryType := strings.ToLower(strings.TrimSpace(r.PathValue("type")))
	spec, ok := h.baseConfigSpec(registryType)
	if !ok {
		writeError(w, http.StatusNotFound, "registry type does not support base settings: "+registryType)
		return
	}
	base, ok := h.baseConfigFor(w, r, registryType)
	if !ok {
		return
	}
	instances, err := h.registriesOfType(r.Context(), registryType, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	res := BaseConfigImpact{Type: registryType, Mode: FlutterApplyFillMissing, DryRun: true, Registries: make([]RegistryBaseImpact, 0, len(instances))}
	for _, inst := range instances {
		impact := RegistryBaseImpact{ID: inst.ID, Slug: inst.Slug, Name: inst.Name}
		impact.Changes = pendingChanges(spec, base, inst.Config, nil, FlutterApplyFillMissing)
		if spec.Diff != nil {
			impact.FieldSources = spec.Diff(base, inst.Config)
		}
		res.Registries = append(res.Registries, impact)
	}
	writeJSON(w, http.StatusOK, res)
}

// handleBaseConfigApply pushes the base settings into existing registries.
func (h *Handler) handleBaseConfigApply(w http.ResponseWriter, r *http.Request, principal *auth.Principal) {
	registryType := strings.ToLower(strings.TrimSpace(r.PathValue("type")))
	spec, ok := h.baseConfigSpec(registryType)
	if !ok {
		writeError(w, http.StatusNotFound, "registry type does not support base settings: "+registryType)
		return
	}
	var body BaseConfigApplyRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON payload")
			return
		}
	}
	mode := strings.ToLower(strings.TrimSpace(body.Mode))
	if mode == "" {
		mode = FlutterApplyFillMissing
	}
	if mode != FlutterApplyFillMissing && mode != FlutterApplyOverwrite {
		writeError(w, http.StatusBadRequest, `mode must be "fill-missing" or "overwrite"`)
		return
	}
	if err := validateApplyFields(spec, body.Fields); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	base, ok := h.baseConfigFor(w, r, registryType)
	if !ok {
		return
	}
	instances, err := h.registriesOfType(r.Context(), registryType, body.RegistryIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	res := BaseConfigImpact{Type: registryType, Mode: mode, DryRun: body.DryRun, Registries: make([]RegistryBaseImpact, 0, len(instances))}
	for _, inst := range instances {
		impact := RegistryBaseImpact{ID: inst.ID, Slug: inst.Slug, Name: inst.Name}
		changes := pendingChanges(spec, base, inst.Config, body.Fields, mode)
		impact.Changes = changes

		report := func() {
			if spec.Diff != nil {
				impact.FieldSources = spec.Diff(base, inst.Config)
			}
		}
		if len(changes) == 0 || body.DryRun {
			report()
			res.Registries = append(res.Registries, impact)
			continue
		}

		next := make(map[string]any, len(inst.Config))
		for k, v := range inst.Config {
			next[k] = v
		}
		for _, c := range changes {
			next[c.Key] = c.To
		}
		// Re-validate the merged result before persisting so an incompatible base config can never
		// corrupt a live registry.
		if spec.Merge != nil {
			merged := spec.Merge(base, next)
			if err := h.manager.ValidateTypeConfig(registryType, merged); err != nil {
				impact.Error = err.Error()
				res.Failed++
				res.Registries = append(res.Registries, impact)
				continue
			}
		}

		patch := inst.Clone()
		patch.Config = next
		updated, err := h.manager.UpdateInstance(r.Context(), inst.ID, patch)
		if err != nil {
			impact.Error = err.Error()
			res.Failed++
			res.Registries = append(res.Registries, impact)
			continue
		}
		impact.Applied = true
		if spec.Diff != nil {
			impact.FieldSources = spec.Diff(base, updated.Config)
		}
		res.Applied++
		res.Registries = append(res.Registries, impact)
	}

	if !body.DryRun {
		h.audit.Record(r.Context(), audit.Entry{
			ActorID:    principal.ActorID(),
			ActorName:  principal.ActorName(),
			ActorType:  principal.Kind,
			Action:     "settings.base_config.apply",
			RegistryID: "",
			Target:     registryType + ":" + mode,
			IPAddress:  clientIP(r),
			Success:    res.Failed == 0,
		})
	}
	writeJSON(w, http.StatusOK, res)
}

// baseConfigFor loads the current platform settings and returns the base config for a type.
func (h *Handler) baseConfigFor(w http.ResponseWriter, r *http.Request, registryType string) (map[string]any, bool) {
	st, err := h.getSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	var base map[string]any
	if strings.EqualFold(registryType, flutterBaseRegistryType) {
		base = st.SeedConfig()
	} else {
		base = st.BaseConfigMap(registryType)
	}
	// Split off base-only keys (e.g. the default upstream URL) so apply never tries to write them
	// into an instance config, where the ecosystem parser would reject them.
	if spec, ok := h.baseConfigSpec(registryType); ok {
		base = dropBaseOnlyKeys(spec, base)
	}
	if len(base) == 0 {
		writeError(w, http.StatusBadRequest, "no base settings are configured for registry type "+registryType)
		return nil, false
	}
	return base, true
}

// pendingChanges computes the field writes needed to bring instCfg in line with base.
func pendingChanges(spec *registry.BaseConfigSpec, base, instCfg map[string]any, fields []string, mode string) []FieldChange {
	allowed := make(map[string]bool, len(fields))
	for _, f := range fields {
		allowed[strings.TrimSpace(f)] = true
	}
	wants := func(k string) bool { return len(allowed) == 0 || allowed[k] }

	instCfg = normalizeForCompare(instCfg)
	changes := make([]FieldChange, 0, len(base))

	for _, key := range spec.Keys {
		if !wants(key) {
			continue
		}
		baseVal, ok := base[key]
		if !ok {
			continue
		}
		curVal, present := instCfg[key]

		switch mode {
		case FlutterApplyFillMissing:
			if present {
				if key == "download_storage" {
					if merged, changed := mergeCategories(spec, baseVal, curVal); changed {
						changes = append(changes, FieldChange{Key: key, From: curVal, To: merged})
					}
				}
				continue
			}
			changes = append(changes, FieldChange{Key: key, To: baseVal})
		case FlutterApplyOverwrite:
			if key == "download_storage" {
				if merged, changed := mergeCategories(spec, baseVal, curVal); changed {
					changes = append(changes, FieldChange{Key: key, From: curVal, To: merged})
				}
				continue
			}
			if present && equalJSON(curVal, baseVal) {
				continue
			}
			changes = append(changes, FieldChange{Key: key, From: curVal, To: baseVal})
		}
	}
	return changes
}

// mergeCategories merges base categories under the instance list via the ecosystem Merge hook and
// reports whether the result differs from the instance list.
func mergeCategories(spec *registry.BaseConfigSpec, baseVal, curVal any) (any, bool) {
	if spec.Merge == nil {
		return curVal, false
	}
	merged := spec.Merge(
		map[string]any{"download_storage": baseVal},
		map[string]any{"download_storage": curVal},
	)
	next := merged["download_storage"]
	if equalJSON(next, curVal) {
		return curVal, false
	}
	return next, true
}

func normalizeForCompare(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		out[k] = v
	}
	return out
}

func equalJSON(a, b any) bool {
	aj, errA := json.Marshal(a)
	bj, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	var aa, bb any
	if err := json.Unmarshal(aj, &aa); err != nil {
		return false
	}
	if err := json.Unmarshal(bj, &bb); err != nil {
		return false
	}
	return jsonEqualDeep(aa, bb)
}

func jsonEqualDeep(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			bvv, ok := bv[k]
			if !ok || !jsonEqualDeep(v, bvv) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsonEqualDeep(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

func validateApplyFields(spec *registry.BaseConfigSpec, fields []string) error {
	if len(fields) == 0 {
		return nil
	}
	known := make(map[string]bool, len(spec.Keys))
	for _, k := range spec.Keys {
		known[k] = true
	}
	for _, f := range fields {
		if !known[strings.TrimSpace(f)] {
			return &unknownFieldError{field: f}
		}
	}
	return nil
}

type unknownFieldError struct{ field string }

func (e *unknownFieldError) Error() string {
	return "unknown config field \"" + e.field + "\""
}

// baseConfigSummary describes one registry type's platform-wide base settings support.
type baseConfigSummary struct {
	Type    string   `json:"type"`
	Keys    []string `json:"keys"`
	Enabled bool     `json:"enabled"`
}

// handleListBaseConfigs reports which installed registry types support base settings.
func (h *Handler) handleListBaseConfigs(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	specs := h.baseConfigSpecs()
	out := make([]baseConfigSummary, 0, len(specs))
	types := make([]string, 0, len(specs))
	for t := range specs {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		out = append(out, baseConfigSummary{Type: t, Keys: specs[t].Keys, Enabled: true})
	}
	writeJSON(w, http.StatusOK, map[string]any{"types": out})
}

// dropBaseOnlyKeys removes keys from a base config that are not part of the instance config schema.
// Keys listed in BaseConfigSpec.Keys are instance-config fields; anything else is base-only unless
// the ecosystem explicitly allows it.
func dropBaseOnlyKeys(spec *registry.BaseConfigSpec, base map[string]any) map[string]any {
	if len(base) == 0 || spec == nil {
		return base
	}
	allowed := make(map[string]bool, len(spec.Keys))
	for _, k := range spec.Keys {
		allowed[k] = true
	}
	out := make(map[string]any, len(base))
	for k, v := range base {
		if allowed[k] {
			out[k] = v
		}
	}
	return out
}
