package core

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/analytics/fakestore"
	"github.com/NordTempo/tempoDock/internal/analytics/ingest"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node/fake"
	"github.com/NordTempo/tempoDock/internal/node/remote"
)

// pageview is a spooled page view of visitor v, as the ingest writes it.
func pageview(v int) []byte {
	return fmt.Appendf(nil, `{"t":1798000000000,"s":3,"sv":40,"rl":981,"h":"shop.example.com","k":1,"p":"/","v":%d}`, v)
}

// storeEvents counts the fake analytics store's events; 0 until the
// store has started, which happens in the background.
func storeEvents(h *harness) int {
	if s := h.rt.AnalyticsStore(); s != nil {
		return len(s.Events())
	}
	return 0
}

func TestAnalyticsForwardsTheSpool(t *testing.T) {
	h := newHarness(t)
	// Events wait in the spool before analytics is even on.
	h.rt.AppendSpool(pageview(1), pageview(2))
	if err := h.c.EnableAnalytics(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the store to start", func() bool { return h.rt.AnalyticsStoreSpec() != nil })
	spec := h.rt.AnalyticsStoreSpec()
	if spec == nil || len(spec.Token) < 32 || spec.MemoryBytes != 512<<20 {
		t.Fatalf("store spec = %+v", spec)
	}
	eventually(t, "the store to have the spool's events", func() bool { return storeEvents(h) == 2 })
	eventually(t, "the spool to be acknowledged", func() bool { return h.rt.SpoolAcked() == 2 })

	// Live: an append reaches the store without anything polling.
	h.rt.AppendSpool(pageview(3))
	eventually(t, "a live event", func() bool { return storeEvents(h) == 3 })

	st, err := h.c.AnalyticsStatus(h.ctx, testSystem)
	if err != nil || !st.Enabled || !st.StoreRunning || st.Events != 3 || st.Server != h.c.local.Name {
		t.Errorf("status = %+v, %v", st, err)
	}

	// After a restart the forwarder resumes from the store's cursor:
	// nothing is written twice.
	h.restart()
	h.rt.AppendSpool(pageview(4))
	eventually(t, "the event after a restart", func() bool {
		s := h.rt.AnalyticsStore()
		return s != nil && len(s.Events()) == 4
	})
}

func TestAnalyticsWaitsForTheStore(t *testing.T) {
	h := newHarness(t)
	if err := h.c.EnableAnalytics(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the store", func() bool { return h.rt.AnalyticsStore() != nil })
	fs := h.rt.AnalyticsStore()
	fs.SetDown(true)
	h.rt.AppendSpool(pageview(1))
	// Nothing is lost while the store is down: the spool keeps it until
	// the store has it.
	if h.rt.SpoolAcked() != 0 {
		t.Fatal("acknowledged while the store was down")
	}
	fs.SetDown(false)
	eventually(t, "the backlog after the store came back", func() bool { return len(fs.Events()) == 1 && h.rt.SpoolAcked() == 1 })
}

func TestAnalyticsSalts(t *testing.T) {
	h := newHarness(t)
	today := ingest.Day(now())
	eventually(t, "today's and tomorrow's salts on the server", func() bool {
		s := h.rt.AnalyticsSalts()
		return len(s[today]) == ingest.SaltBytes && len(s[today+1]) == ingest.SaltBytes && len(s) == 2
	})
	first := h.rt.AnalyticsSalts()[today]
	// A restart keeps the day's salt: visitors stay the same people.
	h.restart()
	eventually(t, "the salts after a restart", func() bool { return string(h.rt.AnalyticsSalts()[today]) == string(first) })

	// The next day: yesterday's salt is gone everywhere.
	h.c.rotateSalts(h.ctx, now().AddDate(0, 0, 1))
	eventually(t, "the next day's salts", func() bool {
		s := h.rt.AnalyticsSalts()
		_, old := s[today]
		return !old && len(s[today+2]) == ingest.SaltBytes
	})
	rows, err := h.store.ListAnalyticsSalts(h.ctx, 0)
	if err != nil || len(rows) != 2 || rows[0].Day != today+1 {
		t.Errorf("salts kept: %+v, %v", rows, err)
	}
}

func TestAnalyticsNeedsAnOperator(t *testing.T) {
	h := newHarness(t)
	admin := Actor{Name: "org-admin", Kind: "token", Scope: apitypes.ScopeAdmin, OrgID: 1}
	if err := h.c.EnableAnalytics(h.ctx, admin); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("an org admin turned analytics on: %v", err)
	}
	if _, err := h.c.SetAnalyticsServer(h.ctx, admin, apitypes.SetAnalyticsServerRequest{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("an org admin chose the analytics server: %v", err)
	}
	if _, err := h.c.SetAnalyticsServer(h.ctx, testSystem, apitypes.SetAnalyticsServerRequest{Server: "nope"}); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("an unknown server: %v", err)
	}
}

// analyticsPool is labPool with homelab on this protocol, so it can run
// the analytics store.
func analyticsPool(t *testing.T) (*pool, *fake.Runtime) {
	t.Helper()
	p, lab := labPool(t)
	p.nodes.add(p.ids["homelab"], lab, analyticsHello)
	return p, lab
}

// analyticsHello is a server on this protocol.
var analyticsHello = remote.Hello{Version: "v0.12.1", Protocol: remote.Protocol, Arch: "arm64", Facts: remote.Facts{Ingress: remote.IngressDirect}}

// visitors are the visitor numbers of a fake store's events, sorted.
func visitors(s *fakestore.Store) []uint64 {
	var out []uint64
	for _, e := range s.Events() {
		out = append(out, e.Visitor)
	}
	slices.Sort(out)
	return out
}

func TestAnalyticsOnAnotherServer(t *testing.T) {
	p, lab := analyticsPool(t)
	cp := p.rt
	lab.AppendSpool(pageview(1))
	cp.AppendSpool(pageview(2))
	if _, err := p.c.SetAnalyticsServer(p.ctx, testSystem, apitypes.SetAnalyticsServerRequest{Server: "homelab"}); err != nil {
		t.Fatal(err)
	}
	// Both servers' spools go to the store on homelab, and the control
	// plane never runs one of its own.
	eventually(t, "the store on homelab with both spools", func() bool {
		s := lab.AnalyticsStore()
		return s != nil && len(s.Events()) == 2
	})
	if cp.AnalyticsStoreSpec() != nil {
		t.Error("a store started on the control plane too")
	}
	if err := p.c.RemoveServer(p.ctx, testSystem, "homelab"); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("removing the analytics server: %v", err)
	}
}

// Moving the store copies its data to the new server, then removes the
// store (container, data, binary) from the old one; events that arrive
// meanwhile wait in the spools and none is lost.
func TestAnalyticsMovesWithItsData(t *testing.T) {
	p, lab := analyticsPool(t)
	cp := p.rt
	cp.AppendSpool(pageview(1), pageview(2))
	if err := p.c.EnableAnalytics(p.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the store on the control plane", func() bool {
		s := cp.AnalyticsStore()
		return s != nil && len(s.Events()) == 2
	})

	st, err := p.c.SetAnalyticsServer(p.ctx, testSystem, apitypes.SetAnalyticsServerRequest{Server: "homelab"})
	if err != nil {
		t.Fatal(err)
	}
	// The fake runtime may finish the move before the status is read.
	if (st.MovingTo != "homelab" || st.Server != p.c.local.Name) && (st.MovingTo != "" || st.Server != "homelab") {
		t.Errorf("status as the move starts = %+v", st)
	}
	lab.AppendSpool(pageview(3))
	eventually(t, "the store on homelab with the old data and the new event", func() bool {
		s := lab.AnalyticsStore()
		return s != nil && slices.Equal(visitors(s), []uint64{1, 2, 3})
	})
	eventually(t, "the old store removed", func() bool { return cp.AnalyticsStoreSpec() == nil })
	if spec := lab.AnalyticsStoreSpec(); spec == nil || len(spec.SHA256) != 64 {
		t.Errorf("homelab's store spec = %+v", spec)
	}
	cp.AppendSpool(pageview(4))
	eventually(t, "a new event, counted once", func() bool {
		return slices.Equal(visitors(lab.AnalyticsStore()), []uint64{1, 2, 3, 4})
	})
	st, err = p.c.AnalyticsStatus(p.ctx, testSystem)
	if err != nil || st.Server != "homelab" || st.MovingTo != "" || st.Error != "" || st.Events != 4 {
		t.Errorf("status after the move = %+v, %v", st, err)
	}
	events, err := p.c.ListAudit(p.ctx, testSystem, AuditFilter{Platform: true, Action: "analytics.moved"})
	// 2 or 3 events: homelab's may reach the old store before the copy.
	if err != nil || len(events) != 1 || !strings.Contains(events[0].Summary, "removed it from "+p.c.local.Name) {
		t.Errorf("audit = %+v, %v", events, err)
	}
}

// A move that fails leaves the store where it was, removes the partial
// copy and says why; forwarding goes on to the old store.
func TestAnalyticsMoveThatFailsStays(t *testing.T) {
	p, lab := analyticsPool(t)
	cp := p.rt
	if err := p.c.EnableAnalytics(p.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the store", func() bool { return cp.AnalyticsStore() != nil })
	lab.FailEnsureAnalyticsStore(errors.New("disk full"))
	if _, err := p.c.SetAnalyticsServer(p.ctx, testSystem, apitypes.SetAnalyticsServerRequest{Server: "homelab"}); err != nil {
		t.Fatal(err)
	}
	var st apitypes.AnalyticsStatus
	eventually(t, "the move to fail", func() bool {
		st, _ = p.c.AnalyticsStatus(p.ctx, testSystem)
		return st.MovingTo == "" && st.Error != ""
	})
	if st.Server != p.c.local.Name || !strings.Contains(st.Error, "disk full") || !strings.Contains(st.Error, "stays on "+p.c.local.Name) {
		t.Errorf("status = %+v", st)
	}
	if !slices.Contains(lab.Ops(), "remove-analytics-store") {
		t.Error("the partial copy wasn't removed")
	}
	cp.AppendSpool(pageview(1))
	eventually(t, "forwarding to the old store again", func() bool { return len(cp.AnalyticsStore().Events()) == 1 })
}

// The old server offline: its data can't be copied, so the move needs
// --start-empty; the old store goes once that server is back.
func TestAnalyticsMoveFromAnOfflineServer(t *testing.T) {
	p, lab := analyticsPool(t)
	if _, err := p.c.SetAnalyticsServer(p.ctx, testSystem, apitypes.SetAnalyticsServerRequest{Server: "homelab"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the store on homelab", func() bool { return lab.AnalyticsStore() != nil })
	p.setOffline("homelab", true)
	p.c.NodeStatus(remote.State{ServerID: p.ids["homelab"], Server: "homelab", Status: remote.StatusOffline})

	_, err := p.c.SetAnalyticsServer(p.ctx, testSystem, apitypes.SetAnalyticsServerRequest{})
	if CodeOf(err) != apitypes.CodeServerOffline || !strings.Contains(err.Error(), "--start-empty") {
		t.Fatalf("a move from an offline server: %v", err)
	}
	if _, err := p.c.SetAnalyticsServer(p.ctx, testSystem, apitypes.SetAnalyticsServerRequest{StartEmpty: true}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the store on the control plane", func() bool {
		st, _ := p.c.AnalyticsStatus(p.ctx, testSystem)
		return st.Server == p.c.local.Name && st.StoreRunning && st.MovingTo == ""
	})

	p.setOffline("homelab", false)
	p.c.NodeStatus(remote.State{ServerID: p.ids["homelab"], Server: "homelab", Status: remote.StatusOnline, Hello: analyticsHello})
	eventually(t, "homelab's old store removed", func() bool { return lab.AnalyticsStoreSpec() == nil })
}

// A store that fails to start says why, and starts once the status is
// read again after the cause is gone: nothing retries on a timer.
func TestAnalyticsStoreStartsAgain(t *testing.T) {
	h := newHarness(t)
	h.rt.FailEnsureAnalyticsStore(errors.New("pull access denied"))
	if err := h.c.EnableAnalytics(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	var st apitypes.AnalyticsStatus
	eventually(t, "the start to fail", func() bool {
		st, _ = h.c.AnalyticsStatus(h.ctx, testSystem)
		return !st.Starting && st.Error != ""
	})
	if st.StoreRunning || !strings.Contains(st.Error, "pull access denied") {
		t.Errorf("status = %+v", st)
	}
	// Not at once: a page refetching as a start ends doesn't start the
	// next one.
	h.rt.FailEnsureAnalyticsStore(nil)
	if st, _ = h.c.AnalyticsStatus(h.ctx, testSystem); st.Starting {
		t.Error("reading the status started the store again right after it failed")
	}
	h.c.analytics.mu.Lock()
	h.c.analytics.startedAt = h.c.analytics.startedAt.Add(-storeRetryAfter)
	h.c.analytics.mu.Unlock()
	eventually(t, "the store started by reading the status", func() bool {
		st, _ = h.c.AnalyticsStatus(h.ctx, testSystem)
		return st.StoreRunning && st.Error == ""
	})
}
