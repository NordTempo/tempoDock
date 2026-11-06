package core

import (
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Switching references to a restored database rewrites service and shared
// references, redeploys who uses them, and switches back the same way.
func TestSwitchReferences(t *testing.T) {
	h := newHarness(t)
	db := h.service("db", apitypes.CreateServiceRequest{})
	h.service("db-restored", apitypes.CreateServiceRequest{})
	api := h.service("api", apitypes.CreateServiceRequest{})
	worker := h.service("worker", apitypes.CreateServiceRequest{})
	env := db.Env()
	h.set(env, "db", map[string]string{"DATABASE_URL": "postgres://db", "PGHOST": "db"}, false)
	h.set(env, "db-restored", map[string]string{"DATABASE_URL": "postgres://db-restored"}, false)
	h.set(env, "api", map[string]string{"DB": "${{db.DATABASE_URL}}", "HOST": "${{ db.PGHOST }}"}, false)
	h.set(env, "", map[string]string{"URL": "${{db.DATABASE_URL}}"}, false)
	h.set(env, "worker", map[string]string{"U": "${{shared.URL}}"}, true)
	for _, ref := range []ServiceRef{api, worker} {
		h.deploy(ref, "app:1")
	}
	sw := func(a Actor, from ServiceRef, to string, dry bool) (apitypes.SwitchReferencesResult, error) {
		return h.c.SwitchReferences(h.ctx, a, from, apitypes.SwitchReferencesRequest{To: to, DryRun: dry})
	}

	if _, err := sw(h.user("reader@example.com", apitypes.ScopeRead), db, "db-restored", true); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("a reader switches: %v, want forbidden", err)
	}
	if _, err := sw(testSystem, db, "nope", true); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("to a missing service: %v, want not found", err)
	}
	if _, err := sw(testSystem, db, "db-restored", true); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "no variable PGHOST") {
		t.Errorf("with a key missing: %v, want it named", err)
	}
	h.set(env, "db-restored", map[string]string{"PGHOST": "db-restored"}, false)
	if _, err := sw(testSystem, db, "db-restored", true); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("with staged changes: %v, want conflict", err)
	}
	h.set(env, "", nil, true)

	dry, err := sw(testSystem, db, "db-restored", true)
	if err != nil {
		t.Fatal(err)
	}
	// The shared variable reaches every service; only api and worker run.
	all := []string{"api", "db", "db-restored", "worker"}
	if !slices.Equal(dry.Services, all) || len(dry.Releases) != 0 {
		t.Fatalf("dry run = %+v", dry)
	}
	if p, _ := h.c.GetChanges(h.ctx, testSystem, env, false); p.Count != 0 {
		t.Fatalf("a dry run left %d staged changes", p.Count)
	}

	res, err := sw(testSystem, db, "db-restored", false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Services, dry.Services) || len(res.Releases) != 2 {
		t.Fatalf("switch = %+v", res)
	}
	for _, r := range res.Releases {
		h.wait(ServiceRef{"shop", "production", r.Service}, r.Number)
	}
	vars, err := h.c.ListVariables(h.ctx, testSystem, env, "", true)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"api.DB": "${{db-restored.DATABASE_URL}}", "api.HOST": "${{db-restored.PGHOST}}", ".URL": "${{db-restored.DATABASE_URL}}",
		"db.DATABASE_URL": "postgres://db", "worker.U": "${{shared.URL}}",
	}
	for _, v := range vars {
		if w, ok := want[v.Service+"."+v.Key]; ok && (v.Value == nil || *v.Value != w) {
			t.Errorf("%s.%s = %v, want %s", v.Service, v.Key, v.Value, w)
		}
	}
	if !slices.ContainsFunc(h.shopAudit(), func(e apitypes.AuditEvent) bool { return e.Action == "service.switch_references" }) {
		t.Error("no service.switch_references audit event")
	}

	back, err := sw(testSystem, ServiceRef{"shop", "production", "db-restored"}, "db", false)
	if err != nil || !slices.Equal(back.Services, all) || len(back.Releases) != 2 {
		t.Fatalf("switching back = %+v, %v", back, err)
	}
}
