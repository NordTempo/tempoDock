package core

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// exportTOML is a project's export as `dock project export` prints it.
func (h *harness) exportTOML(project string) (apitypes.ProjectExport, string) {
	h.t.Helper()
	ex, err := h.c.ExportProject(h.ctx, testSystem, project)
	if err != nil {
		h.t.Fatal(err)
	}
	b, err := ex.TOML()
	if err != nil {
		h.t.Fatal(err)
	}
	return ex, string(b)
}

// lines renders plan changes as "env service kind action name" for
// comparisons.
func lines(chs []apitypes.PlanChange) []string {
	out := make([]string, len(chs))
	for i, c := range chs {
		out[i] = strings.Join(slices.DeleteFunc([]string{c.Environment, c.Service, string(c.Kind), string(c.Action), c.Name}, func(f string) bool { return f == "" }), " ")
	}
	return out
}

// Exporting a project and applying the file stages nothing: the export is
// the live state, sorted, and the plan finds no difference.
func TestApplyExportRoundTrip(t *testing.T) {
	h := newHarness(t)
	cloneSource(h)
	web := ServiceRef{"shop", "production", "web"}
	if _, err := h.c.AddVolume(h.ctx, testSystem, web, apitypes.AddVolumeRequest{Name: "uploads", MountPath: "/uploads", Apply: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.GenerateDomain(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "dev"}); err != nil {
		t.Fatal(err)
	}
	h.set(EnvRef{"shop", "dev"}, "", map[string]string{"API": "${{shared.X}}", "X": "x"}, true)

	_, file := h.exportTOML("shop")
	plan, err := h.c.PlanProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file, Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes)+len(plan.Kept)+len(plan.Manual) != 0 {
		t.Fatalf("plan of the export:\nchanges %v\nkept %v\nmanual %v\n%s", lines(plan.Changes), lines(plan.Kept), lines(plan.Manual), file)
	}
	before, _ := h.c.GetChanges(h.ctx, testSystem, EnvRef{"shop", "production"}, false)
	res, err := h.c.ApplyProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Staged) != 2 || res.Staged[1].Count != before.Count {
		t.Fatalf("applying the export staged something: %+v (before: %d)", res.Staged, before.Count)
	}
}

// A service's pre-deploy command and timeout are in its export, and a file
// that changes them stages the change.
func TestApplyPreDeployCommand(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "web:1", PreDeployCommand: "./migrate up", PreDeployTimeoutSeconds: 90})
	ex, file := h.exportTOML("shop")
	if s := ex.Environments[0].Services[0]; s.PreDeployCommand != "./migrate up" || s.PreDeployTimeoutSeconds != 90 {
		t.Fatalf("export: %+v", s)
	}
	file = strings.Replace(file, "./migrate up", "./migrate all", 1)
	plan, err := h.c.PlanProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(lines(plan.Changes), []string{"production web setting update pre_deploy_command"}) {
		t.Fatalf("plan: %q\n%s", lines(plan.Changes), file)
	}
	if _, err := h.c.ApplyProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file}); err != nil {
		t.Fatal(err)
	}
	ch, err := h.c.GetChanges(h.ctx, testSystem, web.Env(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Settings) != 1 || ch.Settings[0].New != "./migrate all" {
		t.Fatalf("staged: %+v", ch.Settings)
	}
}

// A file giving a service a name the platform keeps under the base domain
// says so in the plan, with the refusal adding the domain gives, and the
// rest of the file applies; an operator's plan adds it.
func TestApplyReservedHostname(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{Image: "web:1", Port: 3000})
	alice := h.in(h.member("alice@x", map[string]apitypes.Scope{"test": apitypes.ScopeAdmin}), "test")
	req := apitypes.ApplyRequest{File: `version = 1
project = "shop"
[[environments]]
  name = "production"
  [[environments.services]]
    name = "web"
    image = "web:1"
    port = 3000
    domains = [{hostname = "status.apps.test"}, {hostname = "shop.apps.test"}]
`}
	const reserved, free = "production web domain add status.apps.test", "production web domain add shop.apps.test"
	plan, err := h.c.PlanProject(h.ctx, alice, "shop", req)
	if err != nil {
		t.Fatal(err)
	}
	_, want := h.c.AddDomain(h.ctx, alice, ServiceRef{"shop", "production", "web"}, apitypes.AddDomainRequest{Hostname: "status.apps.test"})
	i := slices.Index(lines(plan.Manual), reserved)
	if want == nil || i < 0 || plan.Manual[i].Note != want.Error() || slices.Contains(lines(plan.Changes), reserved) || !slices.Contains(lines(plan.Changes), free) {
		t.Fatalf("a member's plan: changes %q, manual %+v; adding the domain says %v", lines(plan.Changes), plan.Manual, want)
	}
	if _, err := h.c.ApplyProject(h.ctx, alice, "shop", req); err != nil {
		t.Fatalf("applying the rest of the file: %v", err)
	}
	if plan, err = h.c.PlanProject(h.ctx, testSystem, "shop", req); err != nil || !slices.Contains(lines(plan.Changes), reserved) {
		t.Fatalf("an operator's plan: %v, changes %q, manual %q", err, lines(plan.Changes), lines(plan.Manual))
	}
}

// A file adding a hostname another service has plans it as needing a
// person, not as a change that would fail once applied; one the file
// takes off its service (--prune) is free.
func TestApplyTakenHostname(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "web:1", Port: 3000})
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	file := func(webDomains string) string {
		return `version = 1
project = "shop"
[[environments]]
  name = "production"
  [[environments.services]]
    name = "api"
    image = "api:1"
    domains = [{hostname = "shop.example.com"}]
  [[environments.services]]
    name = "web"
    image = "web:1"
    port = 3000
    domains = [` + webDomains + `]
`
	}
	const add = "production api domain add shop.example.com"
	plan, err := h.c.PlanProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file(`{hostname = "shop.example.com"}`)})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(lines(plan.Manual), add)
	if i < 0 || slices.Contains(lines(plan.Changes), add) || !strings.Contains(plan.Manual[i].Note, "already in use") {
		t.Fatalf("taken: changes %q, manual %+v", lines(plan.Changes), plan.Manual)
	}
	plan, err = h.c.PlanProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file(""), Prune: true})
	if err != nil || !slices.Contains(lines(plan.Changes), add) {
		t.Fatalf("moved: %v %q", err, lines(plan.Changes))
	}
}

// The property behind config as code: a project exported, applied as a
// new project with its secrets' values, and applied live exports the same
// as the original. Random projects: services with random settings,
// templates, values, references, secrets and volumes over two
// environments.
func TestApplyRecreatesExportedProjects(t *testing.T) {
	for seed := range uint64(12) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			h := newHarness(t)
			rnd := rand.New(rand.NewPCG(seed, 41))
			secrets := randomProject(h, rnd)
			ex, file := h.exportTOML("src")

			var values []apitypes.VariableValue
			for _, e := range ex.Environments {
				for _, k := range e.SharedVariables {
					values = append(values, apitypes.VariableValue{Environment: e.Name, Key: k, Value: secrets[e.Name+"/."+k]})
				}
				for _, s := range e.Services {
					for _, k := range s.Variables {
						// A template's own variables are made anew.
						if v, ok := secrets[e.Name+"/"+s.Name+"."+k]; ok {
							values = append(values, apitypes.VariableValue{Environment: e.Name, Service: s.Name, Key: k, Value: v})
						}
					}
				}
			}
			plan, err := h.c.PlanProject(h.ctx, testSystem, "copy", apitypes.ApplyRequest{File: file, Values: values})
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Manual) != 0 || len(plan.Kept) != 0 || plan.Changes[0].Kind != apitypes.PlanProject {
				t.Fatalf("plan: manual %v, kept %v", lines(plan.Manual), lines(plan.Kept))
			}
			if _, err := h.c.ApplyProject(h.ctx, testSystem, "copy", apitypes.ApplyRequest{File: file, Values: values, Deploy: true}); err != nil {
				t.Fatal(err)
			}
			got, _ := h.c.ExportProject(h.ctx, testSystem, "copy")
			got.Project = ex.Project
			if !reflect.DeepEqual(got, ex) {
				a, _ := json.MarshalIndent(ex, "", " ")
				b, _ := json.MarshalIndent(got, "", " ")
				t.Fatalf("export of the copy differs:\nsource %s\ncopy %s", a, b)
			}
			for k, want := range secrets {
				env, rest, _ := strings.Cut(k, "/")
				svc, key, _ := strings.Cut(rest, ".")
				if have := h.revealedIn(EnvRef{"copy", env}, svc)[key]; have != want {
					t.Fatalf("%s = %q, want %q", k, have, want)
				}
			}
			// And the copy's own export plans no change against itself.
			_, again := h.exportTOML("copy")
			if p, err := h.c.PlanProject(h.ctx, testSystem, "copy", apitypes.ApplyRequest{File: again, Prune: true}); err != nil || len(p.Changes)+len(p.Kept) != 0 {
				t.Fatalf("copy's own plan: %v %v, %v", lines(p.Changes), lines(p.Kept), err)
			}
		})
	}
}

// revealedIn returns a scope's live values: a service's, or the shared
// ones for "".
func (h *harness) revealedIn(env EnvRef, service string) map[string]string {
	h.t.Helper()
	vars, err := h.c.ListVariables(h.ctx, testSystem, env, service, true)
	if err != nil {
		h.t.Fatal(err)
	}
	out := map[string]string{}
	for _, v := range vars {
		if v.Value != nil && (v.Service == service || service == "" && v.Scope == apitypes.ScopeEnvironment) {
			out[v.Key] = *v.Value
		}
	}
	return out
}

// randomProject makes project src with random content and returns its
// literal (secret) values by "env/service.KEY" ("env/.KEY" for shared).
func randomProject(h *harness, rnd *rand.Rand) map[string]string {
	h.t.Helper()
	if _, err := h.c.CreateProject(h.ctx, testSystem, apitypes.CreateProjectRequest{Name: "src"}); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "src", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		h.t.Fatal(err)
	}
	secrets := map[string]string{}
	pick := func(xs ...string) string { return xs[rnd.IntN(len(xs))] }
	for _, env := range []string{"production", "staging"} {
		ref := EnvRef{"src", env}
		var names []string
		for i := range 1 + rnd.IntN(4) {
			name := fmt.Sprintf("s%d", i)
			req := apitypes.CreateServiceRequest{Name: name}
			switch rnd.IntN(5) {
			case 0:
				req.Template = apitypes.Template(pick("postgres", "redis"))
			case 1:
				req.DockerfilePath = pick("Dockerfile", "docker/Dockerfile.prod")
				req.BuildCommand = ""
			default:
				req.Image = pick("nginx:1.27", "ghcr.io/acme/web:1.4", "redis:8")
			}
			if req.Template == "" {
				req.Port = pick0(rnd, 0, 80, 3000)
				req.MemoryMB = pick0(rnd, 0, 256, 1024)
				if rnd.IntN(2) == 0 {
					req.HealthCheckPath, req.HealthCheckTimeoutSeconds = "/up", pick0(rnd, 0, 30)
				}
				if rnd.IntN(3) == 0 {
					req.StartCommand = "node server.js --flag=" + pick("a", "b")
				}
				if rnd.IntN(3) == 0 {
					req.CPUs = 0.5
				}
				if rnd.IntN(3) == 0 {
					req.PreDeployCommand, req.PreDeployTimeoutSeconds = "./migrate --"+pick("up", "all"), pick0(rnd, 0, 120)
				}
			}
			if _, err := h.c.CreateService(h.ctx, testSystem, ref, req); err != nil {
				h.t.Fatal(err)
			}
			if req.Template != "" {
				// A template service deploys itself; wait so its first
				// release doesn't race the copy's export.
				h.wait(ServiceRef{"src", env, name}, 1)
			} else if rnd.IntN(2) == 0 {
				if _, err := h.c.AddVolume(h.ctx, testSystem, ServiceRef{"src", env, name}, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
					h.t.Fatal(err)
				}
			}
			names = append(names, name)
		}
		shared := map[string]string{"LEVEL": fmt.Sprintf("lvl-%d", rnd.IntN(1000))}
		secrets[env+"/.LEVEL"] = shared["LEVEL"]
		h.set(ref, "", shared, false)
		for _, name := range names {
			vars := map[string]string{}
			for i := range rnd.IntN(3) {
				k := fmt.Sprintf("SECRET_%d", i)
				vars[k] = fmt.Sprintf("v-%d", rnd.Int())
				secrets[env+"/"+name+"."+k] = vars[k]
			}
			if rnd.IntN(2) == 0 {
				vars["LINK"] = "${{shared.LEVEL}}"
			}
			if other := pick(names...); other != name && rnd.IntN(2) == 0 {
				vars["PEER"] = "${{" + other + ".PORT}}${{shared.LEVEL}}"
			}
			if len(vars) > 0 {
				h.set(ref, name, vars, false)
			}
		}
		if _, err := h.c.ApplyChanges(h.ctx, testSystem, ref); err != nil {
			h.t.Fatal(err)
		}
	}
	return secrets
}

func pick0(rnd *rand.Rand, xs ...int) int { return xs[rnd.IntN(len(xs))] }

// A plan lists what applying stages, what the file leaves out (kept) and
// what needs a person; applying stages exactly that and nothing goes live
// until the environment's changes are applied, when the new image service
// gets its first release.
func TestApplyPlanStagesTheDiff(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "web:1", Port: 3000})
	h.service("old", apitypes.CreateServiceRequest{Image: "old:1"})
	h.set(web.Env(), "web", map[string]string{"KEEP": "k", "MODE": "a"}, true)
	h.deploy(web, "web:1")
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "old.example.com"}); err != nil {
		t.Fatal(err)
	}

	file := `version = 1
project = "shop"
[[environments]]
  name = "production"
  [[environments.services]]
    name = "web"
    image = "web:1"
    port = 8080
    variables = ["STRIPE_KEY"]
    volumes = [{ name = "uploads", mount_path = "/uploads" }]
    domains = [{ hostname = "shop.example.com" }]
    [environments.services.values]
      MODE = "b"
      API = "${{db.POSTGRES_DB}}"
  [[environments.services]]
    name = "api"
    image = "api:2"
    depends_on = ["db"]
    [environments.services.values]
      DATABASE_URL = "${{db.DATABASE_URL}}"
  [[environments.services]]
    name = "db"
    template = "postgres"
`
	plan, err := h.c.PlanProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file})
	if err != nil {
		t.Fatal(err)
	}
	wantChanges := []string{
		"production api service add api",
		"production api setting add image",
		"production api variable add DATABASE_URL",
		"production db service add db",
		"production web setting update port",
		"production web variable add API",
		"production web variable update MODE",
		"production web volume add uploads",
		"production web domain add shop.example.com",
	}
	wantKept := []string{
		"production web variable remove KEEP",
		"production web domain remove old.example.com",
		"production old service remove old",
	}
	if !slices.Equal(lines(plan.Changes), wantChanges) || !slices.Equal(lines(plan.Kept), wantKept) ||
		!slices.Equal(lines(plan.Manual), []string{"production web variable add STRIPE_KEY"}) {
		t.Fatalf("plan:\nchanges %q\nkept %q\nmanual %q", lines(plan.Changes), lines(plan.Kept), lines(plan.Manual))
	}

	res, err := h.c.ApplyProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file})
	if err != nil {
		t.Fatal(err)
	}
	staged := res.Staged[0]
	if len(staged.Services) != 2 || len(staged.Domains) != 1 || len(staged.Volumes) != 2 || len(staged.Settings) != 1 {
		t.Fatalf("staged = %+v", staged)
	}
	// Nothing is live: web runs as before, the new services run nothing.
	if s, _ := h.c.GetService(h.ctx, testSystem, web); s.Port != 3000 {
		t.Fatalf("port went live before apply: %d", s.Port)
	}
	api, err := h.c.GetService(h.ctx, testSystem, ServiceRef{"shop", "production", "api"})
	if err != nil || api.Staged != "create" || api.ActiveRelease != nil {
		t.Fatalf("api = %+v, %v", api, err)
	}
	// Staging again is a no-op that says so and audits nothing; the plan
	// is still against live values.
	audits := func() int {
		evs, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: "changes.stage_file"})
		return len(evs)
	}
	n := audits()
	if again, err := h.c.ApplyProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file}); err != nil || again.Staged[0].Count != staged.Count ||
		!slices.Equal(again.Unchanged, []string{"production"}) || len(again.Plan.Changes) == 0 || audits() != n {
		t.Fatalf("staging twice: %+v, %v (%d audits, %d before)", again, err, audits(), n)
	}
	// A staged service can't be promoted or restarted into running.
	if _, err := h.c.Restart(h.ctx, testSystem, ServiceRef{"shop", "production", "api"}); err == nil {
		t.Fatal("restarted a staged service")
	}

	applied, err := h.c.ApplyChanges(h.ctx, testSystem, web.Env())
	if err != nil {
		t.Fatal(err)
	}
	var started []string
	for _, r := range applied.Releases {
		started = append(started, r.Service)
	}
	// db (template) before api; web gets a config release.
	if !slices.Equal(started, []string{"web", "db", "api"}) || len(applied.Failed) != 0 {
		t.Fatalf("started %v, failed %v", started, applied.Failed)
	}
	if s, _ := h.c.GetService(h.ctx, testSystem, ServiceRef{"shop", "production", "api"}); s.Staged != "" {
		t.Fatalf("api still staged: %+v", s)
	}
	domains, _ := h.c.ListDomains(h.ctx, testSystem, web)
	if len(domains) != 2 {
		t.Fatalf("domains = %+v", domains)
	}
	if p, _ := h.c.GetChanges(h.ctx, testSystem, web.Env(), false); p.Count != 0 {
		t.Fatalf("left staged: %+v", p)
	}
}

// Removals need --prune. With it, a deploy token may stage variable and
// domain removals but not a service's archive; an admin's staged archive
// can only be applied by an admin. Volumes and a template's own variables
// are never removed.
func TestApplyPruneSafety(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "web:1"})
	h.service("old", apitypes.CreateServiceRequest{Image: "old:1"})
	db := h.templateDB("db", apitypes.TemplatePostgres)
	h.set(web.Env(), "web", map[string]string{"GONE": "x", "KEEP": "y"}, true)
	if _, err := h.c.AddVolume(h.ctx, testSystem, web, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data", Apply: true}); err != nil {
		t.Fatal(err)
	}
	full := `version = 1
[[environments]]
  name = "production"
  [[environments.services]]
    name = "web"
    image = "web:1"
    variables = ["KEEP"]
  [[environments.services]]
    name = "db"
    template = "postgres"
`
	noOld := full
	withOld := full + `  [[environments.services]]
    name = "old"
    image = "old:1"
`
	plan, err := h.c.PlanProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: noOld})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 || !slices.Equal(lines(plan.Kept), []string{"production web variable remove GONE", "production web volume remove data", "production old service remove old"}) {
		t.Fatalf("plan without prune: changes %v, kept %v", lines(plan.Changes), lines(plan.Kept))
	}

	deployer := Actor{Name: "ci", Kind: "token", OrgID: 1, Scope: apitypes.ScopeDeploy}
	if _, err := h.c.ApplyProject(h.ctx, deployer, "shop", apitypes.ApplyRequest{File: noOld, Prune: true}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("deploy token pruned a service: %v", err)
	}
	res, err := h.c.ApplyProject(h.ctx, deployer, "shop", apitypes.ApplyRequest{File: withOld, Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Staged[0]; len(got.Variables) != 1 || got.Variables[0].Key != "GONE" || got.Variables[0].Action != apitypes.ChangeRemove || len(got.Services) != 0 {
		t.Fatalf("deploy token's prune staged %+v", got)
	}
	if _, err := h.c.DiscardChanges(h.ctx, testSystem, web.Env(), apitypes.DiscardChangesRequest{}); err != nil {
		t.Fatal(err)
	}

	if _, err := h.c.ApplyProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: noOld, Prune: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.ApplyChanges(h.ctx, deployer, web.Env()); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("deploy token applied an archive: %v", err)
	}
	if _, err := h.c.ApplyChanges(h.ctx, testSystem, web.Env()); err != nil {
		t.Fatal(err)
	}
	if s, _ := h.c.GetService(h.ctx, testSystem, ServiceRef{"shop", "production", "old"}); s.ArchivedAt == nil {
		t.Fatal("old wasn't archived")
	}
	if vols, _ := h.c.ListVolumes(h.ctx, testSystem, web); len(vols) != 1 {
		t.Fatalf("volumes = %+v", vols)
	}
	if vars := h.revealed(db); len(vars) != 4 {
		t.Fatalf("template variables pruned: %v", vars)
	}
	if vars := h.revealed(web); len(vars) != 1 || vars["KEEP"] != "y" {
		t.Fatalf("web variables = %v", vars)
	}
}

// A secret is named in the file, never valued: the plan asks for it, a
// value sent with the file is staged and never shown back, and one set
// on the staged service before applying counts as given.
func TestApplySecrets(t *testing.T) {
	h := newHarness(t)
	file := `version = 1
[[environments]]
  name = "production"
  [[environments.services]]
    name = "web"
    image = "web:1"
    variables = ["STRIPE_KEY", "OTHER"]
`
	plan, err := h.c.PlanProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(lines(plan.Manual), []string{"production web variable add OTHER", "production web variable add STRIPE_KEY"}) {
		t.Fatalf("manual = %v", lines(plan.Manual))
	}
	if _, err := h.c.PlanProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file, Values: []apitypes.VariableValue{{Environment: "production", Service: "web", Key: "NOPE", Value: "x"}}}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("a value the file doesn't name: %v", err)
	}
	res, err := h.c.ApplyProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file,
		Values: []apitypes.VariableValue{{Environment: "production", Service: "web", Key: "STRIPE_KEY", Value: "sk_live_123"}}})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res); strings.Contains(string(b), "sk_live_123") {
		t.Fatalf("result shows the secret: %s", b)
	}
	h.set(EnvRef{"shop", "production"}, "web", map[string]string{"OTHER": "o"}, false)
	plan, _ = h.c.PlanProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file})
	if len(plan.Manual) != 0 {
		t.Fatalf("manual after setting both: %v", lines(plan.Manual))
	}
}

// A plan doesn't tell a token that can't reveal secrets whether a value it
// sends matches the live one: right and wrong guesses both plan an update.
func TestPlanHidesSecretEquality(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{Image: "web:1"})
	h.set(EnvRef{"shop", "production"}, "web", map[string]string{"STRIPE_KEY": "sk_live_123"}, true)
	p, _ := h.c.GetProject(h.ctx, testSystem, "shop")
	reader := Actor{Kind: "token", OrgID: 1, Name: "ci", Scope: apitypes.ScopeRead, ProjectID: p.ID, ProjectName: "shop"}
	file := `version = 1
[[environments]]
  name = "production"
  [[environments.services]]
    name = "web"
    image = "web:1"
    variables = ["STRIPE_KEY"]
`
	plan := func(a Actor, guess string) []string {
		t.Helper()
		pl, err := h.c.PlanProject(h.ctx, a, "shop", apitypes.ApplyRequest{File: file,
			Values: []apitypes.VariableValue{{Environment: "production", Service: "web", Key: "STRIPE_KEY", Value: guess}}})
		if err != nil {
			t.Fatal(err)
		}
		return slices.DeleteFunc(lines(pl.Changes), func(l string) bool { return !strings.Contains(l, "STRIPE_KEY") })
	}
	want := []string{"production web variable update STRIPE_KEY"}
	for _, guess := range []string{"sk_live_123", "wrong"} {
		if got := plan(reader, guess); !slices.Equal(got, want) {
			t.Fatalf("read token, guess %q: %v", guess, got)
		}
	}
	if got := plan(testSystem, "sk_live_123"); len(got) != 0 {
		t.Fatalf("admin, equal value: %v", got)
	}
}

// A token limited to one environment plans and applies only that one; a
// file naming others is refused rather than partly applied.
func TestApplyEnvironmentLimitedToken(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{Image: "web:1"})
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "dev"}); err != nil {
		t.Fatal(err)
	}
	p, _ := h.c.GetProject(h.ctx, testSystem, "shop")
	envs, _ := h.c.ListEnvironments(h.ctx, testSystem, "shop")
	i := slices.IndexFunc(envs, func(e apitypes.Environment) bool { return e.Name == "dev" })
	dev := Actor{Kind: "token", OrgID: 1, Name: "dev", Scope: apitypes.ScopeDeploy, ProjectID: p.ID, ProjectName: "shop", EnvironmentID: envs[i].ID, EnvironmentName: "dev"}
	file := `version = 1
[[environments]]
  name = "dev"
  [[environments.services]]
    name = "web"
    image = "web:2"
[[environments]]
  name = "production"
  [[environments.services]]
    name = "web"
    image = "web:2"
`
	if _, err := h.c.ApplyProject(h.ctx, dev, "shop", apitypes.ApplyRequest{File: file}); CodeOf(err) != apitypes.CodeForbidden || !strings.Contains(err.Error(), "--environment") {
		t.Fatalf("whole file: %v", err)
	}
	res, err := h.c.ApplyProject(h.ctx, dev, "shop", apitypes.ApplyRequest{File: file, Environment: "dev"})
	if err != nil || len(res.Staged) != 1 || res.Staged[0].Environment != "dev" || len(res.Staged[0].Services) != 1 {
		t.Fatalf("dev only: %+v, %v", res, err)
	}
	if _, err := h.c.ApplyProject(h.ctx, dev, "blog", apitypes.ApplyRequest{File: file, Environment: "dev"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("limited token created a project: %v", err)
	}
}

// Discarding a staged creation deletes the service with what is staged
// on it; a deploy of a staged service applies its creation.
func TestDiscardAndDeployStagedService(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{Image: "web:1"})
	file := `version = 1
[[environments]]
  name = "production"
  [[environments.services]]
    name = "web"
    image = "web:1"
  [[environments.services]]
    name = "a"
    image = "a:1"
    volumes = [{ name = "data", mount_path = "/data" }]
    [environments.services.values]
      X = "1"
  [[environments.services]]
    name = "b"
    image = "b:1"
`
	if _, err := h.c.ApplyProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file}); err != nil {
		t.Fatal(err)
	}
	env := EnvRef{"shop", "production"}
	p, err := h.c.DiscardChanges(h.ctx, testSystem, env, apitypes.DiscardChangesRequest{Services: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Services) != 1 || p.Services[0].Service != "b" || len(p.Variables) != 0 || len(p.Volumes) != 0 {
		t.Fatalf("after discarding a: %+v", p)
	}
	if _, err := h.c.GetService(h.ctx, testSystem, ServiceRef{"shop", "production", "a"}); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("a still exists: %v", err)
	}
	b := ServiceRef{"shop", "production", "b"}
	h.deploy(b, "b:1")
	if s, _ := h.c.GetService(h.ctx, testSystem, b); s.Staged != "" || s.ActiveRelease == nil {
		t.Fatalf("deployed b = %+v", s)
	}
}

// Files are read strictly: unknown keys, a key both named and valued,
// and depends_on outside the environment are errors that say where.
func TestApplyRejectsBadFiles(t *testing.T) {
	h := newHarness(t)
	for file, want := range map[string]string{
		"version = 1\n[[environments]]\nname = \"production\"\nprot = 1\n": "prot (line 4)",
		"version = 2\n": "version is 2",
		"version = 1\n[[environments]]\nname = \"production\"\n[[environments.services]]\nname = \"web\"\nvariables = [\"A\"]\nvalues = { A = \"x\" }\n": "both variables and values",
		"version = 1\n[[environments]]\nname = \"production\"\n[[environments.services]]\nname = \"web\"\ndepends_on = [\"db\"]\n":                       "depends_on",
		"version = 1\n[[environments]]\nname = \"production\"\n[[environments.services]]\nname = \"Web\"\n":                                              "lowercase",
	} {
		if _, err := h.c.PlanProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file}); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", file, err, want)
		}
	}
}

// A locked environment refuses a token's file as one request, quoting
// what the file would change there, and nothing is staged anywhere. Plans
// are reads and stay allowed.
func TestApplyLockedEnvironment(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "web:1", Port: 3000})
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "dev"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.LockEnvironment(h.ctx, testSystem, web.Env(), apitypes.LockEnvironmentRequest{Reason: "live"}); err != nil {
		t.Fatal(err)
	}
	file := `version = 1
[[environments]]
  name = "dev"
  [[environments.services]]
    name = "web"
    image = "web:1"
[[environments]]
  name = "production"
  [[environments.services]]
    name = "web"
    image = "web:1"
    port = 8080
`
	token := h.token(testSystem, apitypes.CreateTokenRequest{Name: "claude", Scope: apitypes.ScopeDeploy})
	if _, err := h.c.PlanProject(h.ctx, token, "shop", apitypes.ApplyRequest{File: file}); err != nil {
		t.Fatalf("plan in a locked environment: %v", err)
	}
	_, err := h.c.ApplyProject(h.ctx, token, "shop", apitypes.ApplyRequest{File: file})
	if CodeOf(err) != apitypes.CodeEnvironmentLocked || !strings.Contains(err.Error(), "~ web setting port 3000 → 8080") {
		t.Fatalf("apply in a locked environment: %v", err)
	}
	if p, _ := h.c.GetChanges(h.ctx, testSystem, EnvRef{"shop", "dev"}, false); p.Count != 0 {
		t.Fatalf("staged in dev despite the refusal: %+v", p)
	}
	// Filed as one access request, whose summary carries the plan.
	err = h.c.FileRequest(h.ctx, token, err, Call{Via: "test", Op: "apply-project-file", Title: "Stage a project file", Args: json.RawMessage(`{}`)})
	reqs, lerr := h.c.ListRequests(h.ctx, testSystem, RequestFilter{})
	if CodeOf(err) != apitypes.CodeEnvironmentLocked || lerr != nil || len(reqs) != 1 || !strings.Contains(reqs[0].Summary, "~ web setting port 3000 → 8080") {
		t.Fatalf("request: %v %v %+v", err, lerr, reqs)
	}
	// A person isn't stopped by the lock.
	if _, err := h.c.ApplyProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file}); err != nil {
		t.Fatal(err)
	}
}

// A stopped service, a log drain and a registry credential are exported
// without secrets and round-trip; a file that differs lists each for a
// person to change, since they act at once or hold credentials.
func TestApplyStoppedDrainsAndRegistry(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "ghcr.io/acme/web:1"})
	h.deploy(web, "ghcr.io/acme/web:1")
	if _, err := h.c.StopService(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.AddLogDrain(h.ctx, testSystem, web.Env(), apitypes.AddLogDrainRequest{Kind: apitypes.DrainHTTP, URL: "https://user:pw@logs.example.com/in", Name: "logs"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SetRegistryCredential(h.ctx, testSystem, "ghcr.io", apitypes.SetRegistryCredentialRequest{Username: "bot", Password: "ghp_secret"}); err != nil {
		t.Fatal(err)
	}
	ex, file := h.exportTOML("shop")
	svc := ex.Environments[0].Services[0]
	if !svc.Stopped || len(ex.Environments[0].LogDrains) != 1 || !slices.Equal(ex.RegistryCredentials, []string{"ghcr.io"}) {
		t.Fatalf("export = %+v", ex)
	}
	if strings.Contains(file, "pw@") || strings.Contains(file, "ghp_secret") {
		t.Fatalf("export holds a secret:\n%s", file)
	}
	if plan, err := h.c.PlanProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file}); err != nil || len(plan.Changes)+len(plan.Kept)+len(plan.Manual) != 0 {
		t.Fatalf("round trip: %v %v %v, %v", lines(plan.Changes), lines(plan.Kept), lines(plan.Manual), err)
	}

	edited := strings.NewReplacer("stopped = true\n", "", "registry_credentials = ['ghcr.io']", "registry_credentials = ['ghcr.io', 'registry.example.com']",
		"name = 'logs'", "name = 'audit'").Replace(file)
	plan, err := h.c.PlanProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: edited})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"production log_drain add audit", "production web service update stopped", "registry_credential add registry.example.com"}
	if !slices.Equal(lines(plan.Manual), want) || !slices.Equal(lines(plan.Kept), []string{"production log_drain remove logs"}) || len(plan.Changes) != 0 {
		t.Fatalf("plan:\nmanual %q\nkept %q\nchanges %q\n%s", lines(plan.Manual), lines(plan.Kept), lines(plan.Changes), edited)
	}
}
