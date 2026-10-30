package core

import (
	"fmt"
	"testing"

	"github.com/NordTempo/tempoDock/internal/analytics/ingest"
	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// pageview is a spooled page view of visitor v, as the ingest writes it.
func pageview(v int) []byte {
	return fmt.Appendf(nil, `{"t":1798000000000,"s":3,"sv":40,"rl":981,"h":"shop.example.com","k":1,"p":"/","v":%d}`, v)
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
	eventually(t, "the store to have the spool's events", func() bool { return len(h.rt.AnalyticsStore().Events()) == 2 })
	eventually(t, "the spool to be acknowledged", func() bool { return h.rt.SpoolAcked() == 2 })

	// Live: an append reaches the store without anything polling.
	h.rt.AppendSpool(pageview(3))
	eventually(t, "a live event", func() bool { return len(h.rt.AnalyticsStore().Events()) == 3 })

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
	if _, err := h.c.SetAnalyticsServer(h.ctx, admin, ""); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("an org admin chose the analytics server: %v", err)
	}
	if _, err := h.c.SetAnalyticsServer(h.ctx, testSystem, "nope"); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("an unknown server: %v", err)
	}
}

func TestAnalyticsOnAnotherServer(t *testing.T) {
	p, lab := labPool(t)
	cp := p.rt
	lab.AppendSpool(pageview(1))
	cp.AppendSpool(pageview(2))
	if _, err := p.c.SetAnalyticsServer(p.ctx, testSystem, "homelab"); err != nil {
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

	// Moving it back: homelab's store stops (its data stays on its
	// volume) and new events go to the control plane's.
	if _, err := p.c.SetAnalyticsServer(p.ctx, testSystem, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the store to move", func() bool { return lab.AnalyticsStoreSpec() == nil && cp.AnalyticsStore() != nil })
	cp.AppendSpool(pageview(3))
	eventually(t, "a new event in the new store", func() bool {
		for _, e := range cp.AnalyticsStore().Events() {
			if e.Visitor == 3 {
				return true
			}
		}
		return false
	})
}
