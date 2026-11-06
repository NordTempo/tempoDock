package fake

import (
	"context"
	"errors"
	"net"
	"slices"
	"testing"
	"time"

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

// DOCK_FAKE_PENDING_DOMAINS keeps the hostnames it names waiting for DNS.
func TestPendingDomains(t *testing.T) {
	t.Setenv("DOCK_FAKE_PENDING_DOMAINS", "shop.example.com, Blog.example.com")
	r := New()
	for host, ok := range map[string]bool{"shop.example.com": false, "blog.example.com": false, "api.example.com": true} {
		got, err := r.CheckDomain(context.Background(), host)
		if err != nil || got.DNSOK != ok || got.CertificateOK != ok {
			t.Errorf("%s: %+v %v, want DNS ok %v", host, got, err, ok)
		}
	}
}

// With PassHealthAfter a started container's check passes like Docker's:
// healthy, with a health_status event; a failed check stays starting.
func TestPassHealthAfter(t *testing.T) {
	ctx := context.Background()
	r := New()
	r.PassHealthAfter(time.Millisecond)
	r.FailHealthCheck(func(s node.ContainerSpec) error {
		if s.Name == "bad" {
			return errors.New("500")
		}
		return nil
	})
	evs, err := r.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	good, _ := r.Start(ctx, node.ContainerSpec{Name: "good", Image: "app", Health: &node.LivenessCheck{}})
	bad, _ := r.Start(ctx, node.ContainerSpec{Name: "bad", Image: "app", Health: &node.LivenessCheck{}})
	for ev := range evs {
		if ev.Action == "health_status" {
			if ev.ContainerID != good.ID || ev.Health != node.HealthHealthy {
				t.Fatalf("event %+v", ev)
			}
			break
		}
	}
	time.Sleep(10 * time.Millisecond)
	if c, _ := r.Inspect(ctx, bad.ID); c.Health != node.HealthStarting {
		t.Fatalf("failed check: %q", c.Health)
	}
}
