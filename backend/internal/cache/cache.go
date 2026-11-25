package cache

import (
	"context"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/amkargar/mirror-registry/backend/internal/storage"
)

// Coordinator provides ecosystem-agnostic concurrent download deduplication
// and cache lifecycle eviction over shared blob storage.
type Coordinator struct {
	group   singleflight.Group
	storage *storage.Service
}

func NewCoordinator(store *storage.Service) *Coordinator {
	return &Coordinator{storage: store}
}

// Do deduplicates concurrent fetches for the same registry + key.
func (c *Coordinator) Do(registryID, key string, fn func() (any, error)) (any, error, bool) {
	return c.group.Do(registryID+"::"+key, fn)
}

// EvictRegistry applies TTL and MaxSize LRU eviction to non-protected cached blobs of a registry.
func (c *Coordinator) EvictRegistry(ctx context.Context, registryID string, archiveTTLSeconds int64, maxCacheBytes int64) (storage.EvictResult, error) {
	if c.storage == nil {
		return storage.EvictResult{}, nil
	}
	var maxAge time.Duration
	if archiveTTLSeconds > 0 {
		maxAge = time.Duration(archiveTTLSeconds) * time.Second
	}
	return c.storage.EvictCachedArtifacts(ctx, registryID, storage.EvictOptions{
		MaxAge:        maxAge,
		MaxCacheBytes: maxCacheBytes,
	})
}

// PurgeRegistry removes all non-protected cached blobs of a registry without touching hosted artifacts.
func (c *Coordinator) PurgeRegistry(ctx context.Context, registryID string) (storage.EvictResult, error) {
	if c.storage == nil {
		return storage.EvictResult{}, nil
	}
	return c.storage.EvictCachedArtifacts(ctx, registryID, storage.EvictOptions{
		PurgeAll: true,
	})
}
