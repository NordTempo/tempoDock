package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// analyticsDeleteTimeout bounds deleting a deleted project's sites'
// events from the analytics store, which may be slow or gone.
const analyticsDeleteTimeout = 30 * time.Second

// DeleteProject deletes an archived project for good (ADR 0077): on its
// servers, its containers, networks, volumes and volume snapshots; in
// the analytics store, its sites' events and replays; in Dock, every row
// of it (store.DeleteProject), which frees its name, its domains'
// hostnames and its sites' public addresses. Open previews' pull request
// comments and statuses say it went. The audit log keeps its events.
//
// It takes an org admin with an unlimited scope, a recent sign-in for
// sessions, and the project's name typed again (req.Confirm). A token is
// refused in a locked environment, which files an access request like any
// change there. Every server the project has something on must be
// online: nothing is deleted until what is on them is gone, so a failure
// halfway leaves a project that can be deleted again. Backups stay in
// their restic repositories; the answer's notes say where.
func (c *Core) DeleteProject(ctx context.Context, a Actor, name string, req apitypes.DeleteProjectRequest) (apitypes.ProjectDeleted, error) {
	const action = "deleting a project"
	if err := a.requireUnlimited(apitypes.ScopeAdmin, action); err != nil {
		return apitypes.ProjectDeleted{}, err
	}
	if err := a.requireRecent(action); err != nil {
		return apitypes.ProjectDeleted{}, err
	}
	p, err := c.projectFor(ctx, a, name, apitypes.ScopeAdmin, action)
	if err != nil {
		return apitypes.ProjectDeleted{}, err
	}
	if req.ID != 0 && req.ID != p.ID {
		return apitypes.ProjectDeleted{}, conflict("project %s is another project now; refresh it before deleting", name)
	}
	if p.ArchivedAt == nil {
		return apitypes.ProjectDeleted{}, invalid("project %s isn't archived; only an archived project can be deleted: archive it first (`dock project archive %s`)", p.Name, p.Name)
	}
	if req.Confirm != p.Name {
		return apitypes.ProjectDeleted{}, invalid("type the project's name, %s, to confirm deleting it and everything in it", p.Name)
	}
	if err := c.checkProjectLocks(ctx, a, p, action); err != nil {
		return apitypes.ProjectDeleted{}, err
	}
	d, err := c.loadDeletion(ctx, p)
	if err != nil {
		return apitypes.ProjectDeleted{}, err
	}
	for _, e := range d.envs {
		if err := c.notMoving(ctx, envScope{p, e}, "delete the project"); err != nil {
			return apitypes.ProjectDeleted{}, err
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
			return apitypes.ProjectDeleted{}, conflict("a backup, snapshot or restore of %s/%s is running; delete the project once it finishes", d.scope(s), s.Name)
		}
		claimed = append(claimed, s.ID)
	}
	if err := c.serversOnline(ctx, a, d); err != nil {
		return apitypes.ProjectDeleted{}, err
	}
	if err := c.removeProjectResources(ctx, d); err != nil {
		return apitypes.ProjectDeleted{}, fmt.Errorf("%w; nothing was deleted from Dock yet, so deleting again picks up where this stopped", err)
	}
	notes := c.deleteProjectAnalytics(ctx, d.sites)
	previews := c.previewsToTell(ctx, d)
	if err := c.deleteProjectRows(ctx, p, d); err != nil {
		return apitypes.ProjectDeleted{}, err
	}
	notes = append(notes, keptBackupNotes(d.backups)...)
	c.tellPreviewsDeleted(p, previews)
	c.kickCloudflare() // records of its domains still to remove
	c.kickUptime()
	c.kickJobs()
	c.kickBackups()
	c.maybePruneRegistry()
	out := apitypes.ProjectDeleted{Project: p.Name, Environments: len(d.envs), Services: len(d.svcs), Volumes: d.volumes(), Notes: notes}
	c.audit(ctx, a, "project.delete", &p, nil, p.Name, archiveSummary(fmt.Sprintf("deleted archived project %s for good: %s, %s, %s and their history; its activity stays",
		p.Name, count(out.Environments, "environment"), count(out.Services, "service"), count(out.Volumes, "volume")), notes))
	return out, nil
}

// deletion is what deleting a project removes, read before anything goes.
type deletion struct {
	project store.Project
	envs    []store.Environment
	svcs    []store.Service
	vols    []store.Volume
	snaps   []store.VolumeSnapshot
	domains []store.Domain
	sites   []int64
	backups []store.ListProjectKeptBackupsRow
}

func (c *Core) loadDeletion(ctx context.Context, p store.Project) (deletion, error) {
	d := deletion{project: p}
	var err error
	if d.envs, err = c.store.ListAllProjectEnvironments(ctx, p.ID); err != nil {
		return d, err
	}
	if d.svcs, err = c.store.ListProjectServices(ctx, p.ID); err != nil {
		return d, err
	}
	if d.vols, err = c.store.ListProjectVolumes(ctx, p.ID); err != nil {
		return d, err
	}
	if d.snaps, err = c.store.ListProjectSnapshots(ctx, p.ID); err != nil {
		return d, err
	}
	if d.domains, err = c.store.ListProjectDomains(ctx, p.ID); err != nil {
		return d, err
	}
	sites, err := c.store.ListProjectSites(ctx, p.ID)
	if err != nil {
		return d, err
	}
	for _, s := range sites {
		d.sites = append(d.sites, s.ID)
	}
	d.backups, err = c.store.ListProjectKeptBackups(ctx, p.ID)
	return d, err
}

// env is the environment with ID id.
func (d deletion) env(id int64) store.Environment {
	i := slices.IndexFunc(d.envs, func(e store.Environment) bool { return e.ID == id })
	if i < 0 {
		return store.Environment{ID: id}
	}
	return d.envs[i]
}

// scope is the environment of service s.
func (d deletion) scope(s store.Service) envScope { return envScope{d.project, d.env(s.EnvironmentID)} }

// service is the service with ID id, in its scope.
func (d deletion) service(id int64) svcScope {
	i := slices.IndexFunc(d.svcs, func(s store.Service) bool { return s.ID == id })
	if i < 0 {
		return svcScope{envScope: envScope{project: d.project}, svc: store.Service{ID: id}}
	}
	return svcScope{d.scope(d.svcs[i]), d.svcs[i]}
}

// volumes counts the volumes that exist on a server (not staged ones).
func (d deletion) volumes() int {
	n := 0
	for _, v := range d.vols {
		if !v.Pending {
			n++
		}
	}
	return n
}

// servers are the servers the project has something on: its
// environments' and its snapshots' files'.
func (d deletion) servers() []int64 {
	ids := map[int64]bool{}
	for _, e := range d.envs {
		ids[e.ServerID] = true
	}
	for _, s := range d.snaps {
		ids[s.ServerID] = true
	}
	return slices.Sorted(maps.Keys(ids))
}

// serversOnline refuses the delete while a server the project has
// something on can't be reached: its volumes would stay there unknown to
// Dock. Server names are for operators only (seesServers).
func (c *Core) serversOnline(ctx context.Context, a Actor, d deletion) error {
	var down []string
	for _, id := range d.servers() {
		if c.connected(id) != nil {
			continue
		}
		link := c.linkOf(ctx, a, id)
		down = append(down, cmp.Or(link.name, "a server")+" ("+string(link.status)+")")
	}
	if len(down) == 0 {
		return nil
	}
	return conflict("project %s has containers, volumes or snapshots on %s, which can't be reached; delete it once the server is back, or have a platform operator remove the server (`dock server remove`), which leaves what is on that machine",
		d.project.Name, strings.Join(down, ", "))
}

// removeProjectResources removes what the project has on its servers, one
// server after another: its containers (found by label, so ones Dock lost
// track of go too), then its environments' networks, its volumes and its
// snapshots' files. Each is idempotent: one already gone counts as
// removed, so a delete that failed halfway can run again.
func (c *Core) removeProjectResources(ctx context.Context, d deletion) error {
	p := d.project
	pid := strconv.FormatInt(p.ID, 10)
	for _, sid := range d.servers() {
		rt := c.connected(sid)
		if rt == nil {
			return conflict("a server of project %s went offline; delete it again once the server is back", p.Name)
		}
		ctrs, err := rt.List(ctx)
		if err != nil {
			return fmt.Errorf("list the containers on a server of project %s: %w", p.Name, err)
		}
		var mine []node.Container
		for _, ct := range ctrs {
			if ct.Labels.Project == pid && !c.foreign(ct.Labels) {
				mine = append(mine, ct)
			}
		}
		if err := c.removeContainers(ctx, sid, mine, node.RemoveOptions{}); err != nil {
			return fmt.Errorf("remove the containers of project %s: %w", p.Name, err)
		}
		for _, e := range d.envs {
			if e.ServerID != sid {
				continue
			}
			es := envScope{p, e}
			labels := node.Labels{ControlPlane: c.install, Project: pid, Environment: strconv.FormatInt(e.ID, 10)}
			if err := rt.RemoveNetwork(ctx, networkName(es), labels); err != nil && !errors.Is(err, node.ErrNotFound) {
				return fmt.Errorf("remove the network of %s: %w", es, err)
			}
		}
		for _, v := range d.vols {
			ss := d.service(v.ServiceID)
			if ss.env.ServerID != sid {
				continue // staged ones never exist; a moved environment's old server keeps its copy (ADR 0053)
			}
			if err := rt.RemoveVolume(ctx, v.DockerName, c.labels(ss, 0)); err != nil && !errors.Is(err, node.ErrNotFound) {
				return fmt.Errorf("remove volume %s of %s: %w", v.Name, ss, err)
			}
		}
		var snaps []store.VolumeSnapshot
		for _, s := range d.snaps {
			if s.ServerID == sid {
				snaps = append(snaps, s)
			}
		}
		if err := c.deleteSnapshots(ctx, sid, snaps); err != nil {
			return fmt.Errorf("delete the volume snapshots of project %s: %w", p.Name, err)
		}
	}
	return nil
}

// deleteProjectAnalytics deletes the sites' raw events, errors, Web
// Vitals and replays from the analytics store. Its daily totals stay
// under the sites' IDs, which no new site gets (migration 0101). A store
// that can't be reached keeps the events; the note says so. Nothing
// reads them once the sites are gone.
func (c *Core) deleteProjectAnalytics(ctx context.Context, sites []int64) []string {
	cl, _ := c.analyticsClient()
	if cl == nil || len(sites) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, analyticsDeleteTimeout)
	defer cancel()
	end := now().AddDate(1, 0, 0).UnixMilli()
	for _, id := range sites {
		_, err := cl.Prune(ctx, analytics.PruneQuery{Site: id, Before: end, ErrorsBefore: end})
		if err == nil {
			_, err = cl.PruneReplays(ctx, analytics.ReplayPrune{Site: id, Before: end})
		}
		if err != nil {
			c.log.Warn("delete a deleted project's analytics", "site", id, "err", err)
			return []string{"the analytics store couldn't be reached: its sites' events stay in it, where nothing reads them"}
		}
	}
	return nil
}

// deleteProjectRows deletes the project's rows, then forgets what memory
// holds of them: open alerts, domain checks and service statuses. Alert
// writes wait meanwhile (alertEngine.writes), so none lands on a row
// that is going.
func (c *Core) deleteProjectRows(ctx context.Context, p store.Project, d deletion) error {
	c.alerts.writes.Lock()
	defer c.alerts.writes.Unlock()
	err := c.store.DeleteProject(ctx, p.ID, func(q *store.Queries) error {
		now, err := q.GetProject(ctx, p.ID)
		if store.IsNotFound(err) {
			return notFound("project %q not found", p.Name)
		}
		if err != nil {
			return err
		}
		if now.Name != p.Name || now.ArchivedAt == nil {
			return conflict("project %s changed meanwhile; refresh it before deleting", p.Name)
		}
		return nil
	})
	if err != nil {
		return err
	}
	svcs := map[int64]bool{}
	for _, s := range d.svcs {
		svcs[s.ID] = true
	}
	c.alerts.mu.Lock()
	maps.DeleteFunc(c.alerts.open, func(_ string, al store.Alert) bool {
		return al.ProjectID != nil && *al.ProjectID == p.ID || al.ServiceID != nil && svcs[*al.ServiceID]
	})
	c.alerts.mu.Unlock()
	c.checksMu.Lock()
	for _, dm := range d.domains {
		delete(c.checks, dm.ID)
	}
	c.checksMu.Unlock()
	c.statusMu.Lock()
	for id := range svcs {
		delete(c.status, id)
	}
	c.statusMu.Unlock()
	return nil
}

// keptBackupNotes says where the deleted services' backups stay, one note
// per repository (rows come ordered by it), with the restic tags that
// find them.
func keptBackupNotes(rows []store.ListProjectKeptBackupsRow) []string {
	var notes []string
	for len(rows) > 0 {
		repo := rows[0].Repository
		end := slices.IndexFunc(rows, func(r store.ListProjectKeptBackupsRow) bool { return r.Repository != repo })
		if end < 0 {
			end = len(rows)
		}
		var n int64
		var tags []string
		for _, r := range rows[:end] {
			n += r.Snapshots
			tags = append(tags, backup.Tag(r.ServiceID))
		}
		rows = rows[end:]
		stay := "stay"
		if n == 1 {
			stay = "stays"
		}
		notes = append(notes, fmt.Sprintf("%s %s in %s, tagged %s: restic with the repository password lists them (`restic snapshots --tag %s`) and removes them (`restic forget --prune ID...`)",
			count(int(n), "backup"), stay, repoWords(repo), strings.Join(tags, ", "), strings.Join(tags, " --tag ")))
	}
	return notes
}
