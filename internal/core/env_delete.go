package core

import (
	"context"
	"fmt"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// DeleteEnvironment deletes an environment for good (ADR 0103): its
// services are archived (routes removed, containers stopped), then on
// its server its containers, network, volumes and volume snapshots go,
// in the analytics store its sites' events, and in Dock every row of it
// (store.DeleteEnvironments), which frees its name and its domains'
// hostnames. Closed previews cloned from it go with it. The audit log
// keeps its events; backups stay in their restic repositories, and the
// answer's notes say where.
//
// It takes what deleting a project takes (ADR 0078): an org admin with
// an unlimited scope, a recent sign-in for sessions, the environment's
// name typed again (req.Confirm) and every server it has something on
// online. A token is refused in a locked environment. It refuses the
// project's only environment (archive the project instead), a preview
// (close it instead) and an environment with open previews.
func (c *Core) DeleteEnvironment(ctx context.Context, a Actor, ref EnvRef, req apitypes.DeleteEnvironmentRequest) (apitypes.EnvironmentDeleted, error) {
	const action = "deleting an environment"
	if err := a.requireUnlimited(apitypes.ScopeAdmin, action); err != nil {
		return apitypes.EnvironmentDeleted{}, err
	}
	if err := a.requireRecent(action); err != nil {
		return apitypes.EnvironmentDeleted{}, err
	}
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeAdmin, action)
	if err != nil {
		return apitypes.EnvironmentDeleted{}, err
	}
	p, e := es.project, es.env
	if req.ID != 0 && req.ID != e.ID {
		return apitypes.EnvironmentDeleted{}, conflict("environment %s is another environment now; refresh it before deleting", es)
	}
	if p.ArchivedAt != nil {
		return apitypes.EnvironmentDeleted{}, invalid("project %s is archived; delete the whole project instead (`dock project delete %s`)", p.Name, p.Name)
	}
	if req.Confirm != e.Name {
		return apitypes.EnvironmentDeleted{}, invalid("type the environment's name, %s, to confirm deleting it and everything in it", e.Name)
	}
	d, err := c.loadDeletion(ctx, p)
	if err != nil {
		return apitypes.EnvironmentDeleted{}, err
	}
	pvs, err := c.store.ListProjectPreviewEnvironments(ctx, p.ID)
	if err != nil {
		return apitypes.EnvironmentDeleted{}, err
	}
	ids, open := []int64{e.ID}, 0
	for _, pv := range pvs {
		switch {
		case pv.EnvironmentID == e.ID:
			return apitypes.EnvironmentDeleted{}, invalid("%s is a preview environment; close it instead (`dock environment previews close %s -p %s`)", es, e.Name, p.Name)
		case pv.BaseEnvironmentID != e.ID:
		case pv.ClosedAt == nil:
			open++
		default:
			ids = append(ids, pv.EnvironmentID)
		}
	}
	if open > 0 {
		return apitypes.EnvironmentDeleted{}, conflict("%s has %s cloned from it; close them first (`dock environment previews -p %s` lists them)", es, count(open, "open preview"), p.Name)
	}
	live := 0
	for _, x := range d.envs {
		if x.ArchivedAt == nil {
			live++
		}
	}
	if live <= 1 {
		return apitypes.EnvironmentDeleted{}, invalid("%s is the only environment of project %s; create another first, or archive the project (`dock project archive %s`)", e.Name, p.Name, p.Name)
	}
	d = d.of("environment "+es.String(), ids)
	for _, x := range d.envs {
		if err := c.notMoving(ctx, envScope{p, x}, "delete the environment"); err != nil {
			return apitypes.EnvironmentDeleted{}, err
		}
	}
	// No backup, snapshot or restore of its services runs meanwhile.
	var claimed []int64
	defer func() {
		for _, id := range claimed {
			c.releaseService(id)
		}
	}()
	for _, s := range d.svcs {
		if !c.claimService(s.ID) {
			return apitypes.EnvironmentDeleted{}, conflict("a backup, snapshot or restore of %s/%s is running; delete the environment once it finishes", d.scope(s), s.Name)
		}
		claimed = append(claimed, s.ID)
	}
	if err := c.serversOnline(ctx, a, d); err != nil {
		return apitypes.EnvironmentDeleted{}, err
	}
	// Its live services are archived first: their routes and DNS records
	// go, and their containers stop before anything is removed.
	notes, err := c.archiveForDelete(ctx, es, d)
	if err != nil {
		return apitypes.EnvironmentDeleted{}, err
	}
	if err := c.removeProjectResources(ctx, d); err != nil {
		return apitypes.EnvironmentDeleted{}, fmt.Errorf("%w; its services are archived but nothing was deleted from Dock yet, so deleting again picks up where this stopped", err)
	}
	notes = append(notes, c.deleteProjectAnalytics(ctx, d.sites)...)
	if err := c.deleteEnvironmentRows(ctx, es, d); err != nil {
		return apitypes.EnvironmentDeleted{}, err
	}
	notes = append(notes, keptBackupNotes(d.backups)...)
	for _, s := range d.svcs {
		c.kickGit(s.ID, true) // stops a fetch in progress
	}
	c.reloadSleep(ctx)
	c.kickCloudflare()
	c.kickUptime()
	c.kickJobs()
	c.kickBackups()
	c.maybePruneRegistry()
	out := apitypes.EnvironmentDeleted{Project: p.Name, Environment: e.Name, Services: len(d.svcs), Volumes: d.volumes(), Previews: len(ids) - 1, Notes: notes}
	what := fmt.Sprintf("deleted environment %s for good: %s, %s and their history", e.Name, count(out.Services, "service"), count(out.Volumes, "volume"))
	if out.Previews > 0 {
		what += ", with " + count(out.Previews, "closed preview")
	}
	c.audit(ctx, a, "environment.delete", &p, &e, es.String(), archiveSummary(what+"; its activity stays", notes))
	return out, nil
}

// archiveForDelete archives the environment's live services and waits
// for their containers to stop, so removeProjectResources finds them
// stopped. A service that is deploying refuses it, and nothing is
// archived then. The notes are finishArchive's.
func (c *Core) archiveForDelete(ctx context.Context, es envScope, d deletion) ([]string, error) {
	var done []archivedService
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		for _, s := range d.svcs {
			if s.EnvironmentID != es.env.ID || s.ArchivedAt != nil {
				continue
			}
			as, ok, err := archiveTx(ctx, q, svcScope{es, s})
			if err != nil {
				return err
			}
			if ok {
				done = append(done, as)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	notes, stopped := c.finishArchive(ctx, done)
	stopped.Wait()
	return notes, nil
}

// deleteEnvironmentRows deletes the deletion's rows, after checking in
// the same transaction that the environment is still the one named and
// no preview opened from it meanwhile, then forgets what memory holds of
// them. Alert writes wait meanwhile (alertEngine.writes).
func (c *Core) deleteEnvironmentRows(ctx context.Context, es envScope, d deletion) error {
	c.alerts.writes.Lock()
	defer c.alerts.writes.Unlock()
	ids := make([]int64, 0, len(d.envs))
	for _, e := range d.envs {
		ids = append(ids, e.ID)
	}
	err := c.store.DeleteEnvironments(ctx, ids, func(q *store.Queries) error {
		now, err := q.GetEnvironment(ctx, es.env.ID)
		if store.IsNotFound(err) {
			return notFound("environment %s not found", es)
		}
		if err != nil {
			return err
		}
		if now.Name != es.env.Name {
			return conflict("environment %s changed meanwhile; refresh it before deleting", es)
		}
		n, err := q.CountOpenBasePreviews(ctx, es.env.ID)
		if err != nil {
			return err
		}
		if n > 0 {
			return conflict("a preview of %s opened meanwhile; close it, then delete again", es)
		}
		return nil
	})
	if err != nil {
		return err
	}
	c.forgetDeleted(d)
	return nil
}
