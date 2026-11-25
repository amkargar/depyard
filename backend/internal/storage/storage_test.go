package storage_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	"github.com/amkargar/mirror-registry/backend/internal/storage"
	memstore "github.com/amkargar/mirror-registry/backend/internal/store/memory"
)

func TestDeduplicationSafeDeletionAndEvictionProtection(t *testing.T) {
	ctx := context.Background()
	fs, err := storage.NewFSBlobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSBlobStore: %v", err)
	}
	refs := memstore.New()
	svc := storage.NewService(fs, refs)

	payload := []byte("identical binary artifact payload across two registries")
	sum := sha256.Sum256(payload)
	expectedDigest := hex.EncodeToString(sum[:])

	// Registry 1 stores as protected hosted artifact
	res1, err := svc.PutArtifact(ctx, storage.PutArtifactInput{
		RegistryID:     "reg-private",
		ArtifactKey:    "secret@1.0.0",
		Origin:         storage.OriginHosted,
		ExpectedDigest: expectedDigest,
		Reader:         bytes.NewReader(payload),
	})
	if err != nil {
		t.Fatalf("PutArtifact reg-private: %v", err)
	}
	if res1.Deduplicated {
		t.Fatalf("expected first write not to be deduplicated")
	}
	if !res1.Ref.Protected {
		t.Fatalf("hosted artifact ref must be protected from cache eviction")
	}

	// Registry 2 stores exact same bytes as cached proxy artifact -> must deduplicate physically
	res2, err := svc.PutArtifact(ctx, storage.PutArtifactInput{
		RegistryID:  "reg-public",
		ArtifactKey: "mirror@1.0.0",
		Origin:      storage.OriginCached,
		Reader:      bytes.NewReader(payload),
	})
	if err != nil {
		t.Fatalf("PutArtifact reg-public: %v", err)
	}
	if !res2.Deduplicated {
		t.Fatalf("expected second identical write to be physically deduplicated")
	}
	if res2.Ref.Protected {
		t.Fatalf("cached artifact ref should not be protected")
	}

	// Verify physical vs logical storage accounting
	gStats, err := svc.GetGlobalStats(ctx)
	if err != nil {
		t.Fatalf("GetGlobalStats: %v", err)
	}
	if gStats.PhysicalBytes != int64(len(payload)) {
		t.Fatalf("expected physical bytes %d, got %d", len(payload), gStats.PhysicalBytes)
	}
	if gStats.LogicalBytes != int64(2*len(payload)) {
		t.Fatalf("expected logical bytes %d, got %d", 2*len(payload), gStats.LogicalBytes)
	}
	if gStats.DeduplicationSaved != int64(len(payload)) {
		t.Fatalf("expected dedup saved %d, got %d", len(payload), gStats.DeduplicationSaved)
	}

	// Evicting cache on reg-private MUST NOT touch hosted/protected blob
	evictPriv, err := svc.EvictCachedArtifacts(ctx, "reg-private", storage.EvictOptions{PurgeAll: true})
	if err != nil {
		t.Fatalf("EvictCachedArtifacts reg-private: %v", err)
	}
	if evictPriv.RemovedRefs != 0 {
		t.Fatalf("hosted artifact should never be evicted, removed=%d", evictPriv.RemovedRefs)
	}

	// Evicting cache on reg-public removes reg-public's ref, MUST keep physical blob alive for reg-private
	evictPub, err := svc.EvictCachedArtifacts(ctx, "reg-public", storage.EvictOptions{PurgeAll: true})
	if err != nil {
		t.Fatalf("EvictCachedArtifacts reg-public: %v", err)
	}
	if evictPub.RemovedRefs != 1 {
		t.Fatalf("expected 1 evicted cached ref, got %d", evictPub.RemovedRefs)
	}

	// reg-private's artifact must still open and verify cleanly
	f, _, err := svc.OpenArtifact(ctx, "reg-private", "secret@1.0.0")
	if err != nil {
		t.Fatalf("reg-private artifact should still exist after shared blob eviction in reg-public: %v", err)
	}
	got, _ := io.ReadAll(f)
	_ = f.Close()
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch after shared ref eviction")
	}

	// Deleting reg-private's ref should now garbage-collect the physical blob completely
	if err := svc.DeleteArtifactRef(ctx, "reg-private", "secret@1.0.0"); err != nil {
		t.Fatalf("DeleteArtifactRef reg-private: %v", err)
	}
	if _, _, err := fs.Open(expectedDigest); !errors.Is(err, storage.ErrBlobNotFound) {
		t.Fatalf("expected physical blob to be removed when ref count reaches 0, got %v", err)
	}
}

func TestIndependentQuotasAndDigestVerification(t *testing.T) {
	ctx := context.Background()
	fs, err := storage.NewFSBlobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSBlobStore: %v", err)
	}
	svc := storage.NewService(fs, memstore.New())

	// Wrong digest rejected
	_, err = svc.PutArtifact(ctx, storage.PutArtifactInput{
		RegistryID:     "reg-a",
		ArtifactKey:    "bad@1.0.0",
		Origin:         storage.OriginCached,
		ExpectedDigest: "0000000000000000000000000000000000000000000000000000000000000000",
		Reader:         bytes.NewReader([]byte("corrupted")),
	})
	if !errors.Is(err, storage.ErrDigestMismatch) {
		t.Fatalf("expected ErrDigestMismatch, got %v", err)
	}

	// Independent quota enforcement
	data := bytes.Repeat([]byte("a"), 100)
	if _, err := svc.PutArtifact(ctx, storage.PutArtifactInput{
		RegistryID:    "reg-a",
		ArtifactKey:   "item1",
		Origin:        storage.OriginHosted,
		RegistryQuota: 150,
		Reader:        bytes.NewReader(data),
	}); err != nil {
		t.Fatalf("first put should fit quota: %v", err)
	}

	// Second 100-byte write into reg-a exceeds 150-byte quota
	if _, err := svc.PutArtifact(ctx, storage.PutArtifactInput{
		RegistryID:    "reg-a",
		ArtifactKey:   "item2",
		Origin:        storage.OriginHosted,
		RegistryQuota: 150,
		Reader:        bytes.NewReader(bytes.Repeat([]byte("b"), 100)),
	}); !errors.Is(err, storage.ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded on reg-a, got %v", err)
	}

	// reg-b has its own independent quota and succeeds
	if _, err := svc.PutArtifact(ctx, storage.PutArtifactInput{
		RegistryID:    "reg-b",
		ArtifactKey:   "item1",
		Origin:        storage.OriginHosted,
		RegistryQuota: 150,
		Reader:        bytes.NewReader(bytes.Repeat([]byte("b"), 100)),
	}); err != nil {
		t.Fatalf("reg-b should not be impacted by reg-a quota: %v", err)
	}
}
