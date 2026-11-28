// Package composer serves Composer v2 / Satis repositories.
package composer

import (
	"encoding/json"
	"strings"

	"github.com/amkargar/depyard/backend/internal/registry"
	repo "github.com/amkargar/depyard/backend/internal/registry/repository"
)

func Registration() registry.Registration {
	index := repo.LinkedIndex{Match: func(p string) bool {
		return p == "/packages.json" || strings.HasPrefix(p, "/p2/") && strings.HasSuffix(p, ".json")
	}, MediaType: "application/json", MaxBytes: 32 << 20, Transform: transform}
	return (repo.Format{
		Type:        "composer",
		Name:        "Composer (PHP)",
		Description: "Composer v2 metadata and distributions; host a prepared Satis repository.",
		Upstream:    "https://repo.packagist.org",
		Routes:      index.Routes,
	}).Registration()
}

func transform(data []byte, rewrite func(string, bool) string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	for _, k := range []string{"metadata-url", "providers-url", "list", "providers-api"} {
		if s, ok := doc[k].(string); ok {
			doc[k] = rewrite(s, false)
		}
	}
	delete(doc, "notify")
	delete(doc, "notify-batch")
	patch := func(v any) {
		pkg, _ := v.(map[string]any)
		dist, _ := pkg["dist"].(map[string]any)
		if s, ok := dist["url"].(string); ok {
			dist["url"] = rewrite(s, true)
		}
	}
	packages, _ := doc["packages"].(map[string]any)
	for _, versions := range packages {
		switch v := versions.(type) {
		case []any:
			for _, pkg := range v {
				patch(pkg)
			}
		case map[string]any:
			for _, pkg := range v {
				patch(pkg)
			}
		}
	}
	return json.Marshal(doc)
}
