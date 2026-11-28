package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/amkargar/depyard/backend/internal/config"
	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/registry/flutter"
	"github.com/amkargar/depyard/backend/internal/registry/goproxy"
	"github.com/amkargar/depyard/backend/internal/registry/npm"
	"github.com/amkargar/depyard/backend/internal/registry/pub"
	"github.com/amkargar/depyard/backend/internal/registry/pypi"
	memstore "github.com/amkargar/depyard/backend/internal/store/memory"
)

// stubRegistry keeps the seeding test independent of protocol implementations.
type stubRegistry struct{ typeName string }

func (s *stubRegistry) Type() string { return s.typeName }
func (s *stubRegistry) Capabilities() registry.Capabilities {
	return registry.Capabilities{Proxy: true, Hosted: true}
}
func (s *stubRegistry) Handler() http.Handler { return http.NewServeMux() }

func seedTestManager(t *testing.T) (*registry.Manager, config.Config) {
	t.Helper()
	for _, key := range []string{
		"SEED_DEFAULT_REGISTRY", "SEED_GO_PROXY", "SEED_PYPI", "SEED_NPM", "SEED_FLUTTER_STORAGE",
	} {
		t.Setenv(key, "true")
	}
	t.Setenv("SEED_DEMO_PACKAGES", "false")
	t.Setenv("DEFAULT_REGISTRY_TYPE", pub.RegistryType)

	mgr := registry.NewManager(memstore.New(), registry.ServiceDeps{})
	caps := registry.Capabilities{Proxy: true, Hosted: true}
	for _, typeName := range []string{
		pub.RegistryType, flutter.RegistryType, goproxy.RegistryType, pypi.RegistryType, npm.RegistryType,
	} {
		typeName := typeName
		if err := mgr.RegisterType(registry.Registration{
			Type:         typeName,
			Capabilities: caps,
			Create: func(_ context.Context, inst *registry.Instance, _ registry.ServiceDeps) (registry.Registry, error) {
				return &stubRegistry{typeName: typeName}, nil
			},
		}); err != nil {
			t.Fatalf("RegisterType(%s): %v", typeName, err)
		}
	}
	for _, reg := range objectFormats() {
		if err := mgr.RegisterType(reg); err != nil {
			t.Fatal(err)
		}
	}
	return mgr, config.Load()
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestSeedDefaultInstancesAreAddedButDisabled verifies the first-install
// catalogue: every known registry is persisted, none accepts traffic yet.
func TestSeedDefaultInstancesAreAddedButDisabled(t *testing.T) {
	ctx := context.Background()
	mgr, cfg := seedTestManager(t)

	seedDefaultInstances(ctx, discardLogger(), cfg, mgr)

	instances, err := mgr.ListInstances(ctx)
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(instances) != 23 {
		t.Fatalf("expected all 23 default registries to be added, got %d", len(instances))
	}
	wantTypes := map[string]bool{
		pub.RegistryType: false, flutter.RegistryType: false, goproxy.RegistryType: false,
		pypi.RegistryType: false, npm.RegistryType: false,
	}
	for _, t := range []string{"alpine", "cargo", "composer", "conan", "conda", "cran", "debian", "oci", "gitlfs", "helm", "huggingface", "ivy", "maven", "nix", "nuget", "pacman", "rpm", "rubygems"} {
		wantTypes[t] = false
	}
	for _, inst := range instances {
		if inst.Enabled {
			t.Errorf("default instance %q must be added disabled", inst.Slug)
		}
		if !inst.IsDefault && (inst.Type == "pub" || inst.Type == "goproxy" || inst.Type == "flutter" || inst.Type == "pypi" || inst.Type == "npm" || inst.Type == "oci") {
			t.Errorf("default instance %q should remain eligible for compat routing", inst.Slug)
		}
		if _, known := wantTypes[inst.Type]; !known {
			t.Errorf("unexpected seeded type %q", inst.Type)
		}
		wantTypes[inst.Type] = true
	}
	for typeName, seen := range wantTypes {
		if !seen {
			t.Errorf("missing default instance of type %q", typeName)
		}
	}
}

// TestSeedDefaultInstancesDoesNotTouchExistingInstallations makes sure a
// restart or an administrator-created instance is never disabled by seeding.
func TestSeedDefaultInstancesDoesNotTouchExistingInstallations(t *testing.T) {
	ctx := context.Background()
	mgr, cfg := seedTestManager(t)
	created, err := mgr.CreateInstance(ctx, &registry.Instance{
		Name: "Existing", Slug: "existing", Type: pub.RegistryType, Enabled: true,
		Mode: registry.ModeHosted, Visibility: registry.VisibilityPublic,
	})
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	seedDefaultInstances(ctx, discardLogger(), cfg, mgr)

	instances, err := mgr.ListInstances(ctx)
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(instances) != 1 || instances[0].ID != created.ID || !instances[0].Enabled {
		t.Fatalf("existing installation was modified by seeding: %+v", instances)
	}
}

// TestSeedDefaultInstancesRespectsSeedFlags keeps the opt-out switches working.
func TestSeedDefaultInstancesRespectsSeedFlags(t *testing.T) {
	ctx := context.Background()
	mgr, _ := seedTestManager(t)
	t.Setenv("SEED_NPM", "false")
	cfg := config.Load()

	seedDefaultInstances(ctx, discardLogger(), cfg, mgr)

	instances, _ := mgr.ListInstances(ctx)
	if len(instances) != 22 {
		t.Fatalf("expected 22 seeded instances with SEED_NPM=false, got %d", len(instances))
	}
	for _, inst := range instances {
		if inst.Type == npm.RegistryType {
			t.Fatal("npm instance should not be seeded when SEED_NPM=false")
		}
	}
}
