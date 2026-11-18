//go:build integration

package docker

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

// A job reads its input on stdin to the end. When reading the input
// fails, the job is killed before it sees the end of it: a script that
// would take the cut-short input for the whole never finishes.
func TestRunJobStdin(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, time.Minute)
	res, err := e.RunJob(ctx, node.JobSpec{
		Name: e.name("stdin"), Image: shellImage, Script: `wc -c; echo done`,
		Stdin: strings.NewReader(strings.Repeat("x", 1<<20)),
	})
	if err != nil || res.ExitCode != 0 || !strings.Contains(res.Output, "1048576") || !strings.Contains(res.Output, "done") {
		t.Fatalf("job = %+v, %v", res, err)
	}

	broken := io.MultiReader(strings.NewReader(strings.Repeat("y", 64<<10)), errReader{errors.New("disk on fire")})
	res, err = e.RunJob(ctx, node.JobSpec{
		Name: e.name("broken"), Image: shellImage, Script: `cat >/dev/null; echo finished`,
		Stdin: broken,
	})
	if err == nil || !strings.Contains(err.Error(), "disk on fire") || res.ExitCode == 0 || strings.Contains(res.Output, "finished") {
		t.Fatalf("broken input: %+v, %v", res, err)
	}
}

// A job's Pause freezes the running service containers for exactly its
// run, also when ctx ends mid-job. Another control plane's container is
// not found and nothing is paused.
func TestRunJobPause(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, 2*time.Minute)
	labels := node.Labels{ControlPlane: e.prefix, Project: "p", Environment: "e", Service: "s", Release: "1"}
	svc := e.start(node.ContainerSpec{Name: e.name("svc"), Image: shellImage, Cmd: "sleep 600", Labels: labels})
	jobLabels := node.Labels{ControlPlane: e.prefix}

	done := make(chan error, 1)
	go func() {
		res, err := e.RunJob(ctx, node.JobSpec{Name: e.name("snap"), Image: shellImage, Script: "sleep 2", Labels: jobLabels, Pause: []string{svc.ID}})
		if err == nil && res.ExitCode != 0 {
			err = errors.New(res.Output)
		}
		done <- err
	}()
	e.waitState(ctx, svc.ID, node.StatePaused)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	e.waitState(ctx, svc.ID, node.StateRunning)

	jctx, cancel := context.WithCancel(ctx)
	go func() {
		_, err := e.RunJob(jctx, node.JobSpec{Name: e.name("cut"), Image: shellImage, Script: "sleep 600", Labels: jobLabels, Pause: []string{svc.ID}})
		done <- err
	}()
	e.waitState(ctx, svc.ID, node.StatePaused)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled job: %v", err)
	}
	e.waitState(ctx, svc.ID, node.StateRunning)

	other := e.start(node.ContainerSpec{Name: e.name("other"), Image: shellImage, Cmd: "sleep 600", Labels: node.Labels{ControlPlane: "another"}})
	_, err := e.RunJob(ctx, node.JobSpec{Name: e.name("foreign"), Image: shellImage, Script: "true", Labels: jobLabels, Pause: []string{svc.ID, other.ID}})
	if !errors.Is(err, node.ErrNotFound) {
		t.Fatalf("foreign container: %v", err)
	}
	e.waitState(ctx, svc.ID, node.StateRunning)
}

// UnpauseStranded unpauses what this runtime's cut-short job left paused
// and removes this runtime's job containers with their anonymous volumes;
// another runtime's job keeps its pause.
func TestUnpauseStranded(t *testing.T) {
	e := newEnv(t)
	e.DataDir = t.TempDir()
	ctx := ctxTimeout(t, time.Minute)
	labels := node.Labels{ControlPlane: e.prefix, Project: "p", Environment: "e", Service: "s", Release: "1"}
	mine := e.start(node.ContainerSpec{Name: e.name("mine"), Image: shellImage, Cmd: "sleep 600", Labels: labels})
	theirs := e.start(node.ContainerSpec{Name: e.name("theirs"), Image: shellImage, Cmd: "sleep 600", Labels: labels})
	// What a crash leaves: a paused service and the job container
	// recording it.
	strand := func(name, owner, id string) string {
		spec := node.JobSpec{Name: e.name(name), Image: shellImage, Script: "sleep 600"}
		opts := jobOptions(spec, []string{id}, owner)
		opts.Config.Volumes = map[string]struct{}{"/scratch": {}} // an image's VOLUME
		created, err := e.cli.ContainerCreate(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.cli.ContainerPause(ctx, id, client.ContainerPauseOptions{}); err != nil {
			t.Fatal(err)
		}
		return created.ID
	}
	myJob := strand("job-mine", e.owner(), mine.ID)
	theirJob := strand("job-theirs", t.TempDir(), theirs.ID)
	// A cut-short job that pauses nothing (a pre-deploy command, a
	// `dock run`), still running.
	runSpec := node.JobSpec{Name: e.name("run-mine"), Image: shellImage, Script: "sleep 600"}
	runOpts := jobOptions(runSpec, nil, e.owner())
	runOpts.Config.Volumes = map[string]struct{}{"/scratch": {}}
	myRun, err := e.cli.ContainerCreate(ctx, runOpts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.cli.ContainerStart(ctx, myRun.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	anon := map[string]string{"paused": anonVolume(t, e, myJob, "/scratch"), "running": anonVolume(t, e, myRun.ID, "/scratch")}

	if n, err := e.UnpauseStranded(ctx); err != nil || n != 1 {
		t.Fatalf("UnpauseStranded = %d, %v", n, err)
	}
	e.waitState(ctx, mine.ID, node.StateRunning)
	if c, err := e.Inspect(ctx, theirs.ID); err != nil || c.State != node.StatePaused {
		t.Fatalf("other runtime's paused container: %+v, %v", c, err)
	}
	if _, err := e.cli.ContainerInspect(ctx, myJob, client.ContainerInspectOptions{}); err == nil {
		t.Error("the cut-short job's container is still there")
	}
	if _, err := e.cli.ContainerInspect(ctx, myRun.ID, client.ContainerInspectOptions{}); err == nil {
		t.Error("the cut-short running job's container is still there")
	}
	for job, vol := range anon {
		if _, err := e.cli.VolumeInspect(ctx, vol, client.VolumeInspectOptions{}); !cerrdefs.IsNotFound(err) {
			t.Errorf("the cut-short %s job's anonymous volume %s: %v, want it gone", job, vol, err)
			_, _ = e.cli.VolumeRemove(context.Background(), vol, client.VolumeRemoveOptions{})
		}
	}
	if _, err := e.cli.ContainerInspect(ctx, theirJob, client.ContainerInspectOptions{}); err != nil {
		t.Errorf("other runtime's job container: %v", err)
	}
	_, _ = e.cli.ContainerUnpause(ctx, theirs.ID, client.ContainerUnpauseOptions{})
}

// waitState waits until container id is in state want.
func (e *testEnv) waitState(ctx context.Context, id string, want node.ContainerState) {
	e.t.Helper()
	for {
		c, err := e.Inspect(ctx, id)
		if err != nil {
			e.t.Fatal(err)
		}
		if c.State == want {
			return
		}
		select {
		case <-ctx.Done():
			e.t.Fatalf("container %s is %s, want %s", id, c.State, want)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// A streamed job's output arrives as it is written, stdout and stderr
// apart, its input reaches it, and with a terminal it sees one of the
// given size; the container is gone afterwards.
func TestRunJobStream(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, time.Minute)
	var stdout, stderr strings.Builder
	res, err := e.RunJob(ctx, node.JobSpec{
		Name: e.name("stream"), Image: shellImage, Script: `read line; echo "got $line"; echo oops >&2; exit 4`,
		Stream: &node.JobStream{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("hello\n")},
	})
	if err != nil || res.ExitCode != 4 || stdout.String() != "got hello\n" || stderr.String() != "oops\n" || !strings.Contains(res.Output, "got hello") {
		t.Fatalf("job = %+v, %v; stdout %q, stderr %q", res, err, stdout.String(), stderr.String())
	}

	var term strings.Builder
	res, err = e.RunJob(ctx, node.JobSpec{
		Name: e.name("tty"), Image: shellImage, Script: `stty size; [ -t 0 ] && echo tty`,
		Stream: &node.JobStream{Stdout: &term, TTY: true, Size: node.TermSize{Cols: 132, Rows: 43}},
	})
	if err != nil || res.ExitCode != 0 || !strings.Contains(term.String(), "43 132") || !strings.Contains(term.String(), "tty") {
		t.Fatalf("tty job = %+v, %v; output %q", res, err, term.String())
	}
	for _, name := range []string{e.name("stream"), e.name("tty")} {
		left, err := e.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("name", name)})
		if err != nil || len(left.Items) != 0 {
			t.Errorf("job container %s left behind: %v", name, err)
		}
	}
}

// A job printing tens of MB returns its last node.MaxJobOutput bytes.
func TestRunJobHugeOutput(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, time.Minute)
	res, err := e.RunJob(ctx, node.JobSpec{Name: e.name("huge"), Image: shellImage, Script: `head -c 40000000 /dev/zero | tr '\0' x; echo; echo the-end`})
	if err != nil || res.ExitCode != 0 || len(res.Output) != node.MaxJobOutput || !strings.HasSuffix(res.Output, "x\nthe-end\n") {
		t.Fatalf("job = %d bytes, exit %d, %v", len(res.Output), res.ExitCode, err)
	}
}

// A job without a network has none: only loopback, not Docker's default
// bridge. One with a network is on that network alone.
func TestRunJobNetwork(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, time.Minute)
	const ifaces = `ls /sys/class/net | tr '\n' ' '`
	res, err := e.RunJob(ctx, node.JobSpec{Name: e.name("nonet"), Image: shellImage, Script: ifaces})
	if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Output) != "lo" {
		t.Fatalf("a job without a network sees %q (exit %d, %v), want only lo", res.Output, res.ExitCode, err)
	}
	labels := node.Labels{ControlPlane: e.prefix}
	network := e.name("net")
	if err := e.EnsureNetwork(ctx, network, labels); err != nil {
		t.Fatal(err)
	}
	res, err = e.RunJob(ctx, node.JobSpec{Name: e.name("net"), Image: shellImage, Script: ifaces, Network: network, Labels: labels})
	if err != nil || res.ExitCode != 0 || len(strings.Fields(res.Output)) != 2 {
		t.Fatalf("a job on a network sees %q (exit %d, %v), want lo and one interface", res.Output, res.ExitCode, err)
	}
}

// Jobs and app containers run under the runtime's limit on processes.
func TestPidsLimitApplied(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, time.Minute)
	e.PidsLimit = 321
	res, err := e.RunJob(ctx, node.JobSpec{Name: e.name("pids"), Image: shellImage, Script: `cat /sys/fs/cgroup/pids.max 2>/dev/null || cat /sys/fs/cgroup/pids/pids.max`})
	if err != nil || strings.TrimSpace(res.Output) != "321" {
		t.Fatalf("a job's pids.max = %q (%v), want 321", res.Output, err)
	}
	svc := e.start(node.ContainerSpec{Name: e.name("svc"), Image: shellImage, Cmd: "sleep 600", Labels: node.Labels{ControlPlane: e.prefix}})
	got, err := e.cli.ContainerInspect(ctx, svc.ID, client.ContainerInspectOptions{})
	if err != nil || got.Container.HostConfig.PidsLimit == nil || *got.Container.HostConfig.PidsLimit != 321 {
		t.Fatalf("an app container's pids limit = %v (%v), want 321", got.Container.HostConfig, err)
	}
}
