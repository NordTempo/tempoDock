package core

import (
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Using a template creates its services live and deploys them, with the
// domain in the first release, without applying other staged changes in
// the environment; planning creates nothing.
func TestUseAppTemplate(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "web:1", Port: 3000})
	h.set(web.Env(), "web", map[string]string{"UNRELATED": "1"}, false)
	env := web.Env()

	plan, err := h.c.UseAppTemplate(h.ctx, testSystem, env, "umami", apitypes.UseTemplateRequest{Plan: true, Domain: "stats.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(plan.Services, ","); got != "umami,umami-db" || !strings.Contains(strings.Join(lines(plan.Plan.Changes), "\n"), "production umami-db service add umami-db") {
		t.Fatalf("plan: %s\n%s", got, lines(plan.Plan.Changes))
	}
	if svcs, _ := h.c.ListServices(h.ctx, testSystem, env); len(svcs) != 1 {
		t.Fatalf("planning created services: %d", len(svcs))
	}

	res, err := h.c.UseAppTemplate(h.ctx, testSystem, env, "umami", apitypes.UseTemplateRequest{Domain: "stats.example.com"})
	if err != nil || len(res.Failed) != 0 || len(res.Releases) != 2 {
		t.Fatalf("use: %v %+v", err, res)
	}
	app, db := ServiceRef{"shop", "production", "umami"}, ServiceRef{"shop", "production", "umami-db"}
	for _, ref := range []ServiceRef{app, db} {
		if rel := h.wait(ref, 1); rel.Status != apitypes.ReleaseActive {
			t.Fatalf("%s: %s (%s)", ref.Service, rel.Status, rel.Error)
		}
	}
	// The app deploys once its database is live, not alongside it.
	ops := h.rt.Ops()
	if dbUp, appPull := indexOf(ops, "start "+h.ctr(db, 1)), slices.IndexFunc(ops, func(op string) bool { return strings.HasPrefix(op, "pull ghcr.io/umami-software") }); dbUp < 0 || appPull < dbUp {
		t.Fatalf("umami was pulled before umami-db started: %v", ops)
	}
	spec, _ := h.rt.Spec(h.ctr(app, 1))
	if spec.Env["DOCK_PUBLIC_DOMAIN"] != "stats.example.com" || !strings.HasPrefix(spec.Env["DATABASE_URL"], "postgresql://postgres:") || len(spec.Env["APP_SECRET"]) != 64 {
		t.Fatalf("umami's first release env: %v", spec.Env)
	}
	svc, err := h.c.GetService(h.ctx, testSystem, app)
	if err != nil || svc.FromTemplate == nil || svc.FromTemplate.Name != "umami" || svc.FromTemplate.Revision < 1 {
		t.Fatalf("from template: %v %+v", err, svc.FromTemplate)
	}
	if pc, _ := h.c.GetChanges(h.ctx, testSystem, env, false); pc.Count != 1 {
		t.Fatalf("the unrelated staged change: %+v", pc)
	}

	// Its names are taken now; another name creates a second copy.
	if _, err := h.c.UseAppTemplate(h.ctx, testSystem, env, "umami", apitypes.UseTemplateRequest{}); CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("taken name: %v", err)
	}
	if _, err := h.c.UseAppTemplate(h.ctx, testSystem, env, "umami", apitypes.UseTemplateRequest{Name: "stats", Plan: true}); err != nil {
		t.Fatalf("another name: %v", err)
	}
}

// Inputs reach their services (secrets sealed), unknown templates and
// inputs are refused with what to do, and a read token may only plan.
func TestUseAppTemplateInputsAndScopes(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{Image: "web:1"})
	env := EnvRef{"shop", "production"}
	if _, err := h.c.UseAppTemplate(h.ctx, testSystem, env, "nope", apitypes.UseTemplateRequest{}); CodeOf(err) != apitypes.CodeNotFound || !strings.Contains(err.Error(), "dock template list") {
		t.Fatalf("unknown template: %v", err)
	}
	if _, err := h.c.UseAppTemplate(h.ctx, testSystem, env, "grafana", apitypes.UseTemplateRequest{}); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "admin password") {
		t.Fatalf("missing input: %v", err)
	}
	reader := h.token(testSystem, apitypes.CreateTokenRequest{Name: "reader", Scope: apitypes.ScopeRead})
	req := apitypes.UseTemplateRequest{Inputs: map[string]string{"GF_SECURITY_ADMIN_PASSWORD": "s3cret"}}
	if _, err := h.c.UseAppTemplate(h.ctx, reader, env, "grafana", req); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("read token use: %v", err)
	}
	req.Plan = true
	if _, err := h.c.UseAppTemplate(h.ctx, reader, env, "grafana", req); err != nil {
		t.Fatalf("read token plan: %v", err)
	}
	req.Plan = false
	if _, err := h.c.UseAppTemplate(h.ctx, testSystem, env, "grafana", req); err != nil {
		t.Fatal(err)
	}
	vars := h.revealed(ServiceRef{"shop", "production", "grafana"})
	if vars["GF_SECURITY_ADMIN_PASSWORD"] != "s3cret" || vars["GF_SECURITY_ADMIN_USER"] != "admin" {
		t.Fatalf("grafana's variables: %v", vars)
	}
	// A database is a template too.
	if res, err := h.c.UseAppTemplate(h.ctx, testSystem, env, "postgres", apitypes.UseTemplateRequest{Name: "db"}); err != nil || res.Services[0] != "db" {
		t.Fatalf("postgres: %v %+v", err, res)
	}
}

// A start command in exec form starts the container with its arguments
// and no shell; a malformed one is refused when set.
func TestStartCommandExecForm(t *testing.T) {
	h := newHarness(t)
	w := h.service("worker", apitypes.CreateServiceRequest{Image: "ak:1", StartCommand: `["worker", "--queue", "mail"]`})
	h.deploy(w, "ak:1")
	spec, _ := h.rt.Spec(h.ctr(w, 1))
	if strings.Join(spec.Args, " ") != "worker --queue mail" || spec.Cmd != "" {
		t.Fatalf("args %q, cmd %q", spec.Args, spec.Cmd)
	}
	bad := `["worker"`
	if _, err := h.c.UpdateService(h.ctx, testSystem, w, apitypes.UpdateServiceRequest{StartCommand: &bad}); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "exec form") {
		t.Fatalf("malformed: %v", err)
	}
}
