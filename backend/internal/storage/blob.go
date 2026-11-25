package storage

import (
	"context"
	"errors"
	"time"
)

const (
	OriginHosted = "hosted"
	OriginCached = "cached"
)

var (
	ErrBlobNotFound   = errors.New("blob not found")
	ErrRefNotFound    = errors.New("artifact reference not found")
	ErrDigestMismatch = errors.New("blob digest mismatch")
	ErrQuotaExceeded  = errors.New("registry storage quota exceeded")
	ErrSizeExceeded   = errors.New("artifact size exceeds limit")
	ErrInvalidDigest  = errors.New("invalid sha256 digest")
)

// Blob represents a deduplicated binary blob stored on disk and tracked in `blobs`.
type Blob struct {
	Digest         string    `json:"digest" bson:"_id"`
	SizeBytes      int64     `json:"size_bytes" bson:"size_bytes"`
	CreatedAt      time.Time `json:"created_at" bson:"created_at"`
	LastAccessedAt time.Time `json:"last_accessed_at" bson:"last_accessed_at"`
}

// ArtifactRef links a registry instance's opaque artifact key to a content-addressed blob.
// Stored in the shared `artifact_refs` collection.
type ArtifactRef struct {
	ID             string    `json:"id" bson:"_id"`
	RegistryID     string    `json:"registry_id" bson:"registry_id"`
	ArtifactKey    string    `json:"artifact_key" bson:"artifact_key"` // Opaque registry-defined key
	Digest         string    `json:"digest" bson:"digest"`
	SizeBytes      int64     `json:"size_bytes" bson:"size_bytes"`
	Origin         string    `json:"origin" bson:"origin"`       // "hosted" or "cached"
	Protected      bool      `json:"protected" bson:"protected"` // True for hosted artifacts; immune to cache eviction
	CreatedAt      time.Time `json:"created_at" bson:"created_at"`
	LastAccessedAt time.Time `json:"last_accessed_at" bson:"last_accessed_at"`
}

// GlobalStorageStats separates physical deduplicated disk usage from logical total usage.
type GlobalStorageStats struct {
	PhysicalBytes       int64   `json:"physical_bytes"`
	PhysicalBlobCount   int64   `json:"physical_blob_count"`
	LogicalBytes        int64   `json:"logical_bytes"`
	LogicalRefCount     int64   `json:"logical_ref_count"`
	DeduplicationSaved  int64   `json:"deduplication_saved_bytes"`
	DeduplicationRatio  float64 `json:"deduplication_ratio"`
}

// RegistryStorageStats tracks logical storage usage for a single registry instance.
type RegistryStorageStats struct {
	RegistryID    string `json:"registry_id"`
	LogicalBytes  int64  `json:"logical_bytes"`
	HostedBytes   int64  `json:"hosted_bytes"`
	CachedBytes   int64  `json:"cached_bytes"`
	TotalRefs     int64  `json:"total_refs"`
	HostedRefs    int64  `json:"hosted_refs"`
	CachedRefs    int64  `json:"cached_refs"`
}

// RefStore persists blob and artifact reference metadata (`blobs` and `artifact_refs` collections).
type RefStore interface {
	UpsertBlob(ctx context.Context, blob *Blob) error
	GetBlob(ctx context.Context, digest string) (*Blob, error)
	DeleteBlob(ctx context.Context, digest string) error
	TouchBlob(ctx context.Context, digest string, ts time.Time) error

	UpsertArtifactRef(ctx context.Context, ref *ArtifactRef) error
	GetArtifactRef(ctx context.Context, registryID, artifactKey string) (*ArtifactRef, error)
	DeleteArtifactRef(ctx context.Context, registryID, artifactKey string) (*ArtifactRef, error)
	DeleteRegistryRefs(ctx context.Context, registryID string) ([]*ArtifactRef, error)
	CountRefsForDigest(ctx context.Context, digest string) (int64, error)
	TouchArtifactRef(ctx context.Context, registryID, artifactKey string, ts time.Time) error

	// ListEvictableCachedRefs returns non-protected cached refs for a registry ordered by LastAccessedAt ascending (LRU).
	ListEvictableCachedRefs(ctx context.Context, registryID string) ([]*ArtifactRef, error)

	GetGlobalStorageStats(ctx context.Context) (GlobalStorageStats, error)
	GetRegistryStorageStats(ctx context.Context, registryID string) (RegistryStorageStats, error)
}
