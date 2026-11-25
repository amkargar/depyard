package registry_test

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amkargar/mirror-registry/backend/internal/registry"
	memstore "github.com/amkargar/mirror-registry/backend/internal/store/memory"
)

// TestCoreNeverImportsConcreteRegistry scans every non-test Go source file in shared core packages
// and guarantees that NONE of them import internal/registry/pub.
func TestCoreNeverImportsConcreteRegistry(t *testing.T) {
	corePackages := []string{
		"internal/registry",
		"internal/storage",
		"internal/cache",
		"internal/upstream",
		"internal/auth",
		"internal/admin",
		"internal/metrics",
		"internal/audit",
		"internal/config",
		"internal/store",
		"internal/tasks",
		"internal/server",
	}

	backendRoot := filepath.Join("..", "..")
	fset := token.NewFileSet()

	for _, relPkg := range corePackages {
		dir := filepath.Join(backendRoot, relPkg)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			// Skip concrete registry module subdirectories inside internal/registry/
			if d.IsDir() && relPkg == "internal/registry" && path != dir {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
				return nil
			}
			fileAST, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if parseErr != nil {
				return parseErr
			}
			for _, imp := range fileAST.Imports {
				impPath := strings.Trim(imp.Path.Value, `"`)
				if strings.Contains(impPath, "/internal/registry/") {
					t.Errorf("core file %s illegally imports concrete registry implementation %s", path, impPath)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", relPkg, err)
		}
	}
}

// minimalTestRegistry is a test-only registry implementation proving that a new ecosystem
// only needs the minimal Registry interface (Type, Capabilities, Handler).
type minimalTestRegistry struct {
	slug string
}

func (m *minimalTestRegistry) Type() string { return "testpkg" }
func (m *minimalTestRegistry) Capabilities() registry.Capabilities {
	return registry.Capabilities{
		Proxy:            true,
		Hosted:           false, // Proxy-only test registry
		Publish:          false,
		ArtifactDeletion: false,
		CacheWarm:        false,
	}
}
func (m *minimalTestRegistry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "testpkg:%s:%s", m.slug, r.URL.Path)
	})
}

func testPkgRegistration() registry.Registration {
	return registry.Registration{
		Type:         "testpkg",
		DisplayName:  "Test-Only Minimal Registry",
		Description:  "Used exclusively in tests to verify multi-registry extensibility.",
		Capabilities: (&minimalTestRegistry{}).Capabilities(),
		ValidateConfig: func(cfg map[string]any) error {
			channel, ok := cfg["channel"].(string)
			if !ok || (channel != "stable" && channel != "beta") {
				return fmt.Errorf("channel must be 'stable' or 'beta'")
			}
			return nil
		},
		Create: func(_ context.Context, inst *registry.Instance, _ registry.ServiceDeps) (registry.Registry, error) {
			return &minimalTestRegistry{slug: inst.Slug}, nil
		},
	}
}

func TestIndependentRegistrationModeAndConfigValidation(t *testing.T) {
	ctx := context.Background()
	mgr := registry.NewManager(memstore.New(), registry.ServiceDeps{})

	// 1. Register minimal test-only registry type independently of Pub
	if err := mgr.RegisterType(testPkgRegistration()); err != nil {
		t.Fatalf("RegisterType: %v", err)
	}

	types := mgr.ListTypes()
	if len(types) != 1 || types[0].Type != "testpkg" {
		t.Fatalf("expected only testpkg registered, got %+v", types)
	}
	if len(types[0].SupportedModes) != 1 || types[0].SupportedModes[0] != registry.ModeProxy {
		t.Fatalf("expected only proxy mode supported, got %+v", types[0].SupportedModes)
	}

	// 2. Uninstalled registry type MUST be rejected
	_, err := mgr.CreateInstance(ctx, &registry.Instance{
		Name: "Uninstalled RPM",
		Slug: "rpm-rocky",
		Type: "rpm",
		Mode: registry.ModeProxy,
		UpstreamURL: "https://example.com",
	})
	if !errors.Is(err, registry.ErrTypeNotRegistered) {
		t.Fatalf("expected ErrTypeNotRegistered for uninstalled type, got %v", err)
	}

	// 3. Unsupported repository mode (hosted/hybrid on proxy-only registry) MUST be rejected
	_, err = mgr.CreateInstance(ctx, &registry.Instance{
		Name:   "Invalid Hosted",
		Slug:   "test-hosted",
		Type:   "testpkg",
		Mode:   registry.ModeHosted,
		Config: map[string]any{"channel": "stable"},
	})
	if !errors.Is(err, registry.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for unsupported mode, got %v", err)
	}

	// 4. Registry-specific config validation MUST reject bad config
	_, err = mgr.CreateInstance(ctx, &registry.Instance{
		Name:        "Bad Config",
		Slug:        "test-bad",
		Type:        "testpkg",
		Mode:        registry.ModeProxy,
		UpstreamURL: "https://upstream.example.com",
		Config:      map[string]any{"channel": "nightly"},
	})
	if !errors.Is(err, registry.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for invalid registry config, got %v", err)
	}

	// 5. Valid instance activates immediately without restart
	inst, err := mgr.CreateInstance(ctx, &registry.Instance{
		Name:        "Test Stable Mirror",
		Slug:        "test-stable",
		Type:        "testpkg",
		Mode:        registry.ModeProxy,
		Enabled:     true,
		UpstreamURL: "https://upstream.example.com",
		Visibility:  registry.VisibilityPublic,
		Config:      map[string]any{"channel": "stable"},
	})
	if err != nil {
		t.Fatalf("CreateInstance valid: %v", err)
	}
	if _, ok := mgr.GetRuntimeBySlug("test-stable"); !ok {
		t.Fatalf("expected test-stable runtime active immediately")
	}

	// 6. Toggle enabled live & delete cleanly
	toggled, err := mgr.SetEnabled(ctx, inst.ID, false)
	if err != nil || toggled.Enabled {
		t.Fatalf("SetEnabled(false) failed: %v", err)
	}
	if err := mgr.DeleteInstance(ctx, inst.ID); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if _, ok := mgr.GetRuntimeBySlug("test-stable"); ok {
		t.Fatalf("expected runtime removed after delete")
	}
}
