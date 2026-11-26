package goproxy

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
	ColGoModules  = "go_modules"
	ColGoVersions = "go_versions"
)

var (
	ErrModuleNotFound  = errors.New("go module not found")
	ErrVersionNotFound = errors.New("go module version not found")
)

// GoModule stores ecosystem-specific metadata for a Go module scoped to a registry instance.
type GoModule struct {
	ID               string     `json:"id" bson:"_id"`
	RegistryID       string     `json:"registry_id" bson:"registry_id"`
	Path             string     `json:"path" bson:"path"`
	Origin           string     `json:"origin" bson:"origin"` // "hosted" or "cached"
	Description      string     `json:"description,omitempty" bson:"description,omitempty"`
	LatestVersion    string     `json:"latest_version,omitempty" bson:"latest_version,omitempty"`
	VersionCount     int        `json:"version_count" bson:"version_count"`
	TotalSizeBytes   int64      `json:"total_size_bytes" bson:"total_size_bytes"`
	Downloads        int64      `json:"downloads" bson:"downloads"`
	UpstreamSyncedAt *time.Time `json:"upstream_synced_at,omitempty" bson:"upstream_synced_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at" bson:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at" bson:"updated_at"`
}

// GoVersion stores metadata for one version of a Go module.
type GoVersion struct {
	ID          string     `json:"id" bson:"_id"`
	RegistryID  string     `json:"registry_id" bson:"registry_id"`
	ModulePath  string     `json:"module_path" bson:"module_path"`
	Version     string     `json:"version" bson:"version"`
	Origin      string     `json:"origin" bson:"origin"`
	InfoTime    time.Time  `json:"info_time" bson:"info_time"`
	GoModDigest string     `json:"go_mod_digest,omitempty" bson:"go_mod_digest,omitempty"`
	ZipDigest   string     `json:"zip_digest,omitempty" bson:"zip_digest,omitempty"`
	ZipSize     int64      `json:"zip_size" bson:"zip_size"`
	ZipCached   bool       `json:"zip_cached" bson:"zip_cached"`
	ModCached   bool       `json:"mod_cached" bson:"mod_cached"`
	ModSize     int64      `json:"mod_size" bson:"mod_size"`
	UpstreamZip string     `json:"upstream_zip_url,omitempty" bson:"upstream_zip_url,omitempty"`
	PublishedAt time.Time  `json:"published_at" bson:"published_at"`
	CachedAt    *time.Time `json:"cached_at,omitempty" bson:"cached_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at" bson:"created_at"`
}

type ModuleListQuery struct {
	RegistryID string
	Search     string
	Origin     string
	Page       int
	Limit      int
}

// MetadataStore persists Go-proxy-specific module and version records scoped by registry_id.
type MetadataStore interface {
	UpsertModule(ctx context.Context, mod *GoModule) error
	GetModule(ctx context.Context, registryID, path string) (*GoModule, error)
	ListModules(ctx context.Context, q ModuleListQuery) ([]*GoModule, int64, error)
	DeleteModule(ctx context.Context, registryID, path string) error
	IncrementModuleDownloads(ctx context.Context, registryID, path string) error

	UpsertVersion(ctx context.Context, ver *GoVersion) error
	GetVersion(ctx context.Context, registryID, modulePath, version string) (*GoVersion, error)
	ListVersions(ctx context.Context, registryID, modulePath string) ([]*GoVersion, error)
	DeleteVersion(ctx context.Context, registryID, modulePath, version string) error
	DeleteCachedModules(ctx context.Context, registryID string) error
	DeleteRegistryMetadata(ctx context.Context, registryID string) error
}

// --- MongoDB Implementation (`go_modules`, `go_versions`) ---

type MongoStore struct {
	modules  *mongo.Collection
	versions *mongo.Collection
}

func NewMongoStore(ctx context.Context, db *mongo.Database) (*MongoStore, error) {
	s := &MongoStore{
		modules:  db.Collection(ColGoModules),
		versions: db.Collection(ColGoVersions),
	}
	if err := s.ensureIndexes(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *MongoStore) ensureIndexes(ctx context.Context) error {
	if _, err := s.modules.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "path", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "updated_at", Value: -1}}},
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "origin", Value: 1}}},
	}); err != nil {
		return err
	}
	if _, err := s.versions.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "module_path", Value: 1}, {Key: "version", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "module_path", Value: 1}}},
	}); err != nil {
		return err
	}
	return nil
}

func moduleDocID(registryID, path string) string {
	return registryID + ":" + path
}

func versionDocID(registryID, path, version string) string {
	return registryID + ":" + path + ":" + version
}

func (s *MongoStore) UpsertModule(ctx context.Context, mod *GoModule) error {
	if mod.ID == "" {
		mod.ID = moduleDocID(mod.RegistryID, mod.Path)
	}
	_, err := s.modules.ReplaceOne(ctx, bson.M{"_id": mod.ID}, mod, options.Replace().SetUpsert(true))
	return err
}

func (s *MongoStore) GetModule(ctx context.Context, registryID, path string) (*GoModule, error) {
	var mod GoModule
	err := s.modules.FindOne(ctx, bson.M{"registry_id": registryID, "path": path}).Decode(&mod)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrModuleNotFound
	}
	return &mod, err
}

func (s *MongoStore) ListModules(ctx context.Context, q ModuleListQuery) ([]*GoModule, int64, error) {
	filter := bson.M{"registry_id": q.RegistryID}
	if q.Origin != "" {
		filter["origin"] = q.Origin
	}
	if search := strings.TrimSpace(q.Search); search != "" {
		filter["$or"] = bson.A{
			bson.M{"path": bson.M{"$regex": search, "$options": "i"}},
			bson.M{"description": bson.M{"$regex": search, "$options": "i"}},
		}
	}

	total, err := s.modules.CountDocuments(ctx, filter)
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

	cur, err := s.modules.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "updated_at", Value: -1}, {Key: "path", Value: 1}}).
		SetSkip(skip).
		SetLimit(int64(limit)))
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close(ctx)

	var out []*GoModule
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s *MongoStore) DeleteModule(ctx context.Context, registryID, path string) error {
	_, _ = s.versions.DeleteMany(ctx, bson.M{"registry_id": registryID, "module_path": path})
	_, err := s.modules.DeleteOne(ctx, bson.M{"registry_id": registryID, "path": path})
	return err
}

func (s *MongoStore) IncrementModuleDownloads(ctx context.Context, registryID, path string) error {
	_, err := s.modules.UpdateOne(
		ctx,
		bson.M{"registry_id": registryID, "path": path},
		bson.M{"$inc": bson.M{"downloads": 1}},
	)
	return err
}

func (s *MongoStore) UpsertVersion(ctx context.Context, ver *GoVersion) error {
	if ver.ID == "" {
		ver.ID = versionDocID(ver.RegistryID, ver.ModulePath, ver.Version)
	}
	_, err := s.versions.ReplaceOne(ctx, bson.M{"_id": ver.ID}, ver, options.Replace().SetUpsert(true))
	return err
}

func (s *MongoStore) GetVersion(ctx context.Context, registryID, modulePath, version string) (*GoVersion, error) {
	var v GoVersion
	err := s.versions.FindOne(ctx, bson.M{
		"registry_id": registryID,
		"module_path": modulePath,
		"version":     version,
	}).Decode(&v)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrVersionNotFound
	}
	return &v, err
}

func (s *MongoStore) ListVersions(ctx context.Context, registryID, modulePath string) ([]*GoVersion, error) {
	cur, err := s.versions.Find(ctx, bson.M{
		"registry_id": registryID,
		"module_path": modulePath,
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var out []*GoVersion
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	SortVersionsAscending(out)
	return out, nil
}

func (s *MongoStore) DeleteVersion(ctx context.Context, registryID, modulePath, version string) error {
	_, err := s.versions.DeleteOne(ctx, bson.M{
		"registry_id": registryID,
		"module_path": modulePath,
		"version":     version,
	})
	return err
}

func (s *MongoStore) DeleteCachedModules(ctx context.Context, registryID string) error {
	_, _ = s.versions.DeleteMany(ctx, bson.M{"registry_id": registryID, "origin": "cached"})
	_, err := s.modules.DeleteMany(ctx, bson.M{"registry_id": registryID, "origin": "cached"})
	return err
}

func (s *MongoStore) DeleteRegistryMetadata(ctx context.Context, registryID string) error {
	_, _ = s.versions.DeleteMany(ctx, bson.M{"registry_id": registryID})
	_, err := s.modules.DeleteMany(ctx, bson.M{"registry_id": registryID})
	return err
}

// --- In-Memory Implementation (for unit/integration tests and standalone local dev) ---

type MemoryStore struct {
	mu       sync.RWMutex
	modules  map[string]*GoModule
	versions map[string]*GoVersion
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		modules:  make(map[string]*GoModule),
		versions: make(map[string]*GoVersion),
	}
}

func (s *MemoryStore) UpsertModule(_ context.Context, mod *GoModule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mod.ID == "" {
		mod.ID = moduleDocID(mod.RegistryID, mod.Path)
	}
	cp := *mod
	s.modules[mod.ID] = &cp
	return nil
}

func (s *MemoryStore) GetModule(_ context.Context, registryID, path string) (*GoModule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	mod, ok := s.modules[moduleDocID(registryID, path)]
	if !ok {
		return nil, ErrModuleNotFound
	}
	cp := *mod
	return &cp, nil
}

func (s *MemoryStore) ListModules(_ context.Context, q ModuleListQuery) ([]*GoModule, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	search := strings.ToLower(strings.TrimSpace(q.Search))
	var matches []*GoModule
	for _, mod := range s.modules {
		if mod.RegistryID != q.RegistryID {
			continue
		}
		if q.Origin != "" && mod.Origin != q.Origin {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(mod.Path), search) &&
			!strings.Contains(strings.ToLower(mod.Description), search) {
			continue
		}
		cp := *mod
		matches = append(matches, &cp)
	}
	sort.Slice(matches, func(i, j int) bool {
		if !matches[i].UpdatedAt.Equal(matches[j].UpdatedAt) {
			return matches[i].UpdatedAt.After(matches[j].UpdatedAt)
		}
		return matches[i].Path < matches[j].Path
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
		return []*GoModule{}, total, nil
	}
	end := start + limit
	if end > len(matches) {
		end = len(matches)
	}
	return matches[start:end], total, nil
}

func (s *MemoryStore) DeleteModule(_ context.Context, registryID, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.modules, moduleDocID(registryID, path))
	for k, v := range s.versions {
		if v.RegistryID == registryID && v.ModulePath == path {
			delete(s.versions, k)
		}
	}
	return nil
}

func (s *MemoryStore) IncrementModuleDownloads(_ context.Context, registryID, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mod, ok := s.modules[moduleDocID(registryID, path)]; ok {
		mod.Downloads++
	}
	return nil
}

func (s *MemoryStore) UpsertVersion(_ context.Context, ver *GoVersion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ver.ID == "" {
		ver.ID = versionDocID(ver.RegistryID, ver.ModulePath, ver.Version)
	}
	cp := *ver
	s.versions[ver.ID] = &cp
	return nil
}

func (s *MemoryStore) GetVersion(_ context.Context, registryID, modulePath, version string) (*GoVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.versions[versionDocID(registryID, modulePath, version)]
	if !ok {
		return nil, ErrVersionNotFound
	}
	cp := *v
	return &cp, nil
}

func (s *MemoryStore) ListVersions(_ context.Context, registryID, modulePath string) ([]*GoVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*GoVersion
	for _, v := range s.versions {
		if v.RegistryID != registryID || v.ModulePath != modulePath {
			continue
		}
		cp := *v
		out = append(out, &cp)
	}
	SortVersionsAscending(out)
	return out, nil
}

func (s *MemoryStore) DeleteVersion(_ context.Context, registryID, modulePath, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.versions, versionDocID(registryID, modulePath, version))
	return nil
}

func (s *MemoryStore) DeleteCachedModules(_ context.Context, registryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.versions {
		if v.RegistryID == registryID && v.Origin == "cached" {
			delete(s.versions, k)
		}
	}
	for k, m := range s.modules {
		if m.RegistryID == registryID && m.Origin == "cached" {
			delete(s.modules, k)
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
	for k, m := range s.modules {
		if m.RegistryID == registryID {
			delete(s.modules, k)
		}
	}
	return nil
}

// SortVersionsAscending orders module versions using Go's semantic version rules.
func SortVersionsAscending(versions []*GoVersion) {
	sort.Slice(versions, func(i, j int) bool {
		return compareVersions(versions[i].Version, versions[j].Version) < 0
	})
}
