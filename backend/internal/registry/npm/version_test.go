package npm_test

import (
	"testing"

	"github.com/amkargar/depyard/backend/internal/registry/npm"
)

func TestSemverParsingAndOrdering(t *testing.T) {
	valid := []string{"0.0.1", "1.2.3", "1.2.3-alpha.1", "2.0.0+build.5", "10.20.30-rc.0+meta"}
	for _, raw := range valid {
		if _, err := npm.ParseVersion(raw); err != nil {
			t.Errorf("expected %q to be valid semver: %v", raw, err)
		}
	}
	for _, raw := range []string{"1.2", "v1.2.3", "01.2.3", "1.2.3-", "latest", ""} {
		if _, err := npm.ParseVersion(raw); err == nil {
			t.Errorf("expected %q to be rejected", raw)
		}
	}

	cases := [][2]string{
		{"1.0.0-alpha", "1.0.0"},
		{"1.0.0-alpha", "1.0.0-alpha.1"},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta"},
		{"1.0.0-beta.2", "1.0.0-beta.11"},
		{"1.0.0", "1.0.1"},
		{"1.9.0", "2.0.0"},
	}
	for _, c := range cases {
		if got := npm.CompareVersions(c[0], c[1]); got >= 0 {
			t.Errorf("expected %q < %q, got %d", c[0], c[1], got)
		}
		if got := npm.CompareVersions(c[1], c[0]); got <= 0 {
			t.Errorf("expected %q > %q, got %d", c[1], c[0], got)
		}
	}

	versions := []string{"1.0.0", "2.0.0-beta.1", "1.5.0", "2.0.0"}
	if got := npm.LatestVersion(versions, false); got != "2.0.0" {
		t.Fatalf("expected stable latest 2.0.0, got %q", got)
	}
	if got := npm.LatestVersion(versions, true); got != "2.0.0" {
		t.Fatalf("expected latest 2.0.0, got %q", got)
	}
	prereleaseOnly := []string{"1.0.0", "2.0.0-beta.1"}
	if got := npm.LatestVersion(prereleaseOnly, false); got != "1.0.0" {
		t.Fatalf("expected pre-release to be excluded from latest, got %q", got)
	}
}

func TestPackageNameAndPathParsing(t *testing.T) {
	for _, name := range []string{"lodash", "@acme/tools", "some.package_name"} {
		if !npm.IsValidPackageName(name) {
			t.Errorf("expected %q to be a valid npm name", name)
		}
	}
	for _, name := range []string{".hidden", "with space", "../escape", "@scope/", "", "A/B/c"} {
		if npm.IsValidPackageName(name) {
			t.Errorf("expected %q to be rejected", name)
		}
	}

	name, rest, err := npm.ParseRequestPath("/@acme%2Ftools/-/tools-1.0.0.tgz")
	if err != nil || name != "@acme/tools" || rest != "/-/tools-1.0.0.tgz" {
		t.Fatalf("escaped scoped path: name=%q rest=%q err=%v", name, rest, err)
	}
	name, rest, err = npm.ParseRequestPath("/@acme/tools")
	if err != nil || name != "@acme/tools" || rest != "" {
		t.Fatalf("unescaped scoped path: name=%q rest=%q err=%v", name, rest, err)
	}
	if _, _, err := npm.ParseRequestPath("/../secret"); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}

func TestConfigDefaultsAndValidation(t *testing.T) {
	cfg, err := npm.ParseConfig(nil)
	if err != nil {
		t.Fatalf("ParseConfig(nil): %v", err)
	}
	if cfg.UpstreamTimeoutSeconds != 60 || cfg.MaxTarballSizeBytes <= 0 || !cfg.VerifyIntegrity {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if _, err := npm.ParseConfig(map[string]any{"blocked_packages": []string{"evil-*"}}); err != nil {
		t.Fatalf("valid pattern config rejected: %v", err)
	}
}
