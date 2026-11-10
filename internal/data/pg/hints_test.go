package pg

import (
	"strconv"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// TestHints: an idle transaction blocking another session is a warning
// on the holder, the duplicate that doesn't back a constraint is the one
// flagged, an unused index waits for a week of counters, and warnings
// come before notes.
func TestHints(t *testing.T) {
	if textMax != strconv.Itoa(apitypes.DataInsightTextMaxBytes) {
		t.Fatalf("textMax %s differs from DataInsightTextMaxBytes", textMax)
	}
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	mb := func(n int64) *int64 { n <<= 20; return &n }
	in := apitypes.DataInsights{
		Since: at(10 * 24 * time.Hour),
		Sessions: []apitypes.DataSession{
			{ID: "1.1", PID: 1, Application: "worker", User: "postgres", Kind: "client", State: "idle_in_transaction", StateAt: at(6 * time.Minute)},
			{ID: "2.2", PID: 2, Application: "api", Kind: "client", State: "active", QueryAt: at(time.Minute), BlockedBy: []string{"1.1"}},
			{ID: "3.3", PID: 3, Kind: "dock", State: "active", QueryAt: at(time.Hour)},
		},
		Indexes: []apitypes.DataIndexStats{
			{Schema: "public", Table: "orders", Name: "orders_status2", Bytes: mb(41), Scans: 3, Valid: true, Duplicate: true},
			{Schema: "public", Table: "orders", Name: "orders_status", Bytes: mb(41), Scans: 9, Valid: true, Duplicate: true, Constraint: true},
			{Schema: "public", Table: "events", Name: "events_at", Bytes: mb(212), Valid: true},
		},
		Tables: []apitypes.DataTableStats{{Schema: "public", Name: "events", EstimatedRows: 50_000, SeqScans: 1204, SeqRowsRead: 1204 * 38_000, IndexScans: 10, AnalyzedAt: at(time.Hour)}},
	}
	ev := evidence{dupGroups: map[string]string{"public.orders_status2": "g", "public.orders_status": "g"}}
	got := New(Options{}).hints(&in, ev, now)

	want := []struct {
		level   string
		kind    apitypes.DataHintKind
		subject string
	}{
		{"warn", apitypes.HintIdleInTransaction, "1.1"},
		{"warn", apitypes.HintDuplicateIndex, "public.orders_status2"},
		{"note", apitypes.HintFullScans, "public.events"},
		{"note", apitypes.HintUnusedIndex, "public.events_at"},
	}
	if len(got) != len(want) {
		t.Fatalf("hints: %+v", got)
	}
	for i, w := range want {
		if g := got[i]; g.Level != w.level || g.Kind != w.kind || g.Subject != w.subject {
			t.Errorf("hint %d: %+v, want %+v", i, g, w)
		}
	}
	if got[0].Detail != "worker (postgres), pid 1; holds its locks and holds back vacuum; blocks 1 session" {
		t.Errorf("idle detail: %q", got[0].Detail)
	}
	if got[1].Copy != `DROP INDEX CONCURRENTLY "public"."orders_status2";` {
		t.Errorf("duplicate copy: %q", got[1].Copy)
	}

	in.Since = at(2 * 24 * time.Hour)
	for _, h := range New(Options{}).hints(&in, ev, now) {
		if h.Kind == apitypes.HintUnusedIndex {
			t.Errorf("unused index flagged on two days of counters: %+v", h)
		}
	}
}
