package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

// RunJob implements node.Containers: create the job container (restart
// policy no, role label RoleJob), start it, wait for it to exit, read the
// tail of its output and remove it. The network and volumes must be managed,
// as for Start. Whatever happens, including ctx ending, the container is
// force-removed before RunJob returns. With spec.Stdin the job gets it on
// standard input (attached before the start); the container is removed
// before that connection closes, so a job that didn't get all of its
// input never sees the end of it. spec.Pause's running containers are
// paused right before the start and unpaused before the job container is
// removed; an unpause that fails is part of the returned error. The job
// container records them (labelPauses) for UnpauseStranded.
func (r *Runtime) RunJob(ctx context.Context, spec node.JobSpec) (_ node.JobResult, err error) {
	if err := r.checkDeps(ctx, node.ContainerSpec{Network: spec.Network, Mounts: spec.Mounts, Labels: spec.Labels}); err != nil {
		return node.JobResult{}, err
	}
	pause, err := r.pauseTargets(ctx, spec)
	if err != nil {
		return node.JobResult{}, err
	}
	created, err := r.cli.ContainerCreate(ctx, jobOptions(spec, pause, r.owner()))
	if cerrdefs.IsConflict(err) {
		return node.JobResult{}, fmt.Errorf("container name %q is already in use", spec.Name)
	}
	if err != nil {
		return node.JobResult{}, fmt.Errorf("create job %s: %w", spec.Name, err)
	}
	var attached *client.HijackedResponse
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		// With its anonymous volumes (see Remove): a job of a Postgres
		// image would leave one per run.
		_, _ = r.cli.ContainerRemove(cctx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		if attached != nil {
			attached.Close()
		}
	}()
	if spec.Stdin != nil {
		att, err := r.cli.ContainerAttach(ctx, created.ID, client.ContainerAttachOptions{Stream: true, Stdin: true})
		if err != nil {
			return node.JobResult{}, fmt.Errorf("attach to job %s: %w", spec.Name, err)
		}
		attached = &att.HijackedResponse
	}
	// Subscribe before starting, so a job that exits at once isn't missed.
	wait := r.cli.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if err := r.pause(ctx, spec.Name, pause); err != nil {
		return node.JobResult{}, err
	}
	// Deferred after the removal, so it runs before it: the job container
	// keeps the record of what is paused until nothing is.
	defer func() { err = errors.Join(err, r.unpause(ctx, pause)) }()
	if _, err := r.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return node.JobResult{}, fmt.Errorf("start job %s: %w", spec.Name, err)
	}
	var input <-chan error
	if attached != nil {
		input = r.feedJob(created.ID, attached, spec.Stdin)
	}
	var res node.JobResult
	select {
	case w := <-wait.Result:
		res.ExitCode = int(w.StatusCode)
		if w.Error != nil && w.Error.Message != "" {
			return res, fmt.Errorf("job %s: %s", spec.Name, w.Error.Message)
		}
	case err := <-wait.Error:
		return res, fmt.Errorf("wait for job %s: %w", spec.Name, err)
	case <-ctx.Done():
		return res, ctx.Err()
	}
	out, err := r.jobOutput(ctx, created.ID)
	res.Output = out
	if err != nil || input == nil {
		return res, err
	}
	// A job that exited 0 read its input to the end, so feeding is over
	// (or about to be); one that failed has its exit code to show.
	select {
	case ierr := <-input:
		return res, ierr
	case <-time.After(5 * time.Second):
		if res.ExitCode == 0 {
			return res, fmt.Errorf("job %s ended before reading all of its input", spec.Name)
		}
		return res, nil
	}
}

// feedJob copies in to a started job's standard input and closes it. If
// reading in fails, the job is killed first, so it never takes what it
// got for all of its input; the error comes back on the channel. A job
// that stops reading (it exited) ends the copy without an error.
func (r *Runtime) feedJob(id string, att *client.HijackedResponse, in io.Reader) <-chan error {
	done := make(chan error, 1)
	go func() {
		src := &inputReader{r: in}
		_, err := io.Copy(att.Conn, src)
		switch {
		case src.err != nil:
			kctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, _ = r.cli.ContainerKill(kctx, id, client.ContainerKillOptions{})
			cancel()
			done <- fmt.Errorf("job input: %w", src.err)
		case err == nil:
			done <- att.CloseWrite()
		default:
			done <- nil
		}
	}()
	return done
}

// inputReader remembers an error reading a job's input, to tell it from
// the job going away while it is written to.
type inputReader struct {
	r   io.Reader
	err error
}

func (ir *inputReader) Read(p []byte) (int, error) {
	n, err := ir.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		ir.err = err
	}
	return n, err
}

// labelPauses is on a job container that pauses services: their IDs,
// comma-separated, so that UnpauseStranded knows what a job of a crashed
// runtime left paused.
const labelPauses = "dock.pauses"

// jobOptions translates a job into Docker's create request. pause are the
// containers the job will pause and owner the runtime's (Runtime.owner),
// recorded in labels when there are any.
func jobOptions(spec node.JobSpec, pause []string, owner string) client.ContainerCreateOptions {
	labels := spec.Labels.Map()
	labels[node.LabelRole] = node.RoleJob
	if len(pause) > 0 {
		labels[labelPauses] = strings.Join(pause, ",")
		labels[node.LabelOwner] = owner
	}
	cfg := &container.Config{
		Image:      spec.Image,
		Labels:     labels,
		Env:        envList(spec.Env),
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{spec.Script},
		// Input arrives through an attach; its end closes stdin.
		OpenStdin: spec.Stdin != nil,
		StdinOnce: spec.Stdin != nil,
	}
	host := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
		LogConfig:     container.LogConfig{Type: "local", Config: map[string]string{"max-size": "10m", "max-file": "2"}},
		Resources: container.Resources{
			Memory:     spec.Resources.MemoryBytes,
			MemorySwap: NoSwap(spec.Resources.MemoryBytes),
			NanoCPUs:   spec.Resources.NanoCPUs,
		},
		SecurityOpt: []string{"no-new-privileges"},
	}
	for _, m := range spec.Mounts {
		host.Mounts = append(host.Mounts, volumeMount(m))
	}
	opts := client.ContainerCreateOptions{Name: spec.Name, Config: cfg, HostConfig: host}
	if spec.Network != "" {
		opts.NetworkingConfig = networking(spec.Network, nil)
	}
	return opts
}

// jobOutput reads a finished job's combined output, keeping the last
// node.MaxJobOutput bytes.
func (r *Runtime) jobOutput(ctx context.Context, id string) (string, error) {
	rc, err := r.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return "", fmt.Errorf("job output: %w", err)
	}
	defer func() { _ = rc.Close() }()
	var tail tailBuffer
	if _, err := stdcopy.StdCopy(&tail, &tail, rc); err != nil && !errors.Is(err, io.EOF) {
		return tail.String(), fmt.Errorf("job output: %w", err)
	}
	return tail.String(), nil
}

// tailBuffer keeps the last node.MaxJobOutput bytes written to it.
type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if over := len(t.b) - node.MaxJobOutput; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.b) }

// pauseTargets checks spec.Pause and returns the IDs of those that are
// running, the ones RunJob pauses. Each must be an app container of
// spec's control plane, or it is not found; nothing is paused yet.
func (r *Runtime) pauseTargets(ctx context.Context, spec node.JobSpec) ([]string, error) {
	var out []string
	for _, id := range spec.Pause {
		c, err := r.inspect(ctx, id)
		if err != nil {
			return nil, err
		}
		if !node.LabelsFrom(c.Config.Labels).Matches(node.Labels{ControlPlane: spec.Labels.ControlPlane}) {
			return nil, notFound("container", id)
		}
		// Paused already is someone else's pause, to keep; stopped
		// containers write nothing.
		if c.State != nil && c.State.Running && !c.State.Paused && !slices.Contains(out, c.ID) {
			out = append(out, c.ID)
		}
	}
	return out, nil
}

// pause freezes ids for job, in order. If one fails, it and those before
// it are unpaused again (a pause cut short by ctx may have happened all
// the same) and the error says which one.
func (r *Runtime) pause(ctx context.Context, job string, ids []string) error {
	for i, id := range ids {
		if _, err := r.cli.ContainerPause(ctx, id, client.ContainerPauseOptions{}); err != nil {
			return errors.Join(fmt.Errorf("pause container %s for job %s: %w", id, job, err), r.unpause(ctx, ids[:i+1]))
		}
	}
	return nil
}

// unpause thaws ids, all of them even if one fails, on a context of its
// own: a frozen service must not outlive a cancelled job. A container
// gone or no longer paused is fine.
func (r *Runtime) unpause(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	var errs []error
	for _, id := range ids {
		_, err := r.cli.ContainerUnpause(ctx, id, client.ContainerUnpauseOptions{})
		if err != nil && !cerrdefs.IsNotFound(err) && !cerrdefs.IsConflict(err) {
			errs = append(errs, fmt.Errorf("unpause container %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// owner is the runtime's node.LabelOwner: its data dir, absolute, as the
// CLI gives BuildKit's. Without a data dir (tests) it is empty.
func (r *Runtime) owner() string {
	if r.DataDir == "" {
		return ""
	}
	if abs, err := filepath.Abs(r.DataDir); err == nil {
		return abs
	}
	return r.DataDir
}

// UnpauseStranded unpauses what jobs of this runtime left paused when it
// stopped without ending them (a crash, a kill, the server losing power),
// and removes those jobs' containers. Dock only pauses a service for the
// length of one job (JobSpec.Pause, ADR 0047), so on start-up no job of
// this runtime runs and anything its old jobs paused would otherwise stay
// frozen for good. Its jobs are told by their node.LabelOwner, so another
// runtime's running job on a shared Docker host keeps its pause. Call it
// once on start-up, before any job runs; it costs one list call when no
// job was cut short, and returns how many containers it unpaused.
func (r *Runtime) UnpauseStranded(ctx context.Context) (int, error) {
	f := managedFilter().Add("label", node.LabelRole+"="+node.RoleJob, labelPauses)
	res, err := r.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return 0, fmt.Errorf("list job containers: %w", err)
	}
	owner, n := r.owner(), 0
	for _, job := range res.Items {
		if job.Labels[node.LabelOwner] != owner {
			continue
		}
		var failed bool
		for _, id := range strings.Split(job.Labels[labelPauses], ",") {
			_, err := r.cli.ContainerUnpause(ctx, id, client.ContainerUnpauseOptions{})
			switch {
			case err == nil:
				n++
				slog.Warn("unpaused a container a cut-short job had paused", "container", id, "job", job.ID)
			case cerrdefs.IsNotFound(err) || cerrdefs.IsConflict(err):
				// Gone, or not paused.
			default:
				failed = true
				slog.Error("unpause a container a cut-short job had paused", "container", id, "job", job.ID, "err", err)
			}
		}
		// Kept while something may still be paused, so the next start
		// tries again.
		if !failed {
			if _, err := r.cli.ContainerRemove(ctx, job.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
				return n, fmt.Errorf("remove job container %s: %w", job.ID, err)
			}
		}
	}
	return n, nil
}
