package core

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Retention and disk pruning (ADR 0020). Both ride on the statistics tick,
// so they add no timer of their own: the tick runs a retention pass at
// most once per retentionEvery, and starts a server's prune when its
// sample shows Docker's disk past [retention] prune_disk_percent, at most
// once per pruneEvery per server.
//
// A pass trims, in batches of short transactions:
//   - release history (build log, saved container output, progress
//     events) of failed and superseded releases out of their service's
//     rollback window and older than build_log_days. Releases a rollback
//     can use keep all of it;
//   - audit events older than audit_days, or past audit_max_events;
//   - resolved alerts older than alert_days.
//
// Statistics prune themselves on the tick (stats.go), and release images
// out of the rollback window go after each deploy (images.go).

const (
	// retentionEvery is the least time between two retention passes.
	retentionEvery = time.Hour
	// pruneEvery is the least time between two prunes of one server.
	pruneEvery = time.Hour
	// retentionBatch is how many rows one transaction deletes, and
	// retentionBatches how many batches of each kind a pass runs: a large
	// backlog (the first pass after an upgrade) is worked off over a few
	// hours rather than in one long pass.
	retentionBatch   = 200
	retentionBatches = 10
)

// retention is when retention last ran and the state of server prunes.
type retention struct {
	mu      sync.Mutex
	last    time.Time
	pruned  map[int64]time.Time // by server ID
	pruning map[int64]bool
}

func newRetention() *retention {
	return &retention{pruned: map[int64]time.Time{}, pruning: map[int64]bool{}}
}

// retentionTick runs on the statistics tick with its fresh host samples:
// it starts prunes of servers whose disk is past the threshold, and a
// retention pass when one is due.
func (c *Core) retentionTick(ctx context.Context, now time.Time, servers []store.Server, hosts map[int64]apitypes.ServerUsage) {
	if p := c.cfg.Retention.PruneDiskPercent; p > 0 {
		for _, srv := range servers {
			u, ok := hosts[srv.ID]
			if !ok || len(u.Disks) == 0 || u.Disks[0].TotalBytes <= 0 {
				continue
			}
			// Disks[0] is the filesystem of Docker's data root: images and
			// BuildKit's cache volume live there.
			if 100*float64(u.Disks[0].UsedBytes)/float64(u.Disks[0].TotalBytes) >= float64(p) {
				c.maybePrune(srv, now)
			}
		}
	}
	r := c.retention
	r.mu.Lock()
	due := now.Sub(r.last) >= retentionEvery
	if due {
		r.last = now
	}
	r.mu.Unlock()
	if due {
		c.retain(ctx, now)
	}
}

// maybePrune starts pruning a server in the background unless it is being
// pruned or was within pruneEvery.
func (c *Core) maybePrune(srv store.Server, now time.Time) {
	r := c.retention
	r.mu.Lock()
	due := !r.pruning[srv.ID] && now.Sub(r.pruned[srv.ID]) >= pruneEvery
	if due {
		r.pruning[srv.ID] = true
	}
	r.mu.Unlock()
	if !due {
		return
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.pruneServer(c.ctx, srv)
		r.mu.Lock()
		r.pruning[srv.ID] = false
		r.pruned[srv.ID] = time.Now()
		r.mu.Unlock()
	}()
}

// pruneServer removes a server's dangling images that the platform made
// (built, or pulled from its registry) except those a rollback may need,
// and on a builder trims its own BuildKit's cache to [retention]
// build_cache_keep_mb. Other images on the server are never touched.
func (c *Core) pruneServer(ctx context.Context, srv store.Server) {
	rt := c.connected(srv.ID)
	if rt == nil {
		return
	}
	started := time.Now()
	keep, err := c.store.ListKeptImageIDs(ctx, store.ListKeptImageIDsParams{ServerID: srv.ID, Keep: int64(max(c.cfg.Defaults.KeepReleases, 1))})
	if err != nil {
		c.log.Warn("prune: list kept images", "server", srv.Name, "err", err)
		return
	}
	req := node.PruneImagesRequest{Keep: keep}
	if c.reg != nil {
		req.Repos = []string{c.reg.Host() + "/" + buildTagPrefix}
	}
	images, err := rt.PruneImages(ctx, req)
	if err != nil {
		c.log.Warn("prune images", "server", srv.Name, "err", err)
	}
	var cache struct {
		Removed        int
		ReclaimedBytes int64
	}
	if srv.Builder {
		r, err := rt.PruneBuildCache(ctx, int64(c.cfg.Retention.BuildCacheKeepMB)<<20)
		if err != nil {
			c.log.Warn("prune build cache", "server", srv.Name, "err", err)
		}
		cache.Removed, cache.ReclaimedBytes = r.Removed, r.ReclaimedBytes
	}
	c.log.Info("pruned a full disk", "server", srv.Name, "images", images.Removed, "image_bytes", images.ReclaimedBytes,
		"cache_records", cache.Removed, "cache_bytes", cache.ReclaimedBytes, "took", time.Since(started).Round(time.Millisecond))
}

// retained counts what one retention pass removed.
type retained struct {
	releases, audit int
	alerts          int64
}

// retain runs one retention pass. Failures are logged; the next pass
// tries again.
func (c *Core) retain(ctx context.Context, now time.Time) retained {
	started := time.Now()
	var out retained
	var err error
	if out.releases, err = c.trimReleaseHistory(ctx, now); err != nil {
		c.log.Warn("retention: release history", "err", err)
	}
	if out.audit, err = c.trimAudit(ctx, now); err != nil {
		c.log.Warn("retention: audit log", "err", err)
	}
	if out.alerts, err = c.store.PruneResolvedAlerts(ctx, ptr(now.Add(-days(c.cfg.Retention.AlertDays)).UTC())); err != nil {
		c.log.Warn("retention: alerts", "err", err)
	}
	took := time.Since(started).Round(time.Microsecond)
	if out != (retained{}) {
		c.log.Info("retention", "release_histories", out.releases, "audit_events", out.audit, "alerts", out.alerts, "took", took)
	} else {
		c.log.Debug("retention: nothing to trim", "took", took)
	}
	return out
}

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

// trimReleaseHistory deletes the history of releases out of their
// rollback window and older than build_log_days: files first, then the
// release's events, and marks the release trimmed. A crash in between
// leaves the release untrimmed, and the next pass finishes it.
func (c *Core) trimReleaseHistory(ctx context.Context, now time.Time) (int, error) {
	cutoff := now.Add(-days(c.cfg.Retention.BuildLogDays)).UTC()
	keep := int64(max(c.cfg.Defaults.KeepReleases, 1))
	n := 0
	for range retentionBatches {
		ids, err := c.store.ListExpiredReleaseHistory(ctx, store.ListExpiredReleaseHistoryParams{Cutoff: cutoff, Keep: keep, MaxRows: retentionBatch})
		if err != nil || len(ids) == 0 {
			return n, err
		}
		for _, id := range ids {
			for _, path := range []string{c.buildLogPath(id), c.containerLogPath(id)} {
				if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return n, err
				}
			}
		}
		err = c.store.Tx(ctx, func(q *store.Queries) error {
			for _, id := range ids {
				if err := q.DeleteReleaseEvents(ctx, id); err != nil {
					return err
				}
				if err := q.MarkReleaseHistoryTrimmed(ctx, id); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return n, err
		}
		n += len(ids)
		if len(ids) < retentionBatch {
			return n, nil
		}
	}
	return n, nil
}

// trimAudit deletes audit events (and their details) from the start of
// the log while they are older than audit_days or past audit_max_events.
// IDs and times grow together, so the log is trimmed from its start and
// a pass with nothing to do reads one row.
func (c *Core) trimAudit(ctx context.Context, now time.Time) (int, error) {
	cfg := c.cfg.Retention
	cutoff := now.Add(-days(cfg.AuditDays))
	var capID int64 // events up to it are past audit_max_events
	if cfg.AuditMaxEvents > 0 {
		maxID, err := c.store.MaxAuditEventID(ctx)
		if err != nil {
			return 0, err
		}
		capID = maxID - int64(cfg.AuditMaxEvents)
	}
	expired := func(r store.ListOldestAuditEventsRow) bool { return r.CreatedAt.Before(cutoff) || r.ID <= capID }
	if first, err := c.store.ListOldestAuditEvents(ctx, 1); err != nil || len(first) == 0 || !expired(first[0]) {
		return 0, err
	}
	n := 0
	for range retentionBatches {
		rows, err := c.store.ListOldestAuditEvents(ctx, retentionBatch)
		if err != nil {
			return n, err
		}
		var through, details int64
		count := 0
		for _, r := range rows {
			if !expired(r) {
				break
			}
			through, count = r.ID, count+1
			if r.DetailID != nil {
				details = max(details, *r.DetailID)
			}
		}
		if count == 0 {
			return n, nil
		}
		err = c.store.Tx(ctx, func(q *store.Queries) error {
			if err := q.DeleteAuditEventsThrough(ctx, through); err != nil {
				return err
			}
			if details > 0 {
				return q.DeleteAuditDetailsThrough(ctx, details)
			}
			return nil
		})
		if err != nil {
			return n, err
		}
		n += count
		if count < len(rows) || len(rows) < retentionBatch {
			return n, nil
		}
	}
	return n, nil
}
