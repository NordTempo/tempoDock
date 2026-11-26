package core

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Features an operator turns on and off in one place (ADR 0101): image
// scanning, Dock Analytics and tracing, the optional parts that take a
// small server's memory and disk. Off stops their containers and jobs at
// once; their data stays unless removed with them.

// Features lists the optional features with what each takes now: memory
// from the last statistics sample, disk from each server's last disk
// reading. A server never read yet is read in the background, and its
// numbers come with the next request. Operators only.
func (c *Core) Features(ctx context.Context, a Actor) (apitypes.Features, error) {
	if err := a.requirePlatform(PlatformView, "reading the platform's features"); err != nil {
		return apitypes.Features{}, err
	}
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return apitypes.Features{}, err
	}
	byID := make(map[int64]store.Server, len(servers))
	for _, s := range servers {
		byID[s.ID] = s
	}
	analyticsServer := c.localID
	if st, err := c.store.GetAnalyticsSettings(ctx); err == nil {
		analyticsServer = c.settingsServer(st)
	} else if !store.IsNotFound(err) {
		return apitypes.Features{}, err
	}

	scanning := apitypes.Feature{Key: apitypes.FeatureScanning, Servers: []string{}, MemoryLimitBytes: node.ScanDatabaseMemory}
	c.scans.mu.Lock()
	scanning.Enabled = c.scans.enabled
	c.scans.mu.Unlock()
	var builders []store.Server
	for _, s := range servers {
		if s.Builder {
			builders = append(builders, s)
			scanning.Servers = append(scanning.Servers, s.Name)
		}
	}
	scanning.DiskBytes = c.diskOf(builders, diskPartScanDB, diskPartScanLeftovers)

	cl, _ := c.analyticsClient()
	analytics := c.storeFeature(apitypes.FeatureAnalytics, cl != nil, byID[analyticsServer], node.MemoryAnalyticsStore,
		max(int64(c.cfg.Analytics.StoreMemoryMB)<<20, analyticsMinMemory))
	tl, _ := c.tracesClient()
	tracing := c.storeFeature(apitypes.FeatureTracing, tl != nil, byID[analyticsServer], node.MemoryTraceStore,
		int64(c.cfg.Traces.StoreMemoryMB)<<20)
	return apitypes.Features{Items: []apitypes.Feature{scanning, analytics, tracing}}, nil
}

// storeFeature is a feature that runs a store's container on srv: its
// memory now under memName, its disk from the reading's part of that
// name (disk.go names them so).
func (c *Core) storeFeature(key apitypes.FeatureKey, on bool, srv store.Server, memName string, limit int64) apitypes.Feature {
	f := apitypes.Feature{Key: key, Enabled: on, Servers: []string{}, MemoryLimitBytes: limit}
	if srv.ID == 0 {
		return f
	}
	f.Servers = append(f.Servers, srv.Name)
	if u, ok := c.stats.serverUsage(srv.ID); ok && on {
		var n int64
		for _, p := range u.MemoryParts {
			if p.Name == memName {
				n += p.Bytes
			}
		}
		f.MemoryBytes = &n
	}
	f.DiskBytes = c.diskOf([]store.Server{srv}, memName)
	return f
}

// diskOf adds up the named parts of the servers' last disk readings; nil
// when one of them has none yet, which is then read in the background.
func (c *Core) diskOf(servers []store.Server, parts ...string) *int64 {
	var total int64
	missing := false
	for _, s := range servers {
		c.disk.mu.Lock()
		d, ok := c.disk.last[s.ID]
		c.disk.mu.Unlock()
		if !ok {
			missing = true
			if c.online(s.ID) {
				c.refreshDisk(s)
			}
			continue
		}
		for _, p := range d.Parts {
			if slices.Contains(parts, p.Name) {
				total += p.Bytes
			}
		}
	}
	if missing {
		return nil
	}
	return &total
}

// SetAnalytics turns Dock Analytics on or off (ADR 0101). On, the store
// runs on the analytics server again, on the data it kept, and the
// servers' ingests take events again. Off, its container stops, the
// ingests get no salts, so they keep nothing, and a site's first domain
// doesn't turn it on again; with RemoveData its data goes too. The
// analytics server must be online to turn it off, and no start or move of
// the store may be running. Operators only.
func (c *Core) SetAnalytics(ctx context.Context, a Actor, req apitypes.AnalyticsSwitch) (apitypes.AnalyticsStatus, error) {
	if err := a.requirePlatform(PlatformSettings, "turning analytics on or off"); err != nil {
		return apitypes.AnalyticsStatus{}, err
	}
	if req.Enabled {
		if cl, _ := c.analyticsClient(); cl == nil {
			if err := c.enableAnalytics(ctx, a.Name, nil); err != nil {
				return apitypes.AnalyticsStatus{}, err
			}
			c.audit(ctx, a.platform(), "analytics.on", nil, nil, "analytics", "turned Dock Analytics on")
		}
		return c.AnalyticsStatus(ctx, a)
	}
	cl, serverID := c.analyticsClient()
	if cl == nil {
		return c.AnalyticsStatus(ctx, a) // off already, or never on
	}
	c.analytics.mu.Lock()
	busy := c.analytics.starting || c.analytics.moving != nil || c.analytics.restoring
	c.analytics.mu.Unlock()
	if busy {
		return apitypes.AnalyticsStatus{}, conflict("the analytics store is starting or moving; turn analytics off once that is done (`dock analytics`)")
	}
	if !c.online(serverID) {
		return apitypes.AnalyticsStatus{}, errf(apitypes.CodeServerOffline, "the analytics server is offline; turn analytics off once it is back, so its store stops there")
	}
	rctx, cancel := context.WithTimeout(ctx, storeStartTimeout)
	defer cancel()
	rt := c.runtime(serverID)
	stop, what := rt.StopAnalyticsStore, "stopped the store, kept its data"
	if req.RemoveData {
		stop, what = rt.RemoveAnalyticsStore, "removed the store with its data"
	}
	if err := stop(rctx, node.StoreAnalytics); err != nil {
		return apitypes.AnalyticsStatus{}, fmt.Errorf("stop the analytics store: %w", c.nodeErr(serverID, err))
	}
	if err := c.store.SetAnalyticsDisabled(ctx, store.SetAnalyticsDisabledParams{UpdatedBy: a.Name, UpdatedAt: now()}); err != nil {
		return apitypes.AnalyticsStatus{}, err
	}
	c.analytics.mu.Lock()
	if c.analytics.runCancel != nil {
		c.analytics.runCancel() // ends the forwarders' tails
	}
	c.analytics.client, c.analytics.storeErr, c.analytics.off = nil, "", true
	c.analytics.mu.Unlock()
	c.wakeForwarders()
	c.pushAnalyticsToAll(ctx) // no salts: the ingests keep nothing
	c.audit(ctx, a.platform(), "analytics.off", nil, nil, "analytics", "turned Dock Analytics off; "+what)
	c.publishAnalytics()
	return c.AnalyticsStatus(ctx, a)
}

// removeScanDatabases removes every online builder's vulnerability
// database, for scanning turned off with RemoveDatabase. A builder that
// is offline keeps its database; the error names it.
func (c *Core) removeScanDatabases(ctx context.Context) error {
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range servers {
		if !s.Builder {
			continue
		}
		rt := c.connected(s.ID)
		if rt == nil {
			errs = append(errs, fmt.Errorf("%s is offline and keeps its database; remove it with `dock server prune %s --only scan-db` once it is back", s.Name, s.Name))
			continue
		}
		if _, err := c.pruneScanDatabase(ctx, s, rt, true); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name, c.nodeErr(s.ID, err)))
		}
	}
	return errors.Join(errs...)
}
