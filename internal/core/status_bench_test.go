package core

import (
	"fmt"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// BenchmarkStatusPage100 is a published page's public view with 100
// components of one uptime check each, 90 days of history (day rows,
// and this hour's minute rows; hour rows of today are left out) and 50
// incidents: what a status page's render reads, at most once every few seconds a page and
// only while someone views it.
func BenchmarkStatusPage100(b *testing.B) {
	bc := newBenchCore(b)
	checks := benchChecks(b, bc, 100)
	t := time.Now()
	today := t.Unix() - t.Unix()%uptimeDay
	// As the statistics tick leaves it: days rolled up to today, hours up
	// to this one.
	minuteFrom := t.Unix() - t.Unix()%uptimeHour
	bc.c.uptime.mu.Lock()
	bc.c.uptime.rolledTo, bc.c.uptime.dayRolledTo = minuteFrom, today
	bc.c.uptime.mu.Unlock()
	comps := make([]apitypes.StatusComponentInput, len(checks))
	for i, chk := range checks {
		comps[i] = apitypes.StatusComponentInput{Name: fmt.Sprintf("Component %d", i), Group: fmt.Sprintf("Group %d", i/10),
			Sources: []apitypes.StatusSourceRef{{Check: chk.ID}}}
		// 89 day rows and this hour's minute rows, one statement each.
		if _, err := bc.c.store.DB.ExecContext(bc.ctx, `WITH RECURSIVE d(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM d WHERE n < ?)
			INSERT INTO uptime_stats (check_id, res, at, up, total, latency_ms_sum) SELECT ?, 86400, ? - n * 86400, 2880, 2880, 0 FROM d`,
			apitypes.StatusDays-1, chk.ID, today); err != nil {
			b.Fatal(err)
		}
		if _, err := bc.c.store.DB.ExecContext(bc.ctx, `WITH RECURSIVE m(at) AS (SELECT ? UNION ALL SELECT at + 60 FROM m WHERE at + 60 < ?)
			INSERT INTO uptime_stats (check_id, res, at, up, total, latency_ms_sum) SELECT ?, 60, at, 2, 2, 80 FROM m`,
			minuteFrom, t.Unix(), chk.ID); err != nil {
			b.Fatal(err)
		}
	}
	if _, err := bc.c.CreateStatusPage(bc.ctx, testSystem, apitypes.CreateStatusPageRequest{Slug: "bench"}); err != nil {
		b.Fatal(err)
	}
	if _, err := bc.c.SetStatusComponents(bc.ctx, testSystem, "bench", apitypes.SetStatusComponentsRequest{Components: comps}); err != nil {
		b.Fatal(err)
	}
	on := true
	if _, err := bc.c.UpdateStatusPage(bc.ctx, testSystem, "bench", apitypes.UpdateStatusPageRequest{Published: &on}); err != nil {
		b.Fatal(err)
	}
	// 50 incidents over the 90 days, two components and three updates
	// each, all but one resolved.
	for i := range 50 {
		inc, err := bc.c.OpenIncident(bc.ctx, testSystem, "bench", apitypes.OpenIncidentRequest{Title: fmt.Sprintf("Incident %d", i), Message: "Looking into it.",
			Components: []apitypes.StatusImpact{{Component: fmt.Sprintf("Component %d", i), Impact: apitypes.StatusPartialOutage},
				{Component: fmt.Sprintf("Component %d", i+50), Impact: apitypes.StatusDegraded}}})
		if err != nil {
			b.Fatal(err)
		}
		if _, err := bc.c.PostIncidentUpdate(bc.ctx, testSystem, inc.ID, apitypes.PostIncidentUpdateRequest{State: apitypes.IncidentIdentified, Message: "Found it."}); err != nil {
			b.Fatal(err)
		}
		if i > 0 {
			if _, err := bc.c.PostIncidentUpdate(bc.ctx, testSystem, inc.ID, apitypes.PostIncidentUpdateRequest{State: apitypes.IncidentResolved, Message: "Fixed."}); err != nil {
				b.Fatal(err)
			}
		}
		at := t.Add(-time.Duration(i) * 40 * time.Hour)
		if _, err := bc.c.store.DB.ExecContext(bc.ctx, `UPDATE status_incidents SET starts_at = ?, resolved_at = CASE WHEN resolved_at IS NULL THEN NULL ELSE ? END WHERE public_id = ?`,
			at, at.Add(time.Hour), inc.ID); err != nil {
			b.Fatal(err)
		}
	}
	p, err := bc.c.store.GetStatusPage(bc.ctx, store.GetStatusPageParams{OrgID: 1, Slug: "bench"})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		page, err := bc.c.PublicStatusPage(bc.ctx, p.ID)
		if err != nil || len(page.Groups) != 10 || len(page.Open) != 1 {
			b.Fatalf("page: %d groups, %v", len(page.Groups), err)
		}
	}
}
