//go:build integration

package docker

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// An exec runs in the running container with its exit code, stdout and
// stderr apart (or one terminal), and input. A session cut short leaves
// nothing running there. Another release's labels are not found, a
// stopped container is not running.
func TestExec(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, 2*time.Minute)
	labels := node.Labels{ControlPlane: e.prefix, Project: "p", Environment: "e", Service: "s", Release: "1"}
	svc := e.start(node.ContainerSpec{Name: e.name("svc"), Image: shellImage, Cmd: "sleep 600", Labels: labels})
	run := func(ctx context.Context, session string, st *node.JobStream, cmd ...string) (int, error) {
		return e.Exec(ctx, node.ExecRequest{ContainerID: svc.ID, Labels: labels, Cmd: cmd, Session: session, Stream: st})
	}

	var out, errOut strings.Builder
	code, err := run(ctx, "s1", &node.JobStream{Stdout: &out, Stderr: &errOut}, "sh", "-c", "echo out; echo err >&2; exit 4")
	if err != nil || code != 4 || out.String() != "out\n" || errOut.String() != "err\n" {
		t.Fatalf("exec = %d, %v, stdout %q, stderr %q", code, err, out.String(), errOut.String())
	}

	out.Reset()
	code, err = run(ctx, "s2", &node.JobStream{Stdout: &out, Stdin: strings.NewReader("echo hi-$((1+1))\nexit 2\n"), TTY: true, Size: node.TermSize{Cols: 100, Rows: 30}}, "sh")
	if err != nil || code != 2 || !strings.Contains(out.String(), "hi-2") {
		t.Fatalf("terminal exec = %d, %v, output %q", code, err, out.String())
	}

	// Cut short: its sleep (and the shell around it) is stopped.
	// Canceled once both sleeps run, which the session says.
	in, inW := io.Pipe()
	defer func() { _ = inW.Close() }()
	cut, cancel := context.WithCancel(ctx)
	ready := &markWriter{mark: "ready", seen: make(chan struct{})}
	go func() {
		select {
		case <-ready.seen:
			cancel()
		case <-ctx.Done():
		}
	}()
	if _, err := run(cut, "s3", &node.JobStream{Stdout: ready, Stdin: in, TTY: true}, "sh", "-c", "sleep 600 & echo ready; sleep 600"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled exec = %v", err)
	}
	out.Reset()
	find := `for p in /proc/[0-9]*; do tr '\0' '\n' < $p/environ 2>/dev/null | grep -qx ` + node.ExecSessionVar + `=s3 && echo left ${p#/proc/}; done; true`
	if _, err := run(ctx, "s4", &node.JobStream{Stdout: &out}, "sh", "-c", find); err != nil || out.String() != "" {
		t.Fatalf("after the session: %v, %q", err, out.String())
	}

	other := labels
	other.Release = "2"
	if _, err := e.Exec(ctx, node.ExecRequest{ContainerID: svc.ID, Labels: other, Cmd: []string{"true"}, Session: "s5", Stream: &node.JobStream{Stdout: io.Discard}}); !errors.Is(err, node.ErrNotFound) {
		t.Fatalf("another release's labels = %v", err)
	}
	if err := e.Stop(ctx, svc.ID, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := run(ctx, "s6", &node.JobStream{Stdout: io.Discard}, "true"); !errors.Is(err, node.ErrNotRunning) {
		t.Fatalf("a stopped container = %v", err)
	}
}

// markWriter closes seen once what was written to it holds mark.
type markWriter struct {
	mu   sync.Mutex
	buf  strings.Builder
	mark string
	seen chan struct{}
	once sync.Once
}

func (m *markWriter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buf.Write(p)
	if strings.Contains(m.buf.String(), m.mark) {
		m.once.Do(func() { close(m.seen) })
	}
	return len(p), nil
}
