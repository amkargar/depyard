package npm

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// ColNpmPackages is the single collection holding npm package metadata.
// Versions are embedded: an npm package and its versions are always edited
// together on publish/unpublish, so a separate collection would add without
// adding consistency for this ecosystem.
const ColNpmPackages = "npm_packages"

// OriginMixed marks a package that has both locally published and mirrored versions.
const OriginMixed = "mixed"

var (
	ErrPackageNotFound = errors.New("npm package not found")
	ErrVersionNotFound = errors.New("npm version not found")
)

// NpmVersion is the metadata of one published version of a package.
type NpmVersion struct {
	Version     string    `json:"version" bson:"version"`
	Filename    string    `json:"filename" bson:"filename"`
	Digest      string    `json:"digest,omitempty" bson:"digest,omitempty"` // hex SHA-256 of the tarball
	Shasum      string    `json:"shasum,omitempty" bson:"shasum,omitempty"`
	Integrity   string    `json:"integrity,omitempty" bson:"integrity,omitempty"`
	SizeBytes   int64     `json:"size_bytes" bson:"size_bytes"`
	Origin      string    `json:"origin" bson:"origin"`
	Description string    `json:"description,omitempty" bson:"description,omitempty"`
	Deprecated  string    `json:"deprecated,omitempty" bson:"deprecated,omitempty"`
	PublishedAt time.Time `json:"published_at" bson:"published_at"`
	// UpstreamTarballURL is used internally to fetch a cached tarball. It is
	// deliberately not part of the packument served to clients.
	UpstreamTarballURL string `json:"-" bson:"upstream_tarball_url,omitempty"`
	// RawMetadata is the full version manifest (JSON text) from `npm publish`
	// or the upstream packument. Keeping it verbatim preserves fields this
	// implementation does not model explicitly while still allowing the
	// registry to rewrite dist.tarball to its own URL.
	RawMetadata string `json:"-" bson:"raw_metadata,omitempty"`
}

// NpmPackage is the metadata record of a single npm package in one registry.
type NpmPackage struct {
	ID               string            `json:"id" bson:"_id"`
	RegistryID       string            `json:"registry_id" bson:"registry_id"`
	Name             string            `json:"name" bson:"name"`
	Description      string            `json:"description,omitempty" bson:"description,omitempty"`
	Readme           string            `json:"readme,omitempty" bson:"readme,omitempty"`
	Homepage         string            `json:"homepage,omitempty" bson:"homepage,omitempty"`
	Repository       string            `json:"repository,omitempty" bson:"repository,omitempty"`
	License          string            `json:"license,omitempty" bson:"license,omitempty"`
	Keywords         []string          `json:"keywords,omitempty" bson:"keywords,omitempty"`
	Maintainers      []string          `json:"maintainers,omitempty" bson:"maintainers,omitempty"`
	DistTags         map[string]string `json:"dist_tags" bson:"dist_tags"`
	Versions         []NpmVersion      `json:"versions" bson:"versions"`
	Origin           string            `json:"origin" bson:"origin"`
	LatestVersion    string            `json:"latest_version,omitempty" bson:"latest_version,omitempty"`
	VersionCount     int               `json:"version_count" bson:"version_count"`
	TotalSizeBytes   int64             `json:"total_size_bytes" bson:"total_size_bytes"`
	Downloads        int64             `json:"downloads" bson:"downloads"`
	UpstreamSyncedAt *time.Time        `json:"upstream_synced_at,omitempty" bson:"upstream_synced_at,omitempty"`
	CreatedAt        time.Time         `json:"created_at" bson:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at" bson:"updated_at"`
}

// PackageListQuery filters package listings for the explorer and admin API.
type PackageListQuery struct {
	RegistryID string
	Search     string
	Origin     string
	Page       int
	Limit      int
}

// MetadataStore persists npm-specific records strictly scoped by registry_id.
type MetadataStore interface {
	UpsertPackage(ctx context.Context, p *NpmPackage) error
	GetPackage(ctx context.Context, registryID, name string) (*NpmPackage, error)
	ListPackages(ctx context.Context, q PackageListQuery) ([]*NpmPackage, int64, error)
	DeletePackage(ctx context.Context, registryID, name string) error
	IncrementDownloads(ctx context.Context, registryID, name string) error
	DeleteCachedPackages(ctx context.Context, registryID string) error
	DeleteRegistryMetadata(ctx context.Context, registryID string) error
}

// --- Memory implementation ---

// MemoryStore is an in-memory MetadataStore used by tests and single-node dev mode.
type MemoryStore struct {
	mu       sync.RWMutex
	packages map[string]*NpmPackage
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{packages: make(map[string]*NpmPackage)}
}

func memoryKey(registryID, name string) string {
	return registryID + "\x00" + NormalizePackageName(name)
}

func clonePackage(p *NpmPackage) *NpmPackage {
	if p == nil {
		return nil
	}
	cp := *p
	cp.DistTags = make(map[string]string, len(p.DistTags))
	for k, v := range p.DistTags {
		cp.DistTags[k] = v
	}
	cp.Versions = append([]NpmVersion(nil), p.Versions...)
	cp.Keywords = append([]string(nil), p.Keywords...)
	cp.Maintainers = append([]string(nil), p.Maintainers...)
	return &cp
}

func (s *MemoryStore) UpsertPackage(_ context.Context, p *NpmPackage) error {
	if p == nil || p.RegistryID == "" || p.Name == "" {
		return errors.New("registry_id and name are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := clonePackage(p)
	cp.Name = NormalizePackageName(p.Name)
	s.packages[memoryKey(p.RegistryID, cp.Name)] = cp
	return nil
}

func (s *MemoryStore) GetPackage(_ context.Context, registryID, name string) (*NpmPackage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.packages[memoryKey(registryID, name)]
	if !ok {
		return nil, ErrPackageNotFound
	}
	return clonePackage(p), nil
}

func (s *MemoryStore) ListPackages(_ context.Context, q PackageListQuery) ([]*NpmPackage, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	matched := make([]*NpmPackage, 0)
	needle := strings.ToLower(strings.TrimSpace(q.Search))
	for _, p := range s.packages {
		if p.RegistryID != q.RegistryID {
			continue
		}
		if q.Origin != "" && p.Origin != q.Origin && p.Origin != OriginMixed {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(p.Name+" "+p.Description+" "+strings.Join(p.Keywords, " ")), needle) {
			continue
		}
		matched = append(matched, clonePackage(p))
	}
	sortPackages(matched)
	total := int64(len(matched))
	page, limit := paginateParams(q.Page, q.Limit)
	start := (page - 1) * limit
	if start >= len(matched) {
		return []*NpmPackage{}, total, nil
	}
	end := start + limit
	if end > len(matched) {
		end = len(matched)
	}
	return matched[start:end], total, nil
}

func (s *MemoryStore) DeletePackage(_ context.Context, registryID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.packages, memoryKey(registryID, name))
	return nil
}

func (s *MemoryStore) IncrementDownloads(_ context.Context, registryID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.packages[memoryKey(registryID, name)]; ok {
		p.Downloads++
	}
	return nil
}

func (s *MemoryStore) DeleteCachedPackages(_ context.Context, registryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, p := range s.packages {
		if p.RegistryID != registryID {
			continue
		}
		kept := make([]NpmVersion, 0, len(p.Versions))
		for _, v := range p.Versions {
			if v.Origin != "cached" {
				kept = append(kept, v)
			}
		}
		if len(kept) == len(p.Versions) {
			continue
		}
		if len(kept) == 0 {
			delete(s.packages, key)
			continue
		}
		p.Versions = kept
		p.Origin = "hosted"
		s.packages[key] = p
	}
	return nil
}

func (s *MemoryStore) DeleteRegistryMetadata(_ context.Context, registryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, p := range s.packages {
		if p.RegistryID == registryID {
			delete(s.packages, key)
		}
	}
	return nil
}

// --- Mongo implementation ---

// MongoStore persists npm metadata in MongoDB.
type MongoStore struct {
	packages *mongo.Collection
}

func NewMongoStore(ctx context.Context, db *mongo.Database) (*MongoStore, error) {
	s := &MongoStore{packages: db.Collection(ColNpmPackages)}
	if err := s.EnsureIndexes(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *MongoStore) EnsureIndexes(ctx context.Context) error {
	_, err := s.packages.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "name", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "origin", Value: 1}}},
	})
	return err
}

func (s *MongoStore) UpsertPackage(ctx context.Context, p *NpmPackage) error {
	if p == nil || p.RegistryID == "" || p.Name == "" {
		return errors.New("registry_id and name are required")
	}
	cp := clonePackage(p)
	cp.Name = NormalizePackageName(p.Name)
	cp.UpdatedAt = time.Now().UTC()
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = cp.UpdatedAt
	}
	_, err := s.packages.ReplaceOne(ctx, bson.M{"_id": cp.ID}, cp, options.Replace().SetUpsert(true))
	return err
}

func (s *MongoStore) GetPackage(ctx context.Context, registryID, name string) (*NpmPackage, error) {
	var p NpmPackage
	err := s.packages.FindOne(ctx, bson.M{
		"registry_id": registryID,
		"name":        NormalizePackageName(name),
	}).Decode(&p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrPackageNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *MongoStore) ListPackages(ctx context.Context, q PackageListQuery) ([]*NpmPackage, int64, error) {
	filter := bson.M{"registry_id": q.RegistryID}
	if q.Origin != "" {
		filter["origin"] = bson.M{"$in": []string{q.Origin, OriginMixed}}
	}
	if q.Search != "" {
		rx := bson.M{"$regex": regexp.QuoteMeta(strings.TrimSpace(q.Search)), "$options": "i"}
		filter["$or"] = []bson.M{{"name": rx}, {"description": rx}, {"keywords": rx}}
	}
	total, err := s.packages.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	page, limit := paginateParams(q.Page, q.Limit)
	cur, err := s.packages.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "name", Value: 1}}).
		SetSkip(int64((page-1)*limit)).
		SetLimit(int64(limit)))
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close(ctx)
	out := make([]*NpmPackage, 0, limit)
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s *MongoStore) DeletePackage(ctx context.Context, registryID, name string) error {
	_, err := s.packages.DeleteOne(ctx, bson.M{
		"registry_id": registryID,
		"name":        NormalizePackageName(name),
	})
	return err
}

func (s *MongoStore) IncrementDownloads(ctx context.Context, registryID, name string) error {
	_, err := s.packages.UpdateOne(ctx,
		bson.M{"registry_id": registryID, "name": NormalizePackageName(name)},
		bson.M{"$inc": bson.M{"downloads": 1}},
	)
	return err
}

func (s *MongoStore) DeleteCachedPackages(ctx context.Context, registryID string) error {
	filter := bson.M{"registry_id": registryID, "origin": bson.M{"$in": []string{"cached", OriginMixed}}}
	cur, err := s.packages.Find(ctx, filter)
	if err != nil {
		return err
	}
	var packages []*NpmPackage
	if err := cur.All(ctx, &packages); err != nil {
		_ = cur.Close(ctx)
		return err
	}
	_ = cur.Close(ctx)
	for _, p := range packages {
		kept := make([]NpmVersion, 0, len(p.Versions))
		for _, v := range p.Versions {
			if v.Origin != "cached" {
				kept = append(kept, v)
			}
		}
		if len(kept) == 0 {
			if err := s.DeletePackage(ctx, registryID, p.Name); err != nil {
				return err
			}
			continue
		}
		p.Versions = kept
		p.Origin = "hosted"
		p.VersionCount = len(kept)
		p.LatestVersion = LatestVersion(versionStrings(kept), true)
		if err := s.UpsertPackage(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

func (s *MongoStore) DeleteRegistryMetadata(ctx context.Context, registryID string) error {
	_, err := s.packages.DeleteMany(ctx, bson.M{"registry_id": registryID})
	return err
}

// --- shared helpers ---

func paginateParams(page, limit int) (int, int) {
	if page < 1 {
		page = 1
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	return page, limit
}

func sortPackages(items []*NpmPackage) {
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
}

func versionStrings(versions []NpmVersion) []string {
	out := make([]string, 0, len(versions))
	for _, v := range versions {
		out = append(out, v.Version)
	}
	return out
}
