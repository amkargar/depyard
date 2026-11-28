package goproxy

import (
	"fmt"
	"strings"
)

// compareVersions compares two Go module versions the way the go command does:
// semantic version ordering, with pre-release versions sorting before the
// corresponding release. Versions that are not valid semver (for example
// "+incompatible" or arbitrary tags) fall back to a lexical comparison so the
// ordering stays deterministic.
func compareVersions(a, b string) int {
	// Strip build metadata: it is ignored for ordering purposes.
	am, bm := stripBuildMetadata(a), stripBuildMetadata(b)

	ap, aPre := splitPrerelease(am)
	bp, bPre := splitPrerelease(bm)

	if c := compareSemver(ap, bp); c != 0 {
		return c
	}

	switch {
	case aPre == "" && bPre == "":
		return 0
	case aPre == "":
		// A release version sorts after any pre-release of the same base.
		return 1
	case bPre == "":
		return -1
	default:
		return comparePrerelease(aPre, bPre)
	}
}

// stripBuildMetadata removes the "+meta" suffix from a version string.
func stripBuildMetadata(v string) string {
	if i := strings.IndexByte(v, '+'); i >= 0 {
		return v[:i]
	}
	return v
}

// splitPrerelease separates the "vX.Y.Z" part from the "-pre" part of a version.
func splitPrerelease(v string) (base, pre string) {
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

// compareSemver compares two "vX.Y.Z" style version strings numerically per component.
func compareSemver(a, b string) int {
	as := strings.Split(strings.TrimPrefix(a, "v"), ".")
	bs := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < 3; i++ {
		av := numericComponent(as, i)
		bv := numericComponent(bs, i)
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	// Fall back to comparing any extra components lexically.
	extra := len(as)
	if len(bs) > extra {
		extra = len(bs)
	}
	for i := 3; i < extra; i++ {
		av, bv := numericComponent(as, i), numericComponent(bs, i)
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

// numericComponent returns the numeric value of the i-th dot-separated component,
// or 0 when the component is missing or not a plain number.
func numericComponent(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n := 0
	for _, c := range parts[i] {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// comparePrerelease compares two pre-release identifiers per the semver spec.
func comparePrerelease(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aNum := atoiStrict(as[i])
		bn, bNum := atoiStrict(bs[i])
		switch {
		case aNum && bNum:
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
		case aNum:
			// Numeric identifiers always have lower precedence than alphanumeric ones.
			return -1
		case bNum:
			return 1
		default:
			if as[i] != bs[i] {
				if as[i] < bs[i] {
					return -1
				}
				return 1
			}
		}
	}
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	default:
		return 0
	}
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

// isPseudoVersion reports whether a version string looks like a Go pseudo-version
// (e.g. v0.0.0-20210101000000-abcdef123456 or v1.2.4-0.20210101000000-abcdef123456).
func isPseudoVersion(v string) bool {
	base, pre := splitPrerelease(stripBuildMetadata(v))
	if pre == "" {
		return false
	}
	if strings.HasPrefix(base, "v0.0.0-") {
		return true
	}
	// vX.Y.Z-0.<timestamp>-<commit> form
	parts := strings.Split(pre, ".")
	if len(parts) >= 3 && parts[0] == "0" && len(parts[1]) == 14 {
		return true
	}
	return false
}

// isValidVersion reports whether a version string is acceptable for proxying.
func isValidVersion(v string) bool {
	if v == "" {
		return false
	}
	if !strings.HasPrefix(v, "v") {
		return false
	}
	// Reject path separators and other traversal characters outright.
	if strings.ContainsAny(v, "/\\") || strings.Contains(v, "..") {
		return false
	}
	return true
}

// isValidModulePath reports whether a module path is well-formed enough to proxy.
//
// The go command rejects module paths containing characters that are not allowed in
// a URL path, so we are equally strict: no uppercase letters, no empty elements,
// and no leading/trailing slashes.
func isValidModulePath(p string) bool {
	if p == "" {
		return false
	}
	if strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return false
	}
	if strings.Contains(p, "//") {
		return false
	}
	if strings.ContainsAny(p, "\\?#") {
		return false
	}
	// Reject any "." or ".." path element to prevent traversal.
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '.' || r == '_' || r == '~':
		case r == '+':
		case r == '/':
		default:
			return false
		}
	}
	// The last path element must not be empty and must not be "." or "..".
	last := p[strings.LastIndexByte(p, '/')+1:]
	if last == "" || last == "." || last == ".." {
		return false
	}
	return true
}

// escapeModulePath encodes a module path for use in an upstream proxy URL.
//
// The go command requires uppercase letters to be encoded as "!" followed by the
// lowercase letter, and every other reserved character to be percent-encoded.
func escapeModulePath(p string) string {
	var b strings.Builder
	b.Grow(len(p))
	for _, c := range []byte(p) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteByte(c)
		case c >= 'A' && c <= 'Z':
			b.WriteByte('!')
			b.WriteByte(c + ('a' - 'A'))
		case c == '-' || c == '.' || c == '_' || c == '~':
			b.WriteByte(c)
		case c == '/':
			// The path separator is kept literal; each segment is escaped separately.
			b.WriteByte(c)
		case c == '+':
			b.WriteString("%2B")
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// SortVersionStrings orders version strings using Go's semantic version rules.
func SortVersionStrings(versions []string) {
	sortVersions(versions)
}

// IsValidModulePath reports whether a module path is well-formed enough to proxy.
func IsValidModulePath(p string) bool {
	return isValidModulePath(p)
}

// EscapeModulePath encodes a module path for use in an upstream proxy URL.
func EscapeModulePath(p string) string {
	return escapeModulePath(p)
}

// IsPseudoVersion reports whether a version string looks like a Go pseudo-version.
func IsPseudoVersion(v string) bool {
	return isPseudoVersion(v)
}

// The wire representation is lowercase; the canonical module name (including
// uppercase letters) is used in zip entry names, policy checks and metadata.
func decodeProxyPath(s string) (string, bool) {
	if !isValidModulePath(strings.ReplaceAll(s, "!", "")) {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '!' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) || s[i] < 'a' || s[i] > 'z' {
			return "", false
		}
		b.WriteByte(s[i] - 'a' + 'A')
	}
	return b.String(), true
}
