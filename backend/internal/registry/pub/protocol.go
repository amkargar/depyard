package pub

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const PubV2MediaType = "application/vnd.pub.v2+json"

// PackageResponse conforms to the Dart Pub Hosted Repository Specification v2.
type PackageResponse struct {
	Name           string            `json:"name"`
	IsDiscontinued bool              `json:"isDiscontinued,omitempty"`
	ReplacedBy     string            `json:"replacedBy,omitempty"`
	Latest         *VersionResponse  `json:"latest,omitempty"`
	Versions       []VersionResponse `json:"versions"`
}

// VersionResponse represents a single version entry in a Pub v2 package response.
type VersionResponse struct {
	Version       string         `json:"version"`
	Retracted     bool           `json:"retracted,omitempty"`
	Pubspec       map[string]any `json:"pubspec"`
	ArchiveURL    string         `json:"archive_url"`
	ArchiveSHA256 string         `json:"archive_sha256,omitempty"`
	Published     string         `json:"published,omitempty"`
}

// UploadInfoResponse is returned by GET /api/packages/versions/new.
type UploadInfoResponse struct {
	URL    string            `json:"url"`
	Fields map[string]string `json:"fields"`
}

type pubErrorBody struct {
	Error pubErrorDetail `json:"error"`
}

type pubErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type pubSuccessBody struct {
	Success pubSuccessDetail `json:"success"`
}

type pubSuccessDetail struct {
	Message string `json:"message"`
}

func writePubJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", PubV2MediaType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writePubError(w http.ResponseWriter, status int, code, message string) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="pub", message=%q`, message))
	}
	writePubJSON(w, status, pubErrorBody{
		Error: pubErrorDetail{Code: code, Message: message},
	})
}

func writePubSuccess(w http.ResponseWriter, message string) {
	writePubJSON(w, http.StatusOK, pubSuccessBody{
		Success: pubSuccessDetail{Message: message},
	})
}

// buildArchiveURL formats the archive download URL relative to the resolved registry base URL.
func buildArchiveURL(baseURL, pkgName, version string) string {
	escapedPkg := url.PathEscape(pkgName)
	escapedVer := url.PathEscape(version)
	rel := fmt.Sprintf("/packages/%s/versions/%s.tar.gz", escapedPkg, escapedVer)
	if baseURL == "" {
		return rel
	}
	return strings.TrimRight(baseURL, "/") + rel
}

// artifactKey builds the opaque storage reference key for a Pub version tarball.
func artifactKey(pkgName, version string) string {
	return pkgName + "@" + version
}

// buildPackageResponse converts stored PubPackage + PubVersions into an official Pub v2 JSON payload.
func buildPackageResponse(baseURL string, pkg *PubPackage, versions []*PubVersion) PackageResponse {
	SortVersionsAscending(versions)
	verResponses := make([]VersionResponse, 0, len(versions))
	for _, v := range versions {
		verResponses = append(verResponses, toVersionResponse(baseURL, v))
	}
	var latestResp *VersionResponse
	if latest := SelectLatestVersion(versions); latest != nil {
		lr := toVersionResponse(baseURL, latest)
		latestResp = &lr
	}
	return PackageResponse{
		Name:           pkg.Name,
		IsDiscontinued: pkg.IsDiscontinued,
		ReplacedBy:     pkg.ReplacedBy,
		Latest:         latestResp,
		Versions:       verResponses,
	}
}

func toVersionResponse(baseURL string, v *PubVersion) VersionResponse {
	pubspec := v.Pubspec
	if pubspec == nil {
		pubspec = map[string]any{
			"name":    v.PackageName,
			"version": v.Version,
		}
	}
	published := ""
	if !v.PublishedAt.IsZero() {
		published = v.PublishedAt.UTC().Format(time.RFC3339Nano)
	}
	return VersionResponse{
		Version:       v.Version,
		Retracted:     v.Retracted,
		Pubspec:       pubspec,
		ArchiveURL:    buildArchiveURL(baseURL, v.PackageName, v.Version),
		ArchiveSHA256: v.ArchiveDigest,
		Published:     published,
	}
}

// ExtractedArchive holds validated metadata parsed from a Pub .tar.gz archive.
type ExtractedArchive struct {
	Name          string
	Version       string
	Description   string
	Homepage      string
	Repository    string
	Documentation string
	Pubspec       map[string]any
	Readme        string
	Changelog     string
}

const (
	maxPubspecBytes  = 512 * 1024
	maxMarkdownBytes = 1024 * 1024
)

// ExtractPubArchive inspects a Pub .tar.gz stream, validates pubspec.yaml, and extracts README/CHANGELOG.
func ExtractPubArchive(r io.Reader) (*ExtractedArchive, error) {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("invalid gzip archive: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	var pubspecBytes []byte
	var readme, changelog string

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid tar archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}

		clean := strings.TrimPrefix(path.Clean("/"+hdr.Name), "/")
		// Reject suspicious nested traversal entries
		if strings.Contains(hdr.Name, "..") {
			return nil, fmt.Errorf("archive contains invalid path %q", hdr.Name)
		}

		lower := strings.ToLower(clean)
		// Match top-level files (either "pubspec.yaml" or "<dir>/pubspec.yaml")
		base := lower
		if strings.Count(lower, "/") == 1 {
			parts := strings.SplitN(lower, "/", 2)
			base = parts[1]
		} else if strings.Count(lower, "/") > 1 {
			continue
		}

		switch base {
		case "pubspec.yaml", "pubspec.yml":
			if pubspecBytes == nil {
				data, err := io.ReadAll(io.LimitReader(tr, maxPubspecBytes+1))
				if err != nil {
					return nil, fmt.Errorf("read pubspec.yaml: %w", err)
				}
				if len(data) > maxPubspecBytes {
					return nil, fmt.Errorf("pubspec.yaml exceeds maximum allowed size")
				}
				pubspecBytes = data
			}
		case "readme.md", "readme.txt", "readme":
			if readme == "" {
				data, _ := io.ReadAll(io.LimitReader(tr, maxMarkdownBytes))
				readme = string(data)
			}
		case "changelog.md", "changelog.txt", "changelog":
			if changelog == "" {
				data, _ := io.ReadAll(io.LimitReader(tr, maxMarkdownBytes))
				changelog = string(data)
			}
		}
	}

	if len(pubspecBytes) == 0 {
		return nil, fmt.Errorf("archive is missing pubspec.yaml")
	}

	var rawPubspec any
	if err := yaml.Unmarshal(pubspecBytes, &rawPubspec); err != nil {
		return nil, fmt.Errorf("parse pubspec.yaml: %w", err)
	}
	norm, ok := normalizeYAMLValue(rawPubspec).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("pubspec.yaml must be a YAML mapping")
	}

	name, _ := norm["name"].(string)
	name = strings.TrimSpace(name)
	if !IsValidPackageName(name) {
		return nil, fmt.Errorf("invalid pub package name %q in pubspec.yaml", name)
	}

	version, _ := norm["version"].(string)
	version = strings.TrimSpace(version)
	if _, err := ParseSemVer(version); err != nil {
		return nil, fmt.Errorf("invalid pub version %q in pubspec.yaml: %w", version, err)
	}

	return &ExtractedArchive{
		Name:          name,
		Version:       version,
		Description:   strField(norm, "description"),
		Homepage:      strField(norm, "homepage"),
		Repository:    strField(norm, "repository"),
		Documentation: strField(norm, "documentation"),
		Pubspec:       norm,
		Readme:        readme,
		Changelog:     changelog,
	}, nil
}

func strField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}

// normalizeYAMLValue converts YAML values into JSON/BSON-safe Go types (string-keyed maps).
func normalizeYAMLValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			out[k] = normalizeYAMLValue(item)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			out[fmt.Sprint(k)] = normalizeYAMLValue(item)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = normalizeYAMLValue(item)
		}
		return out
	default:
		return v
	}
}
