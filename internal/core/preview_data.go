package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/dbcopy"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Preview data (ADR 0092): each service of a base says what its volumes
// start with in a new preview (service_previews.data): empty, its newest
// snapshot (ADR 0056's seed), a copy of the base's live database, or that
// copy masked: the Data panel's sensitive columns replaced while the base
// is read, so their values never reach the preview. Copies are template
// databases' own dumps (dbcopy): a dump job on the base's network into a
// scratch volume, then a load job into the preview's database.
//
// A preview with data to copy deploys its databases first and everything
// else once they are filled: no pull request's code runs before its
// database holds what it will hold. Whatever goes wrong, a database
// starts empty, and preview_data, its release and the audit log say why.
// Pull requests from forks start empty unless data_forks allows them.

// defaultCopyMaxMB is the largest database previews copy without
// [previews] copy_max_mb.
const defaultCopyMaxMB = 2048

// copyMaxMB is the largest database previews copy.
func (c *Core) copyMaxMB() int { return cmp.Or(c.cfg.Previews.CopyMaxMB, defaultCopyMaxMB) }

// previewDataSetting is what one base service's volumes start with.
type previewDataSetting struct {
	mode  apitypes.PreviewData
	forks bool
}

// copyEngine is the copy engine of a template database, false for a
// service that isn't one.
func copyEngine(s store.Service) (dbcopy.Engine, bool) {
	t, ok := templateOf(s)
	if !ok {
		return "", false
	}
	switch t.backup {
	case apitypes.BackupPostgres:
		return dbcopy.Postgres, true
	case apitypes.BackupMySQL:
		return dbcopy.MySQL, true
	case apitypes.BackupRedis:
		return dbcopy.Redis, true
	}
	return "", false
}

// checkPreviewData checks a service's data setting: snapshot needs a
// volume, copy and masked a template database, and handing pull requests
// real data (an unmasked copy or snapshot, or any data to forks) needs
// the Data panel's sensitive columns visible to the actor: who gives
// unreviewed code the data must be allowed to read it.
func (c *Core) checkPreviewData(ctx context.Context, a Actor, ss svcScope, mode apitypes.PreviewData, forks bool) error {
	switch mode {
	case apitypes.PreviewDataEmpty:
		if forks {
			return invalid("forks get data only when previews copy some; pick a data mode for %s too", ss.svc.Name)
		}
		return nil
	case apitypes.PreviewDataSnapshot:
		if _, _, err := c.previewSeeds(ctx, ss.envScope, []string{ss.svc.Name}); err != nil {
			return err
		}
	case apitypes.PreviewDataCopy, apitypes.PreviewDataMasked:
		if _, ok := copyEngine(ss.svc); !ok {
			return invalid("%s isn't a template database, so previews can't copy it; start it from its newest snapshot instead (`--data snapshot`)", ss)
		}
	default:
		return invalid("data is empty, snapshot, copy or masked, not %q", mode)
	}
	if mode == apitypes.PreviewDataMasked && !forks {
		return nil
	}
	perm, err := c.dataPermission(ctx, a, ss.envScope)
	if err != nil {
		return err
	}
	if perm.Sensitive != apitypes.SensitiveVisible {
		what := "an unmasked copy of " + ss.svc.Name
		if mode == apitypes.PreviewDataMasked {
			what = "the data of " + ss.svc.Name + " to forks"
		}
		return errf(apitypes.CodeForbidden, "giving pull requests %s needs its sensitive columns visible to you, and you see them masked (%s); use a masked copy", what, datapanel.Why(perm))
	}
	return nil
}

// previewDataPlan is one base service whose data a new preview copies.
type previewDataPlan struct {
	from svcScope
	set  previewDataSetting
}

// previewDataPlans lists the services of names (those a new preview
// deploys from the base's images) whose data it copies.
func previewDataPlans(base envScope, setup previewSetup, svcs []store.Service, names []string) []previewDataPlan {
	var out []previewDataPlan
	for _, name := range names {
		set, ok := setup.data[name]
		i := slices.IndexFunc(svcs, func(s store.Service) bool { return s.Name == name && s.ArchivedAt == nil })
		if !ok || i < 0 || set.mode == apitypes.PreviewDataEmpty {
			continue
		}
		out = append(out, previewDataPlan{from: svcScope{base, svcs[i]}, set: set})
	}
	return out
}

// startPreviewData fills a new preview's databases (plans) in the
// background, each deployed once its data is in, then deploys the rest
// from the base's images and starts the pull request's builds (conns).
func (c *Core) startPreviewData(a Actor, base, dst envScope, pv store.Preview, plans []previewDataPlan, rest []string, conns []store.GitConnection) {
	ctx := c.ctx
	for _, p := range plans {
		c.beginPreviewData(ctx, dst, pv.ID, p, "waiting to start")
	}
	c.wg.Go(func() {
		var wg sync.WaitGroup
		for _, p := range plans {
			wg.Go(func() { c.fillPreviewDB(ctx, a, dst, pv, p, false) })
		}
		wg.Wait()
		if ctx.Err() != nil {
			return
		}
		if len(rest) > 0 {
			if _, err := c.promote(ctx, a, base, dst, rest, true); err != nil {
				c.setPreviewEvent(pv.ID, "the base's images weren't deployed: "+c.errorLine(err))
			}
		}
		for _, conn := range conns {
			c.kickGit(conn.ServiceID, true)
		}
		c.refreshPreview(pv.ID)
	})
}

// beginPreviewData records a preview database's copy as started.
func (c *Core) beginPreviewData(ctx context.Context, dst envScope, previewID int64, p previewDataPlan, msg string) {
	to, err := c.store.GetServiceByName(ctx, store.GetServiceByNameParams{EnvironmentID: dst.env.ID, Name: p.from.svc.Name})
	if err == nil {
		err = c.store.StartPreviewData(ctx, store.StartPreviewDataParams{
			ServiceID: to.ID, PreviewID: previewID, FromServiceID: p.from.svc.ID, Mode: string(p.set.mode), Message: msg, StartedAt: now(),
		})
	}
	if err != nil {
		c.log.Error("record a preview copy", "env", dst.String(), "service", p.from.svc.Name, "err", err)
	}
}

// previewCopy is one copy into a preview database in flight.
type previewCopy struct {
	c     *Core
	a     Actor
	from  svcScope
	to    svcScope
	pv    store.Preview
	set   previewDataSetting
	start time.Time
	// dirty is set once the database's volumes may hold part of a copy,
	// or it was stopped for one: a failure from there empties it. up is
	// set while it runs.
	dirty, up bool
	stats     dbcopy.Stats
}

// fillPreviewDB fills one preview database from its base and deploys it
// (refresh: starts it again), recording how it went. A failure leaves it
// empty: never with part of a copy, and never unmasked.
func (c *Core) fillPreviewDB(ctx context.Context, a Actor, dst envScope, pv store.Preview, p previewDataPlan, refresh bool) {
	svc, err := c.store.GetServiceByName(ctx, store.GetServiceByNameParams{EnvironmentID: dst.env.ID, Name: p.from.svc.Name})
	if err != nil {
		c.log.Error("fill a preview database", "env", dst.String(), "service", p.from.svc.Name, "err", err)
		return
	}
	pc := &previewCopy{c: c, a: a, from: p.from, to: svcScope{dst, svc}, pv: pv, set: p.set, start: now(), up: refresh}
	err = pc.run(ctx, refresh)
	if ctx.Err() != nil {
		return // shutting down: the next start ends it (resumePreviewData)
	}
	// A failed refresh empties it too: it no longer holds what its row
	// says.
	if err != nil && (pc.dirty || refresh) {
		up, rerr := c.emptyPreviewDB(ctx, pc.to)
		if rerr != nil {
			pc.finish(ctx, fmt.Errorf("%w; emptying it failed too (%w), so it stays stopped", err, rerr))
			return
		}
		pc.up = up
	}
	if !pc.up {
		if derr := pc.deploy(ctx, refresh); derr != nil {
			err = errors.Join(err, fmt.Errorf("it wasn't deployed: %w", derr))
		}
	}
	pc.finish(ctx, err)
}

// run copies the data in. A database restored with its own tools
// (Postgres, MySQL) is deployed first and loaded while it runs; volumes
// (a snapshot, Redis) are filled before it starts.
func (pc *previewCopy) run(ctx context.Context, refresh bool) error {
	c := pc.c
	if pc.pv.Fork && !pc.set.forks {
		return invalid("pull requests from forks start empty; let them have the data with `dock service previews --data-forks`")
	}
	// The preview's Data panel masks what the base's does.
	if err := c.copyMarks(ctx, pc.from.svc.ID, pc.to.svc.ID); err != nil {
		return err
	}
	if pc.set.mode == apitypes.PreviewDataSnapshot {
		pc.step(ctx, "copying the newest snapshot of "+pc.from.String())
		if refresh {
			if err := pc.stop(ctx); err != nil {
				return err
			}
		}
		pc.dirty = true
		msg, err := c.fillPreviewSeed(ctx, pc.from, pc.to.envScope)
		if err != nil {
			return err
		}
		pc.step(ctx, msg)
		return nil
	}
	engine, _ := copyEngine(pc.from.svc)
	if pc.from.env.ServerID != pc.to.env.ServerID {
		return conflict("%s is on another server, and copies need the base and the preview on one; for now it starts empty", pc.from)
	}
	src, err := pc.source(ctx, engine)
	if err != nil {
		return err
	}
	scratch := pc.scratch()
	rt, err := c.runtimeFor(pc.to.env.ServerID)
	if err != nil {
		return err
	}
	_ = rt.RemoveVolume(ctx, scratch, pc.scratchLabels())
	if err := rt.EnsureVolume(ctx, scratch, pc.scratchLabels()); err != nil {
		return fmt.Errorf("create the copy's scratch volume: %w", c.nodeErr(pc.to.env.ServerID, err))
	}
	defer func() {
		if err := rt.RemoveVolume(context.WithoutCancel(ctx), scratch, pc.scratchLabels()); err != nil {
			c.log.Error("remove a preview copy's scratch volume", "volume", scratch, "err", err)
		}
	}()
	pc.step(ctx, fmt.Sprintf("reading %s (%s)", pc.from, describeMode(pc.set.mode)))
	dump, _ := dbcopy.DumpScript(engine)
	// The dump job is the base's: it joins the base's network.
	out, err := c.runCopyJob(ctx, pc.from, node.JobSpec{
		Name:  fmt.Sprintf("dock_p%d_e%d_s%d_copy_dump_%d", pc.to.project.ID, pc.to.env.ID, pc.to.svc.ID, now().UnixNano()),
		Image: jobImage(pc.from), Env: dbcopy.DumpEnv(engine, src), Script: dump, Network: networkName(pc.from.envScope),
		Mounts: []node.Mount{{Volume: scratch, Path: dbcopy.Path}},
	})
	if err != nil {
		return fmt.Errorf("read %s: %w", pc.from, err)
	}
	if pc.stats, err = dbcopy.ParseStats(out); err != nil {
		return conflict("read %s: %v", pc.from, err)
	}
	pc.step(ctx, fmt.Sprintf("loading %s into the preview", megabytes(pc.stats.Bytes)))
	load, _ := dbcopy.LoadScript(engine)
	spec := node.JobSpec{
		Name:  fmt.Sprintf("dock_p%d_e%d_s%d_copy_load_%d", pc.to.project.ID, pc.to.env.ID, pc.to.svc.ID, now().UnixNano()),
		Image: jobImage(pc.to), Script: load,
		Mounts: []node.Mount{{Volume: scratch, Path: dbcopy.Path}},
	}
	if engine == dbcopy.Redis {
		// Into its volume, before it starts.
		if refresh {
			if err := pc.stop(ctx); err != nil {
				return err
			}
		}
		t, _ := templateOf(pc.to.svc)
		vol, err := pc.volume(ctx, rt)
		if err != nil {
			return err
		}
		spec.Env, spec.Mounts = dbcopy.LoadEnv(engine, dbcopy.Target{DataDir: t.mountPath}), append(spec.Mounts, node.Mount{Volume: vol, Path: t.mountPath})
		pc.dirty = true
		if _, err := c.runCopyJob(ctx, pc.to, spec); err != nil {
			return fmt.Errorf("fill the preview's volume: %w", err)
		}
		return nil
	}
	if refresh {
		// A new, empty database, made by the image as at its first start.
		if err := pc.stop(ctx); err != nil {
			return err
		}
		if err := pc.c.wipeVolumes(ctx, pc.to); err != nil {
			return err
		}
	}
	pc.dirty = true
	if err := pc.deploy(ctx, refresh); err != nil {
		return fmt.Errorf("deploy the preview's database: %w", err)
	}
	target, err := pc.target(ctx, engine)
	if err != nil {
		return err
	}
	spec.Env, spec.Network = dbcopy.LoadEnv(engine, target), networkName(pc.to.envScope)
	if _, err := c.runCopyJob(ctx, pc.to, spec); err != nil {
		return fmt.Errorf("load the copy: %w", err)
	}
	// The schema the Data panel cached at its first start is gone.
	c.panel.Forget(pc.to.svc.ID)
	return nil
}

// source is how the dump job reaches the base's database, and what it
// masks.
func (pc *previewCopy) source(ctx context.Context, engine dbcopy.Engine) (dbcopy.Source, error) {
	c := pc.c
	t, _ := templateOf(pc.from.svc)
	src := dbcopy.Source{Host: pc.from.svc.Name, Port: t.port, MaxBytes: int64(c.copyMaxMB()) << 20}
	notRunning := func(err error) error {
		if errors.Is(err, errNotRunning) {
			return conflict("%s isn't running, so there is nothing to copy", pc.from)
		}
		return err
	}
	switch engine {
	case dbcopy.Postgres:
		conn, err := c.dbConnection(ctx, pc.from)
		if err != nil {
			return src, notRunning(err)
		}
		src.Port, src.User, src.Password, src.Database = conn.port, conn.owner.User, conn.owner.Password, conn.database
	default:
		env, err := c.activeEnv(ctx, pc.from)
		if err != nil {
			return src, notRunning(err)
		}
		src.Password = mysqlRootPassword(env)
		if engine == dbcopy.Redis {
			src.Password = env["REDIS_PASSWORD"]
		}
	}
	if pc.set.mode != apitypes.PreviewDataMasked {
		return src, nil
	}
	if engine == dbcopy.Redis {
		keys, err := c.store.ListSensitiveKeys(ctx, pc.from.svc.ID)
		if err != nil {
			return src, err
		}
		if len(keys) > 0 {
			return src, conflict("%s has sensitive key patterns (%s), which a masked copy can't leave out yet, so it starts empty", pc.from, strings.Join(keys, ", "))
		}
		return src, nil
	}
	marks, err := c.store.ListSensitiveColumns(ctx, pc.from.svc.ID)
	if err != nil {
		return src, err
	}
	for _, m := range marks {
		src.Masked = append(src.Masked, dbcopy.Column{Schema: m.SchemaName, Table: m.TableName, Column: m.ColumnName})
	}
	return src, nil
}

// target is how the load job reaches the preview's running database.
func (pc *previewCopy) target(ctx context.Context, engine dbcopy.Engine) (dbcopy.Target, error) {
	c := pc.c
	to, err := c.scopeOf(ctx, pc.to.svc.ID)
	if err != nil {
		return dbcopy.Target{}, err
	}
	t, _ := templateOf(to.svc)
	if engine == dbcopy.Postgres {
		conn, err := c.dbConnection(ctx, to)
		if err != nil {
			return dbcopy.Target{}, notRunningErr(to, err)
		}
		return dbcopy.Target{Host: to.svc.Name, Port: conn.port, User: conn.owner.User, Password: conn.owner.Password, Database: conn.database}, nil
	}
	env, err := c.activeEnv(ctx, to)
	if err != nil {
		return dbcopy.Target{}, notRunningErr(to, err)
	}
	return dbcopy.Target{Host: to.svc.Name, Port: t.port, Password: mysqlRootPassword(env)}, nil
}

// scratch is the Docker name of the copy's scratch volume, one per
// preview database.
func (pc *previewCopy) scratch() string { return volumeName(pc.to, "_copy") }

// scratchLabels label the scratch volume with the project alone: the
// dump job (the base's) and the load job (the preview's) both mount it,
// and the runtime refuses a volume labelled for another service.
func (pc *previewCopy) scratchLabels() node.Labels {
	l := pc.c.labels(pc.to, 0)
	l.Environment, l.Service = "", ""
	return l
}

// volume is the preview database's template volume, created if its first
// release hasn't yet.
func (pc *previewCopy) volume(ctx context.Context, rt node.Runtime) (string, error) {
	vols, err := pc.c.store.ListVolumes(ctx, pc.to.svc.ID)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(vols, func(v store.Volume) bool { return v.Name == templateVolume })
	if i < 0 {
		return "", fmt.Errorf("%s has no %s volume", pc.to, templateVolume)
	}
	if err := rt.EnsureVolume(ctx, vols[i].DockerName, pc.c.labels(pc.to, 0)); err != nil {
		return "", fmt.Errorf("create volume: %w", pc.c.nodeErr(pc.to.env.ServerID, err))
	}
	return vols[i].DockerName, nil
}

// deploy runs the preview's database and waits for it: its first release,
// promoted from the base, or on a refresh its active one started again.
func (pc *previewCopy) deploy(ctx context.Context, refresh bool) error {
	c := pc.c
	if refresh {
		to, err := c.scopeOf(ctx, pc.to.svc.ID)
		if err != nil {
			return err
		}
		active, err := c.store.GetActiveRelease(ctx, to.svc.ID)
		if err != nil {
			return err
		}
		if err := c.restartRelease(ctx, to, active); err != nil {
			return fmt.Errorf("start release %d again: %w", active.Number, c.nodeErr(to.env.ServerID, err))
		}
		pc.up = true
		return nil
	}
	res, last, err := c.promoteSteps(ctx, pc.a, pc.from.envScope, pc.to.envScope, []string{pc.from.svc.Name}, true)
	if err != nil {
		return err
	}
	if last == nil {
		if len(res.Skipped) > 0 {
			return conflict("%s", res.Skipped[0].Message)
		}
		return conflict("no release started")
	}
	pc.up = true
	select {
	case <-last.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if !last.ok {
		return conflict("its release failed; see it for why")
	}
	return nil
}

// stop stops the preview's database for a refresh: nothing may write it
// while its data is replaced.
func (pc *previewCopy) stop(ctx context.Context) error {
	c := pc.c
	active, err := c.store.GetActiveRelease(ctx, pc.to.svc.ID)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := c.removeReleaseContainers(ctx, pc.to, active.ID, keepData); err != nil {
		return fmt.Errorf("stop %s: %w", pc.to.svc.Name, c.nodeErr(pc.to.env.ServerID, err))
	}
	c.setStatus(pc.to, apitypes.ServiceStatus{State: apitypes.ServiceStopped})
	pc.dirty, pc.up = true, false
	return nil
}

// step records the step a copy is at: on the copy's row and, once the
// database has a release, as one of its events.
func (pc *previewCopy) step(ctx context.Context, msg string) {
	c := pc.c
	if err := c.store.SetPreviewDataMessage(ctx, store.SetPreviewDataMessageParams{Message: cleanLine(msg, 500), ServiceID: pc.to.svc.ID}); err != nil {
		c.log.Error("record a preview copy's step", "service", pc.to.String(), "err", err)
	}
	c.setPreviewEvent(pc.pv.ID, pc.to.svc.Name+": "+msg)
}

// finish records how the copy went: on its row, the preview, the
// database's release and the audit log.
func (pc *previewCopy) finish(ctx context.Context, err error) {
	c := pc.c
	status, msg := apitypes.PreviewDataCopied, fmt.Sprintf("%s of %s: %s in %s", describeMode(pc.set.mode), pc.from, megabytes(pc.stats.Bytes), now().Sub(pc.start).Round(time.Second))
	if pc.set.mode == apitypes.PreviewDataSnapshot {
		msg = fmt.Sprintf("newest snapshot of %s, in %s", pc.from, now().Sub(pc.start).Round(time.Second))
	} else if pc.stats.Columns > 0 {
		msg += fmt.Sprintf(", %d %s masked in %d %s", pc.stats.Columns, plural(pc.stats.Columns, "column"), pc.stats.Tables, plural(pc.stats.Tables, "table"))
	} else if pc.set.mode == apitypes.PreviewDataMasked {
		msg += ", no sensitive columns marked to mask"
	}
	if err != nil {
		status, msg = apitypes.PreviewDataStartedEmpty, "started empty: "+c.errorLine(err)
	}
	if ferr := c.store.FinishPreviewData(ctx, store.FinishPreviewDataParams{
		Status: string(status), Message: cleanLine(msg, 500), Bytes: pc.stats.Bytes,
		MaskedTables: int64(pc.stats.Tables), MaskedColumns: int64(pc.stats.Columns), FinishedAt: ptr(now()), ServiceID: pc.to.svc.ID,
	}); ferr != nil {
		c.log.Error("record a preview copy", "service", pc.to.String(), "err", ferr)
	}
	if rel, rerr := c.store.GetActiveRelease(ctx, pc.to.svc.ID); rerr == nil {
		c.releaseEvent(ctx, pc.to, rel, "Data: "+msg)
	}
	c.setPreviewEvent(pc.pv.ID, pc.to.svc.Name+": "+msg)
	c.audit(ctx, System, "preview.data", &pc.to.project, &pc.to.env, pc.to.String(), msg)
}

// describeMode says what kind of copy a mode makes.
func describeMode(mode apitypes.PreviewData) string {
	switch mode {
	case apitypes.PreviewDataSnapshot:
		return "newest snapshot"
	case apitypes.PreviewDataMasked:
		return "masked copy"
	}
	return "copy"
}

// megabytes is a size for people.
func megabytes(b int64) string {
	if b < 1<<20 {
		return "under 1 MB"
	}
	return fmt.Sprintf("%d MB", b>>20)
}

// plural is word for n of it.
func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// copyMarks gives a preview database its base's sensitive columns and
// key patterns, replacing any it had.
func (c *Core) copyMarks(ctx context.Context, from, to int64) error {
	return c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteSensitiveColumns(ctx, to); err != nil {
			return err
		}
		if err := q.DeleteSensitiveKeys(ctx, to); err != nil {
			return err
		}
		p := store.CopySensitiveMarksParams{ToService: to, FromService: from}
		if err := q.CopySensitiveMarks(ctx, p); err != nil {
			return err
		}
		return q.CopySensitiveKeys(ctx, store.CopySensitiveKeysParams(p))
	})
}

// runCopyJob runs a copy job on ss's server in spec.Image (a template's,
// pulled if missing), with the job memory limit. A job that exits
// non-zero is an error with the end of its output, the database's
// passwords masked.
func (c *Core) runCopyJob(ctx context.Context, ss svcScope, spec node.JobSpec) (string, error) {
	rt, err := c.runtimeFor(ss.env.ServerID)
	if err != nil {
		return "", err
	}
	if err := c.ensureImage(ctx, ss.env.ServerID, rt, spec.Image); err != nil {
		return "", err
	}
	spec.Labels = c.labels(ss, 0)
	spec.Resources = node.Resources{MemoryBytes: jobMemoryMB << 20, NanoCPUs: 1e9}
	res, err := rt.RunJob(ctx, spec)
	out := jobLog(spec.Env, res.Output)
	if err != nil {
		return out, c.nodeErr(ss.env.ServerID, err)
	}
	if res.ExitCode != 0 {
		return out, conflict("%s", cmp.Or(jobOutput(out), fmt.Sprintf("exit code %d", res.ExitCode)))
	}
	return out, nil
}

// wipeVolumes empties a stopped (or never started) service's volumes.
func (c *Core) wipeVolumes(ctx context.Context, ss svcScope) error {
	vols, err := c.store.ListVolumes(ctx, ss.svc.ID)
	if err != nil {
		return err
	}
	rt, err := c.runtimeFor(ss.env.ServerID)
	if err != nil {
		return err
	}
	var mounts []node.Mount
	for _, v := range vols {
		if err := rt.EnsureVolume(ctx, v.DockerName, c.labels(ss, 0)); err != nil {
			return fmt.Errorf("create volume: %w", c.nodeErr(ss.env.ServerID, err))
		}
		mounts = append(mounts, node.Mount{Volume: v.DockerName, Path: "/volumes/" + v.Name})
	}
	if len(mounts) == 0 {
		return nil
	}
	_, err = c.runToolJob(ctx, ss.env.ServerID, rt, node.JobSpec{
		Name:   fmt.Sprintf("dock_p%d_e%d_s%d_wipe_%d", ss.project.ID, ss.env.ID, ss.svc.ID, now().UnixNano()),
		Labels: c.labels(ss, 0), Script: "set -eu\nfind /volumes -mindepth 2 -delete\n", Mounts: mounts,
	})
	if err != nil {
		return fmt.Errorf("empty the volumes of %s: %w", ss.svc.Name, err)
	}
	return nil
}

// emptyPreviewDB empties a preview database after a failed copy: it is
// stopped, its volumes emptied and its active release started again (up),
// so the image makes a new database. One never deployed only has its
// volumes emptied. If emptying fails, it stays stopped.
func (c *Core) emptyPreviewDB(ctx context.Context, ss svcScope) (up bool, err error) {
	active, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
	if store.IsNotFound(err) {
		return false, c.wipeVolumes(ctx, ss)
	}
	if err != nil {
		return false, err
	}
	if err := c.removeReleaseContainers(ctx, ss, active.ID, keepData); err != nil {
		return false, fmt.Errorf("stop it: %w", c.nodeErr(ss.env.ServerID, err))
	}
	c.setStatus(ss, apitypes.ServiceStatus{State: apitypes.ServiceStopped})
	if err := c.wipeVolumes(ctx, ss); err != nil {
		return false, err
	}
	return true, c.restartRelease(ctx, ss, active)
}

// RefreshPreviewData copies an open preview's databases from their base
// again: each is stopped, emptied and filled as at the preview's
// creation (its base service's current data setting), then the
// preview's other services restart, so the pull request's migrations run
// on the new copy. The base is only read. The answer comes at once; the
// preview's databases say how it goes. Scope: deploy.
func (c *Core) RefreshPreviewData(ctx context.Context, a Actor, ref EnvRef, req apitypes.RefreshPreviewDataRequest) (apitypes.Preview, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeDeploy, "refreshing a preview's data")
	if err != nil {
		return apitypes.Preview{}, err
	}
	pv, err := c.store.GetPreviewByEnvironment(ctx, es.env.ID)
	if store.IsNotFound(err) {
		return apitypes.Preview{}, notFound("%s is not a preview environment", es)
	}
	if err != nil {
		return apitypes.Preview{}, err
	}
	if pv.ClosedAt != nil {
		return apitypes.Preview{}, invalid("the preview of %s#%d is closed", pv.Repo, pv.Number)
	}
	base, err := c.envByID(ctx, pv.BaseEnvironmentID)
	if err != nil {
		return apitypes.Preview{}, err
	}
	setup, err := c.previewSetupOf(ctx, base.env.ID)
	if err != nil {
		return apitypes.Preview{}, err
	}
	svcs, err := c.store.ListServices(ctx, base.env.ID)
	if err != nil {
		return apitypes.Preview{}, err
	}
	var names []string
	for name, set := range setup.data {
		if set.mode != apitypes.PreviewDataEmpty && (len(req.Services) == 0 || slices.Contains(req.Services, name)) {
			names = append(names, name)
		}
	}
	for _, name := range req.Services {
		if !slices.Contains(names, name) {
			return apitypes.Preview{}, invalid("previews don't copy %s's data from %s; set it with `dock service previews -s %s -e %s --data masked`", name, base.env.Name, name, base.env.Name)
		}
	}
	if len(names) == 0 {
		return apitypes.Preview{}, invalid("no service of %s copies its data into previews; set one with `dock service previews -s db -e %s --data masked`", base.env.Name, base.env.Name)
	}
	slices.Sort(names)
	plans := previewDataPlans(base, setup, svcs, names)
	if pv.Fork {
		plans = slices.DeleteFunc(plans, func(p previewDataPlan) bool { return !p.set.forks })
		if len(plans) == 0 {
			return apitypes.Preview{}, invalid("this preview is of a fork's pull request, and %s gives forks no data", strings.Join(names, ", "))
		}
	}
	data, err := c.store.ListProjectPreviewData(ctx, es.project.ID)
	if err != nil {
		return apitypes.Preview{}, err
	}
	for _, d := range data {
		if d.PreviewID == pv.ID && d.Status == string(apitypes.PreviewDataCopying) {
			return apitypes.Preview{}, conflict("%s is copying its data already; wait for it (`dock env previews`)", d.Service)
		}
	}
	c.audit(ctx, a, "preview.refresh_data", &es.project, &es.env, es.String(), "refreshing the data of "+strings.Join(names, ", ")+" from "+base.env.Name)
	for _, p := range plans {
		c.beginPreviewData(ctx, es, pv.ID, p, "waiting to start")
	}
	c.wg.Go(func() { c.refreshPreviewData(a, es, pv, plans) })
	return c.toPreview(ctx, es, pv)
}

// refreshPreviewData refreshes plans' databases in the preview es, then
// restarts its other services.
func (c *Core) refreshPreviewData(a Actor, es envScope, pv store.Preview, plans []previewDataPlan) {
	ctx := c.ctx
	// An asleep preview wakes first: its databases must run to load.
	if err := c.wakeNow(ctx, es, "refresh from base"); err != nil {
		c.log.Error("wake a preview to refresh it", "env", es.String(), "err", err)
	}
	var wg sync.WaitGroup
	for _, p := range plans {
		wg.Go(func() { c.fillPreviewDB(ctx, a, es, pv, p, true) })
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	svcs, err := c.store.ListServices(ctx, es.env.ID)
	if err != nil {
		c.log.Error("restart a refreshed preview", "env", es.String(), "err", err)
		return
	}
	for _, s := range svcs {
		if s.ArchivedAt != nil || slices.ContainsFunc(plans, func(p previewDataPlan) bool { return p.from.svc.Name == s.Name }) {
			continue
		}
		ss := svcScope{es, s}
		active, err := c.store.GetActiveRelease(ctx, s.ID)
		if err != nil {
			continue // never deployed: nothing to restart
		}
		if _, err := c.startRelease(ctx, a, ss, releaseSpec{source: apitypes.SourceRestart, from: &active}); err != nil {
			c.setPreviewEvent(pv.ID, fmt.Sprintf("%s wasn't restarted on the new data: %s", s.Name, c.errorLine(err)))
		}
	}
	c.refreshPreview(pv.ID)
}

// resumePreviewData ends the copies a restart cut short: each database
// starts empty, as after any failed copy, and the preview's services that
// waited for it are deployed.
func (c *Core) resumePreviewData(ctx context.Context) error {
	rows, err := c.store.ListCopyingPreviewData(ctx)
	if err != nil || len(rows) == 0 {
		return err
	}
	var envs []int64
	for _, r := range rows {
		if r.ClosedAt != nil {
			// Its volumes went with the preview.
			if err := c.store.FinishPreviewData(ctx, store.FinishPreviewDataParams{Status: string(apitypes.PreviewDataStartedEmpty), Message: "the preview closed during the copy", FinishedAt: ptr(now()), ServiceID: r.ServiceID}); err != nil {
				return err
			}
			continue
		}
		ss, err := c.scopeOf(ctx, r.ServiceID)
		if err != nil {
			return err
		}
		pc := &previewCopy{c: c, to: ss, set: previewDataSetting{mode: apitypes.PreviewData(r.Mode)}, start: r.StartedAt}
		if rt, err := c.runtimeFor(ss.env.ServerID); err == nil {
			_ = rt.RemoveVolume(ctx, pc.scratch(), pc.scratchLabels())
		}
		msg := "started empty: a restart of Tempo Dock cut the copy short; refresh it from the base (`dock env previews refresh " + ss.env.Name + "`)"
		if _, err := c.emptyPreviewDB(ctx, ss); err != nil {
			msg += "; emptying it failed (" + c.errorLine(err) + ")"
		}
		if err := c.store.FinishPreviewData(ctx, store.FinishPreviewDataParams{Status: string(apitypes.PreviewDataStartedEmpty), Message: cleanLine(msg, 500), FinishedAt: ptr(now()), ServiceID: r.ServiceID}); err != nil {
			return err
		}
		c.setPreviewEvent(r.PreviewID, ss.svc.Name+": "+msg)
		if !slices.Contains(envs, r.EnvironmentID) {
			envs = append(envs, r.EnvironmentID)
		}
	}
	// A new preview's services waited for the copies: deploy the ones
	// that never were, and start its builds.
	for _, id := range envs {
		es, err := c.envByID(ctx, id)
		if err != nil {
			return err
		}
		pv, err := c.store.GetPreviewByEnvironment(ctx, id)
		if err != nil {
			return err
		}
		base, err := c.envByID(ctx, pv.BaseEnvironmentID)
		if err != nil {
			return err
		}
		svcs, err := c.store.ListServices(ctx, id)
		if err != nil {
			return err
		}
		conns, err := c.store.ListEnvGitConnections(ctx, id)
		if err != nil {
			return err
		}
		var never []string
		for _, s := range svcs {
			if s.ArchivedAt != nil || slices.ContainsFunc(conns, func(g store.GitConnection) bool { return g.ServiceID == s.ID }) {
				continue
			}
			rels, err := c.store.ListReleases(ctx, store.ListReleasesParams{ServiceID: s.ID, Limit: 1})
			if err != nil {
				return err
			}
			if len(rels) == 0 {
				never = append(never, s.Name)
			}
		}
		if len(never) > 0 {
			if _, err := c.promote(ctx, System.InOrg(es.project.OrgID), base, es, never, true); err != nil {
				c.setPreviewEvent(pv.ID, "the base's images weren't deployed: "+c.errorLine(err))
			}
		}
		for _, conn := range conns {
			c.kickGit(conn.ServiceID, true)
		}
	}
	return nil
}
