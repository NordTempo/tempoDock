package core

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/fake"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

const gib = int64(1) << 30

func load(id int64, name string, prod, nonProd bool, memGiB, reservedGiB int64, projects int) serverLoad {
	return serverLoad{
		server: store.Server{ID: id, Name: name, AcceptsProduction: prod, AcceptsNonProduction: nonProd, MemoryBytes: memGiB * gib},
		online: true, reserved: reservedGiB * gib, projects: projects,
	}
}

func TestPickServer(t *testing.T) {
	prod, nonProd := apitypes.KindProduction, apitypes.KindNonProduction
	disabled := func(l serverLoad) serverLoad { l.server.DisabledForPlacement = true; return l }
	offline := func(l serverLoad) serverLoad { l.online = false; return l }
	cases := []struct {
		name  string
		kind  apitypes.EnvironmentKind
		loads []serverLoad
		want  string
		code  apitypes.ErrorCode
		msg   []string
	}{
		{"free room, not total memory", prod, []serverLoad{
			load(1, "big", true, false, 32, 30, 1), load(2, "small", true, false, 8, 1, 3),
		}, "small", "", nil},
		{"tie goes to fewer projects", nonProd, []serverLoad{
			load(1, "a", false, true, 8, 2, 3), load(2, "b", false, true, 8, 2, 1),
		}, "b", "", nil},
		{"full tie goes to the oldest server", nonProd, []serverLoad{
			load(2, "b", false, true, 8, 0, 0), load(1, "a", false, true, 8, 0, 0),
		}, "a", "", nil},
		{"only servers accepting the kind", nonProd, []serverLoad{
			load(1, "prod", true, false, 64, 0, 0), load(2, "lab", false, true, 4, 3, 9),
		}, "lab", "", nil},
		{"offline and disabled servers are skipped", prod, []serverLoad{
			offline(load(1, "a", true, true, 64, 0, 0)), disabled(load(2, "b", true, true, 64, 0, 0)), load(3, "c", true, true, 2, 1, 0),
		}, "c", "", nil},
		{"unknown memory only wins alone", prod, []serverLoad{
			load(1, "new", true, false, 0, 0, 0), load(2, "known", true, false, 4, 3, 2),
		}, "known", "", nil},
		{"overcommitted still placeable", prod, []serverLoad{load(1, "a", true, false, 2, 5, 1)}, "a", "", nil},
		{"nobody accepts the kind", nonProd, []serverLoad{load(1, "a", true, false, 8, 0, 0)}, "",
			apitypes.CodeConflict, []string{"no server accepts non-production", "a platform operator"}},
		{"accepting server offline", nonProd, []serverLoad{
			load(1, "a", true, false, 8, 0, 0), offline(load(2, "lab", false, true, 8, 0, 0)), disabled(load(3, "lab2", false, true, 8, 0, 0)),
		}, "", apitypes.CodeServerOffline, []string{"1 server offline, 1 server disabled for placement"}},
		{"accepting server disabled", prod, []serverLoad{disabled(load(1, "a", true, false, 8, 0, 0))}, "",
			apitypes.CodeConflict, []string{"(1 server disabled for placement)"}},
		{"no servers", prod, nil, "", apitypes.CodeConflict, []string{"no server accepts production"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pickServer(tc.kind, tc.loads)
			if tc.code != "" {
				if CodeOf(err) != tc.code {
					t.Fatalf("err = %v (%s), want code %s", err, CodeOf(err), tc.code)
				}
				for _, m := range tc.msg {
					if !strings.Contains(err.Error(), m) {
						t.Errorf("error %q doesn't mention %q", err, m)
					}
				}
				return
			}
			if err != nil || got.server.Name != tc.want {
				t.Fatalf("picked %q, %v; want %q", got.server.Name, err, tc.want)
			}
		})
	}
}

// A call that panics ends alone: fanOut returns, and the other servers'
// results are there. Unrecovered, the panic would end the process.
func TestFanOutOutlivesAPanic(t *testing.T) {
	got := make([]bool, 3)
	fanOut(context.Background(), len(got), time.Minute, func(_ context.Context, i int) {
		if i == 1 {
			panic("a reply this server's call can't read")
		}
		got[i] = true
	})
	if !got[0] || got[1] || !got[2] {
		t.Fatalf("results = %v, want the first and the last server's", got)
	}
}

// One server that doesn't answer costs fanOut its timeout and holds up no
// other server's call.
func TestFanOutIsolatesHungServers(t *testing.T) {
	var fast atomic.Int64
	start := time.Now()
	fanOut(context.Background(), 3, 100*time.Millisecond, func(ctx context.Context, i int) {
		if i == 0 {
			<-ctx.Done() // hung
			return
		}
		fast.Store(int64(time.Since(start)))
	})
	if d := time.Since(start); d < 100*time.Millisecond || d > 5*time.Second {
		t.Fatalf("fanOut took %v", d)
	}
	if d := time.Duration(fast.Load()); d == 0 || d >= 100*time.Millisecond {
		t.Fatalf("the other servers answered after %v", d)
	}
}

// pool is a harness with several servers: the local one plus fake remote
// ones that tests can take offline.
type pool struct {
	*harness
	ids   map[string]int64
	nodes *fakeNodes
	rts   map[int64]*fake.Runtime
}

type poolServer struct {
	name          string
	prod, nonProd bool
	memGiB        int64
}

// newPool starts a control plane whose local server is local, plus remotes.
func newPool(t *testing.T, local poolServer, remotes ...poolServer) *pool {
	h := newHarness(t)
	p := &pool{harness: h, ids: map[string]int64{}, nodes: newFakeNodes(), rts: map[int64]*fake.Runtime{}}
	// The local server reports its memory through the runtime; make the
	// collector agree with what the test sets.
	h.rt.SetHostStats(node.HostStats{CPUs: 4, MemoryTotal: uint64(local.memGiB * gib)})
	p.ids[local.name] = h.c.localID
	p.configure(h.c.localID, local)
	for _, s := range remotes {
		srv, err := h.store.CreateServer(h.ctx, store.CreateServerParams{Name: s.name})
		if err != nil {
			t.Fatal(err)
		}
		p.ids[s.name] = srv.ID
		rt := fake.New()
		rt.SetHostStats(node.HostStats{CPUs: 4, MemoryTotal: uint64(s.memGiB * gib)})
		p.rts[srv.ID] = rt
		p.nodes.add(srv.ID, rt, remote.Hello{Protocol: remote.Protocol, Facts: remote.Facts{Ingress: remote.IngressDirect}})
		p.configure(srv.ID, s)
	}
	h.nodes = p.nodes
	h.restart()
	// Each remote server's watcher subscribes to its events in its own
	// goroutine: wait for them, or an event a test emits right away (a
	// node's wake request) is lost on a slow runner.
	for name, id := range p.ids {
		if rt := p.rts[id]; rt != nil {
			eventually(t, name+"'s events followed", func() bool { return rt.Subscribers() > 0 })
		}
	}
	return p
}

func (p *pool) configure(id int64, s poolServer) {
	p.t.Helper()
	if _, err := p.store.UpdateServerPlacement(p.ctx, store.UpdateServerPlacementParams{AcceptsProduction: s.prod, AcceptsNonProduction: s.nonProd, ID: id}); err != nil {
		p.t.Fatal(err)
	}
	if err := p.store.SetServerMemory(p.ctx, store.SetServerMemoryParams{MemoryBytes: s.memGiB * gib, ID: id, MemoryBytes_2: s.memGiB * gib}); err != nil {
		p.t.Fatal(err)
	}
}

func (p *pool) setOffline(name string, off bool) { p.nodes.setOffline(p.ids[name], off) }

func (p *pool) project(name string) {
	p.t.Helper()
	if _, err := p.c.CreateProject(p.ctx, testSystem, apitypes.CreateProjectRequest{Name: name}); err != nil {
		p.t.Fatal(err)
	}
}

// env creates an environment and returns the server it was placed on.
func (p *pool) env(project, name string, kind apitypes.EnvironmentKind) (string, error) {
	e, err := p.c.CreateEnvironment(p.ctx, testSystem, project, apitypes.CreateEnvironmentRequest{Name: name, Kind: kind})
	return e.Server, err
}

func (p *pool) mustEnv(project, name string, kind apitypes.EnvironmentKind, want string) {
	p.t.Helper()
	got, err := p.env(project, name, kind)
	if err != nil || got != want {
		p.t.Fatalf("%s/%s placed on %q, %v; want %q", project, name, got, err, want)
	}
}

func (p *pool) server(project, env string) string {
	p.t.Helper()
	e, err := p.c.GetEnvironment(p.ctx, testSystem, EnvRef{project, env})
	if err != nil {
		p.t.Fatal(err)
	}
	return e.Server
}

func TestPlacementKeepsProjectsTogether(t *testing.T) {
	p := newPool(t,
		poolServer{"cp", true, false, 8},
		poolServer{"vps2", true, false, 16},
		poolServer{"homelab", false, true, 32},
		poolServer{"lab2", false, true, 32},
	)
	cpName := p.c.localName()

	// A project's production environment goes where there is most room.
	p.project("shop")
	if got := p.server("shop", "production"); got != "vps2" {
		t.Fatalf("shop/production on %s, want vps2", got)
	}
	// Its first non-production environment picks a server; the tie
	// between the two labs goes to the older one, and the rest follow.
	p.mustEnv("shop", "staging", "", "homelab")
	p.mustEnv("shop", "dev", apitypes.KindNonProduction, "homelab")
	// An explicit production kind follows the project's production.
	p.mustEnv("shop", "prod-eu", apitypes.KindProduction, "vps2")
	if e, _ := p.c.GetEnvironment(p.ctx, testSystem, EnvRef{"shop", "prod-eu"}); e.Kind != apitypes.KindProduction {
		t.Fatalf("prod-eu kind = %s", e.Kind)
	}
	if e, _ := p.c.GetEnvironment(p.ctx, testSystem, EnvRef{"shop", "dev"}); e.Kind != apitypes.KindNonProduction {
		t.Fatalf("dev kind = %s", e.Kind)
	}

	// Reserve 12 GiB on vps2: the next project's production fits better
	// on the control plane's server (8 free vs 4 free).
	if _, err := p.c.CreateService(p.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.CreateServiceRequest{Name: "db", Image: "postgres:17", MemoryMB: 12 << 10}); err != nil {
		t.Fatal(err)
	}
	p.project("blog")
	if got := p.server("blog", "production"); got != cpName {
		t.Fatalf("blog/production on %s, want %s", got, cpName)
	}
	// Equal free memory on the labs: the one with fewer projects wins.
	p.mustEnv("blog", "staging", "", "lab2")

	// Placements stick even when the server stops taking new ones.
	if _, err := p.c.UpdateServer(p.ctx, testSystem, "homelab", apitypes.UpdateServerRequest{DisabledForPlacement: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	p.mustEnv("shop", "preview", "", "homelab")

	// New projects can't go anywhere for non-production now: one lab is
	// disabled, the other offline.
	p.setOffline("lab2", true)
	p.project("wiki")
	_, err := p.env("wiki", "staging", "")
	if CodeOf(err) != apitypes.CodeServerOffline || !strings.Contains(err.Error(), "1 server offline, 1 server disabled for placement") {
		t.Fatalf("placing with no eligible server: %v", err)
	}
	// The failed placement left nothing behind.
	if pls, _ := p.c.ListPlacements(p.ctx, testSystem, "wiki"); len(pls) != 1 || pls[0].Kind != apitypes.KindProduction {
		t.Fatalf("wiki placements = %+v", pls)
	}
	p.setOffline("lab2", false)
	p.mustEnv("wiki", "staging", "", "lab2")

	// The server list reports load and placements.
	servers, err := p.c.ListServers(p.ctx, testSystem)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range servers {
		if s.Name == "vps2" && (s.Projects != 1 || s.Environments != 2 || s.ReservedMemoryBytes != 12*gib || s.MemoryBytes != 16*gib || s.Status != "online") {
			t.Errorf("vps2 = %+v", s)
		}
	}
	d, err := p.c.GetServer(p.ctx, testSystem, "homelab")
	if err != nil || len(d.Placements) != 1 || d.Placements[0].Project != "shop" || strings.Join(d.Placements[0].Environments, ",") != "dev,preview,staging" {
		t.Fatalf("homelab detail = %+v, %v", d, err)
	}
}

func TestPinPlacement(t *testing.T) {
	p := newPool(t,
		poolServer{"cp", true, true, 8},
		poolServer{"big", true, false, 64},
		poolServer{"lab", false, true, 16},
	)
	p.project("shop") // production lands on big
	if got := p.server("shop", "production"); got != "big" {
		t.Fatalf("shop/production on %s", got)
	}

	// Pin before the first non-production environment: it goes to the
	// pinned server, even one disabled for placement.
	if _, err := p.c.UpdateServer(p.ctx, testSystem, "lab", apitypes.UpdateServerRequest{DisabledForPlacement: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	pl, err := p.c.PinPlacement(p.ctx, testSystem, "shop", apitypes.KindNonProduction, "lab")
	if err != nil || !pl.Pinned || pl.Server != "lab" {
		t.Fatalf("pin = %+v, %v", pl, err)
	}
	p.mustEnv("shop", "staging", "", "lab")

	cases := []struct {
		name   string
		actor  Actor
		kind   apitypes.EnvironmentKind
		server string
		code   apitypes.ErrorCode
	}{
		{"server doesn't accept non-production", testSystem, apitypes.KindNonProduction, "big", apitypes.CodeValidation},
		{"server doesn't accept production", testSystem, apitypes.KindProduction, "lab", apitypes.CodeValidation},
		{"production already placed", testSystem, apitypes.KindProduction, p.ids2name("cp"), apitypes.CodeConflict},
		{"unknown server", testSystem, apitypes.KindProduction, "nope", apitypes.CodeNotFound},
		{"deploy scope", Actor{Kind: "token", OrgID: 1, Name: "ci", Scope: apitypes.ScopeDeploy}, apitypes.KindProduction, "big", apitypes.CodeForbidden},
		{"limited admin", Actor{Kind: "token", OrgID: 1, Name: "ops", Scope: apitypes.ScopeAdmin, ProjectID: 1, ProjectName: "shop"}, apitypes.KindProduction, "big", apitypes.CodeForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := p.c.PinPlacement(p.ctx, tc.actor, "shop", tc.kind, tc.server); CodeOf(err) != tc.code {
				t.Fatalf("err = %v (%s), want %s", err, CodeOf(err), tc.code)
			}
		})
	}
	// Re-pinning to where the environments already are is a no-op.
	if _, err := p.c.PinPlacement(p.ctx, testSystem, "shop", apitypes.KindNonProduction, "lab"); err != nil {
		t.Fatal(err)
	}
}

// ids2name returns the server's real name (the local one is named after
// the host).
func (p *pool) ids2name(name string) string {
	srv, err := p.store.GetServer(p.ctx, p.ids[name])
	if err != nil {
		p.t.Fatal(err)
	}
	return srv.Name
}

func TestUpdateServerNeedsUnlimitedAdmin(t *testing.T) {
	h := newHarness(t)
	name := h.c.localName()
	for _, a := range []Actor{
		{Kind: "token", OrgID: 1, Name: "ci", Scope: apitypes.ScopeDeploy},
		{Kind: "token", OrgID: 1, Name: "ops", Scope: apitypes.ScopeAdmin, ProjectID: 1, ProjectName: "shop"},
	} {
		if _, err := h.c.UpdateServer(h.ctx, a, name, apitypes.UpdateServerRequest{AcceptsProduction: ptr(false)}); CodeOf(err) != apitypes.CodeForbidden {
			t.Errorf("%s: %v", a, err)
		}
		if _, err := h.c.GetServer(h.ctx, a, name); CodeOf(err) != apitypes.CodeForbidden {
			t.Errorf("%s: %v", a, err)
		}
		if _, err := h.c.ServerStats(h.ctx, a, name, apitypes.Range1h); CodeOf(err) != apitypes.CodeForbidden {
			t.Errorf("%s: %v", a, err)
		}
	}
	s, err := h.c.UpdateServer(h.ctx, testSystem, name, apitypes.UpdateServerRequest{AcceptsNonProduction: ptr(false), DisabledForPlacement: ptr(true)})
	if err != nil || !s.AcceptsProduction || s.AcceptsNonProduction || !s.DisabledForPlacement {
		t.Fatalf("update = %+v, %v", s, err)
	}
	// The new local server accepted both kinds before.
	audit, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: "server.update", Limit: 1, Platform: true})
	if len(audit) != 1 || !strings.Contains(audit[0].Summary, "no longer accepts non-production, disabled for new placements") {
		t.Fatalf("audit = %+v", audit)
	}
}
