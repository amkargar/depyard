// Package nuget implements the NuGet v3 service index and flat container.
package nuget

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/amkargar/depyard/backend/internal/registry"
	repo "github.com/amkargar/depyard/backend/internal/registry/repository"
)

var idRE = regexp.MustCompile(`^[a-z0-9_][a-z0-9_.-]{0,99}$`)
var verRE = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+){0,3}(?:-[a-z0-9.-]+)?$`)

func Registration() registry.Registration {
	return (repo.Format{
		Type:        "nuget",
		Name:        "NuGet",
		Description: "NuGet v3 flat container, restore and package push.",
		Upstream:    "https://api.nuget.org/v3/index.json",
		Routes:      routes,
	}).Registration()
}

func routes(h *repo.Repository) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/index.json" || p == "/v3/index.json" {
			if !h.ReadOK(w, r, "") {
				return
			}
			resources := []map[string]string{{"@id": repo.Base(r) + "/flat/", "@type": "PackageBaseAddress/3.0.0"}}
			if h.CanHost() {
				resources = append(resources, map[string]string{"@id": repo.Base(r) + "/push", "@type": "PackagePublish/2.0.0"})
			}
			repo.JSON(w, 200, map[string]any{"version": "3.0.0", "resources": resources})
			return
		}
		if p == "/push" && r.Method == "PUT" {
			push(h, w, r)
			return
		}
		parts := strings.Split(strings.TrimPrefix(p, "/flat/"), "/")
		if !strings.HasPrefix(p, "/flat/") || len(parts) < 2 || !idRE.MatchString(parts[0]) {
			repo.Error(w, repo.Status(404))
			return
		}
		id := parts[0]
		if !h.ReadOK(w, r, id) {
			return
		}
		obj := func(rel string) repo.Object {
			return repo.Object{Path: "/flat/" + rel, Immutable: !strings.HasSuffix(rel, "/index.json"), Fetch: func(ctx context.Context) (*http.Response, error) {
				discovery := repo.Object{Path: "/@service-index", Fetch: func(ctx context.Context) (*http.Response, error) {
					return h.Request(ctx, "GET", h.Instance.UpstreamURL, nil, nil)
				}}
				data, err := h.Bytes(ctx, discovery, 4<<20)
				if err != nil {
					return nil, err
				}
				var index struct {
					Resources []struct {
						ID   string `json:"@id"`
						Type string `json:"@type"`
					}
				}
				if json.Unmarshal(data, &index) != nil {
					return nil, repo.Status(502)
				}
				for _, res := range index.Resources {
					if strings.HasPrefix(res.Type, "PackageBaseAddress/") {
						base, _ := url.Parse(h.Instance.UpstreamURL)
						u, err := base.Parse(res.ID)
						if err != nil {
							return nil, repo.Status(502)
						}
						return h.Request(ctx, "GET", strings.TrimRight(u.String(), "/")+"/"+rel, nil, nil)
					}
				}
				return nil, repo.Status(502)
			}}
		}
		if len(parts) == 2 && parts[1] == "index.json" {
			refs, err := h.HostedRefs(r.Context(), "/flat/"+id+"/")
			if err != nil {
				repo.Error(w, err)
				return
			}
			versions := map[string]bool{}
			for _, ref := range refs {
				tail := strings.TrimPrefix(repo.HostedPath(ref), "/flat/"+id+"/")
				if strings.HasSuffix(tail, ".nupkg") {
					v, _, _ := strings.Cut(tail, "/")
					versions[v] = true
				}
			}
			if len(versions) == 0 {
				h.Serve(w, r, obj(strings.Join(parts, "/")))
				return
			}
			if h.CanProxy() {
				b, err := h.Bytes(r.Context(), obj(strings.Join(parts, "/")), 8<<20)
				if err == nil {
					var ix struct{ Versions []string }
					if json.Unmarshal(b, &ix) == nil {
						for _, v := range ix.Versions {
							versions[v] = true
						}
					}
				}
			}
			list := make([]string, 0, len(versions))
			for v := range versions {
				list = append(list, v)
			}
			sort.Strings(list)
			repo.JSON(w, 200, map[string]any{"versions": list})
			return
		}
		if len(parts) != 3 || !verRE.MatchString(parts[1]) {
			repo.Error(w, repo.Status(404))
			return
		}
		h.Serve(w, r, obj(strings.Join(parts, "/")))
	})
}
