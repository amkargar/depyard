package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/amkargar/depyard/backend/internal/auth"
	"github.com/amkargar/depyard/backend/internal/config"
)

func TestAdminPasswordHasNoPredictableDefault(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD", "")

	cfg := config.Load()
	if cfg.AdminPassword != "" {
		t.Fatalf("expected no default admin password, got %q", cfg.AdminPassword)
	}
	if strings.Contains(strings.ToLower(cfg.AdminPassword), "admin") {
		t.Fatalf("the default admin password must not be derived from a predictable word, got %q", cfg.AdminPassword)
	}
}

func TestAdminPasswordIsReadFromTheEnvironment(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD", "an-explicitly-configured-password")

	cfg := config.Load()
	if cfg.AdminPassword != "an-explicitly-configured-password" {
		t.Fatalf("expected the configured password, got %q", cfg.AdminPassword)
	}
}

func TestNpmSeedingIsConfigurable(t *testing.T) {
	t.Setenv("SEED_NPM", "")
	t.Setenv("DEFAULT_NPM_UPSTREAM_URL", "")
	cfg := config.Load()
	if !cfg.SeedNPM || cfg.DefaultNPMSlug != "npm-packages" || cfg.DefaultNPMUpstreamURL != "https://registry.npmjs.org" {
		t.Fatalf("unexpected npm defaults: %+v", cfg)
	}

	t.Setenv("SEED_NPM", "false")
	t.Setenv("DEFAULT_NPM_SLUG", "internal-npm")
	cfg = config.Load()
	if cfg.SeedNPM || cfg.DefaultNPMSlug != "internal-npm" {
		t.Fatalf("npm seed settings were not read from the environment: %+v", cfg)
	}
}

func TestMinPasswordLengthDefaultsToTwelveAndIsConfigurable(t *testing.T) {
	t.Setenv("MIN_PASSWORD_LENGTH", "")

	cfg := config.Load()
	if cfg.MinPasswordLength != auth.DefaultMinPasswordLength {
		t.Fatalf("expected the default minimum length of %d, got %d", auth.DefaultMinPasswordLength, cfg.MinPasswordLength)
	}
	if cfg.MinPasswordLength < 12 {
		t.Fatalf("the password policy floor must be at least 12 characters, got %d", cfg.MinPasswordLength)
	}

	t.Setenv("MIN_PASSWORD_LENGTH", "8")
	cfg = config.Load()
	if cfg.MinPasswordLength != 8 {
		t.Fatalf("expected the configured minimum length of 8, got %d", cfg.MinPasswordLength)
	}

	// A nonsense value falls back to the safe default.
	t.Setenv("MIN_PASSWORD_LENGTH", "not-a-number")
	cfg = config.Load()
	if cfg.MinPasswordLength != auth.DefaultMinPasswordLength {
		t.Fatalf("expected the safe default, got %d", cfg.MinPasswordLength)
	}
	_ = os.Getenv("MIN_PASSWORD_LENGTH")
}

// TestShippedDeploymentFilesHaveNoWeakAdminPassword guards the deployment
// manifests, not just the Go config.
//
// The Docker Compose files used to pin ADMIN_PASSWORD to a well-known value.
// When the password policy was tightened that value fell below the minimum
// length, so the stock `docker compose up` aborted at startup — a failure that
// no unit test on config.Load() could see, because the value came from the
// manifest. Assert the manifests neither pin a known default nor pin anything
// the password policy would reject.
func TestShippedDeploymentFilesHaveNoWeakAdminPassword(t *testing.T) {
	manifests := []string{
		"../../../docker-compose.yml",
		"../../../docker-compose.release.yml",
	}

	knownDefaults := []string{"changeme", "admin123", "password123", "secret123"}
	minLen := auth.DefaultMinPasswordLength

	for _, path := range manifests {
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				t.Logf("skipping %s: not present in this checkout", path)
				continue
			}
			t.Fatalf("reading %s: %v", path, err)
		}
		content := string(raw)

		lower := strings.ToLower(content)
		for _, weak := range knownDefaults {
			if strings.Contains(lower, weak) {
				t.Errorf("%s advertises or pins the known default password %q", path, weak)
			}
		}

		for _, line := range strings.Split(content, "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "ADMIN_PASSWORD:") {
				continue
			}
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "ADMIN_PASSWORD:"))
			// ${ADMIN_PASSWORD:-} defers to the environment and falls back to
			// generating a password: that is the behaviour we want.
			if value == "" || strings.HasPrefix(value, "${") {
				continue
			}
			if len(value) < minLen {
				t.Errorf("%s pins ADMIN_PASSWORD to %d characters, below the minimum of %d",
					path, len(value), minLen)
			}
		}
	}
}
