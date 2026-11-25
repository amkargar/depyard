package pub_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"testing"

	"github.com/amkargar/mirror-registry/backend/internal/registry/pub"
)

func TestPubSemVerOrderingAndLatestSelection(t *testing.T) {
	versions := []*pub.PubVersion{
		{Version: "1.0.0-alpha.1"},
		{Version: "1.0.0"},
		{Version: "2.0.0-beta.1"},
		{Version: "1.2.0"},
		{Version: "1.10.0"},
		{Version: "1.11.0", Retracted: true},
	}

	pub.SortVersionsAscending(versions)
	wantOrder := []string{"1.0.0-alpha.1", "1.0.0", "1.2.0", "1.10.0", "1.11.0", "2.0.0-beta.1"}
	for i, want := range wantOrder {
		if versions[i].Version != want {
			t.Fatalf("index %d: expected %s, got %s", i, want, versions[i].Version)
		}
	}

	// Latest must prefer highest non-retracted stable (1.10.0), skipping retracted 1.11.0 and prerelease 2.0.0-beta.1
	latest := pub.SelectLatestVersion(versions)
	if latest == nil || latest.Version != "1.10.0" {
		t.Fatalf("expected latest 1.10.0, got %+v", latest)
	}
}

func TestPubPackageNameAndConfigValidation(t *testing.T) {
	if !pub.IsValidPackageName("flutter_bloc") {
		t.Fatalf("flutter_bloc should be valid")
	}
	if pub.IsValidPackageName("Invalid-Name") || pub.IsValidPackageName("123pkg") {
		t.Fatalf("invalid package names should be rejected")
	}

	// Reject unknown Pub config keys
	if _, err := pub.ParseConfig(map[string]any{"rpm_gpg_key": "nope"}); err == nil {
		t.Fatalf("expected unknown pub config key to fail validation")
	}
	// Reject out-of-range timeout
	if _, err := pub.ParseConfig(map[string]any{"upstream_timeout_seconds": 9999}); err == nil {
		t.Fatalf("expected excessive timeout to fail validation")
	}
	// Accept valid config
	cfg, err := pub.ParseConfig(map[string]any{
		"upstream_timeout_seconds": 15,
		"allow_retract":            true,
		"protected_namespaces":     []string{"acme_*"},
	})
	if err != nil {
		t.Fatalf("valid pub config failed: %v", err)
	}
	if cfg.UpstreamTimeoutSeconds != 15 || len(cfg.ProtectedNamespaces) != 1 {
		t.Fatalf("unexpected parsed config: %+v", cfg)
	}
}

func TestExtractPubArchiveAndTraversalProtection(t *testing.T) {
	// Valid archive
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	files := map[string]string{
		"pubspec.yaml": "name: my_pkg\nversion: 1.2.3\ndescription: Hello Pub\n",
		"README.md":    "# My Pkg\n",
	}
	for name, content := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))})
		_, _ = tw.Write([]byte(content))
	}
	_ = tw.Close()
	_ = gzw.Close()

	ex, err := pub.ExtractPubArchive(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ExtractPubArchive valid: %v", err)
	}
	if ex.Name != "my_pkg" || ex.Version != "1.2.3" || ex.Description != "Hello Pub" {
		t.Fatalf("unexpected extracted metadata: %+v", ex)
	}

	// Path traversal archive rejected
	var bad bytes.Buffer
	gzw2 := gzip.NewWriter(&bad)
	tw2 := tar.NewWriter(gzw2)
	_ = tw2.WriteHeader(&tar.Header{Name: "../evil.txt", Mode: 0o644, Size: 4})
	_, _ = tw2.Write([]byte("evil"))
	_ = tw2.Close()
	_ = gzw2.Close()

	if _, err := pub.ExtractPubArchive(bytes.NewReader(bad.Bytes())); err == nil {
		t.Fatalf("expected path traversal archive to be rejected")
	}
}
