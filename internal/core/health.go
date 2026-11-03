package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Ongoing health checks (ADR 0043). Docker runs each active container's
// health probe (dock-probe, mounted into the container) and reports
// health_status events on the stream core already follows, so nothing
// here polls. When a running service turns unhealthy, core raises an
// unhealthy alert and restarts the container in place (same container,
// port and volumes, so a stateful service never has two containers on
// its volume), with backoff between restarts. The statistics tick makes
// restarts that were held back by the backoff once they are due. The
// alert resolves when the check passes again: that resolution is the
// recovered notice.

// healthBackoff is the least time between one health restart and the
// next, by the number of restarts already made in a row: the first
// restart is at once, later ones wait longer, up to 30 minutes, so a
// broken app is restarted every half hour at most instead of flapping.
// Each wait comes on top of the time the check takes to fail again (the
// start period and retries times the interval: 2.5 minutes by default).
var healthBackoff = [...]time.Duration{0, time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}

// healthCalm is how long a service must stay healthy before its restarts
// in a row start from zero again.
const healthCalm = 30 * time.Minute

// ownRestartWindow bounds how long a health restart's events are taken
// for its own (see healthWatch.own), should its restart event be lost.
const ownRestartWindow = stopTimeout + time.Minute

// healthWatch is the state of every service's ongoing health check.
type healthWatch struct {
	mu sync.Mutex
	// svc is by service ID: bounded by services, and an entry goes when
	// its service stops or a new release goes live (forget).
	svc map[int64]*serviceHealth
	// own holds the containers being restarted for their health check,
	// until when: their die and start events are the restart, not a
	// crash. An entry goes on the container's restart event.
	own map[string]time.Time
}

// serviceHealth is one service's ongoing health check.
type serviceHealth struct {
	// container is the active container this is about; another one
	// starts over.
	container string
	unhealthy bool
	// since is when the check started failing; healthyAt when it last
	// passed (zero: not since this was created).
	since, healthyAt time.Time
	// streak is the health restarts made in a row, which pick the next
	// backoff; last is when the latest one was made.
	streak int
	last   time.Time
	// restarts counts every health restart of the container, for
	// ServiceStatus.HealthRestarts.
	restarts int
	// busy is set while a goroutine handles the unhealthy container.
	busy bool
}

func newHealthWatch() healthWatch {
	return healthWatch{svc: map[int64]*serviceHealth{}, own: map[string]time.Time{}}
}

// state returns a service's entry for container, starting over when the
// entry is about another container. Callers hold mu.
func (w *healthWatch) state(serviceID int64, container string) *serviceHealth {
	h := w.svc[serviceID]
	if h == nil || h.container != container {
		h = &serviceHealth{container: container}
		w.svc[serviceID] = h
	}
	return h
}

// restarts is the number of health restarts of a service's container,
// for statuses built from scratch (reconcile).
func (w *healthWatch) restarts(serviceID int64, container string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if h := w.svc[serviceID]; h != nil && h.container == container {
		return h.restarts
	}
	return 0
}

// forget drops a service's entry: it stopped, or a new release went live.
func (w *healthWatch) forget(serviceID int64) {
	w.mu.Lock()
	delete(w.svc, serviceID)
	w.mu.Unlock()
}

// ownEvent reports whether ev belongs to a health restart core is making,
// forgetting the restart on its restart event (Docker's last of a
// restart: die, start, restart).
func (w *healthWatch) ownEvent(ev node.ContainerEvent, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	until, ok := w.own[ev.ContainerID]
	if !ok {
		return false
	}
	if ev.Action == "restart" || now.After(until) {
		delete(w.own, ev.ContainerID)
		return now.Before(until)
	}
	return true
}

// onHealth handles a health state change of a service's active
// container (a health_status event, or what an inspect found): unhealthy
// raises the alert and restarts it when the backoff allows, healthy
// resolves the alert. It returns the container's health restarts so far,
// for its status.
func (c *Core) onHealth(ss svcScope, containerID string, health string, now time.Time) int {
	w := &c.health
	w.mu.Lock()
	h := w.state(ss.svc.ID, containerID)
	restarts := h.restarts
	switch health {
	case node.HealthHealthy:
		h.unhealthy, h.healthyAt, h.since = false, now, time.Time{}
		w.mu.Unlock()
		c.resolveAlert(c.ctx, healthAlertKey(ss.svc.ID), now)
	case node.HealthUnhealthy:
		if !h.unhealthy {
			h.unhealthy = true
			if h.since.IsZero() {
				h.since = now
			}
			if !h.healthyAt.IsZero() && now.Sub(h.healthyAt) >= healthCalm {
				h.streak = 0
			}
		}
		start := !h.busy
		h.busy = true
		w.mu.Unlock()
		if start {
			c.wg.Add(1)
			go c.handleUnhealthy(ss, containerID, now)
		}
	default:
		w.mu.Unlock()
	}
	return restarts
}

// healthTick runs on the statistics tick: it handles unhealthy services
// whose next restart the backoff held back and is now due. Nothing is
// read when no service is unhealthy.
func (c *Core) healthTick(ctx context.Context, now time.Time) {
	type due struct {
		service   int64
		container string
	}
	var todo []due
	w := &c.health
	w.mu.Lock()
	for id, h := range w.svc {
		if h.unhealthy && !h.busy && !now.Before(h.last.Add(backoffAfter(h.streak))) {
			h.busy = true
			todo = append(todo, due{id, h.container})
		}
	}
	w.mu.Unlock()
	for _, d := range todo {
		ss, err := c.scopeOf(ctx, d.service)
		if err != nil {
			c.healthDone(d.service, d.container, false, now)
			continue
		}
		c.wg.Add(1)
		go c.handleUnhealthy(ss, d.container, now)
	}
}

// backoffAfter is the wait before the next health restart after streak
// restarts in a row.
func backoffAfter(streak int) time.Duration {
	return healthBackoff[min(streak, len(healthBackoff)-1)]
}

// handleUnhealthy runs in its own goroutine for an unhealthy container
// (now is when the event or tick came): it checks the container still is
// the running, unhealthy, active one, raises or updates the alert, and
// restarts it if the backoff allows.
// A service with a deploy in flight is left to the deploy: a stateful
// deploy may be stopping this container to replace it.
func (c *Core) handleUnhealthy(ss svcScope, containerID string, now time.Time) {
	defer c.wg.Done()
	ctx := c.ctx
	restarted := false
	defer func() { c.healthDone(ss.svc.ID, containerID, restarted, now) }()

	rel, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
	if err != nil || rel.ContainerID != containerID {
		return
	}
	rt := c.runtime(ss.env.ServerID)
	ct, err := rt.Inspect(ctx, containerID)
	if err != nil {
		if !isOffline(err) && ctx.Err() == nil {
			c.log.Warn("health: inspect container", "service", ss.String(), "err", err)
		}
		return
	}
	switch {
	case ct.State != node.StateRunning:
		// Stopped, or crashed (Docker restarts it and its check starts
		// over): the tick has nothing to do until it fails again.
		c.health.mu.Lock()
		c.health.state(ss.svc.ID, containerID).unhealthy = false
		c.health.mu.Unlock()
		return
	case ct.Health == node.HealthHealthy:
		// Recovered; its healthy event may have been lost to a stream
		// reconnect, so apply it here.
		restarts := c.onHealth(ss, containerID, ct.Health, now)
		c.changeStatus(ss, func(s *apitypes.ServiceStatus) {
			s.Health, s.HealthRestarts = apitypes.HealthHealthy, restarts
		})
		return
	case ct.Health != node.HealthUnhealthy:
		return // starting again: the tick looks once it is due
	}
	c.health.mu.Lock()
	h := c.health.state(ss.svc.ID, containerID)
	streak, dueAt, since := h.streak, h.last.Add(backoffAfter(h.streak)), h.since
	c.health.mu.Unlock()

	inflight := false
	if _, err := c.store.GetInFlightRelease(ctx, ss.svc.ID); err == nil {
		inflight = true
	} else if !store.IsNotFound(err) {
		c.log.Warn("health: read in-flight release", "service", ss.String(), "err", err)
		return
	}
	if c.isRemoving(containerID) {
		return // the daemon is replacing or archiving it
	}
	restart := !inflight && !now.Before(dueAt)
	next := dueAt
	if restart {
		next = now.Add(backoffAfter(streak + 1))
	}
	c.raiseAlert(ctx, unhealthyAlert(ss, ct.HealthOutput, since, streak, restart, inflight, next), now)
	if !restart {
		return
	}

	c.health.mu.Lock()
	c.health.own[containerID] = now.Add(ownRestartWindow)
	c.health.mu.Unlock()
	msg := "Health check failing; restarting the container"
	if ct.HealthOutput != "" {
		msg = fmt.Sprintf("Health check failing (%s); restarting the container", ct.HealthOutput)
	}
	c.releaseEvent(ctx, ss, rel, msg)
	c.audit(ctx, System, "service.health_restart", &ss.project, &ss.env, ss.String(), msg)
	if err := rt.Restart(ctx, containerID, stopTimeout); err != nil {
		c.health.mu.Lock()
		delete(c.health.own, containerID)
		c.health.mu.Unlock()
		if !errors.Is(err, node.ErrNotRunning) && ctx.Err() == nil {
			c.releaseEvent(ctx, ss, rel, "Could not restart the container: "+c.nodeErr(ss.env.ServerID, err).Error())
		}
		return
	}
	restarted = true
}

// healthDone records the end of handleUnhealthy, and a restart it made.
func (c *Core) healthDone(serviceID int64, containerID string, restarted bool, now time.Time) {
	w := &c.health
	w.mu.Lock()
	h := w.svc[serviceID]
	if h == nil || h.container != containerID {
		w.mu.Unlock()
		return
	}
	h.busy = false
	if !restarted {
		w.mu.Unlock()
		return
	}
	h.streak++
	h.restarts++
	h.last = now
	// It starts over as starting; a new unhealthy event says it failed
	// again. Since stays: the alert says how long it has been failing.
	h.unhealthy = false
	n := h.restarts
	w.mu.Unlock()
	if ss, err := c.scopeOf(c.ctx, serviceID); err == nil {
		c.changeStatus(ss, func(s *apitypes.ServiceStatus) { s.HealthRestarts = n })
	}
}

// adoptHealth reads the health of a container that just became active:
// its first health_status events may have come while its release was
// still deploying, when they don't count for the service.
func (c *Core) adoptHealth(ctx context.Context, ss svcScope, containerID string) {
	ct, err := c.runtime(ss.env.ServerID).Inspect(ctx, containerID)
	if err != nil || ct.Health == "" {
		return
	}
	restarts := c.onHealth(ss, containerID, ct.Health, time.Now())
	c.changeStatus(ss, func(s *apitypes.ServiceStatus) {
		s.Health, s.HealthRestarts = apitypes.HealthState(ct.Health), restarts
	})
}

// healthAlertKey is the key of a service's unhealthy alert.
func healthAlertKey(serviceID int64) string {
	return "health:" + alertServiceKey(serviceID, "", "", "")
}

// unhealthyAlert is the alert for a running service whose health check
// keeps failing (output: what the last probe said). It turns critical
// once a restart didn't help. next is when the next restart may be.
func unhealthyAlert(ss svcScope, output string, since time.Time, streak int, restarting, inflight bool, next time.Time) alertSignal {
	msg := "Its health check keeps failing"
	if output != "" {
		msg += " (" + output + ")"
	}
	msg += " since " + since.UTC().Format("15:04 MST") + "."
	at := next.UTC().Format("15:04 MST")
	switch {
	case inflight:
		msg += " A deploy is in progress and replaces the container."
	case restarting && streak == 0:
		msg += " Restarting it."
	case restarting:
		msg += fmt.Sprintf(" Restarted %d times without recovering; restarting it again, then no sooner than %s.", streak, at)
	default:
		msg += fmt.Sprintf(" Restarted %d times without recovering; the next restart is at %s at the earliest.", streak, at)
	}
	msg += " Check its logs, or roll back if a release broke it."
	sev := apitypes.SeverityWarning
	if streak > 0 {
		sev = apitypes.SeverityCritical
	}
	return alertSignal{
		Key: healthAlertKey(ss.svc.ID), Kind: apitypes.AlertUnhealthy, Severity: sev,
		Title:   serviceKey(ss.project.Name, ss.env.Name, ss.svc.Name) + " is unhealthy",
		Message: msg, projectID: ss.project.ID, serviceID: ss.svc.ID,
		Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name,
	}
}
