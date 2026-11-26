package core

import (
	"errors"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// lock locks shop/<env> as the system.
func (h *harness) lock(env string) {
	h.t.Helper()
	if _, err := h.c.LockEnvironment(h.ctx, testSystem, EnvRef{"shop", env}, apitypes.LockEnvironmentRequest{}); err != nil {
		h.t.Fatal(err)
	}
}

// freeze locks shop/<env> for people too, as the system.
func (h *harness) freeze(env string) {
	h.t.Helper()
	if _, err := h.c.LockEnvironment(h.ctx, testSystem, EnvRef{"shop", env}, apitypes.LockEnvironmentRequest{People: true}); err != nil {
		h.t.Fatal(err)
	}
}

// TestLockStopsTokensOnly: in a locked environment a token's change is
// refused with environment_locked, whether an API token or a `dock login`
// token, while a person in the UI and the daemon change it freely and
// reads stay open to everyone.
func TestLockStopsTokensOnly(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.lock("production")
	set := apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"A": "1"}}
	agent := Actor{Kind: "token", OrgID: 1, Name: "agent", TokenID: 9, Scope: apitypes.ScopeAdmin}
	cliLogin := Actor{Kind: "token", OrgID: 1, Name: "cli", TokenID: 10, Scope: apitypes.ScopeAdmin, Email: "ada@example.com", UserID: 1}
	user := Actor{Kind: "user", OrgID: 1, Name: "ada@example.com", UserID: 1, Scope: apitypes.ScopeAdmin}
	for _, a := range []Actor{agent, cliLogin} {
		_, err := h.c.SetVariables(h.ctx, a, web.Env(), set)
		le, ok := errors.AsType[*LockedError](err)
		if CodeOf(err) != apitypes.CodeEnvironmentLocked || !ok || le.Environment != "production" || le.Action != "setting variables" {
			t.Fatalf("%s: %v", a, err)
		}
		if _, err := h.c.ListReleases(h.ctx, a, web, 5); err != nil {
			t.Fatalf("%s reads: %v", a, err)
		}
	}
	for _, a := range []Actor{user, testSystem} {
		if _, err := h.c.SetVariables(h.ctx, a, web.Env(), set); err != nil {
			t.Fatalf("%s: %v", a, err)
		}
	}
}

// TestLockOpenUntil: an opened lock lets tokens in until its time, then
// holds again by itself.
func TestLockOpenUntil(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.lock("production")
	e, err := h.c.UnlockEnvironment(h.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.UnlockEnvironmentRequest{Minutes: 10})
	if err != nil || e.Lock == nil || e.Lock.OpenUntil == nil {
		t.Fatalf("open: %+v %v", e, err)
	}
	es, err := h.c.loadEnv(h.ctx, 1, EnvRef{"shop", "production"})
	if err != nil {
		t.Fatal(err)
	}
	if lockHolds(es.env, now()) || !lockHolds(es.env, now().Add(11*time.Minute)) {
		t.Fatalf("open until %v: holds now or not after", es.env.OpenUntil)
	}
	if e.Lock.Holds(now()) {
		t.Fatal("API lock holds while open")
	}
}

// TestLockedProjectCantBeArchivedByTokens: archiving a project changes
// every environment, so one locked environment stops a token.
func TestLockedProjectCantBeArchivedByTokens(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.lock("production")
	agent := Actor{Kind: "token", OrgID: 1, Name: "agent", TokenID: 9, Scope: apitypes.ScopeAdmin}
	if _, err := h.c.ArchiveProject(h.ctx, agent, "shop"); CodeOf(err) != apitypes.CodeEnvironmentLocked {
		t.Fatalf("archive: %v", err)
	}
}

// TestLockedDataProposes: in a locked environment a token's data level is
// capped at propose, even an admin token's on non-production, so its
// changes wait for an admin in the Data panel.
func TestLockedDataProposes(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	agent := Actor{Kind: "token", OrgID: 1, Name: "agent", TokenID: 9, Scope: apitypes.ScopeAdmin}
	es, err := h.c.loadEnv(h.ctx, 1, EnvRef{"shop", "staging"})
	if err != nil {
		t.Fatal(err)
	}
	if perm, _ := h.c.dataPermission(h.ctx, agent, es); perm.Level != apitypes.LevelWrite {
		t.Fatalf("unlocked: %+v", perm)
	}
	h.lock("staging")
	if es, err = h.c.loadEnv(h.ctx, 1, EnvRef{"shop", "staging"}); err != nil {
		t.Fatal(err)
	}
	perm, err := h.c.dataPermission(h.ctx, agent, es)
	last := perm.Why[len(perm.Why)-1]
	if err != nil || perm.Level != apitypes.LevelPropose || perm.Approve || perm.Manage || last.Source != apitypes.PermissionLocked {
		t.Fatalf("locked: %+v %v", perm, err)
	}
	user := Actor{Kind: "user", OrgID: 1, Name: "ada@example.com", UserID: 1, Scope: apitypes.ScopeAdmin}
	if perm, _ := h.c.dataPermission(h.ctx, user, es); perm.Level != apitypes.LevelWrite {
		t.Fatalf("user in locked: %+v", perm)
	}
}

// TestNewProductionStartsLocked: with the org setting on, production
// environments start locked and others don't.
func TestNewProductionStartsLocked(t *testing.T) {
	h := newHarness(t)
	if _, err := h.c.SetOrgLockSettings(h.ctx, testSystem, apitypes.OrgLockSettings{LockProduction: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CreateProject(h.ctx, testSystem, apitypes.CreateProjectRequest{Name: "shop"}); err != nil {
		t.Fatal(err)
	}
	prod, err := h.c.GetEnvironment(h.ctx, testSystem, EnvRef{"shop", "production"})
	if err != nil || prod.Lock == nil || !prod.Lock.Holds(now()) {
		t.Fatalf("production: %+v %v", prod, err)
	}
	staging, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"})
	if err != nil || staging.Lock != nil {
		t.Fatalf("staging: %+v %v", staging, err)
	}
}
