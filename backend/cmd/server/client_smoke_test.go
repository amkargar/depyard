package main

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/server"
)

// Optional real clients, with isolated homes, no public remote traffic and no
// external daemons. The wire-contract suite does not require any of these tools.
func smokeServer(t *testing.T, kind string) (*fixture, string) {
	t.Helper()
	f := newFixture(t, kind, registry.ModeHosted, "")
	s := httptest.NewServer(server.New(server.Options{Manager: f.mgr}))
	t.Cleanup(s.Close)
	return f, s.URL + f.prefix
}

func testClient(t *testing.T, key string) string {
	t.Helper()
	bin := os.Getenv(key)
	if bin == "" {
		t.Skip("set " + key + " to run native client test")
	}
	return bin
}

func runClient(t *testing.T, dir, bin string, args ...string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", filepath.Base(bin), args[0], err, out)
	}
}

func TestConanNativeClients(t *testing.T) {
	for _, version := range []string{"1", "2"} {
		t.Run(version, func(t *testing.T) {
			bin := testClient(t, "DEPYARD_TEST_CONAN"+version)
			f, remote := smokeServer(t, "conan")
			dir := t.TempDir()
			t.Setenv("HOME", dir)
			t.Setenv("CONAN_HOME", filepath.Join(dir, "cache2"))
			t.Setenv("CONAN_USER_HOME", dir)
			t.Setenv("CONAN_NON_INTERACTIVE", "1")
			recipe := "from conan import ConanFile\nclass Demo(ConanFile):\n    name = 'sample'\n    version = '1.0'\n"
			if version == "1" {
				recipe = strings.Replace(recipe, "from conan ", "from conans ", 1)
			}
			if err := os.WriteFile(filepath.Join(dir, "conanfile.py"), []byte(recipe), 0600); err != nil {
				t.Fatal(err)
			}
			run := func(args ...string) { t.Helper(); runClient(t, dir, bin, args...) }
			run("remote", "add", "depyard", remote)
			if version == "1" {
				ref := "sample/1.0@builder/testing"
				run("user", "-r", "depyard", "-p", f.token, "builder")
				run("create", ".", "builder/testing")
				run("upload", ref, "--all", "-r", "depyard", "--confirm")
				run("remove", ref, "-f")
				run("download", ref, "-r", "depyard")
			} else {
				run("profile", "detect")
				run("remote", "login", "depyard", "builder", "-p", f.token)
				run("create", ".", "-nr")
				run("upload", "sample/1.0", "-r", "depyard", "-c")
				run("remove", "sample/1.0", "-c")
				run("download", "sample/1.0", "-r", "depyard")
			}
		})
	}
}

func TestHubNativeClient(t *testing.T) {
	bin := testClient(t, "DEPYARD_TEST_HF_PYTHON")
	f, remote := smokeServer(t, "huggingface")
	sha := strings.Repeat("a", 40)
	f.write("PUT", "/acme/demo/resolve/"+sha+"/config.json", "{}", 201)
	f.write("PUT", "/acme/demo/refs/main", sha, 201)
	dir := t.TempDir()
	t.Setenv("HF_HOME", dir)
	t.Setenv("HF_HUB_DISABLE_TELEMETRY", "1")
	runClient(t, dir, bin, "-c", `from huggingface_hub import snapshot_download
from pathlib import Path
import sys
assert (Path(snapshot_download('acme/demo',endpoint=sys.argv[1]))/'config.json').read_text()=='{}'`, remote)
}

func TestGoToolDownloadsHostedModule(t *testing.T) {
	f, remote := smokeServer(t, "goproxy")
	const module = "example.com/Acme"
	f.write("PUT", "/example.com/!acme/@v/v1.0.0.zip", zipData(t, module+"@v1.0.0/go.mod", "module "+module+"\n\ngo 1.25.0\n"), 201)
	dir := t.TempDir()
	t.Setenv("GOPROXY", remote)
	t.Setenv("GONOPROXY", "none")
	t.Setenv("GONOSUMDB", module)
	t.Setenv("GOMODCACHE", filepath.Join(dir, "cache"))
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOFLAGS", "-modcacherw")
	t.Setenv("GOWORK", "off")
	runClient(t, dir, filepath.Join(runtime.GOROOT(), "bin", "go"), "mod", "download", module+"@v1.0.0")
}
