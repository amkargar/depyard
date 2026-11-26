package npm

import (
	"encoding/json"
	"sort"
	"time"
)

// buildPackument renders the JSON document npm clients fetch for a package.
//
// The full form keeps the raw version manifests (preserving dependencies,
// engines, bin entries and other fields npm needs) while rewriting every
// dist.tarball URL to this Depyard instance. The abbreviated form is the
// install-v1 representation: only name, version and dist are exposed.
func buildPackument(pkg *NpmPackage, baseURL string, abbreviated bool) map[string]any {
	distTags := make(map[string]string, len(pkg.DistTags)+1)
	for tag, version := range pkg.DistTags {
		distTags[tag] = version
	}
	latest := pkg.LatestVersion
	if latest == "" {
		versions := versionStrings(pkg.Versions)
		latest = LatestVersion(versions, true)
	}
	if latest != "" && distTags["latest"] == "" {
		distTags["latest"] = latest
	}

	versions := make(map[string]any, len(pkg.Versions))
	times := map[string]string{}
	if !pkg.CreatedAt.IsZero() {
		times["created"] = timestamp(pkg.CreatedAt)
	}
	if !pkg.UpdatedAt.IsZero() {
		times["modified"] = timestamp(pkg.UpdatedAt)
	}
	for _, v := range pkg.Versions {
		times[v.Version] = timestamp(v.PublishedAt)
		if abbreviated {
			dist := map[string]any{"tarball": tarballURL(baseURL, pkg.Name, v.Version, v.Filename)}
			if v.Shasum != "" {
				dist["shasum"] = v.Shasum
			}
			if v.Integrity != "" {
				dist["integrity"] = v.Integrity
			}
			versions[v.Version] = map[string]any{
				"name":    pkg.Name,
				"version": v.Version,
				"dist":    dist,
			}
			continue
		}

		manifest := decodeRawVersion(v.RawMetadata)
		if manifest == nil {
			manifest = map[string]any{}
		}
		manifest["name"] = pkg.Name
		manifest["version"] = v.Version
		dist, ok := manifest["dist"].(map[string]any)
		if !ok || dist == nil {
			dist = map[string]any{}
		}
		dist["tarball"] = tarballURL(baseURL, pkg.Name, v.Version, v.Filename)
		if v.Shasum != "" {
			dist["shasum"] = v.Shasum
		}
		if v.Integrity != "" {
			dist["integrity"] = v.Integrity
		}
		manifest["dist"] = dist
		versions[v.Version] = manifest
	}

	doc := map[string]any{
		"name":        pkg.Name,
		"dist-tags":   distTags,
		"versions":    versions,
		"time":        times,
		"maintainers": nonNilStrings(pkg.Maintainers),
	}
	if abbreviated {
		return map[string]any{
			"name":      pkg.Name,
			"dist-tags": distTags,
			"versions":  versions,
		}
	}
	if pkg.Description != "" {
		doc["description"] = pkg.Description
	}
	if pkg.Readme != "" {
		doc["readme"] = pkg.Readme
	}
	if pkg.Homepage != "" {
		doc["homepage"] = pkg.Homepage
	}
	if pkg.Repository != "" {
		doc["repository"] = pkg.Repository
	}
	if pkg.License != "" {
		doc["license"] = pkg.License
	}
	if len(pkg.Keywords) > 0 {
		doc["keywords"] = pkg.Keywords
	}
	return doc
}

// decodeRawVersion decodes a stored version manifest. Malformed records are
// treated as empty manifests rather than failing the whole packument request.
func decodeRawVersion(raw string) map[string]any {
	if raw == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// encodeRawVersion serializes a version manifest for storage.
func encodeRawVersion(manifest map[string]any) string {
	b, err := json.Marshal(manifest)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// sortedVersions returns the package's version identifiers, newest first.
func sortedVersions(pkg *NpmPackage) []string {
	out := versionStrings(pkg.Versions)
	SortVersions(out)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// findVersion returns a pointer to a stored version by identifier.
func findVersion(pkg *NpmPackage, version string) *NpmVersion {
	for i := range pkg.Versions {
		if pkg.Versions[i].Version == version {
			return &pkg.Versions[i]
		}
	}
	return nil
}

// originForVersions derives the package origin from its versions.
func originForVersions(versions []NpmVersion) string {
	hosted, cached := false, false
	for _, v := range versions {
		switch v.Origin {
		case "hosted":
			hosted = true
		default:
			cached = true
		}
	}
	switch {
	case hosted && cached:
		return OriginMixed
	case hosted:
		return "hosted"
	default:
		return "cached"
	}
}

// recalculatePackage refreshes derived fields after a publish or deletion.
func recalculatePackage(pkg *NpmPackage, includePrerelease bool) {
	pkg.VersionCount = len(pkg.Versions)
	var total int64
	for _, v := range pkg.Versions {
		total += v.SizeBytes
	}
	pkg.TotalSizeBytes = total
	pkg.Origin = originForVersions(pkg.Versions)
	versions := sortedVersions(pkg)
	pkg.LatestVersion = LatestVersion(versions, includePrerelease)
	if tagLatest := pkg.DistTags["latest"]; tagLatest == "" || findVersion(pkg, tagLatest) == nil {
		if pkg.LatestVersion != "" {
			if pkg.DistTags == nil {
				pkg.DistTags = map[string]string{}
			}
			pkg.DistTags["latest"] = pkg.LatestVersion
		}
	}
	pkg.UpdatedAt = timestampTime(pkg.UpdatedAt)
}

// timestampTime keeps UpdatedAt in UTC without losing sub-second precision.
func timestampTime(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC()
}

// removeVersion drops a version and its dist-tags from a package.
func removeVersion(pkg *NpmPackage, version string, includePrerelease bool) {
	kept := make([]NpmVersion, 0, len(pkg.Versions))
	for _, v := range pkg.Versions {
		if v.Version != version {
			kept = append(kept, v)
		}
	}
	pkg.Versions = kept
	for tag, v := range pkg.DistTags {
		if v == version {
			delete(pkg.DistTags, tag)
		}
	}
	recalculatePackage(pkg, includePrerelease)
}

// ensureSorted is used by explorer output to show newest versions first.
func ensureSorted(versions []NpmVersion) []NpmVersion {
	out := append([]NpmVersion(nil), versions...)
	sort.SliceStable(out, func(i, j int) bool {
		return CompareVersions(out[i].Version, out[j].Version) > 0
	})
	return out
}
