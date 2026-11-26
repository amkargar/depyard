package pypi

import (
	"fmt"
	"strconv"
	"strings"
)

// PEP 440 version handling.
//
// Python versions are not semver. A public version identifier has the form
//
//	[N!]N(.N)*[{a|b|rc}N][.postN][.devN][+local]
//
// and they are ordered by the scheme defined in PEP 440, which is implemented here
// directly from the specification text rather than approximated with semver rules.

// Version is a parsed PEP 440 version identifier.
type Version struct {
	// Epoch is the "N!" segment; 0 when absent.
	Epoch int
	// Release holds the numeric release segments (e.g. 1, 2, 3 for "1.2.3").
	Release []int
	// PreKind is 0 (none), 1 (a), 2 (b) or 3 (rc).
	PreKind int
	// PreNum is the numeric component of the pre-release segment.
	PreNum int
	// HasPre reports whether a pre-release segment was present.
	HasPre bool
	// PostNum is the numeric component of the post-release segment.
	PostNum int
	// HasPost reports whether a post-release segment was present.
	HasPost bool
	// DevNum is the numeric component of the development release segment.
	DevNum int
	// HasDev reports whether a development release segment was present.
	HasDev bool
	// Local holds the local version identifier segments (without the "+").
	Local []string
	// Raw is the original spelling.
	Raw string
}

// Pre-release kind ordering values.
const (
	preNone = 0
	preA    = 1
	preB    = 2
	preRC   = 3
)

// ParseVersion parses a PEP 440 version identifier.
//
// The grammar follows PEP 440 Appendix B. The returned error describes the first
// problem encountered so callers can surface an actionable message.
func ParseVersion(raw string) (*Version, error) {
	original := raw
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, fmt.Errorf("version must not be empty")
	}
	// A single leading "v" is permitted and ignored.
	if s[0] == 'v' || s[0] == 'V' {
		s = s[1:]
	}
	if s == "" {
		return nil, fmt.Errorf("invalid version %q", original)
	}

	v := &Version{Raw: original}

	// Epoch: "N!"
	if idx := strings.IndexByte(s, '!'); idx >= 0 {
		epochStr := s[:idx]
		if epochStr == "" {
			return nil, fmt.Errorf("invalid version %q: empty epoch", original)
		}
		n, err := strconv.Atoi(epochStr)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid version %q: malformed epoch", original)
		}
		v.Epoch = n
		s = s[idx+1:]
	}

	// Local version: "+local"
	if idx := strings.IndexByte(s, '+'); idx >= 0 {
		local := s[idx+1:]
		s = s[:idx]
		if local == "" {
			return nil, fmt.Errorf("invalid version %q: empty local version", original)
		}
		for _, seg := range strings.FieldsFunc(local, func(r rune) bool {
			return r == '.' || r == '-' || r == '_'
		}) {
			if seg == "" {
				return nil, fmt.Errorf("invalid version %q: malformed local version", original)
			}
			v.Local = append(v.Local, strings.ToLower(seg))
		}
		if len(v.Local) == 0 {
			return nil, fmt.Errorf("invalid version %q: malformed local version", original)
		}
	}

	// Development release: ".devN" (may appear last, after post).
	if idx := findSegment(s, "dev"); idx >= 0 {
		numStr := s[idx:]
		num, err := parseTrailingNumber(numStr)
		if err != nil {
			return nil, fmt.Errorf("invalid version %q: malformed dev release", original)
		}
		v.HasDev = true
		v.DevNum = num
		s = strings.TrimRight(s[:idx], ".-_")
	}

	// Post release: ".postN", ".revN" or "-N".
	if idx := findPostSegment(s); idx >= 0 {
		numStr := s[idx:]
		num, err := parseTrailingNumber(numStr)
		if err != nil {
			return nil, fmt.Errorf("invalid version %q: malformed post release", original)
		}
		v.HasPost = true
		v.PostNum = num
		s = strings.TrimRight(s[:idx], ".-_")
	}

	// Pre release: ".aN", ".bN", ".rcN" (and the alpha/beta/c/pre/preview spellings).
	if idx, spelling := findPreSegment(s); idx >= 0 {
		num, err := parseTrailingNumber(s[idx:])
		if err != nil {
			return nil, fmt.Errorf("invalid version %q: malformed pre-release", original)
		}
		kind := preKindOf(spelling)
		if kind == preNone {
			return nil, fmt.Errorf("invalid version %q: unknown pre-release marker %q", original, spelling)
		}
		v.HasPre = true
		v.PreKind = kind
		v.PreNum = num
		s = strings.TrimRight(s[:idx], ".-_")
	}

	// Release segment: "N(.N)*"
	if s == "" {
		return nil, fmt.Errorf("invalid version %q: missing release segment", original)
	}
	parts := strings.Split(s, ".")
	for _, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("invalid version %q: malformed release segment", original)
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid version %q: malformed release segment", original)
		}
		v.Release = append(v.Release, n)
	}
	return v, nil
}

// findSegment locates a trailing ".dev"/"dev" segment and returns its index.
func findSegment(s, keyword string) int {
	lower := strings.ToLower(s)
	idx := strings.LastIndex(lower, keyword)
	if idx < 0 {
		return -1
	}
	// The keyword must start a segment (preceded by a separator or be at the start).
	if idx > 0 {
		prev := lower[idx-1]
		if prev != '.' && prev != '-' && prev != '_' {
			return -1
		}
	}
	// It must be followed by digits only.
	rest := lower[idx+len(keyword):]
	for _, c := range rest {
		if c < '0' || c > '9' {
			return -1
		}
	}
	return idx
}

// findPostSegment locates a trailing post-release segment and returns its index.
func findPostSegment(s string) int {
	lower := strings.ToLower(s)
	for _, keyword := range []string{"post", "rev"} {
		idx := strings.LastIndex(lower, keyword)
		if idx < 0 {
			continue
		}
		if idx > 0 {
			prev := lower[idx-1]
			if prev != '.' && prev != '-' && prev != '_' {
				continue
			}
		}
		rest := lower[idx+len(keyword):]
		allDigits := rest != ""
		for _, c := range rest {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if !allDigits {
			continue
		}
		return idx
	}
	// The implicit form "-N" (which normalizes to ".postN").
	if idx := strings.LastIndex(s, "-"); idx > 0 {
		rest := s[idx+1:]
		if rest != "" {
			allDigits := true
			for _, c := range rest {
				if c < '0' || c > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				return idx
			}
		}
	}
	return -1
}

// findPreSegment locates a trailing pre-release segment and returns its index.
//
// PEP 440 allows the pre-release marker to be attached directly to the release
// segment ("1.0a1") or separated by ".", "-" or "_" ("1.0.a1", "1.0-a1").
func findPreSegment(s string) (int, string) {
	lower := strings.ToLower(s)
	// Longest spellings first so "alpha" is not mistaken for "a".
	for _, spelling := range []string{"alpha", "beta", "preview", "pre", "rc", "a", "b", "c"} {
		idx := strings.LastIndex(lower, spelling)
		if idx < 0 {
			continue
		}
		// The marker must start a segment: either at the beginning of the string, or
		// preceded by a separator, or directly attached to a digit.
		if idx > 0 {
			prev := lower[idx-1]
			if prev != '.' && prev != '-' && prev != '_' && (prev < '0' || prev > '9') {
				continue
			}
		}
		// The marker must be followed by digits only (or nothing at all).
		rest := lower[idx+len(spelling):]
		for _, c := range rest {
			if c < '0' || c > '9' {
				return -1, ""
			}
		}
		// The character before the marker must not be a letter, otherwise this is part
		// of a longer word ("1.0foobar" is not a pre-release).
		if idx > 0 {
			prev := lower[idx-1]
			if prev >= 'a' && prev <= 'z' {
				continue
			}
		}
		return idx, spelling
	}
	return -1, ""
}

// parseTrailingNumber extracts the numeric component of a version segment.
// An absent number means 0, matching PEP 440 ("1.0a" == "1.0a0").
func parseTrailingNumber(segment string) (int, error) {
	// The segment starts with the keyword (dev/post/rev/a/b/rc/...) optionally followed
	// by digits. Skip the leading non-digit characters, then read the digits.
	lower := strings.ToLower(segment)
	digits := ""
	for _, c := range lower {
		if c >= '0' && c <= '9' {
			digits += string(c)
		}
	}
	if digits == "" {
		// An absent number means 0, matching PEP 440 ("1.0a" == "1.0a0").
		return 0, nil
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// compareLocalSegment compares two local version segments.
//
// Numeric segments compare as integers; a numeric segment always sorts above an
// alphanumeric one.
func compareLocalSegment(a, b string) int {
	an, aNum := atoiStrict(a)
	bn, bNum := atoiStrict(b)
	switch {
	case aNum && bNum:
		if an != bn {
			if an < bn {
				return -1
			}
			return 1
		}
		return 0
	case aNum:
		return 1
	case bNum:
		return -1
	default:
		return strings.Compare(a, b)
	}
}

// preKindOf maps a pre-release spelling to its ordering value.
func preKindOf(spelling string) int {
	switch spelling {
	case "a", "alpha":
		return preA
	case "b", "beta":
		return preB
	case "rc", "c", "pre", "preview":
		return preRC
	}
	return preNone
}

// canonicalPreKind is the canonical spelling for a pre-release kind.
func canonicalPreKind(kind int) string {
	switch kind {
	case preA:
		return "a"
	case preB:
		return "b"
	case preRC:
		return "rc"
	}
	return ""
}

// String renders the canonical PEP 440 form of the version.
func (v *Version) String() string {
	var b strings.Builder
	if v.Epoch > 0 {
		b.WriteString(strconv.Itoa(v.Epoch))
		b.WriteByte('!')
	}
	for i, n := range v.Release {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(strconv.Itoa(n))
	}
	if v.HasPre {
		// PEP 440's canonical form attaches the pre-release marker directly to the
		// release segment: "1.0a1" (not "1.0.a1").
		b.WriteString(canonicalPreKind(v.PreKind))
		b.WriteString(strconv.Itoa(v.PreNum))
	}
	if v.HasPost {
		b.WriteString(".post")
		b.WriteString(strconv.Itoa(v.PostNum))
	}
	if v.HasDev {
		b.WriteString(".dev")
		b.WriteString(strconv.Itoa(v.DevNum))
	}
	if len(v.Local) > 0 {
		b.WriteByte('+')
		b.WriteString(strings.Join(v.Local, "."))
	}
	return b.String()
}

// IsPreRelease reports whether the version is a pre-release (alpha/beta/rc) or a
// development release. Both are excluded by default when serving an index.
func (v *Version) IsPreRelease() bool {
	return v.HasPre || v.HasDev
}

// Compare returns -1, 0 or 1 depending on the PEP 440 ordering of v and other.
//
// The ordering key is: epoch, then the zero-padded release segment, then the
// pre-release key, then the post-release key, then the development-release key.
//
// The two edge rules that matter:
//   - a version with only a dev segment sorts BELOW every pre-release of the same
//     release (1.0.dev1 < 1.0a1);
//   - a missing post sorts BELOW every post, and a missing dev sorts ABOVE every dev.
func (v *Version) Compare(other *Version) int {
	if v.Epoch != other.Epoch {
		if v.Epoch < other.Epoch {
			return -1
		}
		return 1
	}

	if c := compareRelease(v.Release, other.Release); c != 0 {
		return c
	}

	if c := comparePre(v, other); c != 0 {
		return c
	}

	// Post: a missing post sorts below every post.
	switch {
	case !v.HasPost && !other.HasPost:
	case !v.HasPost:
		return -1
	case !other.HasPost:
		return 1
	case v.PostNum != other.PostNum:
		if v.PostNum < other.PostNum {
			return -1
		}
		return 1
	}

	// Dev: a missing dev sorts above every dev.
	switch {
	case !v.HasDev && !other.HasDev:
	case !v.HasDev:
		return 1
	case !other.HasDev:
		return -1
	case v.DevNum != other.DevNum:
		if v.DevNum < other.DevNum {
			return -1
		}
		return 1
	}

	return compareLocal(v.Local, other.Local)
}

// compareRelease compares two release segments, zero-padding the shorter one.
func compareRelease(a, b []int) int {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		av, bv := 0, 0
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

// comparePre compares the pre-release parts of two versions.
//
// The rules from PEP 440:
//   - a missing pre-release sorts ABOVE every pre-release (1.0a1 < 1.0);
//   - a dev-only version sorts BELOW every pre-release (1.0.dev1 < 1.0a1);
//   - otherwise the pre kinds order a < b < rc, then by number.
func comparePre(v, other *Version) int {
	switch {
	case !v.HasPre && !other.HasPre:
		// Both may still differ via dev, handled by the caller.
		return 0
	case !v.HasPre:
		// v has no pre-release. If v is dev-only it sorts below other's pre-release.
		if v.HasDev {
			return -1
		}
		return 1
	case !other.HasPre:
		if other.HasDev {
			return 1
		}
		return -1
	}

	if v.PreKind != other.PreKind {
		if v.PreKind < other.PreKind {
			return -1
		}
		return 1
	}
	if v.PreNum != other.PreNum {
		if v.PreNum < other.PreNum {
			return -1
		}
		return 1
	}
	return 0
}

// compareLocal compares two local version segments.
//
// Per PEP 440 a local version sorts ABOVE the same version without a local segment.
// Numeric segments compare as integers and always sort above alphanumeric ones.
func compareLocal(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return -1
	case len(b) == 0:
		return 1
	}

	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if i >= len(a) {
			return -1
		}
		if i >= len(b) {
			return 1
		}
		if c := compareLocalSegment(a[i], b[i]); c != 0 {
			return c
		}
	}
	return 0
}

// atoiStrict parses a non-negative integer, reporting whether the string was purely numeric.
func atoiStrict(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// NormalizeVersion parses a version and returns its canonical PEP 440 spelling.
// It returns the trimmed input when the version cannot be parsed, so that unusual but
// upstream-issued versions still round-trip through the index.
func NormalizeVersion(raw string) string {
	v, err := ParseVersion(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return v.String()
}

// CompareVersions compares two version strings using PEP 440 ordering.
// Unparseable versions fall back to a lexical comparison so the order stays stable.
func CompareVersions(a, b string) int {
	av, aErr := ParseVersion(a)
	bv, bErr := ParseVersion(b)
	if aErr != nil && bErr != nil {
		return strings.Compare(strings.TrimSpace(a), strings.TrimSpace(b))
	}
	if aErr != nil {
		return -1
	}
	if bErr != nil {
		return 1
	}
	return av.Compare(bv)
}

// IsPreReleaseVersion reports whether a version string is a pre-release.
func IsPreReleaseVersion(raw string) bool {
	v, err := ParseVersion(raw)
	if err != nil {
		return false
	}
	return v.IsPreRelease()
}
