package pypi

import (
	"regexp"
	"strings"
)

// nameSeparatorPattern matches any run of "-", "_" or "." characters.
var nameSeparatorPattern = regexp.MustCompile(`[-_.]+`)

// NormalizeProjectName normalizes a project name according to the Python packaging
// name-normalization specification:
//
//	re.sub(r"[-_.]+", "-", name).lower()
//
// The normalized name is the identity of a project: "Flask", "flask" and "FLASK"
// all refer to the same project, and "zope.interface" and "zope-interface" do too.
func NormalizeProjectName(name string) string {
	return strings.ToLower(nameSeparatorPattern.ReplaceAllString(strings.TrimSpace(name), "-"))
}

// IsValidProjectName reports whether a name is a legal Python distribution name.
//
// The packaging specification allows ASCII letters, digits, ".", "-" and "_" only.
func IsValidProjectName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// projectNamePattern is used for allow/block/protect project policies.
func matchProjectPattern(pattern, project string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	if strings.EqualFold(pattern, project) {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(strings.ToLower(project), strings.ToLower(strings.TrimSuffix(pattern, "*")))
	}
	return false
}
