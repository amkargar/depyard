package mongostore

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/amkargar/mirror-registry/backend/internal/storage"
)

func (s *Store) UpsertBlob(ctx context.Context, blob *storage.Blob) error {
	_, err := s.db.Collection(ColBlobs).UpdateOne(
		ctx,
		bson.M{"_id": blob.Digest},
		bson.M{
			"$setOnInsert": bson.M{
				"_id":        blob.Digest,
				"size_bytes": blob.SizeBytes,
				"created_at": blob.CreatedAt,
			},
			"$set": bson.M{
				"last_accessed_at": blob.LastAccessedAt,
			},
		},
		options.UpdateOne().SetUpsert(true),
	)
	return err
}

func (s *Store) GetBlob(ctx context.Context, digest string) (*storage.Blob, error) {
	var b storage.Blob
	err := s.db.Collection(ColBlobs).FindOne(ctx, bson.M{"_id": digest}).Decode(&b)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, storage.ErrBlobNotFound
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *Store) DeleteBlob(ctx context.Context, digest string) error {
	_, err := s.db.Collection(ColBlobs).DeleteOne(ctx, bson.M{"_id": digest})
	return err
}

func (s *Store) TouchBlob(ctx context.Context, digest string, ts time.Time) error {
	_, err := s.db.Collection(ColBlobs).UpdateOne(
		ctx,
		bson.M{"_id": digest},
		bson.M{"$set": bson.M{"last_accessed_at": ts}},
	)
	return err
}

func (s *Store) UpsertArtifactRef(ctx context.Context, ref *storage.ArtifactRef) error {
	_, err := s.db.Collection(ColArtifactRefs).UpdateOne(
		ctx,
		bson.M{"registry_id": ref.RegistryID, "artifact_key": ref.ArtifactKey},
		bson.M{
			"$setOnInsert": bson.M{
				"_id":        ref.ID,
				"created_at": ref.CreatedAt,
			},
			"$set": bson.M{
				"registry_id":      ref.RegistryID,
				"artifact_key":     ref.ArtifactKey,
				"digest":           ref.Digest,
				"size_bytes":       ref.SizeBytes,
				"origin":           ref.Origin,
				"protected":        ref.Protected,
				"last_accessed_at": ref.LastAccessedAt,
			},
		},
		options.UpdateOne().SetUpsert(true),
	)
	return err
}

func (s *Store) GetArtifactRef(ctx context.Context, registryID, artifactKey string) (*storage.ArtifactRef, error) {
	var ref storage.ArtifactRef
	err := s.db.Collection(ColArtifactRefs).FindOne(ctx, bson.M{
		"registry_id":  registryID,
		"artifact_key": artifactKey,
	}).Decode(&ref)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, storage.ErrRefNotFound
	}
	if err != nil {
		return nil, err
	}
	return &ref, nil
}

func (s *Store) DeleteArtifactRef(ctx context.Context, registryID, artifactKey string) (*storage.ArtifactRef, error) {
	var ref storage.ArtifactRef
	err := s.db.Collection(ColArtifactRefs).FindOneAndDelete(ctx, bson.M{
		"registry_id":  registryID,
		"artifact_key": artifactKey,
	}).Decode(&ref)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, storage.ErrRefNotFound
	}
	if err != nil {
		return nil, err
	}
	return &ref, nil
}

func (s *Store) DeleteRegistryRefs(ctx context.Context, registryID string) ([]*storage.ArtifactRef, error) {
	cur, err := s.db.Collection(ColArtifactRefs).Find(ctx, bson.M{"registry_id": registryID})
	if err != nil {
		return nil, err
	}
	var refs []*storage.ArtifactRef
	if err := cur.All(ctx, &refs); err != nil {
		_ = cur.Close(ctx)
		return nil, err
	}
	_ = cur.Close(ctx)

	if _, err := s.db.Collection(ColArtifactRefs).DeleteMany(ctx, bson.M{"registry_id": registryID}); err != nil {
		return nil, err
	}
	return refs, nil
}

func (s *Store) CountRefsForDigest(ctx context.Context, digest string) (int64, error) {
	return s.db.Collection(ColArtifactRefs).CountDocuments(ctx, bson.M{"digest": digest})
}

func (s *Store) TouchArtifactRef(ctx context.Context, registryID, artifactKey string, ts time.Time) error {
	_, err := s.db.Collection(ColArtifactRefs).UpdateOne(
		ctx,
		bson.M{"registry_id": registryID, "artifact_key": artifactKey},
		bson.M{"$set": bson.M{"last_accessed_at": ts}},
	)
	return err
}

func (s *Store) ListEvictableCachedRefs(ctx context.Context, registryID string) ([]*storage.ArtifactRef, error) {
	cur, err := s.db.Collection(ColArtifactRefs).Find(
		ctx,
		bson.M{
			"registry_id": registryID,
			"origin":      storage.OriginCached,
			"protected":   false,
		},
		options.Find().SetSort(bson.D{{Key: "last_accessed_at", Value: 1}}),
	)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var out []*storage.ArtifactRef
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetGlobalStorageStats(ctx context.Context) (storage.GlobalStorageStats, error) {
	var out storage.GlobalStorageStats

	// Physical bytes & count from `blobs`
	blobPipe := mongo.Pipeline{
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: nil},
			{Key: "bytes", Value: bson.D{{Key: "$sum", Value: "$size_bytes"}}},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
	}
	cur, err := s.db.Collection(ColBlobs).Aggregate(ctx, blobPipe)
	if err != nil {
		return out, err
	}
	var blobAgg []struct {
		Bytes int64 `bson:"bytes"`
		Count int64 `bson:"count"`
	}
	if err := cur.All(ctx, &blobAgg); err != nil {
		_ = cur.Close(ctx)
		return out, err
	}
	_ = cur.Close(ctx)
	if len(blobAgg) > 0 {
		out.PhysicalBytes = blobAgg[0].Bytes
		out.PhysicalBlobCount = blobAgg[0].Count
	}

	// Logical bytes & count from `artifact_refs`
	refPipe := mongo.Pipeline{
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: nil},
			{Key: "bytes", Value: bson.D{{Key: "$sum", Value: "$size_bytes"}}},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
	}
	cur2, err := s.db.Collection(ColArtifactRefs).Aggregate(ctx, refPipe)
	if err != nil {
		return out, err
	}
	var refAgg []struct {
		Bytes int64 `bson:"bytes"`
		Count int64 `bson:"count"`
	}
	if err := cur2.All(ctx, &refAgg); err != nil {
		_ = cur2.Close(ctx)
		return out, err
	}
	_ = cur2.Close(ctx)
	if len(refAgg) > 0 {
		out.LogicalBytes = refAgg[0].Bytes
		out.LogicalRefCount = refAgg[0].Count
	}

	if out.LogicalBytes > out.PhysicalBytes {
		out.DeduplicationSaved = out.LogicalBytes - out.PhysicalBytes
	}
	if out.LogicalBytes > 0 {
		out.DeduplicationRatio = float64(out.DeduplicationSaved) / float64(out.LogicalBytes)
	}
	return out, nil
}

func (s *Store) GetRegistryStorageStats(ctx context.Context, registryID string) (storage.RegistryStorageStats, error) {
	out := storage.RegistryStorageStats{RegistryID: registryID}
	pipe := mongo.Pipeline{
		{{Key: "$match", Value: bson.D{{Key: "registry_id", Value: registryID}}}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$origin"},
			{Key: "bytes", Value: bson.D{{Key: "$sum", Value: "$size_bytes"}}},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
	}
	cur, err := s.db.Collection(ColArtifactRefs).Aggregate(ctx, pipe)
	if err != nil {
		return out, err
	}
	defer cur.Close(ctx)

	var rows []struct {
		Origin string `bson:"_id"`
		Bytes  int64  `bson:"bytes"`
		Count  int64  `bson:"count"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return out, err
	}
	for _, r := range rows {
		out.LogicalBytes += r.Bytes
		out.TotalRefs += r.Count
		if r.Origin == storage.OriginHosted {
			out.HostedBytes += r.Bytes
			out.HostedRefs += r.Count
		} else {
			out.CachedBytes += r.Bytes
			out.CachedRefs += r.Count
		}
	}
	return out, nil
}
