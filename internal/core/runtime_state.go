package core

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/NordTempo/platform/internal/apitypes"
	"github.com/NordTempo/platform/internal/node"
	"github.com/NordTempo/platform/internal/store"
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

func (c *Core) onContainerEvent(ctx context.Context, ev node.ContainerEvent) {
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
		s.OOMKilled = true
	case "destroy":
		s.State = apitypes.ServiceStopped
		c.drift(ctx, &ss, fmt.Sprintf("container of active release %d of %s was removed outside the platform", rel.Number, ss))
	default:
		return
	}
	c.setStatus(ss, s)
}

// recover fails deploys that were in flight when the daemon stopped and
// removes their containers. If a stateful deploy had already stopped the
// previous release, that release is started again.
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
		if r.ContainerID != "" {
			c.removeContainer(ctx, ss.env.ServerID, r.ContainerID)
		}
		msg := "daemon restarted during deploy"
		if err := c.store.FailRelease(ctx, store.FailReleaseParams{Error: msg, ErrorCode: string(apitypes.CodeDeployFailed), FinishedAt: ptr(now()), ID: r.ID}); err != nil {
			return err
		}
		c.releaseEvent(ctx, ss, r, fmt.Sprintf("Release %d failed: %s", r.Number, msg))
		if active, err := c.store.GetActiveRelease(ctx, r.ServiceID); err == nil {
			c.ensureRunning(ctx, ss, active)
		}
	}
	return nil
}

// ensureRunning starts an active release again if its container is gone or
// stopped (after an interrupted stateful recreate).
func (c *Core) ensureRunning(ctx context.Context, ss svcScope, active store.Release) {
	rt := c.runtime(ss.env.ServerID)
	if active.ContainerID != "" {
		ct, err := rt.Inspect(ctx, active.ContainerID)
		if err == nil && ct.State == node.StateRunning {
			return
		}
		if err == nil {
			_ = rt.Remove(ctx, active.ContainerID)
		} else if !errors.Is(err, node.ErrNotFound) {
			c.log.Warn("inspect container", "id", active.ContainerID, "err", err)
			return
		}
	}
	c.releaseEvent(ctx, ss, active, fmt.Sprintf("Starting release %d again: its container was stopped by an interrupted deploy", active.Number))
	if err := c.restartRelease(ctx, ss, active); err != nil {
		c.drift(ctx, &ss, fmt.Sprintf("could not start release %d of %s again: %v", active.Number, ss, err))
	}
}

// reconcile compares active releases with the runtime's containers, sets
// service statuses and reports drift. It never touches containers.
func (c *Core) reconcile(ctx context.Context) error {
	ctrs, err := c.rt.List(ctx)
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
		if ss.env.ServerID != c.local.ID {
			continue
		}
		ct, ok := byID[r.ContainerID]
		if !ok {
			c.setStatus(ss, apitypes.ServiceStatus{State: apitypes.ServiceStopped})
			c.drift(ctx, &ss, fmt.Sprintf("container of active release %d of %s is missing; redeploy or roll back to start it", r.Number, ss))
			continue
		}
		c.setStatus(ss, statusOf(ct))
	}
	for _, ct := range ctrs {
		if !activeIDs[ct.Labels.Release] {
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
