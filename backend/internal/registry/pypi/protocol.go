package pypi

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// APIVersion is the Simple Repository API version this implementation speaks.
//
// "1.1" covers PEP 700 (`versions` and `size` fields) in addition to PEP 691.
const APIVersion = "1.1"

// Content types defined by PEP 691.
const (
	ContentTypeJSON = "application/vnd.pypi.simple.v1+json"
	ContentTypeHTML = "application/vnd.pypi.simple.v1+html"
)

// simpleMeta is the "meta" object of a PEP 691 response.
type simpleMeta struct {
	APIVersion string `json:"api-version"`
}

// simpleProject is one entry of the PEP 691 project index.
type simpleProject struct {
	Name string `json:"name"`
}

// simpleIndex is the PEP 691 project index (the response of GET /simple/).
type simpleIndex struct {
	Meta     simpleMeta      `json:"meta"`
	Projects []simpleProject `json:"projects"`
}

// simpleFile is one distribution file in the PEP 691 project detail response.
//
// The field names follow PEP 691 exactly, with the optional PEP 700 additions
// ("versions" on the project, "size" and "upload-time" on the file) and the
// PEP 658 metadata fields.
type simpleFile struct {
	Filename       string            `json:"filename"`
	URL            string            `json:"url"`
	Hashes         map[string]string `json:"hashes"`
	RequiresPython string            `json:"requires-python,omitempty"`
	Yanked         bool              `json:"yanked"`
	YankedReason   string            `json:"yanked_reason,omitempty"`
	// DistInfoMetadata carries the PEP 658 metadata hash.
	//
	// The Simple Repository API defines this key as either a boolean or a
	// hash description. This implementation uses the "<algorithm>=<digest>"
	// string form, which is the same syntax the HTML "data-dist-info-metadata"
	// attribute uses and the form every released pip version understands.
	DistInfoMetadata string `json:"dist-info-metadata,omitempty"`
	// CoreMetadata is the PEP 714 alias of dist-info-metadata.
	CoreMetadata string `json:"core-metadata,omitempty"`
	// Size is the PEP 700 file size in bytes.
	Size int64 `json:"size,omitempty"`
	// UploadTime is the PEP 700 upload timestamp in ISO 8601.
	UploadTime string `json:"upload-time,omitempty"`
}

// simpleDetail is the PEP 691 project detail response.
type simpleDetail struct {
	Meta simpleMeta `json:"meta"`
	Name string     `json:"name"`
	// Files lists every distribution file of the project.
	Files []simpleFile `json:"files"`
	// Versions is the PEP 700 list of release versions.
	Versions []string `json:"versions,omitempty"`
	// Meta keeps the "meta" key first in JSON output.
	_ struct{} `json:"-"`
}

// negotiatedFormat describes the serialization chosen for a response.
type negotiatedFormat int

const (
	formatHTML negotiatedFormat = iota
	formatJSON
)

// contentType records the exact Content-Type to return for a negotiated response.
//
// PEP 691 distinguishes the dedicated "+html" media type from the legacy
// "text/html" one: a client that explicitly asks for
// "application/vnd.pypi.simple.v1+html" must receive exactly that type, while a
// client that accepts the generic "text/html" receives the legacy type.
type contentType struct {
	value string
}

// legacyHTMLContentType is the Content-Type of the PEP 503 HTML representation.
const legacyHTMLContentType = "text/html; charset=utf-8"

// negotiate performs the PEP 691 content negotiation and returns the selected
// format together with the exact Content-Type to send.
//
// The returned Content-Type distinguishes the dedicated PEP 691 HTML media type
// ("application/vnd.pypi.simple.v1+html") from the legacy "text/html" one: a
// client that explicitly asks for the dedicated type receives exactly that type,
// while a client that accepts the generic "text/html" receives the legacy type.
func negotiate(accept string) (negotiatedFormat, string, bool) {
	accept = strings.TrimSpace(accept)
	if accept == "" {
		return formatHTML, legacyHTMLContentType, true
	}

	best := formatHTML
	bestContentType := legacyHTMLContentType
	bestScore := -1
	supported := false

	for _, clause := range strings.Split(accept, ",") {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		mediaType, params := splitMediaType(clause)
		q := 1.0
		if raw, ok := params["q"]; ok {
			if parsed, err := strconv.ParseFloat(raw, 64); err == nil {
				q = parsed
			}
		}
		if q <= 0 {
			continue
		}

		format, score, ok := matchMediaType(mediaType)
		if !ok {
			continue
		}
		supported = true
		if score > bestScore {
			bestScore = score
			best = format
			if format == formatJSON {
				bestContentType = ContentTypeJSON
			} else if score >= 100 {
				bestContentType = ContentTypeHTML
			} else {
				bestContentType = legacyHTMLContentType
			}
		}
	}

	if !supported {
		return formatHTML, legacyHTMLContentType, false
	}
	return best, bestContentType, true
}

// splitMediaType separates a media type from its parameters.
func splitMediaType(clause string) (string, map[string]string) {
	parts := strings.Split(clause, ";")
	mediaType := strings.ToLower(strings.TrimSpace(parts[0]))
	params := make(map[string]string)
	for _, p := range parts[1:] {
		key, value, found := strings.Cut(p, "=")
		if !found {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"`)
		params[key] = value
	}
	return mediaType, params
}

// matchMediaType maps a single media type to a negotiated format and a specificity
// score. Higher scores win when several media types are acceptable.
func matchMediaType(mediaType string) (negotiatedFormat, int, bool) {
	switch mediaType {
	case ContentTypeJSON, "application/vnd.pypi.simple.latest+json":
		return formatJSON, 100, true
	case ContentTypeHTML, "application/vnd.pypi.simple.latest+html":
		return formatHTML, 100, true
	case "application/json":
		return formatJSON, 50, true
	case "text/html":
		return formatHTML, 50, true
	case "text/*":
		// A generic text wildcard still resolves to the HTML representation.
		return formatHTML, 10, true
	case "*/*", "application/*":
		return formatHTML, 1, true
	default:
		// Every other media type — including application/xml and text/plain —
		// is unsupported and must produce a 406.
		return formatHTML, 0, false
	}
}

// renderJSON writes a PEP 691 JSON document.
func renderJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		http.Error(w, `{"error":"failed to encode response"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", ContentTypeJSON)
	w.WriteHeader(status)
	_, _ = w.Write(body)
	_, _ = w.Write([]byte("\n"))
}

// renderHTML writes an HTML document with the negotiated content type.
func renderHTML(w http.ResponseWriter, status int, contentType, body string) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// htmlIndexPage renders the PEP 503 project index.
func htmlIndexPage(projects []string) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html>\n  <head>\n")
	b.WriteString("    <meta name=\"pypi:repository-version\" content=\"" + APIVersion + "\">\n")
	b.WriteString("    <title>Simple index</title>\n")
	b.WriteString("  </head>\n  <body>\n")
	for _, p := range projects {
		b.WriteString("    <a href=\"" + html.EscapeString(p+"/") + "\">" + html.EscapeString(p) + "</a><br>\n")
	}
	b.WriteString("  </body>\n</html>\n")
	return b.String()
}

// htmlFileEntry is one anchor of the PEP 503 project detail page.
type htmlFileEntry struct {
	// URL is the absolute or relative download URL.
	URL string
	// Text is the anchor's visible text (normally the filename).
	Text string
	// RequiresPython maps to the data-requires-python attribute.
	RequiresPython string
	// Hashes maps to the data-dist-info-metadata / data-core-metadata attributes.
	Hashes map[string]string
	// Yanked reports whether the file is yanked (PEP 592).
	Yanked bool
	// YankedReason is the optional reason shown to the user.
	YankedReason string
}

// htmlDetailPage renders the PEP 503 project detail page.
//
// The attribute set covers PEP 503 (href), PEP 592 (data-yanked) and PEP 658/714
// (data-dist-info-metadata, data-core-metadata).
func htmlDetailPage(project string, files []htmlFileEntry) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html>\n  <head>\n")
	b.WriteString("    <meta name=\"pypi:repository-version\" content=\"" + APIVersion + "\">\n")
	b.WriteString("    <title>Links for " + html.EscapeString(project) + "</title>\n")
	b.WriteString("  </head>\n  <body>\n")
	b.WriteString("    <h1>Links for " + html.EscapeString(project) + "</h1>\n")
	for _, f := range files {
		b.WriteString("    <a href=\"" + html.EscapeString(f.URL) + "\"")
		if f.RequiresPython != "" {
			b.WriteString(" data-requires-python=\"" + html.EscapeString(f.RequiresPython) + "\"")
		}
		if len(f.Hashes) > 0 {
			b.WriteString(" data-dist-info-metadata=\"" + html.EscapeString(formatHashAttr(f.Hashes)) + "\"")
			b.WriteString(" data-core-metadata=\"" + html.EscapeString(formatHashAttr(f.Hashes)) + "\"")
		}
		if f.Yanked {
			b.WriteString(" data-yanked=\"true\"")
			if f.YankedReason != "" {
				b.WriteString(" data-yanked-reason=\"" + html.EscapeString(f.YankedReason) + "\"")
			}
		}
		b.WriteString(">" + html.EscapeString(f.Text) + "</a><br>\n")
	}
	b.WriteString("  </body>\n</html>\n")
	return b.String()
}

// formatHashAttr renders a hash map as the PEP 658 "algorithm=value" attribute form.
func formatHashAttr(hashes map[string]string) string {
	keys := make([]string, 0, len(hashes))
	for k := range hashes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+hashes[k])
	}
	return strings.Join(parts, ",")
}

// buildSimpleFiles converts stored file records into the PEP 691 representation.
//
// baseURL is the absolute base of the registry instance, used to build absolute
// download URLs as PEP 691 requires for the JSON representation.
func buildSimpleFiles(files []*PyFile, baseURL string, includePre bool) []simpleFile {
	out := make([]simpleFile, 0, len(files))
	for _, f := range files {
		if !includePre && IsPreReleaseVersion(f.Version) {
			continue
		}
		hashes := map[string]string{}
		if f.Digest != "" {
			hashes["sha256"] = f.Digest
		}
		if f.MD5Digest != "" {
			hashes["md5"] = f.MD5Digest
		}
		entry := simpleFile{
			Filename:       f.Filename,
			URL:            fileDownloadURL(baseURL, f),
			Hashes:         hashes,
			RequiresPython: f.RequiresPython,
			Yanked:         f.Yanked,
			YankedReason:   f.YankedReason,
			Size:           f.SizeBytes,
		}
		if !f.UploadTime.IsZero() {
			entry.UploadTime = f.UploadTime.UTC().Format(time.RFC3339)
		}
		if f.HasMetadata && f.MetadataDigest != "" {
			// Use the "<algorithm>=<digest>" string form, matching the HTML
			// attribute syntax and the form pip expects.
			ref := "sha256=" + f.MetadataDigest
			entry.DistInfoMetadata = ref
			entry.CoreMetadata = ref
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if c := CompareVersions(versionOfFile(out[i].Filename), versionOfFile(out[j].Filename)); c != 0 {
			return c < 0
		}
		return out[i].Filename < out[j].Filename
	})
	return out
}

// versionOfFile recovers the version from a filename. It is only used as a
// tie-breaking sort key when the caller did not pre-sort the records.
func versionOfFile(filename string) string {
	base := filename
	if i := strings.IndexByte(base, '-'); i > 0 {
		base = base[:i]
	}
	return base
}

// fileDownloadURL builds the download URL of a distribution file.
func fileDownloadURL(baseURL string, f *PyFile) string {
	path := "/packages/" + f.ProjectName + "/" + f.Version + "/" + f.Filename
	if baseURL == "" {
		return path
	}
	return strings.TrimRight(baseURL, "/") + path
}

// problemJSON is an RFC 9457 problem document.
type problemJSON struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// writeProblem writes an RFC 9457 "application/problem+json" error response.
func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	body, err := json.MarshalIndent(problemJSON{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Detail: detail,
	}, "", "  ")
	if err != nil {
		http.Error(w, title, status)
		return
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
	_, _ = w.Write([]byte("\n"))
}

// writeProblemText writes a plain-text error response, used by the legacy upload API.
func writeProblemText(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintln(w, message)
}
