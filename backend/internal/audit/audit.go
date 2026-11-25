package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Entry represents a single audit log record stored in the `audit_logs` collection.
type Entry struct {
	ID         string         `json:"id" bson:"_id"`
	Timestamp  time.Time      `json:"timestamp" bson:"timestamp"`
	ActorID    string         `json:"actor_id,omitempty" bson:"actor_id,omitempty"`
	ActorName  string         `json:"actor_name" bson:"actor_name"`
	ActorType  string         `json:"actor_type" bson:"actor_type"` // "user", "token", "system", "anonymous"
	Action     string         `json:"action" bson:"action"`
	RegistryID string         `json:"registry_id,omitempty" bson:"registry_id,omitempty"`
	Target     string         `json:"target,omitempty" bson:"target,omitempty"`
	IPAddress  string         `json:"ip_address,omitempty" bson:"ip_address,omitempty"`
	Success    bool           `json:"success" bson:"success"`
	Details    map[string]any `json:"details,omitempty" bson:"details,omitempty"`
}

// Query filters audit log queries.
type Query struct {
	RegistryID string
	Action     string
	ActorName  string
	Limit      int
	Offset     int
}

// Store persists audit log entries.
type Store interface {
	InsertAuditLog(ctx context.Context, entry *Entry) error
	ListAuditLogs(ctx context.Context, q Query) ([]*Entry, int64, error)
}

// Service records and queries audit logs.
type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) Record(ctx context.Context, entry Entry) {
	if s == nil || s.store == nil {
		return
	}
	if entry.ID == "" {
		entry.ID = newID()
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	_ = s.store.InsertAuditLog(ctx, &entry)
}

func (s *Service) List(ctx context.Context, q Query) ([]*Entry, int64, error) {
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	return s.store.ListAuditLogs(ctx, q)
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
