package fake

import (
	"context"
	"errors"
	"net"
	"slices"
	"testing"

	"github.com/NordTempo/tempoDock/internal/node"
)

func TestDial(t *testing.T) {
	ctx := context.Background()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	r := New()
	r.DialTo(ln.Addr().String())
	c, err := r.Start(ctx, node.ContainerSpec{Name: "db", Labels: node.Labels{Service: "1"}})
	if err != nil {
		t.Fatal(err)
	}

	conn, err := r.Dial(ctx, node.DialRequest{ContainerID: c.ID, Port: 5432})
	if err != nil {
		t.Fatalf("dial running service: %v", err)
	}
	_ = conn.Close()

	if _, err := r.Dial(ctx, node.DialRequest{ContainerID: "nope", Port: 5432}); !errors.Is(err, node.ErrNotFound) {
		t.Fatalf("dial unknown container: got %v, want ErrNotFound", err)
	}
	if err := r.Stop(ctx, c.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Dial(ctx, node.DialRequest{ContainerID: c.ID, Port: 5432}); err == nil {
		t.Fatal("dial stopped container: want an error")
	}
}

// A job's Pause shows the running containers paused to OnJob, and running
// again afterwards; an unknown ID fails before anything is paused.
func TestRunJobPause(t *testing.T) {
	ctx := context.Background()
	r := New()
	svc, err := r.Start(ctx, node.ContainerSpec{Name: "db", Labels: node.Labels{Service: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	var during node.ContainerState
	r.OnJob(func(node.JobSpec) (node.JobResult, error) {
		c, err := r.Inspect(ctx, svc.ID)
		during = c.State
		return node.JobResult{}, err
	})
	if _, err := r.RunJob(ctx, node.JobSpec{Name: "snap", Pause: []string{svc.ID}}); err != nil {
		t.Fatal(err)
	}
	after, _ := r.Inspect(ctx, svc.ID)
	if during != node.StatePaused || after.State != node.StateRunning {
		t.Fatalf("state during job %s, after %s", during, after.State)
	}

	if _, err := r.RunJob(ctx, node.JobSpec{Name: "bad", Pause: []string{svc.ID, "nope"}}); !errors.Is(err, node.ErrNotFound) {
		t.Fatalf("unknown container: %v", err)
	}
	ops := r.Ops()
	want := []string{"start db", "job snap", "pause " + svc.ID, "unpause " + svc.ID, "job bad"}
	if !slices.Equal(ops, want) {
		t.Fatalf("ops = %q, want %q", ops, want)
	}
}
