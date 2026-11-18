package core

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
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
// in the DB; each change is written, published as an alert event and
// handed to the notifier (alerts_notify.go).
//
// An alert belongs to an org or to the platform (ADR 0062), decided by
// what it names when it opens (alertOrg): one about a project or service
// is that org's, for its members and its channels; one about a server or
// Dock itself is the platform's, for operators and platform channels.

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
	// Message is a human sentence; the command that shows more goes in
	// Command, what it points at in Target.
	Message string
	Command string
	Target  *apitypes.AlertTarget
	// What the alert is about, where that applies.
	Project, Environment, Service, Server string
	projectID, serviceID                  int64
	// orgID, when set, is the org the alert belongs to, for one that
	// names no project or service (an org's own connection).
	orgID int64
	// Resolve closes the open alert with Key; the other fields are unused.
	Resolve bool
}

// alertEngine is the alert state. Its methods are called from the bus
// subscriber, the statistics tick and API requests, under mu.
type alertEngine struct {
	// mu guards the maps below. Nothing holding it writes to the store: a
	// write there would hold every other alert path behind SQLite's
	// write lock, and its busy wait.
	mu sync.Mutex
	// mailTested is when each organization (0: the platform) last tested
	// an email channel (TestAlertChannel): an entry per organization.
	mailTested map[int64]time.Time
	// busy holds, by key, the raise or resolve writing an alert now,
	// closed when it is done (hold): one write per alert at a time.
	busy map[string]chan struct{}
	// writes is held shared by every alert write and alone by a project
	// rename, which relabels the project's open alerts in its transaction,
	// and by a project delete, which deletes its alerts.
	writes sync.RWMutex
	// sub is the engine's bus subscription, which alertsHandled sends its
	// barrier through.
	sub *subscription
	// open holds the open alerts by key: a handful, bounded by servers,
	// services and domains.
	open map[string]store.Alert
	// restarts tracks container restarts per running service (stable ID)
	// for crash loops; an entry goes when the service stops (archived,
	// nothing deployed) or a new release goes live. A quiet entry is kept
	// with no times, so its next restart counts.
	restarts map[string]*restartLog
	// failed is the number of each service's newest failed release while
	// its deploy alert may be open: an older release's active event, which
	// its cutover publishes after the release reads active, can arrive
	// after a newer release already failed, and must not resolve that
	// failure's alert. An entry goes when a release at least as new goes
	// live; bounded by services.
	failed map[string]int64
	// offline is when each unreachable server was first seen so.
	offline map[int64]time.Time
	// hot is when each server's memory or CPU was first seen above its
	// threshold, by "memory:<id>" or "cpu:<id>"; an entry goes when the
	// server is back under it. Bounded by servers.
	hot map[string]time.Time
	// causes are the diagnoses (ADR 0071) of open crash loop and
	// unhealthy alerts, by alert key: raiseAlert says the cause and points
	// at it. An entry is kept while its alert is open, so the condition is
	// diagnosed once per alert; resolveAlert removes it. diagnosing marks
	// a crash loop whose diagnosis is being captured, before its alert
	// opens. Both are bounded by services.
	causes     map[string]alertCause
	diagnosing map[string]bool
	// started bounds how long a server counts as offline: nothing is
	// known of the time before the daemon started.
	started time.Time
	// outbox holds notifications being sent (alerts_notify.go), hooks the
	// status pages' updates being posted (status_hooks.go).
	outbox outbox
	hooks  deliveryQueue[hookDelivery]
	// client posts to the platform's webhook and Slack channels
	// (newChannelClient), never to addresses guard refuses; orgClient to
	// orgs' channels and status page hooks, never to what orgGuard
	// refuses, which also keeps private addresses out (ADR 0062).
	client, orgClient *http.Client
	guard, orgGuard   egressGuard
}

// alertCause is an alert's diagnosis: its release, the likely cause
// (empty when no rule matched) and the command that shows it.
type alertCause struct {
	release          int64
	summary, command string
}

// withCause says a diagnosed alert's likely cause and points it at the
// diagnosis; the message's cause is the rule's sentence, never log text.
func withCause(s alertSignal, cs alertCause) alertSignal {
	if cs.summary != "" {
		s.Message += " Likely cause: " + cs.summary + "."
	}
	s.Command, s.Target = cs.command, &apitypes.AlertTarget{Release: cs.release}
	return s
}

// diagnosedAlert captures a running service's diagnosis for an alert of
// kind about to open (a crash loop), then raises it with its cause, so
// the first notification says it. Runs in its own goroutine: capturing
// reads the container over the node link, within a few seconds.
func (c *Core) diagnosedAlert(s alertSignal, kind apitypes.DiagnosisKind) {
	defer c.wg.Done()
	ctx := c.ctx
	var ref *apitypes.DiagnosisRef
	ss, err := c.scopeOf(ctx, s.serviceID)
	if err == nil {
		ref = c.diagnoseRunning(ctx, ss, kind)
	}
	e := c.alerts
	e.mu.Lock()
	delete(e.diagnosing, s.Key)
	// A service stopped meanwhile lost its restart count: no alert.
	still := e.restarts[strings.TrimPrefix(s.Key, "crash:")] != nil
	if ref != nil && still {
		e.causes[s.Key] = c.causeOf(ss, ref)
	}
	e.mu.Unlock()
	if still && ctx.Err() == nil {
		c.raiseAlert(ctx, s, time.Now())
	}
}

// causeOf is the alert cause of a running service's diagnosis.
func (c *Core) causeOf(ss svcScope, ref *apitypes.DiagnosisRef) alertCause {
	cs := alertCause{command: fmt.Sprintf("dock diagnose %s", svcFlags(ss.project.Name, ss.env.Name, ss.svc.Name))}
	if ref.Rule != "" {
		cs.summary = ref.Summary
	}
	if rel, err := c.store.GetActiveRelease(c.ctx, ss.svc.ID); err == nil {
		cs.release = rel.Number
	}
	return cs
}

// restartLog is a service's restart count as last seen and the times of
// its latest restarts (at most [alerts] crash_loop_restarts).
type restartLog struct {
	count int
	times []time.Time
}

// newAlertEngine is the alerts' state; allow is the private CIDRs orgs'
// channels and hooks may reach (`[network] allow_private`).
func newAlertEngine(allow []netip.Prefix) *alertEngine {
	e := &alertEngine{
		open: map[string]store.Alert{}, restarts: map[string]*restartLog{}, failed: map[string]int64{}, offline: map[int64]time.Time{}, hot: map[string]time.Time{},
		causes: map[string]alertCause{}, diagnosing: map[string]bool{}, busy: map[string]chan struct{}{},
		outbox: outbox{queues: map[int64][]apitypes.AlertNotification{}},
	}
	e.guard.what = "alerts"
	e.orgGuard.what, e.orgGuard.private, e.orgGuard.allow = "alerts", true, allow
	e.client, e.orgClient = newChannelClient(&e.guard), newChannelClient(&e.orgGuard)
	return e
}

// startAlerts moves email channels onto Dock's email server (ADR 0079),
// makes the channel copies migration 0070 asked for, loads the open alerts, brings deploy and domain alerts in
// line with the DB (they may have changed while the daemon was down) and
// starts following the bus. Start calls it after recovery.
func (c *Core) startAlerts(ctx context.Context) error {
	if err := c.moveEmailChannels(ctx); err != nil {
		return fmt.Errorf("move email channels onto Dock's email: %w", err)
	}
	if err := c.copyPlatformChannels(ctx); err != nil {
		return err
	}
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
	e.mu.Lock()
	e.sub = sub
	e.mu.Unlock()
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
			if ev.barrier != nil {
				close(ev.barrier)
				continue
			}
			if sub.takeLagged() {
				c.reconcileAlerts(c.ctx, time.Now())
			}
			c.onAlertEvent(c.ctx, ev, time.Now())
		}
	}
}

// alertsHandled returns once the engine has handled every bus event
// published before the call: a barrier through its subscription, which it
// reads in order. Tests wait on it instead of sleeping. False if ctx ends
// first or the engine isn't running.
func (c *Core) alertsHandled(ctx context.Context) bool {
	e := c.alerts
	e.mu.Lock()
	sub := e.sub
	e.mu.Unlock()
	if sub == nil {
		return false
	}
	done := make(chan struct{})
	select {
	case sub.ch <- LiveEvent{&liveEvent{barrier: done}}:
	case <-ctx.Done():
		return false
	}
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// hold waits until no raise or resolve of alert key runs and claims it;
// release ends the claim. False if ctx ends first.
func (e *alertEngine) hold(ctx context.Context, key string) (release func(), ok bool) {
	for {
		e.mu.Lock()
		running, busy := e.busy[key]
		if !busy {
			done := make(chan struct{})
			e.busy[key] = done
			e.mu.Unlock()
			return func() {
				e.mu.Lock()
				delete(e.busy, key)
				e.mu.Unlock()
				close(done)
			}, true
		}
		e.mu.Unlock()
		select {
		case <-running:
		case <-ctx.Done():
			return nil, false
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
			c.alerts.mu.Lock()
			c.alerts.failed[svc] = max(c.alerts.failed[svc], r.Number)
			c.alerts.mu.Unlock()
			cause := ""
			if d := r.Diagnosis; d != nil && d.Rule != "" {
				cause = d.Summary
			}
			s := deployFailed(ev.Project, ev.Environment, ev.Service, r.Number, r.Error, cause, ev.serviceID, c.releaseServes(ctx, ev.serviceID))
			s.projectID = ev.projectID
			c.raiseAlert(ctx, s, now)
		case apitypes.ReleaseActive:
			// A new container serves: its restarts start from zero. The
			// deploy alert resolves unless a newer release failed (the
			// event came late, see alertEngine.failed).
			c.alerts.mu.Lock()
			stale := r.Number < c.alerts.failed[svc]
			if !stale {
				delete(c.alerts.failed, svc)
			}
			c.alerts.mu.Unlock()
			if !stale {
				c.resolveAlert(ctx, "deploy:"+svc, now)
			}
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
			for _, k := range []string{"disk:", "memory:", "cpu:", "update:", "firewall:"} {
				c.resolveAlert(ctx, k+id, now)
			}
			c.alerts.mu.Lock()
			delete(c.alerts.hot, "memory:"+id)
			delete(c.alerts.hot, "cpu:"+id)
			c.alerts.mu.Unlock()
		}
		if ev.Message == "removed" || s.Status == apitypes.ServerOnline || s.Status == apitypes.ServerRevoked {
			c.resolveAlert(ctx, "server:"+id, now)
			c.alerts.mu.Lock()
			delete(c.alerts.offline, s.ID)
			c.alerts.mu.Unlock()
		}
	}
}

// deployFailed is the alert for a service whose newest release failed;
// cause is its diagnosis's likely cause (ADR 0071), empty when none;
// serving says an earlier release is active, the one a failed deploy
// leaves serving (releaseServes).
func deployFailed(project, env, service string, number int64, reason, cause string, serviceID int64, serving bool) alertSignal {
	svc := serviceKey(project, env, service)
	msg := fmt.Sprintf("Release %d failed: %s. The release before it keeps serving.", number, reason)
	if !serving {
		msg = fmt.Sprintf("Release %d failed: %s. Nothing served before it, so the service has no running release.", number, reason)
	}
	cmd := fmt.Sprintf("dock release show %d %s", number, svcFlags(project, env, service))
	if cause != "" {
		msg += " Likely cause: " + cause + "."
		cmd = fmt.Sprintf("dock diagnose -r %d %s", number, svcFlags(project, env, service))
	}
	return alertSignal{
		Key: "deploy:" + alertServiceKey(serviceID, project, env, service), serviceID: serviceID, Kind: apitypes.AlertDeployFailed, Severity: apitypes.SeverityWarning,
		Title:   "Deploy of " + svc + " failed",
		Message: msg,
		Command: cmd,
		Target:  &apitypes.AlertTarget{Release: number},
		Project: project, Environment: env, Service: service,
	}
}

// releaseServes reports whether a service has an active release: what
// keeps serving when its deploy fails. One indexed read (releases_one_active)
// per failed deploy; false for events without a service (tests).
func (c *Core) releaseServes(ctx context.Context, serviceID int64) bool {
	if serviceID == 0 {
		return false
	}
	_, err := c.store.GetActiveRelease(ctx, serviceID)
	return err == nil
}

// backupFailed is the alert for a service's latest backup failing. The
// next successful backup resolves it.
func backupFailed(project, env, service string, id int64, reason string, serviceID int64) alertSignal {
	svc := serviceKey(project, env, service)
	return alertSignal{
		Key: "backup:" + alertServiceKey(serviceID, project, env, service), serviceID: serviceID, Kind: apitypes.AlertBackupFailed, Severity: apitypes.SeverityWarning,
		Title:   "Backup of " + svc + " failed",
		Message: fmt.Sprintf("Backup %d failed: %s.", id, strings.TrimRight(strings.TrimSpace(reason), ".:; ")),
		Command: fmt.Sprintf("dock backup log %d %s", id, svcFlags(project, env, service)),
		Target:  &apitypes.AlertTarget{Backup: id},
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

// noContainer reports whether a state means the service has no running
// container on purpose or at all: nothing to alert on.
func noContainer(s apitypes.ServiceState) bool {
	return s == apitypes.ServiceNone || s == apitypes.ServiceStopped || s == apitypes.ServiceAsleep
}

// onServiceStatus counts a service's restarts and raises a crash loop
// alert when [alerts] crash_loop_restarts fall within crash_loop_minutes.
// A service without a container (nothing deployed, archived, stopped
// on purpose, ADR 0050, or asleep) resolves it.
func (c *Core) onServiceStatus(ctx context.Context, ev LiveEvent, svc string, st apitypes.ServiceStatus, now time.Time) {
	if noContainer(st.State) {
		c.alerts.mu.Lock()
		delete(c.alerts.restarts, svc)
		c.alerts.mu.Unlock()
		c.resolveAlert(ctx, "crash:"+svc, now)
		// This event may be read late: only a service that is still
		// stopped loses its health check state and alert.
		if s := c.getStatus(ev.serviceID).State; ev.serviceID != 0 && noContainer(s) {
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
	sig := alertSignal{
		Key: "crash:" + svc, Kind: apitypes.AlertCrashLoop, Severity: apitypes.SeverityCritical,
		Title:     serviceKey(ev.Project, ev.Environment, ev.Service) + " is crash looping",
		projectID: ev.projectID, serviceID: ev.serviceID, Message: msg,
		Project: ev.Project, Environment: ev.Environment, Service: ev.Service,
	}
	// A new crash loop is diagnosed first (ADR 0071), so its alert says
	// why; until then later restarts wait for it.
	e.mu.Lock()
	_, open := e.open[sig.Key]
	waiting := e.diagnosing[sig.Key]
	diagnose := !open && !waiting && ev.serviceID != 0
	if diagnose {
		e.diagnosing[sig.Key] = true
	}
	e.mu.Unlock()
	switch {
	case diagnose:
		c.wg.Add(1)
		go c.diagnosedAlert(sig, apitypes.DiagnosisCrashLoop)
	case !waiting:
		c.raiseAlert(ctx, sig, now)
	}
}

// alertTick runs on the statistics tick: disk, memory and CPU alerts from
// the samples just taken (hosts, by server ID), servers offline for too
// long, crash loops that calmed down, analytics alerts that need a clock,
// and reminders.
func (c *Core) alertTick(ctx context.Context, now time.Time, servers []store.Server, hosts map[int64]apitypes.ServerUsage) {
	for _, srv := range servers {
		if u, ok := hosts[srv.ID]; ok && len(u.Disks) > 0 {
			c.diskAlert(ctx, srv, u, now)
		}
		if u, ok := hosts[srv.ID]; ok {
			c.memoryAlert(ctx, srv, u, now)
			c.cpuAlert(ctx, srv, u, now)
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

// pressureHysteresis is how far below its threshold a server's memory or
// CPU must drop before its alert resolves.
const pressureHysteresis = 5

// sustained tracks how long a reading has been above its threshold: it
// reports whether it has been for at least d, and forgets the reading
// once it is under (resolving its alert) below the hysteresis band.
func (c *Core) sustained(ctx context.Context, key string, over, under bool, d time.Duration, now time.Time) bool {
	e := c.alerts
	e.mu.Lock()
	since, seen := e.hot[key]
	switch {
	case over && !seen:
		e.hot[key], since = now, now
	case under:
		delete(e.hot, key)
	}
	e.mu.Unlock()
	if under {
		c.resolveAlert(ctx, key, now)
		return false
	}
	return over && now.Sub(since) >= d
}

// memoryAlert raises a server's memory alert once its memory has been
// fuller than [alerts] memory_warning_percent for memory_minutes, critical
// above memory_critical_percent, and resolves it once it is back under.
func (c *Core) memoryAlert(ctx context.Context, srv store.Server, u apitypes.ServerUsage, now time.Time) {
	if u.MemoryTotalBytes <= 0 {
		return
	}
	cfg := c.cfg.Alerts
	pct := 100 * float64(u.MemoryUsedBytes) / float64(u.MemoryTotalBytes)
	key := "memory:" + strconv.FormatInt(srv.ID, 10)
	over, under := pct >= float64(cfg.MemoryWarningPercent), pct < float64(cfg.MemoryWarningPercent-pressureHysteresis)
	if !c.sustained(ctx, key, over, under, time.Duration(cfg.MemoryMinutes)*time.Minute, now) {
		return
	}
	sev := apitypes.SeverityWarning
	if pct >= float64(cfg.MemoryCriticalPercent) {
		sev = apitypes.SeverityCritical
	}
	c.raiseAlert(ctx, alertSignal{
		Key: key, Kind: apitypes.AlertMemory, Severity: sev, Server: srv.Name,
		Title:   fmt.Sprintf("Memory %d%% used on %s", int(math.Floor(pct)), srv.Name),
		Message: fmt.Sprintf("%s of %s used for over %d minutes. Containers past their limits get killed and new deploys may not fit; lower services' memory or add a server.", gb(u.MemoryUsedBytes), gb(u.MemoryTotalBytes), cfg.MemoryMinutes),
		Command: "dock server show " + srv.Name,
	}, now)
}

// cpuAlert raises a server's CPU alert once its CPUs have been busier than
// [alerts] cpu_percent for cpu_minutes (0 turns it off), and resolves it
// once they are back under.
func (c *Core) cpuAlert(ctx context.Context, srv store.Server, u apitypes.ServerUsage, now time.Time) {
	cfg := c.cfg.Alerts
	if cfg.CPUPercent == 0 {
		return
	}
	key := "cpu:" + strconv.FormatInt(srv.ID, 10)
	over, under := u.CPUPercent >= float64(cfg.CPUPercent), u.CPUPercent < float64(cfg.CPUPercent-pressureHysteresis)
	if !c.sustained(ctx, key, over, under, time.Duration(cfg.CPUMinutes)*time.Minute, now) {
		return
	}
	c.raiseAlert(ctx, alertSignal{
		Key: key, Kind: apitypes.AlertCPU, Severity: apitypes.SeverityWarning, Server: srv.Name,
		Title:   fmt.Sprintf("CPU %d%% busy on %s", int(math.Floor(u.CPUPercent)), srv.Name),
		Message: fmt.Sprintf("Its %d CPUs have been over %d%% busy for %d minutes; apps there answer slower. See which services use it, then limit them or move some to another server.", u.CPUs, cfg.CPUPercent, cfg.CPUMinutes),
		Command: "dock server show " + srv.Name,
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
		s := deployFailed(r.Project, r.Environment, r.Service, r.Number, r.Error, r.Cause, r.ServiceID, r.Serving)
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
	// A maintenance window in progress holds alerts about what it covers:
	// recorded, sent when it ends (status_maintenance.go).
	muted := c.maintMutedBy(s)
	e := c.alerts
	// Claimed, then read under mu, written without it, then stored again
	// under mu: the claim keeps a second write of this alert out.
	release, ok := e.hold(ctx, s.Key)
	if !ok {
		return
	}
	defer release()
	e.writes.RLock()
	e.mu.Lock()
	if name := c.bus.projectName(s.projectID, s.Project); name != s.Project {
		s.Title = strings.Replace(s.Title, s.Project+"/", name+"/", 1)
		s.Command = strings.Replace(s.Command, "-p "+s.Project+" ", "-p "+name+" ", 1)
		s.Project = name
	}
	if cs, ok := e.causes[s.Key]; ok {
		s = withCause(s, cs)
	}
	cur, ok := e.open[s.Key]
	e.mu.Unlock()
	target := alertTargetJSON(s.Target)
	if ok {
		if cur.Severity == string(s.Severity) && cur.Title == s.Title && cur.Message == s.Message && cur.Project == s.Project &&
			cur.Command == s.Command && cur.Target == target {
			e.writes.RUnlock()
			return
		}
		escalated := severityRank(s.Severity) > severityRank(apitypes.AlertSeverity(cur.Severity))
		err := c.store.UpdateAlert(ctx, store.UpdateAlertParams{
			Severity: string(s.Severity), Title: s.Title, Message: s.Message, Project: s.Project, Command: s.Command, Target: target, UpdatedAt: t, ID: cur.ID,
		})
		if err != nil {
			e.writes.RUnlock()
			c.log.Error("update alert", "alert", s.Key, "err", err)
			return
		}
		// Onto the entry as it is now: a maintenance window may have
		// unmuted it meanwhile.
		e.mu.Lock()
		if latest, still := e.open[s.Key]; still {
			cur = latest
		}
		cur.Severity, cur.Title, cur.Message, cur.Project, cur.UpdatedAt = string(s.Severity), s.Title, s.Message, s.Project, t
		cur.Command, cur.Target = s.Command, target
		e.open[s.Key] = cur
		e.mu.Unlock()
		e.writes.RUnlock()
		c.publishAlert(cur)
		if escalated && cur.MutedBy == nil {
			c.notify(ctx, cur, "escalated", now)
		}
		return
	}
	var pid, sid, mid *int64
	if muted != 0 {
		mid = &muted
	}
	if s.projectID != 0 {
		pid = &s.projectID
	}
	if s.serviceID != 0 {
		sid = &s.serviceID
	}
	row, err := c.store.OpenAlert(ctx, store.OpenAlertParams{
		Key: s.Key, Kind: string(s.Kind), Severity: string(s.Severity), Title: s.Title, Message: s.Message,
		Project: s.Project, Environment: s.Environment, Service: s.Service, Server: s.Server, OpenedAt: t, UpdatedAt: t, ProjectID: pid, ServiceID: sid,
		Command: s.Command, Target: target, OrgID: c.alertOrg(ctx, s), MutedBy: mid,
	})
	if err != nil {
		e.writes.RUnlock()
		c.log.Error("open alert", "alert", s.Key, "err", err)
		return
	}
	e.mu.Lock()
	e.open[s.Key] = row
	e.mu.Unlock()
	e.writes.RUnlock()
	c.log.Info("alert opened", "alert", s.Key, "severity", s.Severity, "title", s.Title)
	c.publishAlert(row)
	c.statusAlertOpened(ctx, row)
	if mid != nil {
		c.log.Info("alert held by maintenance", "alert", s.Key, "window", muted)
		return
	}
	c.notify(ctx, row, "opened", now)
}

// alertOrg is the org an alert belongs to: the one its signal names
// (orgID), its project's, or its service's; nil (the platform's) for an
// alert that names none, about a server or Dock itself. A new kind of alert falls into place by what
// it names. One indexed read, when an alert opens.
func (c *Core) alertOrg(ctx context.Context, s alertSignal) *int64 {
	var org int64
	var err error
	switch {
	case s.orgID != 0:
		return &s.orgID
	case s.projectID != 0:
		org, err = c.store.AlertProjectOrg(ctx, s.projectID)
	case s.serviceID != 0:
		org, err = c.store.AlertServiceOrg(ctx, s.serviceID)
	default:
		return nil
	}
	if err != nil {
		// Projects are never deleted: only a failing DB gets here.
		c.log.Error("alerts: find the alert's org", "alert", s.Key, "err", err)
		return nil
	}
	return &org
}

// alertOrgID is an alert's org, 0 for the platform.
func alertOrgID(a store.Alert) int64 {
	if a.OrgID == nil {
		return 0
	}
	return *a.OrgID
}

// seesAlert reports whether a sees an org alert: a token limited to an
// environment sees that environment's alerts only. Alerts keep their
// environment by name, so it is matched by name.
func (a Actor) seesAlert(r store.Alert) bool {
	env := int64(0)
	if a.EnvironmentID != 0 {
		if r.Environment != a.EnvironmentName {
			return false
		}
		env = a.EnvironmentID
	}
	return a.canSee(alertOrgID(r), alertProjectID(r), env)
}

// alertProjectID is the project an alert is about, 0 for none.
func alertProjectID(a store.Alert) int64 {
	if a.ProjectID == nil {
		return 0
	}
	return *a.ProjectID
}

// resolveServiceAlerts closes every alert an archived service can hold:
// its deploy, backup, crash and health alerts and those of its domains
// (hostnames), and drops its health check state. finishArchive calls it,
// so the alerts close at once instead of at the next reconcileAlerts.
func (c *Core) resolveServiceAlerts(ctx context.Context, serviceID int64, hostnames []string) {
	now := time.Now()
	svc := alertServiceKey(serviceID, "", "", "")
	for _, kind := range []string{"deploy:", "backup:", "crash:", "health:", vulnAlertKey} {
		c.resolveAlert(ctx, kind+svc, now)
	}
	for _, h := range hostnames {
		c.resolveAlert(ctx, "domain:"+h, now)
	}
	c.health.forget(serviceID)
}

// resolveAlert closes the open alert for key, if there is one, and tells
// the channels that were told about it.
func (c *Core) resolveAlert(ctx context.Context, key string, now time.Time) {
	c.resolveAlertID(ctx, key, 0, now)
}

// resolveAlertID is resolveAlert for the open alert with row ID id only
// (0: whichever is open), for a caller that decided on an alert it read
// earlier and must not close one opened since.
func (c *Core) resolveAlertID(ctx context.Context, key string, id int64, now time.Time) {
	e := c.alerts
	release, ok := e.hold(ctx, key) // as raiseAlert: no write under mu
	if !ok {
		return
	}
	defer release()
	e.writes.RLock()
	e.mu.Lock()
	a, ok := e.open[key]
	if id != 0 && ok && a.ID != id {
		e.mu.Unlock()
		e.writes.RUnlock()
		return // a newer alert under the key: not this caller's
	}
	delete(e.causes, key) // the next occurrence is diagnosed again
	e.mu.Unlock()
	if !ok {
		e.writes.RUnlock()
		return
	}
	t := now.UTC()
	if err := c.store.ResolveAlert(ctx, store.ResolveAlertParams{At: &t, ID: a.ID}); err != nil {
		e.writes.RUnlock()
		c.log.Error("resolve alert", "alert", key, "err", err)
		return
	}
	e.mu.Lock()
	if latest, still := e.open[key]; still {
		a = latest
	}
	delete(e.open, key)
	e.mu.Unlock()
	e.writes.RUnlock()
	a.ResolvedAt, a.UpdatedAt = &t, t
	c.log.Info("alert resolved", "alert", key)
	c.publishAlert(a)
	c.statusAlertResolved(ctx, a)
	c.notify(ctx, a, "resolved", now)
}

func severityRank(s apitypes.AlertSeverity) int {
	if s == apitypes.SeverityCritical {
		return 2
	}
	return 1
}

// publishAlert sends an alert event to those who see the alert: its
// org's members, or for a platform alert operators.
func (c *Core) publishAlert(a store.Alert) {
	out := toAlert(a)
	var projectID int64
	if a.ProjectID != nil && a.ResolvedAt == nil {
		projectID = *a.ProjectID
	}
	org := alertOrgID(a)
	c.bus.publish(busEvent{orgID: org, projectID: projectID, serverStats: org == 0, ev: apitypes.Event{
		Type: apitypes.EventAlert, Project: a.Project, Environment: a.Environment, Service: a.Service, Alert: &out,
	}})
}

func toAlert(a store.Alert) apitypes.Alert {
	out := apitypes.Alert{
		ID: a.ID, Kind: apitypes.AlertKind(a.Kind), Severity: apitypes.AlertSeverity(a.Severity), Title: a.Title, Message: a.Message,
		Project: a.Project, Environment: a.Environment, Service: a.Service, Server: a.Server, Command: a.Command,
		OpenedAt: a.OpenedAt, UpdatedAt: a.UpdatedAt, ResolvedAt: a.ResolvedAt, Platform: a.OrgID == nil,
	}
	if a.Target != "" {
		var t apitypes.AlertTarget
		if json.Unmarshal([]byte(a.Target), &t) == nil {
			out.Target = &t
		}
	}
	return out
}

// alertTargetJSON is how an alert's target is stored: JSON, or empty
// without one.
func alertTargetJSON(t *apitypes.AlertTarget) string {
	if t == nil {
		return ""
	}
	b, _ := json.Marshal(t) // a struct of strings and numbers: never fails
	return string(b)
}

// svcFlags are the CLI flags naming a service, for an alert's command.
func svcFlags(project, env, service string) string {
	return "-p " + project + " -e " + env + " -s " + service
}

// maxAlertList caps one page of alerts.
const maxAlertList = 500

// alertsIn is the org whose alerts the actor reads or changes: with
// platform the platform's (0), for operators; otherwise the actor's org,
// with scope need.
func alertsIn(a Actor, platform bool, need apitypes.Scope, action string) (int64, error) {
	if platform {
		perm := PlatformAlerts
		if need == apitypes.ScopeRead {
			perm = PlatformView
		}
		return 0, a.requirePlatform(perm, action)
	}
	return a.OrgID, a.require(need, action)
}

// ListAlerts returns the open alerts of the actor's org (with platform,
// the platform's), worst and newest first, or with all every alert there
// newest first (limit at most maxAlertList, paged with beforeID). A token
// limited to a project sees that project's alerts.
func (c *Core) ListAlerts(ctx context.Context, a Actor, platform, all bool, limit int, beforeID int64) ([]apitypes.Alert, error) {
	org, err := alertsIn(a, platform, apitypes.ScopeRead, "reading alerts")
	if err != nil {
		return nil, err
	}
	sees := func(r store.Alert) bool { return alertOrgID(r) == org && (org == 0 || a.seesAlert(r)) }
	if !all {
		c.alerts.mu.Lock()
		out := make([]apitypes.Alert, 0, len(c.alerts.open))
		for _, r := range c.alerts.open {
			if sees(r) {
				out = append(out, toAlert(r))
			}
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
	rows, err := c.store.ListAlerts(ctx, store.ListAlertsParams{Org: org, BeforeID: beforeID, MaxRows: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.Alert, 0, len(rows))
	for _, r := range rows {
		if sees(r) {
			out = append(out, toAlert(r))
		}
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
// is still there, the engine raises a new one. An org's alert takes deploy
// in the org; a platform alert an operator.
func (c *Core) ResolveAlert(ctx context.Context, a Actor, id int64) (apitypes.Alert, error) {
	row, err := c.store.GetAlert(ctx, id)
	missing := notFound("alert %d not found; `dock alerts --all` lists alerts", id)
	if store.IsNotFound(err) {
		return apitypes.Alert{}, missing
	}
	if err != nil {
		return apitypes.Alert{}, err
	}
	org := alertOrgID(row)
	if org == 0 {
		err = a.requirePlatform(PlatformAlerts, "resolving platform alerts")
	} else if err = a.require(apitypes.ScopeDeploy, "resolving alerts"); err == nil && !a.seesAlert(row) {
		err = missing
	} else if err == nil && a.hasGrants && row.ProjectID != nil {
		// alerts.resolve where the alert is (ADR 0080), not anywhere.
		var env int64
		var kind uint8
		if row.ServiceID != nil {
			svc, serr := c.store.GetService(ctx, *row.ServiceID)
			if serr != nil {
				return apitypes.Alert{}, serr
			}
			if kind, err = c.envKindOf(ctx, a, svc.EnvironmentID); err != nil {
				return apitypes.Alert{}, err
			}
			env = svc.EnvironmentID
		}
		if !a.holdsAt(PermAlertsResolve, *row.ProjectID, env, kind) {
			err = a.refuseGrant(apitypes.ScopeDeploy, "resolving alerts", row.Project)
		}
	}
	if err != nil {
		return apitypes.Alert{}, err
	}
	if row.ResolvedAt != nil {
		return toAlert(row), nil
	}
	c.resolveAlert(ctx, row.Key, time.Now())
	by := a
	if org == 0 {
		by = a.platform()
	}
	c.audit(ctx, by, "alert.resolve", nil, nil, "alert "+strconv.FormatInt(id, 10), "resolved alert: "+row.Title)
	if row, err = c.store.GetAlert(ctx, id); err != nil {
		return apitypes.Alert{}, err
	}
	return toAlert(row), nil
}
