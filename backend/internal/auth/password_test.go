package auth_test

import (
	"strings"
	"testing"

	"github.com/amkargar/mirror-registry/backend/internal/auth"
)

func TestGeneratePasswordIsRandomAndLongEnough(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 25; i++ {
		pw := auth.GeneratePassword(24)
		if len(pw) < 20 {
			t.Fatalf("expected a password of at least 20 characters, got %d", len(pw))
		}
		if strings.Contains(pw, " ") || strings.Contains(pw, "/") || strings.Contains(pw, "+") {
			t.Fatalf("expected a URL-safe password without padding characters, got %q", pw)
		}
		if seen[pw] {
			t.Fatalf("expected a unique password, got %q twice", pw)
		}
		seen[pw] = true
	}
}

func TestGeneratePasswordHonoursALargerMinimum(t *testing.T) {
	pw := auth.GeneratePassword(40)
	if len(pw) != 40 {
		t.Fatalf("expected a 40-character password, got %d", len(pw))
	}
}
