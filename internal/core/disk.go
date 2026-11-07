package core

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Where a server's disk goes (ADR 0035). Read when someone asks: Docker
// sizes every image and volume for it, which takes seconds, so nothing
// reads it on a timer. The last reading of each server is kept in memory
// and answered at once; one older than diskFresh is read again in the
// background and the new one goes out as a disk event (the 0035
// amendment). Concurrent reads of one server share one read.

// diskFresh is how old a kept reading may be before asking for it starts
// a new read in the background.
const diskFresh = time.Minute

// diskReadTimeout bounds one disk read.
const diskReadTimeout = 3 * time.Minute

// diskState is the last disk reading of each server (one per server, so
// bounded by the servers), and which servers are being read again.
type diskState struct {
	mu         sync.Mutex
	last       map[int64]apitypes.ServerDisk
	refreshing map[int64]bool
	// volumes are the same readings' volume bytes per service ID, for
	// each org's use on the console (orgUsage).
	volumes map[int64]map[int64]int64
	// tried is when the console's Organizations page last asked for each
	// server to be read again (refreshStaleDisks), so a read that keeps
	// failing, whose error event refetches the page, isn't asked for
	// again within diskFresh.
	tried map[int64]time.Time
}

// ServerDisk returns where a server's disk goes: its services' volumes,
// images by what keeps them, BuildKit's cache, Dock's own data and the
// rest of the filesystem, and what a prune would free. The first time it
// waits for the read; after that it answers with the last reading, and if
// that is older than diskFresh, marks it refreshing and reads again in
// the background. Operators only.
func (c *Core) ServerDisk(ctx context.Context, a Actor, name string) (apitypes.ServerDisk, error) {
	if err := a.requireOperator("managing servers"); err != nil {
		return apitypes.ServerDisk{}, err
	}
	srv, err := c.loadServer(ctx, name)
	if err != nil {
		return apitypes.ServerDisk{}, err
	}
	s := &c.disk
	s.mu.Lock()
	d, ok := s.last[srv.ID]
	busy := s.refreshing[srv.ID]
	s.mu.Unlock()
	if !ok {
		return c.readDiskShared(ctx, srv)
	}
	if busy || now().Sub(d.ReadAt) >= diskFresh {
		c.refreshDisk(srv)
		d.Refreshing = true
	}
	return d, nil
}

// readDiskShared reads a server's disk, sharing a read already under way,
// and keeps the reading.
func (c *Core) readDiskShared(ctx context.Context, srv store.Server) (apitypes.ServerDisk, error) {
	v, err, _ := c.diskReads.Do(strconv.FormatInt(srv.ID, 10), func() (any, error) {
		// Not the caller's ctx: others may wait on this read.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), diskReadTimeout)
		defer cancel()
		d, vols, err := c.readDisk(ctx, srv)
		if err == nil {
			c.disk.mu.Lock()
			if c.disk.last == nil {
				c.disk.last, c.disk.volumes = map[int64]apitypes.ServerDisk{}, map[int64]map[int64]int64{}
			}
			c.disk.last[srv.ID], c.disk.volumes[srv.ID] = d, vols
			c.disk.mu.Unlock()
		}
		return d, err
	})
	if err != nil {
		return apitypes.ServerDisk{}, err
	}
	return v.(apitypes.ServerDisk), nil
}

// refreshDisk reads a server's disk again in the background, unless that
// is under way, and publishes the reading as a disk event (operators
// only). If the read fails, the kept reading goes out with the error.
func (c *Core) refreshDisk(srv store.Server) {
	s := &c.disk
	s.mu.Lock()
	if s.refreshing[srv.ID] {
		s.mu.Unlock()
		return
	}
	if s.refreshing == nil {
		s.refreshing = map[int64]bool{}
	}
	s.refreshing[srv.ID] = true
	s.mu.Unlock()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		d, err := c.readDiskShared(c.ctx, srv)
		s.mu.Lock()
		delete(s.refreshing, srv.ID)
		if err != nil {
			d = s.last[srv.ID]
			d.Error = "reading it again failed: " + err.Error()
		}
		s.mu.Unlock()
		if err != nil {
			c.log.Warn("read a server's disk", "server", srv.Name, "err", err)
			if d.Server == "" {
				return // nothing was ever read to show
			}
		}
		c.bus.publish(busEvent{serverStats: true, ev: apitypes.Event{Type: apitypes.EventDisk, Disk: &d}})
	}()
}

// readDisk asks a server for its disk usage and filesystem and groups it,
// with the bytes of each service's volumes by service ID.
func (c *Core) readDisk(ctx context.Context, srv store.Server) (apitypes.ServerDisk, map[int64]int64, error) {
	rt := c.connected(srv.ID)
	if rt == nil {
		return apitypes.ServerDisk{}, nil, errf(apitypes.CodeServerOffline, "server %s is offline; its disk can be read once it is back", srv.Name)
	}
	req, err := c.pruneRequest(ctx, srv.ID)
	if err != nil {
		return apitypes.ServerDisk{}, nil, err
	}
	du, err := rt.DiskUsage(ctx, req)
	if err != nil {
		return apitypes.ServerDisk{}, nil, c.nodeErr(srv.ID, err)
	}
	host, err := rt.HostStats(ctx)
	if err != nil {
		return apitypes.ServerDisk{}, nil, c.nodeErr(srv.ID, err)
	}
	vols := map[int64]int64{}
	for _, v := range du.Volumes {
		if id, err := strconv.ParseInt(v.Service, 10, 64); err == nil && (v.ControlPlane == "" || v.ControlPlane == c.install) {
			vols[id] += v.Bytes
		}
	}
	var registry int64
	if srv.ID == c.local.ID && c.reg != nil {
		if id, err := c.registryStorageID(ctx); err == nil && id == nil {
			registry, _ = c.reg.Stored(ctx)
		}
	}
	services, err := c.store.ListServerServices(ctx, srv.ID)
	if err != nil {
		return apitypes.ServerDisk{}, nil, err
	}
	names := make(map[string]string, len(services))
	for _, s := range services {
		names[strconv.FormatInt(s.ID, 10)] = s.ProjectName + "/" + s.EnvironmentName + "/" + s.Name
	}
	return c.diskParts(srv, du, host, registry, names), vols, nil
}

// diskParts groups a server's disk usage into the console's parts.
// registry is the registry's storage on this server's disk (the control
// plane's, while its images aren't in a bucket); names are the server's
// services (project/environment/service) by ID. Everything Docker and
// dock don't account for on the data root's filesystem is "the rest".
func (c *Core) diskParts(srv store.Server, du node.DiskUsage, host node.HostStats, registry int64, names map[string]string) apitypes.ServerDisk {
	out := apitypes.ServerDisk{Server: srv.Name, ReadAt: now().UTC().Truncate(time.Second)}
	// Disks[0] is the filesystem of Docker's data root; a second one
	// means dock's data dir is elsewhere.
	sameFS := true
	if len(host.Disks) > 0 {
		out.UsedBytes, out.TotalBytes = toInt64(host.Disks[0].Used), toInt64(host.Disks[0].Total)
		sameFS = len(host.Disks) == 1
	}
	add := func(g apitypes.DiskGroup, name, detail string, bytes, reclaimable int64) {
		if bytes > 0 {
			out.Parts = append(out.Parts, apitypes.DiskPart{Group: g, Name: name, Detail: detail, Bytes: bytes, Reclaimable: reclaimable})
		}
	}

	var backups, snapshots, dockVolumes, cacheBytes, otherVolumes int64
	for _, v := range du.Volumes {
		switch {
		case v.ControlPlane != "" && v.ControlPlane != c.install:
			otherVolumes += v.Bytes // another install's, on a shared Docker host
		case v.Name == node.BuildCacheVolume:
			cacheBytes += v.Bytes
		case strings.HasPrefix(v.Name, backup.LocalVolumePrefix):
			backups += v.Bytes
		case strings.HasPrefix(v.Name, snapshotVolumePrefix):
			snapshots += v.Bytes
		case v.Service != "":
			name, ok := names[v.Service]
			if !ok {
				name = "archived or moved service"
			}
			add(apitypes.DiskApps, name, "volume "+v.Name, v.Bytes, 0)
		default:
			dockVolumes += v.Bytes
		}
	}
	add(apitypes.DiskApps, "container layers", "files containers wrote outside volumes", du.Containers, 0)

	add(apitypes.DiskImages, "running", "images containers use", du.ImagesRunning, 0)
	add(apitypes.DiskImages, "for rollbacks", "images Dock keeps for its releases", du.ImagesKept, 0)
	add(apitypes.DiskImages, "shared layers", "layers several images use, counted once", du.ImagesShared, 0)
	add(apitypes.DiskImages, "not Dock's", "images other work on the server pulled", du.ImagesOther, 0)
	add(apitypes.DiskReclaimable, "unused images", "dangling images Dock made that no release needs", du.ImagesReclaimable, du.ImagesReclaimable)
	if cacheBytes > 0 {
		var free int64
		if srv.Builder {
			free = max(cacheBytes-c.buildCacheKeep(), 0)
		}
		add(apitypes.DiskBuild, "BuildKit cache", fmt.Sprintf("a prune keeps %d MB", c.cfg.Retention.BuildCacheKeepMB), cacheBytes, free)
	}
	// With dock's data dir on the root filesystem, the registry's dir
	// (under /var/lib too) most likely is; on a split layout neither is
	// counted against Docker's filesystem.
	if sameFS {
		add(apitypes.DiskDock, "data dir", "database, keys, build logs, tool caches", du.DataDir, 0)
		add(apitypes.DiskDock, "registry", "the image registry's storage dir", registry, 0)
	}
	add(apitypes.DiskDock, "local backups", "restic repository on this server, not off-box", backups, 0)
	add(apitypes.DiskDock, "volume snapshots", "copies of volumes kept on this server (dock volume snapshot)", snapshots, 0)
	add(apitypes.DiskDock, "other volumes", "Dock's other volumes", dockVolumes, 0)
	add(apitypes.DiskOther, "volumes not Dock's", "", du.OtherVolumes+otherVolumes, 0)

	var known int64
	for _, p := range out.Parts {
		known += p.Bytes
		out.ReclaimableBytes += p.Reclaimable
	}
	add(apitypes.DiskOther, "rest of the filesystem", "logs, the system, files Dock doesn't own", out.UsedBytes-known, 0)

	order := []apitypes.DiskGroup{apitypes.DiskApps, apitypes.DiskImages, apitypes.DiskReclaimable, apitypes.DiskBuild, apitypes.DiskDock, apitypes.DiskOther}
	slices.SortStableFunc(out.Parts, func(a, b apitypes.DiskPart) int {
		return cmp.Or(cmp.Compare(slices.Index(order, a.Group), slices.Index(order, b.Group)), cmp.Compare(b.Bytes, a.Bytes))
	})
	if out.Parts == nil {
		out.Parts = []apitypes.DiskPart{}
	}
	return out
}

// PruneServer frees what a full disk's prune frees, now: images Dock made
// that no release needs, and on a builder BuildKit's cache above what is
// kept. Running apps and rollback images are never touched. It runs to
// the end even if the caller goes away. A prune that partly failed
// answers with what it freed and the error in the result. Refused while
// the server is being pruned. Operators only.
func (c *Core) PruneServer(ctx context.Context, a Actor, name string) (apitypes.ServerPruneResult, error) {
	if err := a.requireOperator("managing servers"); err != nil {
		return apitypes.ServerPruneResult{}, err
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
	images, cache, err := c.pruneServer(ctx, srv)
	// What the prune freed shows on the server page once read again.
	c.refreshDisk(srv)
	r.mu.Lock()
	r.pruning[srv.ID] = false
	if err == nil {
		// A failed prune doesn't hold back the automatic one.
		r.pruned[srv.ID] = time.Now()
	}
	r.mu.Unlock()
	if err != nil && images.Removed == 0 && cache.ReclaimedBytes == 0 {
		return apitypes.ServerPruneResult{}, err
	}
	out := apitypes.ServerPruneResult{
		Server: srv.Name, ImagesRemoved: images.Removed, ImageBytes: images.ReclaimedBytes, CacheBytes: cache.ReclaimedBytes,
	}
	summary := fmt.Sprintf("pruned server %s: %d images (%s), %s of build cache", srv.Name, images.Removed, humanBytes(images.ReclaimedBytes), humanBytes(cache.ReclaimedBytes))
	if err != nil {
		out.Error = err.Error()
		summary += fmt.Sprintf("; %v", err)
	}
	c.audit(ctx, a.platform(), "server.prune", nil, nil, "server "+srv.Name, summary)
	return out, nil
}

// pruneTimeout bounds a prune someone started.
const pruneTimeout = 10 * time.Minute
