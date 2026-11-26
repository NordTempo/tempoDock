package core

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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

// hostPort is the host port of ref's active release.
func (p *pool) hostPort(ref ServiceRef) int {
	p.t.Helper()
	ss, err := p.c.serviceFor(p.ctx, testSystem, ref, apitypes.ScopeRead, "testing")
	if err != nil {
		p.t.Fatal(err)
	}
	rel, err := p.store.GetActiveRelease(p.ctx, ss.svc.ID)
	if err != nil || rel.HostPort == 0 {
		p.t.Fatalf("%s's release: %+v, %v", ref.Service, rel, err)
	}
	return int(rel.HostPort)
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
	// homelab forwards what still reaches it, holding requests until
	// the forward listens.
	if i := slices.IndexFunc(lab.Routes(), func(r node.Route) bool { return slices.Contains(r.Hostnames, "shop.example.com") }); i < 0 || !lab.Routes()[i].Hold {
		t.Errorf("homelab doesn't forward shop.example.com: %+v", lab.Routes())
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
		t.Errorf("left on cp: %v\n%s", got, p.leftoverReport(prefix))
	}
	if got := lab.VolumeData(p.volume(db, "data")); !bytes.Equal(got, data) {
		t.Errorf("db's data on homelab = %q", got)
	}
}

// leftoverReport explains containers of an environment left on cp after
// a rolled back move, for a failure that doesn't reproduce: the latest
// move's row and services, and each container with its ID, state and
// start, and whether it is the one the move started (never removed) or
// another (started again after the rollback removed it).
func (p *pool) leftoverReport(prefix string) string {
	var b strings.Builder
	moved := map[string]string{}
	if m, err := p.store.GetLatestEnvironmentMove(p.ctx, p.envID("staging")); err == nil {
		fmt.Fprintf(&b, "move %d: status %s, step %s, error %q, finished %v\n", m.ID, m.Status, m.Step, m.Error, m.FinishedAt)
		if rows, err := p.store.ListEnvironmentMoveServices(p.ctx, m.ID); err == nil {
			for _, r := range rows {
				fmt.Fprintf(&b, "  service %d: %s, target container %.12s\n", r.ServiceID, r.Status, r.ContainerID)
				moved[r.ContainerID] = "the move's"
			}
		}
	} else {
		fmt.Fprintf(&b, "latest move: %v\n", err)
	}
	ctrs, _ := p.rt.List(p.ctx)
	for _, ct := range ctrs {
		if !strings.HasPrefix(ct.Name, prefix) {
			continue
		}
		whose := cmp.Or(moved[ct.ID], "not the move's")
		fmt.Fprintf(&b, "  %s %.12s: %s, started %s, %s\n", ct.Name, ct.ID, ct.State, ct.StartedAt.Format("15:04:05.000"), whose)
	}
	return b.String()
}

// envPrefix is the container name prefix of ref's environment.
func (p *pool) envPrefix(ref ServiceRef) string {
	ss, _ := p.c.serviceFor(p.ctx, testSystem, ref, apitypes.ScopeRead, "testing")
	return fmt.Sprintf("dock_p%d_e%d_", ss.project.ID, ss.env.ID)
}

// No move starts while an environment of its project is being deleted:
// the delete would miss what the move made. A drain waits it out. A
// delete started during a move is refused.
func TestMoveWaitsForEnvironmentDelete(t *testing.T) {
	p, _, _, _, _ := movePool(t)
	staging := EnvRef{"shop", "staging"}
	es, err := p.c.loadEnv(p.ctx, 1, staging)
	if err != nil {
		t.Fatal(err)
	}
	p.c.envDeletes.Store(es.project.ID, true)
	_, err = p.c.MoveEnvironment(p.ctx, testSystem, staging, apitypes.MoveEnvironmentRequest{To: p.ids2name("cp")})
	wantCode(t, "a move during a delete", err, apitypes.CodeConflict)
	if !isBusy(err) {
		t.Errorf("a drain pauses on %v instead of waiting it out", err)
	}
	p.c.envDeletes.Delete(es.project.ID)
	if _, err := p.c.MoveEnvironment(p.ctx, testSystem, staging, apitypes.MoveEnvironmentRequest{To: p.ids2name("cp")}); err != nil {
		t.Fatal(err)
	}
	// The other way round: a delete started during the move is refused,
	// and leaves no hold behind.
	// (Another ID: should the move have ended, it still deletes nothing.)
	_, err = p.c.DeleteEnvironment(p.ctx, testSystem, staging, apitypes.DeleteEnvironmentRequest{Confirm: "staging", ID: es.env.ID + 1000})
	wantCode(t, "a delete during a move", err, apitypes.CodeConflict)
	if open, _ := p.c.store.GetOpenEnvironmentMove(p.ctx, es.env.ID); open.ID != 0 && !strings.Contains(err.Error(), "is moving") {
		t.Errorf("a delete during a move: %v", err)
	}
	if _, held := p.c.envDeletes.Load(es.project.ID); held {
		t.Error("a refused delete kept its hold")
	}
	if m := p.waitMove(staging); m.Status != apitypes.MoveSucceeded {
		t.Fatalf("the move after the delete's hold cleared: %s %s", m.Status, m.Error)
	}
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
	// Recorded just after the move reads as succeeded.
	eventually(t, "the move's audit event", func() bool {
		evs, err := p.c.ListAudit(p.ctx, testSystem, AuditFilter{Action: "environment.moved"})
		return err == nil && len(evs) == 1
	})
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
	// While a move runs: no second one and no snapshot; a deploy waits.
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
	if _, err := p.c.CreateSnapshot(p.ctx, testSystem, db, apitypes.CreateSnapshotRequest{}); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("snapshot during a move: %v", err)
	}
}

func TestMoveEnvironmentRollsBack(t *testing.T) {
	p, lab, db, web, data := movePool(t)
	staging := EnvRef{"shop", "staging"}
	p.rt.FailHealthCheck(func(spec node.ContainerSpec) error {
		if strings.HasPrefix(spec.Name, p.envPrefix(db)) {
			return errNotRunning
		}
		return nil
	})
	var heldPort int
	p.c.moves.hook = func(_ int64, s apitypes.MoveStep) {
		if s == apitypes.MoveCopy {
			heldPort = upstreamPort(t, lab.Routes(), "shop.example.com")
		}
	}
	if _, err := p.c.MoveEnvironment(p.ctx, testSystem, staging, apitypes.MoveEnvironmentRequest{To: p.ids2name("cp")}); err != nil {
		t.Fatal(err)
	}
	m := p.waitMove(staging)
	if m.Status != apitypes.MoveFailed || !strings.Contains(m.Error, "health check") {
		t.Fatalf("move = %+v", m)
	}
	p.rt.FailHealthCheck(nil)
	assertStayed(t, p, lab, db, data)
	// web's route points at its container again, and the hold's port
	// closed after that.
	port := p.hostPort(web)
	if got := upstreamPort(t, lab.Routes(), "shop.example.com"); got != port {
		t.Errorf("after the rollback homelab routes web to port %d, want its container's %d", got, port)
	}
	for _, held := range []int{port, heldPort} {
		if lab.ForwardAddr(held) != "" {
			t.Errorf("homelab still listens on port %d after the rollback", held)
		}
	}
	// The move reads as failed just before its audit event is written.
	eventually(t, "the move_failed audit event", func() bool {
		evs, err := p.c.ListAudit(p.ctx, testSystem, AuditFilter{Action: "environment.move_failed"})
		if err != nil {
			t.Fatal(err)
		}
		return len(evs) == 1
	})
}

// A move that rolled back stays rolled back when its driver is started
// once more: a resume lists the open moves and may get to one only after
// it ended.
func TestEndedMoveIsNotDrivenAgain(t *testing.T) {
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
	if m := p.waitMove(staging); m.Status != apitypes.MoveFailed {
		t.Fatalf("move = %+v", m)
	}
	p.rt.FailHealthCheck(nil) // a second try would get through
	idle := func() bool {
		p.c.moves.mu.Lock()
		defer p.c.moves.mu.Unlock()
		return len(p.c.moves.driving) == 0
	}
	eventually(t, "the move's driver to end", idle)
	row, err := p.store.GetLatestEnvironmentMove(p.ctx, p.envID("staging"))
	if err != nil {
		t.Fatal(err)
	}
	p.c.startMove(row.ID)
	eventually(t, "the second driver to end", idle)
	if m, err := p.c.EnvironmentMoveStatus(p.ctx, testSystem, staging); err != nil || m.Status != apitypes.MoveFailed {
		t.Fatalf("move after a second driver = %+v, %v", m, err)
	}
	assertStayed(t, p, lab, db, data)
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

// TestMoveWithoutDowntime: a write that lands while the move copies is
// on the target, requests are held during the switch and forwarded from
// the source after it, and a deploy during the move waits for it and
// runs on the target (ADR 0063).
func TestMoveWithoutDowntime(t *testing.T) {
	p, lab, db, web, _ := movePool(t)
	staging := EnvRef{"shop", "staging"}
	ss, err := p.c.serviceFor(p.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	old, err := p.store.GetActiveRelease(p.ctx, ss.svc.ID)
	if err != nil || old.HostPort == 0 {
		t.Fatalf("web's release: %+v, %v", old, err)
	}
	echo, _ := echoServer(t)
	p.rt.DialTo(echo) // web's container on cp

	written := []byte("rows written while the copy ran")
	var held bool
	var restarted error
	// The deploy made during the move starts only once the move ended:
	// the order in which its environment as it was made is stale.
	moved := make(chan struct{})
	p.c.moves.deployHook = func() { <-moved }
	p.c.moves.hook = func(_ int64, s apitypes.MoveStep) {
		switch s {
		case apitypes.MoveStop: // after the live passes, before the fence
			lab.SetVolumeData(p.volume(db, "data"), written)
		case apitypes.MoveCopy: // in the hold
			i := slices.IndexFunc(lab.Routes(), func(r node.Route) bool { return slices.Contains(r.Hostnames, "shop.example.com") })
			held = i >= 0 && lab.Routes()[i].Hold
			_, restarted = p.c.Restart(p.ctx, testSystem, web)
		}
	}
	if _, err := p.c.MoveEnvironment(p.ctx, testSystem, staging, apitypes.MoveEnvironmentRequest{To: p.ids2name("cp")}); err != nil {
		t.Fatal(err)
	}
	m := p.waitMove(staging)
	close(moved)
	if m.Status != apitypes.MoveSucceeded || m.Services[0].Passes < 1 {
		t.Fatalf("move = %+v", m)
	}
	if got := p.rt.VolumeData(p.volume(db, "data")); !bytes.Equal(got, written) {
		t.Errorf("db's data on cp = %q, want the write made during the move", got)
	}
	if !held {
		t.Error("homelab didn't hold shop.example.com's requests during the switch")
	}
	if restarted != nil {
		t.Errorf("deploy during the move: %v", restarted)
	}
	if r := p.wait(web, 2); r.Status != apitypes.ReleaseActive || !slices.Contains(p.rt.Running(), p.ctr(web, 2)) {
		t.Errorf("the deploy made during the move: %s, on cp: %v", r.Status, p.rt.Running())
	}

	// A connection that still reaches homelab's route reaches web on cp,
	// through the hold's port, which isn't the release's.
	fwd := upstreamPort(t, lab.Routes(), "shop.example.com")
	if fwd == int(old.HostPort) {
		t.Errorf("homelab forwards web's release port %d, want the hold's own", fwd)
	}
	var addr string
	eventually(t, "homelab to forward web's route", func() bool { addr = lab.ForwardAddr(fwd); return addr != "" })
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("GET /")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "GET /" {
		t.Fatalf("through the forward: %q, %v", buf, err)
	}
}

// TestMoveSwitchKeepsTheSourcesRoutes: the source's routes after the
// switch are the ones it held requests with, so Caddy has nothing to
// reload: a reload ends every held request with a 502. The moved service
// keeps its place before another service on the source, and its route
// is traced before the hold, in it and after the switch alike: a hold
// that changed the config would reload Caddy too, and that ends requests
// on idle keep-alive connections. Only the token differs once the
// service's release is the one on its new server, and tokens aren't in
// Caddy's config.
func TestMoveSwitchKeepsTheSourcesRoutes(t *testing.T) {
	p, lab, _, _, _ := movePool(t)
	p.mustEnv("shop", "qa", "", "homelab")
	docs := p.createService("qa", "docs", apitypes.CreateServiceRequest{Port: 8080})
	p.deploy(docs, "docs:1")
	if _, err := p.c.AddDomain(p.ctx, testSystem, docs, apitypes.AddDomainRequest{Hostname: "docs.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.c.EnableTraces(p.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	// Whether a route is traced is in Caddy's config, its token isn't.
	shape := func(routes []node.Route) string {
		routes = slices.Clone(routes)
		for i := range routes {
			if routes[i].Trace != "" {
				routes[i].Trace = "traced"
			}
			// The hold itself is an upstream file, read per request
			// (ADR 0091), and the hold port is the route's from then on.
			routes[i].Hold, routes[i].Upstream = false, ""
		}
		return fmt.Sprint(routes)
	}
	before := lab.Routes()
	var inHold []node.Route
	p.c.moves.hook = func(_ int64, s apitypes.MoveStep) {
		if s == apitypes.MoveCopy {
			inHold = lab.Routes()
		}
	}
	staging := EnvRef{"shop", "staging"}
	if _, err := p.c.MoveEnvironment(p.ctx, testSystem, staging, apitypes.MoveEnvironmentRequest{To: p.ids2name("cp")}); err != nil {
		t.Fatal(err)
	}
	if m := p.waitMove(staging); m.Status != apitypes.MoveSucceeded {
		t.Fatalf("move = %+v", m)
	}
	after := lab.Routes()
	if i := slices.IndexFunc(after, func(r node.Route) bool { return slices.Contains(r.Hostnames, "shop.example.com") }); i < 0 || !after[i].Hold || after[i].Trace == "" {
		t.Fatalf("homelab's forwarded route: %+v", after)
	}
	if shape(before) != shape(inHold) {
		t.Errorf("homelab's routes changed when the hold began:\nbefore      %+v\nin the hold %+v", before, inHold)
	}
	for i := range inHold {
		if inHold[i].Trace != "" {
			inHold[i].Trace = "traced"
		}
	}
	for i := range after {
		if after[i].Trace != "" {
			after[i].Trace = "traced"
		}
	}
	if fmt.Sprint(inHold) != fmt.Sprint(after) {
		t.Errorf("homelab's routes changed at the switch:\nin the hold %+v\nafter       %+v", inHold, after)
	}
}

// heldConn connects to a port a server listens on for a move and sends
// a request, the way Caddy does for a held route.
func heldConn(t *testing.T, addr string) net.Conn {
	t.Helper()
	if addr == "" {
		t.Fatal("nothing listens on the held port")
	}
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Write([]byte("GET /")); err != nil {
		t.Fatal(err)
	}
	return conn
}

// readsBack checks that a held connection reached the echo server.
func readsBack(t *testing.T, conn net.Conn, what string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "GET /" {
		t.Errorf("%s: %q, %v", what, buf, err)
	}
}

// upstreamPort is the port of the route serving host.
func upstreamPort(t *testing.T, routes []node.Route, host string) int {
	t.Helper()
	i := slices.IndexFunc(routes, func(r node.Route) bool { return slices.Contains(r.Hostnames, host) })
	if i < 0 {
		t.Fatalf("no route for %s in %+v", host, routes)
	}
	_, port, _ := net.SplitHostPort(routes[i].Upstream)
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("route for %s: upstream %q", host, routes[i].Upstream)
	}
	return n
}

// TestMoveHoldsConnections: in the hold the source's route points at a
// fresh port the source listens on, before the service stops, so no
// request meets the stopping container (ADR 0091). Its connections wait
// for the switch, then reach the target. Moving back to a server that
// forwards the service, its forward's connections wait the same way and
// reach the new container there, not the one the forward pointed at, now
// stopped.
func TestMoveHoldsConnections(t *testing.T) {
	p, lab, _, web, _ := movePool(t)
	echo, _ := echoServer(t)
	p.rt.DialTo(echo)
	lab.DialTo(echo)
	labPort := p.hostPort(web)
	var conn net.Conn
	var heldPort int
	p.c.moves.hook = func(_ int64, s apitypes.MoveStep) {
		if s == apitypes.MoveCopy { // in the hold, web stopped on the source
			heldPort = upstreamPort(t, lab.Routes(), "shop.example.com")
			conn = heldConn(t, lab.ForwardAddr(heldPort))
		}
	}
	staging := EnvRef{"shop", "staging"}
	if _, err := p.c.MoveEnvironment(p.ctx, testSystem, staging, apitypes.MoveEnvironmentRequest{To: p.ids2name("cp")}); err != nil {
		t.Fatal(err)
	}
	if m := p.waitMove(staging); m.Status != apitypes.MoveSucceeded {
		t.Fatalf("move = %+v", m)
	}
	readsBack(t, conn, "a connection held on homelab")
	if heldPort == labPort {
		t.Errorf("homelab held web on its release's port %d, want a fresh one", labPort)
	}

	var back, forwarded net.Conn
	p.c.moves.hook = func(_ int64, s apitypes.MoveStep) {
		if s == apitypes.MoveCopy {
			back = heldConn(t, p.rt.ForwardAddr(upstreamPort(t, p.rt.Routes(), "shop.example.com")))
			forwarded = heldConn(t, lab.ForwardAddr(heldPort))
		}
	}
	if _, err := p.c.MoveEnvironment(p.ctx, testSystem, staging, apitypes.MoveEnvironmentRequest{To: "homelab"}); err != nil {
		t.Fatal(err)
	}
	if m := p.waitMove(staging); m.Status != apitypes.MoveSucceeded {
		t.Fatalf("move back = %+v", m)
	}
	readsBack(t, back, "a connection held on cp")
	readsBack(t, forwarded, "a connection to homelab's forward")
}

// TestMoveRefusesALongHold: data that keeps changing faster than the
// copy catches up fails the move before anything stops, instead of
// holding requests for longer than the budget.
func TestMoveRefusesALongHold(t *testing.T) {
	p, lab, db, _, data := movePool(t)
	staging := EnvRef{"shop", "staging"}
	lab.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if spec.Script == moveSizeScript {
			return node.JobResult{Output: "dock-move: kib 4\n"}, nil
		}
		if strings.Contains(spec.Script, "send --live") {
			time.Sleep(40 * time.Millisecond) // a pass this slow, every time
		}
		return node.JobResult{}, fake.ErrDefault
	})
	p.c.moves.holdUnit = 10 * time.Millisecond // a hold of 5 is 50 ms
	if _, err := p.c.MoveEnvironment(p.ctx, testSystem, staging, apitypes.MoveEnvironmentRequest{To: p.ids2name("cp"), MaxHold: 5}); err != nil {
		t.Fatal(err)
	}
	m := p.waitMove(staging)
	if m.Status != apitypes.MoveFailed || m.Step != apitypes.MoveSync || !strings.Contains(m.Error, "--max-hold") || m.DowntimeFrom != nil {
		t.Fatalf("move = %+v", m)
	}
	assertStayed(t, p, lab, db, data)
}

// TestMoveKeepsOneGeneratedDomainPerServer: moving back and forth keeps
// one generated domain per server that serves the service (the one it is
// on, and the one forwarding it) instead of piling up new ones, moving
// back needs no DNS change for its own name, and removing a server drops
// the name under its base domain (ADR 0063).
func TestMoveKeepsOneGeneratedDomainPerServer(t *testing.T) {
	p, _, _, web, _ := movePool(t)
	staging := EnvRef{"shop", "staging"}
	cp := p.ids2name("cp")
	for name, base := range map[string]string{"homelab": "lab.example.dev", cp: "cp.example.dev"} {
		if _, err := p.c.UpdateServer(p.ctx, testSystem, name, apitypes.UpdateServerRequest{BaseDomain: ptr(base)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.c.GenerateDomain(p.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	ss, err := p.c.serviceFor(p.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	generated := func() []string {
		t.Helper()
		doms, err := p.store.ListDomainsByService(p.ctx, ss.svc.ID)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, d := range doms {
			if d.Generated {
				out = append(out, d.Hostname)
			}
		}
		slices.Sort(out)
		return out
	}
	move := func(to string) apitypes.EnvironmentMove {
		t.Helper()
		if _, err := p.c.MoveEnvironment(p.ctx, testSystem, staging, apitypes.MoveEnvironmentRequest{To: to}); err != nil {
			t.Fatal(err)
		}
		m := p.waitMove(staging)
		if m.Status != apitypes.MoveSucceeded {
			t.Fatalf("move to %s = %+v", to, m)
		}
		return m
	}
	// One under each base, kept from the first move on: moving back and
	// forth never makes a new hostname.
	var both []string
	var onCP string
	for i, to := range []string{cp, "homelab", cp} {
		m := move(to)
		got := generated()
		if i == 0 {
			both = got
			for _, host := range got {
				if generatedLike("web-shop-staging", "cp.example.dev").MatchString(host) {
					onCP = host
				}
			}
			if len(got) != 2 || onCP == "" || !slices.ContainsFunc(got, generatedLike("web-shop-staging", "lab.example.dev").MatchString) {
				t.Fatalf("after move 1: generated domains %v, want one under each base", got)
			}
		}
		if !slices.Equal(got, both) {
			t.Fatalf("after move %d (to %s): generated domains %v, want %v", i+1, to, got, both)
		}
		if notes := strings.Join(m.Notes, "\n"); strings.Contains(notes, "point the DNS of web-shop-staging") {
			t.Errorf("move %d asks to repoint a generated domain: %s", i+1, notes)
		}
	}
	if err := p.c.RemoveServer(p.ctx, testSystem, "homelab"); err != nil {
		t.Fatal(err)
	}
	if got := generated(); !slices.Equal(got, []string{onCP}) {
		t.Fatalf("after removing homelab: generated domains %v, want %v", got, onCP)
	}
}

// A server's base domain can be a parent of another's (example.dev and
// lab.example.dev): a name under the node's base isn't the control
// plane's, so moving to the control plane gives the service one there.
func TestMoveWithNestedBaseDomains(t *testing.T) {
	p, _, _, web, _ := movePool(t)
	staging := EnvRef{"shop", "staging"}
	cp := p.ids2name("cp")
	for name, base := range map[string]string{"homelab": "lab.example.dev", cp: "example.dev"} {
		if _, err := p.c.UpdateServer(p.ctx, testSystem, name, apitypes.UpdateServerRequest{BaseDomain: ptr(base)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.c.GenerateDomain(p.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	if _, err := p.c.MoveEnvironment(p.ctx, testSystem, staging, apitypes.MoveEnvironmentRequest{To: cp}); err != nil {
		t.Fatal(err)
	}
	if m := p.waitMove(staging); m.Status != apitypes.MoveSucceeded {
		t.Fatalf("move = %+v", m)
	}
	doms, err := p.c.ListDomains(p.ctx, testSystem, web)
	if err != nil {
		t.Fatal(err)
	}
	var bases []string
	for _, d := range doms {
		if d.Generated {
			_, base, _ := strings.Cut(d.Hostname, ".")
			bases = append(bases, base)
		}
	}
	slices.Sort(bases)
	if want := []string{"example.dev", "lab.example.dev"}; !slices.Equal(bases, want) {
		t.Errorf("generated domains under %v, want one under each of %v", bases, want)
	}
}

// TestMoveCopiesServicesSideBySide: in the hold, the final copies of an
// environment's stateful services run at the same time, so requests wait
// for the slowest copy rather than the sum of them.
func TestMoveCopiesServicesSideBySide(t *testing.T) {
	p, lab, db, _, data := movePool(t)
	cache := p.createService("staging", "cache", apitypes.CreateServiceRequest{Port: 6379})
	if _, err := p.c.AddVolume(p.ctx, testSystem, cache, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
		t.Fatal(err)
	}
	p.deploy(cache, "redis:8")
	both := make(chan struct{})
	var mu sync.Mutex
	finals, together := 0, false
	lab.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if spec.Script == moveSizeScript {
			return node.JobResult{Output: "dock-move: kib 4\n"}, nil
		}
		// A final pass's sender: the first waits for the second.
		if strings.Contains(spec.Script, " send ") && !strings.Contains(spec.Script, string(syncLive)) && !strings.Contains(spec.Script, string(syncCheck)) {
			mu.Lock()
			finals++
			if finals == 2 {
				together = true
				close(both)
			}
			mu.Unlock()
			select {
			case <-both:
			case <-time.After(2 * time.Second):
			}
		}
		return node.JobResult{}, fake.ErrDefault
	})
	staging := EnvRef{"shop", "staging"}
	if _, err := p.c.MoveEnvironment(p.ctx, testSystem, staging, apitypes.MoveEnvironmentRequest{To: p.ids2name("cp")}); err != nil {
		t.Fatal(err)
	}
	if m := p.waitMove(staging); m.Status != apitypes.MoveSucceeded {
		t.Fatalf("move = %+v", m)
	}
	mu.Lock()
	defer mu.Unlock()
	if !together {
		t.Error("db's and cache's final copies ran one after the other")
	}
	if got := p.rt.VolumeData(p.volume(db, "data")); !bytes.Equal(got, data) {
		t.Errorf("db's data on cp = %q", got)
	}
}

// TestForwardLeavesOutOtherServersNames: the server a service moved away
// from forwards its own generated name and its custom domains, not the
// name the move made under the target's base: that would change its
// config, and the next move's hold would reload its Caddy.
func TestForwardLeavesOutOtherServersNames(t *testing.T) {
	p, lab, _, web, _ := movePool(t)
	cp := p.ids2name("cp")
	for name, base := range map[string]string{"homelab": "lab.example.dev", cp: "cp.example.dev"} {
		if _, err := p.c.UpdateServer(p.ctx, testSystem, name, apitypes.UpdateServerRequest{BaseDomain: ptr(base)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.c.GenerateDomain(p.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	staging := EnvRef{"shop", "staging"}
	if _, err := p.c.MoveEnvironment(p.ctx, testSystem, staging, apitypes.MoveEnvironmentRequest{To: cp}); err != nil {
		t.Fatal(err)
	}
	if m := p.waitMove(staging); m.Status != apitypes.MoveSucceeded {
		t.Fatalf("move = %+v", m)
	}
	// The next change on homelab (the hold of another move) renders its
	// routes again.
	if err := p.c.applyRoutes(p.ctx, p.ids["homelab"]); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(lab.Routes(), func(r node.Route) bool { return slices.Contains(r.Hostnames, "shop.example.com") })
	if i < 0 {
		t.Fatalf("homelab doesn't forward web: %+v", lab.Routes())
	}
	hosts := lab.Routes()[i].Hostnames
	if !slices.ContainsFunc(hosts, generatedLike("web-shop-staging", "lab.example.dev").MatchString) {
		t.Errorf("homelab forwards %v, without web's name under its own base", hosts)
	}
	if slices.ContainsFunc(hosts, generatedLike("web-shop-staging", "cp.example.dev").MatchString) {
		t.Errorf("homelab forwards %v, with web's name under cp's base", hosts)
	}
}
