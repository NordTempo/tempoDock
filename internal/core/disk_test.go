package core

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// A server's disk is grouped for the console: services' volumes by
// service, images by what keeps them, BuildKit's cache with what a prune
// frees above the kept amount, Dock's own, and the rest of the
// filesystem, which makes the parts add up to what is used.
func TestServerDisk(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	if _, err := h.c.AddVolume(h.ctx, testSystem, web, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
		t.Fatal(err)
	}
	const gb = 1 << 30
	h.rt.SetHostStats(node.HostStats{Disks: []node.Disk{{Paths: []string{"/var/lib/docker", "/var/lib/dock"}, Total: 40 * gb, Used: 20 * gb}}})
	h.rt.SetDiskUsage(node.DiskUsage{
		ImagesRunning: 3 * gb, ImagesKept: 4 * gb, ImagesReclaimable: 2 * gb, ImagesShared: 1 * gb,
		ImagesRollbacks: gb / 4, ScanLeftovers: gb / 2,
		Volumes: []node.VolumeUsage{
			{Name: "dock_v1", Service: "1", Bytes: 2 * gb},
			{Name: "theirs", Service: "1", ControlPlane: "another-install", Bytes: gb / 8},
			{Name: node.BuildCacheVolume, Bytes: 3 * gb},
			{Name: "dock_backups_abc", Bytes: gb / 2},
			{Name: "dock_scan_db_abc", Role: node.VolumeScanDB, Bytes: 2 * gb},
		},
		Containers: gb / 4, DataDir: gb / 4,
	})
	srv := h.c.localName()
	if _, err := h.c.ServerDisk(h.ctx, Actor{Kind: "token", OrgID: 1, Name: "ops", Scope: apitypes.ScopeAdmin}, srv); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org admin read a server's disk: %v", err)
	}
	d, err := h.c.ServerDisk(h.ctx, testSystem, srv)
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	got := map[string]apitypes.DiskPart{}
	for _, p := range d.Parts {
		sum += p.Bytes
		got[string(p.Group)+"/"+p.Name] = p
	}
	if d.UsedBytes != 20*gb || sum != d.UsedBytes {
		t.Fatalf("parts add up to %d of %d used: %+v", sum, d.UsedBytes, d.Parts)
	}
	// Build cache keeps 2048 MB by default: 1 GB of the 3 can go.
	if c := got["build/BuildKit cache"]; c.Bytes != 3*gb || c.Reclaimable != gb {
		t.Errorf("build cache = %+v", c)
	}
	if d.ReclaimableBytes != 3*gb+gb/2 {
		t.Errorf("reclaimable = %d, want unused images, scanner leftovers and cache above the kept amount", d.ReclaimableBytes)
	}
	// The scanner's volume splits into its database and the leftovers.
	if db, left := got["dock/vulnerability database"], got["reclaimable/scanner leftovers"]; db.Bytes != gb+gb/2 || left.Bytes != gb/2 || left.Reclaimable != gb/2 {
		t.Errorf("scanner volume = %+v and %+v", db, left)
	}
	want := []apitypes.DiskPrune{
		{Target: apitypes.PruneImages, Bytes: 2 * gb, Safe: true}, {Target: apitypes.PruneScanner, Bytes: gb / 2, Safe: true},
		{Target: apitypes.PruneCache, Bytes: gb, Safe: true}, {Target: apitypes.PruneRollbacks, Bytes: gb / 4},
		{Target: apitypes.PruneAllCache, Bytes: 3 * gb}, {Target: apitypes.PruneScanDB, Bytes: 2 * gb},
	}
	if !slices.Equal(d.Prunes, want) {
		t.Errorf("prunes = %+v", d.Prunes)
	}
	if got["other/volumes not Dock's"].Bytes != gb/8 {
		t.Errorf("another install's volume = %+v", got["other/volumes not Dock's"])
	}
	if got["other/rest of the filesystem"].Bytes != 20*gb-(3+4+2+1+2+3+2)*gb-gb/2-gb/4-gb/4-gb/8-gb/4 {
		t.Errorf("rest = %+v", got["other/rest of the filesystem"])
	}
	if got["apps/shop/production/web"].Detail != "volume dock_v1" {
		t.Errorf("the service's volume isn't named after it: %+v", d.Parts)
	}
	if got["dock/local backups"].Bytes != gb/2 || got["apps/container layers"].Bytes != gb/4 {
		t.Errorf("parts = %+v", d.Parts)
	}
	groups := []apitypes.DiskGroup{}
	for _, p := range d.Parts {
		if len(groups) == 0 || groups[len(groups)-1] != p.Group {
			groups = append(groups, p.Group)
		}
	}
	if !slices.Equal(groups, []apitypes.DiskGroup{apitypes.DiskApps, apitypes.DiskImages, apitypes.DiskReclaimable, apitypes.DiskBuild, apitypes.DiskDock, apitypes.DiskOther}) {
		t.Errorf("groups in order %v", groups)
	}

}

// A prune runs what it is asked, a wider target in place of the narrower
// one, and without targets the safe ones; each says what it freed. An
// unknown target is refused before anything runs.
func TestPruneServerTargets(t *testing.T) {
	h := newHarness(t)
	srv := h.c.localName()
	since := len(h.rt.Ops())
	ran := func() []string {
		var out []string
		for _, op := range h.rt.Ops()[since:] {
			// Not "prune logs": the harness's own retention pass runs that.
			if strings.HasPrefix(op, "prune images") || strings.HasPrefix(op, "prune cache") || strings.HasPrefix(op, "scan-database prune") {
				out = append(out, strings.Fields(op)[0]+" "+strings.Fields(op)[1]+map[bool]string{true: " all"}[strings.HasSuffix(op, " all")])
			}
		}
		since = len(h.rt.Ops())
		return out
	}
	targets := func(r apitypes.ServerPruneResult) []apitypes.PruneTarget {
		var out []apitypes.PruneTarget
		for _, f := range r.Freed {
			out = append(out, f.Target)
		}
		return out
	}

	res, err := h.c.PruneServer(h.ctx, testSystem, srv, nil)
	if err != nil || res.Server != srv {
		t.Fatalf("prune = %+v, %v", res, err)
	}
	if got := targets(res); !slices.Equal(got, []apitypes.PruneTarget{apitypes.PruneImages, apitypes.PruneScanner, apitypes.PruneCache}) {
		t.Errorf("a prune without targets ran %v", got)
	}
	if ops := ran(); !slices.Equal(ops, []string{"prune images", "scan-database prune", "prune cache"}) {
		t.Errorf("ops = %v", ops)
	}

	res, err = h.c.PruneServer(h.ctx, testSystem, srv, []apitypes.PruneTarget{apitypes.PruneScanDB, apitypes.PruneCache, apitypes.PruneAllCache, apitypes.PruneScanner})
	if err != nil {
		t.Fatal(err)
	}
	if got := targets(res); !slices.Equal(got, []apitypes.PruneTarget{apitypes.PruneAllCache, apitypes.PruneScanDB}) {
		t.Errorf("the wider targets ran %v", got)
	}
	if ops := ran(); !slices.Equal(ops, []string{"prune cache", "scan-database prune all"}) || !slices.Contains(h.rt.Ops(), "prune cache 0") {
		t.Errorf("ops = %v", ops)
	}

	if _, err := h.c.PruneServer(h.ctx, testSystem, srv, []apitypes.PruneTarget{"volumes"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("an unknown target: %v", err)
	}
	if ops := ran(); len(ops) != 0 {
		t.Errorf("a refused prune ran %v", ops)
	}
}

// A server that stopped building needs none of the build cache and
// scanner database it kept: both are reclaimable in full, and a prune
// without targets frees them.
func TestDiskOfServerThatStoppedBuilding(t *testing.T) {
	h := newHarness(t)
	const gb = 1 << 30
	h.rt.SetHostStats(node.HostStats{Disks: []node.Disk{{Paths: []string{"/var/lib/docker", "/var/lib/dock"}, Total: 40 * gb, Used: 20 * gb}}})
	h.rt.SetDiskUsage(node.DiskUsage{ScanLeftovers: gb / 2, Volumes: []node.VolumeUsage{
		{Name: node.BuildCacheVolume, Bytes: 3 * gb},
		{Name: "dock_scan_db_abc", Role: node.VolumeScanDB, Bytes: 2 * gb},
	}})
	local, err := h.store.GetLocalServer(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.UpdateServerSettings(h.ctx, store.UpdateServerSettingsParams{BaseDomain: local.BaseDomain, Builder: false, ID: local.ID}); err != nil {
		t.Fatal(err)
	}
	d, err := h.c.ServerDisk(h.ctx, testSystem, local.Name)
	if err != nil {
		t.Fatal(err)
	}
	want := []apitypes.DiskPrune{{Target: apitypes.PruneScanner, Bytes: 2 * gb, Safe: true}, {Target: apitypes.PruneCache, Bytes: 3 * gb, Safe: true}}
	if !slices.Equal(d.Prunes, want) || d.ReclaimableBytes != 5*gb {
		t.Fatalf("prunes = %+v, reclaimable %d", d.Prunes, d.ReclaimableBytes)
	}
	since := len(h.rt.Ops())
	if _, err := h.c.PruneServer(h.ctx, testSystem, local.Name, nil); err != nil {
		t.Fatal(err)
	}
	ops := h.rt.Ops()[since:]
	if !slices.Contains(ops, "prune cache 0") || !slices.ContainsFunc(ops, func(op string) bool {
		return strings.HasPrefix(op, "scan-database prune") && strings.HasSuffix(op, " all")
	}) {
		t.Fatalf("ops = %v; want all of the cache and the database", ops)
	}
}

// A builder that scans nothing needs none of its scanner database: a
// prune without targets frees all of it and keeps the build cache.
func TestDiskOfBuilderThatScansNothing(t *testing.T) {
	h := newHarness(t)
	const gb = 1 << 30
	h.rt.SetHostStats(node.HostStats{Disks: []node.Disk{{Paths: []string{"/var/lib/docker", "/var/lib/dock"}, Total: 40 * gb, Used: 20 * gb}}})
	h.rt.SetDiskUsage(node.DiskUsage{Volumes: []node.VolumeUsage{{Name: "dock_scan_db_abc", Role: node.VolumeScanDB, Bytes: 2 * gb}}})
	local, err := h.store.GetLocalServer(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.UpdateServerSettings(h.ctx, store.UpdateServerSettingsParams{BaseDomain: local.BaseDomain, Builder: true, BuildsNonProduction: true, ID: local.ID}); err != nil {
		t.Fatal(err)
	}
	d, err := h.c.ServerDisk(h.ctx, testSystem, local.Name)
	if err != nil {
		t.Fatal(err)
	}
	if want := []apitypes.DiskPrune{{Target: apitypes.PruneScanner, Bytes: 2 * gb, Safe: true}}; !slices.Equal(d.Prunes, want) {
		t.Fatalf("prunes = %+v, want %+v", d.Prunes, want)
	}
	since := len(h.rt.Ops())
	if _, err := h.c.PruneServer(h.ctx, testSystem, local.Name, nil); err != nil {
		t.Fatal(err)
	}
	ops := h.rt.Ops()[since:]
	if !slices.ContainsFunc(ops, func(op string) bool {
		return strings.HasPrefix(op, "scan-database prune") && strings.HasSuffix(op, " all")
	}) || slices.Contains(ops, "prune cache 0") {
		t.Fatalf("ops = %v; want all of the database and the cache kept", ops)
	}
}

// A prune's images come from every release on the server: each live
// service's rollback window and active release are kept, the rest are
// unneeded, and with the registry the kept ones it has too, but the
// active release's and the one before it, are older rollbacks.
func TestImagePruneSortsReleases(t *testing.T) {
	p, _ := labPool(t)
	p.opts.Config.Defaults.KeepReleases = 3
	p.withRegistry(&fakeRegistry{})
	p.buildsProduction("homelab")
	web := p.createService("production", "web", apitypes.CreateServiceRequest{})
	var rels []apitypes.Release
	for range 5 {
		rels = append(rels, p.deploySource(web))
	}
	ids := func(rs ...apitypes.Release) []string {
		var out []string
		for _, r := range rs {
			out = append(out, r.ImageID)
		}
		slices.Sort(out)
		return out
	}
	if len(slices.Compact(ids(rels...))) != 5 {
		t.Fatalf("five builds made %v", ids(rels...))
	}
	srv, err := p.c.store.GetServer(p.ctx, p.ids["cp"])
	if err != nil {
		t.Fatal(err)
	}
	req, err := p.c.imagePrune(p.ctx, srv)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(req.Keep, ids(rels[2:]...)) || !slices.Equal(req.Unneeded, ids(rels[:2]...)) || !slices.Equal(req.Rollbacks, ids(rels[2])) {
		t.Fatalf("keep %v, unneeded %v, rollbacks %v; releases %v", req.Keep, req.Unneeded, req.Rollbacks, ids(rels...))
	}
	if !slices.Contains(req.Pinned, p.c.cfg.Scan.GrypeImage) || req.Tool != p.c.cfg.Backup.ResticImage {
		t.Errorf("pinned %v, tool %q", req.Pinned, req.Tool)
	}
}

// A kept reading is answered without asking the server again while it is
// fresh. An older one is answered at once, marked refreshing, and the new
// reading goes out as a disk event; a prune reads it again too.
func TestServerDiskKeepsTheLastReading(t *testing.T) {
	h := newHarness(t)
	srv := h.c.localName()
	reads := func() int {
		n := 0
		for _, op := range h.rt.Ops() {
			if strings.HasPrefix(op, "disk usage") {
				n++
			}
		}
		return n
	}
	first, err := h.c.ServerDisk(h.ctx, testSystem, srv)
	if err != nil || first.Refreshing {
		t.Fatalf("first read = %+v, %v", first, err)
	}
	if again, err := h.c.ServerDisk(h.ctx, testSystem, srv); err != nil || again.Refreshing || reads() != 1 {
		t.Fatalf("a fresh reading was read again: %+v, %v, %d reads", again, err, reads())
	}

	sub := h.c.bus.subscribe(func(e busEvent) bool { return e.ev.Type == apitypes.EventDisk })
	defer sub.close()
	h.c.disk.mu.Lock()
	old := h.c.disk.last[h.c.localID]
	old.ReadAt = old.ReadAt.Add(-2 * diskFresh)
	h.c.disk.last[h.c.localID] = old
	h.c.disk.mu.Unlock()
	stale, err := h.c.ServerDisk(h.ctx, testSystem, srv)
	if err != nil || !stale.Refreshing || !stale.ReadAt.Equal(old.ReadAt) {
		t.Fatalf("an old reading = %+v, %v; want it at once, refreshing", stale, err)
	}
	select {
	case ev := <-sub.ch:
		if d := ev.Disk; d == nil || d.Refreshing || !d.ReadAt.After(old.ReadAt) || d.Server != srv {
			t.Fatalf("disk event = %+v", ev.Disk)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no disk event after the refresh")
	}
	if reads() != 2 {
		t.Fatalf("%d reads, want 2", reads())
	}

	if _, err := h.c.PruneServer(h.ctx, testSystem, srv, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sub.ch:
	case <-time.After(10 * time.Second):
		t.Fatal("no disk event after a prune")
	}
}
