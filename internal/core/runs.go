package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// One-off commands in a service's active image (`dock run`, ADR 0048).
// A run is a job container (node.Containers.RunJob) with the active
// release's image, resolved variables and resource limits on the
// environment's private network, recorded in service_runs while it runs
// and audited as service.run when it ends.

const (
	// maxRunsPerService caps a service's concurrent runs, so a loop of
	// runs can't take its server's memory.
	maxRunsPerService = 4
	// keepRuns is how many runs a service's history keeps.
	keepRuns = 50
	// runAuditCommand caps the command an audit summary quotes; the run
	// keeps all of it.
	runAuditCommand = 300
)

// runState counts the runs in flight per service (by ID), and the ones
// with their volumes mounted for writing. Entries go when their runs end.
type runState struct {
	mu      sync.Mutex
	running map[int64]int
	writing map[int64]bool
}

// Run is a command checked and recorded, ready to run once: Wait runs
// it, Abandon gives it up unrun. OpenRun makes one.
type Run struct {
	c       *Core
	actor   Actor
	ss      svcScope
	rel     store.Release
	cfg     releaseConfig
	row     store.ServiceRun
	once    sync.Once
	release func()
}

// OpenRun checks that the actor may run req's command in the service
// (scope deploy on its environment), checks the command, the volumes and
// the service's limit of concurrent runs, and records the run as running.
// tty marks a terminal session, whose output isn't kept.
func (c *Core) OpenRun(ctx context.Context, a Actor, ref ServiceRef, req apitypes.RunCommandRequest, tty bool) (*Run, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "running a command")
	if err != nil {
		return nil, err
	}
	if ss.svc.ArchivedAt != nil {
		return nil, invalid("service %s is archived; restore it to run commands in it", ss.svc.Name)
	}
	cmd := strings.TrimSpace(req.Command)
	switch {
	case cmd == "":
		return nil, invalid("give a command to run, e.g. `dock run -- ls`")
	case len(cmd) > apitypes.MaxCommandBytes:
		return nil, invalid("the command is %d bytes; at most %d", len(cmd), apitypes.MaxCommandBytes)
	case !utf8.ValidString(cmd) || strings.ContainsRune(cmd, 0):
		return nil, invalid("the command must be UTF-8 text without NUL bytes")
	}
	timeout := req.TimeoutSeconds
	switch {
	case timeout == 0:
		timeout = apitypes.DefaultRunTimeoutSeconds
	case timeout < 0 || timeout > apitypes.MaxRunTimeoutSeconds:
		return nil, invalid("timeout must be between 1 and %d seconds", apitypes.MaxRunTimeoutSeconds)
	}
	rel, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
	if store.IsNotFound(err) {
		return nil, conflict("%s has no active release to run the command in; deploy it first", ss.svc.Name)
	}
	if err != nil {
		return nil, err
	}
	cfg, err := c.openConfig(rel)
	if err != nil {
		return nil, err
	}
	volumes, err := runVolumes(ss, cfg, req.Volumes)
	if err != nil {
		return nil, err
	}
	release, err := c.holdRun(ss, volumes)
	if err != nil {
		return nil, err
	}
	params := store.InsertServiceRunParams{
		ServiceID: ss.svc.ID, ReleaseID: &rel.ID, Trigger: string(apitypes.TriggerCommand), Command: cmd, Tty: tty,
		Volumes: string(volumes), TimeoutSeconds: int64(timeout), Actor: a.Name, ActorKind: a.Kind, StartedAt: now(),
	}
	if a.UserID != 0 {
		params.UserID = &a.UserID
	}
	if a.TokenID != 0 {
		params.TokenID = &a.TokenID
	}
	row, err := c.store.InsertServiceRun(ctx, params)
	if err != nil {
		release()
		return nil, err
	}
	r := &Run{c: c, actor: a, ss: ss, rel: rel, cfg: cfg, row: row, release: release}
	c.publishRun(ss, rel, row)
	return r, nil
}

// runVolumes decides how a run gets the service's volumes. A database
// template's volume belongs to its server process and is never mounted
// (clients reach the database over the network); another stateful
// service's are read-only unless asked for write; a stateless service
// has none.
func runVolumes(ss svcScope, cfg releaseConfig, want apitypes.RunVolumes) (apitypes.RunVolumes, error) {
	_, database := templateOf(ss.svc)
	switch {
	case want != "" && want != apitypes.VolumesNone && want != apitypes.VolumesRead && want != apitypes.VolumesWrite:
		return "", invalid("volumes must be none, read or write")
	case len(cfg.Mounts) == 0 || want == apitypes.VolumesNone:
		if want == apitypes.VolumesRead || want == apitypes.VolumesWrite {
			return "", invalid("%s has no volumes to mount", ss.svc.Name)
		}
		return apitypes.VolumesNone, nil
	case database:
		if want != "" {
			return "", invalid("%s is a database: commands run without its volume, which only its server may open; reach it over the network (its variables are set)", ss.svc.Name)
		}
		return apitypes.VolumesNone, nil
	case want == "":
		return apitypes.VolumesRead, nil
	}
	return want, nil
}

// holdRun takes one of the service's run slots (and its one write slot
// for a run mounting volumes for writing); the returned func gives them
// back.
func (c *Core) holdRun(ss svcScope, volumes apitypes.RunVolumes) (func(), error) {
	id := ss.svc.ID
	c.runs.mu.Lock()
	defer c.runs.mu.Unlock()
	if c.runs.running[id] >= maxRunsPerService {
		return nil, conflict("%s already runs %d commands, the most at once; wait for one to end", ss.svc.Name, maxRunsPerService)
	}
	write := volumes == apitypes.VolumesWrite
	if write && c.runs.writing[id] {
		return nil, conflict("another command has %s's volumes open for writing; wait for it to end, or mount them read-only", ss.svc.Name)
	}
	c.runs.running[id]++
	if write {
		c.runs.writing[id] = true
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			c.runs.mu.Lock()
			defer c.runs.mu.Unlock()
			if c.runs.running[id]--; c.runs.running[id] <= 0 {
				delete(c.runs.running, id)
			}
			if write {
				delete(c.runs.writing, id)
			}
		})
	}, nil
}

// Info is the run as it was recorded.
func (r *Run) Info() apitypes.CommandRun { return r.c.toRun(r.ss, r.rel, r.row) }

// Abandon ends a run that never ran (the caller couldn't take its
// output) as canceled. It does nothing once Wait was called.
func (r *Run) Abandon() {
	r.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.c.ctx), cleanupTimeout)
		defer cancel()
		r.finish(ctx, apitypes.RunCanceled, nil, "it was canceled before it started", "")
	})
}

// Wait runs the command and blocks until it ended, returning how. stream
// (nil: none) connects it to its caller; without one, only the tail of
// its output is kept. The run ends at its timeout, when ctx ends
// (canceled) or when the control plane stops (interrupted). Only the
// first call runs it.
func (r *Run) Wait(ctx context.Context, stream *node.JobStream) apitypes.CommandRun {
	out := r.Info()
	r.once.Do(func() { out = r.wait(ctx, stream) })
	return out
}

func (r *Run) wait(ctx context.Context, stream *node.JobStream) apitypes.CommandRun {
	c := r.c
	timeout := time.Duration(r.row.TimeoutSeconds) * time.Second
	jctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()

	res, err := r.runJob(jctx, stream)
	// Recording the end outlives the run's own context.
	fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer fcancel()
	output := ""
	if !r.row.Tty {
		output = runTail(res.Output)
	}
	switch {
	case err == nil && res.ExitCode == 0:
		return r.finish(fctx, apitypes.RunSucceeded, ptr(0), "", output)
	case err == nil:
		return r.finish(fctx, apitypes.RunFailed, ptr(res.ExitCode), "", output)
	case c.ctx.Err() != nil:
		return r.finish(fctx, apitypes.RunInterrupted, nil, "the control plane stopped while it ran; run it again", output)
	case ctx.Err() != nil:
		return r.finish(fctx, apitypes.RunCanceled, nil, "its caller went away, so it was stopped", output)
	case errors.Is(jctx.Err(), context.DeadlineExceeded):
		return r.finish(fctx, apitypes.RunTimedOut, nil, fmt.Sprintf("it ran longer than its %s timeout and was stopped", timeout), output)
	}
	return r.finish(fctx, apitypes.RunFailed, nil, "it could not run: "+c.nodeErr(r.ss.env.ServerID, err).Error(), output)
}

// runJob runs the command's job container on the service's server.
func (r *Run) runJob(ctx context.Context, stream *node.JobStream) (node.JobResult, error) {
	c, ss := r.c, r.ss
	rt, err := c.runtimeFor(ss.env.ServerID)
	if err != nil {
		return node.JobResult{}, err
	}
	network := networkName(ss.envScope)
	if err := rt.EnsureNetwork(ctx, network, c.labels(ss, 0)); err != nil {
		return node.JobResult{}, err
	}
	spec := c.releaseJob(ss, r.rel, r.cfg, fmt.Sprintf("dock_p%d_e%d_s%d_run%d", ss.project.ID, ss.env.ID, ss.svc.ID, r.row.ID), r.row.Command)
	if err := c.ensureImage(ctx, ss.env.ServerID, rt, spec.Image); err != nil {
		return node.JobResult{}, err
	}
	if stream == nil {
		// A stream that only keeps the tail: on a remote server a streamed
		// job is bound by its own timeout, not a control call's.
		stream = &node.JobStream{}
	}
	spec.Stream = stream
	if v := apitypes.RunVolumes(r.row.Volumes); v != apitypes.VolumesNone {
		for _, m := range r.cfg.Mounts {
			spec.Mounts = append(spec.Mounts, node.Mount{Volume: m.DockerName, Path: m.Path, ReadOnly: v == apitypes.VolumesRead})
		}
	}
	return rt.RunJob(ctx, spec)
}

// finish records how the run ended, trims the service's history, audits
// it and publishes it.
func (r *Run) finish(ctx context.Context, status apitypes.RunStatus, exit *int, msg, output string) apitypes.CommandRun {
	c := r.c
	defer r.release()
	at := now()
	var code *int64
	if exit != nil {
		code = ptr(int64(*exit))
	}
	if err := c.store.FinishServiceRun(ctx, store.FinishServiceRunParams{
		Status: string(status), ExitCode: code, Error: msg, Output: output, FinishedAt: &at, ID: r.row.ID,
	}); err != nil {
		c.log.Error("record the end of a run", "run", r.row.ID, "err", err)
	}
	r.row.Status, r.row.ExitCode, r.row.Error, r.row.Output, r.row.FinishedAt = string(status), code, msg, output, &at
	if err := c.store.TrimServiceRuns(ctx, store.TrimServiceRunsParams{ServiceID: r.ss.svc.ID, Keep: keepRuns}); err != nil {
		c.log.Error("trim run history", "service", r.ss.String(), "err", err)
	}
	c.audit(ctx, r.actor, "service.run", &r.ss.project, &r.ss.env, r.ss.String(), runSummary(r.row))
	c.publishRun(r.ss, r.rel, r.row)
	return c.toRun(r.ss, r.rel, r.row)
}

// runSummary is a finished run's audit summary: the command and how it
// ended.
func runSummary(row store.ServiceRun) string {
	took := ""
	if row.FinishedAt != nil {
		took = " after " + row.FinishedAt.Sub(row.StartedAt).Round(time.Second).String()
	}
	end := ""
	switch apitypes.RunStatus(row.Status) {
	case apitypes.RunSucceeded, apitypes.RunFailed:
		if row.ExitCode != nil {
			end = fmt.Sprintf("exit code %d%s", *row.ExitCode, took)
			break
		}
		end = row.Error
	default:
		end = strings.ReplaceAll(row.Status, "_", " ") + took
	}
	kind := "ran"
	if row.Tty {
		kind = "ran a terminal session:"
	}
	return fmt.Sprintf("%s `%s`: %s", kind, cleanLine(row.Command, runAuditCommand), end)
}

// runTail keeps the last apitypes.MaxRunOutputBytes of a run's output, as
// valid UTF-8.
func runTail(out string) string {
	if len(out) > apitypes.MaxRunOutputBytes {
		out = out[len(out)-apitypes.MaxRunOutputBytes:]
	}
	return strings.ToValidUTF8(out, "")
}

// StartRun opens a run and runs it in the background, without a stream
// (the UI's and API's runs): the answer is the run, running; a run
// event says when it ended.
func (c *Core) StartRun(ctx context.Context, a Actor, ref ServiceRef, req apitypes.RunCommandRequest) (apitypes.CommandRun, error) {
	r, err := c.OpenRun(ctx, a, ref, req, false)
	if err != nil {
		return apitypes.CommandRun{}, err
	}
	info := r.Info() // before it runs: the run's end rewrites it
	c.wg.Go(func() { r.Wait(c.ctx, nil) })
	return info, nil
}

// RunCommand opens a run and blocks until it ended, without a stream
// (agents' run_command): ctx ending cancels it.
func (c *Core) RunCommand(ctx context.Context, a Actor, ref ServiceRef, req apitypes.RunCommandRequest) (apitypes.CommandRun, error) {
	r, err := c.OpenRun(ctx, a, ref, req, false)
	if err != nil {
		return apitypes.CommandRun{}, err
	}
	return r.Wait(ctx, nil), nil
}

// ListRuns returns a service's latest runs, newest first, without their
// output. Scope read.
func (c *Core) ListRuns(ctx context.Context, a Actor, ref ServiceRef) ([]apitypes.CommandRun, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "listing a service's runs")
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListServiceRuns(ctx, store.ListServiceRunsParams{ServiceID: ss.svc.ID, Limit: keepRuns})
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.CommandRun, 0, len(rows))
	for _, row := range rows {
		out = append(out, c.toRun(ss, store.Release{Number: row.ReleaseNumber}, store.ServiceRun{
			ID: row.ID, ServiceID: row.ServiceID, ReleaseID: row.ReleaseID, Trigger: row.Trigger, Command: row.Command, Tty: row.Tty,
			Volumes: row.Volumes, TimeoutSeconds: row.TimeoutSeconds, Status: row.Status, ExitCode: row.ExitCode, Error: row.Error,
			Actor: row.Actor, ActorKind: row.ActorKind, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
		}))
	}
	return out, nil
}

// GetRun returns one of a service's runs with its output's tail. Its
// output is whatever the command printed, which can be its variables, so
// it takes the scope running one does: deploy.
func (c *Core) GetRun(ctx context.Context, a Actor, ref ServiceRef, id int64) (apitypes.CommandRun, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "reading a run's output")
	if err != nil {
		return apitypes.CommandRun{}, err
	}
	row, err := c.store.GetServiceRun(ctx, store.GetServiceRunParams{ID: id, ServiceID: ss.svc.ID})
	if store.IsNotFound(err) {
		return apitypes.CommandRun{}, notFound("run %d of %s not found; see `dock runs`", id, ss.svc.Name)
	}
	if err != nil {
		return apitypes.CommandRun{}, err
	}
	rel, err := c.runRelease(ctx, row)
	if err != nil {
		return apitypes.CommandRun{}, err
	}
	return c.toRun(ss, rel, row), nil
}

// runRelease is the release a run ran in; none (zero) for a run that
// never had one.
func (c *Core) runRelease(ctx context.Context, row store.ServiceRun) (store.Release, error) {
	if row.ReleaseID == nil {
		return store.Release{}, nil
	}
	return c.store.GetRelease(ctx, *row.ReleaseID)
}

// toRun converts a run row.
func (c *Core) toRun(ss svcScope, rel store.Release, row store.ServiceRun) apitypes.CommandRun {
	out := apitypes.CommandRun{
		ID: row.ID, Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name,
		Trigger: apitypes.RunTrigger(row.Trigger), Command: row.Command, Release: rel.Number, TTY: row.Tty,
		Volumes: apitypes.RunVolumes(row.Volumes), TimeoutSeconds: int(row.TimeoutSeconds), Status: apitypes.RunStatus(row.Status),
		Error: row.Error, Output: row.Output, CreatedBy: row.Actor, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
	}
	if row.ExitCode != nil {
		out.ExitCode = ptr(int(*row.ExitCode))
	}
	return out
}

// publishRun sends a run event, without the run's output: that is read
// with deploy (GetRun), and events go to everyone who may read the
// service.
func (c *Core) publishRun(ss svcScope, rel store.Release, row store.ServiceRun) {
	run := c.toRun(ss, rel, row)
	run.Output = ""
	c.publish(ss, apitypes.Event{Type: apitypes.EventRun, Run: &run})
}

// interruptRuns ends the runs a restart cut short, at start: their
// containers were removed with the daemon (or, after a crash, by the
// runtime's start-up sweep of its stranded jobs, docker's
// UnpauseStranded), and each is audited as it would have been.
func (c *Core) interruptRuns(ctx context.Context) error {
	rows, err := c.store.ListRunningServiceRuns(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		ss, err := c.scopeOf(ctx, row.ServiceID)
		if err != nil {
			return err
		}
		rel, err := c.runRelease(ctx, row)
		if err != nil {
			return err
		}
		a := Actor{Kind: row.ActorKind, Name: row.Actor, OrgID: ss.project.OrgID}
		if row.UserID != nil {
			a.UserID = *row.UserID
		}
		if row.TokenID != nil {
			a.TokenID = *row.TokenID
		}
		r := &Run{c: c, actor: a, ss: ss, rel: rel, row: row, release: func() {}}
		r.finish(ctx, apitypes.RunInterrupted, nil, "the control plane restarted while it ran; run it again", "")
	}
	return nil
}
