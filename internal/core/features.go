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
	// It runs on the scanners; its disk is every builder's, since one that
	// stopped scanning keeps its database until a prune.
	for s := range c.scanRoles(servers) {
		scanning.Servers = append(scanning.Servers, s.Name)
	}
	var builders []store.Server
	for _, s := range servers {
		if s.Builder {
			builders = append(builders, s)
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
			if err := c.enableAnalytics(ctx, a.Name, nil, false); err != nil {
				return apitypes.AnalyticsStatus{}, err
			}
			c.audit(ctx, a.platform(), "analytics.on", nil, nil, "analytics", "turned Dock Analytics on")
		} else {
			// On already: the servers get their salts and rules again, in
			// case an earlier turn on couldn't reach them all.
			c.pushAnalyticsToAll(ctx)
		}
		return c.AnalyticsStatus(ctx, a)
	}
	c.analytics.switching.Lock()
	defer c.analytics.switching.Unlock()
	cl, serverID := c.analyticsClient()
	if cl == nil {
		// Never on, or off already: recorded, so a site's first domain
		// doesn't turn it on.
		if !c.analyticsOff() {
			if err := c.recordAnalyticsOff(ctx, a); err != nil {
				return apitypes.AnalyticsStatus{}, err
			}
			c.audit(ctx, a.platform(), "analytics.off", nil, nil, "analytics", "turned Dock Analytics off before it ran")
		}
		if req.RemoveData {
			if err := c.removeKeptAnalytics(ctx, a); err != nil {
				return apitypes.AnalyticsStatus{}, err
			}
		}
		return c.AnalyticsStatus(ctx, a)
	}
	if !c.online(serverID) {
		return apitypes.AnalyticsStatus{}, errf(apitypes.CodeServerOffline, "the analytics server is offline; turn analytics off once it is back, so its store stops there")
	}
	// The client goes first, under the lock a start checks it under
	// (ensureAnalyticsStore): no start begins once it is gone, and one
	// running is refused here.
	c.analytics.mu.Lock()
	busy := c.analytics.starting || c.analytics.moving != nil || c.analytics.restoring
	if !busy {
		if c.analytics.runCancel != nil {
			c.analytics.runCancel() // ends the forwarders' tails
		}
		c.analytics.client, c.analytics.storeErr = nil, ""
	}
	c.analytics.mu.Unlock()
	if busy {
		return apitypes.AnalyticsStatus{}, conflict("the analytics store is starting or moving; turn analytics off once that is done (`dock analytics`)")
	}
	c.wakeForwarders()
	// Past here it goes through: the caller going away doesn't leave it
	// half off.
	ctx = context.WithoutCancel(ctx)
	rctx, cancel := context.WithTimeout(ctx, storeStartTimeout)
	defer cancel()
	rt := c.runtime(serverID)
	stop, what := rt.StopAnalyticsStore, "stopped the store, kept its data"
	if req.RemoveData {
		stop, what = rt.RemoveAnalyticsStore, "removed the store with its data"
	}
	if err := stop(rctx, node.StoreAnalytics); err != nil {
		c.resumeAnalytics(ctx)
		return apitypes.AnalyticsStatus{}, fmt.Errorf("stop the analytics store: %w", c.nodeErr(serverID, err))
	}
	if err := c.recordAnalyticsOff(rctx, a); err != nil {
		// Still on as far as the settings go: run it again.
		c.resumeAnalytics(ctx)
		return apitypes.AnalyticsStatus{}, fmt.Errorf("record analytics as off; it runs again: %w", err)
	}
	c.audit(ctx, a.platform(), "analytics.off", nil, nil, "analytics", "turned Dock Analytics off; "+what)
	c.publishAnalytics()
	return c.AnalyticsStatus(ctx, a)
}

// removeKeptAnalytics removes the data a store turned off kept, on its
// server, which must be online: off with RemoveData after an off that kept
// it. Removing a store that isn't there is no error. The caller holds
// analytics.switching.
func (c *Core) removeKeptAnalytics(ctx context.Context, a Actor) error {
	st, err := c.store.GetAnalyticsSettings(ctx)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	serverID := c.settingsServer(st)
	if !c.online(serverID) {
		return errf(apitypes.CodeServerOffline, "the analytics server is offline; remove the data once it is back (`dock analytics off --remove-data`)")
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), storeStartTimeout)
	defer cancel()
	if err := c.runtime(serverID).RemoveAnalyticsStore(rctx, node.StoreAnalytics); err != nil {
		return fmt.Errorf("remove the analytics store's data: %w", c.nodeErr(serverID, err))
	}
	c.audit(ctx, a.platform(), "analytics.off", nil, nil, "analytics", "removed the turned-off analytics store's data")
	return nil
}

// resumeAnalytics runs analytics again from its settings, after turning
// it off failed part way; a failure is logged, and the next status read
// starts the store.
func (c *Core) resumeAnalytics(ctx context.Context) {
	st, err := c.store.GetAnalyticsSettings(ctx)
	if err == nil {
		err = c.runAnalytics(ctx, st)
	}
	if err != nil {
		c.log.Error("analytics: run it again after turning it off failed", "err", err)
	}
}

// recordAnalyticsOff stores that an operator turned analytics off, making
// the settings row if analytics never ran, and withholds the servers'
// salts (analyticsConfig). The caller holds analytics.switching.
func (c *Core) recordAnalyticsOff(ctx context.Context, a Actor) error {
	sealed, err := c.box.Seal([]byte(newAnalyticsToken()), analyticsTokenAAD)
	if err != nil {
		return err
	}
	t := now()
	// A no-op when the row exists; its token and server stay.
	if err := c.store.CreateAnalyticsSettings(ctx, store.CreateAnalyticsSettingsParams{Token: sealed, UpdatedBy: a.Name, UpdatedAt: t}); err != nil {
		return err
	}
	if err := c.store.SetAnalyticsDisabled(ctx, store.SetAnalyticsDisabledParams{UpdatedBy: a.Name, UpdatedAt: t}); err != nil {
		return err
	}
	c.analytics.mu.Lock()
	c.analytics.off = true
	c.analytics.mu.Unlock()
	c.pushAnalyticsToAll(ctx) // no salts: the ingests keep nothing
	return nil
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
