package cargo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	repo "github.com/amkargar/depyard/backend/internal/registry/repository"
	"github.com/amkargar/depyard/backend/internal/storage"
)

func (h *handler) fetchCrate(ctx context.Context, name, version string) (*http.Response, error) {
	data, err := h.Bytes(ctx, repo.Object{Path: "/config.json", RemoteOnly: true}, 1<<20)
	if err != nil {
		return nil, err
	}
	var cfg struct{ DL string }
	if json.Unmarshal(data, &cfg) != nil || cfg.DL == "" {
		return nil, repo.Status(502)
	}
	dl := cfg.DL
	if !strings.Contains(dl, "{") {
		dl = strings.TrimRight(dl, "/") + "/" + name + "/" + version + "/download"
	} else {
		checksum := ""
		if strings.Contains(dl, "{sha256-checksum}") {
			checksum, err = h.upstreamChecksum(ctx, name, version)
			if err != nil {
				return nil, err
			}
		}
		dl = strings.NewReplacer("{crate}", name, "{version}", version, "{prefix}", prefix(name), "{lowerprefix}", prefix(strings.ToLower(name)), "{sha256-checksum}", checksum).Replace(dl)
	}
	return h.Request(ctx, http.MethodGet, dl, nil, nil)
}
func (h *handler) upstreamChecksum(ctx context.Context, name, version string) (string, error) {
	name = strings.ToLower(name)
	data, err := h.Bytes(ctx, repo.Object{Path: "/" + prefix(name) + "/" + name, RemoteOnly: true}, 16<<20)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var entry struct{ Vers, Cksum string }
		err = dec.Decode(&entry)
		if errors.Is(err, io.EOF) {
			return "", storage.ErrRefNotFound
		}
		if err != nil {
			return "", repo.Status(502)
		}
		if entry.Vers == version && storage.IsValidSHA256(entry.Cksum) {
			return entry.Cksum, nil
		}
	}
}
