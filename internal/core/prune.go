package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/registry"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Pruning a server's disk (ADR 0035, amendment 2): all of what nothing
// needs, or one target at a time (apitypes.PruneTarget). The safe targets
// run by themselves once a disk passes [retention] prune_disk_percent
// (maybePrune) and after archives (pruneArchived); the others only when
// someone asks, since each costs something later.

// PruneServer frees a server's disk now: the given targets, or without
// any the safe ones. Running apps, the rollback images the registry
// doesn't have and volumes are never touched. It runs to the end even if
// the caller goes away. A prune that partly failed answers with what it
// freed and the error in the result. Refused while the server is being
// pruned. Operators only.
func (c *Core) PruneServer(ctx context.Context, a Actor, name string, targets []apitypes.PruneTarget) (apitypes.ServerPruneResult, error) {
	if err := a.requirePlatform(ServersManage, "managing servers"); err != nil {
		return apitypes.ServerPruneResult{}, err
	}
	for _, t := range targets {
		if !slices.Contains(apitypes.PruneTargets, t) {
			return apitypes.ServerPruneResult{}, invalid("there is no prune target %q; pick from %s", t, joinTargets(apitypes.PruneTargets))
		}
	}
	srv, err := c.loadServer(ctx, name)
	if err != nil {
		return apitypes.ServerPruneResult{}, err
	}
	r := c.retention
	r.mu.Lock()
	busy := r.pruning[srv.ID]
	r.pruning[srv.ID] = true
	r.mu.Unlock()
	if busy {
		return apitypes.ServerPruneResult{}, conflict("server %s is being pruned already; wait for it to finish", srv.Name)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pruneTimeout)
	defer cancel()
	out, err := c.pruneServer(ctx, srv, targets)
	// What the prune freed shows on the server page once read again.
	c.refreshDisk(srv)
	r.mu.Lock()
	r.pruning[srv.ID] = false
	if err == nil {
		// A failed prune doesn't hold back the automatic one.
		r.pruned[srv.ID] = time.Now()
	}
	r.mu.Unlock()
	if err != nil && freedBytes(out) == 0 && out.ImagesRemoved == 0 {
		return apitypes.ServerPruneResult{}, err
	}
	parts := make([]string, 0, len(out.Freed))
	for _, f := range out.Freed {
		parts = append(parts, fmt.Sprintf("%s %s", f.Target, humanBytes(f.Bytes)))
	}
	summary := fmt.Sprintf("pruned server %s: %s", srv.Name, strings.Join(parts, ", "))
	if err != nil {
		out.Error = err.Error()
		summary += fmt.Sprintf("; %v", err)
	}
	c.audit(ctx, a.platform(), "server.prune", nil, nil, "server "+srv.Name, summary)
	return out, nil
}

// joinTargets lists targets for a message: "images, scanner, cache".
func joinTargets(ts []apitypes.PruneTarget) string {
	s := make([]string, len(ts))
	for i, t := range ts {
		s[i] = string(t)
	}
	return strings.Join(s, ", ")
}

// freedBytes is what a prune freed in all.
func freedBytes(r apitypes.ServerPruneResult) int64 {
	var n int64
	for _, f := range r.Freed {
		n += f.Bytes
	}
	return n
}

// pruneTargets are the targets a prune runs, in apitypes.PruneTargets'
// order: the safe ones without any, and a wider target in place of the
// narrower one it covers (all-cache for cache, scan-db for scanner).
// Build cache and the vulnerability database are only on builders.
func pruneTargets(srv store.Server, asked []apitypes.PruneTarget) []apitypes.PruneTarget {
	want := func(t apitypes.PruneTarget) bool {
		if len(asked) == 0 {
			return t.Safe()
		}
		return slices.Contains(asked, t)
	}
	var out []apitypes.PruneTarget
	for _, t := range apitypes.PruneTargets {
		switch {
		case !want(t):
		case !srv.Builder && (t == apitypes.PruneCache || t == apitypes.PruneAllCache || t == apitypes.PruneScanner || t == apitypes.PruneScanDB):
		case t == apitypes.PruneCache && want(apitypes.PruneAllCache), t == apitypes.PruneScanner && want(apitypes.PruneScanDB):
		default:
			out = append(out, t)
		}
	}
	return out
}

// pruneServer runs the targets (pruneTargets) on a server, each even if
// one before it failed, and says what each freed. The caller holds the
// server's retention.pruning.
func (c *Core) pruneServer(ctx context.Context, srv store.Server, asked []apitypes.PruneTarget) (apitypes.ServerPruneResult, error) {
	out := apitypes.ServerPruneResult{Server: srv.Name, Freed: []apitypes.PruneFreed{}}
	rt := c.connected(srv.ID)
	if rt == nil {
		return out, errf(apitypes.CodeServerOffline, "server %s is offline; prune it once it is back", srv.Name)
	}
	targets := pruneTargets(srv, asked)
	var req node.PruneImagesRequest
	if slices.ContainsFunc(targets, func(t apitypes.PruneTarget) bool { return t == apitypes.PruneImages || t == apitypes.PruneRollbacks }) {
		var err error
		if req, err = c.imagePrune(ctx, srv); err != nil {
			return out, err
		}
	}
	var errs []error
	for _, t := range targets {
		var res node.PruneResult
		var err error
		switch t {
		case apitypes.PruneImages:
			r := req
			r.Rollbacks, r.Tool = nil, ""
			res, err = rt.PruneImages(ctx, r)
		case apitypes.PruneRollbacks:
			if len(req.Rollbacks) == 0 {
				break // nothing the registry has too, or no registry
			}
			r := req
			r.OnlyRollbacks, r.Tool = true, ""
			res, err = rt.PruneImages(ctx, r)
		case apitypes.PruneCache:
			res, err = rt.PruneBuildCache(ctx, c.buildCacheKeep())
		case apitypes.PruneAllCache:
			res, err = rt.PruneBuildCache(ctx, 0)
		case apitypes.PruneScanner, apitypes.PruneScanDB:
			res, err = c.pruneScanDatabase(ctx, srv, rt, t == apitypes.PruneScanDB)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("prune %s: %w", t, c.nodeErr(srv.ID, err)))
		}
		out.Freed = append(out.Freed, apitypes.PruneFreed{Target: t, Removed: res.Removed, Bytes: res.ReclaimedBytes})
		switch t {
		case apitypes.PruneImages, apitypes.PruneRollbacks:
			out.ImagesRemoved += res.Removed
			out.ImageBytes += res.ReclaimedBytes
		case apitypes.PruneCache, apitypes.PruneAllCache:
			out.CacheBytes += res.ReclaimedBytes
		}
	}
	return out, errors.Join(errs...)
}

// pruneScanDatabase frees a builder's vulnerability database: its
// leftovers, or with all the whole of it. The scanner stops first (a
// running scan is cancelled, none starts) and goes on after; a database
// that went is downloaded again before the next scan.
func (c *Core) pruneScanDatabase(ctx context.Context, srv store.Server, rt node.Runtime, all bool) (node.PruneResult, error) {
	c.scanBusy()
	defer c.scanDone()
	res, err := rt.PruneScanDatabase(ctx, node.ScanDatabasePrune{Tool: c.cfg.Backup.ResticImage, All: all, Labels: node.Labels{ControlPlane: c.install}})
	if err == nil && all {
		c.scans.mu.Lock()
		delete(c.scans.db, srv.ID)
		c.scans.mu.Unlock()
		c.setServerScanError(srv.ID, nil, "")
	}
	return res, err
}

// imagePrune says which of a server's images a prune may remove and how
// to size them (node.PruneImagesRequest), from every release on it (one
// query):
//
//   - kept: each service's rollback window ([defaults] keep_releases, by
//     release number) and its active and in-flight releases. Archived
//     services keep nothing: they are never rolled back;
//   - unneeded: every other image a release used;
//   - rollbacks (with the registry): kept images it has too, other than
//     each service's active release and the one before it (where a plain
//     rollback goes), which a rollback pulls again;
//   - pinned: the images Dock runs its own work in, whose other versions
//     go.
func (c *Core) imagePrune(ctx context.Context, srv store.Server) (node.PruneImagesRequest, error) {
	rows, err := c.store.ListServerReleaseImages(ctx, srv.ID)
	if err != nil {
		return node.PruneImagesRequest{}, fmt.Errorf("list release images: %w", err)
	}
	req := node.PruneImagesRequest{Pinned: c.pinnedImages(srv), Tool: c.cfg.Backup.ResticImage}
	if c.reg != nil {
		req.Repos = []string{c.reg.Host() + "/" + buildTagPrefix}
	}
	bySvc := map[int64][]store.ListServerReleaseImagesRow{}
	for _, r := range rows {
		bySvc[r.ServiceID] = append(bySvc[r.ServiceID], r)
	}
	keepN := int64(c.keepReleases())
	keep, hard, candidates, all := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, rels := range bySvc {
		for _, r := range rels {
			all[r.ImageID] = true
		}
		if rels[0].Archived != 0 {
			continue
		}
		// Newest first: the window, and the release before the active one.
		slices.SortFunc(rels, func(a, b store.ListServerReleaseImagesRow) int { return int(b.Number - a.Number) })
		var active int64 = -1
		for _, r := range rels {
			if apitypes.ReleaseStatus(r.Status) == apitypes.ReleaseActive {
				active = r.Number
				break
			}
		}
		before := false
		for _, r := range rels {
			st := apitypes.ReleaseStatus(r.Status)
			inWindow := r.Number > rels[0].Number-keepN
			switch {
			case !st.Done() || st == apitypes.ReleaseActive:
				keep[r.ImageID], hard[r.ImageID] = true, true
			case !before && st == apitypes.ReleaseSuperseded && (active < 0 || r.Number < active):
				before = true
				keep[r.ImageID], hard[r.ImageID] = true, true
			case inWindow:
				keep[r.ImageID] = true
				if c.reg != nil && r.ImageDigest != "" {
					candidates[r.ImageID] = true
				} else {
					hard[r.ImageID] = true // nowhere else to pull it from
				}
			}
		}
	}
	for id := range all {
		if !keep[id] {
			req.Unneeded = append(req.Unneeded, id)
		}
	}
	for id := range keep {
		req.Keep = append(req.Keep, id)
	}
	for id := range candidates {
		if !hard[id] {
			req.Rollbacks = append(req.Rollbacks, id)
		}
	}
	slices.Sort(req.Keep)
	slices.Sort(req.Unneeded)
	slices.Sort(req.Rollbacks)
	return req, nil
}

// pinnedImages are the images Dock runs its own work in on srv, by
// reference with a digest: the scanners, restic, the registry and the
// analytics store's base everywhere, since the control plane names them
// for every server; BuildKit and cloudflared only on the control plane's
// own server, since each server pins those in its own config.
func (c *Core) pinnedImages(srv store.Server) []string {
	pins := []string{c.cfg.Scan.SyftImage, c.cfg.Scan.GrypeImage, c.cfg.Backup.ResticImage, registry.Image, node.AnalyticsBaseImage}
	if srv.ID == c.localID {
		pins = append(pins, c.cfg.BuildKit.Image, c.cfg.Ingress.CloudflaredImage)
	}
	return slices.DeleteFunc(pins, func(p string) bool { return p == "" })
}

// pruneArchived removes the images archived services leave on their
// servers, in the background once their containers are gone (stopped):
// the images target of a prune on each, which also takes whatever else
// nothing needs. A server being pruned is skipped; its prune sees them.
func (c *Core) pruneArchived(servers []int64, stopped *sync.WaitGroup) {
	if len(servers) == 0 {
		return
	}
	c.wg.Add(1)
	c.goCaught("pruning archived services' images", func() {
		defer c.wg.Done()
		if stopped != nil {
			stopped.Wait()
		}
		for _, id := range slices.Compact(slices.Sorted(slices.Values(servers))) {
			srv, err := c.store.GetServer(c.ctx, id)
			if err != nil {
				continue
			}
			r := c.retention
			r.mu.Lock()
			busy := r.pruning[id]
			r.pruning[id] = true
			r.mu.Unlock()
			if busy {
				continue
			}
			res, err := c.pruneServer(c.ctx, srv, []apitypes.PruneTarget{apitypes.PruneImages})
			r.mu.Lock()
			r.pruning[id] = false
			r.mu.Unlock()
			if err != nil {
				c.log.Warn("prune archived services' images", "server", srv.Name, "err", err)
			} else if res.ImagesRemoved > 0 {
				c.log.Info("pruned archived services' images", "server", srv.Name, "images", res.ImagesRemoved, "bytes", res.ImageBytes)
			}
		}
	})
}
