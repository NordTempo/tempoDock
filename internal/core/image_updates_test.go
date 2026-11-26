package core

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The daily tag check is a step of its own: it runs when it is due
// whether or not a builder's database is, and Check now asks at once.
func TestTagCheck(t *testing.T) {
	h := scanHarness(t)
	tag := imageTag(templates[apitypes.TemplatePostgres].image)
	db := h.service("db", apitypes.CreateServiceRequest{Template: apitypes.TemplatePostgres})
	h.waitStatus(db, 1, apitypes.ReleaseActive)
	h.waitScan(db, apitypes.ScanScanned)

	// A day after the last check, with every database fresh.
	refreshes := func() int {
		return len(slices.DeleteFunc(h.rt.Ops(), func(op string) bool { return op != "scan-database update" }))
	}
	before := refreshes()
	h.rt.SetRemoteDigest(tag, "sha256:"+strings.Repeat("a", 64))
	h.dueTagCheck()
	h.waitTagCheck()
	if d, err := h.c.ServiceVulnerabilities(h.ctx, testSystem, db, 0); err != nil || d.Scan.Base == nil || !d.Scan.Base.Moved {
		t.Fatalf("after the daily check: %+v, %v", d.Scan.Base, err)
	}
	if refreshes() != before {
		t.Fatalf("the tag check came with a database refresh: %v", h.rt.Ops())
	}

	// Check now records a newer answer at once.
	newer := "sha256:" + strings.Repeat("b", 64)
	h.rt.SetRemoteDigest(tag, newer)
	h.agoTagChecks(time.Hour)
	scan, err := h.c.CheckImageTag(h.ctx, testSystem, db)
	if err != nil || scan.Base == nil || scan.Base.Latest != newer || !scan.Base.Own {
		t.Fatalf("check now = %+v, %v", scan.Base, err)
	}

	reader := Actor{Kind: "token", OrgID: 1, Name: "ci", TokenID: 9, Scope: apitypes.ScopeRead}
	if _, err := h.c.CheckImageTag(h.ctx, reader, db); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a read token checked: %v", err)
	}
	// An image deployed by digest alone follows no tag.
	pinned := h.service("pinned", apitypes.CreateServiceRequest{})
	h.deploy(pinned, "busybox@sha256:"+strings.Repeat("c", 64))
	h.waitScan(pinned, apitypes.ScanScanned)
	if _, err := h.c.CheckImageTag(h.ctx, testSystem, pinned); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("checked an image without a tag: %v", err)
	}
}

// With auto_update on, the daily check updates a service to its tag's
// newer image, a snapshot first; a frozen environment waits.
func TestAutoUpdate(t *testing.T) {
	h := scanHarness(t)
	tag := imageTag(templates[apitypes.TemplatePostgres].image)
	db := h.service("db", apitypes.CreateServiceRequest{Template: apitypes.TemplatePostgres})
	h.waitStatus(db, 1, apitypes.ReleaseActive)
	h.waitScan(db, apitypes.ScanScanned)

	// Only an image deployed by tag can follow it.
	web := h.service("web", apitypes.CreateServiceRequest{})
	if _, err := h.c.DeploySource(h.ctx, testSystem, web, sourceTar("v1"), 1<<20); err != nil {
		t.Fatal(err)
	}
	h.waitStatus(web, 1, apitypes.ReleaseActive)
	if _, err := h.c.SetAutoUpdate(h.ctx, testSystem, web, apitypes.AutoUpdateRequest{AutoUpdate: true}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("a built service turned auto updates on: %v", err)
	}
	if svc, err := h.c.SetAutoUpdate(h.ctx, testSystem, db, apitypes.AutoUpdateRequest{AutoUpdate: true}); err != nil || !svc.AutoUpdate {
		t.Fatalf("auto update = %v, %v", svc.AutoUpdate, err)
	}

	// Frozen: the check sees the newer image and leaves it.
	if _, err := h.c.LockEnvironment(h.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.LockEnvironmentRequest{People: true}); err != nil {
		t.Fatal(err)
	}
	newer := "sha256:" + strings.Repeat("d", 64)
	h.rt.SetRemoteDigest(tag, newer)
	h.dueTagCheck()
	h.waitTagCheck()
	if rels, _ := h.c.ListReleases(h.ctx, testSystem, db, 5); len(rels) != 1 {
		t.Fatalf("a frozen environment was updated: %d releases", len(rels))
	}

	if _, err := h.c.UnlockEnvironment(h.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.UnlockEnvironmentRequest{}); err != nil {
		t.Fatal(err)
	}
	h.dueTagCheck()
	h.waitTagCheck()
	h.waitStatus(db, 2, apitypes.ReleaseActive)
	rels, err := h.c.ListReleases(h.ctx, testSystem, db, 5)
	if err != nil || rels[0].ImageRef != tag+"@"+newer || rels[0].CreatedBy != autoUpdateActor {
		t.Fatalf("release 2 = %+v, %v", rels[0], err)
	}
	snaps, err := h.c.ServiceSnapshots(h.ctx, testSystem, db)
	if err != nil || len(snaps.Snapshots) != 1 || snaps.Snapshots[0].Trigger != apitypes.SnapshotBeforeDeploy {
		t.Fatalf("snapshots = %+v, %v", snaps.Snapshots, err)
	}
}

// dueTagCheck makes the daily tag check due and starts the scanner, as
// the hourly pass would a day after the last one.
func (h *harness) dueTagCheck() {
	h.c.scans.mu.Lock()
	h.c.scans.tagsChecked = time.Time{}
	h.c.scans.refreshAt = now().Add(-time.Minute)
	h.c.scans.mu.Unlock()
	h.c.kickScanRefresh()
}

// waitTagCheck waits for the daily check dueTagCheck started to finish,
// its updates started.
func (h *harness) waitTagCheck() {
	eventually(h.t, "the daily check", func() bool {
		h.c.scans.mu.Lock()
		defer h.c.scans.mu.Unlock()
		return !h.c.scans.tagsChecked.IsZero()
	})
}

// agoTagChecks moves every recorded tag check back by d, past Check
// now's minute.
func (h *harness) agoTagChecks(d time.Duration) {
	h.t.Helper()
	if _, err := h.c.store.DB.ExecContext(h.ctx, "UPDATE image_scans SET base_checked_at = base_checked_at - ?", int64(d.Seconds())); err != nil {
		h.t.Fatal(err)
	}
}
