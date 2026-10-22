package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
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
// input never sees the end of it.
func (r *Runtime) RunJob(ctx context.Context, spec node.JobSpec) (node.JobResult, error) {
	if err := r.checkDeps(ctx, node.ContainerSpec{Network: spec.Network, Mounts: spec.Mounts, Labels: spec.Labels}); err != nil {
		return node.JobResult{}, err
	}
	created, err := r.cli.ContainerCreate(ctx, jobOptions(spec))
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
		_, _ = r.cli.ContainerRemove(cctx, created.ID, client.ContainerRemoveOptions{Force: true})
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

// jobOptions translates a job into Docker's create request.
func jobOptions(spec node.JobSpec) client.ContainerCreateOptions {
	labels := spec.Labels.Map()
	labels[node.LabelRole] = node.RoleJob
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
