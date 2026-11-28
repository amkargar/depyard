package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Exercise the prepared-tree adapter with APT itself. All sources, package lists,
// archives, dpkg state and downloads live in the temporary directory. Nothing is
// installed and no system signing keys or public mirrors are used.
func TestAPTNativeClient(t *testing.T) {
	apt := testClient(t, "DEPYARD_TEST_APT_GET")
	f, remote := smokeServer(t, "debian")
	dir := t.TempDir()
	write := func(name, contents string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	const control = "Package: depyard-test\nVersion: 1.0\nArchitecture: all\nMaintainer: Test <test@example.invalid>\nDescription: Local test fixture\n"
	write("pkg/DEBIAN/control", control)
	// dpkg-deb rejects private control directories. The enclosing temp directory remains private.
	if err := os.Chmod(filepath.Join(dir, "pkg/DEBIAN"), 0755); err != nil {
		t.Fatal(err)
	}
	runClient(t, dir, "dpkg-deb", "--build", "pkg", "package.deb")
	deb := read("package.deb")
	packages := fmt.Sprintf("%sFilename: pool/package.deb\nSize: %d\nSHA256: %s\n\n", control, len(deb), digest(deb))
	const index = "main/binary-amd64/Packages"
	release := fmt.Sprintf("Suite: test\nCodename: test\nArchitectures: amd64\nComponents: main\nSHA256:\n %s %d %s\n", digest(packages), len(packages), index)
	f.write("PUT", "/pool/package.deb", deb, 201)
	f.write("PUT", "/dists/test/"+index, packages, 201)
	f.write("PUT", "/dists/test/Release", release, 201)

	// Trust only this synthetic loopback source; keep verification of repository
	// and package SHA-256 checksums enabled. Do not inherit host hooks/sources.
	write("etc/apt/sources.list", "deb [trusted=yes arch=amd64] "+remote+" test main\n")
	write("var/lib/dpkg/status", "")
	write("apt.conf", fmt.Sprintf(`Dir %q;
Dir::Etc::main "unused.conf";
Dir::Etc::sourcelist "sources.list";
Dir::State::status %q;
Dir::Bin::Methods "/usr/lib/apt/methods";
APT::Architecture "amd64";
Acquire::Languages "none";
Acquire::http::Proxy "DIRECT";
`, dir, filepath.Join(dir, "var/lib/dpkg/status")))
	t.Setenv("APT_CONFIG", filepath.Join(dir, "apt.conf"))
	runClient(t, dir, apt, "update", "-o", "APT::Update::Error-Mode=any")
	runClient(t, dir, apt, "download", "depyard-test=1.0")
	if got := read("depyard-test_1.0_all.deb"); got != deb {
		t.Fatal("downloaded .deb differs from the hosted package")
	}
}
