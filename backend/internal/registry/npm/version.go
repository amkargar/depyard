package npm

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// SemVersion is a parsed semantic version (semver.org). Build metadata is
// accepted but ignored for ordering, exactly as the spec requires.
type SemVersion struct {
	Major, Minor, Patch int
	Pre                 []string
	Raw                 string
}

// ParseVersion parses a strict semver 2.0.0 identifier.
func ParseVersion(raw string) (*SemVersion, error) {
	s := strings.TrimSpace(raw)
	m := versionRegex.FindStringSubmatch(s)
	if m == nil {
		return nil, fmt.Errorf("invalid semantic version %q", raw)
	}
	v := &SemVersion{Raw: s}
	var err error
	if v.Major, err = strconv.Atoi(m[1]); err != nil {
		return nil, fmt.Errorf("invalid semantic version %q", raw)
	}
	if v.Minor, err = strconv.Atoi(m[2]); err != nil {
		return nil, fmt.Errorf("invalid semantic version %q", raw)
	}
	if v.Patch, err = strconv.Atoi(m[3]); err != nil {
		return nil, fmt.Errorf("invalid semantic version %q", raw)
	}
	if m[4] != "" {
		v.Pre = strings.Split(m[4], ".")
	}
	return v, nil
}

// IsPrerelease reports whether the version carries a pre-release identifier.
func (v *SemVersion) IsPrerelease() bool { return len(v.Pre) > 0 }

// compareInt returns -1, 0 or 1 for two integers.
func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// CompareVersions orders two versions per semver 2.0.0 section 11. It falls
// back to a lexical comparison for non-semver strings so upstream data with a
// nonstandard version never disappears from a listing.
func CompareVersions(a, b string) int {
	va, errA := ParseVersion(a)
	vb, errB := ParseVersion(b)
	if errA != nil || errB != nil {
		return strings.Compare(a, b)
	}
	if c := compareInt(va.Major, vb.Major); c != 0 {
		return c
	}
	if c := compareInt(va.Minor, vb.Minor); c != 0 {
		return c
	}
	if c := compareInt(va.Patch, vb.Patch); c != 0 {
		return c
	}
	if len(va.Pre) == 0 && len(vb.Pre) == 0 {
		return 0
	}
	if len(va.Pre) == 0 {
		return 1
	}
	if len(vb.Pre) == 0 {
		return -1
	}
	for i := 0; i < len(va.Pre) && i < len(vb.Pre); i++ {
		if va.Pre[i] == vb.Pre[i] {
			continue
		}
		na, errA := strconv.Atoi(va.Pre[i])
		nb, errB := strconv.Atoi(vb.Pre[i])
		switch {
		case errA == nil && errB == nil:
			return compareInt(na, nb)
		case errA == nil:
			return -1 // numeric identifiers have lower precedence
		case errB == nil:
			return 1
		default:
			return strings.Compare(va.Pre[i], vb.Pre[i])
		}
	}
	return compareInt(len(va.Pre), len(vb.Pre))
}

// SortVersions orders versions oldest first, newest last.
func SortVersions(versions []string) {
	sort.SliceStable(versions, func(i, j int) bool {
		return CompareVersions(versions[i], versions[j]) < 0
	})
}

// LatestVersion returns the highest version, optionally ignoring pre-releases.
func LatestVersion(versions []string, includePrerelease bool) string {
	var latest string
	for _, v := range versions {
		if !includePrerelease {
			parsed, err := ParseVersion(v)
			if err == nil && parsed.IsPrerelease() {
				continue
			}
		}
		if latest == "" || CompareVersions(v, latest) > 0 {
			latest = v
		}
	}
	return latest
}
