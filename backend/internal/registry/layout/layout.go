// Package layout registers repository formats whose wire protocol is a tree
// of files. Clients consume the native index and signatures, byte for byte;
// publishers prepare those files with their ecosystem's own indexing tools.
package layout

import (
	"net/http"
	"strings"

	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/registry/repository"
)

func Registrations() []registry.Registration {
	formats := []repository.Format{
		{
			Type:        "alpine",
			Name:        "Alpine (APK)",
			Description: "APKINDEX, signed indexes and .apk packages in an Alpine mirror layout.",
			Upstream:    "https://dl-cdn.alpinelinux.org/alpine",
		},
		{
			Type:        "conda",
			Name:        "Conda",
			Description: "Conda channel subdirectories, repodata and .conda/.tar.bz2 packages.",
			Upstream:    "https://repo.anaconda.com/pkgs/main",
		},
		{
			Type:        "cran",
			Name:        "CRAN (R)",
			Description: "CRAN source/binary repository trees with PACKAGES indexes.",
			Upstream:    "https://cloud.r-project.org",
		},
		{
			Type:        "debian",
			Name:        "Debian (APT)",
			Description: "APT dists and pool layouts including InRelease, by-hash and signatures.",
			Upstream:    "https://deb.debian.org/debian",
		},
		{Type: "ivy", Name: "Apache Ivy", Description: "HTTP resolver with publisher-defined Ivy descriptor and artifact patterns."},
		{
			Type:        "maven",
			Name:        "Apache Maven",
			Description: "Maven 2 layout, POMs, JARs, snapshot metadata and checksums; supports Maven deploy.",
			Upstream:    "https://repo.maven.apache.org/maven2",
		},
		{
			Type:        "nix",
			Name:        "Nix binary cache",
			Description: "nix-cache-info, signed narinfo records and NAR archives.",
			Upstream:    "https://cache.nixos.org",
		},
		{
			Type:        "pacman",
			Name:        "Arch Linux (Pacman)",
			Description: "Pacman sync databases, packages and detached signatures.",
			Upstream:    "https://geo.mirror.pkgbuild.com",
		},
		{
			Type:        "rpm",
			Name:        "RPM (YUM/DNF)",
			Description: "RPM-MD repositories with repodata, RPM packages and GPG signatures.",
			Upstream:    "https://dl.fedoraproject.org/pub/fedora/linux",
		},
		{
			Type:        "rubygems",
			Name:        "RubyGems",
			Description: "RubyGems static and Bundler compact indexes, gemspecs and .gem downloads.",
			Upstream:    "https://rubygems.org",
			Routes:      gemFiles,
		},
	}
	var regs []registry.Registration
	for _, f := range formats {
		regs = append(regs, f.Registration())
	}
	return regs
}

func gemFiles(h *repository.Repository) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The account and dependency APIs are not static index files. Returning
		// 404 lets gem clients fall back to their native compact / Marshal indexes.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		h.Files(w, r)
	})
}
