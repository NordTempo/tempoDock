package core

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/fake"
	"github.com/NordTempo/tempoDock/internal/node/remote"
)

// statefulMidDeploy creates a stateful service db in env, deploys release
// 1 and leaves release 2 health checking on rt, its old container already
// removed.
func statefulMidDeploy(t *testing.T, h *harness, rt *fake.Runtime, env string) ServiceRef {
	t.Helper()
	db := ServiceRef{"shop", env, "db"}
	if env == "production" {
		db = h.service("db", apitypes.CreateServiceRequest{Port: 5432})
	} else if _, err := h.c.CreateService(h.ctx, testSystem, db.Env(), apitypes.CreateServiceRequest{Name: "db", Port: 5432}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.AddVolume(h.ctx, testSystem, db, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
		t.Fatal(err)
	}
	h.deploy(db, "postgres:17")
	rt.HoldHealthChecks() // never released: the crashed daemon's deploy ends with its context
	rel, err := h.c.DeployImage(h.ctx, testSystem, db, apitypes.DeployImageRequest{Image: "postgres:17.1"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(db, rel.Number, apitypes.ReleaseHealthChecking)
	return db
}

// A stateful release cut short while its server is offline starts again
// when the server is back, even if the daemon restarted in between.
func TestStatefulRestartSurvivesDaemonRestart(t *testing.T) {
	p, lab := labPool(t)
	db := statefulMidDeploy(t, p.harness, lab, "staging")

	p.setOffline("homelab", true)
	p.crash()   // release 2 fails; release 1 is to start when homelab is back
	p.restart() // and the daemon restarts before it is
	p.setOffline("homelab", false)
	p.c.NodeStatus(remote.State{ServerID: p.ids["homelab"], Server: "homelab", Status: remote.StatusOnline})

	eventually(t, "release 1 to run again, alone", func() bool { return slices.Equal(lab.Running(), []string{p.ctr(db, 1)}) })
	if ids, err := p.store.ListEnsureRunning(p.ctx, p.ids["homelab"]); err != nil || len(ids) != 0 {
		t.Fatalf("still to start: %v, %v", ids, err)
	}
}

// Recovery never starts a stateful release next to a leftover container
// it couldn't remove; the next start tries again.
func TestRecoveryNeverSharesAVolume(t *testing.T) {
	h := newHarness(t)
	db := statefulMidDeploy(t, h, h.rt, "production")

	h.rt.FailStop(errors.New("stuck"))
	h.crash()
	if got := h.rt.Running(); !slices.Equal(got, []string{h.ctr(db, 2)}) {
		t.Fatalf("running = %v, want only release 2's container", got)
	}

	h.rt.FailStop(nil)
	h.restart()
	h.waitRunning(h.ctr(db, 1))
}

// A push deploy cut short by a crash gets a failure commit status instead
// of staying pending.
func TestGitStatusOfInterruptedDeploy(t *testing.T) {
	h, gh := gitHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	token, on := "tok", true
	k := h.connect(web, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main", Dir: "apps/web", Token: &token, Status: &on})
	h.rt.HoldHealthChecks() // never released: the crashed daemon's deploy ends with its context
	if _, err := h.push(k, pushBody("refs/heads/main", shaA, "apps/web/server.js")); err != nil {
		t.Fatal(err)
	}
	h.waitReleases(web, 1)
	h.waitStatus(web, 1, apitypes.ReleaseHealthChecking)
	prefix := "aaaaaaa %s dock/shop/production/web https://dock.example.com/test/shop/production/web/releases/1"
	pending, failure := fmt.Sprintf(prefix, "pending"), fmt.Sprintf(prefix, "failure")
	eventually(t, "the pending status", func() bool { s, _, _ := gh.seen(); return slices.Equal(s, []string{pending}) })

	h.crash()
	eventually(t, "the failure status", func() bool { s, _, _ := gh.seen(); return slices.Equal(s, []string{pending, failure}) })
}

// A failed write of a release's failure is retried, so the service isn't
// left with a deploy in flight that refuses every other.
func TestFailedReleaseWriteIsRetried(t *testing.T) {
	h := newHarness(t)
	logged := make(chan struct{}, 1)
	h.opts.Logger = slog.New(slog.NewTextHandler(signalWriter{"mark release failed", logged}, nil))
	h.restart()
	web := h.service("web", apitypes.CreateServiceRequest{})
	db := h.store.DB
	if _, err := db.ExecContext(h.ctx, `CREATE TRIGGER no_fail BEFORE UPDATE ON releases WHEN NEW.status = 'failed'
		BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	h.rt.FailHealthCheck(func(node.ContainerSpec) error { return errors.New("not ready") })
	rel, err := h.c.DeployImage(h.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:1"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-logged:
	case <-time.After(5 * time.Second):
		t.Fatal("the failure was never written")
	}
	if _, err := db.ExecContext(h.ctx, `DROP TRIGGER no_fail`); err != nil {
		t.Fatal(err)
	}
	if r := h.wait(web, rel.Number); r.Status != apitypes.ReleaseFailed {
		t.Fatalf("release 1: %s", r.Status)
	}
	h.rt.FailHealthCheck(nil)
	if r := h.deploy(web, "app:2"); r.Status != apitypes.ReleaseActive {
		t.Fatalf("release 2: %s", r.Status)
	}
}

// signalWriter is a log destination that signals ch whenever a line
// contains match.
type signalWriter struct {
	match string
	ch    chan struct{}
}

func (w signalWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(w.match)) {
		select {
		case w.ch <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}
