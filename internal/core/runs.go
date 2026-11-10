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
	// runSnapshotNote caps the command the note of the snapshot taken
	// before a run quotes.
	runSnapshotNote = 100
)

// runSecond is a second of a run's timeout. A variable so a test can time
// a run out without waiting a second.
var runSecond = time.Second

// runState counts the runs in flight per service (by ID); the ones with
// their volumes mounted for writing are in volumeWriters. Entries go when
// their runs end.
type runState struct {
	mu      sync.Mutex
	running map[int64]int
	// ended holds each run in flight's channel, closed when it ends, by
	// run ID, for WaitRun.
	ended map[int64]chan struct{}
}

// Run is a command checked and recorded, ready to run once: Wait runs
// it, Abandon gives it up unrun. OpenRun makes one, and the job
// scheduler for a job's runs (job set).
type Run struct {
	c       *Core
	actor   Actor
	ss      svcScope
	rel     store.Release
	cfg     releaseConfig
	row     store.ServiceRun
	job     *store.ServiceJob
	once    sync.Once
	release func()
}

// runSpec is what a run runs: a command's or a job's.
type runSpec struct {
	command string
	timeout int // seconds, checked
	volumes apitypes.RunVolumes
	tty     bool
	trigger apitypes.RunTrigger
	job     *store.ServiceJob
	// noSnapshot skips the snapshot before a command that writes the
	// volumes (--no-snapshot); a job's runs never take one.
	noSnapshot bool
}

// OpenRun checks that the actor may run req's command in the service
// (scope deploy on its environment), checks the command, the volumes and
// the service's limit of concurrent runs, and records the run as running.
// A run mounting the volumes for writing first waits for a snapshot of
// them (ADR 0047) unless req.NoSnapshot; a failed snapshot refuses the
// run. tty marks a terminal session, whose output isn't kept.
func (c *Core) OpenRun(ctx context.Context, a Actor, ref ServiceRef, req apitypes.RunCommandRequest, tty bool) (*Run, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "running a command")
	if err != nil {
		return nil, err
	}
	cmd, err := checkCommand(req.Command)
	if err != nil {
		return nil, err
	}
	timeout, err := checkTimeout(req.TimeoutSeconds)
	if err != nil {
		return nil, err
	}
	return c.openRun(ctx, a, ss, runSpec{command: cmd, timeout: timeout, volumes: req.Volumes, tty: tty, trigger: apitypes.TriggerCommand, noSnapshot: req.NoSnapshot})
}

// checkCommand checks a command to run and returns it trimmed.
func checkCommand(cmd string) (string, error) {
	cmd = strings.TrimSpace(cmd)
	switch {
	case cmd == "":
		return "", invalid("give a command to run, e.g. `dock run -- ls`")
	case len(cmd) > apitypes.MaxCommandBytes:
		return "", invalid("the command is %d bytes; at most %d", len(cmd), apitypes.MaxCommandBytes)
	case !utf8.ValidString(cmd) || strings.ContainsRune(cmd, 0):
		return "", invalid("the command must be UTF-8 text without NUL bytes")
	}
	return cmd, nil
}

// checkTimeout checks a run's timeout in seconds; 0 is the default.
func checkTimeout(timeout int) (int, error) {
	switch {
	case timeout == 0:
		return apitypes.DefaultRunTimeoutSeconds, nil
	case timeout < 0 || timeout > apitypes.MaxRunTimeoutSeconds:
		return 0, invalid("timeout must be between 1 and %d seconds", apitypes.MaxRunTimeoutSeconds)
	}
	return timeout, nil
}

// openRun records a checked run in a service as running, once it has
// an active release, its volumes are decided and a run slot is free.
func (c *Core) openRun(ctx context.Context, a Actor, ss svcScope, spec runSpec) (*Run, error) {
	// Writes after a move's fence would be lost; before it the copy
	// catches them.
	if err := c.moveHolds(ctx, ss.envScope, "run a command"); err != nil {
		return nil, err
	}
	if ss.svc.ArchivedAt != nil {
		return nil, invalid("service %s is archived; restore it to run commands in it", ss.svc.Name)
	}
	// A run is a request to use the environment: an asleep one wakes,
	// and it stays awake while the run lasts.
	c.wakeFor(ctx, ss.envScope, a.Name)
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
	volumes, err := runVolumes(ss, cfg, spec.volumes)
	if err != nil {
		return nil, err
	}
	endJob := func() {}
	if spec.job != nil {
		if endJob, err = c.holdJob(*spec.job); err != nil {
			return nil, err
		}
	}
	endRun, err := c.holdRun(ss, volumes)
	if err != nil {
		endJob()
		return nil, err
	}
	endAwake := c.holdAwake(ss.env.ID)
	release := func() { endRun(); endJob(); endAwake() }
	var snapshot *int64
	if spec.job == nil && volumes == apitypes.VolumesWrite && !spec.noSnapshot {
		if snapshot, err = c.snapshotBeforeRun(ctx, a, ss, spec.command); err != nil {
			release()
			return nil, err
		}
	}
	params := store.InsertServiceRunParams{
		ServiceID: ss.svc.ID, ReleaseID: &rel.ID, Trigger: string(spec.trigger), Command: spec.command, Tty: spec.tty,
		Volumes: string(volumes), TimeoutSeconds: int64(spec.timeout), Actor: a.Name, ActorKind: a.Kind, StartedAt: now(),
		SnapshotID: snapshot,
	}
	if spec.job != nil {
		params.JobID = &spec.job.ID
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
	ended := make(chan struct{})
	c.runs.mu.Lock()
	c.runs.ended[row.ID] = ended
	c.runs.mu.Unlock()
	hold := release
	release = func() {
		hold()
		c.runs.mu.Lock()
		if ch, ok := c.runs.ended[row.ID]; ok {
			close(ch)
			delete(c.runs.ended, row.ID)
		}
		c.runs.mu.Unlock()
	}
	r := &Run{c: c, actor: a, ss: ss, rel: rel, cfg: cfg, row: row, job: spec.job, release: release}
	c.publishRun(ss, rel, row, r.jobName())
	return r, nil
}

// jobName is the run's job's name, "" for a command.
func (r *Run) jobName() string {
	if r.job == nil {
		return ""
	}
	return r.job.Name
}

// snapshotBeforeRun snapshots the service's volumes before a run that
// mounts them for writing and returns the snapshot's ID. Its write slot
// (holdRun) is held, so no other run writes meanwhile. A failure is the
// run's error, saying --no-snapshot skips it.
func (c *Core) snapshotBeforeRun(ctx context.Context, a Actor, ss svcScope, cmd string) (*int64, error) {
	snap, err := c.SnapshotBefore(ctx, a, ss, apitypes.SnapshotBeforeRun, "before `dock run`: "+cleanLine(cmd, runSnapshotNote))
	if err != nil {
		code := CodeOf(err)
		if code == apitypes.CodeInternal {
			code = apitypes.CodeConflict
		}
		return nil, errf(code, "the snapshot of %s's volumes before the run failed, so the command didn't run: %v; fix that, or run it without a snapshot (--no-snapshot)", ss.svc.Name, err)
	}
	if snap == nil {
		return nil, nil
	}
	return &snap.ID, nil
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
// for a run mounting volumes for writing, holdWritingRun); the returned
// func gives them back.
func (c *Core) holdRun(ss svcScope, volumes apitypes.RunVolumes) (func(), error) {
	id := ss.svc.ID
	c.runs.mu.Lock()
	defer c.runs.mu.Unlock()
	if c.runs.running[id] >= maxRunsPerService {
		return nil, conflict("%s already runs %d commands, the most at once; wait for one to end", ss.svc.Name, maxRunsPerService)
	}
	endWrite := func() {}
	if volumes == apitypes.VolumesWrite {
		var err error
		if endWrite, err = c.holdWritingRun(ss); err != nil {
			return nil, err
		}
	}
	c.runs.running[id]++
	var once sync.Once
	return func() {
		once.Do(func() {
			endWrite()
			c.runs.mu.Lock()
			defer c.runs.mu.Unlock()
			if c.runs.running[id]--; c.runs.running[id] <= 0 {
				delete(c.runs.running, id)
			}
		})
	}, nil
}

// Info is the run as it was recorded.
func (r *Run) Info() apitypes.CommandRun {
	out := r.c.toRun(r.ss, r.rel, r.row)
	out.Job = r.jobName()
	return out
}

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
	timeout := time.Duration(r.row.TimeoutSeconds) * runSecond
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
	cause := ""
	if status == apitypes.RunFailed && exit != nil && !r.row.Tty {
		// Why, by the deploy diagnosis's rules (ADR 0071); a terminal
		// session keeps no output to judge.
		cause = runCause(r.row.Command, r.cfg, *exit, output)
	}
	if err := c.store.FinishServiceRun(ctx, store.FinishServiceRunParams{
		Status: string(status), ExitCode: code, Error: msg, Output: output, Cause: cause, FinishedAt: &at, ID: r.row.ID,
	}); err != nil {
		c.log.Error("record the end of a run", "run", r.row.ID, "err", err)
	}
	r.row.Status, r.row.ExitCode, r.row.Error, r.row.Output, r.row.Cause, r.row.FinishedAt = string(status), code, msg, output, cause, &at
	// The run's slots go before its end is announced: a client that saw
	// it end may act on the job at once (remove it, run it again).
	r.release()
	if r.job != nil {
		// A job's run is its history, not the audit log's (a job every
		// minute would bury it); running one now is audited as job.run.
		c.trimJobRuns(ctx, r.job.ID)
		c.publishRun(r.ss, r.rel, r.row, r.job.Name)
		c.jobEnded(r.ss, *r.job, r.row)
	} else {
		if err := c.store.TrimServiceRuns(ctx, store.TrimServiceRunsParams{ServiceID: r.ss.svc.ID, Keep: keepRuns}); err != nil {
			c.log.Error("trim run history", "service", r.ss.String(), "err", err)
		}
		c.audit(ctx, r.actor, "service.run", &r.ss.project, &r.ss.env, r.ss.String(), runSummary(r.row))
		c.publishRun(r.ss, r.rel, r.row, "")
	}
	out := c.toRun(r.ss, r.rel, r.row)
	out.Job = r.jobName()
	return out
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

// WaitRun waits until one of a service's runs ended, at most wait, and
// returns it as GetRun does (scope deploy): ended, or still running when
// the wait is over. Nothing polls: the run closes its channel as it ends.
func (c *Core) WaitRun(ctx context.Context, a Actor, ref ServiceRef, id int64, wait time.Duration) (apitypes.CommandRun, error) {
	if _, err := c.GetRun(ctx, a, ref, id); err != nil {
		return apitypes.CommandRun{}, err
	}
	c.runs.mu.Lock()
	ended, running := c.runs.ended[id]
	c.runs.mu.Unlock()
	if running {
		t := time.NewTimer(wait)
		select {
		case <-ended:
		case <-t.C:
		case <-ctx.Done():
		}
		t.Stop()
	}
	return c.GetRun(ctx, a, ref, id)
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
			Volumes: row.Volumes, TimeoutSeconds: row.TimeoutSeconds, Status: row.Status, ExitCode: row.ExitCode, Error: row.Error, Cause: row.Cause,
			Actor: row.Actor, ActorKind: row.ActorKind, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, SnapshotID: row.SnapshotID,
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
	out := c.toRun(ss, rel, row)
	if row.JobID != nil {
		if job, err := c.store.GetServiceJobByID(ctx, *row.JobID); err == nil {
			out.Job = job.Name
		}
	}
	return out, nil
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
		Error: row.Error, Cause: row.Cause, Output: row.Output, CreatedBy: row.Actor, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
		Snapshot: deref(row.SnapshotID),
	}
	if row.ExitCode != nil {
		out.ExitCode = ptr(int(*row.ExitCode))
	}
	return out
}

// publishRun sends a run event, without the run's output: that is read
// with deploy (GetRun), and events go to everyone who may read the
// service.
func (c *Core) publishRun(ss svcScope, rel store.Release, row store.ServiceRun, job string) {
	run := c.toRun(ss, rel, row)
	run.Output, run.Job = "", job
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
		if row.JobID != nil {
			job, err := c.store.GetServiceJobByID(ctx, *row.JobID)
			if err != nil {
				return err
			}
			r.job = &job
		}
		r.finish(ctx, apitypes.RunInterrupted, nil, "the control plane restarted while it ran; run it again", "")
	}
	return nil
}
