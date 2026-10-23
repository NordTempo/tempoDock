package core

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Retention deletes the history of old releases out of the rollback
// window, and keeps every release a rollback can use (the newest
// keep_releases and the active one, however old) and recent ones.
func TestReleaseHistoryRetentionKeepsRollbackWindow(t *testing.T) {
	h := newHarness(t)
	<-h.c.stats.firstTick // its retention pass is done
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	h.deploy(web, "app:2") // active from here on; release 1 is superseded
	h.rt.FailPull(errors.New("manifest unknown"))
	for range 6 {
		h.deploy(web, "app:bad") // releases 3 to 8 fail
	}
	svc := h.serviceID(web)
	rels, err := h.store.ListReleases(h.ctx, store.ListReleasesParams{ServiceID: svc, Limit: 100})
	if err != nil || len(rels) != 8 {
		t.Fatalf("releases = %d, %v", len(rels), err)
	}
	byNumber := map[int64]store.Release{}
	for _, r := range rels {
		byNumber[r.Number] = r
		for _, p := range []string{h.c.buildLogPath(r.ID), h.c.containerLogPath(r.ID)} {
			if err := os.WriteFile(p, []byte("output\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	has := func(number int64) bool {
		r := byNumber[number]
		_, err := os.Stat(h.c.buildLogPath(r.ID))
		events, _ := h.store.ListReleaseEvents(h.ctx, r.ID)
		_, cerr := os.Stat(h.c.containerLogPath(r.ID))
		if (err == nil) != (len(events) > 0) || (err == nil) != (cerr == nil) {
			t.Fatalf("release %d is half trimmed: log %v, %d events, container log %v", number, err, len(events), cerr)
		}
		return err == nil
	}

	if got := h.c.retain(h.ctx, time.Now()); got.releases != 0 {
		t.Fatalf("recent releases were trimmed: %+v", got)
	}
	later := time.Now().Add(31 * 24 * time.Hour)
	if got := h.c.retain(h.ctx, later); got.releases != 2 {
		t.Fatalf("trimmed %d release histories, want 2 (releases 1 and 3)", got.releases)
	}
	for n := int64(1); n <= 8; n++ {
		want := n != 1 && n != 3 // 2 is active, 4 to 8 are the window
		if has(n) != want {
			t.Fatalf("release %d history kept = %v, want %v", n, !want, want)
		}
	}
	if got := h.c.retain(h.ctx, later); got.releases != 0 {
		t.Fatalf("a second pass trimmed %d again", got.releases)
	}
	// A trimmed release still reads, without its history.
	rel, err := h.c.GetRelease(h.ctx, testSystem, web, 1)
	if err != nil || len(rel.Events) != 0 {
		t.Fatalf("release 1 = %+v, %v", rel, err)
	}
}

// Audit events go once older than audit_days or past audit_max_events,
// oldest first, with their details.
func TestAuditRetention(t *testing.T) {
	h := newHarness(t)
	<-h.c.stats.firstTick // its retention pass is done
	add := func(at time.Time, detail bool) (event, detailID int64) {
		p := store.AddAuditEventWithDetailParams{Actor: "system", ActorKind: "system", Action: "data.query", Target: "x", Summary: "y", CreatedAt: at}
		if detail {
			id, err := h.store.AddAuditDetail(h.ctx, store.AddAuditDetailParams{Sql: "select 1", Outcome: "ok"})
			if err != nil {
				t.Fatal(err)
			}
			p.DetailID, detailID = &id, id
		}
		e, err := h.store.AddAuditEventWithDetail(h.ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		return e.ID, detailID
	}
	old := time.Now().Add(-400 * 24 * time.Hour)
	add(old, false)
	_, oldDetail := add(old, true)
	add(old.Add(time.Hour), false)
	_, newDetail := add(time.Now(), true)
	add(time.Now(), false)
	newest, _ := add(time.Now(), false)

	if got := h.c.retain(h.ctx, time.Now()); got.audit != 3 {
		t.Fatalf("trimmed %d audit events, want 3", got.audit)
	}
	if _, err := h.store.GetAuditDetail(h.ctx, oldDetail); !store.IsNotFound(err) {
		t.Fatalf("an old event's detail is left: %v", err)
	}
	if _, err := h.store.GetAuditDetail(h.ctx, newDetail); err != nil {
		t.Fatalf("a kept event's detail is gone: %v", err)
	}
	h.c.cfg.Retention.AuditMaxEvents = 1
	if got := h.c.retain(h.ctx, time.Now()); got.audit != 2 {
		t.Fatalf("trimmed %d past the cap, want 2", got.audit)
	}
	left, _ := h.store.ListOldestAuditEvents(h.ctx, 10)
	if len(left) != 1 || left[0].ID != newest {
		t.Fatalf("left = %+v", left)
	}
}

// Resolved alerts are kept alert_days; open ones stay however old.
func TestResolvedAlertRetention(t *testing.T) {
	h := newHarness(t)
	<-h.c.stats.firstTick // its retention pass is done
	t0 := time.Now()
	h.c.raiseAlert(h.ctx, alertSignal{Key: "a", Kind: "test", Severity: apitypes.SeverityWarning, Title: "a"}, t0)
	h.c.raiseAlert(h.ctx, alertSignal{Key: "b", Kind: "test", Severity: apitypes.SeverityWarning, Title: "b"}, t0)
	h.c.resolveAlert(h.ctx, "a", t0)
	if got := h.c.retain(h.ctx, t0.Add(91*24*time.Hour)); got.alerts != 1 {
		t.Fatalf("pruned %d alerts, want 1", got.alerts)
	}
	all, _ := h.c.ListAlerts(h.ctx, testSystem, true, 0, 0)
	if len(all) != 1 || all[0].Title != "b" {
		t.Fatalf("alerts = %+v", all)
	}
}
