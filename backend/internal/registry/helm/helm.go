// Package helm serves classic Helm chart repositories (OCI charts use the OCI adapter).
package helm

import (
	"gopkg.in/yaml.v3"

	"github.com/amkargar/depyard/backend/internal/registry"
	repo "github.com/amkargar/depyard/backend/internal/registry/repository"
)

func Registration() registry.Registration {
	idx := repo.LinkedIndex{
		Match:     func(p string) bool { return p == "/index.yaml" },
		MediaType: "application/yaml",
		MaxBytes:  64 << 20,
		Transform: transform,
	}
	return (repo.Format{
		Type:        "helm",
		Name:        "Helm",
		Description: "Classic chart repositories: index.yaml, chart archives and provenance files.",
		Upstream:    "https://charts.helm.sh/stable",
		Routes:      idx.Routes,
	}).Registration()
}

func transform(data []byte, rewrite func(string, bool) string) ([]byte, error) {
	var idx map[string]any
	if err := yaml.Unmarshal(data, &idx); err != nil {
		return nil, err
	}
	changed := false
	entries, _ := idx["entries"].(map[string]any)
	for _, items := range entries {
		charts, _ := items.([]any)
		for _, chart := range charts {
			fields, _ := chart.(map[string]any)
			urls, _ := fields["urls"].([]any)
			for i, v := range urls {
				if s, ok := v.(string); ok {
					if link := rewrite(s, true); link != s {
						urls[i] = link
						changed = true
					}
				}
			}
		}
	}
	if changed {
		return yaml.Marshal(idx)
	}
	return data, nil
}
