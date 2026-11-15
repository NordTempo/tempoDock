package core

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// member creates a user who belongs to each org (slug → role), creating
// the orgs that don't exist, and returns them in no org, as
// authentication first sees them.
func (h *harness) member(email string, orgs map[string]apitypes.Scope) Actor {
	h.t.Helper()
	u, err := h.store.CreateUser(h.ctx, store.CreateUserParams{Email: email, Name: email, PasswordHash: "x", EmailProven: true})
	if err != nil {
		h.t.Fatal(err)
	}
	for slug, role := range orgs {
		var id int64
		err := h.store.DB.QueryRowContext(h.ctx, "SELECT id FROM orgs WHERE slug = ?", slug).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			var o store.Org
			o, err = createOrgTx(h.ctx, h.store.Queries, slug, slug)
			id = o.ID
		}
		if err != nil {
			h.t.Fatal(err)
		}
		// In the seeded group of their role, as a role change puts them.
		if err := joinOrgMember(h.ctx, h.store.Queries, id, u.ID, role); err != nil {
			h.t.Fatal(err)
		}
	}
	return userActor(u)
}

// in puts a user's actor in the org named by slug, as a request with
// ?org=slug does.
func (h *harness) in(a Actor, slug string) Actor {
	h.t.Helper()
	a, err := h.c.actIn(h.ctx, a, slug, apitypes.ScopeAdmin)
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

// Two orgs share nothing: the same project name is two projects, and a
// member of one can't read, change or follow the other's projects,
// tokens, audit log or servers.
func TestOrgsAreIsolated(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"}) // shop in the test org
	h.deploy(web, "app:1")
	bob := h.in(h.member("bob@x", map[string]apitypes.Scope{"acme": apitypes.ScopeAdmin}), "acme")

	projects, err := h.c.ListProjects(h.ctx, bob, false)
	if err != nil || len(projects) != 0 {
		t.Fatalf("acme's projects = %+v, %v; want none", projects, err)
	}
	if _, err := h.c.GetService(h.ctx, bob, web); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("the test org's service from acme: %v, want not found", err)
	}
	if _, err := h.c.CreateProject(h.ctx, bob, apitypes.CreateProjectRequest{Name: "shop"}); err != nil {
		t.Fatalf("acme's own shop: %v", err)
	}
	theirs, _ := h.c.GetProject(h.ctx, bob, "shop")
	ours, _ := h.c.GetProject(h.ctx, testSystem, "shop")
	if theirs.ID == ours.ID {
		t.Fatal("one project for two orgs' shop")
	}

	// Tokens: listing, revoking, and a token acting in another org.
	tok, err := h.c.CreateToken(h.ctx, testSystem, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeDeploy})
	if err != nil {
		t.Fatal(err)
	}
	list, _ := h.c.ListTokens(h.ctx, bob)
	if len(list) != 0 {
		t.Errorf("acme lists the test org's tokens: %+v", list)
	}
	if err := h.c.RevokeToken(h.ctx, bob, tok.Token.ID); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("acme revokes the test org's token: %v", err)
	}
	if _, err := h.c.AuthenticateToken(h.ctx, tok.Secret, "acme"); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("test org token acting in acme: %v", err)
	}

	// The audit log and live events.
	events := h.follow(bob)
	if _, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SetVariables(h.ctx, bob, EnvRef{"shop", "production"}, apitypes.SetVariablesRequest{Set: map[string]string{"B": "1"}}); err != nil {
		t.Fatal(err)
	}
	// The test org's change came first, so had it leaked it would arrive
	// before acme's own.
	for done := false; !done; {
		select {
		case ev := <-events:
			if ev.Audit != nil && ev.Audit.Action == "variables.set" {
				if !strings.Contains(ev.Audit.Summary, "B") {
					t.Fatalf("acme followed the test org's event: %+v", ev.Audit)
				}
				done = true
			}
		case <-time.After(5 * time.Second):
			t.Fatal("acme's own event never came")
		}
	}
	audit, _ := h.c.ListAudit(h.ctx, bob, AuditFilter{})
	for _, ev := range audit {
		if ev.Action != "project.create" && ev.Action != "variables.set" || ev.Action == "variables.set" && !strings.Contains(ev.Summary, "B") {
			t.Errorf("acme's audit has the test org's %s: %s", ev.Action, ev.Summary)
		}
	}

	// Servers are the platform's (ADR 0062): an org admin can't list or
	// read them, and its environments say whether their server is online
	// but not which one it is. Operators see the name.
	if _, err := h.c.ListServers(h.ctx, bob); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("acme's admin lists servers: %v", err)
	}
	if _, err := h.c.GetServer(h.ctx, bob, "local"); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("acme's admin reads a server: %v", err)
	}
	env, err := h.c.GetEnvironment(h.ctx, bob, EnvRef{"shop", "production"})
	if err != nil || env.Server != "" || env.ServerStatus != apitypes.ServerOnline {
		t.Errorf("acme's environment = %+v, %v", env, err)
	}
	if env, err := h.c.GetEnvironment(h.ctx, testSystem, web.Env()); err != nil || env.Server != h.c.localName() {
		t.Errorf("the operator's environment = %+v, %v", env, err)
	}
}

// follow subscribes a to live events and returns them as they come.
func (h *harness) follow(a Actor) <-chan LiveEvent {
	h.t.Helper()
	stream, err := h.c.Events(h.ctx, a, EventFilter{})
	if err != nil {
		h.t.Fatal(err)
	}
	ready, out := make(chan struct{}), make(chan LiveEvent, 64)
	go func() {
		_ = stream(h.ctx, func() { close(ready) }, func(evs ...LiveEvent) error {
			for _, ev := range evs {
				select {
				case out <- ev:
				case <-time.After(5 * time.Second):
				}
			}
			return nil
		})
	}()
	<-ready
	return out
}

// A user with one org acts in it without naming it; with several they
// name one, and its role is theirs; an org that isn't theirs is not found.
func TestUserActsInOneOrg(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	ada := h.member("ada@x", map[string]apitypes.Scope{"test": apitypes.ScopeRead})

	only := h.in(ada, "")
	if only.OrgSlug != "test" || only.Scope != apitypes.ScopeRead {
		t.Fatalf("one org: %+v", only)
	}
	if _, err := h.c.CreateOrg(h.ctx, only, apitypes.CreateOrgRequest{Slug: "side"}); err != nil {
		t.Fatal(err)
	}
	none := h.in(ada, "")
	if _, err := h.c.ListProjects(h.ctx, none, false); err == nil || !strings.Contains(err.Error(), "--org") {
		t.Fatalf("two orgs, none named: %v", err)
	}
	if side := h.in(ada, "side"); side.Scope != apitypes.ScopeAdmin {
		t.Fatalf("in the org she made: %+v", side)
	}
	if _, err := h.c.actIn(h.ctx, ada, "acme", apitypes.ScopeAdmin); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("someone else's org: %v", err)
	}
	if _, err := h.c.CreateOrg(h.ctx, only, apitypes.CreateOrgRequest{Slug: "side"}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("taken slug: %v", err)
	}
	if _, err := h.c.CreateOrg(h.ctx, Actor{Kind: "token", Name: "ci", OrgID: 1, Scope: apitypes.ScopeAdmin}, apitypes.CreateOrgRequest{Slug: "bots"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a token creating an org: %v", err)
	}
}

// The first user of a fresh install is its operator and the admin of its
// first org, with an operator token.
func TestBootstrapCreatesTheFirstOrg(t *testing.T) {
	h := newHarness(t)
	// The harness's test org stands in for a migrated install's; a fresh
	// one has none.
	if _, err := h.store.DB.ExecContext(h.ctx, "DELETE FROM orgs"); err != nil {
		t.Fatal(err)
	}
	b, ok, err := h.c.Bootstrap(h.ctx, "admin@localhost")
	if err != nil || !ok {
		t.Fatalf("bootstrap: %v, %v", ok, err)
	}
	a, err := h.c.AuthenticateToken(h.ctx, b.Token, "")
	if err != nil || !a.Operator || a.OrgSlug != "default" || a.Scope != apitypes.ScopeAdmin {
		t.Fatalf("bootstrap token: %+v, %v", a, err)
	}
	u, _ := h.store.GetUserByEmail(h.ctx, "admin@localhost")
	if ua := h.in(userActor(u), ""); !ua.Operator || ua.OrgSlug != "default" || ua.Scope != apitypes.ScopeAdmin {
		t.Fatalf("first user: %+v", ua)
	}
}
