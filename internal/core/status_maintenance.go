package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Maintenance windows on status pages (ADR 0073, phase 3). A window is an
// incident of kind maintenance: scheduled, in progress, completed or
// cancelled, over the components it names. Dock starts and completes
// windows on the minute statistics tick (maintenanceTick), which looks
// at them only while one is scheduled or in progress: one time
// comparison against the next boundary.
//
// While a window is in progress, what its components are built from is
// muted: alerts about those services and checks are recorded (with
// muted_by) but sent to no channel and get no reminders, and their
// uptime probes write no rows and never count as down, so planned work
// neither pages anyone nor dents the 90-day percentages. When the window
// ends, its alerts still open are sent: a deploy that broke something
// during maintenance still pages someone.

const (
	// statusMaxWindow bounds a maintenance window's length.
	statusMaxWindow = 7 * 24 * time.Hour
	// Dock's own words when it starts and ends a window.
	maintStarted   = "Maintenance started."
	maintCompleted = "Maintenance completed."
)

// maintState is what Dock holds in memory about maintenance windows not
// finished yet: a handful, read when Dock starts and whenever one
// changes.
type maintState struct {
	mu      sync.Mutex
	windows []maintWindow
	// next is the earliest boundary (a scheduled window's start, an
	// in-progress one's end); zero without windows.
	next time.Time
	// services and checks are what in-progress windows mute, with the
	// window muting each.
	services, checks map[int64]int64
}

// maintWindow is a window and what its components are built from.
type maintWindow struct {
	id, page         int64
	state            apitypes.IncidentState
	start, end       time.Time
	services, checks []int64
}

// loadMaintenance reads the windows not finished yet and works out what
// they mute and when the next one starts or ends.
func (c *Core) loadMaintenance(ctx context.Context) error {
	rows, err := c.store.ListMaintenanceWindows(ctx)
	if err != nil {
		return err
	}
	var windows []maintWindow
	for _, r := range rows {
		if n := len(windows); n == 0 || windows[n-1].id != r.ID {
			end := r.StartsAt
			if r.EndsAt != nil {
				end = *r.EndsAt
			}
			windows = append(windows, maintWindow{id: r.ID, page: r.PageID, state: apitypes.IncidentState(r.State), start: r.StartsAt, end: end})
		}
		w := &windows[len(windows)-1]
		if r.ServiceID != nil {
			w.services = append(w.services, *r.ServiceID)
		}
		if r.CheckID != nil {
			w.checks = append(w.checks, *r.CheckID)
		}
	}
	m := &c.maint
	m.mu.Lock()
	defer m.mu.Unlock()
	m.windows, m.next = windows, time.Time{}
	m.services, m.checks = map[int64]int64{}, map[int64]int64{}
	for _, w := range windows {
		at := w.start
		if w.state == apitypes.MaintenanceInProgress {
			at = w.end
			for _, id := range w.services {
				m.services[id] = w.id
			}
			for _, id := range w.checks {
				m.checks[id] = w.id
			}
		}
		if m.next.IsZero() || at.Before(m.next) {
			m.next = at
		}
	}
	return nil
}

// maintMutedBy is the in-progress window muting the alert s, 0 for none:
// one whose components show its service, or the uptime check it is
// about.
func (c *Core) maintMutedBy(s alertSignal) int64 {
	m := &c.maint
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.services) == 0 && len(m.checks) == 0 {
		return 0
	}
	if id, ok := m.services[s.serviceID]; ok && s.serviceID != 0 {
		return id
	}
	if s.Kind == apitypes.AlertUptimeDown {
		var check int64
		if _, err := fmt.Sscanf(s.Key, "uptime:%d", &check); err == nil {
			return m.checks[check]
		}
	}
	return 0
}

// checkMuted says whether an in-progress window mutes an uptime check:
// it, or its service, is what a component of the window shows.
func (c *Core) checkMuted(check, service int64) bool {
	m := &c.maint
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.services) == 0 && len(m.checks) == 0 {
		return false
	}
	_, byCheck := m.checks[check]
	_, byService := m.services[service]
	return byCheck || byService
}

// maintenanceTick starts the scheduled windows whose time has come and
// completes the in-progress ones whose end has passed. Without windows,
// or before the next boundary, it is one comparison.
func (c *Core) maintenanceTick(ctx context.Context, now time.Time) {
	m := &c.maint
	m.mu.Lock()
	if m.next.IsZero() || now.Before(m.next) {
		m.mu.Unlock()
		return
	}
	var start, end []int64
	for _, w := range m.windows {
		switch {
		case w.state == apitypes.MaintenanceScheduled && !now.Before(w.start):
			start = append(start, w.id)
		case w.state == apitypes.MaintenanceInProgress && !now.Before(w.end):
			end = append(end, w.id)
		}
	}
	m.mu.Unlock()
	for _, id := range start {
		c.moveWindow(ctx, id, apitypes.MaintenanceInProgress, maintStarted, now)
	}
	for _, id := range end {
		c.moveWindow(ctx, id, apitypes.MaintenanceCompleted, maintCompleted, now)
	}
	c.maintenanceChanged(ctx)
}

// moveWindow starts or completes a window at its boundary, posting Dock's
// update, as the system.
func (c *Core) moveWindow(ctx context.Context, id int64, to apitypes.IncidentState, msg string, now time.Time) {
	row, err := c.store.GetStatusIncidentByID(ctx, id)
	if err != nil {
		c.log.Error("maintenance: read a window", "id", id, "err", err)
		return
	}
	next := incidentNext(row.StatusIncident)
	next.State = string(to)
	if to == apitypes.MaintenanceCompleted {
		next.ResolvedAt = &now
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if _, err := q.UpdateStatusIncident(ctx, next); err != nil {
			return err
		}
		_, err := q.CreateStatusUpdate(ctx, store.CreateStatusUpdateParams{IncidentID: id, State: string(to), Body: msg, At: now, CreatedBy: statusDraftBy})
		return err
	})
	if err != nil {
		c.log.Error("maintenance: move a window", "id", id, "to", to, "err", err)
		return
	}
	action := "maintenance.start"
	if to == apitypes.MaintenanceCompleted {
		action = "maintenance.complete"
	}
	c.auditIncident(ctx, System, action, row.StatusPage, row.StatusIncident.PublicID,
		fmt.Sprintf("maintenance %s %q %s", row.StatusIncident.PublicID, row.StatusIncident.Title, strings.ReplaceAll(string(to), "_", " ")))
	c.incidentChanged()
}

// maintenanceChanged reloads the windows after one changed and sends the
// alerts that windows no longer in progress held.
func (c *Core) maintenanceChanged(ctx context.Context) {
	if err := c.loadMaintenance(ctx); err != nil {
		c.log.Error("maintenance: read the windows", "err", err)
		return
	}
	c.releaseHeld(ctx, time.Now())
	c.incidentChanged()
}

// releaseHeld sends the open alerts held by windows that are no longer in
// progress, as though they had just opened.
func (c *Core) releaseHeld(ctx context.Context, now time.Time) {
	m := &c.maint
	m.mu.Lock()
	running := map[int64]bool{}
	for _, w := range m.windows {
		if w.state == apitypes.MaintenanceInProgress {
			running[w.id] = true
		}
	}
	m.mu.Unlock()
	var held []store.Alert
	e := c.alerts
	e.mu.Lock()
	for key, a := range e.open {
		if a.MutedBy != nil && !running[*a.MutedBy] {
			a.MutedBy = nil
			e.open[key] = a
			held = append(held, a)
		}
	}
	e.mu.Unlock()
	for _, a := range held {
		if err := c.store.UnmuteAlert(ctx, a.ID); err != nil {
			c.log.Error("maintenance: send a held alert", "alert", a.Key, "err", err)
		}
		c.publishAlert(a)
		c.notify(ctx, a, "opened", now)
	}
}

// ScheduleMaintenance schedules a maintenance window on a page over the
// components named, public at once. A window whose start has passed
// starts now. Deploy scope, like incidents.
func (c *Core) ScheduleMaintenance(ctx context.Context, a Actor, slug string, req apitypes.ScheduleMaintenanceRequest) (apitypes.StatusIncident, error) {
	if err := incidentsIn(a, "scheduling maintenance"); err != nil {
		return apitypes.StatusIncident{}, err
	}
	p, err := c.loadStatusPage(ctx, a, slug, apitypes.ScopeRead, "scheduling maintenance")
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	title, err := cleanTitle(req.Title)
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	msg, err := cleanMessage(req.Message)
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	t := now()
	start, end := req.StartsAt.UTC(), req.EndsAt.UTC()
	if err := checkWindow(start, end, t); err != nil {
		return apitypes.StatusIncident{}, err
	}
	if len(req.Components) == 0 {
		return apitypes.StatusIncident{}, invalid("a maintenance window names the components it takes down; pass at least one")
	}
	ims, err := c.resolveImpacts(ctx, a, p, maintImpacts(req.Components), apitypes.KindMaintenance)
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	if msg == "" {
		msg = "Scheduled maintenance."
	}
	var row store.StatusIncident
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		row, err = q.CreateStatusIncident(ctx, store.CreateStatusIncidentParams{PageID: p.ID, PublicID: newPublicID(), Kind: string(apitypes.KindMaintenance),
			Title: title, State: string(apitypes.MaintenanceScheduled), StartsAt: start, EndsAt: &end, PublishedAt: &t, CreatedAt: t, CreatedBy: a.Name})
		if err != nil {
			return err
		}
		if err := writeImpacts(ctx, q, row.ID, ims); err != nil {
			return err
		}
		_, err = q.CreateStatusUpdate(ctx, store.CreateStatusUpdateParams{IncidentID: row.ID, State: row.State, Body: msg, At: t, CreatedBy: a.Name})
		return err
	})
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	c.auditIncident(ctx, a, "maintenance.schedule", p, row.PublicID, fmt.Sprintf("scheduled maintenance %s %q from %s to %s on %s",
		row.PublicID, title, start.Format(time.RFC3339), end.Format(time.RFC3339), impactsText(ims)))
	c.maintenanceChanged(ctx)
	c.maintenanceTick(ctx, t)
	return c.GetIncident(ctx, a, row.PublicID)
}

// maintImpacts are the components a window names, each under maintenance.
func maintImpacts(names []string) []apitypes.StatusImpact {
	out := make([]apitypes.StatusImpact, len(names))
	for i, n := range names {
		out[i] = apitypes.StatusImpact{Component: n, Impact: apitypes.StatusMaintenance}
	}
	return out
}

// checkWindow refuses a window that ends before it starts, has ended
// already or is longer than statusMaxWindow.
func checkWindow(start, end, now time.Time) error {
	switch {
	case start.IsZero() || end.IsZero():
		return invalid("a maintenance window needs a start and an end")
	case !end.After(start):
		return invalid("a maintenance window ends after it starts")
	case !end.After(now):
		return invalid("that window has ended already; pick an end in the future")
	case end.Sub(start) > statusMaxWindow:
		return invalid("a maintenance window is at most %d days; schedule several", int(statusMaxWindow.Hours()/24))
	}
	return nil
}

// maintTransition checks that an update may move a window from state
// from to state to, and says what the move is for the audit log.
func maintTransition(id string, from, to apitypes.IncidentState) (string, error) {
	switch {
	case to == from && (to == apitypes.MaintenanceScheduled || to == apitypes.MaintenanceInProgress):
		return "maintenance.update", nil
	case from == apitypes.MaintenanceScheduled && to == apitypes.MaintenanceInProgress:
		return "maintenance.start", nil
	case from == apitypes.MaintenanceInProgress && to == apitypes.MaintenanceCompleted:
		return "maintenance.complete", nil
	case to == apitypes.MaintenanceCancelled:
		return "maintenance.cancel", nil
	case from == apitypes.MaintenanceScheduled && to == apitypes.MaintenanceCompleted:
		return "", conflict("maintenance %s hasn't started; cancel it instead (`dock maintenance cancel %s`)", id, id)
	}
	return "", invalid("a maintenance window's update is scheduled, in_progress, completed or cancelled, and moves forward; it is %s", from)
}
