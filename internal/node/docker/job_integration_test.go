//go:build integration

package docker

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

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
// and removes the job's container; another runtime's job keeps its pause.
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
		created, err := e.cli.ContainerCreate(ctx, jobOptions(spec, []string{id}, owner))
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
