package flutter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// VersionRegistry documents every Flutter SDK release known to this mirror together with the
// FLUTTER_STORAGE_BASE_URL storage settings the Flutter CLI resolves for each version.
//
// It is derived from the upstream releases_*.json manifests (already cached & rewritten by this
// mirror), so admins get one live catalogue answering: "which Flutter versions can I install
// through this server, and where do their SDK / engine / Dart artifacts come from?".
type VersionRegistry struct {
	RegistryID      string           `json:"registry_id"`
	RegistrySlug    string           `json:"registry_slug"`
	StorageName     string           `json:"storage_name"`
	UpstreamURL     string           `json:"upstream_url"`
	ReleasesBaseURL string           `json:"releases_base_url"`
	BasePath        string           `json:"base_path"`
	GeneratedAt     time.Time        `json:"generated_at"`
	ExpiresAt       time.Time        `json:"expires_at"`
	CurrentRelease  string           `json:"current_release,omitempty"`
	Channels        []VersionChannel `json:"channels"`
	Storage         []StorageChannel `json:"storage"`
	Versions        []FlutterVersion `json:"versions"`
	TotalVersions   int              `json:"total_versions"`
	Warnings        []string         `json:"warnings,omitempty"`
}

// VersionChannel maps a release channel (stable/beta/dev) to its current version and manifest URL.
type VersionChannel struct {
	Name           string `json:"name"`
	CurrentRelease string `json:"current_release,omitempty"`
	ManifestURL    string `json:"manifest_url,omitempty"`
	ManagedBy      string `json:"managed_by,omitempty"` // upstream-manifest or admin-override
}

// StorageChannel is the public (credential-free) view of a FLUTTER_STORAGE_BASE_URL category.
type StorageChannel struct {
	Name               string   `json:"name"`
	Enabled            bool     `json:"enabled"`
	UpstreamURL        string   `json:"upstream_url"`
	MirrorURL          string   `json:"mirror_url"`
	AllowedPrefixes    []string `json:"allowed_prefixes"`
	RewritesManifests  bool     `json:"rewrites_manifests"`
	ManifestTTLSeconds int      `json:"manifest_ttl_seconds"`
	InheritsUpstream   bool     `json:"inherits_upstream"`
}

// FlutterVersion is the per-version registry entry.
type FlutterVersion struct {
	Version         string            `json:"version"`
	Channel         string            `json:"channel"`
	DartSDKVersion  string            `json:"dart_sdk_version,omitempty"`
	ReleaseDate     string            `json:"release_date,omitempty"`
	Hash            string            `json:"hash,omitempty"`
	Archived        bool              `json:"archived"`
	Downloads       []VersionDownload `json:"downloads"`
	Storage         []VersionStorage  `json:"storage"`
	SDKDownloadURL  string            `json:"sdk_download_url,omitempty"`
	DartSDKURL      string            `json:"dart_sdk_url,omitempty"`
	EngineManifest  string            `json:"engine_manifest_url,omitempty"`
	RegistryEntryAt string            `json:"registry_entry_url"`
}

// VersionDownload records one platform archive for a Flutter version.
type VersionDownload struct {
	Platform  string `json:"platform"`
	Archive   string `json:"archive"`
	MirrorURL string `json:"mirror_url"`
	SHA256    string `json:"sha256,omitempty"`
}

// VersionStorage explains where a Flutter version's SDK / engine / Dart archives are served from.
type VersionStorage struct {
	Category      string `json:"category"`
	Path          string `json:"path"`
	UpstreamURL   string `json:"upstream_url"`
	MirrorURL     string `json:"mirror_url"`
	InheritedFrom string `json:"inherited_from,omitempty"` // registry when the category inherits the registry upstream
}

// flutterReleasesManifest mirrors the upstream releases_<platform>.json schema.
type flutterReleasesManifest struct {
	BaseURL        string                   `json:"base_url"`
	CurrentRelease flutterManifestCurrent   `json:"current_release"`
	Releases       []flutterManifestRelease `json:"releases"`
}

type flutterManifestCurrent struct {
	Beta   string `json:"beta"`
	Dev    string `json:"dev"`
	Stable string `json:"stable"`
}

type flutterManifestRelease struct {
	Hash           string `json:"hash"`
	Channel        string `json:"channel"`
	Version        string `json:"version"`
	DartSDKVersion string `json:"dart_sdk_version"`
	ReleaseDate    string `json:"release_date"`
	Archive        string `json:"archive"`
	Archived       bool   `json:"archived"`
}

var errVersionRegistryUnavailable = errors.New("flutter version registry unavailable: no upstream release manifests could be fetched")

// registryPlatforms are the release manifests aggregated into the version registry.
// Linux covers x64 + arm64 SDK archives, macOS covers x64 + arm64, Windows covers x64.
var registryPlatforms = []string{"linux", "macos", "windows"}

// releasesBaseURL resolves where release manifests live.
func (r *FlutterRegistry) releasesBaseURL() string {
	if r.config().ReleasesBaseURL != "" {
		return strings.TrimRight(r.config().ReleasesBaseURL, "/")
	}
	return strings.TrimRight(r.inst.UpstreamURL, "/") + "/flutter_infra_release/releases"
}

// fetchReleasesManifest downloads and decodes an upstream releases_<platform>.json manifest using
// the shared upstream client with per-registry auth / proxy / TLS / timeout settings applied.
func (r *FlutterRegistry) fetchReleasesManifest(ctx context.Context, platform string) (*flutterReleasesManifest, error) {
	cleanPath := "flutter_infra_release/releases/releases_" + platform + ".json"
	rec, _, err := r.fetchAndCacheFromUpstream(ctx, cleanPath, true)
	if err != nil {
		cached, cerr := r.store.GetArtifact(ctx, r.inst.ID, cleanPath)
		if cerr != nil || cached == nil {
			return nil, err
		}
		rec = cached
	}
	if rec == nil || r.deps.Storage == nil {
		return nil, fmt.Errorf("release manifest %s is unavailable", cleanPath)
	}
	f, _, openErr := r.deps.Storage.OpenArtifact(ctx, r.inst.ID, rec.Path)
	if openErr != nil {
		return nil, openErr
	}
	defer f.Close()
	raw, readErr := io.ReadAll(io.LimitReader(f, 32*1024*1024))
	if readErr != nil {
		return nil, fmt.Errorf("read releases_%s.json: %w", platform, readErr)
	}
	var manifest flutterReleasesManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("decode releases_%s.json: %w", platform, err)
	}
	return &manifest, nil
}

var platformOrder = []string{"linux", "linux-arm64", "macos", "macos-arm64", "windows"}

func platformRank(p string) int {
	switch p {
	case "linux":
		return 0
	case "linux-arm64", "linux_arm64":
		return 1
	case "macos":
		return 2
	case "macos-arm64", "macos_arm64":
		return 3
	case "windows":
		return 4
	}
	return 100
}

// platformFromArchive extracts the platform folder from an archive path such as
// "stable/linux/flutter_linux_3.27.1-stable.tar.xz" or "stable/linux-arm64/...".
func platformFromArchive(archive string) string {
	parts := strings.Split(strings.Trim(archive, "/"), "/")
	for _, p := range parts {
		if platformRank(p) < 100 {
			return p
		}
	}
	return ""
}

func channelRank(name string) int {
	switch name {
	case "stable":
		return 0
	case "beta":
		return 1
	case "dev":
		return 2
	}
	return 3
}

// buildVersionRegistry aggregates the upstream release manifests into the per-version registry index.
func (r *FlutterRegistry) buildVersionRegistry(ctx context.Context, mirrorBaseURL string) (*VersionRegistry, error) {
	now := time.Now().UTC()
	out := &VersionRegistry{
		RegistryID:      r.inst.ID,
		RegistrySlug:    r.inst.Slug,
		StorageName:     r.config().StorageName,
		UpstreamURL:     r.inst.UpstreamURL,
		ReleasesBaseURL: r.releasesBaseURL(),
		BasePath:        mirrorBaseURL,
		GeneratedAt:     now,
		ExpiresAt:       now.Add(time.Duration(r.config().RegistryTTLSeconds) * time.Second),
	}

	versions := make(map[string]*FlutterVersion)
	channelCurrent := make(map[string]string)
	fetched := 0

	for _, platform := range registryPlatforms {
		manifest, err := r.fetchReleasesManifest(ctx, platform)
		if err != nil {
			out.Warnings = append(out.Warnings, err.Error())
			continue
		}
		fetched++

		manifestManifestURL := mirrorBaseURL + "/flutter_infra_release/releases/releases_" + platform + ".json"
		for name, current := range map[string]string{
			"stable": manifest.CurrentRelease.Stable,
			"beta":   manifest.CurrentRelease.Beta,
			"dev":    manifest.CurrentRelease.Dev,
		} {
			if current == "" {
				continue
			}
			if _, ok := channelCurrent[name]; !ok {
				channelCurrent[name] = current
			}
			out.Channels = upsertChannel(out.Channels, VersionChannel{
				Name:           name,
				CurrentRelease: current,
				ManifestURL:    manifestManifestURL,
				ManagedBy:      "upstream-manifest",
			})
		}

		for _, rel := range manifest.Releases {
			if strings.TrimSpace(rel.Version) == "" {
				continue
			}
			entry, ok := versions[rel.Version]
			if !ok {
				entry = &FlutterVersion{
					Version:        rel.Version,
					Channel:        rel.Channel,
					DartSDKVersion: rel.DartSDKVersion,
					ReleaseDate:    rel.ReleaseDate,
					Hash:           rel.Hash,
					Archived:       rel.Archived,
				}
				versions[rel.Version] = entry
			}
			if entry.Channel == "" {
				entry.Channel = rel.Channel
			}
			if entry.DartSDKVersion == "" {
				entry.DartSDKVersion = rel.DartSDKVersion
			}
			if entry.Hash == "" {
				entry.Hash = rel.Hash
			}

			archive := strings.TrimLeft(rel.Archive, "/")
			if archive == "" {
				continue
			}
			platformName := platformFromArchive(archive)
			if platformName == "" {
				platformName = platform
			}
			entry.Downloads = append(entry.Downloads, VersionDownload{
				Platform:  platformName,
				Archive:   archive,
				MirrorURL: mirrorBaseURL + "/flutter_infra_release/releases/" + archive,
			})
		}
	}

	out.Storage = r.storageChannels(mirrorBaseURL)
	if fetched == 0 {
		if len(out.Warnings) == 0 {
			out.Warnings = append(out.Warnings, errVersionRegistryUnavailable.Error())
		}
		return out, nil
	}

	list := make([]FlutterVersion, 0, len(versions))
	for _, entry := range versions {
		v := *entry
		sort.SliceStable(v.Downloads, func(i, j int) bool {
			ri, rj := platformRank(v.Downloads[i].Platform), platformRank(v.Downloads[j].Platform)
			if ri != rj {
				return ri < rj
			}
			return v.Downloads[i].Archive < v.Downloads[j].Archive
		})
		v.Storage = r.versionStorageTargets(mirrorBaseURL, v.Downloads)
		v.RegistryEntryAt = mirrorBaseURL + "/api/registry/versions/" + v.Version
		v.SDKDownloadURL = pickSDKDownload(v.Downloads)
		v.DartSDKURL = mirrorBaseURL + "/dart-archive/channels/stable/release/" + v.Version + "/sdk/dartsdk-linux-x64-release.zip"
		v.EngineManifest = mirrorBaseURL + "/flutter_infra/flutter/engine_manifest.json"
		list = append(list, v)
	}

	if len(list) == 0 {
		if len(out.Warnings) == 0 {
			out.Warnings = append(out.Warnings, errVersionRegistryUnavailable.Error())
		}
		return out, nil
	}

	sort.SliceStable(list, func(i, j int) bool {
		if c := compareFlutterVersions(list[i].Version, list[j].Version); c != 0 {
			return c > 0
		}
		return list[i].Channel < list[j].Channel
	})
	out.Versions = list
	out.TotalVersions = len(list)
	out.CurrentRelease = channelCurrent["stable"]
	if out.CurrentRelease == "" {
		out.CurrentRelease = list[0].Version
	}

	sort.SliceStable(out.Channels, func(i, j int) bool {
		return channelRank(out.Channels[i].Name) < channelRank(out.Channels[j].Name)
	})
	return out, nil
}

func pickSDKDownload(downloads []VersionDownload) string {
	for _, d := range downloads {
		if d.Platform == "linux" {
			return d.MirrorURL
		}
	}
	if len(downloads) > 0 {
		return downloads[0].MirrorURL
	}
	return ""
}

func upsertChannel(list []VersionChannel, next VersionChannel) []VersionChannel {
	for i := range list {
		if list[i].Name == next.Name {
			return list
		}
	}
	return append(list, next)
}

// versionStorageTargets explains where this version's SDK, engine and Dart archives are served.
func (r *FlutterRegistry) versionStorageTargets(mirrorBaseURL string, downloads []VersionDownload) []VersionStorage {
	targets := make([]VersionStorage, 0, 3)
	push := func(path string) {
		channel, ok := r.config().resolveCategory(path)
		upstreamURL := r.config().categoryUpstreamURL(channel, r.inst.UpstreamURL)
		target := VersionStorage{
			Category:    channel.Name,
			Path:        path,
			UpstreamURL: upstreamURL,
			MirrorURL:   mirrorBaseURL + "/" + path,
		}
		if !ok {
			target.Category = categoryForPath(path)
		}
		if channel.UpstreamURL == "" {
			target.InheritedFrom = "registry"
		}
		targets = append(targets, target)
	}
	for _, d := range downloads {
		if d.Platform == "linux" {
			push("flutter_infra_release/releases/" + d.Archive)
			break
		}
	}
	push("flutter_infra/flutter/engine_manifest.json")
	push("dart-archive/channels/stable/release/latest/release-linux-x64.json")
	return targets
}

// storageChannels exposes the effective per-category storage settings for the public registry view.
func (r *FlutterRegistry) storageChannels(mirrorBaseURL string) []StorageChannel {
	out := make([]StorageChannel, 0, len(r.config().DownloadStorage))
	for _, d := range r.config().DownloadStorage {
		out = append(out, StorageChannel{
			Name:               d.Name,
			Enabled:            d.isEnabled(),
			UpstreamURL:        r.config().categoryUpstreamURL(d, r.inst.UpstreamURL),
			MirrorURL:          mirrorBaseURL + "/" + d.Name,
			AllowedPrefixes:    d.prefixes(),
			RewritesManifests:  r.config().categoryRewriteManifests(d),
			ManifestTTLSeconds: r.config().categoryManifestTTL(d),
			InheritsUpstream:   d.UpstreamURL == "",
		})
	}
	return out
}

// compareFlutterVersions compares Flutter version strings such as "3.27.1" or "3.27.0-0.1.pre".
func compareFlutterVersions(a, b string) int {
	pa, pb := versionSegments(a), versionSegments(b)
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var av, bv int
		if i < len(pa) {
			av = pa[i]
		}
		if i < len(pb) {
			bv = pb[i]
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	// Pre-release builds ("3.27.0-0.1.pre") sort before their release counterpart.
	aPre, bPre := strings.Contains(a, "-"), strings.Contains(b, "-")
	switch {
	case aPre && !bPre:
		return -1
	case !aPre && bPre:
		return 1
	}
	return strings.Compare(a, b)
}

func versionSegments(v string) []int {
	base := v
	if idx := strings.IndexAny(base, "-+"); idx != -1 {
		base = base[:idx]
	}
	parts := strings.Split(base, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		for _, r := range p {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		out = append(out, n)
	}
	return out
}
