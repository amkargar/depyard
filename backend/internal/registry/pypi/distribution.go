package pypi

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// DistributionInfo describes a Python distribution file (a wheel or an sdist).
type DistributionInfo struct {
	// Filename is the original file name.
	Filename string
	// Packagetype is "bdist_wheel" or "sdist".
	Packagetype string
	// PythonVersion is the wheel's python tag (e.g. "py3", "cp312") or "source".
	PythonVersion string
	// RequiresPython is the "Requires-Python" specifier from the core metadata.
	RequiresPython string
	// Summary is the one-line description from the core metadata.
	Summary string
	// Description is the long description body.
	Description string
	// Homepage, Author, AuthorEmail and License come from the core metadata.
	Homepage    string
	Author      string
	AuthorEmail string
	License     string
	// Classifiers are the trove classifiers.
	Classifiers []string
	// Keywords are the "Keywords" metadata entries.
	Keywords []string
	// ProjectURLs maps a label to a URL from the "Project-URL" entries.
	ProjectURLs map[string]string
	// MetadataVersion is the core metadata version.
	MetadataVersion string
	// Version is the distribution's own version, read from its file name.
	Version string
	// ProjectName is the distribution's own project name, read from its file name.
	ProjectName string
	// CoreMetadata is the raw METADATA/PKG-INFO payload.
	CoreMetadata []byte
	// MetadataDigest is the SHA-256 of CoreMetadata.
	MetadataDigest string
	// Digest is the SHA-256 of the distribution file.
	Digest string
	// MD5Digest is the MD5 of the distribution file.
	MD5Digest string
	// SizeBytes is the distribution file's size.
	SizeBytes int64
}

// Package types as used by the legacy upload API.
const (
	PackageTypeWheel = "bdist_wheel"
	PackageTypeSdist = "sdist"
)

// ErrUnsupportedDistribution is returned for files that are neither wheels nor sdists.
var ErrUnsupportedDistribution = errors.New("unsupported distribution file")

// InspectDistribution analyses an uploaded distribution file and extracts its
// core metadata.
//
// It handles wheels (ZIP archives containing a "*.dist-info/METADATA" file) and
// source distributions (gzip-compressed tarballs containing a "PKG-INFO" file).
func InspectDistribution(filename string, data []byte) (*DistributionInfo, error) {
	info := &DistributionInfo{
		Filename:  filename,
		Digest:    sha256Hex(data),
		MD5Digest: md5Hex(data),
		SizeBytes: int64(len(data)),
	}

	switch {
	case strings.HasSuffix(strings.ToLower(filename), ".whl"):
		info.Packagetype = PackageTypeWheel
		meta, err := extractWheelMetadata(data)
		if err != nil {
			return nil, err
		}
		info.CoreMetadata = meta
		info.PythonVersion = wheelPythonTag(filename)
	case isSdistFilename(filename):
		info.Packagetype = PackageTypeSdist
		info.PythonVersion = "source"
		meta, err := extractSdistMetadata(data)
		if err != nil {
			return nil, err
		}
		info.CoreMetadata = meta
	default:
		return nil, fmt.Errorf("%w: %q is neither a wheel nor a source distribution", ErrUnsupportedDistribution, filename)
	}

	info.ProjectName, info.Version = nameAndVersionFromFilename(filename)

	if len(info.CoreMetadata) > 0 {
		info.MetadataDigest = sha256Hex(info.CoreMetadata)
		applyCoreMetadata(info, info.CoreMetadata)
	}
	return info, nil
}

// isSdistFilename reports whether a filename looks like a source distribution.
func isSdistFilename(filename string) bool {
	lower := strings.ToLower(filename)
	for _, ext := range []string{".tar.gz", ".tgz", ".zip", ".tar.bz2", ".tar.xz"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// extractWheelMetadata reads the "*.dist-info/METADATA" member of a wheel.
func extractWheelMetadata(data []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("read wheel archive: %w", err)
	}
	for _, f := range zr.File {
		name := f.Name
		if !strings.HasSuffix(name, ".dist-info/METADATA") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", name, err)
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, 4<<20))
	}
	return nil, errors.New("wheel does not contain a .dist-info/METADATA file")
}

// extractSdistMetadata reads the "PKG-INFO" member of a source distribution.
func extractSdistMetadata(data []byte) ([]byte, error) {
	if len(data) >= 4 && data[0] == 'P' && data[1] == 'K' && data[2] == 3 && data[3] == 4 {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, fmt.Errorf("read sdist archive: %w", err)
		}
		for _, f := range zr.File {
			if path.Base(f.Name) != "PKG-INFO" {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("open %s: %w", f.Name, err)
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, 4<<20))
		}
		return nil, errors.New("source distribution does not contain a PKG-INFO file")
	}

	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("read sdist archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read sdist archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if path.Base(header.Name) != "PKG-INFO" {
			continue
		}
		return io.ReadAll(io.LimitReader(tr, 4<<20))
	}
	return nil, errors.New("source distribution does not contain a PKG-INFO file")
}

// wheelPythonTag extracts the Python tag from a wheel filename.
//
// The wheel filename convention is
// "{distribution}-{version}(-{build tag})?-{python tag}-{abi tag}-{platform tag}.whl".
func wheelPythonTag(filename string) string {
	base := strings.TrimSuffix(path.Base(filename), path.Ext(filename))
	parts := strings.Split(base, "-")
	if len(parts) < 3 {
		return ""
	}
	// The python tag is the third-from-last component.
	return parts[len(parts)-3]
}

// applyCoreMetadata parses an RFC 822 style core metadata document into the info struct.
func applyCoreMetadata(info *DistributionInfo, raw []byte) {
	text := string(raw)
	headers, body := splitCoreMetadata(text)

	info.MetadataVersion = headers["metadata-version"]
	info.RequiresPython = headers["requires-python"]
	info.Summary = headers["summary"]
	info.Homepage = headers["homepage"]
	info.Author = headers["author"]
	info.AuthorEmail = headers["author-email"]
	info.License = headers["license"]
	info.Description = strings.TrimSpace(body)

	if classifiers := headers["classifier"]; classifiers != "" {
		info.Classifiers = splitHeaderList(classifiers)
	}
	if keywords := headers["keywords"]; keywords != "" {
		info.Keywords = splitHeaderList(keywords)
	}
	if urls := headers["project-url"]; urls != "" {
		info.ProjectURLs = map[string]string{}
		for _, line := range splitHeaderList(urls) {
			label, url, found := strings.Cut(line, ",")
			if !found {
				continue
			}
			info.ProjectURLs[strings.TrimSpace(label)] = strings.TrimSpace(url)
		}
	}
}

// splitCoreMetadata splits an RFC 822 metadata document into its headers and body.
func splitCoreMetadata(text string) (map[string]string, string) {
	headers := map[string]string{}
	// Normalize line endings so the parser works for both LF and CRLF documents.
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	parts := strings.SplitN(normalized, "\n\n", 2)
	headerBlock := parts[0]
	body := ""
	if len(parts) > 1 {
		body = parts[1]
	}

	var lastKey string
	for _, line := range strings.Split(headerBlock, "\n") {
		if line == "" {
			continue
		}
		// A continuation line starts with whitespace.
		if line[0] == ' ' || line[0] == '\t' {
			if lastKey != "" {
				headers[lastKey] += "\n" + strings.TrimSpace(line)
			}
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		lastKey = strings.ToLower(strings.TrimSpace(key))
		headers[lastKey] = strings.TrimSpace(value)
	}
	return headers, body
}

// splitHeaderList splits a comma-separated metadata header into its entries.
func splitHeaderList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func md5Hex(data []byte) string {
	sum := md5Sum(data)
	return hex.EncodeToString(sum[:])
}

// md5Sum computes the MD5 digest of the data.
func md5Sum(data []byte) [16]byte {
	return md5.Sum(data)
}

// nameAndVersionFromFilename splits a distribution file name into its project
// name and version.
//
// Wheels follow "{distribution}-{version}(-{build})?-{python}-{abi}-{platform}.whl"
// and source distributions follow "{name}-{version}.tar.gz".
func nameAndVersionFromFilename(filename string) (string, string) {
	base := filename
	for _, ext := range []string{".whl", ".tar.gz", ".tgz", ".zip", ".tar.bz2", ".tar.xz"} {
		if strings.HasSuffix(strings.ToLower(base), ext) {
			base = base[:len(base)-len(ext)]
			break
		}
	}
	parts := strings.Split(base, "-")
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	return base, ""
}
