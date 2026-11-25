package tasks

import (
	"context"
	"log/slog"
	"time"

	"github.com/amkargar/mirror-registry/backend/internal/cache"
	"github.com/amkargar/mirror-registry/backend/internal/metrics"
	"github.com/amkargar/mirror-registry/backend/internal/registry"
)

// Runner executes lightweight periodic shared maintenance tasks.
type Runner struct {
	manager       *registry.Manager
	cache         *cache.Coordinator
	metrics       *metrics.Collector
	evictInterval time.Duration
	healthInterval time.Duration
	flushInterval time.Duration
	logger        *slog.Logger
}

func NewRunner(
	manager *registry.Manager,
	cacheCoord *cache.Coordinator,
	collector *metrics.Collector,
	evictInterval, healthInterval, flushInterval time.Duration,
	logger *slog.Logger,
) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		manager:        manager,
		cache:          cacheCoord,
		metrics:        collector,
		evictInterval:  evictInterval,
		healthInterval: healthInterval,
		flushInterval:  flushInterval,
		logger:         logger,
	}
}

// Start launches background maintenance loops and stops cleanly when ctx is cancelled.
func (r *Runner) Start(ctx context.Context) {
	if r.evictInterval > 0 {
		go r.loop(ctx, "cache-eviction", r.evictInterval, r.runCacheEviction)
	}
	if r.healthInterval > 0 {
		go r.loop(ctx, "upstream-health", r.healthInterval, r.runHealthChecks)
	}
	if r.flushInterval > 0 {
		go r.loop(ctx, "metrics-flush", r.flushInterval, r.runMetricsFlush)
	}
}

func (r *Runner) loop(ctx context.Context, name string, interval time.Duration, fn func(context.Context)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fn(ctx)
		}
	}
}

func (r *Runner) runCacheEviction(ctx context.Context) {
	for _, rt := range r.manager.AllRuntimes() {
		inst := rt.Instance
		if !inst.Enabled || (inst.Mode != registry.ModeProxy && inst.Mode != registry.ModeHybrid) {
			continue
		}
		if inst.CachePolicy.ArchiveTTLSeconds <= 0 && inst.CachePolicy.MaxSizeBytes <= 0 {
			continue
		}
		res, err := r.cache.EvictRegistry(ctx, inst.ID, inst.CachePolicy.ArchiveTTLSeconds, inst.CachePolicy.MaxSizeBytes)
		if err != nil {
			r.logger.Warn("cache eviction failed", "registry", inst.Slug, "error", err)
		} else if res.RemovedRefs > 0 {
			r.logger.Info("cache eviction completed", "registry", inst.Slug, "removed_refs", res.RemovedRefs, "freed_bytes", res.FreedLogical)
		}
	}
}

func (r *Runner) runHealthChecks(ctx context.Context) {
	for _, rt := range r.manager.AllRuntimes() {
		inst := rt.Instance
		if !inst.Enabled || inst.UpstreamURL == "" || (inst.Mode != registry.ModeProxy && inst.Mode != registry.ModeHybrid) {
			continue
		}
		checker, ok := rt.Registry.(registry.HealthChecker)
		if !ok {
			continue
		}
		res := checker.CheckHealth(ctx)
		now := res.CheckedAt
		r.metrics.SetUpstreamHealth(inst.ID, metrics.UpstreamHealth{
			Checked:    true,
			Available:  res.Available,
			StatusCode: res.StatusCode,
			LatencyMs:  res.LatencyMs,
			Error:      res.Error,
			CheckedAt:  &now,
		})
	}
}

func (r *Runner) runMetricsFlush(ctx context.Context) {
	if err := r.metrics.Flush(ctx); err != nil {
		r.logger.Warn("metrics flush failed", "error", err)
	}
}
