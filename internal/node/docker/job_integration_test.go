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

// A volume streams out of one job with busybox tar and into another, as
// an environment move copies it (ADR 0053): owner, a 0700 root (Postgres
// refuses anything else), a symlink and hidden files come back as they
// were, and the target's old files are gone. Busybox tar doesn't lchown,
// so a symlink itself comes back owned by root (what it points to keeps
// its owner): nothing Dock runs checks a symlink's own owner.
func TestRunJobCopiesAVolume(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, 2*time.Minute)
	src, dst := e.name("src"), e.name("dst")
	for _, v := range []string{src, dst} {
		if err := e.EnsureVolume(ctx, v, node.Labels{Project: "p1"}); err != nil {
			t.Fatal(err)
		}
	}
	run := func(name, script, vol string, in io.Reader, out io.Writer) string {
		t.Helper()
		spec := node.JobSpec{Name: e.name(name), Image: shellImage, Script: script, Mounts: []node.Mount{{Volume: vol, Path: "/v"}}}
		var tail strings.Builder
		if in != nil || out != nil {
			spec.Stream = &node.JobStream{Stdin: in, Stdout: cmpWriter(out, &tail), Stderr: &tail}
		}
		res, err := e.RunJob(ctx, spec)
		if spec.Stream != nil {
			res.Output = tail.String()
		}
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("%s: %+v, %v", name, res, err)
		}
		return res.Output
	}
	run("fill", "mkdir -p /v/base && head -c 3000000 /dev/urandom > /v/base/big && echo x > /v/.hidden && "+
		"ln -s base/big /v/link && chown -R 999:999 /v && chmod 700 /v", src, nil, nil)
	run("old", "touch /v/stale", dst, nil, nil)
	const list = "cd /v && find . | sort | while read f; do if [ -L \"$f\" ]; then stat -c '%n %a %F %N' \"$f\"; else stat -c '%n %u:%g %a %F %s' \"$f\"; fi; done"
	want := run("list-src", list, src, nil, nil)

	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := e.RunJob(ctx, node.JobSpec{
			Name: e.name("export"), Image: shellImage, Script: "tar -cf - -C /v .",
			Mounts: []node.Mount{{Volume: src, Path: "/v", ReadOnly: true}}, Stream: &node.JobStream{Stdout: pw, Stderr: io.Discard},
		})
		pw.CloseWithError(err)
		done <- err
	}()
	run("import", "rm -rf /v/* /v/.[!.]* /v/..?*; tar -xf - -C /v", dst, pr, nil)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := run("list-dst", list, dst, nil, nil); got != want {
		t.Fatalf("copy differs:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(want, ". 999:999 700 directory") || !strings.Contains(want, "symbolic link") {
		t.Fatalf("source isn't what the test meant to copy:\n%s", want)
	}
}

// cmpWriter is w, or def when w is nil.
func cmpWriter(w, def io.Writer) io.Writer {
	if w == nil {
		return def
	}
	return w
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
	// A cut-short job that pauses nothing (a pre-deploy command, a
	// `dock run`), still running.
	runSpec := node.JobSpec{Name: e.name("run-mine"), Image: shellImage, Script: "sleep 600"}
	myRun, err := e.cli.ContainerCreate(ctx, jobOptions(runSpec, nil, e.owner()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.cli.ContainerStart(ctx, myRun.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}

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
