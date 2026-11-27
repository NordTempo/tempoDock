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

// TestExec runs a session over the link: input comes back as output, the
// exit code is the node's, labels that don't match are ErrNotFound, and
// canceling ends it on the node. A node before ExecProtocol is told to
// upgrade.
func TestExec(t *testing.T) {
	h := newHarness(t)
	ctr := h.startService("web-1")
	ctx := context.Background()
	want := node.Labels{Service: "3", Release: "7"}

	var out syncBuffer
	in := strings.NewReader("hello")
	code, err := h.client.Exec(ctx, node.ExecRequest{ContainerID: ctr.ID, Labels: want, Cmd: []string{"cat"}, Session: "s1", Stream: &node.JobStream{Stdout: &out, Stdin: in}})
	if err != nil || code != 0 || out.String() != "hello" {
		t.Fatalf("Exec cat = %d, %v, output %q", code, err, out.String())
	}
	if code, err := h.client.Exec(ctx, node.ExecRequest{ContainerID: ctr.ID, Labels: want, Cmd: []string{"exit", "3"}, Session: "s2", Stream: &node.JobStream{Stdout: io.Discard}}); err != nil || code != 3 {
		t.Fatalf("Exec exit 3 = %d, %v", code, err)
	}
	other := node.Labels{Service: "4"}
	if _, err := h.client.Exec(ctx, node.ExecRequest{ContainerID: ctr.ID, Labels: other, Cmd: []string{"ls"}, Session: "s3", Stream: &node.JobStream{Stdout: io.Discard}}); !errors.Is(err, node.ErrNotFound) {
		t.Fatalf("another service's labels = %v, want ErrNotFound", err)
	}

	open, inW := io.Pipe()
	defer func() { _ = inW.Close() }()
	cctx, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	if _, err := h.client.Exec(cctx, node.ExecRequest{ContainerID: ctr.ID, Labels: want, Cmd: []string{"sh"}, Session: "s4", Stream: &node.JobStream{Stdout: io.Discard, Stdin: open}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Exec after cancel = %v", err)
	}

	h.stopNode()
	h.startNode(func(n *Node) { n.protocol = ExecProtocol - 1 })
	waitFor(t, "the older node", func() bool { return h.hub.State(h.id).Hello.Protocol == ExecProtocol-1 && h.client.Online() })
	if _, err := h.client.Exec(ctx, node.ExecRequest{ContainerID: ctr.ID, Labels: want, Cmd: []string{"ls"}, Session: "s5", Stream: &node.JobStream{Stdout: io.Discard}}); err == nil || !strings.Contains(err.Error(), "dock exec") {
		t.Fatalf("older node = %v", err)
	}
}
