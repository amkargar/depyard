package npm

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Package names are case-insensitive for the purposes of this registry and are
// stored in their lowercase canonical form. npm itself recommends lowercase
// names; canonicalizing avoids two records for "Acme" and "acme".
var (
	namePattern  = regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9._-]{0,213}/)?[a-z0-9][a-z0-9._-]{0,213}$`)
	versionRegex = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)
)

// NormalizePackageName returns the canonical (lowercase, unescaped) package name.
// Scoped names keep their single "@scope/name" separator.
func NormalizePackageName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// IsValidPackageName reports whether name follows npm's naming rules.
// Both unscoped ("lodash") and scoped ("@acme/tools") names are accepted.
func IsValidPackageName(name string) bool {
	name = NormalizePackageName(name)
	if name == "" || len(name) > 256 || strings.Contains(name, "//") {
		return false
	}
	// Reject traversal attempts and reserved names before the general pattern.
	segment := name
	if idx := strings.LastIndexByte(segment, '/'); idx >= 0 {
		segment = segment[idx+1:]
	}
	if segment == "" || segment == "." || segment == ".." || strings.HasPrefix(segment, ".") || strings.HasPrefix(segment, "_") {
		return false
	}
	return namePattern.MatchString(name)
}

// escapePackagePath encodes a package name for use in a URL path. The slash of a
// scoped name MUST stay escaped (%2F), otherwise npm clients cannot distinguish
// "@scope/name" from two path segments.
func escapePackagePath(name string) string {
	return url.PathEscape(NormalizePackageName(name))
}

// ParseRequestPath splits an incoming registry path into the canonical package
// name and the remaining (still escaped) path. It accepts both forms npm emits:
//
//	/lodash
//	/@scope%2Fname
//	/@scope%2Fname/-/name-1.2.3.tgz
func ParseRequestPath(escapedPath string) (name, rest string, err error) {
	trimmed := strings.TrimPrefix(escapedPath, "/")
	if trimmed == "" {
		return "", "", fmt.Errorf("package name is required")
	}
	first, remaining, _ := strings.Cut(trimmed, "/")
	rawFirst, err := url.PathUnescape(first)
	if err != nil {
		return "", "", fmt.Errorf("invalid package name encoding: %w", err)
	}
	rawName := rawFirst
	// npm normally escapes the slash of a scoped name as %2F. Accept the
	// unescaped /@scope/name spelling too, since proxies sometimes rewrite it.
	if strings.HasPrefix(rawFirst, "@") && remaining != "" && !strings.HasPrefix(remaining, "-/") {
		second, tail, _ := strings.Cut(remaining, "/")
		rawName = rawFirst + "/" + second
		if tail == "" {
			remaining = ""
		} else {
			remaining = tail
		}
	}
	name = NormalizePackageName(rawName)
	if !IsValidPackageName(name) {
		return "", "", fmt.Errorf("invalid package name %q", rawName)
	}
	if remaining == "" {
		return name, "", nil
	}
	return name, "/" + remaining, nil
}

// tarballFilename extracts a tarball filename from a "/-/..." remainder.
// An empty filename means the request is not a tarball request.
func tarballFilename(rest string) (string, error) {
	if rest == "" {
		return "", nil
	}
	if !strings.HasPrefix(rest, "/-/") {
		return "", fmt.Errorf("unknown package path %q", rest)
	}
	raw := strings.TrimPrefix(rest, "/-/")
	if raw == "" || strings.Contains(raw, "/") {
		return "", fmt.Errorf("invalid tarball path %q", rest)
	}
	filename, err := url.PathUnescape(raw)
	if err != nil {
		return "", fmt.Errorf("invalid tarball name encoding: %w", err)
	}
	if filename == "" || strings.ContainsAny(filename, `/\`) || !strings.HasSuffix(filename, ".tgz") {
		return "", fmt.Errorf("invalid tarball name %q", filename)
	}
	return filename, nil
}

// stripRevisionSuffix removes the CouchDB-style "/-rev/<revision>" suffix that
// npm sends when unpublishing an entire package.
func stripRevisionSuffix(escapedPath string) string {
	if idx := strings.Index(escapedPath, "/-rev/"); idx >= 0 {
		return escapedPath[:idx]
	}
	return strings.TrimSuffix(escapedPath, "/-rev")
}

// defaultTarballFilename builds the conventional npm tarball filename.
func defaultTarballFilename(name, version string) string {
	short := name
	if idx := strings.LastIndexByte(short, '/'); idx >= 0 {
		short = short[idx+1:]
	}
	return short + "-" + version + ".tgz"
}

// matchPackagePattern implements the shared allow/block/protect policy syntax:
// an exact name, "*" for everything, or a trailing "*" prefix pattern.
func matchPackagePattern(pattern, name string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	name = NormalizePackageName(name)
	if pattern == name {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
	}
	return false
}
