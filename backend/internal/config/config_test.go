package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/amkargar/mirror-registry/backend/internal/auth"
	"github.com/amkargar/mirror-registry/backend/internal/config"
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
