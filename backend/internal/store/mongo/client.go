package mongostore

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	ColRegistries   = "registries"
	ColBlobs        = "blobs"
	ColArtifactRefs = "artifact_refs"
	ColUsers        = "users"
	ColTokens       = "tokens"
	ColSessions     = "sessions"
	ColStatistics   = "statistics"
	ColAuditLogs    = "audit_logs"
	ColSettings     = "settings"
)

// Store implements all shared persistence interfaces on MongoDB.
type Store struct {
	client *mongo.Client
	db     *mongo.Database
}

// Connect establishes a MongoDB connection, verifies connectivity, and ensures indexes on shared collections.
func Connect(ctx context.Context, uri, dbName string) (*Store, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("connect mongodb: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, nil); err != nil {
		_ = client.Disconnect(ctx)
		return nil, fmt.Errorf("ping mongodb: %w", err)
	}

	s := &Store{
		client: client,
		db:     client.Database(dbName),
	}
	if err := s.ensureIndexes(ctx); err != nil {
		_ = client.Disconnect(ctx)
		return nil, fmt.Errorf("ensure mongodb indexes: %w", err)
	}
	return s, nil
}

// Database exposes the underlying *mongo.Database for the composition root to pass to registry modules.
func (s *Store) Database() *mongo.Database {
	return s.db
}

func (s *Store) Close(ctx context.Context) error {
	return s.client.Disconnect(ctx)
}

func (s *Store) ensureIndexes(ctx context.Context) error {
	// registries: unique slug
	if _, err := s.db.Collection(ColRegistries).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "slug", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "type", Value: 1}}},
	}); err != nil {
		return err
	}

	// artifact_refs: unique (registry_id, artifact_key), digest lookup, LRU eviction lookup
	if _, err := s.db.Collection(ColArtifactRefs).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "artifact_key", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "digest", Value: 1}}},
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "origin", Value: 1}, {Key: "protected", Value: 1}, {Key: "last_accessed_at", Value: 1}}},
	}); err != nil {
		return err
	}

	// users: unique username
	if _, err := s.db.Collection(ColUsers).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "username", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return err
	}

	// sessions: unique token_hash, TTL on expires_at
	if _, err := s.db.Collection(ColSessions).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "token_hash", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)},
		{Keys: bson.D{{Key: "user_id", Value: 1}}},
	}); err != nil {
		return err
	}

	// tokens: unique token_hash, registry filter
	if _, err := s.db.Collection(ColTokens).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "token_hash", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "registry_id", Value: 1}}},
	}); err != nil {
		return err
	}

	// statistics: registry_id + bucket_date
	if _, err := s.db.Collection(ColStatistics).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "bucket_date", Value: -1}},
	}); err != nil {
		return err
	}

	// audit_logs: timestamp desc, registry_id filter
	if _, err := s.db.Collection(ColAuditLogs).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "timestamp", Value: -1}}},
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "timestamp", Value: -1}}},
	}); err != nil {
		return err
	}

	return nil
}
