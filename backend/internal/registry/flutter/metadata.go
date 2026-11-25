package flutter

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const ColFlutterArtifacts = "flutter_artifacts"

var ErrArtifactNotFound = errors.New("flutter artifact not found")

// ArtifactRecord persists metadata for a cached Flutter storage object scoped strictly to RegistryID.
type ArtifactRecord struct {
	ID           string     `json:"id" bson:"_id"`
	RegistryID   string     `json:"registry_id" bson:"registry_id"`
	Path         string     `json:"path" bson:"path"`
	Category     string     `json:"category" bson:"category"`
	ContentType  string     `json:"content_type" bson:"content_type"`
	ETag         string     `json:"etag,omitempty" bson:"etag,omitempty"`
	LastModified string     `json:"last_modified,omitempty" bson:"last_modified,omitempty"`
	Digest       string     `json:"digest" bson:"digest"`
	SizeBytes    int64      `json:"size_bytes" bson:"size_bytes"`
	IsManifest   bool       `json:"is_manifest" bson:"is_manifest"`
	UpstreamURL  string     `json:"upstream_url,omitempty" bson:"upstream_url,omitempty"`
	Downloads    int64      `json:"downloads" bson:"downloads"`
	CachedAt     time.Time  `json:"cached_at" bson:"cached_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty" bson:"expires_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at" bson:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at" bson:"updated_at"`
}

// ListQuery filters cached Flutter artifacts for Admin browsing and the Flutter Storage Explorer.
type ListQuery struct {
	Search   string
	Category string
	Page     int
	Limit    int
}

// MetadataStore persists Flutter Storage artifact metadata in dedicated registry_id-scoped storage.
type MetadataStore interface {
	UpsertArtifact(ctx context.Context, rec *ArtifactRecord) error
	GetArtifact(ctx context.Context, registryID, cleanPath string) (*ArtifactRecord, error)
	ListArtifacts(ctx context.Context, registryID string, q ListQuery) ([]*ArtifactRecord, int64, error)
	DeleteArtifact(ctx context.Context, registryID, cleanPath string) error
	IncrementDownloads(ctx context.Context, registryID, cleanPath string) error
	DeleteRegistryMetadata(ctx context.Context, registryID string) error
}

func docID(registryID, cleanPath string) string {
	return registryID + ":" + cleanPath
}

func categoryForPath(cleanPath string) string {
	if idx := strings.IndexByte(cleanPath, '/'); idx != -1 {
		return cleanPath[:idx]
	}
	return cleanPath
}

// --- MongoDB MetadataStore ---

type MongoStore struct {
	col *mongo.Collection
}

func NewMongoStore(ctx context.Context, db *mongo.Database) (*MongoStore, error) {
	col := db.Collection(ColFlutterArtifacts)
	_, err := col.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "registry_id", Value: 1}, {Key: "path", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{
			Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "category", Value: 1}, {Key: "updated_at", Value: -1}},
		},
	})
	if err != nil {
		return nil, err
	}
	return &MongoStore{col: col}, nil
}

func (s *MongoStore) UpsertArtifact(ctx context.Context, rec *ArtifactRecord) error {
	if rec.ID == "" {
		rec.ID = docID(rec.RegistryID, rec.Path)
	}
	_, err := s.col.ReplaceOne(ctx, bson.M{"_id": rec.ID}, rec, options.Replace().SetUpsert(true))
	return err
}

func (s *MongoStore) GetArtifact(ctx context.Context, registryID, cleanPath string) (*ArtifactRecord, error) {
	var rec ArtifactRecord
	err := s.col.FindOne(ctx, bson.M{"registry_id": registryID, "path": cleanPath}).Decode(&rec)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrArtifactNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *MongoStore) ListArtifacts(ctx context.Context, registryID string, q ListQuery) ([]*ArtifactRecord, int64, error) {
	if q.Page <= 0 {
		q.Page = 1
	}
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 25
	}
	filter := bson.M{"registry_id": registryID}
	if cat := strings.TrimSpace(q.Category); cat != "" {
		filter["category"] = cat
	}
	if search := strings.TrimSpace(q.Search); search != "" {
		filter["path"] = bson.M{"$regex": search, "$options": "i"}
	}

	total, err := s.col.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "updated_at", Value: -1}, {Key: "path", Value: 1}}).
		SetSkip(int64((q.Page - 1) * q.Limit)).
		SetLimit(int64(q.Limit))
	cur, err := s.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close(ctx)

	var out []*ArtifactRecord
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s *MongoStore) DeleteArtifact(ctx context.Context, registryID, cleanPath string) error {
	res, err := s.col.DeleteOne(ctx, bson.M{"registry_id": registryID, "path": cleanPath})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrArtifactNotFound
	}
	return nil
}

func (s *MongoStore) IncrementDownloads(ctx context.Context, registryID, cleanPath string) error {
	_, err := s.col.UpdateOne(
		ctx,
		bson.M{"registry_id": registryID, "path": cleanPath},
		bson.M{"$inc": bson.M{"downloads": 1}},
	)
	return err
}

func (s *MongoStore) DeleteRegistryMetadata(ctx context.Context, registryID string) error {
	_, err := s.col.DeleteMany(ctx, bson.M{"registry_id": registryID})
	return err
}

// --- In-Memory MetadataStore (for tests & STORAGE_BACKEND=memory) ---

type MemoryStore struct {
	mu    sync.RWMutex
	items map[string]*ArtifactRecord
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		items: make(map[string]*ArtifactRecord),
	}
}

func cloneRecord(r *ArtifactRecord) *ArtifactRecord {
	if r == nil {
		return nil
	}
	cp := *r
	if r.ExpiresAt != nil {
		t := *r.ExpiresAt
		cp.ExpiresAt = &t
	}
	return &cp
}

func (s *MemoryStore) UpsertArtifact(_ context.Context, rec *ArtifactRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := cloneRecord(rec)
	if cp.ID == "" {
		cp.ID = docID(cp.RegistryID, cp.Path)
	}
	s.items[cp.ID] = cp
	return nil
}

func (s *MemoryStore) GetArtifact(_ context.Context, registryID, cleanPath string) (*ArtifactRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.items[docID(registryID, cleanPath)]
	if !ok {
		return nil, ErrArtifactNotFound
	}
	return cloneRecord(rec), nil
}

func (s *MemoryStore) ListArtifacts(_ context.Context, registryID string, q ListQuery) ([]*ArtifactRecord, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if q.Page <= 0 {
		q.Page = 1
	}
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 25
	}
	searchLower := strings.ToLower(strings.TrimSpace(q.Search))
	catFilter := strings.TrimSpace(q.Category)

	var matched []*ArtifactRecord
	for _, rec := range s.items {
		if rec.RegistryID != registryID {
			continue
		}
		if catFilter != "" && rec.Category != catFilter {
			continue
		}
		if searchLower != "" && !strings.Contains(strings.ToLower(rec.Path), searchLower) {
			continue
		}
		matched = append(matched, cloneRecord(rec))
	}

	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].UpdatedAt.Equal(matched[j].UpdatedAt) {
			return matched[i].UpdatedAt.After(matched[j].UpdatedAt)
		}
		return matched[i].Path < matched[j].Path
	})

	total := int64(len(matched))
	start := (q.Page - 1) * q.Limit
	if start >= len(matched) {
		return []*ArtifactRecord{}, total, nil
	}
	end := start + q.Limit
	if end > len(matched) {
		end = len(matched)
	}
	return matched[start:end], total, nil
}

func (s *MemoryStore) DeleteArtifact(_ context.Context, registryID, cleanPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := docID(registryID, cleanPath)
	if _, ok := s.items[id]; !ok {
		return ErrArtifactNotFound
	}
	delete(s.items, id)
	return nil
}

func (s *MemoryStore) IncrementDownloads(_ context.Context, registryID, cleanPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.items[docID(registryID, cleanPath)]; ok {
		rec.Downloads++
	}
	return nil
}

func (s *MemoryStore) DeleteRegistryMetadata(_ context.Context, registryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, rec := range s.items {
		if rec.RegistryID == registryID {
			delete(s.items, id)
		}
	}
	return nil
}
