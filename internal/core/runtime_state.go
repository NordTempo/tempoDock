package core

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Runtime status of services is kept in memory: Docker is the source of
// truth, read once on start (reconcile) and then followed via its event
// stream. Nothing polls.

// storedStatus is a service's status in memory, with the statusSets
// count of the setStatus call that last set it outright.
type storedStatus struct {
	apitypes.ServiceStatus
	set uint64
}

func (c *Core) getStatus(serviceID int64) apitypes.ServiceStatus {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	if s, ok := c.status[serviceID]; ok {
		return s.ServiceStatus
	}
	return apitypes.ServiceStatus{State: apitypes.ServiceNone}
}

// serverLink is a server's link status and, for operators, its name,
// which reads put on the statuses of services there (mark).
type serverLink struct {
	name   string
	status apitypes.ServerStatus
}

// linkOf returns a server's link as a sees it: with the name only for
// operators (seesServers). A connected server costs nothing (the hub's
// state is in memory); one that isn't costs one query, for its name and
// whether it is revoked.
func (c *Core) linkOf(ctx context.Context, a Actor, serverID int64) serverLink {
	if c.online(serverID) {
		return serverLink{status: apitypes.ServerOnline}
	}
	s, err := c.store.GetServer(ctx, serverID)
	if err != nil {
		return serverLink{status: apitypes.ServerOffline}
	}
	return serverLink{name: a.serverName(s.Name), status: c.serverStatus(s)}
}

// mark flags a service status as last known when its server can't be
// reached: what the service does now is unknown until it reconnects.
func (l serverLink) mark(s apitypes.ServiceStatus) apitypes.ServiceStatus {
	if l.status != apitypes.ServerOnline {
		s.ServerOffline, s.Server, s.ServerStatus = true, l.name, l.status
	}
	return s
}

// setStatus stores a service's status and publishes it if it changed.
// Callers that change what the service's status follows (an archive, a
// stop, a sleep) write the store first: a container event read before
// that write is then read again (changeStatusSince).
func (c *Core) setStatus(ss svcScope, s apitypes.ServiceStatus) {
	c.statusMu.Lock()
	prev, had := c.status[ss.svc.ID]
	changed := !had || !sameStatus(prev.ServiceStatus, s)
	t := now()
	s.UpdatedAt = &t
	c.statusSets++
	c.status[ss.svc.ID] = storedStatus{s, c.statusSets}
	c.statusMu.Unlock()
	if changed {
		c.publish(ss, apitypes.Event{Type: apitypes.EventServiceStatus, ServiceStatus: &s})
	}
}

// sameStatus reports whether two statuses say the same: when they were
// set aside, and start times compared by value.
func sameStatus(a, b apitypes.ServiceStatus) bool {
	if (a.StartedAt == nil) != (b.StartedAt == nil) || a.StartedAt != nil && !a.StartedAt.Equal(*b.StartedAt) {
		return false
	}
	a.UpdatedAt, b.UpdatedAt, a.StartedAt, b.StartedAt = nil, nil, nil, nil
	return a == b
}

// changeStatus changes a service's stored status with fn, under the lock,
// and publishes it if it changed: for changes made beside the event
// stream (health restarts), which a read and a later setStatus could
// undo.
func (c *Core) changeStatus(ss svcScope, fn func(*apitypes.ServiceStatus)) {
	c.changeStatusSince(ss, math.MaxUint64, fn)
}

// statusClock is the statusSets count now, read before a container
// event's store reads for changeStatusSince.
func (c *Core) statusClock() uint64 {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	return c.statusSets
}

// changeStatusSince is changeStatus for a change worked out from reads
// made after statusClock said since. It changes nothing and returns false
// if setStatus set the service's status after that: the reads may predate
// what it followed (an archive's superseded release), so the caller reads
// again.
func (c *Core) changeStatusSince(ss svcScope, since uint64, fn func(*apitypes.ServiceStatus)) bool {
	c.statusMu.Lock()
	cur, ok := c.status[ss.svc.ID]
	if ok && cur.set > since {
		c.statusMu.Unlock()
		return false
	}
	s := cur.ServiceStatus
	if !ok {
		s = apitypes.ServiceStatus{State: apitypes.ServiceNone}
	}
	fn(&s)
	if ok && sameStatus(s, cur.ServiceStatus) {
		c.statusMu.Unlock()
		return true
	}
	t := now()
	s.UpdatedAt = &t
	c.status[ss.svc.ID] = storedStatus{s, cur.set}
	c.statusMu.Unlock()
	c.publish(ss, apitypes.Event{Type: apitypes.EventServiceStatus, ServiceStatus: &s})
	return true
}

// statusOf is a service's status as its container reads.
func statusOf(ct node.Container) apitypes.ServiceStatus {
	s := apitypes.ServiceStatus{RestartCount: ct.RestartCount, OOMKilled: ct.OOMKilled, ExitCode: ct.ExitCode, Health: apitypes.HealthState(ct.Health)}
	switch ct.State {
	case node.StateRunning, node.StatePaused:
		// Dock pauses a service only while a snapshot copies its volumes
		// (ADR 0047): seconds, with its connections kept.
		s.State, s.StartedAt = apitypes.ServiceRunning, startedAt(ct)
	case node.StateRestarting:
		s.State = apitypes.ServiceRestarting
	case node.StateExited, node.StateDead:
		s.State = apitypes.ServiceCrashed
	default:
		// Created or paused: it serves nothing, and Dock didn't stop it
		// (ServiceStopped is a service stopped on purpose, ADR 0050).
		s.State = apitypes.ServiceNone
	}
	return s
}

// startedAt is a container's start time for ServiceStatus.StartedAt, or
// nil when the runtime didn't say (a failed Inspect's zero Container).
func startedAt(ct node.Container) *time.Time {
	if ct.StartedAt.IsZero() {
		return nil
	}
	t := ct.StartedAt
	return &t
}

// memoryLimitMB is a release's memory limit (0 if its config can't be read).
func (c *Core) memoryLimitMB(rel store.Release) int {
	cfg, err := c.openConfig(rel)
	if err != nil {
		return 0
	}
	return cfg.MemoryMB
}

// scopeOf loads a service with its environment and project.
func (c *Core) scopeOf(ctx context.Context, serviceID int64) (svcScope, error) {
	s, err := c.store.GetService(ctx, serviceID)
	if err != nil {
		return svcScope{}, err
	}
	e, err := c.store.GetEnvironment(ctx, s.EnvironmentID)
	if err != nil {
		return svcScope{}, err
	}
	p, err := c.store.GetProject(ctx, e.ProjectID)
	if err != nil {
		return svcScope{}, err
	}
	return svcScope{envScope{p, e}, s}, nil
}

// watchRuntime follows the local server's container lifecycle events and
// keeps the status of each service's active container up to date.
func (c *Core) watchRuntime(events <-chan node.ContainerEvent) {
	defer c.wg.Done()
	for ev := range events {
		c.containerEvent(c.ctx, c.localID, ev)
	}
}

// containerEvent is onContainerEvent for the loops that read events
// (watchRuntime, watchServer): a panic on one event ends that event's
// handling, and the loop goes on with the next.
func (c *Core) containerEvent(ctx context.Context, serverID int64, ev node.ContainerEvent) {
	defer recovered(c.log, "handling a container event")
	c.onContainerEvent(ctx, serverID, ev)
}

// foreign reports whether a resource is labelled for another control plane
// sharing the Docker host. Those are never reconciled, reported or removed.
func (c *Core) foreign(l node.Labels) bool {
	return l.ControlPlane != "" && l.ControlPlane != c.install
}

// onContainerEvent applies a container event from serverID. Labels are
// only a lookup: an event counts for a service only when its container is
// the one the store has for the labelled release, of a service on
// serverID, so an event carrying another service's labels (from a
// container someone else labelled, or a misbehaving node) changes nothing.
func (c *Core) onContainerEvent(ctx context.Context, serverID int64, ev node.ContainerEvent) {
	if c.foreign(ev.Labels) {
		return
	}
	if ev.Role == node.RoleSidecar {
		// A database's WAL archiver, not an app (ADR 0093).
		c.onArchiverEvent(ctx, serverID, ev)
		return
	}
	if ev.Action == node.ActionWake {
		// No container's: a node's wake check (ADR 0070).
		c.wakeFromNode(ctx, serverID, ev)
		return
	}
	c.drainContainerEvent(serverID, ev)
	own := c.ownRemoval(ev)
	since := c.statusClock()
	rel, ss, ok := c.eventTarget(ctx, serverID, ev)
	if !ok {
		return
	}
	if ev.Action == "die" || ev.Action == "destroy" {
		// The service's database container went away (redeploy, archive,
		// crash): drop the panel's connections to it. Its release is
		// superseded already when the daemon replaces or archives it.
		c.panel.Forget(ss.svc.ID)
	}
	if own || !followsRelease(rel, ss) {
		return
	}
	// The status is changed in place under its lock: a read here and a
	// write after the work below would undo what the deploy's cutover or
	// the health watch set meanwhile. A status set outright after the
	// reads above (an archive right after a deploy, with this its
	// container's late start event) means reading again: the release may
	// no longer be the service's.
	change := func(fn func(*apitypes.ServiceStatus)) {
		for !c.changeStatusSince(ss, since, fn) {
			since = c.statusClock()
			if rel, ss, ok = c.eventTarget(ctx, serverID, ev); !ok || !followsRelease(rel, ss) {
				return
			}
		}
	}
	now := time.Now()
	if (ev.Action == "die" || ev.Action == "start" || ev.Action == "restart") && c.health.ownEvent(ev, now) {
		// A health restart (health.go): not a crash, and not counted in
		// RestartCount; HealthRestarts counts it.
		if ev.Action == "restart" {
			return // its last event: the restart is over
		}
		change(func(s *apitypes.ServiceStatus) {
			if ev.Action == "die" {
				s.State, s.StartedAt = apitypes.ServiceRestarting, nil
				return
			}
			started(s, ev)
			if s.Health != "" {
				s.Health = apitypes.HealthStarting
			}
		})
		return
	}
	var restarts, limitMB int
	switch ev.Action {
	case "start", "restart", "die", "destroy":
	case "health_status":
		restarts = c.onHealth(ss, ev.ContainerID, ev.Health, now)
	case "oom":
		limitMB = c.memoryLimitMB(rel)
	default:
		return
	}
	change(func(s *apitypes.ServiceStatus) {
		switch ev.Action {
		case "start", "restart":
			if s.State == apitypes.ServiceCrashed || s.State == apitypes.ServiceRestarting || ev.Action == "restart" {
				s.RestartCount++
			}
			started(s, ev)
			if ev.Action == "start" && s.Health != "" {
				s.Health = apitypes.HealthStarting // Docker starts its check over
			}
		case "health_status":
			s.Health, s.HealthRestarts = apitypes.HealthState(ev.Health), restarts
		case "die":
			s.State, s.ExitCode, s.StartedAt = apitypes.ServiceCrashed, ev.ExitCode, nil
		case "oom":
			s.OOMKilled, s.MemoryLimitMB = true, limitMB
		case "destroy":
			// Not ServiceStopped: that is a service stopped on purpose
			// (ADR 0050).
			s.State, s.StartedAt = apitypes.ServiceNone, nil
		}
	})
	if ev.Action == "destroy" {
		c.drift(ctx, &ss, fmt.Sprintf("container of active release %d of %s was removed outside the platform", rel.Number, ss))
	}
}

// eventTarget reads the release and service a container event from
// serverID is about. ok is false unless the event's container is the one
// the store has for the labelled release, of a service on serverID.
func (c *Core) eventTarget(ctx context.Context, serverID int64, ev node.ContainerEvent) (store.Release, svcScope, bool) {
	id, err := strconv.ParseInt(ev.Labels.Release, 10, 64)
	if err != nil {
		return store.Release{}, svcScope{}, false
	}
	rel, err := c.store.GetRelease(ctx, id)
	if err != nil || rel.ContainerID == "" || rel.ContainerID != ev.ContainerID {
		return store.Release{}, svcScope{}, false
	}
	ss, err := c.scopeOf(ctx, rel.ServiceID)
	if err != nil || ss.env.ServerID != serverID {
		return store.Release{}, svcScope{}, false
	}
	return rel, ss, true
}

// followsRelease reports whether a release's container events set its
// service's status: it is the active release, the service isn't stopped
// on purpose (ADR 0050) and its environment doesn't sleep or wake
// (sleep.go), which set the status themselves.
func followsRelease(rel store.Release, ss svcScope) bool {
	return rel.Status == string(apitypes.ReleaseActive) && ss.svc.StoppedAt == nil && ss.env.AsleepAt == nil
}

// started marks a status running after a start or restart event. The
// event's time is its start time unless the status has one already: the
// exact one, from Inspect (a deploy's cutover), for the same start, since
// a container doesn't start again without a die event, which clears it.
func started(s *apitypes.ServiceStatus, ev node.ContainerEvent) {
	if s.State != apitypes.ServiceRunning || s.StartedAt == nil {
		t := ev.Time
		if t.IsZero() {
			t = time.Now()
		}
		s.StartedAt = &t
	}
	s.State = apitypes.ServiceRunning
}

// recover fails deploys that were in flight when the daemon stopped and
// removes their containers, along with any other container of a failed or
// superseded release (a drain cut short by a restart, a stop that failed).
// If a stateful deploy had already stopped the previous release, that
// release is started again, after the new container is gone. Each
// interrupted service is recorded to start again (ensure_running) in the
// same transaction that fails its release, so a crash in between loses
// nothing; on remote servers, which aren't connected yet, it starts when
// each reconnects (startInterrupted).
func (c *Core) recover(ctx context.Context) error {
	inflight, err := c.store.ListInFlightReleases(ctx)
	if err != nil {
		return err
	}
	for _, r := range inflight {
		ss, err := c.scopeOf(ctx, r.ServiceID)
		if err != nil {
			return err
		}
		msg := "daemon restarted during deploy"
		err = c.store.Tx(ctx, func(q *store.Queries) error {
			if err := q.FailRelease(ctx, store.FailReleaseParams{Error: msg, ErrorCode: string(apitypes.CodeDeployFailed), FinishedAt: ptr(now()), ID: r.ID}); err != nil {
				return err
			}
			return q.AddEnsureRunning(ctx, r.ServiceID)
		})
		if err != nil {
			return err
		}
		c.releaseEvent(ctx, ss, r, fmt.Sprintf("Release %d failed: %s", r.Number, msg))
		c.restage(ctx, ss, r, "failed ("+msg+")")
		c.gitDeployInterrupted(ctx, ss, r, msg)
	}
	return c.startInterrupted(ctx, c.localID, c.rt)
}

// ensureLater records that a service's active release must be started
// again once its server is reachable and the interrupted release's
// container is gone (startInterrupted, on the next reconnect or start).
func (c *Core) ensureLater(ctx context.Context, serviceID int64) {
	if err := c.store.AddEnsureRunning(ctx, serviceID); err != nil {
		c.log.Error("record release to start again", "service", serviceID, "err", err)
	}
}

// startInterrupted finishes interrupted deploys on a server: recover runs
// it for the local server, reconnected for a remote one. It removes the
// server's leftover containers (sweepRetired), then starts the active
// release of each service recorded in ensure_running again and clears its
// row. A service whose leftover container couldn't be removed keeps its
// row (two containers must never share a volume), as do one with a deploy
// in flight and one whose server became unreachable meanwhile: the next
// start or reconnect tries again.
func (c *Core) startInterrupted(ctx context.Context, serverID int64, rt node.Runtime) error {
	blocked, err := c.sweepRetired(ctx, serverID, rt)
	if err != nil {
		return err
	}
	ids, err := c.store.ListEnsureRunning(ctx, serverID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if blocked[id] {
			continue
		}
		if _, err := c.store.GetInFlightRelease(ctx, id); err == nil {
			// A deploy started since the server came back owns the
			// service: it stops the old release itself, and restarts it
			// if it fails. The row stays for the next pass.
			continue
		}
		ss, err := c.scopeOf(ctx, id)
		if err != nil {
			return err
		}
		if c.restoreWriting(ss, "start it") != nil {
			// A restore writes its volumes and starts it again itself
			// once done; the row stays for the next pass.
			continue
		}
		active, err := c.store.GetActiveRelease(ctx, id)
		switch {
		case err == nil:
			if isOffline(c.ensureRunning(ctx, ss, active)) {
				continue
			}
		case !store.IsNotFound(err):
			return err
		}
		// Started (or it couldn't be, which is drift), or there is nothing
		// to start: the service was archived or never went live.
		if err := c.store.DeleteEnsureRunning(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// sweepRetired removes a server's containers of failed and superseded
// releases, and of stopped services' active releases, then frees the host
// ports of retired releases whose containers are gone. A container counts
// as ours only if all its labels match the release's project, environment
// and service, and it carries this control plane's label or none (node.Labels.Matches). This is also how an archive
// on an unreachable server finishes: its release is superseded, so its
// container goes when the server reconnects. It returns the IDs of the
// services whose leftover containers it couldn't remove (reported as
// drift): their active release must not start next to them.
func (c *Core) sweepRetired(ctx context.Context, serverID int64, rt node.Runtime) (blocked map[int64]bool, err error) {
	ctrs, err := rt.List(ctx)
	if err != nil {
		return nil, err
	}
	byRelease := map[int64][]node.Container{}
	for _, ct := range ctrs {
		if c.foreign(ct.Labels) {
			continue
		}
		if id, err := strconv.ParseInt(ct.Labels.Release, 10, 64); err == nil {
			byRelease[id] = append(byRelease[id], ct)
		}
	}
	blocked = map[int64]bool{}
	remaining := map[int64]bool{}
	for id, cs := range byRelease {
		rel, err := c.store.GetRelease(ctx, id)
		if store.IsNotFound(err) {
			continue // reconcile reports it
		}
		if err != nil {
			return nil, err
		}
		why := rel.Status
		switch rel.Status {
		case string(apitypes.ReleaseFailed), string(apitypes.ReleaseSuperseded):
		case string(apitypes.ReleaseActive):
			// A stopped service's (ADR 0050), when its stop couldn't remove
			// the container, e.g. on a server that went offline.
			svc, err := c.store.GetService(ctx, rel.ServiceID)
			if err != nil {
				return nil, err
			}
			if svc.StoppedAt == nil {
				continue
			}
			why = "service stopped"
		default:
			continue
		}
		remaining[id] = true
		ss, err := c.scopeOf(ctx, rel.ServiceID)
		if err != nil {
			return nil, err
		}
		want := c.labels(ss, rel.ID)
		if ss.env.ServerID != serverID || slices.ContainsFunc(cs, func(ct node.Container) bool { return !ct.Labels.Matches(want) }) {
			continue
		}
		c.releaseEvent(ctx, ss, rel, fmt.Sprintf("Removing leftover container of release %d (%s)", rel.Number, why))
		opts := keepData // it went live, so it may hold the service's data
		if rel.Status == string(apitypes.ReleaseFailed) {
			opts = node.RemoveOptions{}
		}
		if err := c.removeContainers(ctx, ss.env.ServerID, cs, opts); err != nil {
			c.drift(ctx, &ss, fmt.Sprintf("could not remove leftover container of release %d of %s: %v", rel.Number, ss, err))
			blocked[ss.svc.ID] = true
			continue
		}
		remaining[id] = false
		if rel.Status == string(apitypes.ReleaseActive) {
			c.forgetContainer(ctx, rel.ID)
		}
	}
	holders, err := c.store.ListRetiredPortHolders(ctx, serverID)
	if err != nil {
		return nil, err
	}
	for _, id := range holders {
		if !remaining[id] {
			c.freePort(ctx, id)
		}
	}
	return blocked, nil
}

// ensureRunning makes sure an active release has a running container:
// one started just before a crash is adopted, a stopped or missing one is
// replaced (after an interrupted stateful recreate). A failure to start it
// is reported as drift; isOffline(err) says the server couldn't be
// reached. A service stopped on purpose (ADR 0050) is left alone, and so
// is one whose environment is asleep (its wake starts it).
func (c *Core) ensureRunning(ctx context.Context, ss svcScope, active store.Release) error {
	if ss.svc.StoppedAt != nil || ss.env.AsleepAt != nil {
		return nil
	}
	rt := c.runtime(ss.env.ServerID)
	ctrs, err := rt.List(ctx)
	if err != nil {
		c.log.Warn("list containers", "err", err)
		return err
	}
	var stopped []node.Container
	for _, ct := range c.ofRelease(ctrs, ss, active.ID) {
		if ct.State != node.StateRunning && ct.State != node.StateRestarting {
			stopped = append(stopped, ct)
			continue
		}
		if ct.ID != active.ContainerID {
			if err := c.store.SetReleaseContainer(ctx, store.SetReleaseContainerParams{ContainerID: ct.ID, ID: active.ID}); err != nil {
				c.log.Error("adopt container", "release", active.ID, "err", err)
			}
		}
		return nil
	}
	err = c.removeContainers(ctx, ss.env.ServerID, stopped, keepData)
	if err == nil {
		c.releaseEvent(ctx, ss, active, fmt.Sprintf("Starting release %d again", active.Number))
		err = c.restartRelease(ctx, ss, active)
	}
	if err != nil {
		c.drift(ctx, &ss, fmt.Sprintf("could not start release %d of %s again: %v", active.Number, ss, err))
	}
	return err
}

// reconcileServer compares the active releases on a server with its
// containers and sets service statuses. With report it also reports
// drift (missing containers, containers of no active release). It never
// touches containers.
func (c *Core) reconcileServer(ctx context.Context, serverID int64, rt node.Runtime, report bool) error {
	ctrs, err := rt.List(ctx)
	if err != nil {
		return err
	}
	byID := map[string]node.Container{}
	for _, ct := range ctrs {
		byID[ct.ID] = ct
	}
	actives, err := c.store.ListActiveReleases(ctx)
	if err != nil {
		return err
	}
	activeIDs := map[string]bool{}
	for _, r := range actives {
		activeIDs[strconv.FormatInt(r.ID, 10)] = true
		ss, err := c.scopeOf(ctx, r.ServiceID)
		if err != nil {
			return err
		}
		if ss.env.ServerID != serverID {
			continue
		}
		if ss.svc.StoppedAt != nil {
			// Stopped on purpose (ADR 0050): no container is expected.
			c.setStatus(ss, apitypes.ServiceStatus{State: apitypes.ServiceStopped})
			continue
		}
		if ss.env.AsleepAt != nil {
			// Asleep (sleep.go): its container is stopped until it wakes.
			c.setStatus(ss, apitypes.ServiceStatus{State: apitypes.ServiceAsleep})
			continue
		}
		ct, ok := byID[r.ContainerID]
		if !ok {
			c.setStatus(ss, apitypes.ServiceStatus{State: apitypes.ServiceNone})
			if report {
				c.drift(ctx, &ss, fmt.Sprintf("container of active release %d of %s is missing; redeploy or roll back to start it", r.Number, ss))
			}
			continue
		}
		s := statusOf(ct)
		if s.OOMKilled {
			s.MemoryLimitMB = c.memoryLimitMB(r)
		}
		s.HealthRestarts = c.health.restarts(ss.svc.ID, ct.ID)
		if s.Health == apitypes.HealthUnhealthy && ct.State == node.StateRunning {
			// It turned unhealthy while the daemon (or the server's link)
			// was down: handle it as if the event had just come.
			c.onHealth(ss, ct.ID, ct.Health, time.Now())
		}
		c.setStatus(ss, s)
	}
	if !report {
		return nil
	}
	inflight, err := c.store.ListInFlightReleases(ctx)
	if err != nil {
		return err
	}
	for _, r := range inflight {
		activeIDs[strconv.FormatInt(r.ID, 10)] = true // a deploy's new container
	}
	// One report for all of them: a server's container list is its own
	// word, and one audit row per container would let it flood the log.
	var stray []string
	n := 0
	for _, ct := range ctrs {
		if !activeIDs[ct.Labels.Release] && !c.foreign(ct.Labels) {
			if n++; len(stray) < maxStrayNamed {
				stray = append(stray, fmt.Sprintf("%.64s (%.12s)", ct.Name, ct.ID))
			}
		}
	}
	switch {
	case n == 1:
		c.drift(ctx, nil, fmt.Sprintf("container %s is not part of any active release; leaving it alone", stray[0]))
	case n > 1:
		more := ""
		if n > len(stray) {
			more = fmt.Sprintf(" and %d more", n-len(stray))
		}
		c.drift(ctx, nil, fmt.Sprintf("%d containers are not part of any active release; leaving them alone: %s%s", n, strings.Join(stray, ", "), more))
	}
	return nil
}

// maxStrayNamed is how many stray containers a drift report names.
const maxStrayNamed = 10

// drift reports a difference between the DB and the runtime: logged,
// audited and published.
func (c *Core) drift(ctx context.Context, ss *svcScope, msg string) {
	c.log.Warn("drift: " + msg)
	ev := apitypes.Event{Type: apitypes.EventDrift, Message: msg}
	if ss == nil {
		c.audit(ctx, System, "reconcile.drift", nil, nil, "runtime", msg)
		c.bus.publish(busEvent{ev: ev})
		return
	}
	c.audit(ctx, System, "reconcile.drift", &ss.project, &ss.env, ss.String(), msg)
	c.publish(*ss, ev)
}
