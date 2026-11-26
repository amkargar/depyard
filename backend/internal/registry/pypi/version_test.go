package pypi

import (
	"testing"
)

func TestPEP440VersionOrdering(t *testing.T) {
	// The canonical ordering chain from PEP 440 ("Summary of permitted suffixes and
	// relative ordering"), extended with epochs and local versions.
	ordered := []string{
		"1.0.dev1",
		"1.0a1",
		"1.0b1",
		"1.0rc1",
		"1.0",
		"1.0.post1",
		"1.1",
	}

	for i := 0; i < len(ordered)-1; i++ {
		lower, upper := ordered[i], ordered[i+1]
		if CompareVersions(lower, upper) >= 0 {
			t.Fatalf("expected %s < %s", lower, upper)
		}
		if CompareVersions(upper, lower) <= 0 {
			t.Fatalf("expected %s > %s", upper, lower)
		}
	}
}

func TestPEP440DevOnlySortsBelowPreReleases(t *testing.T) {
	// PEP 440: "1.0.dev1" sorts before "1.0a1".
	if CompareVersions("1.0.dev1", "1.0a1") >= 0 {
		t.Fatalf("expected 1.0.dev1 < 1.0a1")
	}
	if CompareVersions("1.0a1.dev1", "1.0a1") >= 0 {
		t.Fatalf("expected 1.0a1.dev1 < 1.0a1")
	}
	// A missing dev sorts ABOVE every dev.
	if CompareVersions("1.0", "1.0.dev1") <= 0 {
		t.Fatalf("expected 1.0 > 1.0.dev1")
	}
}

func TestPEP440EpochOrdering(t *testing.T) {
	// An explicit epoch sorts after every non-epoch version.
	if CompareVersions("1!1.0", "2.0") <= 0 {
		t.Fatalf("expected 1!1.0 > 2.0")
	}
	if CompareVersions("1!1.0", "1!0.9") <= 0 {
		t.Fatalf("expected 1!1.0 > 1!0.9")
	}
	if CompareVersions("2!1.0", "1!99.0") <= 0 {
		t.Fatalf("expected 2!1.0 > 1!99.0")
	}
}

func TestPEP440LocalVersionOrdering(t *testing.T) {
	// A local version sorts above the same version without one.
	if CompareVersions("1.0", "1.0+local") >= 0 {
		t.Fatalf("expected 1.0 < 1.0+local")
	}
	if CompareVersions("1.0+abc", "1.0+abd") >= 0 {
		t.Fatalf("expected 1.0+abc < 1.0+abd")
	}
	// Numeric local segments compare as integers and sort above alphanumeric ones.
	if CompareVersions("1.0+1", "1.0+abc") <= 0 {
		t.Fatalf("expected 1.0+1 > 1.0+abc")
	}
	if CompareVersions("1.0+2", "1.0+10") >= 0 {
		t.Fatalf("expected 1.0+2 < 1.0+10")
	}
}

func TestPEP440VersionParsing(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"1.0", "1.0"},
		{"1.0.0", "1.0.0"},
		{"v1.0", "1.0"},
		{"1.0a1", "1.0a1"},
		{"1.0alpha1", "1.0a1"},
		{"1.0.dev1", "1.0.dev1"},
		{"1.0.post1", "1.0.post1"},
		{"1.0-1", "1.0.post1"},
		{"1.0rc1", "1.0rc1"},
		{"1!2.0", "1!2.0"},
		{"1.0+ubuntu.1", "1.0+ubuntu.1"},
		{"1.0a", "1.0a0"},
		{"1.0.0.0.0.beta", "1.0.0.0.0b0"},
		{"1.0.0.0.0", "1.0.0.0.0"},
	}

	for _, c := range cases {
		v, err := ParseVersion(c.input)
		if err != nil {
			t.Fatalf("ParseVersion(%q) failed: %v", c.input, err)
		}
		if got := v.String(); got != c.want {
			t.Fatalf("ParseVersion(%q).String() = %q, want %q", c.input, got, c.want)
		}
	}

	// Invalid versions must be rejected.
	for _, bad := range []string{"", "abc", "1.0+", "1..0", "!1.0", "1.0.0-", ".."} {
		if _, err := ParseVersion(bad); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}

func TestPEP440IsPreRelease(t *testing.T) {
	pre := []string{"1.0a1", "1.0b1", "1.0rc1", "1.0.dev1", "1.0a1.dev1"}
	for _, v := range pre {
		if !IsPreReleaseVersion(v) {
			t.Fatalf("expected %q to be a pre-release", v)
		}
	}
	final := []string{"1.0", "1.0.1", "1.0.post1", "2!1.0"}
	for _, v := range final {
		if IsPreReleaseVersion(v) {
			t.Fatalf("expected %q to NOT be a pre-release", v)
		}
	}
}

func TestProjectNameNormalization(t *testing.T) {
	cases := map[string]string{
		"Flask":            "flask",
		"flask":            "flask",
		"FLASK":            "flask",
		"zope.interface":   "zope-interface",
		"zope-interface":   "zope-interface",
		"zope_interface":   "zope-interface",
		"my--package":      "my-package",
		"my.-._package":    "my-package",
		"Ruamel.YAML":      "ruamel-yaml",
		"  spaced  ":       "spaced",
		"django-rest-auth": "django-rest-auth",
	}

	for input, want := range cases {
		if got := NormalizeProjectName(input); got != want {
			t.Fatalf("NormalizeProjectName(%q) = %q, want %q", input, got, want)
		}
	}

	// The normalized form must be stable under re-normalization.
	for input := range cases {
		once := NormalizeProjectName(input)
		if twice := NormalizeProjectName(once); twice != once {
			t.Fatalf("normalization is not idempotent for %q: %q vs %q", input, once, twice)
		}
	}
}

func TestProjectNameValidation(t *testing.T) {
	valid := []string{"requests", "Flask", "zope.interface", "my-package", "my_package", "pkg2"}
	for _, n := range valid {
		if !IsValidProjectName(n) {
			t.Fatalf("expected %q to be valid", n)
		}
	}

	invalid := []string{"", "has space", "bad/slash", "emoji🙂", "plus+name"}
	for _, n := range invalid {
		if IsValidProjectName(n) {
			t.Fatalf("expected %q to be invalid", n)
		}
	}
}

func TestProjectPatternMatching(t *testing.T) {
	cases := []struct {
		pattern string
		project string
		want    bool
	}{
		{"*", "anything", true},
		{"requests", "requests", true},
		{"Requests", "requests", true},
		{"requests", "flask", false},
		{"internal-*", "internal-tools", true},
		{"internal-*", "requests", false},
	}
	for _, c := range cases {
		if got := matchProjectPattern(c.pattern, c.project); got != c.want {
			t.Fatalf("pattern %q vs %q: expected %v, got %v", c.pattern, c.project, c.want, got)
		}
	}
}
