package core

import (
	"context"
	"fmt"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Stopping a service on purpose (ADR 0050): services.stopped_at is set
// while it is stopped. Its active release stays active (with its config,
// image and host port) but has no container, its domains answer 503, and
// recovery, reconcile and container events leave it alone. The next
// release of the service that goes live (StartService, or any deploy)
// clears it in cutover's activation transaction.

// StopService stops a service: it is marked stopped, its domains switch
// to the 503 page, then its active release's containers are removed.
// Volumes, domains, variables and releases are kept. Stopping a stopped
// service changes nothing, except that it tries again to remove a
// container a failed stop left.
func (c *Core) StopService(ctx context.Context, a Actor, ref ServiceRef) (apitypes.Service, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "stopping a service")
	if err != nil {
		return apitypes.Service{}, err
	}
	if ss.svc.ArchivedAt != nil {
		return apitypes.Service{}, invalid("service %s is archived", ss)
	}
	// Like deploys, nothing waits for a server that can't be reached.
	if _, err := c.runtimeFor(ss.env.ServerID); err != nil {
		return apitypes.Service{}, err
	}
	var active store.Release
	var stopped bool
	// One transaction with the in-flight check, which startRelease makes
	// in its own: a deploy and a stop never both go ahead. An in-place
	// restore stops the service and starts it again itself; it marks the
	// service in a transaction too (lockForRestore), so it either sees
	// this stop when it reads the service again or this stop sees it.
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := c.restoreWriting(ss, "stop it"); err != nil {
			return err
		}
		if r, err := q.GetInFlightRelease(ctx, ss.svc.ID); err == nil {
			return deployInProgress(ss, r)
		}
		svc, err := q.GetService(ctx, ss.svc.ID)
		if err != nil {
			return err
		}
		if svc.ArchivedAt != nil {
			return invalid("service %s is archived", ss)
		}
		active, err = q.GetActiveRelease(ctx, svc.ID)
		if store.IsNotFound(err) {
			return invalid("service %s has no active release, so nothing runs to stop; deploy it first", ss)
		}
		if err != nil {
			return err
		}
		n, err := q.StopService(ctx, store.StopServiceParams{StoppedAt: ptr(now()), StoppedBy: a.Name, ID: svc.ID})
		if err != nil {
			return err
		}
		stopped = n > 0
		ss.svc, err = q.GetService(ctx, svc.ID)
		return err
	})
	if err != nil {
		return apitypes.Service{}, err
	}
	// Routes first, so no request reaches a container that is stopping.
	if err := c.applyRoutes(ctx, ss.env.ServerID); err != nil {
		c.log.Error("apply routes after stop", "service", ss.String(), "err", err)
	}
	c.setStatus(ss, apitypes.ServiceStatus{State: apitypes.ServiceStopped})
	if stopped {
		c.releaseEvent(ctx, ss, active, fmt.Sprintf("Stopping release %d: %s stopped %s", active.Number, a.Name, ss.svc.Name))
	}
	if err := c.removeReleaseContainers(ctx, ss, active.ID, keepData); err != nil {
		err = c.nodeErr(ss.env.ServerID, err)
		c.releaseEvent(ctx, ss, active, fmt.Sprintf("Could not remove the container of release %d: %v", active.Number, err))
		code := CodeOf(err)
		if code != apitypes.CodeServerOffline {
			code = apitypes.CodeConflict
		}
		return apitypes.Service{}, errf(code, "%s is stopped and its domains answer 503, but its container could not be removed: %v; stop it again (on a server that went offline it is removed when the server reconnects)", ss, err)
	}
	c.forgetContainer(ctx, active.ID)
	if stopped {
		c.releaseEvent(ctx, ss, active, fmt.Sprintf("Stopped release %d; volumes, domains and variables are kept", active.Number))
		c.audit(ctx, a, "service.stop", &ss.project, &ss.env, ss.String(), fmt.Sprintf("stopped release %d", active.Number))
	}
	return c.toService(ctx, a, ss, apitypes.PendingChanges{})
}

// StartService starts a stopped service again: a new release copying its
// active one, through the normal health-checked path. Its source is
// restart, which it is (the releases table only takes the sources it
// always had); its first event and the audit log say it is a start. It is
// running again once that release goes live; if it fails, the service
// stays stopped. A service that isn't stopped is a conflict.
func (c *Core) StartService(ctx context.Context, a Actor, ref ServiceRef) (apitypes.Release, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "starting a service")
	if err != nil {
		return apitypes.Release{}, err
	}
	if ss.svc.ArchivedAt != nil {
		return apitypes.Release{}, invalid("service %s is archived", ss)
	}
	if ss.svc.StoppedAt == nil {
		return apitypes.Release{}, conflict("service %s isn't stopped; to start a fresh copy of its active release, restart it (`dock restart`)", ss)
	}
	active, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
	if store.IsNotFound(err) {
		return apitypes.Release{}, invalid("service %s has no active release to start; deploy it first", ss)
	}
	if err != nil {
		return apitypes.Release{}, err
	}
	rel, err := c.startRelease(ctx, a, ss, releaseSpec{source: apitypes.SourceRestart, from: &active})
	if err != nil {
		return apitypes.Release{}, err
	}
	c.releaseEvent(ctx, ss, rel, fmt.Sprintf("Starting %s again (stopped by %s): release %d copies release %d", ss.svc.Name, ss.svc.StoppedBy, rel.Number, active.Number))
	c.audit(ctx, a, "service.start", &ss.project, &ss.env, ss.String(), fmt.Sprintf("release %d starts release %d", rel.Number, active.Number))
	return c.toRelease(ctx, ss, rel, false)
}

// forgetContainer clears an active release's container once a stop removed
// it: stats, the data panel and container events then see none.
func (c *Core) forgetContainer(ctx context.Context, releaseID int64) {
	if err := c.store.SetReleaseContainer(ctx, store.SetReleaseContainerParams{ContainerID: "", ID: releaseID}); err != nil {
		c.log.Error("forget the stopped release's container", "release", releaseID, "err", err)
	}
}
