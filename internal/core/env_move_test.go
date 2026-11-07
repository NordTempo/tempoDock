package core

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/fake"
)

// movePool is labPool with the control plane taking non-production too,
// and shop/staging on homelab running db (a volume holding data) and web
// (stateless, with a domain).
func movePool(t *testing.T) (p *pool, lab *fake.Runtime, db, web ServiceRef, data []byte) {
	t.Helper()
	p, lab = labPool(t)
	p.configure(p.ids["cp"], poolServer{"cp", true, true, 8})
	db = p.createService("staging", "db", apitypes.CreateServiceRequest{Port: 5432})
	if _, err := p.c.AddVolume(p.ctx, testSystem, db, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
		t.Fatal(err)
	}
	p.deploy(db, "postgres:17")
	web = p.createService("staging", "web", apitypes.CreateServiceRequest{Port: 8080})
	p.deploy(web, "app:1")
	if _, err := p.c.AddDomain(p.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	data = []byte("the database's files")
	lab.SetVolumeData(p.volume(db, "data"), data)
	lab.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if spec.Script == moveSizeScript {
			return node.JobResult{Output: "dock-move: kib 4\n"}, nil
		}
		return node.JobResult{}, fake.ErrDefault
	})
	return p, lab, db, web, data
}

// volume is the Docker name of a service's volume.
func (p *pool) volume(ref ServiceRef, name string) string {
	p.t.Helper()
	ss, err := p.c.serviceFor(p.ctx, testSystem, ref, apitypes.ScopeRead, "testing")
	if err != nil {
		p.t.Fatal(err)
	}
	return volumeName(ss, name)
}

// waitMove waits for env's move to end.
func (h *harness) waitMove(env EnvRef) apitypes.EnvironmentMove {
	h.t.Helper()
	var m apitypes.EnvironmentMove
	eventually(h.t, "the move to end", func() bool {
		var err error
		if m, err = h.c.EnvironmentMoveStatus(h.ctx, testSystem, env); err != nil {
			h.t.Fatal(err)
		}
		return m.Status == apitypes.MoveSucceeded || m.Status == apitypes.MoveFailed
	})
	return m
}

// envContainers lists rt's running containers of an environment, by
// name.
func envContainers(rt *fake.Runtime, prefix string) []string {
	return slices.DeleteFunc(rt.Running(), func(n string) bool { return !strings.HasPrefix(n, prefix) })
}

// assertMoved checks staging serves from cp with db's data, and nothing
// of it is left on homelab.
func assertMoved(t *testing.T, p *pool, lab *fake.Runtime, db, web ServiceRef, data []byte) {
	t.Helper()
	if s := p.server("shop", "staging"); s != p.ids2name("cp") {
		t.Fatalf("staging is on %s", s)
	}
	prefix := p.envPrefix(db)
	if got := envContainers(p.rt, prefix); len(got) != 2 {
		t.Errorf("running on cp: %v", got)
	}
	if got := envContainers(lab, prefix); len(got) != 0 {
		t.Errorf("left on homelab: %v", got)
	}
	if got := p.rt.VolumeData(p.volume(db, "data")); !bytes.Equal(got, data) {
		t.Errorf("db's data on cp = %q", got)
	}
	if !slices.ContainsFunc(p.rt.Routes(), func(r node.Route) bool { return slices.Contains(r.Hostnames, "shop.example.com") }) {
		t.Errorf("cp doesn't route shop.example.com: %+v", p.rt.Routes())
	}
	if slices.ContainsFunc(lab.Routes(), func(r node.Route) bool { return slices.Contains(r.Hostnames, "shop.example.com") }) {
		t.Error("homelab still routes shop.example.com")
	}
	// The next deploy happens on cp.
	if rel := p.deploy(web, "app:2"); rel.Status != apitypes.ReleaseActive || !slices.Contains(p.rt.Running(), p.ctr(web, 2)) {
		t.Errorf("deploy after the move: %s on %v", rel.Status, p.rt.Running())
	}
}

// assertStayed checks staging still serves from homelab, as before.
func assertStayed(t *testing.T, p *pool, lab *fake.Runtime, db ServiceRef, data []byte) {
	t.Helper()
	if s := p.server("shop", "staging"); s != "homelab" {
		t.Fatalf("staging is on %s", s)
	}
	prefix := p.envPrefix(db)
	if got := envContainers(lab, prefix); len(got) != 2 {
		t.Errorf("running on homelab: %v", got)
	}
	if got := envContainers(p.rt, prefix); len(got) != 0 {
		t.Errorf("left on cp: %v", got)
	}
	if got := lab.VolumeData(p.volume(db, "data")); !bytes.Equal(got, data) {
		t.Errorf("db's data on homelab = %q", got)
	}
}

// envPrefix is the container name prefix of ref's environment.
func (p *pool) envPrefix(ref ServiceRef) string {
	ss, _ := p.c.serviceFor(p.ctx, testSystem, ref, apitypes.ScopeRead, "testing")
	return fmt.Sprintf("dock_p%d_e%d_", ss.project.ID, ss.env.ID)
}

func TestMoveEnvironment(t *testing.T) {
	p, lab, db, web, data := movePool(t)
	staging := EnvRef{"shop", "staging"}
	m, err := p.c.MoveEnvironment(p.ctx, testSystem, staging, apitypes.MoveEnvironmentRequest{To: p.ids2name("cp")})
	if err != nil {
		t.Fatal(err)
	}
	if m.From != "homelab" || m.To != p.ids2name("cp") || len(m.Services) != 2 || !m.Services[0].Stateful || m.Services[0].Service != "db" {
		t.Fatalf("move = %+v", m)
	}
	m = p.waitMove(staging)
	if m.Status != apitypes.MoveSucceeded || m.Step != apitypes.MoveDone || m.DowntimeFrom == nil || m.DowntimeTo == nil {
		t.Fatalf("move = %+v", m)
	}
	if m.Services[0].Status != "healthy" || m.Services[0].CopiedBytes != int64(len(data)) {
		t.Errorf("db = %+v", m.Services[0])
	}
	notes := strings.Join(m.Notes, "\n")
	if !strings.Contains(notes, "shop.example.com") || !strings.Contains(notes, "docker volume rm "+p.volume(db, "data")) {
		t.Errorf("notes = %q", notes)
	}
	assertMoved(t, p, lab, db, web, data)
	// Its next environment of the kind goes where it went.
	if pl, err := p.c.ListPlacements(p.ctx, testSystem, "shop"); err != nil || !slices.Contains(pl, apitypes.Placement{Kind: apitypes.KindNonProduction, Server: p.ids2name("cp")}) {
		t.Errorf("placements = %+v, %v", pl, err)
	}
	if evs, err := p.c.ListAudit(p.ctx, testSystem, AuditFilter{Action: "environment.moved"}); err != nil || len(evs) != 1 {
		t.Errorf("audit = %+v, %v", evs, err)
	}
}

func TestMoveEnvironmentRefused(t *testing.T) {
	p, _, db, _, _ := movePool(t)
	staging := EnvRef{"shop", "staging"}
	move := func(a Actor, to string) error {
		_, err := p.c.MoveEnvironment(p.ctx, a, staging, apitypes.MoveEnvironmentRequest{To: to})
		return err
	}
	admin := p.token(testSystem, apitypes.CreateTokenRequest{Name: "admin", Scope: apitypes.ScopeAdmin})
	for _, c := range []struct {
		name string
		err  error
		want apitypes.ErrorCode
	}{
		{"not an operator", move(admin, p.ids2name("cp")), apitypes.CodeForbidden},
		{"same server", move(testSystem, "homelab"), apitypes.CodeValidation},
		{"unknown server", move(testSystem, "nope"), apitypes.CodeNotFound},
	} {
		if CodeOf(c.err) != c.want {
			t.Errorf("%s: %v, want %s", c.name, c.err, c.want)
		}
	}
	p.configure(p.ids["cp"], poolServer{"cp", true, false, 8})
	if err := move(testSystem, p.ids2name("cp")); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "accept") {
		t.Errorf("kind: %v", err)
	}
	p.configure(p.ids["cp"], poolServer{"cp", true, true, 1})
	p.createService("staging", "big", apitypes.CreateServiceRequest{MemoryMB: 2048})
	if err := move(testSystem, p.ids2name("cp")); CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "memory") {
		t.Errorf("memory: %v", err)
	}
	p.configure(p.ids["cp"], poolServer{"cp", true, true, 8})
	p.setOffline("homelab", true)
	if err := move(testSystem, p.ids2name("cp")); CodeOf(err) != apitypes.CodeServerOffline {
		t.Errorf("offline: %v", err)
	}
	p.setOffline("homelab", false)
	// While a move runs: no second one, and no deploy.
	release := make(chan struct{})
	p.c.moves.hook = func(_ int64, s apitypes.MoveStep) {
		if s == apitypes.MoveCopy {
			<-release
		}
	}
	t.Cleanup(func() { close(release) })
	if err := move(testSystem, p.ids2name("cp")); err != nil {
		t.Fatal(err)
	}
	if err := move(testSystem, p.ids2name("cp")); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("second move: %v", err)
	}
	if _, err := p.c.Restart(p.ctx, testSystem, db); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("deploy during a move: %v", err)
	}
	if _, err := p.c.CreateSnapshot(p.ctx, testSystem, db, apitypes.CreateSnapshotRequest{}); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("snapshot during a move: %v", err)
	}
}

func TestMoveEnvironmentRollsBack(t *testing.T) {
	p, lab, db, _, data := movePool(t)
	staging := EnvRef{"shop", "staging"}
	p.rt.FailHealthCheck(func(spec node.ContainerSpec) error {
		if strings.HasPrefix(spec.Name, p.envPrefix(db)) {
			return errNotRunning
		}
		return nil
	})
	if _, err := p.c.MoveEnvironment(p.ctx, testSystem, staging, apitypes.MoveEnvironmentRequest{To: p.ids2name("cp")}); err != nil {
		t.Fatal(err)
	}
	m := p.waitMove(staging)
	if m.Status != apitypes.MoveFailed || !strings.Contains(m.Error, "health check") {
		t.Fatalf("move = %+v", m)
	}
	p.rt.FailHealthCheck(nil)
	assertStayed(t, p, lab, db, data)
	// The move reads as failed just before its audit event is written.
	eventually(t, "the move_failed audit event", func() bool {
		evs, err := p.c.ListAudit(p.ctx, testSystem, AuditFilter{Action: "environment.move_failed"})
		if err != nil {
			t.Fatal(err)
		}
		return len(evs) == 1
	})
}

// A daemon that dies at any step of a move finishes it on start: before
// the switch it rolls back, after it the clean-up goes on.
func TestMoveEnvironmentSurvivesACrashAtEveryStep(t *testing.T) {
	for _, step := range []apitypes.MoveStep{apitypes.MovePrepare, apitypes.MoveStop, apitypes.MoveCopy, apitypes.MoveStart, apitypes.MoveSwitch, apitypes.MoveCleanup} {
		t.Run(string(step), func(t *testing.T) {
			p, lab, db, web, data := movePool(t)
			staging := EnvRef{"shop", "staging"}
			reached, stuck := make(chan struct{}), make(chan struct{})
			p.c.moves.hook = func(_ int64, s apitypes.MoveStep) {
				if s == step {
					close(reached)
					<-stuck // the daemon dies here
				}
			}
			if _, err := p.c.MoveEnvironment(p.ctx, testSystem, staging, apitypes.MoveEnvironmentRequest{To: p.ids2name("cp")}); err != nil {
				t.Fatal(err)
			}
			<-reached
			p.crash()
			// Before the dead daemon's Close (cleanups run last first),
			// which waits for its move.
			t.Cleanup(func() { close(stuck) })
			m := p.waitMove(staging)
			if step == apitypes.MoveCleanup {
				if m.Status != apitypes.MoveSucceeded {
					t.Fatalf("move = %+v", m)
				}
				assertMoved(t, p, lab, db, web, data)
				return
			}
			if m.Status != apitypes.MoveFailed || !strings.Contains(m.Error, "restarted") {
				t.Fatalf("move = %+v", m)
			}
			assertStayed(t, p, lab, db, data)
		})
	}
}

// A move whose source goes offline waits for it, then goes on.
func TestMoveEnvironmentWaitsForAnOfflineServer(t *testing.T) {
	p, lab, db, web, data := movePool(t)
	staging := EnvRef{"shop", "staging"}
	reached, release := make(chan struct{}), make(chan struct{})
	p.c.moves.hook = func(_ int64, s apitypes.MoveStep) {
		if s == apitypes.MoveCleanup {
			close(reached)
			<-release
		}
	}
	if _, err := p.c.MoveEnvironment(p.ctx, testSystem, staging, apitypes.MoveEnvironmentRequest{To: p.ids2name("cp")}); err != nil {
		t.Fatal(err)
	}
	<-reached
	p.setOffline("homelab", true)
	p.c.moves.hook = nil
	close(release)
	eventually(t, "the move to wait", func() bool {
		p.c.moves.mu.Lock()
		defer p.c.moves.mu.Unlock()
		return len(p.c.moves.driving) == 0
	})
	if m, _ := p.c.EnvironmentMoveStatus(p.ctx, testSystem, staging); m.Status != apitypes.MoveRunning || m.Step != apitypes.MoveCleanup {
		t.Fatalf("move = %+v", m)
	}
	p.setOffline("homelab", false)
	p.c.NodeStatus(p.nodes.State(p.ids["homelab"]))
	if m := p.waitMove(staging); m.Status != apitypes.MoveSucceeded {
		t.Fatalf("move = %+v", m)
	}
	assertMoved(t, p, lab, db, web, data)
}

// A service stopped on purpose (ADR 0050) moves stopped: its data goes
// with it, nothing of it starts on the target, and it starts there when
// it is started again.
func TestMoveEnvironmentKeepsAStoppedServiceStopped(t *testing.T) {
	p, lab, db, web, data := movePool(t)
	if _, err := p.c.StopService(p.ctx, testSystem, db); err != nil {
		t.Fatal(err)
	}
	staging := EnvRef{"shop", "staging"}
	if _, err := p.c.MoveEnvironment(p.ctx, testSystem, staging, apitypes.MoveEnvironmentRequest{To: p.ids2name("cp")}); err != nil {
		t.Fatal(err)
	}
	if m := p.waitMove(staging); m.Status != apitypes.MoveSucceeded {
		t.Fatalf("move = %+v", m)
	}
	prefix := p.envPrefix(db)
	if got := envContainers(p.rt, prefix); len(got) != 1 || !slices.Contains(got, p.ctr(web, 1)) {
		t.Errorf("running on cp: %v, want web alone", got)
	}
	if got := envContainers(lab, prefix); len(got) != 0 {
		t.Errorf("left on homelab: %v", got)
	}
	if got := p.rt.VolumeData(p.volume(db, "data")); !bytes.Equal(got, data) {
		t.Errorf("db's data on cp = %q", got)
	}
	rel, err := p.c.StartService(p.ctx, testSystem, db)
	if err != nil {
		t.Fatal(err)
	}
	if r := p.wait(db, rel.Number); r.Status != apitypes.ReleaseActive || !slices.Contains(p.rt.Running(), p.ctr(db, rel.Number)) {
		t.Errorf("started again: %s, running %v", r.Status, p.rt.Running())
	}
}
