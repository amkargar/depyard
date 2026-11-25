package pub

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

const (
	ColPubPackages = "pub_packages"
	ColPubVersions = "pub_versions"
)

var (
	ErrPackageNotFound = errors.New("pub package not found")
	ErrVersionNotFound = errors.New("pub package version not found")
	ErrVersionExists   = errors.New("pub package version already exists")
)

// PubPackage stores ecosystem-specific metadata for a Pub package scoped to a registry instance.
type PubPackage struct {
	ID               string     `json:"id" bson:"_id"`
	RegistryID       string     `json:"registry_id" bson:"registry_id"`
	Name             string     `json:"name" bson:"name"`
	Origin           string     `json:"origin" bson:"origin"` // "hosted" or "cached"
	Description      string     `json:"description,omitempty" bson:"description,omitempty"`
	Homepage         string     `json:"homepage,omitempty" bson:"homepage,omitempty"`
	Repository       string     `json:"repository,omitempty" bson:"repository,omitempty"`
	Documentation    string     `json:"documentation,omitempty" bson:"documentation,omitempty"`
	Readme           string     `json:"readme,omitempty" bson:"readme,omitempty"`
	Changelog        string     `json:"changelog,omitempty" bson:"changelog,omitempty"`
	LatestVersion    string     `json:"latest_version,omitempty" bson:"latest_version,omitempty"`
	VersionCount     int        `json:"version_count" bson:"version_count"`
	TotalSizeBytes   int64      `json:"total_size_bytes" bson:"total_size_bytes"`
	IsDiscontinued   bool       `json:"is_discontinued" bson:"is_discontinued"`
	ReplacedBy       string     `json:"replaced_by,omitempty" bson:"replaced_by,omitempty"`
	Uploaders        []string   `json:"uploaders,omitempty" bson:"uploaders,omitempty"`
	Downloads        int64      `json:"downloads" bson:"downloads"`
	UpstreamSyncedAt *time.Time `json:"upstream_synced_at,omitempty" bson:"upstream_synced_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at" bson:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at" bson:"updated_at"`
}

// PubVersion stores ecosystem-specific metadata for one version of a Pub package.
type PubVersion struct {
	ID                 string         `json:"id" bson:"_id"`
	RegistryID         string         `json:"registry_id" bson:"registry_id"`
	PackageName        string         `json:"package_name" bson:"package_name"`
	Version            string         `json:"version" bson:"version"`
	Origin             string         `json:"origin" bson:"origin"` // "hosted" or "cached"
	Pubspec            map[string]any `json:"pubspec" bson:"pubspec"`
	ArchiveDigest      string         `json:"archive_digest,omitempty" bson:"archive_digest,omitempty"`
	ArchiveSize        int64          `json:"archive_size" bson:"archive_size"`
	ArchiveCached      bool           `json:"archive_cached" bson:"archive_cached"`
	UpstreamArchiveURL string         `json:"upstream_archive_url,omitempty" bson:"upstream_archive_url,omitempty"`
	Retracted          bool           `json:"retracted" bson:"retracted"`
	PublishedAt        time.Time      `json:"published_at" bson:"published_at"`
	CachedAt           *time.Time     `json:"cached_at,omitempty" bson:"cached_at,omitempty"`
	CreatedAt          time.Time      `json:"created_at" bson:"created_at"`
}

type PackageListQuery struct {
	RegistryID string
	Search     string
	Origin     string
	Page       int
	Limit      int
}

// MetadataStore persists Pub-specific package and version records strictly scoped by registry_id.
type MetadataStore interface {
	UpsertPackage(ctx context.Context, pkg *PubPackage) error
	GetPackage(ctx context.Context, registryID, name string) (*PubPackage, error)
	ListPackages(ctx context.Context, q PackageListQuery) ([]*PubPackage, int64, error)
	DeletePackage(ctx context.Context, registryID, name string) error
	IncrementPackageDownloads(ctx context.Context, registryID, name string) error

	UpsertVersion(ctx context.Context, ver *PubVersion) error
	GetVersion(ctx context.Context, registryID, pkgName, version string) (*PubVersion, error)
	ListVersions(ctx context.Context, registryID, pkgName string) ([]*PubVersion, error)
	DeleteVersion(ctx context.Context, registryID, pkgName, version string) error
	DeleteCachedPackages(ctx context.Context, registryID string) error
	DeleteRegistryMetadata(ctx context.Context, registryID string) error
}

// --- MongoDB Implementation (`pub_packages`, `pub_versions`) ---

type MongoStore struct {
	packages *mongo.Collection
	versions *mongo.Collection
}

func NewMongoStore(ctx context.Context, db *mongo.Database) (*MongoStore, error) {
	s := &MongoStore{
		packages: db.Collection(ColPubPackages),
		versions: db.Collection(ColPubVersions),
	}
	if err := s.ensureIndexes(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *MongoStore) ensureIndexes(ctx context.Context) error {
	if _, err := s.packages.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "name", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "updated_at", Value: -1}}},
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "origin", Value: 1}}},
	}); err != nil {
		return err
	}
	if _, err := s.versions.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "package_name", Value: 1}, {Key: "version", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "package_name", Value: 1}}},
	}); err != nil {
		return err
	}
	return nil
}

func pkgDocID(registryID, name string) string {
	return registryID + ":" + name
}

func verDocID(registryID, name, version string) string {
	return registryID + ":" + name + ":" + version
}

func (s *MongoStore) UpsertPackage(ctx context.Context, pkg *PubPackage) error {
	if pkg.ID == "" {
		pkg.ID = pkgDocID(pkg.RegistryID, pkg.Name)
	}
	_, err := s.packages.ReplaceOne(ctx, bson.M{"_id": pkg.ID}, pkg, options.Replace().SetUpsert(true))
	return err
}

func (s *MongoStore) GetPackage(ctx context.Context, registryID, name string) (*PubPackage, error) {
	var pkg PubPackage
	err := s.packages.FindOne(ctx, bson.M{"registry_id": registryID, "name": name}).Decode(&pkg)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrPackageNotFound
	}
	return &pkg, err
}

func (s *MongoStore) ListPackages(ctx context.Context, q PackageListQuery) ([]*PubPackage, int64, error) {
	filter := bson.M{"registry_id": q.RegistryID}
	if q.Origin != "" {
		filter["origin"] = q.Origin
	}
	if search := strings.TrimSpace(q.Search); search != "" {
		filter["$or"] = bson.A{
			bson.M{"name": bson.M{"$regex": search, "$options": "i"}},
			bson.M{"description": bson.M{"$regex": search, "$options": "i"}},
		}
	}

	total, err := s.packages.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	page := q.Page
	if page <= 0 {
		page = 1
	}
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	skip := int64((page - 1) * limit)

	cur, err := s.packages.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "updated_at", Value: -1}, {Key: "name", Value: 1}}).
		SetSkip(skip).
		SetLimit(int64(limit)))
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close(ctx)

	var out []*PubPackage
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s *MongoStore) DeletePackage(ctx context.Context, registryID, name string) error {
	_, _ = s.versions.DeleteMany(ctx, bson.M{"registry_id": registryID, "package_name": name})
	_, err := s.packages.DeleteOne(ctx, bson.M{"registry_id": registryID, "name": name})
	return err
}

func (s *MongoStore) IncrementPackageDownloads(ctx context.Context, registryID, name string) error {
	_, err := s.packages.UpdateOne(
		ctx,
		bson.M{"registry_id": registryID, "name": name},
		bson.M{"$inc": bson.M{"downloads": 1}},
	)
	return err
}

func (s *MongoStore) UpsertVersion(ctx context.Context, ver *PubVersion) error {
	if ver.ID == "" {
		ver.ID = verDocID(ver.RegistryID, ver.PackageName, ver.Version)
	}
	_, err := s.versions.ReplaceOne(ctx, bson.M{"_id": ver.ID}, ver, options.Replace().SetUpsert(true))
	return err
}

func (s *MongoStore) GetVersion(ctx context.Context, registryID, pkgName, version string) (*PubVersion, error) {
	var v PubVersion
	err := s.versions.FindOne(ctx, bson.M{
		"registry_id":  registryID,
		"package_name": pkgName,
		"version":      version,
	}).Decode(&v)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrVersionNotFound
	}
	return &v, err
}

func (s *MongoStore) ListVersions(ctx context.Context, registryID, pkgName string) ([]*PubVersion, error) {
	cur, err := s.versions.Find(ctx, bson.M{
		"registry_id":  registryID,
		"package_name": pkgName,
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var out []*PubVersion
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	SortVersionsAscending(out)
	return out, nil
}

func (s *MongoStore) DeleteVersion(ctx context.Context, registryID, pkgName, version string) error {
	_, err := s.versions.DeleteOne(ctx, bson.M{
		"registry_id":  registryID,
		"package_name": pkgName,
		"version":      version,
	})
	return err
}

func (s *MongoStore) DeleteCachedPackages(ctx context.Context, registryID string) error {
	_, _ = s.versions.DeleteMany(ctx, bson.M{"registry_id": registryID, "origin": "cached"})
	_, err := s.packages.DeleteMany(ctx, bson.M{"registry_id": registryID, "origin": "cached"})
	return err
}

func (s *MongoStore) DeleteRegistryMetadata(ctx context.Context, registryID string) error {
	_, _ = s.versions.DeleteMany(ctx, bson.M{"registry_id": registryID})
	_, err := s.packages.DeleteMany(ctx, bson.M{"registry_id": registryID})
	return err
}

// --- In-Memory Implementation (for unit/integration tests and standalone local dev) ---

type MemoryStore struct {
	mu       sync.RWMutex
	packages map[string]*PubPackage
	versions map[string]*PubVersion
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		packages: make(map[string]*PubPackage),
		versions: make(map[string]*PubVersion),
	}
}

func (s *MemoryStore) UpsertPackage(_ context.Context, pkg *PubPackage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pkg.ID == "" {
		pkg.ID = pkgDocID(pkg.RegistryID, pkg.Name)
	}
	cp := *pkg
	cp.Uploaders = append([]string(nil), pkg.Uploaders...)
	s.packages[pkg.ID] = &cp
	return nil
}

func (s *MemoryStore) GetPackage(_ context.Context, registryID, name string) (*PubPackage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pkg, ok := s.packages[pkgDocID(registryID, name)]
	if !ok {
		return nil, ErrPackageNotFound
	}
	cp := *pkg
	cp.Uploaders = append([]string(nil), pkg.Uploaders...)
	return &cp, nil
}

func (s *MemoryStore) ListPackages(_ context.Context, q PackageListQuery) ([]*PubPackage, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	search := strings.ToLower(strings.TrimSpace(q.Search))
	var matches []*PubPackage
	for _, pkg := range s.packages {
		if pkg.RegistryID != q.RegistryID {
			continue
		}
		if q.Origin != "" && pkg.Origin != q.Origin {
			continue
		}
		if search != "" {
			if !strings.Contains(strings.ToLower(pkg.Name), search) &&
				!strings.Contains(strings.ToLower(pkg.Description), search) {
				continue
			}
		}
		cp := *pkg
		cp.Uploaders = append([]string(nil), pkg.Uploaders...)
		matches = append(matches, &cp)
	}
	sort.Slice(matches, func(i, j int) bool {
		if !matches[i].UpdatedAt.Equal(matches[j].UpdatedAt) {
			return matches[i].UpdatedAt.After(matches[j].UpdatedAt)
		}
		return matches[i].Name < matches[j].Name
	})

	total := int64(len(matches))
	page := q.Page
	if page <= 0 {
		page = 1
	}
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	start := (page - 1) * limit
	if start >= len(matches) {
		return []*PubPackage{}, total, nil
	}
	end := start + limit
	if end > len(matches) {
		end = len(matches)
	}
	return matches[start:end], total, nil
}

func (s *MemoryStore) DeletePackage(_ context.Context, registryID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.packages, pkgDocID(registryID, name))
	for k, v := range s.versions {
		if v.RegistryID == registryID && v.PackageName == name {
			delete(s.versions, k)
		}
	}
	return nil
}

func (s *MemoryStore) IncrementPackageDownloads(_ context.Context, registryID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pkg, ok := s.packages[pkgDocID(registryID, name)]; ok {
		pkg.Downloads++
	}
	return nil
}

func (s *MemoryStore) UpsertVersion(_ context.Context, ver *PubVersion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ver.ID == "" {
		ver.ID = verDocID(ver.RegistryID, ver.PackageName, ver.Version)
	}
	cp := *ver
	s.versions[ver.ID] = &cp
	return nil
}

func (s *MemoryStore) GetVersion(_ context.Context, registryID, pkgName, version string) (*PubVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.versions[verDocID(registryID, pkgName, version)]
	if !ok {
		return nil, ErrVersionNotFound
	}
	cp := *v
	return &cp, nil
}

func (s *MemoryStore) ListVersions(_ context.Context, registryID, pkgName string) ([]*PubVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*PubVersion
	for _, v := range s.versions {
		if v.RegistryID == registryID && v.PackageName == pkgName {
			cp := *v
			out = append(out, &cp)
		}
	}
	SortVersionsAscending(out)
	return out, nil
}

func (s *MemoryStore) DeleteVersion(_ context.Context, registryID, pkgName, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.versions, verDocID(registryID, pkgName, version))
	return nil
}

func (s *MemoryStore) DeleteCachedPackages(_ context.Context, registryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.versions {
		if v.RegistryID == registryID && v.Origin == "cached" {
			delete(s.versions, k)
		}
	}
	for k, p := range s.packages {
		if p.RegistryID == registryID && p.Origin == "cached" {
			delete(s.packages, k)
		}
	}
	return nil
}

func (s *MemoryStore) DeleteRegistryMetadata(_ context.Context, registryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.versions {
		if v.RegistryID == registryID {
			delete(s.versions, k)
		}
	}
	for k, p := range s.packages {
		if p.RegistryID == registryID {
			delete(s.packages, k)
		}
	}
	return nil
}
