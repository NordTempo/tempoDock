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

// A template's scheduled jobs are set on its new services, and a start
// command in exec form reaches the container as arguments.
func TestUseAppTemplateJobsAndExecForm(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{Image: "web:1"})
	env := EnvRef{"shop", "production"}
	if _, err := h.c.UseAppTemplate(h.ctx, testSystem, env, "nextcloud", apitypes.UseTemplateRequest{
		Domain: "cloud.example.com", Inputs: map[string]string{"NEXTCLOUD_ADMIN_PASSWORD": "pw"},
	}); err != nil {
		t.Fatal(err)
	}
	jobs, err := h.c.ListJobs(h.ctx, testSystem, ServiceRef{"shop", "production", "nextcloud"})
	if err != nil || len(jobs) != 1 || jobs[0].Name != "cron" || jobs[0].Schedule != "*/5 * * * *" {
		t.Fatalf("jobs: %v %+v", err, jobs)
	}

	res, err := h.c.UseAppTemplate(h.ctx, testSystem, env, "authentik", apitypes.UseTemplateRequest{
		Domain: "id.example.com", Inputs: map[string]string{"AUTHENTIK_BOOTSTRAP_EMAIL": "me@example.com", "AUTHENTIK_BOOTSTRAP_PASSWORD": "pw"},
	})
	if err != nil || len(res.Failed) != 0 {
		t.Fatalf("authentik: %v %+v", err, res.Failed)
	}
	worker := ServiceRef{"shop", "production", "authentik-worker"}
	h.wait(worker, 1)
	if spec, _ := h.rt.Spec(h.ctr(worker, 1)); strings.Join(spec.Args, " ") != "worker" {
		t.Fatalf("worker args %q", spec.Args)
	}
}

// An environment's services saved as an organization template: settings,
// references and database templates kept, secrets turned into inputs,
// listed for the org only, and used like Dock's own. Members can't save.
func TestOrgTemplates(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	api := h.service("api", apitypes.CreateServiceRequest{Image: "acme/api:2", Port: 3000})
	h.set(api.Env(), "api", map[string]string{"DATABASE_URL": "${{db.DATABASE_URL}}", "STRIPE_KEY": "sk_live_1"}, true)
	if _, err := h.c.GenerateDomain(h.ctx, testSystem, api); err != nil {
		t.Fatal(err)
	}
	reader := h.token(testSystem, apitypes.CreateTokenRequest{Name: "r", Scope: apitypes.ScopeDeploy})
	req := apitypes.SaveTemplateFromRequest{Name: "shop-stack", Services: []string{"api", "db"}}
	if _, err := h.c.SaveAppTemplateFrom(h.ctx, reader, db.Env(), req); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("deploy token saving: %v", err)
	}
	saved, err := h.c.SaveAppTemplateFrom(h.ctx, testSystem, db.Env(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Org || !saved.Domain || len(saved.Inputs) != 1 || saved.Inputs[0].Key != "STRIPE_KEY" || saved.Inputs[0].Kind != apitypes.InputSecret {
		t.Fatalf("saved = %+v", saved)
	}
	file, _ := h.c.AppTemplateFile(h.ctx, testSystem, "shop-stack")
	if strings.Contains(file, "sk_live_1") || !strings.Contains(file, "${{db.DATABASE_URL}}") || !strings.Contains(file, `main = 'api'`) {
		t.Fatalf("file:\n%s", file)
	}
	list, _ := h.c.ListAppTemplates(h.ctx, testSystem)
	if list[0].Name != "shop-stack" {
		t.Fatalf("first listed: %s", list[0].Name)
	}

	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	res, err := h.c.UseAppTemplate(h.ctx, testSystem, EnvRef{"shop", "staging"}, "shop-stack",
		apitypes.UseTemplateRequest{Name: "api", Domain: "staging.example.com", Inputs: map[string]string{"STRIPE_KEY": "sk_test_2"}})
	if err != nil || strings.Join(res.Services, ",") != "api,api-db" {
		t.Fatalf("use: %v %+v", err, res)
	}
	vars := h.revealed(ServiceRef{"shop", "staging", "api"})
	if vars["STRIPE_KEY"] != "sk_test_2" || vars["DATABASE_URL"] != "${{api-db.DATABASE_URL}}" {
		t.Fatalf("staging api variables: %v", vars)
	}

	if _, err := h.c.SaveAppTemplate(h.ctx, testSystem, apitypes.SaveTemplateRequest{File: strings.Replace(file, "name = 'shop-stack'", "name = 'umami'", 1)}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("saving over Dock's umami: %v", err)
	}
	if err := h.c.RemoveAppTemplate(h.ctx, testSystem, "shop-stack"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.GetAppTemplate(h.ctx, testSystem, "shop-stack"); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("removed: %v", err)
	}
}
