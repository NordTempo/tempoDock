package remote

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// TestStreamJob runs a job with a Stream over the link: input reaches the
// job as it is sent, output and stderr come back as written, resizes
// arrive, and the exit code is the result's.
func TestStreamJob(t *testing.T) {
	h := newHarness(t)
	sizes := make(chan node.TermSize, 4)
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		st := spec.Stream
		if st == nil || !st.TTY || st.Size != (node.TermSize{Cols: 80, Rows: 24}) || spec.Script != "sh" {
			t.Errorf("job spec on the node = %+v, stream %+v", spec, st)
			return node.JobResult{}, nil
		}
		sizes <- <-st.Resize
		buf := make([]byte, 5)
		if _, err := io.ReadFull(st.Stdin, buf); err != nil {
			return node.JobResult{}, err
		}
		_, _ = io.WriteString(st.Stdout, "got "+string(buf)+"\n")
		_, _ = io.WriteString(st.Stderr, "warning\n")
		return node.JobResult{ExitCode: 3}, nil
	})
	in, inW := io.Pipe()
	defer func() { _ = inW.Close() }()
	resize := make(chan node.TermSize, 1)
	resize <- node.TermSize{Cols: 120, Rows: 40}
	var stdout, stderr syncBuffer
	done := make(chan struct{})
	var res node.JobResult
	var err error
	go func() {
		defer close(done)
		res, err = h.client.RunJob(context.Background(), node.JobSpec{Name: "run1", Image: "app", Script: "sh",
			Stream: &node.JobStream{Stdout: &stdout, Stderr: &stderr, Stdin: in, TTY: true, Size: node.TermSize{Cols: 80, Rows: 24}, Resize: resize}})
	}()
	if got := recv(t, sizes); got != (node.TermSize{Cols: 120, Rows: 40}) {
		t.Fatalf("resize on the node = %+v", got)
	}
	_, _ = io.WriteString(inW, "hello")
	<-done
	if err != nil || res.ExitCode != 3 {
		t.Fatalf("RunJob = %+v, %v", res, err)
	}
	if stdout.String() != "got hello\n" || stderr.String() != "warning\n" || !strings.Contains(res.Output, "got hello") {
		t.Fatalf("stdout %q, stderr %q, tail %q", stdout.String(), stderr.String(), res.Output)
	}
}

// TestStreamJobCancel: canceling the caller's ctx ends the job on the
// node.
func TestStreamJobCancel(t *testing.T) {
	h := newHarness(t)
	ended := make(chan error, 1)
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		// Blocks until the call is gone: the node then closes the input.
		_, err := io.Copy(io.Discard, spec.Stream.Stdin)
		ended <- err
		return node.JobResult{}, err
	})
	in, inW := io.Pipe()
	defer func() { _ = inW.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, err := h.client.RunJob(ctx, node.JobSpec{Name: "run2", Image: "app", Script: "sleep 1d", Stream: &node.JobStream{Stdin: in}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunJob after cancel = %v", err)
	}
	select {
	case err := <-ended:
		_ = err // any end will do: the job stopped
	case <-time.After(5 * time.Second):
		t.Fatal("the job kept running on the node after cancel")
	}
}
