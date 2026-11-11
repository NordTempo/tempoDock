package core

import (
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// webCheck is the schedule row of web's uptime check chk, as the
// scheduler hands it to recordProbe.
func (h *harness) webCheck(chk apitypes.UptimeCheck) store.ListUptimeScheduleRow {
	h.t.Helper()
	var r store.ListUptimeScheduleRow
	if err := h.store.DB.QueryRowContext(h.ctx, `SELECT s.id, e.id, e.project_id FROM services s JOIN environments e ON e.id = s.environment_id WHERE s.name = 'web'`).
		Scan(&r.ServiceID, &r.EnvironmentID, &r.ProjectID); err != nil {
		h.t.Fatal(err)
	}
	r.ID, r.Url, r.OrgID, r.Project, r.Environment, r.Service = chk.ID, chk.URL, 1, "shop", "production", "web"
	return r
}

// A maintenance window starts and ends on the tick. While it is in
// progress its components read maintenance, alerts about its services
// are recorded but sent to no channel (and draft no incident), and its
// checks' probes write no rows and never count as down. When it ends,
// alerts still open are sent; its day is marked.
func TestStatusMaintenance(t *testing.T) {
	h, checks, page := incidentPage(t)
	k := newReceiver(h)
	h.channel(apitypes.CreateAlertChannelRequest{Name: "hook", Kind: apitypes.ChannelWebhook, URL: k.srv.URL})
	t0 := time.Now().UTC()
	start, end := t0.Add(time.Hour), t0.Add(2*time.Hour)

	if _, err := h.c.ScheduleMaintenance(h.ctx, testSystem, "acme", apitypes.ScheduleMaintenanceRequest{Title: "Upgrade", Components: []string{"api"},
		StartsAt: start, EndsAt: start}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("a window ending as it starts: %v", err)
	}
	w, err := h.c.ScheduleMaintenance(h.ctx, testSystem, "acme", apitypes.ScheduleMaintenanceRequest{Title: "Database upgrade",
		Components: []string{"api"}, StartsAt: start, EndsAt: end, Message: "Writes pause."})
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != apitypes.KindMaintenance || w.State != apitypes.MaintenanceScheduled || w.Components[0].Impact != apitypes.StatusMaintenance ||
		w.EndsAt == nil || !w.EndsAt.Equal(end.Truncate(time.Microsecond)) && !w.EndsAt.Equal(end) {
		t.Fatalf("window = %+v", w)
	}
	pub, err := h.c.PublicStatusPage(h.ctx, page)
	if err != nil || len(pub.Maintenance) != 1 || len(pub.Open) != 0 || pub.Status != apitypes.StatusOperational {
		t.Fatalf("scheduled: %+v, %v", pub, err)
	}
	if _, err := h.c.PostIncidentUpdate(h.ctx, testSystem, w.ID, apitypes.PostIncidentUpdateRequest{State: apitypes.MaintenanceCompleted, Message: "Done."}); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("completing a window that hasn't started: %v", err)
	}

	h.c.maintenanceTick(h.ctx, start.Add(time.Second))
	if got := h.componentStatuses(t); got[0] != apitypes.StatusMaintenance || got[1] != apitypes.StatusOperational || got[2] != apitypes.StatusMaintenance {
		t.Errorf("in progress: %v", got)
	}
	if w, err = h.c.GetIncident(h.ctx, testSystem, w.ID); err != nil || w.State != apitypes.MaintenanceInProgress || w.Updates[1].By != "Dock" {
		t.Fatalf("started = %+v, %v", w, err)
	}

	chk := h.webCheck(checks[0])
	for range uptimeFailures + 1 {
		h.c.recordProbe(h.ctx, chk, "edge", node.ProbeResult{Error: "connection refused"}, time.Now())
	}
	if state, last := h.c.checkStatus(chk.ID); state == apitypes.UptimeDown || last == nil {
		t.Errorf("a muted check: %s, last probe %v", state, last)
	}
	var rows int
	if err := h.store.DB.QueryRowContext(h.ctx, `SELECT count(*) FROM uptime_stats WHERE check_id = ?`, chk.ID).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("a muted check wrote %d rows (%v)", rows, err)
	}
	h.c.raiseAlert(h.ctx, uptimeDown(chk, "edge", "connection refused", time.Now()), time.Now())
	h.outboxSent()
	h.c.alerts.mu.Lock()
	held := h.c.alerts.open[uptimeKey(chk.ID)]
	h.c.alerts.mu.Unlock()
	if held.MutedBy == nil {
		t.Fatalf("the alert isn't held: %+v", held)
	}
	if len(k.got) != 0 {
		t.Errorf("a held alert was sent: %+v", k.got)
	}
	if p, _ := h.c.GetStatusPage(h.ctx, testSystem, "acme"); len(p.Incidents) != 1 {
		t.Errorf("a held alert drafted an incident: %+v", p.Incidents)
	}

	if _, err := h.c.UpdateIncident(h.ctx, testSystem, w.ID, apitypes.UpdateIncidentRequest{EndsAt: new(end.Add(time.Hour))}); err != nil {
		t.Fatal(err)
	}
	h.c.maintenanceTick(h.ctx, end.Add(time.Second))
	if got := h.componentStatuses(t); got[0] != apitypes.StatusMaintenance {
		t.Errorf("an extended window ended at its old end: %v", got)
	}
	h.c.maintenanceTick(h.ctx, end.Add(time.Hour+time.Second))
	if evs := k.events(t, 1); evs[0] != "opened" {
		t.Errorf("the held alert at the window's end: %v", evs)
	}
	h.c.alerts.mu.Lock()
	held = h.c.alerts.open[uptimeKey(chk.ID)]
	h.c.alerts.mu.Unlock()
	if held.MutedBy != nil {
		t.Error("the alert is still held after the window")
	}
	if pub, err = h.c.PublicStatusPage(h.ctx, page); err != nil {
		t.Fatal(err)
	}
	if len(pub.Maintenance) != 0 || len(pub.Recent) != 1 || pub.Recent[0].State != apitypes.MaintenanceCompleted ||
		!pub.Groups[0].Components[0].Days[apitypes.StatusDays-1].Maintenance {
		t.Errorf("completed: maintenance %d, recent %+v", len(pub.Maintenance), pub.Recent)
	}
	// Without the percentages the day keeps its maintenance.
	if _, err := h.c.UpdateStatusPage(h.ctx, testSystem, "acme", apitypes.UpdateStatusPageRequest{ShowUptime: new(false)}); err != nil {
		t.Fatal(err)
	}
	if pub, err = h.c.PublicStatusPage(h.ctx, page); err != nil || !pub.Groups[0].Components[0].Days[apitypes.StatusDays-1].Maintenance {
		t.Errorf("without uptime the maintenance day went: %v", err)
	}
	if _, err := h.c.UpdateStatusPage(h.ctx, testSystem, "acme", apitypes.UpdateStatusPageRequest{ShowUptime: new(true)}); err != nil {
		t.Fatal(err)
	}

	// A cancelled window leaves nothing public.
	c, err := h.c.ScheduleMaintenance(h.ctx, testSystem, "acme", apitypes.ScheduleMaintenanceRequest{Title: "Later",
		Components: []string{"Jobs"}, StartsAt: t0.Add(24 * time.Hour), EndsAt: t0.Add(25 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.PostIncidentUpdate(h.ctx, testSystem, c.ID, apitypes.PostIncidentUpdateRequest{State: apitypes.MaintenanceCancelled, Message: "Not needed."}); err != nil {
		t.Fatal(err)
	}
	if pub, err = h.c.PublicStatusPage(h.ctx, page); err != nil || len(pub.Maintenance) != 0 || len(pub.Recent) != 1 {
		t.Errorf("cancelled: %+v, %v", pub.Maintenance, err)
	}
	h.c.maint.mu.Lock()
	n, next := len(h.c.maint.windows), h.c.maint.next
	h.c.maint.mu.Unlock()
	if n != 0 || !next.IsZero() {
		t.Errorf("windows left in memory: %d, next %v", n, next)
	}
}

// A window whose start has passed starts as it is scheduled.
func TestStatusMaintenanceStartsNow(t *testing.T) {
	h, _, _ := incidentPage(t)
	w, err := h.c.ScheduleMaintenance(h.ctx, testSystem, "acme", apitypes.ScheduleMaintenanceRequest{Title: "Now",
		Components: []string{"API"}, StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if w.State != apitypes.MaintenanceInProgress {
		t.Errorf("state = %s", w.State)
	}
	if !h.c.checkMuted(0, h.webCheck(apitypes.UptimeCheck{}).ServiceID) {
		t.Error("the window's service isn't muted")
	}
}
