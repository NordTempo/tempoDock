package core

import (
	"errors"
	"slices"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
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
