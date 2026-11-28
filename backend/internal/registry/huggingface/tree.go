package huggingface

import (
	"bytes"
	"crypto/sha1" // Git object IDs, not a security/integrity decision
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	repo "github.com/amkargar/depyard/backend/internal/registry/repository"
	"github.com/amkargar/depyard/backend/internal/storage"
)

type node struct {
	kind, name, oid string
	size            int64
	lfs             map[string]any
	children        map[string]*node
}

func gitOID(kind string, b []byte) string {
	s := sha1.New()
	fmt.Fprintf(s, "%s %d\x00", kind, len(b))
	s.Write(b)
	return hex.EncodeToString(s.Sum(nil))
}

func directory(name string) *node {
	return &node{kind: "directory", name: name, children: make(map[string]*node)}
}

func (n *node) treeID() string {
	if n.oid != "" {
		return n.oid
	}
	var names []string
	for k, v := range n.children {
		if v.kind == "directory" {
			k += "/"
		}
		names = append(names, k)
	}
	sort.Strings(names)
	var entries bytes.Buffer
	for _, key := range names {
		name := strings.TrimSuffix(key, "/")
		child := n.children[name]
		mode := "100644"
		if child.kind == "directory" {
			mode = "40000"
		}
		fmt.Fprintf(&entries, "%s %s\x00", mode, name)
		oid, _ := hex.DecodeString(child.treeID())
		entries.Write(oid)
	}
	n.oid = gitOID("tree", entries.Bytes())
	return n.oid
}

func (h *hub) tree(w http.ResponseWriter, r *http.Request, refs []*storage.ArtifactRef, prefix, folder string) {
	root := directory("")
	for _, ref := range refs {
		p := strings.TrimPrefix(repo.HostedPath(ref), prefix)
		parts := strings.Split(p, "/")
		d := root
		for i, part := range parts[:len(parts)-1] {
			if d.children[part] == nil {
				d.children[part] = directory(strings.Join(parts[:i+1], "/"))
			}
			d = d.children[part]
			if d.children == nil {
				repo.Error(w, repo.Status(409))
				return
			}
		}
		pointer := []byte(fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n", ref.Digest, ref.SizeBytes))
		d.children[parts[len(parts)-1]] = &node{
			kind: "file",
			name: p,
			oid:  gitOID("blob", pointer),
			size: ref.SizeBytes,
			lfs:  map[string]any{"oid": ref.Digest, "size": ref.SizeBytes, "pointerSize": len(pointer)},
		}
	}
	dir := root
	if folder != "" {
		for _, part := range strings.Split(folder, "/") {
			dir = dir.children[part]
			if dir == nil || dir.kind != "directory" {
				notFound(w)
				return
			}
		}
	}
	recursive := r.URL.Query().Get("recursive") == "true"
	out := []map[string]any{}
	var walk func(*node)
	walk = func(parent *node) {
		for _, n := range parent.children {
			item := map[string]any{"type": n.kind, "path": n.name, "oid": n.treeID()}
			if n.kind == "file" {
				item["size"] = n.size
				item["lfs"] = n.lfs
			}
			out = append(out, item)
			if recursive {
				walk(n)
			}
		}
	}
	walk(dir)
	sort.Slice(out, func(i, j int) bool { return out[i]["path"].(string) < out[j]["path"].(string) })
	repo.JSON(w, 200, out)
}

func (h *hub) remoteTree(w http.ResponseWriter, r *http.Request, o repo.Object) {
	o.Headers = []string{"Link"}
	f, ref, err := h.Open(r.Context(), o)
	if err != nil {
		repo.Error(w, err)
		return
	}
	defer f.Close()
	b, err := repo.Limited(f, 16<<20)
	if err != nil {
		repo.Error(w, err)
		return
	}
	var entries []map[string]any
	if json.Unmarshal(b, &entries) != nil {
		repo.Error(w, repo.Status(502))
		return
	}
	for _, e := range entries {
		delete(e, "xetHash")
	} // offer HTTP downloads, not a nonexistent local Xet service
	// Keep pagination inside the instance without copying an upstream origin or credentials.
	for _, link := range strings.Split(ref.Headers["Link"], ",") {
		target, rel, ok := strings.Cut(link, ">")
		if !ok || !strings.Contains(rel, `rel="next"`) {
			continue
		}
		u, err := url.Parse(strings.TrimPrefix(strings.TrimSpace(target), "<"))
		if err == nil && u.RawQuery != "" {
			w.Header().Set("Link", "<"+repo.Base(r)+r.URL.EscapedPath()+"?"+u.Query().Encode()+`>; rel="next"`)
			break
		}
	}
	repo.JSON(w, 200, entries)
}
