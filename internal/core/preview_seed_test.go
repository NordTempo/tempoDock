package core

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// seedHarness is previewHarness with snapshot jobs answered and previews
// seeding db; restores records the snapshot restore jobs run.
func seedHarness(t *testing.T) (*harness, string, func() []node.JobSpec) {
	h, _, _, appHook := previewHarness(t)
	var mu sync.Mutex
	var restores []node.JobSpec
	h.snapshotJobs(func(spec node.JobSpec) *node.JobResult {
		if spec.Script == snapshotRestoreScript {
			mu.Lock()
			restores = append(restores, spec)
			mu.Unlock()
		}
		return nil
	})
	h.servicePreviews(ServiceRef{"shop", "staging", "db"}, apitypes.SetServicePreviewsRequest{Seed: true})
	return h, appHook, func() []node.JobSpec {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(restores)
	}
}

// waitPreviewEvent waits until preview env's last event contains want.
func (h *harness) waitPreviewEvent(env, want string) {
	h.t.Helper()
	// The audit event: the preview's last event moves on with its deploys.
	eventually(h.t, "preview.seed "+want, func() bool {
		evs, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Project: "shop", Environment: env, Action: "preview.seed"})
		return err == nil && slices.ContainsFunc(evs, func(e apitypes.AuditEvent) bool { return strings.Contains(e.Summary, want) })
	})
}

// A seeded database is filled from staging's latest snapshot, into the
// preview's own volume, before its first container; it keeps staging's
// credentials, which the copied data needs. Staging's volume isn't
// mounted, and its database keeps running.
func TestPreviewSeedFromSnapshot(t *testing.T) {
	h, appHook, restores := seedHarness(t)
	base := ServiceRef{"shop", "staging", "db"}
	h.snapshot(base)
	s := h.snapshot(base) // the newest is used
	h.settled("staging")
	before := h.envSnapshot("staging")
	if _, err := h.deliver(appHook, "d1", prEvent{action: "opened", number: 7, sha: shaA, updated: time.Now()}); err != nil {
		t.Fatal(err)
	}
	db := ServiceRef{"shop", "pr-7", "db"}
	if r := h.wait(db, h.waitReleases(db, 1)[0].Number); r.Status != apitypes.ReleaseActive {
		t.Fatalf("db release %+v", r)
	}
	h.waitPreviewEvent("pr-7", "db seeded from snapshot")
	jobs := restores()
	if len(jobs) != 1 || jobs[0].Env["SNAPSHOT"] != itoa(s.ID) {
		t.Fatalf("restore jobs %+v, want one of snapshot %d", jobs, s.ID)
	}
	// The snapshot volume, and the preview's own volume as the target.
	var into []string
	for _, m := range jobs[0].Mounts {
		if strings.HasPrefix(m.Path, snapshotVolumesPath+"/") {
			into = append(into, m.Volume)
		}
	}
	if want := h.dockerVolume(db); !slices.Equal(into, []string{want}) {
		t.Errorf("restored into %v, want the preview's own %s", into, want)
	}
	if slices.Contains(into, h.dockerVolume(base)) {
		t.Error("the base's volume was mounted")
	}
	ops := h.rt.Ops()
	fill := slices.IndexFunc(ops, func(op string) bool { return strings.Contains(op, "_restore_snapshot") })
	if start := indexOf(ops, "start "+h.ctr(db, 1)); fill < 0 || start < fill {
		t.Errorf("fill at %d, start at %d: %v", fill, start, ops)
	}
	if got, want := h.revealed(db)["POSTGRES_PASSWORD"], h.revealed(base)["POSTGRES_PASSWORD"]; got != want {
		t.Error("a seeded database needs the base's password, which its data files hold")
	}
	if got := h.revealed(ServiceRef{"shop", "pr-7", "web"}); got["TOKEN"] != "" {
		t.Error("seeding copied an unlisted literal of another service")
	}
	if h.envSnapshot("staging") != before {
		t.Error("the base was touched")
	}
}

// Without a snapshot, a seeded database starts empty and the preview says
// so; nothing is restored.
func TestPreviewSeedWithoutSnapshot(t *testing.T) {
	h, appHook, restores := seedHarness(t)
	if _, err := h.deliver(appHook, "d1", prEvent{action: "opened", number: 8, sha: shaA, updated: time.Now()}); err != nil {
		t.Fatal(err)
	}
	db := ServiceRef{"shop", "pr-8", "db"}
	if r := h.wait(db, h.waitReleases(db, 1)[0].Number); r.Status != apitypes.ReleaseActive {
		t.Fatalf("db release %+v", r)
	}
	h.waitPreviewEvent("pr-8", "db starts empty: shop/staging/db has no snapshot")
	if jobs := restores(); len(jobs) != 0 {
		t.Fatalf("restored without a snapshot: %+v", jobs)
	}
}

// Seeding is a setting like the others: admin only, and only of a base
// service with a volume.
func TestPreviewSeedSettings(t *testing.T) {
	h, _, _ := seedHarness(t)
	set, err := h.c.GetPreviews(h.ctx, testSystem, "shop")
	if err != nil || !slices.Equal(set.SeedFromSnapshot, []string{"db"}) {
		t.Fatalf("settings %+v, %v", set.SeedFromSnapshot, err)
	}
	deployer := Actor{Kind: "token", OrgID: 1, Name: "deploy", Scope: apitypes.ScopeDeploy}
	db := ServiceRef{"shop", "staging", "db"}
	if _, err := h.c.SetServicePreviews(h.ctx, deployer, db, apitypes.SetServicePreviewsRequest{Seed: true}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("deploy scope set a seed: %v", err)
	}
	// api has no volume to seed.
	if _, err := h.c.SetServicePreviews(h.ctx, testSystem, ServiceRef{"shop", "staging", "api"}, apitypes.SetServicePreviewsRequest{Seed: true}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("seed api: %v", err)
	}
}

// dockerVolume is the Docker name of ref's one volume.
func (h *harness) dockerVolume(ref ServiceRef) string {
	h.t.Helper()
	vols, err := h.store.ListVolumes(h.ctx, h.serviceID(ref))
	if err != nil || len(vols) != 1 {
		h.t.Fatalf("volumes of %v: %+v, %v", ref, vols, err)
	}
	return vols[0].DockerName
}
