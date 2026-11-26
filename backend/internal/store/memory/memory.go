package memstore

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/amkargar/depyard/backend/internal/admin"
	"github.com/amkargar/depyard/backend/internal/audit"
	"github.com/amkargar/depyard/backend/internal/auth"
	"github.com/amkargar/depyard/backend/internal/metrics"
	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/storage"
)

// Store provides thread-safe in-memory persistence for tests and standalone local development.
type Store struct {
	mu        sync.RWMutex
	instances map[string]*registry.Instance
	blobs     map[string]*storage.Blob
	refs      map[string]*storage.ArtifactRef // key: registryID + "::" + artifactKey
	users     map[string]*auth.User
	sessions  map[string]*auth.Session // key: tokenHash
	tokens    map[string]*auth.AccessToken
	stats     map[string]*metrics.BucketRecord // key: id
	auditLogs []*audit.Entry
	settings  *admin.PlatformSettings
}

func New() *Store {
	return &Store{
		instances: make(map[string]*registry.Instance),
		blobs:     make(map[string]*storage.Blob),
		refs:      make(map[string]*storage.ArtifactRef),
		users:     make(map[string]*auth.User),
		sessions:  make(map[string]*auth.Session),
		tokens:    make(map[string]*auth.AccessToken),
		stats:     make(map[string]*metrics.BucketRecord),
	}
}

func refKey(registryID, artifactKey string) string {
	return registryID + "::" + artifactKey
}

// --- registry.InstanceStore ---

func (s *Store) CreateInstance(_ context.Context, inst *registry.Instance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.instances {
		if existing.Slug == inst.Slug {
			return registry.ErrSlugExists
		}
	}
	s.instances[inst.ID] = inst.Clone()
	return nil
}

func (s *Store) GetInstanceByID(_ context.Context, id string) (*registry.Instance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inst, ok := s.instances[id]
	if !ok {
		return nil, registry.ErrInstanceNotFound
	}
	return inst.Clone(), nil
}

func (s *Store) GetInstanceBySlug(_ context.Context, slug string) (*registry.Instance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, inst := range s.instances {
		if inst.Slug == slug {
			return inst.Clone(), nil
		}
	}
	return nil, registry.ErrInstanceNotFound
}

func (s *Store) ListInstances(_ context.Context) ([]*registry.Instance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*registry.Instance, 0, len(s.instances))
	for _, inst := range s.instances {
		out = append(out, inst.Clone())
	}
	return out, nil
}

func (s *Store) UpdateInstance(_ context.Context, inst *registry.Instance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.instances[inst.ID]; !ok {
		return registry.ErrInstanceNotFound
	}
	s.instances[inst.ID] = inst.Clone()
	return nil
}

func (s *Store) DeleteInstance(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.instances[id]; !ok {
		return registry.ErrInstanceNotFound
	}
	delete(s.instances, id)
	return nil
}

// --- storage.RefStore ---

func (s *Store) UpsertBlob(_ context.Context, blob *storage.Blob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.blobs[blob.Digest]; ok {
		cur.LastAccessedAt = blob.LastAccessedAt
		return nil
	}
	cp := *blob
	s.blobs[blob.Digest] = &cp
	return nil
}

func (s *Store) GetBlob(_ context.Context, digest string) (*storage.Blob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.blobs[digest]
	if !ok {
		return nil, storage.ErrBlobNotFound
	}
	cp := *b
	return &cp, nil
}

func (s *Store) DeleteBlob(_ context.Context, digest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.blobs, digest)
	return nil
}

func (s *Store) TouchBlob(_ context.Context, digest string, ts time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.blobs[digest]; ok {
		b.LastAccessedAt = ts
	}
	return nil
}

func (s *Store) UpsertArtifactRef(_ context.Context, ref *storage.ArtifactRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := refKey(ref.RegistryID, ref.ArtifactKey)
	cp := *ref
	if cur, ok := s.refs[k]; ok {
		cp.ID = cur.ID
		cp.CreatedAt = cur.CreatedAt
	}
	s.refs[k] = &cp
	return nil
}

func (s *Store) GetArtifactRef(_ context.Context, registryID, artifactKey string) (*storage.ArtifactRef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ref, ok := s.refs[refKey(registryID, artifactKey)]
	if !ok {
		return nil, storage.ErrRefNotFound
	}
	cp := *ref
	return &cp, nil
}

func (s *Store) DeleteArtifactRef(_ context.Context, registryID, artifactKey string) (*storage.ArtifactRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := refKey(registryID, artifactKey)
	ref, ok := s.refs[k]
	if !ok {
		return nil, storage.ErrRefNotFound
	}
	delete(s.refs, k)
	cp := *ref
	return &cp, nil
}

func (s *Store) DeleteRegistryRefs(_ context.Context, registryID string) ([]*storage.ArtifactRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed []*storage.ArtifactRef
	for k, ref := range s.refs {
		if ref.RegistryID == registryID {
			cp := *ref
			removed = append(removed, &cp)
			delete(s.refs, k)
		}
	}
	return removed, nil
}

func (s *Store) CountRefsForDigest(_ context.Context, digest string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var count int64
	for _, ref := range s.refs {
		if ref.Digest == digest {
			count++
		}
	}
	return count, nil
}

func (s *Store) TouchArtifactRef(_ context.Context, registryID, artifactKey string, ts time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ref, ok := s.refs[refKey(registryID, artifactKey)]; ok {
		ref.LastAccessedAt = ts
	}
	return nil
}

func (s *Store) ListEvictableCachedRefs(_ context.Context, registryID string) ([]*storage.ArtifactRef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*storage.ArtifactRef
	for _, ref := range s.refs {
		if ref.RegistryID == registryID && ref.Origin == storage.OriginCached && !ref.Protected {
			cp := *ref
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastAccessedAt.Before(out[j].LastAccessedAt)
	})
	return out, nil
}

func (s *Store) GetGlobalStorageStats(_ context.Context) (storage.GlobalStorageStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out storage.GlobalStorageStats
	for _, b := range s.blobs {
		out.PhysicalBytes += b.SizeBytes
		out.PhysicalBlobCount++
	}
	for _, r := range s.refs {
		out.LogicalBytes += r.SizeBytes
		out.LogicalRefCount++
	}
	if out.LogicalBytes > out.PhysicalBytes {
		out.DeduplicationSaved = out.LogicalBytes - out.PhysicalBytes
	}
	if out.LogicalBytes > 0 {
		out.DeduplicationRatio = float64(out.DeduplicationSaved) / float64(out.LogicalBytes)
	}
	return out, nil
}

func (s *Store) GetRegistryStorageStats(_ context.Context, registryID string) (storage.RegistryStorageStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := storage.RegistryStorageStats{RegistryID: registryID}
	for _, r := range s.refs {
		if r.RegistryID != registryID {
			continue
		}
		out.LogicalBytes += r.SizeBytes
		out.TotalRefs++
		if r.Origin == storage.OriginHosted {
			out.HostedBytes += r.SizeBytes
			out.HostedRefs++
		} else {
			out.CachedBytes += r.SizeBytes
			out.CachedRefs++
		}
	}
	return out, nil
}

// --- auth.Store ---

func (s *Store) CreateUser(_ context.Context, user *auth.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.users {
		if existing.Username == user.Username {
			return auth.ErrAlreadyExists
		}
	}
	cp := *user
	s.users[user.ID] = &cp
	return nil
}

func (s *Store) GetUserByID(_ context.Context, id string) (*auth.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[id]
	if !ok {
		return nil, auth.ErrNotFound
	}
	cp := *u
	return &cp, nil
}

func (s *Store) GetUserByUsername(_ context.Context, username string) (*auth.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if u.Username == username {
			cp := *u
			return &cp, nil
		}
	}
	return nil, auth.ErrNotFound
}

func (s *Store) ListUsers(_ context.Context) ([]*auth.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*auth.User, 0, len(s.users))
	for _, u := range s.users {
		cp := *u
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) UpdateUser(_ context.Context, user *auth.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[user.ID]; !ok {
		return auth.ErrNotFound
	}
	cp := *user
	s.users[user.ID] = &cp
	return nil
}

func (s *Store) DeleteUser(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[id]; !ok {
		return auth.ErrNotFound
	}
	delete(s.users, id)
	return nil
}

func (s *Store) CountActiveAdmins(_ context.Context) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var count int64
	for _, u := range s.users {
		if u.Role == auth.RoleAdmin && !u.Disabled {
			count++
		}
	}
	return count, nil
}

func (s *Store) CountUsers(_ context.Context) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return int64(len(s.users)), nil
}

func (s *Store) CreateSession(_ context.Context, session *auth.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *session
	s.sessions[session.TokenHash] = &cp
	return nil
}

func (s *Store) GetSessionByTokenHash(_ context.Context, tokenHash string) (*auth.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[tokenHash]
	if !ok {
		return nil, auth.ErrNotFound
	}
	cp := *sess
	return &cp, nil
}

func (s *Store) DeleteSession(_ context.Context, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, tokenHash)
	return nil
}

func (s *Store) DeleteSessionsByUser(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, sess := range s.sessions {
		if sess.UserID == userID {
			delete(s.sessions, k)
		}
	}
	return nil
}

func (s *Store) CreateToken(_ context.Context, token *auth.AccessToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *token
	cp.Scopes = append([]auth.Permission(nil), token.Scopes...)
	cp.Namespaces = append([]string(nil), token.Namespaces...)
	s.tokens[token.ID] = &cp
	return nil
}

func (s *Store) GetTokenByID(_ context.Context, id string) (*auth.AccessToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tok, ok := s.tokens[id]
	if !ok {
		return nil, auth.ErrNotFound
	}
	cp := *tok
	return &cp, nil
}

func (s *Store) GetTokenByHash(_ context.Context, tokenHash string) (*auth.AccessToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, tok := range s.tokens {
		if tok.TokenHash == tokenHash {
			cp := *tok
			cp.Scopes = append([]auth.Permission(nil), tok.Scopes...)
			cp.Namespaces = append([]string(nil), tok.Namespaces...)
			return &cp, nil
		}
	}
	return nil, auth.ErrNotFound
}

func (s *Store) ListTokens(_ context.Context, registryID string) ([]*auth.AccessToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*auth.AccessToken
	for _, tok := range s.tokens {
		if registryID != "" && tok.RegistryID != registryID {
			continue
		}
		cp := *tok
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) ListTokensByUser(_ context.Context, userID string) ([]*auth.AccessToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*auth.AccessToken
	for _, tok := range s.tokens {
		if tok.UserID != userID {
			continue
		}
		cp := *tok
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) UpdateTokenLastUsed(_ context.Context, id string, ts time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tok, ok := s.tokens[id]; ok {
		t := ts
		tok.LastUsedAt = &t
	}
	return nil
}

func (s *Store) RevokeToken(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, ok := s.tokens[id]
	if !ok {
		return auth.ErrNotFound
	}
	tok.Revoked = true
	return nil
}

func (s *Store) RevokeTokensByUser(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tok := range s.tokens {
		if tok.UserID == userID {
			tok.Revoked = true
		}
	}
	return nil
}

func (s *Store) DeleteToken(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tokens[id]; !ok {
		return auth.ErrNotFound
	}
	delete(s.tokens, id)
	return nil
}

// --- metrics.Store ---

func (s *Store) IncrementBucket(_ context.Context, rec *metrics.BucketRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.stats[rec.ID]
	if !ok {
		cp := *rec
		s.stats[rec.ID] = &cp
		return nil
	}
	cur.Downloads += rec.Downloads
	cur.Uploads += rec.Uploads
	cur.BytesServed += rec.BytesServed
	cur.BytesReceived += rec.BytesReceived
	cur.CacheHits += rec.CacheHits
	cur.CacheMisses += rec.CacheMisses
	cur.UpstreamErrors += rec.UpstreamErrors
	cur.UpdatedAt = rec.UpdatedAt
	return nil
}

func (s *Store) GetRegistryTotals(_ context.Context, registryID string) (metrics.BucketRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := metrics.BucketRecord{RegistryID: registryID}
	for _, rec := range s.stats {
		if rec.RegistryID != registryID {
			continue
		}
		out.Downloads += rec.Downloads
		out.Uploads += rec.Uploads
		out.BytesServed += rec.BytesServed
		out.BytesReceived += rec.BytesReceived
		out.CacheHits += rec.CacheHits
		out.CacheMisses += rec.CacheMisses
		out.UpstreamErrors += rec.UpstreamErrors
	}
	return out, nil
}

func (s *Store) GetAllRegistryTotals(_ context.Context) (map[string]metrics.BucketRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]metrics.BucketRecord)
	for _, rec := range s.stats {
		cur := out[rec.RegistryID]
		cur.RegistryID = rec.RegistryID
		cur.Downloads += rec.Downloads
		cur.Uploads += rec.Uploads
		cur.BytesServed += rec.BytesServed
		cur.BytesReceived += rec.BytesReceived
		cur.CacheHits += rec.CacheHits
		cur.CacheMisses += rec.CacheMisses
		cur.UpstreamErrors += rec.UpstreamErrors
		out[rec.RegistryID] = cur
	}
	return out, nil
}

func (s *Store) ListRecentBuckets(_ context.Context, registryID string, days int) ([]*metrics.BucketRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
	var out []*metrics.BucketRecord
	for _, rec := range s.stats {
		if rec.BucketDate < cutoff {
			continue
		}
		if registryID != "" && rec.RegistryID != registryID {
			continue
		}
		cp := *rec
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].BucketDate != out[j].BucketDate {
			return out[i].BucketDate < out[j].BucketDate
		}
		return out[i].RegistryID < out[j].RegistryID
	})
	return out, nil
}

func (s *Store) DeleteRegistryStats(_ context.Context, registryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, rec := range s.stats {
		if rec.RegistryID == registryID {
			delete(s.stats, k)
		}
	}
	return nil
}

// --- audit.Store ---

func (s *Store) InsertAuditLog(_ context.Context, entry *audit.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *entry
	s.auditLogs = append(s.auditLogs, &cp)
	return nil
}

func (s *Store) ListAuditLogs(_ context.Context, q audit.Query) ([]*audit.Entry, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var filtered []*audit.Entry
	for i := len(s.auditLogs) - 1; i >= 0; i-- {
		e := s.auditLogs[i]
		if q.RegistryID != "" && e.RegistryID != q.RegistryID {
			continue
		}
		if q.Action != "" && e.Action != q.Action {
			continue
		}
		if q.ActorName != "" && e.ActorName != q.ActorName {
			continue
		}
		cp := *e
		filtered = append(filtered, &cp)
	}
	total := int64(len(filtered))
	if q.Offset >= len(filtered) {
		return []*audit.Entry{}, total, nil
	}
	end := q.Offset + q.Limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[q.Offset:end], total, nil
}

// --- admin.SettingsStore ---

func (s *Store) GetSettings(_ context.Context) (*admin.PlatformSettings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.settings == nil {
		return nil, nil
	}
	cp := *s.settings
	return &cp, nil
}

func (s *Store) SaveSettings(_ context.Context, st *admin.PlatformSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *st
	cp.ID = "global"
	cp.UpdatedAt = time.Now().UTC()
	s.settings = &cp
	return nil
}
