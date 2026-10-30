package core

import (
	"slices"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
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
		Volumes: []node.VolumeUsage{
			{Name: "dock_v1", Service: "1", Bytes: 2 * gb},
			{Name: node.BuildCacheVolume, Bytes: 3 * gb},
			{Name: "dock_backups_abc", Bytes: gb / 2},
		},
		Containers: gb / 4, DataDir: gb / 4,
	})
	srv := h.c.local.Name
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
	if d.ReclaimableBytes != 3*gb {
		t.Errorf("reclaimable = %d, want unused images and cache above the kept amount", d.ReclaimableBytes)
	}
	if got["other/rest of the filesystem"].Bytes != 20*gb-(3+4+2+1+2+3)*gb-gb/2-gb/4-gb/4 {
		t.Errorf("rest = %+v", got["other/rest of the filesystem"])
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

	res, err := h.c.PruneServer(h.ctx, testSystem, srv)
	if err != nil || res.Server != srv {
		t.Fatalf("prune = %+v, %v", res, err)
	}
	ops := h.rt.Ops()
	if !slices.ContainsFunc(ops, func(o string) bool { return len(o) > 12 && o[:12] == "prune images" }) {
		t.Errorf("prune didn't prune images: %v", ops)
	}
}
