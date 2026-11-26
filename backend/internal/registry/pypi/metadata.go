package pypi

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
	ColPyProjects = "pypi_projects"
	ColPyReleases = "pypi_releases"
	ColPyFiles    = "pypi_files"
)

var (
	ErrProjectNotFound = errors.New("pypi project not found")
	ErrReleaseNotFound = errors.New("pypi release not found")
	ErrFileNotFound    = errors.New("pypi file not found")
)

// Origin values shared with the storage layer.
const (
	OriginHosted = "hosted"
	OriginCached = "cached"
)

// PyProject is the metadata record for a single Python project.
//
// Unlike most ecosystems, a Python project carries many distribution files per
// release (one sdist plus any number of wheels), so projects, releases and files
// are modelled as three separate records.
type PyProject struct {
	ID          string `json:"id" bson:"_id"`
	RegistryID  string `json:"registry_id" bson:"registry_id"`
	Name        string `json:"name" bson:"name"` // Normalized (PEP 503) name
	DisplayName string `json:"display_name,omitempty" bson:"display_name,omitempty"`
	Origin      string `json:"origin" bson:"origin"`
	Summary     string `json:"summary,omitempty" bson:"summary,omitempty"`
	Description string `json:"description,omitempty" bson:"description,omitempty"`
	Homepage    string `json:"homepage,omitempty" bson:"homepage,omitempty"`
	Author      string `json:"author,omitempty" bson:"author,omitempty"`
	AuthorEmail string `json:"author_email,omitempty" bson:"author_email,omitempty"`
	License     string `json:"license,omitempty" bson:"license,omitempty"`
	// RequiresPython is the project's declared "Requires-Python" specifier.
	RequiresPython string            `json:"requires_python,omitempty" bson:"requires_python,omitempty"`
	Classifiers    []string          `json:"classifiers,omitempty" bson:"classifiers,omitempty"`
	ProjectURLs    map[string]string `json:"project_urls,omitempty" bson:"project_urls,omitempty"`
	// Keywords and the raw core metadata keep the project browsable in the UI.
	Keywords      []string `json:"keywords,omitempty" bson:"keywords,omitempty"`
	LatestVersion string   `json:"latest_version,omitempty" bson:"latest_version,omitempty"`
	ReleaseCount  int      `json:"release_count" bson:"release_count"`
	FileCount     int      `json:"file_count" bson:"file_count"`
	TotalSize     int64    `json:"total_size_bytes" bson:"total_size_bytes"`
	Downloads     int64    `json:"downloads" bson:"downloads"`
	// UpstreamSyncedAt records when the project metadata was last refreshed.
	UpstreamSyncedAt *time.Time `json:"upstream_synced_at,omitempty" bson:"upstream_synced_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at" bson:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at" bson:"updated_at"`
}

// PyRelease is the metadata record for one release (project + version) of a project.
type PyRelease struct {
	ID          string `json:"id" bson:"_id"`
	RegistryID  string `json:"registry_id" bson:"registry_id"`
	ProjectName string `json:"project_name" bson:"project_name"`
	Version     string `json:"version" bson:"version"`
	Origin      string `json:"origin" bson:"origin"`
	Summary     string `json:"summary,omitempty" bson:"summary,omitempty"`
	// RequiresPython is the release-level "Requires-Python" specifier.
	RequiresPython string `json:"requires_python,omitempty" bson:"requires_python,omitempty"`
	Yanked         bool   `json:"yanked" bson:"yanked"`
	YankedReason   string `json:"yanked_reason,omitempty" bson:"yanked_reason,omitempty"`
	// UploadTime is the release's publication timestamp.
	UploadTime time.Time `json:"upload_time" bson:"upload_time"`
	FileCount  int       `json:"file_count" bson:"file_count"`
	TotalSize  int64     `json:"total_size_bytes" bson:"total_size_bytes"`
	Downloads  int64     `json:"downloads" bson:"downloads"`
	CreatedAt  time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt  time.Time `json:"updated_at" bson:"updated_at"`
}

// PyFile is the metadata record for one distribution file (a wheel or an sdist).
type PyFile struct {
	ID          string `json:"id" bson:"_id"`
	RegistryID  string `json:"registry_id" bson:"registry_id"`
	ProjectName string `json:"project_name" bson:"project_name"`
	Version     string `json:"version" bson:"version"`
	Filename    string `json:"filename" bson:"filename"`
	// Packagetype is "bdist_wheel" for wheels and "sdist" for source distributions.
	Packagetype string `json:"packagetype" bson:"packagetype"`
	// PythonVersion is the wheel's python tag (e.g. "py3") or "source" for sdists.
	PythonVersion string `json:"python_version,omitempty" bson:"python_version,omitempty"`
	// RequiresPython is the file-level "Requires-Python" specifier.
	RequiresPython string `json:"requires_python,omitempty" bson:"requires_python,omitempty"`
	// Digest is the lowercase hex SHA-256 of the distribution file.
	Digest string `json:"digest,omitempty" bson:"digest,omitempty"`
	// MD5Digest is the lowercase hex MD5 of the distribution file (legacy).
	MD5Digest string `json:"md5_digest,omitempty" bson:"md5_digest,omitempty"`
	// SizeBytes is the distribution file's size.
	SizeBytes int64 `json:"size_bytes" bson:"size_bytes"`
	// MetadataDigest is the SHA-256 of the PEP 658 core metadata file.
	MetadataDigest string `json:"metadata_digest,omitempty" bson:"metadata_digest,omitempty"`
	// HasMetadata reports whether the PEP 658 metadata file is available.
	HasMetadata bool `json:"has_metadata" bson:"has_metadata"`
	// Yanked mirrors PEP 592 yanking at the file level.
	Yanked       bool   `json:"yanked" bson:"yanked"`
	YankedReason string `json:"yanked_reason,omitempty" bson:"yanked_reason,omitempty"`
	Origin       string `json:"origin" bson:"origin"`
	// UploadTime is the file's publication timestamp.
	UploadTime time.Time `json:"upload_time" bson:"upload_time"`
	// CachedAt records when the file was mirrored from upstream.
	CachedAt  *time.Time `json:"cached_at,omitempty" bson:"cached_at,omitempty"`
	CreatedAt time.Time  `json:"created_at" bson:"created_at"`
}

// ProjectListQuery filters the project listing used by the admin API and the explorer.
type ProjectListQuery struct {
	RegistryID string
	Search     string
	Origin     string
	Page       int
	Limit      int
}

// ReleaseListQuery filters the release listing.
type ReleaseListQuery struct {
	RegistryID  string
	ProjectName string
	IncludePre  bool
	Origin      string
	Page        int
	Limit       int
}

// FileListQuery filters the file listing.
type FileListQuery struct {
	RegistryID  string
	ProjectName string
	Version     string
	IncludePre  bool
	Origin      string
	Page        int
	Limit       int
}

// MetadataStore persists PyPI-specific records strictly scoped by registry_id.
type MetadataStore interface {
	UpsertProject(ctx context.Context, p *PyProject) error
	GetProject(ctx context.Context, registryID, name string) (*PyProject, error)
	ListProjects(ctx context.Context, q ProjectListQuery) ([]*PyProject, int64, error)
	DeleteProject(ctx context.Context, registryID, name string) error
	IncrementProjectDownloads(ctx context.Context, registryID, name string) error

	UpsertRelease(ctx context.Context, r *PyRelease) error
	GetRelease(ctx context.Context, registryID, projectName, version string) (*PyRelease, error)
	ListReleases(ctx context.Context, q ReleaseListQuery) ([]*PyRelease, int64, error)
	DeleteRelease(ctx context.Context, registryID, projectName, version string) error

	UpsertFile(ctx context.Context, f *PyFile) error
	GetFile(ctx context.Context, registryID, projectName, version, filename string) (*PyFile, error)
	ListFiles(ctx context.Context, q FileListQuery) ([]*PyFile, int64, error)
	DeleteFile(ctx context.Context, registryID, projectName, version, filename string) error

	// DeleteCachedProjects removes every cached (mirrored) record for a registry.
	DeleteCachedProjects(ctx context.Context, registryID string) error
	// DeleteRegistryMetadata removes every record for a registry.
	DeleteRegistryMetadata(ctx context.Context, registryID string) error
}

// --- Memory implementation ---

// MemoryStore is an in-memory MetadataStore used in tests and single-node deployments.
type MemoryStore struct {
	mu       sync.RWMutex
	projects map[string]*PyProject
	releases map[string]*PyRelease
	files    map[string]*PyFile
}

// NewMemoryStore returns an empty in-memory metadata store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		projects: make(map[string]*PyProject),
		releases: make(map[string]*PyRelease),
		files:    make(map[string]*PyFile),
	}
}

func memProjectKey(registryID, name string) string { return registryID + "\x00" + name }
func memReleaseKey(registryID, name, version string) string {
	return registryID + "\x00" + name + "\x00" + version
}
func memFileKey(registryID, name, version, filename string) string {
	return registryID + "\x00" + name + "\x00" + version + "\x00" + filename
}

func (s *MemoryStore) UpsertProject(_ context.Context, p *PyProject) error {
	if p == nil || p.RegistryID == "" || p.Name == "" {
		return errors.New("registry_id and name are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *p
	cp.Name = NormalizeProjectName(p.Name)
	s.projects[memProjectKey(p.RegistryID, cp.Name)] = &cp
	return nil
}

func (s *MemoryStore) GetProject(_ context.Context, registryID, name string) (*PyProject, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.projects[memProjectKey(registryID, NormalizeProjectName(name))]
	if !ok {
		return nil, ErrProjectNotFound
	}
	cp := *p
	return &cp, nil
}

func (s *MemoryStore) ListProjects(_ context.Context, q ProjectListQuery) ([]*PyProject, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var matched []*PyProject
	for _, p := range s.projects {
		if p.RegistryID != q.RegistryID {
			continue
		}
		if q.Origin != "" && p.Origin != q.Origin {
			continue
		}
		if q.Search != "" && !strings.Contains(strings.ToLower(p.Name+" "+p.DisplayName+" "+p.Summary), strings.ToLower(q.Search)) {
			continue
		}
		cp := *p
		matched = append(matched, &cp)
	}
	sortProjects(matched)
	return paginateProjects(matched, q.Page, q.Limit)
}

func (s *MemoryStore) DeleteProject(_ context.Context, registryID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = NormalizeProjectName(name)
	delete(s.projects, memProjectKey(registryID, name))
	for k, rel := range s.releases {
		if rel.RegistryID == registryID && rel.ProjectName == name {
			delete(s.releases, k)
		}
	}
	for k, f := range s.files {
		if f.RegistryID == registryID && f.ProjectName == name {
			delete(s.files, k)
		}
	}
	return nil
}

func (s *MemoryStore) IncrementProjectDownloads(_ context.Context, registryID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.projects[memProjectKey(registryID, NormalizeProjectName(name))]; ok {
		p.Downloads++
	}
	return nil
}

func (s *MemoryStore) UpsertRelease(_ context.Context, r *PyRelease) error {
	if r == nil || r.RegistryID == "" || r.ProjectName == "" || r.Version == "" {
		return errors.New("registry_id, project_name and version are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *r
	cp.ProjectName = NormalizeProjectName(r.ProjectName)
	s.releases[memReleaseKey(r.RegistryID, cp.ProjectName, cp.Version)] = &cp
	return nil
}

func (s *MemoryStore) GetRelease(_ context.Context, registryID, projectName, version string) (*PyRelease, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.releases[memReleaseKey(registryID, NormalizeProjectName(projectName), version)]
	if !ok {
		return nil, ErrReleaseNotFound
	}
	cp := *r
	return &cp, nil
}

func (s *MemoryStore) ListReleases(_ context.Context, q ReleaseListQuery) ([]*PyRelease, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var matched []*PyRelease
	for _, r := range s.releases {
		if r.RegistryID != q.RegistryID {
			continue
		}
		if q.ProjectName != "" && r.ProjectName != NormalizeProjectName(q.ProjectName) {
			continue
		}
		if q.Origin != "" && r.Origin != q.Origin {
			continue
		}
		if !q.IncludePre && IsPreReleaseVersion(r.Version) {
			continue
		}
		cp := *r
		matched = append(matched, &cp)
	}
	sortReleases(matched)
	return paginateReleases(matched, q.Page, q.Limit)
}

func (s *MemoryStore) DeleteRelease(_ context.Context, registryID, projectName, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	projectName = NormalizeProjectName(projectName)
	delete(s.releases, memReleaseKey(registryID, projectName, version))
	for k, f := range s.files {
		if f.RegistryID == registryID && f.ProjectName == projectName && f.Version == version {
			delete(s.files, k)
		}
	}
	return nil
}

func (s *MemoryStore) UpsertFile(_ context.Context, f *PyFile) error {
	if f == nil || f.RegistryID == "" || f.ProjectName == "" || f.Version == "" || f.Filename == "" {
		return errors.New("registry_id, project_name, version and filename are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *f
	cp.ProjectName = NormalizeProjectName(f.ProjectName)
	s.files[memFileKey(f.RegistryID, cp.ProjectName, cp.Version, cp.Filename)] = &cp
	return nil
}

func (s *MemoryStore) GetFile(_ context.Context, registryID, projectName, version, filename string) (*PyFile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.files[memFileKey(registryID, NormalizeProjectName(projectName), version, filename)]
	if !ok {
		return nil, ErrFileNotFound
	}
	cp := *f
	return &cp, nil
}

func (s *MemoryStore) ListFiles(_ context.Context, q FileListQuery) ([]*PyFile, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var matched []*PyFile
	for _, f := range s.files {
		if f.RegistryID != q.RegistryID {
			continue
		}
		if q.ProjectName != "" && f.ProjectName != NormalizeProjectName(q.ProjectName) {
			continue
		}
		if q.Version != "" && f.Version != q.Version {
			continue
		}
		if q.Origin != "" && f.Origin != q.Origin {
			continue
		}
		if !q.IncludePre && IsPreReleaseVersion(f.Version) {
			continue
		}
		cp := *f
		matched = append(matched, &cp)
	}
	sortFiles(matched)
	return paginateFiles(matched, q.Page, q.Limit)
}

func (s *MemoryStore) DeleteFile(_ context.Context, registryID, projectName, version, filename string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, memFileKey(registryID, NormalizeProjectName(projectName), version, filename))
	return nil
}

func (s *MemoryStore) DeleteCachedProjects(_ context.Context, registryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, p := range s.projects {
		if p.RegistryID == registryID && p.Origin == OriginCached {
			delete(s.projects, k)
		}
	}
	for k, r := range s.releases {
		if r.RegistryID == registryID && r.Origin == OriginCached {
			delete(s.releases, k)
		}
	}
	for k, f := range s.files {
		if f.RegistryID == registryID && f.Origin == OriginCached {
			delete(s.files, k)
		}
	}
	return nil
}

func (s *MemoryStore) DeleteRegistryMetadata(_ context.Context, registryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, p := range s.projects {
		if p.RegistryID == registryID {
			delete(s.projects, k)
		}
	}
	for k, r := range s.releases {
		if r.RegistryID == registryID {
			delete(s.releases, k)
		}
	}
	for k, f := range s.files {
		if f.RegistryID == registryID {
			delete(s.files, k)
		}
	}
	return nil
}

// --- MongoDB implementation ---

// MongoStore persists PyPI metadata in MongoDB.
type MongoStore struct {
	projects *mongo.Collection
	releases *mongo.Collection
	files    *mongo.Collection
}

// NewMongoStore builds a Mongo-backed metadata store and ensures its indexes.
func NewMongoStore(ctx context.Context, db *mongo.Database) (*MongoStore, error) {
	s := &MongoStore{
		projects: db.Collection(ColPyProjects),
		releases: db.Collection(ColPyReleases),
		files:    db.Collection(ColPyFiles),
	}
	if err := s.EnsureIndexes(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// EnsureIndexes creates the indexes the queries rely on.
func (s *MongoStore) EnsureIndexes(ctx context.Context) error {
	_, err := s.projects.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "name", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "origin", Value: 1}}},
	})
	if err != nil {
		return err
	}
	if _, err := s.releases.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "project_name", Value: 1}, {Key: "version", Value: 1}}, Options: options.Index().SetUnique(true)},
	}); err != nil {
		return err
	}
	if _, err := s.files.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "project_name", Value: 1}, {Key: "version", Value: 1}, {Key: "filename", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "registry_id", Value: 1}, {Key: "origin", Value: 1}}},
	}); err != nil {
		return err
	}
	return nil
}

func (s *MongoStore) UpsertProject(ctx context.Context, p *PyProject) error {
	if p == nil || p.RegistryID == "" || p.Name == "" {
		return errors.New("registry_id and name are required")
	}
	cp := *p
	cp.Name = NormalizeProjectName(p.Name)
	cp.UpdatedAt = time.Now().UTC()
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = cp.UpdatedAt
	}
	_, err := s.projects.ReplaceOne(ctx, bson.M{"_id": cp.ID}, cp, options.Replace().SetUpsert(true))
	if err != nil {
		return err
	}
	return nil
}

func (s *MongoStore) GetProject(ctx context.Context, registryID, name string) (*PyProject, error) {
	var p PyProject
	err := s.projects.FindOne(ctx, bson.M{
		"registry_id": registryID,
		"name":        NormalizeProjectName(name),
	}).Decode(&p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrProjectNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *MongoStore) ListProjects(ctx context.Context, q ProjectListQuery) ([]*PyProject, int64, error) {
	filter := bson.M{"registry_id": q.RegistryID}
	if q.Origin != "" {
		filter["origin"] = q.Origin
	}
	if q.Search != "" {
		rx := primitiveRegex(q.Search)
		filter["$or"] = []bson.M{
			{"name": rx},
			{"display_name": rx},
			{"summary": rx},
		}
	}
	total, err := s.projects.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	page, limit := paginateParams(q.Page, q.Limit)
	opts := options.Find().
		SetSort(bson.D{{Key: "name", Value: 1}}).
		SetSkip(int64((page - 1) * limit)).
		SetLimit(int64(limit))
	cur, err := s.projects.Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close(ctx)
	out := make([]*PyProject, 0, limit)
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s *MongoStore) DeleteProject(ctx context.Context, registryID, name string) error {
	name = NormalizeProjectName(name)
	if _, err := s.projects.DeleteOne(ctx, bson.M{"registry_id": registryID, "name": name}); err != nil {
		return err
	}
	if _, err := s.releases.DeleteMany(ctx, bson.M{"registry_id": registryID, "project_name": name}); err != nil {
		return err
	}
	_, err := s.files.DeleteMany(ctx, bson.M{"registry_id": registryID, "project_name": name})
	return err
}

func (s *MongoStore) IncrementProjectDownloads(ctx context.Context, registryID, name string) error {
	_, err := s.projects.UpdateOne(ctx,
		bson.M{"registry_id": registryID, "name": NormalizeProjectName(name)},
		bson.M{"$inc": bson.M{"downloads": 1}},
	)
	return err
}

func (s *MongoStore) UpsertRelease(ctx context.Context, r *PyRelease) error {
	if r == nil || r.RegistryID == "" || r.ProjectName == "" || r.Version == "" {
		return errors.New("registry_id, project_name and version are required")
	}
	cp := *r
	cp.ProjectName = NormalizeProjectName(r.ProjectName)
	cp.UpdatedAt = time.Now().UTC()
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = cp.UpdatedAt
	}
	_, err := s.releases.ReplaceOne(ctx, bson.M{"_id": cp.ID}, cp, options.Replace().SetUpsert(true))
	return err
}

func (s *MongoStore) GetRelease(ctx context.Context, registryID, projectName, version string) (*PyRelease, error) {
	var r PyRelease
	err := s.releases.FindOne(ctx, bson.M{
		"registry_id":  registryID,
		"project_name": NormalizeProjectName(projectName),
		"version":      version,
	}).Decode(&r)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrReleaseNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *MongoStore) ListReleases(ctx context.Context, q ReleaseListQuery) ([]*PyRelease, int64, error) {
	filter := bson.M{"registry_id": q.RegistryID}
	if q.ProjectName != "" {
		filter["project_name"] = NormalizeProjectName(q.ProjectName)
	}
	if q.Origin != "" {
		filter["origin"] = q.Origin
	}
	total, err := s.releases.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	page, limit := paginateParams(q.Page, q.Limit)
	opts := options.Find().
		SetSort(bson.D{{Key: "project_name", Value: 1}, {Key: "version", Value: 1}}).
		SetSkip(int64((page - 1) * limit)).
		SetLimit(int64(limit))
	cur, err := s.releases.Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close(ctx)
	out := make([]*PyRelease, 0, limit)
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s *MongoStore) DeleteRelease(ctx context.Context, registryID, projectName, version string) error {
	projectName = NormalizeProjectName(projectName)
	if _, err := s.releases.DeleteOne(ctx, bson.M{
		"registry_id":  registryID,
		"project_name": projectName,
		"version":      version,
	}); err != nil {
		return err
	}
	_, err := s.files.DeleteMany(ctx, bson.M{
		"registry_id":  registryID,
		"project_name": projectName,
		"version":      version,
	})
	return err
}

func (s *MongoStore) UpsertFile(ctx context.Context, f *PyFile) error {
	if f == nil || f.RegistryID == "" || f.ProjectName == "" || f.Version == "" || f.Filename == "" {
		return errors.New("registry_id, project_name, version and filename are required")
	}
	cp := *f
	cp.ProjectName = NormalizeProjectName(f.ProjectName)
	cp.CreatedAt = time.Now().UTC()
	_, err := s.files.ReplaceOne(ctx, bson.M{"_id": cp.ID}, cp, options.Replace().SetUpsert(true))
	return err
}

func (s *MongoStore) GetFile(ctx context.Context, registryID, projectName, version, filename string) (*PyFile, error) {
	var f PyFile
	err := s.files.FindOne(ctx, bson.M{
		"registry_id":  registryID,
		"project_name": NormalizeProjectName(projectName),
		"version":      version,
		"filename":     filename,
	}).Decode(&f)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrFileNotFound
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (s *MongoStore) ListFiles(ctx context.Context, q FileListQuery) ([]*PyFile, int64, error) {
	filter := bson.M{"registry_id": q.RegistryID}
	if q.ProjectName != "" {
		filter["project_name"] = NormalizeProjectName(q.ProjectName)
	}
	if q.Version != "" {
		filter["version"] = q.Version
	}
	if q.Origin != "" {
		filter["origin"] = q.Origin
	}
	total, err := s.files.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	page, limit := paginateParams(q.Page, q.Limit)
	opts := options.Find().
		SetSort(bson.D{{Key: "project_name", Value: 1}, {Key: "version", Value: 1}, {Key: "filename", Value: 1}}).
		SetSkip(int64((page - 1) * limit)).
		SetLimit(int64(limit))
	cur, err := s.files.Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close(ctx)
	out := make([]*PyFile, 0, limit)
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s *MongoStore) DeleteFile(ctx context.Context, registryID, projectName, version, filename string) error {
	_, err := s.files.DeleteOne(ctx, bson.M{
		"registry_id":  registryID,
		"project_name": NormalizeProjectName(projectName),
		"version":      version,
		"filename":     filename,
	})
	return err
}

func (s *MongoStore) DeleteCachedProjects(ctx context.Context, registryID string) error {
	filter := bson.M{"registry_id": registryID, "origin": OriginCached}
	if _, err := s.projects.DeleteMany(ctx, filter); err != nil {
		return err
	}
	if _, err := s.releases.DeleteMany(ctx, filter); err != nil {
		return err
	}
	_, err := s.files.DeleteMany(ctx, filter)
	return err
}

func (s *MongoStore) DeleteRegistryMetadata(ctx context.Context, registryID string) error {
	filter := bson.M{"registry_id": registryID}
	if _, err := s.projects.DeleteMany(ctx, filter); err != nil {
		return err
	}
	if _, err := s.releases.DeleteMany(ctx, filter); err != nil {
		return err
	}
	_, err := s.files.DeleteMany(ctx, filter)
	return err
}

// --- shared helpers ---

func primitiveRegex(pattern string) bson.M {
	return bson.M{"$regex": pattern, "$options": "i"}
}

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

func paginateProjects(items []*PyProject, page, limit int) ([]*PyProject, int64, error) {
	total := int64(len(items))
	page, limit = paginateParams(page, limit)
	start := (page - 1) * limit
	if start >= len(items) {
		return []*PyProject{}, total, nil
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], total, nil
}

func paginateReleases(items []*PyRelease, page, limit int) ([]*PyRelease, int64, error) {
	total := int64(len(items))
	page, limit = paginateParams(page, limit)
	start := (page - 1) * limit
	if start >= len(items) {
		return []*PyRelease{}, total, nil
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], total, nil
}

func paginateFiles(items []*PyFile, page, limit int) ([]*PyFile, int64, error) {
	total := int64(len(items))
	page, limit = paginateParams(page, limit)
	start := (page - 1) * limit
	if start >= len(items) {
		return []*PyFile{}, total, nil
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], total, nil
}

// sortProjects orders projects by normalized name.
func sortProjects(items []*PyProject) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].DisplayName < items[j].DisplayName
	})
}

// sortReleases orders releases by PEP 440 version ordering, oldest first.
func sortReleases(items []*PyRelease) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].ProjectName != items[j].ProjectName {
			return items[i].ProjectName < items[j].ProjectName
		}
		return CompareVersions(items[i].Version, items[j].Version) < 0
	})
}

// sortFiles orders files by project, then PEP 440 version, then filename.
func sortFiles(items []*PyFile) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].ProjectName != items[j].ProjectName {
			return items[i].ProjectName < items[j].ProjectName
		}
		if c := CompareVersions(items[i].Version, items[j].Version); c != 0 {
			return c < 0
		}
		return items[i].Filename < items[j].Filename
	})
}
