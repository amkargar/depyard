package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"
)

// Service coordinates content-addressed blob storage, reference counting,
// per-registry quota enforcement, and safe garbage collection.
type Service struct {
	fs   *FSBlobStore
	refs RefStore
}

func NewService(fs *FSBlobStore, refs RefStore) *Service {
	return &Service{fs: fs, refs: refs}
}

type PutArtifactInput struct {
	RegistryID        string
	ArtifactKey       string
	Origin            string // OriginHosted or OriginCached
	ExpectedDigest    string // Optional expected SHA-256 hex digest
	MaxArtifactBytes  int64  // Optional per-artifact max size (0 = unlimited)
	RegistryQuota     int64  // Optional per-registry logical storage quota in bytes (0 = unlimited)
	Reader            io.Reader
}

type PutArtifactResult struct {
	Ref          *ArtifactRef
	Deduplicated bool
}

// PutArtifact writes a binary artifact to content-addressed storage and binds a registry-scoped reference.
func (s *Service) PutArtifact(ctx context.Context, in PutArtifactInput) (PutArtifactResult, error) {
	if in.RegistryID == "" || in.ArtifactKey == "" {
		return PutArtifactResult{}, fmt.Errorf("registry_id and artifact_key are required")
	}
	origin := in.Origin
	if origin != OriginHosted && origin != OriginCached {
		return PutArtifactResult{}, fmt.Errorf("invalid origin %q", origin)
	}

	// Stream and hash to content-addressed store atomically.
	wr, err := s.fs.Write(in.Reader, in.ExpectedDigest, in.MaxArtifactBytes)
	if err != nil {
		return PutArtifactResult{}, err
	}

	// Check if an existing reference for this exact key already exists (to compute net quota delta).
	var existingSize int64
	if existing, err := s.refs.GetArtifactRef(ctx, in.RegistryID, in.ArtifactKey); err == nil && existing != nil {
		existingSize = existing.SizeBytes
	}

	// Enforce per-registry logical storage quota independently of physical deduplication.
	if in.RegistryQuota > 0 {
		stats, err := s.refs.GetRegistryStorageStats(ctx, in.RegistryID)
		if err != nil {
			s.cleanupIfUnreferenced(ctx, wr.Digest)
			return PutArtifactResult{}, err
		}
		projected := stats.LogicalBytes - existingSize + wr.SizeBytes
		if projected > in.RegistryQuota {
			s.cleanupIfUnreferenced(ctx, wr.Digest)
			return PutArtifactResult{}, ErrQuotaExceeded
		}
	}

	now := time.Now().UTC()
	blob := &Blob{
		Digest:         wr.Digest,
		SizeBytes:      wr.SizeBytes,
		CreatedAt:      now,
		LastAccessedAt: now,
	}
	if err := s.refs.UpsertBlob(ctx, blob); err != nil {
		s.cleanupIfUnreferenced(ctx, wr.Digest)
		return PutArtifactResult{}, fmt.Errorf("upsert blob metadata: %w", err)
	}

	ref := &ArtifactRef{
		ID:             newID(),
		RegistryID:     in.RegistryID,
		ArtifactKey:    in.ArtifactKey,
		Digest:         wr.Digest,
		SizeBytes:      wr.SizeBytes,
		Origin:         origin,
		Protected:      origin == OriginHosted,
		CreatedAt:      now,
		LastAccessedAt: now,
	}
	if err := s.refs.UpsertArtifactRef(ctx, ref); err != nil {
		s.cleanupIfUnreferenced(ctx, wr.Digest)
		return PutArtifactResult{}, fmt.Errorf("upsert artifact ref: %w", err)
	}

	return PutArtifactResult{
		Ref:          ref,
		Deduplicated: wr.Deduplicated,
	}, nil
}

// OpenArtifact opens a streaming reader for an artifact bound to a specific registry instance.
// Callers MUST authorize access against the registry before calling OpenArtifact.
func (s *Service) OpenArtifact(ctx context.Context, registryID, artifactKey string) (*os.File, *ArtifactRef, error) {
	ref, err := s.refs.GetArtifactRef(ctx, registryID, artifactKey)
	if err != nil {
		return nil, nil, err
	}
	f, _, err := s.fs.Open(ref.Digest)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	_ = s.refs.TouchArtifactRef(ctx, registryID, artifactKey, now)
	_ = s.refs.TouchBlob(ctx, ref.Digest, now)
	return f, ref, nil
}

// GetArtifactRef returns the reference metadata for a registry artifact without opening the blob.
func (s *Service) GetArtifactRef(ctx context.Context, registryID, artifactKey string) (*ArtifactRef, error) {
	return s.refs.GetArtifactRef(ctx, registryID, artifactKey)
}

// VerifyArtifact verifies on-disk integrity for a registry's artifact reference.
func (s *Service) VerifyArtifact(ctx context.Context, registryID, artifactKey string) error {
	ref, err := s.refs.GetArtifactRef(ctx, registryID, artifactKey)
	if err != nil {
		return err
	}
	return s.fs.Verify(ref.Digest)
}

// DeleteArtifactRef removes a registry's reference and safely deletes the physical blob
// only if no other registry or artifact still references that digest.
func (s *Service) DeleteArtifactRef(ctx context.Context, registryID, artifactKey string) error {
	ref, err := s.refs.DeleteArtifactRef(ctx, registryID, artifactKey)
	if err != nil {
		return err
	}
	if ref != nil {
		s.cleanupIfUnreferenced(ctx, ref.Digest)
	}
	return nil
}

// DeleteRegistryArtifacts removes all artifact references belonging to a registry instance
// and safely garbage-collects blobs that are no longer referenced anywhere else.
func (s *Service) DeleteRegistryArtifacts(ctx context.Context, registryID string) (int, error) {
	removed, err := s.refs.DeleteRegistryRefs(ctx, registryID)
	if err != nil {
		return 0, err
	}
	seen := make(map[string]struct{}, len(removed))
	for _, ref := range removed {
		if _, ok := seen[ref.Digest]; ok {
			continue
		}
		seen[ref.Digest] = struct{}{}
		s.cleanupIfUnreferenced(ctx, ref.Digest)
	}
	return len(removed), nil
}

// EvictOptions controls proxy cache eviction. Protected/hosted artifacts are NEVER evicted.
type EvictOptions struct {
	PurgeAll      bool
	MaxAge        time.Duration // Evict cached refs older than MaxAge (0 = skip TTL eviction)
	MaxCacheBytes int64         // Evict LRU cached refs until cached bytes <= MaxCacheBytes (0 = skip size eviction)
}

// EvictResult reports what cache eviction removed.
type EvictResult struct {
	RemovedRefs  int   `json:"removed_refs"`
	FreedLogical int64 `json:"freed_logical_bytes"`
}

// EvictCachedArtifacts removes ONLY non-protected cached references for a registry instance.
// Private hosted blobs are strictly immune even if they share a physical digest with a cached blob.
func (s *Service) EvictCachedArtifacts(ctx context.Context, registryID string, opts EvictOptions) (EvictResult, error) {
	refs, err := s.refs.ListEvictableCachedRefs(ctx, registryID)
	if err != nil {
		return EvictResult{}, err
	}

	var totalCached int64
	for _, ref := range refs {
		if ref.Protected || ref.Origin != OriginCached {
			continue
		}
		totalCached += ref.SizeBytes
	}

	now := time.Now().UTC()
	var res EvictResult
	for _, ref := range refs {
		// Defensive guard: never evict hosted or protected blobs.
		if ref.Protected || ref.Origin != OriginCached {
			continue
		}
		shouldEvict := opts.PurgeAll
		if !shouldEvict && opts.MaxAge > 0 && now.Sub(ref.LastAccessedAt) > opts.MaxAge {
			shouldEvict = true
		}
		if !shouldEvict && opts.MaxCacheBytes > 0 && totalCached > opts.MaxCacheBytes {
			shouldEvict = true
		}
		if !shouldEvict {
			continue
		}

		deleted, err := s.refs.DeleteArtifactRef(ctx, registryID, ref.ArtifactKey)
		if err != nil {
			continue
		}
		if deleted != nil {
			totalCached -= deleted.SizeBytes
			res.RemovedRefs++
			res.FreedLogical += deleted.SizeBytes
			s.cleanupIfUnreferenced(ctx, deleted.Digest)
		}
	}
	return res, nil
}

func (s *Service) GetGlobalStats(ctx context.Context) (GlobalStorageStats, error) {
	return s.refs.GetGlobalStorageStats(ctx)
}

func (s *Service) GetRegistryStats(ctx context.Context, registryID string) (RegistryStorageStats, error) {
	return s.refs.GetRegistryStorageStats(ctx, registryID)
}

// cleanupIfUnreferenced acquires the per-digest lock and removes the physical blob + metadata
// strictly when zero references remain across all registries.
func (s *Service) cleanupIfUnreferenced(ctx context.Context, digest string) {
	unlock := s.fs.LockDigest(digest)
	defer unlock()

	count, err := s.refs.CountRefsForDigest(ctx, digest)
	if err != nil || count > 0 {
		return
	}
	_ = s.fs.Remove(digest)
	_ = s.refs.DeleteBlob(ctx, digest)
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
