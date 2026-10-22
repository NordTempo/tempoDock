package core

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Runtime status of services is kept in memory: Docker is the source of
// truth, read once on start (reconcile) and then followed via its event
// stream. Nothing polls.

func (c *Core) getStatus(serviceID int64) apitypes.ServiceStatus {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	if s, ok := c.status[serviceID]; ok {
		return s
	}
	return apitypes.ServiceStatus{State: apitypes.ServiceNone}
}

// serverLink is a server's name and link status, which reads put on the
// statuses of services there (mark).
type serverLink struct {
	name   string
	status apitypes.ServerStatus
}

// linkOf returns a server's link. A connected server costs nothing (the
// hub's state is in memory); one that isn't costs one query, for its name
// and whether it is revoked.
func (c *Core) linkOf(ctx context.Context, serverID int64) serverLink {
	if c.online(serverID) {
		return serverLink{status: apitypes.ServerOnline}
	}
	s, err := c.store.GetServer(ctx, serverID)
	if err != nil {
		return serverLink{status: apitypes.ServerOffline}
	}
	return serverLink{name: s.Name, status: c.serverStatus(s)}
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
func (c *Core) setStatus(ss svcScope, s apitypes.ServiceStatus) {
	c.statusMu.Lock()
	prev, had := c.status[ss.svc.ID]
	prev.UpdatedAt = nil
	changed := !had || prev != s
	t := now()
	s.UpdatedAt = &t
	c.status[ss.svc.ID] = s
	c.statusMu.Unlock()
	if changed {
		c.publish(ss, apitypes.Event{Type: apitypes.EventServiceStatus, ServiceStatus: &s})
	}
}

func statusOf(ct node.Container) apitypes.ServiceStatus {
	s := apitypes.ServiceStatus{RestartCount: ct.RestartCount, OOMKilled: ct.OOMKilled, ExitCode: ct.ExitCode}
	switch ct.State {
	case node.StateRunning:
		s.State = apitypes.ServiceRunning
	case node.StateRestarting:
		s.State = apitypes.ServiceRestarting
	case node.StateExited, node.StateDead:
		s.State = apitypes.ServiceCrashed
	default:
		s.State = apitypes.ServiceStopped
	}
	return s
}

// memoryLimitMB is a release's memory limit (0 if its config can't be read).
func (c *Core) memoryLimitMB(rel store.Release) int {
	cfg, err := c.openConfig(rel.Config)
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

// watchRuntime follows container lifecycle events and keeps the status of
// each service's active container up to date.
func (c *Core) watchRuntime(events <-chan node.ContainerEvent) {
	defer c.wg.Done()
	for ev := range events {
		c.onContainerEvent(c.ctx, ev)
	}
}

// foreign reports whether a resource is labelled for another control plane
// sharing the Docker host. Those are never reconciled, reported or removed.
func (c *Core) foreign(l node.Labels) bool {
	return l.ControlPlane != "" && l.ControlPlane != c.install
}

func (c *Core) onContainerEvent(ctx context.Context, ev node.ContainerEvent) {
	if c.foreign(ev.Labels) {
		return
	}
	if ev.Action == "die" || ev.Action == "destroy" {
		// A database's container went away (redeploy, archive, crash):
		// drop the panel's connections to it.
		if id, err := strconv.ParseInt(ev.Labels.Service, 10, 64); err == nil {
			c.data.Forget(id)
		}
	}
	if c.ownRemoval(ev) {
		return // the daemon is stopping it: not a crash, not drift
	}
	id, err := strconv.ParseInt(ev.Labels.Release, 10, 64)
	if err != nil {
		return
	}
	rel, err := c.store.GetRelease(ctx, id)
	if err != nil || rel.Status != string(apitypes.ReleaseActive) || rel.ContainerID != ev.ContainerID {
		return // only the active container defines the service's status
	}
	ss, err := c.scopeOf(ctx, rel.ServiceID)
	if err != nil {
		return
	}
	s := c.getStatus(ss.svc.ID)
	s.UpdatedAt = nil
	switch ev.Action {
	case "start", "restart":
		if s.State == apitypes.ServiceCrashed || s.State == apitypes.ServiceRestarting || ev.Action == "restart" {
			s.RestartCount++
		}
		s.State = apitypes.ServiceRunning
	case "die":
		s.State, s.ExitCode = apitypes.ServiceCrashed, ev.ExitCode
	case "oom":
		s.OOMKilled, s.MemoryLimitMB = true, c.memoryLimitMB(rel)
	case "destroy":
		s.State = apitypes.ServiceStopped
		c.drift(ctx, &ss, fmt.Sprintf("container of active release %d of %s was removed outside the platform", rel.Number, ss))
	default:
		return
	}
	c.setStatus(ss, s)
}

// recover fails deploys that were in flight when the daemon stopped and
// removes their containers, along with any other container of a failed or
// superseded release (a drain cut short by a restart, a stop that failed).
// If a stateful deploy had already stopped the previous release, that
// release is started again, after the new container is gone. On remote
// servers, which aren't connected yet, both happen when each reconnects.
func (c *Core) recover(ctx context.Context) error {
	inflight, err := c.store.ListInFlightReleases(ctx)
	if err != nil {
		return err
	}
	var interrupted []svcScope
	for _, r := range inflight {
		ss, err := c.scopeOf(ctx, r.ServiceID)
		if err != nil {
			return err
		}
		msg := "daemon restarted during deploy"
		if err := c.store.FailRelease(ctx, store.FailReleaseParams{Error: msg, ErrorCode: string(apitypes.CodeDeployFailed), FinishedAt: ptr(now()), ID: r.ID}); err != nil {
			return err
		}
		c.releaseEvent(ctx, ss, r, fmt.Sprintf("Release %d failed: %s", r.Number, msg))
		c.restage(ctx, ss, r, msg)
		interrupted = append(interrupted, ss)
	}
	if err := c.sweepRetired(ctx, c.local.ID, c.rt); err != nil {
		return err
	}
	for _, ss := range interrupted {
		if ss.env.ServerID != c.local.ID {
			c.ensureLater(ss.env.ServerID, ss.svc.ID)
			continue
		}
		if active, err := c.store.GetActiveRelease(ctx, ss.svc.ID); err == nil {
			c.ensureRunning(ctx, ss, active)
		}
	}
	return nil
}

// sweepRetired removes a server's containers of failed and superseded
// releases, then frees the host ports of retired releases whose containers
// are gone. A container counts as ours only if all its labels match the
// release's project, environment and service, and it carries this control
// plane's label or none (node.Labels.Matches). This is also how an archive
// on an unreachable server finishes: its release is superseded, so its
// container goes when the server reconnects.
func (c *Core) sweepRetired(ctx context.Context, serverID int64, rt node.Runtime) error {
	ctrs, err := rt.List(ctx)
	if err != nil {
		return err
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
	remaining := map[int64]bool{}
	for id, cs := range byRelease {
		rel, err := c.store.GetRelease(ctx, id)
		if store.IsNotFound(err) {
			continue // reconcile reports it
		}
		if err != nil {
			return err
		}
		if rel.Status != string(apitypes.ReleaseFailed) && rel.Status != string(apitypes.ReleaseSuperseded) {
			continue
		}
		remaining[id] = true
		ss, err := c.scopeOf(ctx, rel.ServiceID)
		if err != nil {
			return err
		}
		want := c.labels(ss, rel.ID)
		if ss.env.ServerID != serverID || slices.ContainsFunc(cs, func(ct node.Container) bool { return !ct.Labels.Matches(want) }) {
			continue
		}
		c.releaseEvent(ctx, ss, rel, fmt.Sprintf("Removing leftover container of release %d (%s)", rel.Number, rel.Status))
		if err := c.removeContainers(ctx, ss.env.ServerID, cs); err != nil {
			c.drift(ctx, &ss, fmt.Sprintf("could not remove leftover container of release %d of %s: %v", rel.Number, ss, err))
			continue
		}
		remaining[id] = false
	}
	holders, err := c.store.ListRetiredPortHolders(ctx, serverID)
	if err != nil {
		return err
	}
	for _, id := range holders {
		if !remaining[id] {
			c.freePort(ctx, id)
		}
	}
	return nil
}

// ensureRunning makes sure an active release has a running container:
// one started just before a crash is adopted, a stopped or missing one is
// replaced (after an interrupted stateful recreate).
func (c *Core) ensureRunning(ctx context.Context, ss svcScope, active store.Release) {
	rt := c.runtime(ss.env.ServerID)
	ctrs, err := rt.List(ctx)
	if err != nil {
		c.log.Warn("list containers", "err", err)
		return
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
		return
	}
	if err := c.removeContainers(ctx, ss.env.ServerID, stopped); err != nil {
		c.drift(ctx, &ss, fmt.Sprintf("could not start release %d of %s again: %v", active.Number, ss, err))
		return
	}
	c.releaseEvent(ctx, ss, active, fmt.Sprintf("Starting release %d again", active.Number))
	if err := c.restartRelease(ctx, ss, active); err != nil {
		c.drift(ctx, &ss, fmt.Sprintf("could not start release %d of %s again: %v", active.Number, ss, err))
	}
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
		ct, ok := byID[r.ContainerID]
		if !ok {
			c.setStatus(ss, apitypes.ServiceStatus{State: apitypes.ServiceStopped})
			if report {
				c.drift(ctx, &ss, fmt.Sprintf("container of active release %d of %s is missing; redeploy or roll back to start it", r.Number, ss))
			}
			continue
		}
		s := statusOf(ct)
		if s.OOMKilled {
			s.MemoryLimitMB = c.memoryLimitMB(r)
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
	for _, ct := range ctrs {
		if !activeIDs[ct.Labels.Release] && !c.foreign(ct.Labels) {
			c.drift(ctx, nil, fmt.Sprintf("container %s (%s) is not part of any active release; leaving it alone", ct.Name, ct.ID))
		}
	}
	return nil
}

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
