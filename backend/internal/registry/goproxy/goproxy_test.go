package goproxy_test

import (
	"testing"

	"github.com/amkargar/mirror-registry/backend/internal/registry/goproxy"
)

func TestGoVersionOrdering(t *testing.T) {
	versions := []string{
		"v1.10.0",
		"v1.2.0",
		"v1.0.0",
		"v2.0.0-rc.1",
		"v2.0.0",
		"v0.0.0-20210101000000-abcdef123456",
		"v1.11.0-beta.1",
	}
	goproxy.SortVersionStrings(versions)

	want := []string{
		"v0.0.0-20210101000000-abcdef123456",
		"v1.0.0",
		"v1.2.0",
		"v1.10.0",
		"v1.11.0-beta.1",
		"v2.0.0-rc.1",
		"v2.0.0",
	}
	if len(versions) != len(want) {
		t.Fatalf("unexpected length: %v", versions)
	}
	for i := range want {
		if versions[i] != want[i] {
			t.Fatalf("index %d: expected %s, got %s (full: %v)", i, want[i], versions[i], versions)
		}
	}
}

func TestGoModulePathValidation(t *testing.T) {
	valid := []string{
		"github.com/pkg/errors",
		"golang.org/x/text",
		"gopkg.in/yaml.v3",
		"example.com/foo-bar_baz",
		"rsc.io/quote",
	}
	for _, p := range valid {
		if !goproxy.IsValidModulePath(p) {
			t.Fatalf("expected %q to be a valid module path", p)
		}
	}

	invalid := []string{
		"",
		"/leading",
		"trailing/",
		"double//slash",
		"github.com/Pkg/Errors", // uppercase is invalid in module paths
		"github.com/pkg/../../etc",
		"github.com/pkg?query=1",
		"github.com/pkg#frag",
	}
	for _, p := range invalid {
		if goproxy.IsValidModulePath(p) {
			t.Fatalf("expected %q to be rejected", p)
		}
	}
}

func TestGoModulePathEscaping(t *testing.T) {
	// Uppercase letters must be encoded as "!" plus the lowercase letter.
	if got := goproxy.EscapeModulePath("github.com/Sirupsen/logrus"); got != "github.com/!sirupsen/logrus" {
		t.Fatalf("unexpected escaping: %q", got)
	}
	if got := goproxy.EscapeModulePath("golang.org/x/text"); got != "golang.org/x/text" {
		t.Fatalf("unexpected escaping: %q", got)
	}
	// "+" is a legal module path character and must not be escaped to "%2B"
	// by the "!x" scheme; it stays literal in the path segment.
	if got := goproxy.EscapeModulePath("example.com/a+b"); got == "" {
		t.Fatalf("expected non-empty escaping")
	}
}

func TestGoConfigValidation(t *testing.T) {
	// Unknown fields must be rejected.
	if _, err := goproxy.ParseConfig(map[string]any{"npm_registry": "nope"}); err == nil {
		t.Fatalf("expected unknown goproxy config key to fail validation")
	}
	// Out-of-range timeout must be rejected.
	if _, err := goproxy.ParseConfig(map[string]any{"upstream_timeout_seconds": 9999}); err == nil {
		t.Fatalf("expected excessive timeout to fail validation")
	}
	// Valid config is accepted.
	cfg, err := goproxy.ParseConfig(map[string]any{
		"upstream_timeout_seconds": 15,
		"blocked_modules":          []string{"github.com/private/*"},
		"include_prerelease":       false,
	})
	if err != nil {
		t.Fatalf("valid goproxy config failed: %v", err)
	}
	if cfg.UpstreamTimeoutSeconds != 15 {
		t.Fatalf("unexpected timeout: %d", cfg.UpstreamTimeoutSeconds)
	}
	if len(cfg.BlockedModules) != 1 {
		t.Fatalf("unexpected blocked modules: %+v", cfg.BlockedModules)
	}
	if cfg.IncludePrerelease {
		t.Fatalf("expected include_prerelease to be false")
	}
}

func TestGoCapabilities(t *testing.T) {
	caps := goproxy.GoCapabilities()
	if !caps.Proxy {
		t.Fatalf("expected the Go proxy to support proxy mode")
	}
	if caps.Hosted || caps.Publish {
		t.Fatalf("the GOPROXY protocol has no publish operation, so hosted/publish must be false: %+v", caps)
	}
	if !caps.ListVersions || !caps.CacheWarm {
		t.Fatalf("expected list_versions and cache_warm support: %+v", caps)
	}
}

func TestGoModulePatternMatching(t *testing.T) {
	cases := []struct {
		pattern string
		module  string
		want    bool
	}{
		{"*", "anything.example.com/x", true},
		{"github.com/pkg/errors", "github.com/pkg/errors", true},
		{"github.com/pkg/errors", "github.com/other/errors", false},
		{"github.com/*", "github.com/pkg/errors", true},
		{"github.com/*", "golang.org/x/text", false},
		{"github.com/...", "github.com/pkg/errors", true},
		{"github.com/...", "github.com", true},
		{"github.com/...", "golang.org/x/text", false},
	}
	for _, c := range cases {
		if got := goproxy.MatchModulePattern(c.pattern, c.module); got != c.want {
			t.Fatalf("pattern %q vs %q: expected %v, got %v", c.pattern, c.module, c.want, got)
		}
	}
}
