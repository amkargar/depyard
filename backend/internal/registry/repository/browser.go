package repository

import (
	"context"
	"strings"

	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/storage"
)

// ListArtifacts exposes the stored objects (including indexes and signatures)
// to the shared admin table, without inventing ecosystem-specific package IDs.
func (r *Repository) ListArtifacts(ctx context.Context, q registry.ArtifactQuery) (registry.ArtifactPage, error) {
	page, limit := max(q.Page, 1), q.Limit
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	refs, total, err := r.deps.Storage.ListArtifactRefs(ctx, r.Instance.ID, storage.RefQuery{Contains: q.Search, Origin: q.Origin, Offset: (page - 1) * limit, Limit: limit})
	if err != nil {
		return registry.ArtifactPage{}, err
	}
	items := make([]registry.ArtifactDescriptor, 0, len(refs))
	for _, ref := range refs {
		key := strings.TrimPrefix(strings.TrimPrefix(ref.ArtifactKey, hosted), cached)
		items = append(items, registry.ArtifactDescriptor{
			ID:         ref.ID,
			RegistryID: ref.RegistryID,
			Name:       key,
			Origin:     ref.Origin,
			Digest:     ref.Digest,
			SizeBytes:  ref.SizeBytes,
			CreatedAt:  ref.CreatedAt,
			UpdatedAt:  ref.UpdatedAt,
		})
	}
	return registry.ArtifactPage{Items: items, Total: total, Page: page, Limit: limit}, nil
}
