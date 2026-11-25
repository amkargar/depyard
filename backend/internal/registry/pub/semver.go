package pub

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Dart Pub package naming rules: lowercase letters, digits, underscores; cannot start with a digit.
var pubPackageNameRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Pub SemVer grammar (Dart pub_semver): major.minor.patch[-prerelease][+build]
var pubSemVerRe = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

// SemVer represents a parsed Dart Pub semantic version.
type SemVer struct {
	Raw        string
	Major      int
	Minor      int
	Patch      int
	Prerelease []string
	Build      []string
}

func IsValidPackageName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	return pubPackageNameRe.MatchString(name)
}

func ParseSemVer(raw string) (SemVer, error) {
	m := pubSemVerRe.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return SemVer{}, fmt.Errorf("invalid pub semantic version %q", raw)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	var pre, build []string
	if m[4] != "" {
		pre = strings.Split(m[4], ".")
	}
	if m[5] != "" {
		build = strings.Split(m[5], ".")
	}
	return SemVer{
		Raw:        strings.TrimSpace(raw),
		Major:      major,
		Minor:      minor,
		Patch:      patch,
		Prerelease: pre,
		Build:      build,
	}, nil
}

func (v SemVer) IsPrerelease() bool {
	return len(v.Prerelease) > 0
}

// CompareSemVer compares two Pub SemVer values (-1 if a < b, 0 if equal, 1 if a > b).
func CompareSemVer(a, b SemVer) int {
	if a.Major != b.Major {
		return cmpInt(a.Major, b.Major)
	}
	if a.Minor != b.Minor {
		return cmpInt(a.Minor, b.Minor)
	}
	if a.Patch != b.Patch {
		return cmpInt(a.Patch, b.Patch)
	}
	// Release > prerelease
	aPre, bPre := len(a.Prerelease) > 0, len(b.Prerelease) > 0
	if !aPre && bPre {
		return 1
	}
	if aPre && !bPre {
		return -1
	}
	if aPre && bPre {
		if c := compareIdentifiers(a.Prerelease, b.Prerelease); c != 0 {
			return c
		}
	}
	// Dart pub_semver also orders build identifiers for deterministic latest selection.
	if len(a.Build) != 0 || len(b.Build) != 0 {
		return compareIdentifiers(a.Build, b.Build)
	}
	return 0
}

func compareIdentifiers(a, b []string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		ai, aErr := strconv.Atoi(a[i])
		bi, bErr := strconv.Atoi(b[i])
		if aErr == nil && bErr == nil {
			if ai != bi {
				return cmpInt(ai, bi)
			}
			continue
		}
		if aErr == nil && bErr != nil {
			return -1 // Numeric identifiers have lower precedence than non-numeric
		}
		if aErr != nil && bErr == nil {
			return 1
		}
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return cmpInt(len(a), len(b))
}

func cmpInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// SortVersionsAscending sorts PubVersion pointers in ascending Dart Pub SemVer order.
func SortVersionsAscending(versions []*PubVersion) {
	sort.Slice(versions, func(i, j int) bool {
		vi, errI := ParseSemVer(versions[i].Version)
		vj, errJ := ParseSemVer(versions[j].Version)
		if errI != nil || errJ != nil {
			return versions[i].Version < versions[j].Version
		}
		return CompareSemVer(vi, vj) < 0
	})
}

// SelectLatestVersion picks the latest non-retracted stable version per Dart Pub semantics,
// falling back to latest non-retracted prerelease, then latest overall.
func SelectLatestVersion(versions []*PubVersion) *PubVersion {
	if len(versions) == 0 {
		return nil
	}
	sorted := make([]*PubVersion, len(versions))
	copy(sorted, versions)
	SortVersionsAscending(sorted)

	// Prefer highest non-retracted stable version
	for i := len(sorted) - 1; i >= 0; i-- {
		v := sorted[i]
		if v.Retracted {
			continue
		}
		if sv, err := ParseSemVer(v.Version); err == nil && !sv.IsPrerelease() {
			return v
		}
	}
	// Fallback: highest non-retracted prerelease
	for i := len(sorted) - 1; i >= 0; i-- {
		if !sorted[i].Retracted {
			return sorted[i]
		}
	}
	return sorted[len(sorted)-1]
}
