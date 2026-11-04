package core

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Alerts (ADR 0020). The alert engine turns what the daemon already
// observes into alerts. Events on the bus raise and resolve most of them
// as they happen (a release failed or went live, a container restarted, a
// domain's certificate failed, a server came back); the statistics tick
// adds what needs a clock (disk use, a server offline for minutes, a crash
// loop that calmed down, reminders). Nothing polls: between events and
// ticks the engine's goroutine sleeps on its bus subscription.
//
// An alert's key names its condition (disk:<server ID>, deploy:<service>);
// at most one alert per key is open. Raising an open key updates the
// alert in place, resolving it closes it. Open alerts live in memory and
// in the DB; each change is written, published as an alert event to
// operators and handed to the notifier (alerts_notify.go).

// alertSignal raises or resolves an alert. Code that sees something
// alert-worthy beyond the built-in conditions (a failed backup) calls
// signalAlert, or attaches a signal to the event it publishes anyway
// (busEvent.alert): the engine picks it up from the bus.
type alertSignal struct {
	// Key names the condition: one open alert per key.
	Key      string
	Kind     apitypes.AlertKind
	Severity apitypes.AlertSeverity
	Title    string
	Message  string
	// What the alert is about, where that applies.
	Project, Environment, Service, Server string
	projectID, serviceID                  int64
	// Resolve closes the open alert with Key; the other fields are unused.
	Resolve bool
}

// alertEngine is the alert state. Its methods are called from the bus
// subscriber, the statistics tick and API requests, under mu.
type alertEngine struct {
	mu sync.Mutex
	// open holds the open alerts by key: a handful, bounded by servers,
	// services and domains.
	open map[string]store.Alert
	// restarts tracks container restarts per running service (stable ID)
	// for crash loops; an entry goes when the service stops (archived,
	// nothing deployed) or a new release goes live. A quiet entry is kept
	// with no times, so its next restart counts.
	restarts map[string]*restartLog
	// offline is when each unreachable server was first seen so.
	offline map[int64]time.Time
	// started bounds how long a server counts as offline: nothing is
	// known of the time before the daemon started.
	started time.Time
	// outbox holds notifications being sent (alerts_notify.go).
	outbox outbox
	// client posts to webhook and Slack channels (newChannelClient);
	// loopback lets it reach this server, for tests only.
	client   *http.Client
	loopback atomic.Bool
}

// restartLog is a service's restart count as last seen and the times of
// its latest restarts (at most [alerts] crash_loop_restarts).
type restartLog struct {
	count int
	times []time.Time
}

func newAlertEngine() *alertEngine {
	e := &alertEngine{
		open: map[string]store.Alert{}, restarts: map[string]*restartLog{}, offline: map[int64]time.Time{},
		outbox: outbox{queues: map[int64][]apitypes.AlertNotification{}},
	}
	e.client = newChannelClient(e)
	return e
}

// startAlerts loads the open alerts, brings deploy and domain alerts in
// line with the DB (they may have changed while the daemon was down) and
// starts following the bus. Start calls it after recovery.
func (c *Core) startAlerts(ctx context.Context) error {
	rows, err := c.store.ListOpenAlerts(ctx)
	if err != nil {
		return fmt.Errorf("load alerts: %w", err)
	}
	e := c.alerts
	e.mu.Lock()
	e.started = time.Now()
	for _, r := range rows {
		e.open[r.Key] = r
	}
	e.mu.Unlock()
	sub := c.bus.subscribe(func(ev busEvent) bool {
		return ev.alert != nil || alertEventTypes[ev.ev.Type]
	})
	c.reconcileAlerts(ctx, time.Now())
	c.wg.Add(1)
	go c.watchAlerts(sub)
	return nil
}

// alertEventTypes are the bus events the engine reads.
var alertEventTypes = map[apitypes.EventType]bool{
	apitypes.EventRelease: true, apitypes.EventServiceStatus: true, apitypes.EventDomain: true, apitypes.EventServer: true,
}

// watchAlerts applies bus events until Core closes. If events were
// dropped, deploy and domain alerts are reconciled from the DB.
func (c *Core) watchAlerts(sub *subscription) {
	defer c.wg.Done()
	defer sub.close()
	for {
		select {
		case <-c.ctx.Done():
			return
		case ev := <-sub.ch:
			if sub.takeLagged() {
				c.reconcileAlerts(c.ctx, time.Now())
			}
			c.onAlertEvent(c.ctx, ev, time.Now())
		}
	}
}

// signalAlert raises or resolves an alert right away.
func (c *Core) signalAlert(ctx context.Context, s alertSignal) {
	if s.Resolve {
		c.resolveAlert(ctx, s.Key, time.Now())
		return
	}
	c.raiseAlert(ctx, s, time.Now())
}

// serviceKey is the human-readable address of a service.
func serviceKey(project, env, service string) string { return project + "/" + env + "/" + service }

// alertServiceKey uses a service's stable ID; events without an identity
// (synthetic events in tests) retain their human-readable address.
func alertServiceKey(id int64, project, env, service string) string {
	if id != 0 {
		return "service:" + strconv.FormatInt(id, 10)
	}
	return serviceKey(project, env, service)
}

// onAlertEvent applies one bus event.
func (c *Core) onAlertEvent(ctx context.Context, ev LiveEvent, now time.Time) {
	if s := ev.signal; s != nil {
		if s.Resolve {
			c.resolveAlert(ctx, s.Key, now)
		} else {
			c.raiseAlert(ctx, *s, now)
		}
	}
	svc := alertServiceKey(ev.serviceID, ev.Project, ev.Environment, ev.Service)
	switch ev.Type {
	case apitypes.EventRelease:
		r := ev.Release
		if r == nil {
			return
		}
		switch r.Status {
		case apitypes.ReleaseFailed:
			s := deployFailed(ev.Project, ev.Environment, ev.Service, r.Number, r.Error, ev.serviceID)
			s.projectID = ev.projectID
			c.raiseAlert(ctx, s, now)
		case apitypes.ReleaseActive:
			// A new container serves: its restarts start from zero.
			c.resolveAlert(ctx, "deploy:"+svc, now)
			c.resolveAlert(ctx, "crash:"+svc, now)
			// The health alert is resolved at the cutover (adoptHealth):
			// by the time this event is read, the new container may be
			// failing its own check already.
			c.alerts.mu.Lock()
			delete(c.alerts.restarts, svc)
			c.alerts.mu.Unlock()
		}
	case apitypes.EventServiceStatus:
		if st := ev.ServiceStatus; st != nil {
			c.onServiceStatus(ctx, ev, svc, *st, now)
		}
	case apitypes.EventDomain:
		d := ev.Domain
		if d == nil {
			return
		}
		switch {
		case ev.Message == "removed" || d.Status == apitypes.DomainActive:
			c.resolveAlert(ctx, "domain:"+d.Hostname, now)
		case d.Status == apitypes.DomainError:
			s := domainError(ev.Project, ev.Environment, ev.Service, d.Hostname, d.StatusReason)
			s.projectID, s.serviceID = ev.projectID, ev.serviceID
			c.raiseAlert(ctx, s, now)
		}
	case apitypes.EventServer:
		s := ev.Server
		if s == nil {
			return
		}
		id := strconv.FormatInt(s.ID, 10)
		if ev.Message == "removed" {
			c.resolveAlert(ctx, "disk:"+id, now)
		}
		if ev.Message == "removed" || s.Status == apitypes.ServerOnline || s.Status == apitypes.ServerRevoked {
			c.resolveAlert(ctx, "server:"+id, now)
			c.alerts.mu.Lock()
			delete(c.alerts.offline, s.ID)
			c.alerts.mu.Unlock()
		}
	}
}

// deployFailed is the alert for a service whose newest release failed.
func deployFailed(project, env, service string, number int64, reason string, serviceID int64) alertSignal {
	svc := serviceKey(project, env, service)
	return alertSignal{
		Key: "deploy:" + alertServiceKey(serviceID, project, env, service), serviceID: serviceID, Kind: apitypes.AlertDeployFailed, Severity: apitypes.SeverityWarning,
		Title:   "Deploy of " + svc + " failed",
		Message: fmt.Sprintf("Release %d failed: %s. The release before it keeps serving.", number, reason),
		Project: project, Environment: env, Service: service,
	}
}

// backupFailed is the alert for a service's latest backup failing. The
// next successful backup resolves it.
func backupFailed(project, env, service string, id int64, reason string, serviceID int64) alertSignal {
	svc := serviceKey(project, env, service)
	return alertSignal{
		Key: "backup:" + alertServiceKey(serviceID, project, env, service), serviceID: serviceID, Kind: apitypes.AlertBackupFailed, Severity: apitypes.SeverityWarning,
		Title:   "Backup of " + svc + " failed",
		Message: fmt.Sprintf("Backup %d failed: %s.", id, strings.TrimRight(strings.TrimSpace(reason), ".:; ")),
		Project: project, Environment: env, Service: service,
	}
}

// domainError is the alert for a domain in error.
func domainError(project, env, service, hostname, reason string) alertSignal {
	return alertSignal{
		Key: "domain:" + hostname, Kind: apitypes.AlertDomain, Severity: apitypes.SeverityWarning,
		Title: "No certificate for " + hostname, Message: reason,
		Project: project, Environment: env, Service: service,
	}
}

// onServiceStatus counts a service's restarts and raises a crash loop
// alert when [alerts] crash_loop_restarts fall within crash_loop_minutes.
// A stopped service (archived, or nothing deployed) resolves it.
func (c *Core) onServiceStatus(ctx context.Context, ev LiveEvent, svc string, st apitypes.ServiceStatus, now time.Time) {
	if st.State == apitypes.ServiceNone || st.State == apitypes.ServiceStopped {
		c.alerts.mu.Lock()
		delete(c.alerts.restarts, svc)
		c.alerts.mu.Unlock()
		c.resolveAlert(ctx, "crash:"+svc, now)
		// This event may be read late: only a service that is still
		// stopped loses its health check state and alert.
		if s := c.getStatus(ev.serviceID).State; ev.serviceID != 0 && (s == apitypes.ServiceNone || s == apitypes.ServiceStopped) {
			c.resolveAlert(ctx, "health:"+svc, now)
			c.health.forget(ev.serviceID)
		}
		return
	}
	cfg := c.cfg.Alerts
	window := time.Duration(cfg.CrashLoopMinutes) * time.Minute
	e := c.alerts
	e.mu.Lock()
	log := e.restarts[svc]
	switch {
	case log == nil:
		// First sight (or a daemon restart): the count so far is history.
		e.restarts[svc] = &restartLog{count: st.RestartCount}
		e.mu.Unlock()
		return
	case st.RestartCount < log.count:
		// Another container: counting starts again.
		log.count, log.times = st.RestartCount, log.times[:0]
	}
	for n := min(st.RestartCount-log.count, cfg.CrashLoopRestarts); n > 0; n-- {
		log.times = append(log.times, now)
	}
	log.count = st.RestartCount
	if extra := len(log.times) - cfg.CrashLoopRestarts; extra > 0 {
		log.times = append(log.times[:0], log.times[extra:]...)
	}
	looping := len(log.times) >= cfg.CrashLoopRestarts && now.Sub(log.times[0]) <= window
	e.mu.Unlock()
	if !looping {
		return
	}
	msg := fmt.Sprintf("Restarted %d times in %d minutes; last exit code %d.", cfg.CrashLoopRestarts, cfg.CrashLoopMinutes, st.ExitCode)
	if st.OOMKilled {
		msg += fmt.Sprintf(" It was killed for using more than its %d MB memory limit.", st.MemoryLimitMB)
	}
	c.raiseAlert(ctx, alertSignal{
		Key: "crash:" + svc, Kind: apitypes.AlertCrashLoop, Severity: apitypes.SeverityCritical,
		Title:     serviceKey(ev.Project, ev.Environment, ev.Service) + " is crash looping",
		projectID: ev.projectID, serviceID: ev.serviceID, Message: msg,
		Project: ev.Project, Environment: ev.Environment, Service: ev.Service,
	}, now)
}

// alertTick runs on the statistics tick: disk alerts from the samples just
// taken (hosts, by server ID), servers offline for too long, crash loops
// that calmed down, analytics alerts that need a clock, and reminders.
func (c *Core) alertTick(ctx context.Context, now time.Time, servers []store.Server, hosts map[int64]apitypes.ServerUsage) {
	for _, srv := range servers {
		if u, ok := hosts[srv.ID]; ok && len(u.Disks) > 0 {
			c.diskAlert(ctx, srv, u, now)
		}
		if !srv.Local {
			c.offlineAlert(ctx, srv, now)
		}
	}
	c.calmCrashLoops(ctx, now)
	c.analyticsAlertTick(ctx, now)
	c.remind(ctx, now)
}

// diskPercent is how full the fullest of a server's disks is.
func diskPercent(u apitypes.ServerUsage) (pct float64, disk apitypes.DiskUsage) {
	for _, d := range u.Disks {
		if d.TotalBytes <= 0 {
			continue
		}
		if p := 100 * float64(d.UsedBytes) / float64(d.TotalBytes); p > pct {
			pct, disk = p, d
		}
	}
	return pct, disk
}

// diskHysteresis is how far below the warning threshold a disk must drop
// before its alert resolves, so one hovering at the line doesn't flap.
const diskHysteresis = 2

// diskAlert raises, updates or resolves a server's disk alert.
func (c *Core) diskAlert(ctx context.Context, srv store.Server, u apitypes.ServerUsage, now time.Time) {
	cfg := c.cfg.Alerts
	key := "disk:" + strconv.FormatInt(srv.ID, 10)
	pct, disk := diskPercent(u)
	sev := apitypes.SeverityWarning
	switch {
	case pct >= float64(cfg.DiskCriticalPercent):
		sev = apitypes.SeverityCritical
	case pct >= float64(cfg.DiskWarningPercent):
	case pct < float64(cfg.DiskWarningPercent-diskHysteresis):
		c.resolveAlert(ctx, key, now)
		return
	default:
		return // in the hysteresis band: an open alert stays as it is
	}
	shown := int(math.Floor(pct))
	msg := fmt.Sprintf("%s: %s of %s used.", strings.Join(disk.Paths, ", "), gb(disk.UsedBytes), gb(disk.TotalBytes))
	if p := c.cfg.Retention.PruneDiskPercent; p > 0 && shown >= p {
		msg += " Old images and build cache are pruned already; remove volumes or data that aren't needed, or grow the disk."
	}
	c.raiseAlert(ctx, alertSignal{
		Key: key, Kind: apitypes.AlertDisk, Severity: sev,
		Title: fmt.Sprintf("Disk %d%% full on %s", shown, srv.Name), Message: msg, Server: srv.Name,
	}, now)
}

// gb formats bytes as gigabytes with one decimal.
func gb(b int64) string { return strconv.FormatFloat(float64(b)/(1<<30), 'f', 1, 64) + " GB" }

// offlineAlert raises a server's offline alert once it has been
// unreachable for [alerts] server_offline_minutes, and resolves it when it
// is back or revoked.
func (c *Core) offlineAlert(ctx context.Context, srv store.Server, now time.Time) {
	key := "server:" + strconv.FormatInt(srv.ID, 10)
	status := c.serverStatus(srv)
	e := c.alerts
	e.mu.Lock()
	if status == apitypes.ServerOnline || status == apitypes.ServerRevoked {
		delete(e.offline, srv.ID)
		e.mu.Unlock()
		c.resolveAlert(ctx, key, now)
		return
	}
	since, ok := e.offline[srv.ID]
	if !ok {
		since = e.started
		if srv.LastSeenAt != nil && srv.LastSeenAt.After(since) {
			since = *srv.LastSeenAt
		}
		e.offline[srv.ID] = since
	}
	e.mu.Unlock()
	if now.Sub(since) < time.Duration(c.cfg.Alerts.ServerOfflineMinutes)*time.Minute {
		return
	}
	msg := fmt.Sprintf("Unreachable since %s. Its apps keep running, but nothing can be deployed there or watched until it is back.", since.UTC().Format("2006-01-02 15:04 MST"))
	if status == apitypes.ServerIncompatible {
		msg = "Connected, but its dock is too old for this control plane: nothing is deployed there until dock is upgraded on it."
	}
	c.raiseAlert(ctx, alertSignal{
		Key: key, Kind: apitypes.AlertServerOffline, Severity: apitypes.SeverityCritical,
		Title: "Server " + srv.Name + " is offline", Message: msg, Server: srv.Name,
	}, now)
}

// calmCrashLoops resolves crash loop alerts of services that haven't
// restarted for [alerts] crash_loop_minutes, and clears quiet services'
// restart times.
func (c *Core) calmCrashLoops(ctx context.Context, now time.Time) {
	window := time.Duration(c.cfg.Alerts.CrashLoopMinutes) * time.Minute
	e := c.alerts
	var calm []string
	e.mu.Lock()
	for svc, log := range e.restarts {
		quiet := len(log.times) == 0 || now.Sub(log.times[len(log.times)-1]) > window
		if quiet {
			log.times = log.times[:0]
		}
		if _, open := e.open["crash:"+svc]; open && quiet {
			calm = append(calm, "crash:"+svc)
		}
	}
	for key, a := range e.open {
		// A crash loop alert from before a restart, for a service that
		// hasn't restarted since.
		if a.Kind == string(apitypes.AlertCrashLoop) && e.restarts[strings.TrimPrefix(key, "crash:")] == nil && now.Sub(e.started) > window {
			calm = append(calm, key)
		}
	}
	e.mu.Unlock()
	for _, key := range calm {
		c.resolveAlert(ctx, key, now)
	}
}

// reconcileAlerts makes deploy and domain alerts match the DB: services
// whose newest release failed and domains in error have one, nothing else
// does. It runs on start and when the engine missed bus events.
func (c *Core) reconcileAlerts(ctx context.Context, now time.Time) {
	want := map[string]alertSignal{}
	failed, err := c.store.ListFailedLatestReleases(ctx)
	if err != nil {
		c.log.Warn("alerts: list failed releases", "err", err)
		return
	}
	for _, r := range failed {
		s := deployFailed(r.Project, r.Environment, r.Service, r.Number, r.Error, r.ServiceID)
		s.projectID = r.ProjectID
		want[s.Key] = s
	}
	pending, err := c.store.ListPendingDomains(ctx)
	if err != nil {
		c.log.Warn("alerts: list domains", "err", err)
		return
	}
	for _, d := range pending {
		if d.Status != string(apitypes.DomainError) {
			continue
		}
		ss, err := c.scopeOf(ctx, d.ServiceID)
		if err != nil || ss.svc.ArchivedAt != nil {
			continue
		}
		s := domainError(ss.project.Name, ss.env.Name, ss.svc.Name, d.Hostname, d.StatusReason)
		s.projectID, s.serviceID = ss.project.ID, ss.svc.ID
		want[s.Key] = s
	}
	var stale []string
	c.alerts.mu.Lock()
	for key, a := range c.alerts.open {
		kind := apitypes.AlertKind(a.Kind)
		if _, ok := want[key]; !ok && (kind == apitypes.AlertDeployFailed || kind == apitypes.AlertDomain) {
			stale = append(stale, key)
		}
	}
	c.alerts.mu.Unlock()
	for _, key := range stale {
		c.resolveAlert(ctx, key, now)
	}
	for _, s := range want {
		c.raiseAlert(ctx, s, now)
	}
}

// raiseAlert opens an alert for s.Key, or updates the open one when its
// severity, title or message changed. A new alert, or one whose severity
// went up, is sent to the channels.
func (c *Core) raiseAlert(ctx context.Context, s alertSignal, now time.Time) {
	t := now.UTC()
	e := c.alerts
	e.mu.Lock()
	if name := c.bus.projectName(s.projectID, s.Project); name != s.Project {
		s.Title = strings.Replace(s.Title, s.Project+"/", name+"/", 1)
		s.Project = name
	}
	cur, ok := e.open[s.Key]
	if ok {
		if cur.Severity == string(s.Severity) && cur.Title == s.Title && cur.Message == s.Message && cur.Project == s.Project {
			e.mu.Unlock()
			return
		}
		escalated := severityRank(s.Severity) > severityRank(apitypes.AlertSeverity(cur.Severity))
		cur.Severity, cur.Title, cur.Message, cur.Project, cur.UpdatedAt = string(s.Severity), s.Title, s.Message, s.Project, t
		err := c.store.UpdateAlert(ctx, store.UpdateAlertParams{Severity: cur.Severity, Title: cur.Title, Message: cur.Message, Project: cur.Project, UpdatedAt: t, ID: cur.ID})
		if err != nil {
			e.mu.Unlock()
			c.log.Error("update alert", "alert", s.Key, "err", err)
			return
		}
		e.open[s.Key] = cur
		e.mu.Unlock()
		c.publishAlert(cur)
		if escalated {
			c.notify(ctx, cur, "escalated", now)
		}
		return
	}
	var pid, sid *int64
	if s.projectID != 0 {
		pid = &s.projectID
	}
	if s.serviceID != 0 {
		sid = &s.serviceID
	}
	row, err := c.store.OpenAlert(ctx, store.OpenAlertParams{
		Key: s.Key, Kind: string(s.Kind), Severity: string(s.Severity), Title: s.Title, Message: s.Message,
		Project: s.Project, Environment: s.Environment, Service: s.Service, Server: s.Server, OpenedAt: t, UpdatedAt: t, ProjectID: pid, ServiceID: sid,
	})
	if err != nil {
		e.mu.Unlock()
		c.log.Error("open alert", "alert", s.Key, "err", err)
		return
	}
	e.open[s.Key] = row
	e.mu.Unlock()
	c.log.Info("alert opened", "alert", s.Key, "severity", s.Severity, "title", s.Title)
	c.publishAlert(row)
	c.notify(ctx, row, "opened", now)
}

// resolveAlert closes the open alert for key, if there is one, and tells
// the channels that were told about it.
func (c *Core) resolveAlert(ctx context.Context, key string, now time.Time) {
	e := c.alerts
	e.mu.Lock()
	a, ok := e.open[key]
	if !ok {
		e.mu.Unlock()
		return
	}
	t := now.UTC()
	if err := c.store.ResolveAlert(ctx, store.ResolveAlertParams{At: &t, ID: a.ID}); err != nil {
		e.mu.Unlock()
		c.log.Error("resolve alert", "alert", key, "err", err)
		return
	}
	delete(e.open, key)
	e.mu.Unlock()
	a.ResolvedAt, a.UpdatedAt = &t, t
	c.log.Info("alert resolved", "alert", key)
	c.publishAlert(a)
	c.notify(ctx, a, "resolved", now)
}

func severityRank(s apitypes.AlertSeverity) int {
	if s == apitypes.SeverityCritical {
		return 2
	}
	return 1
}

// publishAlert sends an alert event to operators.
func (c *Core) publishAlert(a store.Alert) {
	out := toAlert(a)
	var projectID int64
	if a.ProjectID != nil && a.ResolvedAt == nil {
		projectID = *a.ProjectID
	}
	c.bus.publish(busEvent{projectID: projectID, serverStats: true, ev: apitypes.Event{
		Type: apitypes.EventAlert, Project: a.Project, Environment: a.Environment, Service: a.Service, Alert: &out,
	}})
}

func toAlert(a store.Alert) apitypes.Alert {
	return apitypes.Alert{
		ID: a.ID, Kind: apitypes.AlertKind(a.Kind), Severity: apitypes.AlertSeverity(a.Severity), Title: a.Title, Message: a.Message,
		Project: a.Project, Environment: a.Environment, Service: a.Service, Server: a.Server,
		OpenedAt: a.OpenedAt, UpdatedAt: a.UpdatedAt, ResolvedAt: a.ResolvedAt,
	}
}

// maxAlertList caps one page of alerts.
const maxAlertList = 500

// ListAlerts returns the open alerts, worst and newest first, or with all
// every alert newest first (limit at most maxAlertList, paged with
// beforeID). Operators only.
func (c *Core) ListAlerts(ctx context.Context, a Actor, all bool, limit int, beforeID int64) ([]apitypes.Alert, error) {
	if err := a.requireOperator("reading alerts"); err != nil {
		return nil, err
	}
	if !all {
		c.alerts.mu.Lock()
		out := make([]apitypes.Alert, 0, len(c.alerts.open))
		for _, r := range c.alerts.open {
			out = append(out, toAlert(r))
		}
		c.alerts.mu.Unlock()
		sortAlerts(out)
		return out, nil
	}
	if limit <= 0 || limit > maxAlertList {
		limit = maxAlertList
	}
	if beforeID <= 0 {
		beforeID = math.MaxInt64
	}
	rows, err := c.store.ListAlerts(ctx, store.ListAlertsParams{BeforeID: beforeID, MaxRows: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.Alert, len(rows))
	for i, r := range rows {
		out[i] = toAlert(r)
	}
	return out, nil
}

// sortAlerts orders open alerts critical first, then newest first.
func sortAlerts(as []apitypes.Alert) {
	slices.SortFunc(as, func(x, y apitypes.Alert) int {
		if d := severityRank(y.Severity) - severityRank(x.Severity); d != 0 {
			return d
		}
		return int(y.ID - x.ID)
	})
}

// ResolveAlert closes an open alert by hand, for one whose condition
// won't clear by itself (a failed backup never retried). If the condition
// is still there, the engine raises a new one. Operators only.
func (c *Core) ResolveAlert(ctx context.Context, a Actor, id int64) (apitypes.Alert, error) {
	if err := a.requireOperator("resolving alerts"); err != nil {
		return apitypes.Alert{}, err
	}
	row, err := c.store.GetAlert(ctx, id)
	if store.IsNotFound(err) {
		return apitypes.Alert{}, notFound("alert %d not found; `dock alerts --all` lists alerts", id)
	}
	if err != nil {
		return apitypes.Alert{}, err
	}
	if row.ResolvedAt != nil {
		return toAlert(row), nil
	}
	c.resolveAlert(ctx, row.Key, time.Now())
	c.audit(ctx, a.platform(), "alert.resolve", nil, nil, "alert "+strconv.FormatInt(id, 10), "resolved alert: "+row.Title)
	if row, err = c.store.GetAlert(ctx, id); err != nil {
		return apitypes.Alert{}, err
	}
	return toAlert(row), nil
}
