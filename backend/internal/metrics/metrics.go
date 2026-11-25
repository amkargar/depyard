package metrics

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// BucketRecord is a bounded-cardinality daily/hourly statistics record stored in `statistics`.
type BucketRecord struct {
	ID             string    `json:"id" bson:"_id"` // "<registry_id>:<YYYY-MM-DD>"
	RegistryID     string    `json:"registry_id" bson:"registry_id"`
	BucketDate     string    `json:"bucket_date" bson:"bucket_date"` // YYYY-MM-DD UTC
	Downloads      int64     `json:"downloads" bson:"downloads"`
	Uploads        int64     `json:"uploads" bson:"uploads"`
	BytesServed    int64     `json:"bytes_served" bson:"bytes_served"`
	BytesReceived  int64     `json:"bytes_received" bson:"bytes_received"`
	CacheHits      int64     `json:"cache_hits" bson:"cache_hits"`
	CacheMisses    int64     `json:"cache_misses" bson:"cache_misses"`
	UpstreamErrors int64     `json:"upstream_errors" bson:"upstream_errors"`
	UpdatedAt      time.Time `json:"updated_at" bson:"updated_at"`
}

// UpstreamHealth stores the latest observed upstream reachability for a registry.
type UpstreamHealth struct {
	Checked    bool       `json:"checked"`
	Available  bool       `json:"available"`
	StatusCode int        `json:"status_code,omitempty"`
	LatencyMs  int64      `json:"latency_ms"`
	Error      string     `json:"error,omitempty"`
	CheckedAt  *time.Time `json:"checked_at,omitempty"`
}

// RegistrySnapshot represents aggregated counters + live gauges for one registry instance.
type RegistrySnapshot struct {
	RegistryID      string         `json:"registry_id"`
	Downloads       int64          `json:"downloads"`
	Uploads         int64          `json:"uploads"`
	BytesServed     int64          `json:"bytes_served"`
	BytesReceived   int64          `json:"bytes_received"`
	CacheHits       int64          `json:"cache_hits"`
	CacheMisses     int64          `json:"cache_misses"`
	CacheHitRatio   float64        `json:"cache_hit_ratio"`
	UpstreamErrors  int64          `json:"upstream_errors"`
	ActiveTransfers int64          `json:"active_transfers"`
	Upstream        UpstreamHealth `json:"upstream"`
}

// Store persists bounded-cardinality statistics records in the `statistics` collection.
type Store interface {
	IncrementBucket(ctx context.Context, rec *BucketRecord) error
	GetRegistryTotals(ctx context.Context, registryID string) (BucketRecord, error)
	GetAllRegistryTotals(ctx context.Context) (map[string]BucketRecord, error)
	ListRecentBuckets(ctx context.Context, registryID string, days int) ([]*BucketRecord, error)
	DeleteRegistryStats(ctx context.Context, registryID string) error
}

// Collector provides unified global and per-registry metrics without duplicating logic per ecosystem.
type Collector struct {
	store Store

	mu         sync.RWMutex
	pending    map[string]*BucketRecord
	active     map[string]*atomic.Int64
	upstream   map[string]UpstreamHealth
	globalAct  atomic.Int64
}

func NewCollector(store Store) *Collector {
	return &Collector{
		store:    store,
		pending:  make(map[string]*BucketRecord),
		active:   make(map[string]*atomic.Int64),
		upstream: make(map[string]UpstreamHealth),
	}
}

// BeginTransfer increments the active transfer gauge and returns a stop func.
func (c *Collector) BeginTransfer(registryID string) func() {
	c.globalAct.Add(1)
	g := c.getActiveGauge(registryID)
	g.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			g.Add(-1)
			c.globalAct.Add(-1)
		})
	}
}

func (c *Collector) ActiveTransfersGlobal() int64 {
	return c.globalAct.Load()
}

func (c *Collector) ActiveTransfersRegistry(registryID string) int64 {
	c.mu.RLock()
	g, ok := c.active[registryID]
	c.mu.RUnlock()
	if !ok {
		return 0
	}
	return g.Load()
}

func (c *Collector) RecordDownload(registryID string, bytes int64, cacheHit bool) {
	c.mutatePending(registryID, func(b *BucketRecord) {
		b.Downloads++
		b.BytesServed += bytes
		if cacheHit {
			b.CacheHits++
		} else {
			b.CacheMisses++
		}
	})
}

func (c *Collector) RecordUpload(registryID string, bytes int64) {
	c.mutatePending(registryID, func(b *BucketRecord) {
		b.Uploads++
		b.BytesReceived += bytes
	})
}

func (c *Collector) RecordCacheLookup(registryID string, hit bool) {
	c.mutatePending(registryID, func(b *BucketRecord) {
		if hit {
			b.CacheHits++
		} else {
			b.CacheMisses++
		}
	})
}

func (c *Collector) RecordUpstreamError(registryID string) {
	c.mutatePending(registryID, func(b *BucketRecord) {
		b.UpstreamErrors++
	})
}

func (c *Collector) SetUpstreamHealth(registryID string, h UpstreamHealth) {
	c.mu.Lock()
	c.upstream[registryID] = h
	c.mu.Unlock()
}

func (c *Collector) GetUpstreamHealth(registryID string) UpstreamHealth {
	c.mu.RLock()
	h := c.upstream[registryID]
	c.mu.RUnlock()
	return h
}

// Flush persists accumulated in-memory counter deltas into the statistics store.
func (c *Collector) Flush(ctx context.Context) error {
	if c.store == nil {
		return nil
	}
	c.mu.Lock()
	if len(c.pending) == 0 {
		c.mu.Unlock()
		return nil
	}
	batch := c.pending
	c.pending = make(map[string]*BucketRecord)
	c.mu.Unlock()

	var firstErr error
	for _, rec := range batch {
		if err := c.store.IncrementBucket(ctx, rec); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// GetRegistrySnapshot returns combined persisted + pending metrics and live gauges for a registry.
func (c *Collector) GetRegistrySnapshot(ctx context.Context, registryID string) (RegistrySnapshot, error) {
	var tot BucketRecord
	if c.store != nil {
		var err error
		tot, err = c.store.GetRegistryTotals(ctx, registryID)
		if err != nil {
			return RegistrySnapshot{}, err
		}
	}
	c.addPendingForRegistry(registryID, &tot)
	return c.toSnapshot(registryID, tot), nil
}

// GetAllSnapshots returns snapshots for every registry with stats or live state.
func (c *Collector) GetAllSnapshots(ctx context.Context, registryIDs []string) (map[string]RegistrySnapshot, error) {
	var totals map[string]BucketRecord
	if c.store != nil {
		var err error
		totals, err = c.store.GetAllRegistryTotals(ctx)
		if err != nil {
			return nil, err
		}
	}
	if totals == nil {
		totals = make(map[string]BucketRecord)
	}
	c.mu.RLock()
	for _, p := range c.pending {
		cur := totals[p.RegistryID]
		cur.RegistryID = p.RegistryID
		cur.Downloads += p.Downloads
		cur.Uploads += p.Uploads
		cur.BytesServed += p.BytesServed
		cur.BytesReceived += p.BytesReceived
		cur.CacheHits += p.CacheHits
		cur.CacheMisses += p.CacheMisses
		cur.UpstreamErrors += p.UpstreamErrors
		totals[p.RegistryID] = cur
	}
	c.mu.RUnlock()

	out := make(map[string]RegistrySnapshot, len(registryIDs))
	for _, id := range registryIDs {
		out[id] = c.toSnapshot(id, totals[id])
	}
	for id, tot := range totals {
		if _, exists := out[id]; !exists {
			out[id] = c.toSnapshot(id, tot)
		}
	}
	return out, nil
}

func (c *Collector) ListRecentBuckets(ctx context.Context, registryID string, days int) ([]*BucketRecord, error) {
	if days <= 0 || days > 90 {
		days = 14
	}
	_ = c.Flush(ctx)
	if c.store == nil {
		return nil, nil
	}
	return c.store.ListRecentBuckets(ctx, registryID, days)
}

func (c *Collector) DeleteRegistry(ctx context.Context, registryID string) {
	c.mu.Lock()
	for k, p := range c.pending {
		if p.RegistryID == registryID {
			delete(c.pending, k)
		}
	}
	delete(c.active, registryID)
	delete(c.upstream, registryID)
	c.mu.Unlock()
	if c.store != nil {
		_ = c.store.DeleteRegistryStats(ctx, registryID)
	}
}

func (c *Collector) toSnapshot(registryID string, tot BucketRecord) RegistrySnapshot {
	var ratio float64
	totalLookups := tot.CacheHits + tot.CacheMisses
	if totalLookups > 0 {
		ratio = float64(tot.CacheHits) / float64(totalLookups)
	}
	return RegistrySnapshot{
		RegistryID:      registryID,
		Downloads:       tot.Downloads,
		Uploads:         tot.Uploads,
		BytesServed:     tot.BytesServed,
		BytesReceived:   tot.BytesReceived,
		CacheHits:       tot.CacheHits,
		CacheMisses:     tot.CacheMisses,
		CacheHitRatio:   ratio,
		UpstreamErrors:  tot.UpstreamErrors,
		ActiveTransfers: c.ActiveTransfersRegistry(registryID),
		Upstream:        c.GetUpstreamHealth(registryID),
	}
}

func (c *Collector) mutatePending(registryID string, fn func(*BucketRecord)) {
	date := time.Now().UTC().Format("2006-01-02")
	key := registryID + ":" + date
	c.mu.Lock()
	rec, ok := c.pending[key]
	if !ok {
		rec = &BucketRecord{
			ID:         key,
			RegistryID: registryID,
			BucketDate: date,
		}
		c.pending[key] = rec
	}
	fn(rec)
	rec.UpdatedAt = time.Now().UTC()
	c.mu.Unlock()
}

func (c *Collector) addPendingForRegistry(registryID string, dst *BucketRecord) {
	c.mu.RLock()
	for _, p := range c.pending {
		if p.RegistryID != registryID {
			continue
		}
		dst.Downloads += p.Downloads
		dst.Uploads += p.Uploads
		dst.BytesServed += p.BytesServed
		dst.BytesReceived += p.BytesReceived
		dst.CacheHits += p.CacheHits
		dst.CacheMisses += p.CacheMisses
		dst.UpstreamErrors += p.UpstreamErrors
	}
	c.mu.RUnlock()
}

func (c *Collector) getActiveGauge(registryID string) *atomic.Int64 {
	c.mu.RLock()
	g, ok := c.active[registryID]
	c.mu.RUnlock()
	if ok {
		return g
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if g, ok = c.active[registryID]; ok {
		return g
	}
	g = &atomic.Int64{}
	c.active[registryID] = g
	return g
}
