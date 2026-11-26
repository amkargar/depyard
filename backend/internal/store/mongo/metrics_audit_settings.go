package mongostore

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/amkargar/depyard/backend/internal/admin"
	"github.com/amkargar/depyard/backend/internal/audit"
	"github.com/amkargar/depyard/backend/internal/metrics"
)

// --- Metrics Store (`statistics` collection) ---

func (s *Store) IncrementBucket(ctx context.Context, rec *metrics.BucketRecord) error {
	_, err := s.db.Collection(ColStatistics).UpdateOne(
		ctx,
		bson.M{"_id": rec.ID},
		bson.M{
			"$setOnInsert": bson.M{
				"_id":         rec.ID,
				"registry_id": rec.RegistryID,
				"bucket_date": rec.BucketDate,
			},
			"$inc": bson.M{
				"downloads":       rec.Downloads,
				"uploads":         rec.Uploads,
				"bytes_served":    rec.BytesServed,
				"bytes_received":  rec.BytesReceived,
				"cache_hits":      rec.CacheHits,
				"cache_misses":    rec.CacheMisses,
				"upstream_errors": rec.UpstreamErrors,
			},
			"$set": bson.M{
				"updated_at": rec.UpdatedAt,
			},
		},
		options.UpdateOne().SetUpsert(true),
	)
	return err
}

func (s *Store) GetRegistryTotals(ctx context.Context, registryID string) (metrics.BucketRecord, error) {
	pipe := mongo.Pipeline{
		{{Key: "$match", Value: bson.D{{Key: "registry_id", Value: registryID}}}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$registry_id"},
			{Key: "downloads", Value: bson.D{{Key: "$sum", Value: "$downloads"}}},
			{Key: "uploads", Value: bson.D{{Key: "$sum", Value: "$uploads"}}},
			{Key: "bytes_served", Value: bson.D{{Key: "$sum", Value: "$bytes_served"}}},
			{Key: "bytes_received", Value: bson.D{{Key: "$sum", Value: "$bytes_received"}}},
			{Key: "cache_hits", Value: bson.D{{Key: "$sum", Value: "$cache_hits"}}},
			{Key: "cache_misses", Value: bson.D{{Key: "$sum", Value: "$cache_misses"}}},
			{Key: "upstream_errors", Value: bson.D{{Key: "$sum", Value: "$upstream_errors"}}},
		}}},
	}
	cur, err := s.db.Collection(ColStatistics).Aggregate(ctx, pipe)
	if err != nil {
		return metrics.BucketRecord{RegistryID: registryID}, err
	}
	defer cur.Close(ctx)

	var rows []metrics.BucketRecord
	if err := cur.All(ctx, &rows); err != nil {
		return metrics.BucketRecord{RegistryID: registryID}, err
	}
	if len(rows) == 0 {
		return metrics.BucketRecord{RegistryID: registryID}, nil
	}
	rows[0].RegistryID = registryID
	return rows[0], nil
}

func (s *Store) GetAllRegistryTotals(ctx context.Context) (map[string]metrics.BucketRecord, error) {
	pipe := mongo.Pipeline{
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$registry_id"},
			{Key: "downloads", Value: bson.D{{Key: "$sum", Value: "$downloads"}}},
			{Key: "uploads", Value: bson.D{{Key: "$sum", Value: "$uploads"}}},
			{Key: "bytes_served", Value: bson.D{{Key: "$sum", Value: "$bytes_served"}}},
			{Key: "bytes_received", Value: bson.D{{Key: "$sum", Value: "$bytes_received"}}},
			{Key: "cache_hits", Value: bson.D{{Key: "$sum", Value: "$cache_hits"}}},
			{Key: "cache_misses", Value: bson.D{{Key: "$sum", Value: "$cache_misses"}}},
			{Key: "upstream_errors", Value: bson.D{{Key: "$sum", Value: "$upstream_errors"}}},
		}}},
	}
	cur, err := s.db.Collection(ColStatistics).Aggregate(ctx, pipe)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var rows []struct {
		RegistryID     string `bson:"_id"`
		Downloads      int64  `bson:"downloads"`
		Uploads        int64  `bson:"uploads"`
		BytesServed    int64  `bson:"bytes_served"`
		BytesReceived  int64  `bson:"bytes_received"`
		CacheHits      int64  `bson:"cache_hits"`
		CacheMisses    int64  `bson:"cache_misses"`
		UpstreamErrors int64  `bson:"upstream_errors"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make(map[string]metrics.BucketRecord, len(rows))
	for _, r := range rows {
		out[r.RegistryID] = metrics.BucketRecord{
			RegistryID:     r.RegistryID,
			Downloads:      r.Downloads,
			Uploads:        r.Uploads,
			BytesServed:    r.BytesServed,
			BytesReceived:  r.BytesReceived,
			CacheHits:      r.CacheHits,
			CacheMisses:    r.CacheMisses,
			UpstreamErrors: r.UpstreamErrors,
		}
	}
	return out, nil
}

func (s *Store) ListRecentBuckets(ctx context.Context, registryID string, days int) ([]*metrics.BucketRecord, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
	filter := bson.M{"bucket_date": bson.M{"$gte": cutoff}}
	if registryID != "" {
		filter["registry_id"] = registryID
	}
	cur, err := s.db.Collection(ColStatistics).Find(
		ctx,
		filter,
		options.Find().SetSort(bson.D{{Key: "bucket_date", Value: 1}}),
	)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var out []*metrics.BucketRecord
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) DeleteRegistryStats(ctx context.Context, registryID string) error {
	_, err := s.db.Collection(ColStatistics).DeleteMany(ctx, bson.M{"registry_id": registryID})
	return err
}

// --- Audit Store (`audit_logs` collection) ---

func (s *Store) InsertAuditLog(ctx context.Context, entry *audit.Entry) error {
	_, err := s.db.Collection(ColAuditLogs).InsertOne(ctx, entry)
	return err
}

func (s *Store) ListAuditLogs(ctx context.Context, q audit.Query) ([]*audit.Entry, int64, error) {
	filter := bson.M{}
	if q.RegistryID != "" {
		filter["registry_id"] = q.RegistryID
	}
	if q.Action != "" {
		filter["action"] = q.Action
	}
	if q.ActorName != "" {
		filter["actor_name"] = q.ActorName
	}

	col := s.db.Collection(ColAuditLogs)
	total, err := col.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "timestamp", Value: -1}}).
		SetSkip(int64(q.Offset)).
		SetLimit(int64(q.Limit))
	cur, err := col.Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close(ctx)

	var out []*audit.Entry
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// --- Settings Store (`settings` collection) ---

func (s *Store) GetSettings(ctx context.Context) (*admin.PlatformSettings, error) {
	var st admin.PlatformSettings
	err := s.db.Collection(ColSettings).FindOne(ctx, bson.M{"_id": "global"}).Decode(&st)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Store) SaveSettings(ctx context.Context, st *admin.PlatformSettings) error {
	st.ID = "global"
	st.UpdatedAt = time.Now().UTC()
	_, err := s.db.Collection(ColSettings).ReplaceOne(
		ctx,
		bson.M{"_id": "global"},
		st,
		options.Replace().SetUpsert(true),
	)
	return err
}
