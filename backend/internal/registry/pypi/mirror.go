package pypi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/amkargar/mirror-registry/backend/internal/storage"
)

// upstreamJSONIndex is the PEP 691 JSON representation of an upstream project.
type upstreamJSONIndex struct {
	Meta struct {
		APIVersion string `json:"api-version"`
	} `json:"meta"`
	Name  string `json:"name"`
	Files []struct {
		Filename       string            `json:"filename"`
		URL            string            `json:"url"`
		Hashes         map[string]string `json:"hashes"`
		RequiresPython string            `json:"requires-python"`
		Yanked         bool              `json:"yanked"`
		UploadTime     string            `json:"upload-time"`
		// CoreMetadata is either a hash dictionary (PEP 691) or a
		// "<algorithm>=<digest>" string (the form pip uses).
		CoreMetadata     json.RawMessage `json:"core-metadata"`
		DistInfoMetadata json.RawMessage `json:"dist-info-metadata"`
		Size             int64           `json:"size"`
	} `json:"files"`
	Versions []string `json:"versions"`
}

// upstreamHTMLLink is one anchor of an upstream PEP 503 HTML index page.
type upstreamHTMLLink struct {
	Href           string
	Text           string
	RequiresPython string
	Hashes         map[string]string
	Yanked         bool
}

// hrefPattern extracts the href of an anchor tag.
var hrefPattern = regexp.MustCompile(`href\s*=\s*["']([^"']+)["']`)

// requiresPythonPattern extracts the data-requires-python attribute.
var requiresPythonPattern = regexp.MustCompile(`data-requires-python\s*=\s*["']([^"']*)["']`)

// metadataHashPattern extracts the data-dist-info-metadata / data-core-metadata attribute.
var metadataHashPattern = regexp.MustCompile(`data-(?:dist-info|core)-metadata\s*=\s*["']([^"']*)["']`)

// yankedPattern extracts the data-yanked attribute.
var yankedPattern = regexp.MustCompile(`data-yanked\s*=\s*["']([^"']*)["']`)

// simpleIndexPath builds the upstream Simple API path of a project.
func simpleIndexPath(project string) string {
	return "/simple/" + project + "/"
}

// syncProjectFromUpstream fetches the upstream index of a project and records
// its files in the local metadata store.
//
// It returns true when the upstream project exists and was synced.
func (r *PyPIRegistry) syncProjectFromUpstream(ctx context.Context, project string) (bool, error) {
	if !r.canProxy(project) {
		return false, nil
	}
	base := strings.TrimRight(r.inst.UpstreamURL, "/")
	if base == "" {
		return false, nil
	}

	target := base + simpleIndexPath(project)
	opts := r.inst.UpstreamRequestOptions(map[string]string{
		"Accept": "application/vnd.pypi.simple.v1+json, application/vnd.pypi.simple.v1+html;q=0.9, text/html;q=0.8",
	}, r.upstreamTimeout())

	resp, err := r.deps.Upstream.GetWithOptions(ctx, target, opts)
	if err != nil {
		return false, fmt.Errorf("fetch upstream index: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("upstream index returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return false, fmt.Errorf("read upstream index: %w", err)
	}

	files, err := parseUpstreamIndex(body, resp.Header.Get("Content-Type"), project)
	if err != nil {
		return false, err
	}
	if len(files) == 0 {
		return false, nil
	}

	now := time.Now().UTC()
	for _, f := range files {
		if err := r.store.UpsertFile(ctx, &PyFile{
			ID:             f.ID,
			RegistryID:     r.inst.ID,
			ProjectName:    project,
			Version:        f.Version,
			Filename:       f.Filename,
			Packagetype:    f.Packagetype,
			PythonVersion:  f.PythonVersion,
			RequiresPython: f.RequiresPython,
			Digest:         f.Digest,
			MD5Digest:      f.MD5Digest,
			SizeBytes:      f.SizeBytes,
			MetadataDigest: f.MetadataDigest,
			HasMetadata:    f.HasMetadata,
			Yanked:         f.Yanked,
			Origin:         OriginCached,
			UploadTime:     f.UploadTime,
			CachedAt:       &now,
			CreatedAt:      now,
		}); err != nil {
			return false, err
		}
	}

	// Record the project itself so it shows up in the index.
	proj, err := r.store.GetProject(ctx, r.inst.ID, project)
	if err != nil {
		proj = &PyProject{
			ID:         newID(),
			RegistryID: r.inst.ID,
			Name:       project,
			Origin:     OriginCached,
			CreatedAt:  now,
		}
	}
	proj.Origin = OriginCached
	proj.UpstreamSyncedAt = &now
	proj.UpdatedAt = now
	if err := r.store.UpsertProject(ctx, proj); err != nil {
		return false, err
	}
	if err := r.refreshProjectStats(ctx, project); err != nil {
		return false, err
	}
	return true, nil
}

// parseUpstreamIndex parses an upstream Simple API response, in either the
// PEP 691 JSON form or the PEP 503 HTML form.
func parseUpstreamIndex(body []byte, contentType, project string) ([]*PyFile, error) {
	if strings.Contains(strings.ToLower(contentType), "json") ||
		(strings.HasPrefix(strings.TrimSpace(string(body)), "{")) {
		return parseUpstreamJSONIndex(body, project)
	}
	return parseUpstreamHTMLIndex(body, project)
}

// parseUpstreamJSONIndex parses the PEP 691 JSON representation.
func parseUpstreamJSONIndex(body []byte, project string) ([]*PyFile, error) {
	var idx upstreamJSONIndex
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, fmt.Errorf("parse upstream JSON index: %w", err)
	}

	var out []*PyFile
	for _, f := range idx.Files {
		digest := ""
		md5 := ""
		if v, ok := f.Hashes["sha256"]; ok {
			digest = strings.ToLower(v)
		}
		if v, ok := f.Hashes["md5"]; ok {
			md5 = strings.ToLower(v)
		}
		metadataDigest := ""
		hasMetadata := false
		if digest, ok := parseMetadataHash(f.CoreMetadata); ok {
			metadataDigest = digest
			hasMetadata = true
		}
		if !hasMetadata {
			if digest, ok := parseMetadataHash(f.DistInfoMetadata); ok {
				metadataDigest = digest
				hasMetadata = true
			}
		}

		uploadTime := time.Time{}
		if f.UploadTime != "" {
			if parsed, err := time.Parse(time.RFC3339, f.UploadTime); err == nil {
				uploadTime = parsed
			}
		}

		out = append(out, &PyFile{
			ID:             newID(),
			ProjectName:    project,
			Version:        versionForFilename(f.Filename),
			Filename:       f.Filename,
			Packagetype:    packagetypeForFilename(f.Filename),
			PythonVersion:  pythonTagForFilename(f.Filename),
			RequiresPython: f.RequiresPython,
			Digest:         digest,
			MD5Digest:      md5,
			SizeBytes:      f.Size,
			MetadataDigest: metadataDigest,
			HasMetadata:    hasMetadata,
			Yanked:         f.Yanked,
			UploadTime:     uploadTime,
		})
	}
	return out, nil
}

// parseUpstreamHTMLIndex parses the PEP 503 HTML representation.
func parseUpstreamHTMLIndex(body []byte, project string) ([]*PyFile, error) {
	links := extractHTMLLinks(string(body))
	var out []*PyFile
	for _, l := range links {
		filename := l.Text
		if filename == "" {
			filename = path.Base(l.Href)
		}
		if filename == "" {
			continue
		}
		digest := ""
		md5 := ""
		metadataDigest := ""
		if v, ok := l.Hashes["sha256"]; ok {
			digest = strings.ToLower(v)
		}
		if v, ok := l.Hashes["md5"]; ok {
			md5 = strings.ToLower(v)
		}
		if v, ok := l.Hashes["sha256"]; ok && metadataDigest == "" {
			metadataDigest = strings.ToLower(v)
		}
		out = append(out, &PyFile{
			ID:             newID(),
			ProjectName:    project,
			Version:        versionForFilename(filename),
			Filename:       filename,
			Packagetype:    packagetypeForFilename(filename),
			PythonVersion:  pythonTagForFilename(filename),
			RequiresPython: l.RequiresPython,
			Digest:         digest,
			MD5Digest:      md5,
			MetadataDigest: metadataDigest,
			HasMetadata:    metadataDigest != "",
			Yanked:         l.Yanked,
		})
	}
	return out, nil
}

// extractHTMLLinks extracts the anchors of a PEP 503 HTML index page.
func extractHTMLLinks(html string) []upstreamHTMLLink {
	var out []upstreamHTMLLink
	for _, chunk := range strings.Split(html, "<a ") {
		if !strings.Contains(chunk, "href=") {
			continue
		}
		hrefMatch := hrefPattern.FindStringSubmatch(chunk)
		if hrefMatch == nil {
			continue
		}
		link := upstreamHTMLLink{Href: htmlUnescape(hrefMatch[1])}
		if m := requiresPythonPattern.FindStringSubmatch(chunk); m != nil {
			link.RequiresPython = htmlUnescape(m[1])
		}
		if m := metadataHashPattern.FindStringSubmatch(chunk); m != nil {
			link.Hashes = parseHashAttribute(m[1])
		}
		if m := yankedPattern.FindStringSubmatch(chunk); m != nil {
			link.Yanked = !strings.EqualFold(strings.TrimSpace(m[1]), "false")
		}
		// The anchor's visible text is the file name.
		if start := strings.Index(chunk, ">"); start >= 0 {
			end := strings.Index(chunk[start:], "</a>")
			if end > 0 {
				link.Text = strings.TrimSpace(htmlUnescape(chunk[start+1 : start+end]))
			}
		}
		out = append(out, link)
	}
	return out
}

// parseHashAttribute parses the "algorithm=value,algorithm=value" attribute form.
func parseHashAttribute(value string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(value, ",") {
		algo, digest, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		algo = strings.TrimSpace(algo)
		digest = strings.TrimSpace(digest)
		if algo == "" || digest == "" {
			continue
		}
		out[algo] = digest
	}
	return out
}

// htmlUnescape decodes the HTML entities that appear in index pages.
func htmlUnescape(s string) string {
	replacer := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", "\"",
		"&#39;", "'",
		"&apos;", "'",
	)
	return replacer.Replace(s)
}

// packagetypeForFilename derives the package type of a distribution file.
func packagetypeForFilename(filename string) string {
	if strings.HasSuffix(strings.ToLower(filename), ".whl") {
		return PackageTypeWheel
	}
	return PackageTypeSdist
}

// pythonTagForFilename derives the Python tag of a distribution file.
func pythonTagForFilename(filename string) string {
	if strings.HasSuffix(strings.ToLower(filename), ".whl") {
		return wheelPythonTag(filename)
	}
	return "source"
}

// cacheUpstreamFile downloads a distribution file from the upstream index and
// caches it in the local blob store.
func (r *PyPIRegistry) cacheUpstreamFile(ctx context.Context, project, version, filename string) (*PyFile, error) {
	if !r.canProxy(project) {
		return nil, ErrFileNotFound
	}
	base := strings.TrimRight(r.inst.UpstreamURL, "/")
	if base == "" {
		return nil, ErrFileNotFound
	}

	// Resolve the file's upstream URL from the upstream index.
	target, err := r.resolveUpstreamFileURL(ctx, project, filename)
	if err != nil {
		return nil, err
	}
	if target == "" {
		target = base + "/packages/" + project + "/" + version + "/" + filename
	}
	// An upstream index may advertise a relative or absolute URL; resolve it
	// against the upstream base so the request always targets the upstream host.
	target = resolveAgainstBase(base, target)

	opts := r.inst.UpstreamRequestOptions(nil, r.upstreamTimeout())
	resp, err := r.deps.Upstream.GetWithOptions(ctx, target, opts)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream file returned status %d", resp.StatusCode)
	}

	// Buffer the file so its digest can be verified before it is stored.
	data, err := io.ReadAll(io.LimitReader(resp.Body, r.cfg.MaxFileSizeBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > r.cfg.MaxFileSizeBytes {
		return nil, fmt.Errorf("upstream file exceeds the maximum allowed size")
	}

	digest := sha256Hex(data)
	if _, err := r.storeArtifact(ctx,
		artifactKey(project, version, filename),
		strings.NewReader(string(data)),
		digest,
		storage.OriginCached,
	); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	file := &PyFile{
		ID:            newID(),
		RegistryID:    r.inst.ID,
		ProjectName:   project,
		Version:       version,
		Filename:      filename,
		Packagetype:   packagetypeForFilename(filename),
		PythonVersion: pythonTagForFilename(filename),
		Digest:        digest,
		MD5Digest:     md5Hex(data),
		SizeBytes:     int64(len(data)),
		Origin:        OriginCached,
		UploadTime:    now,
		CachedAt:      &now,
		CreatedAt:     now,
	}
	if err := r.store.UpsertFile(ctx, file); err != nil {
		return nil, err
	}
	_ = r.refreshProjectStats(ctx, project)
	return file, nil
}

// resolveUpstreamFileURL looks up the upstream download URL of a file.
func (r *PyPIRegistry) resolveUpstreamFileURL(ctx context.Context, project, filename string) (string, error) {
	base := strings.TrimRight(r.inst.UpstreamURL, "/")
	if base == "" {
		return "", nil
	}
	target := base + simpleIndexPath(project)
	opts := r.inst.UpstreamRequestOptions(map[string]string{
		"Accept": "application/vnd.pypi.simple.v1+json, text/html;q=0.8",
	}, r.upstreamTimeout())

	resp, err := r.deps.Upstream.GetWithOptions(ctx, target, opts)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", err
	}
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(strings.ToLower(contentType), "json") ||
		strings.HasPrefix(strings.TrimSpace(string(body)), "{") {
		var idx upstreamJSONIndex
		if err := json.Unmarshal(body, &idx); err != nil {
			return "", err
		}
		for _, f := range idx.Files {
			if f.Filename == filename {
				return f.URL, nil
			}
		}
		return "", nil
	}
	for _, l := range extractHTMLLinks(string(body)) {
		if l.Text == filename {
			return l.Href, nil
		}
	}
	return "", nil
}

// WarmArtifact implements registry.CacheWarmer.
//
// It mirrors a project's index and its distribution files so they are ready to serve.
func (r *PyPIRegistry) WarmArtifact(ctx context.Context, name string) error {
	project := NormalizeProjectName(name)
	if !r.allowsProject(project) {
		return fmt.Errorf("project %q is not allowed", project)
	}
	synced, err := r.syncProjectFromUpstream(ctx, project)
	if err != nil {
		return err
	}
	if !synced {
		return fmt.Errorf("project %q was not found upstream", project)
	}
	return nil
}

// resolveAgainstBase resolves a possibly relative URL against the upstream base.
func resolveAgainstBase(base, target string) string {
	if target == "" {
		return ""
	}
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return target
	}
	if !strings.HasPrefix(target, "/") {
		target = "/" + target
	}
	return strings.TrimRight(base, "/") + target
}

// parseMetadataHash extracts the metadata digest from a PEP 691/714 metadata
// reference.
//
// The key is either a hash dictionary ({"sha256": "..."}), a "<algorithm>=<digest>"
// string, or a boolean. Only the first two forms carry a usable digest.
func parseMetadataHash(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "true" || trimmed == "false" {
		return "", false
	}
	if strings.HasPrefix(trimmed, "{") {
		var hashes map[string]string
		if err := json.Unmarshal(raw, &hashes); err != nil {
			return "", false
		}
		if v, ok := hashes["sha256"]; ok && v != "" {
			return strings.ToLower(v), true
		}
		for _, v := range hashes {
			if v != "" {
				return strings.ToLower(v), true
			}
		}
		return "", false
	}
	// String form: "sha256=<digest>".
	value := strings.Trim(trimmed, "\"")
	if idx := strings.Index(value, "="); idx >= 0 {
		algo := strings.ToLower(strings.TrimSpace(value[:idx]))
		digest := strings.TrimSpace(value[idx+1:])
		if algo != "" && digest != "" {
			return strings.ToLower(digest), true
		}
	}
	return "", false
}
