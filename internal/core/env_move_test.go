package core

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
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

	// A connection that still reaches homelab's old port reaches web on cp.
	var addr string
	eventually(t, "homelab to forward web's old port", func() bool { addr = lab.ForwardAddr(int(old.HostPort)); return addr != "" })
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
// isn't traced on either side of the switch.
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
	if i := slices.IndexFunc(after, func(r node.Route) bool { return slices.Contains(r.Hostnames, "shop.example.com") }); i < 0 || !after[i].Hold || after[i].Trace != "" {
		t.Fatalf("homelab's forwarded route: %+v", after)
	}
	if fmt.Sprint(inHold) != fmt.Sprint(after) {
		t.Errorf("homelab's routes changed at the switch:\nin the hold %+v\nafter       %+v", inHold, after)
	}
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
