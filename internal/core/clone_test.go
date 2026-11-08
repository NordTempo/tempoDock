package core

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// cloneSource builds shop/production: web (settings, a domain, a literal
// secret, a reference, a staged variable), a Postgres template database,
// an archived service and a shared variable.
func cloneSource(h *harness) {
	h.t.Helper()
	web := h.service("web", apitypes.CreateServiceRequest{Port: 3000, HealthCheckPath: "/up", MemoryMB: 256, CPUs: 0.5, StartCommand: "node server.js"})
	h.templateDB("postgres", apitypes.TemplatePostgres)
	old := h.service("old", apitypes.CreateServiceRequest{Image: "old:1"})
	prod := EnvRef{"shop", "production"}
	h.set(prod, "", map[string]string{"LOG_LEVEL": "lvl-7f3a"}, false)
	h.set(prod, "web", map[string]string{"STRIPE_KEY": "sk_live_secret", "DATABASE_URL": "${{postgres.DATABASE_URL}}"}, false)
	h.set(prod, "old", map[string]string{"OLD": "x"}, false)
	if _, err := h.c.ApplyChanges(h.ctx, testSystem, prod); err != nil {
		h.t.Fatal(err)
	}
	h.set(prod, "web", map[string]string{"FEATURE_X": "on"}, false) // staged only
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.c.ArchiveService(h.ctx, testSystem, old); err != nil {
		h.t.Fatal(err)
	}
}

// An admin's clone of the same kind copies services, settings, template
// settings and every live value; volumes are new and staged; nothing is
// deployed; domains, staged changes, releases and backups stay behind.
func TestCloneEnvironment(t *testing.T) {
	h := newHarness(t)
	cloneSource(h)

	res, err := h.c.CloneEnvironment(h.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.CloneEnvironmentRequest{Name: "prod-2", Kind: apitypes.KindProduction})
	if err != nil {
		t.Fatal(err)
	}
	if res.Environment.Kind != apitypes.KindProduction || !slices.Equal(res.Services, []string{"postgres", "web"}) {
		t.Fatalf("clone = %+v", res)
	}
	if res.Variables != 7 || len(res.Generated) != 0 || len(res.Skipped) != 0 || res.Volumes != 1 || res.Deploy != nil || len(res.CopiedAcrossKinds) != 0 {
		t.Fatalf("variables %d, generated %v, skipped %v, volumes %d", res.Variables, res.Generated, res.Skipped, res.Volumes)
	}
	if !slices.Equal(res.NotCopied, []string{"1 domain", "1 staged change", "releases", "backups"}) {
		t.Fatalf("not copied = %v", res.NotCopied)
	}

	src, _ := h.c.GetService(h.ctx, testSystem, ServiceRef{"shop", "production", "web"})
	web, err := h.c.GetService(h.ctx, testSystem, ServiceRef{"shop", "prod-2", "web"})
	if err != nil || web.ServiceSettings != src.ServiceSettings || web.ActiveRelease != nil {
		t.Fatalf("cloned web = %+v, %v; source settings %+v", web, err, src.ServiceSettings)
	}
	db, _ := h.c.GetService(h.ctx, testSystem, ServiceRef{"shop", "prod-2", "postgres"})
	if db.Template != apitypes.TemplatePostgres || db.Database != apitypes.DatabasePostgres || db.ActiveRelease != nil || db.Stateful {
		t.Fatalf("cloned postgres = %+v", db)
	}
	if _, err := h.c.GetService(h.ctx, testSystem, ServiceRef{"shop", "prod-2", "old"}); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("archived service cloned: %v", err)
	}

	// Values are live at once, and the admin's copy is exact.
	clone := EnvRef{"shop", "prod-2"}
	want := h.revealed(ServiceRef{"shop", "production", "postgres"})
	if got := h.revealed(ServiceRef{"shop", "prod-2", "postgres"}); !maps.Equal(got, want) {
		t.Fatalf("postgres variables %v, want %v", got, want)
	}
	vars, _ := h.c.ListVariables(h.ctx, testSystem, clone, "web", true)
	got := map[string]string{}
	for _, v := range vars {
		if v.Pending != "" || v.Value == nil {
			t.Fatalf("%s is staged in the clone", v.Key)
		}
		got[v.Key] = *v.Value
	}
	if got["STRIPE_KEY"] != "sk_live_secret" || got["LOG_LEVEL"] != "lvl-7f3a" || got["DATABASE_URL"] != "${{postgres.DATABASE_URL}}" || got["FEATURE_X"] != "" {
		t.Fatalf("web variables = %v", got)
	}

	// The volume is new and staged, so the backup scheduler leaves the
	// undeployed clone alone; nothing else came along.
	vols, _ := h.c.ListVolumes(h.ctx, testSystem, ServiceRef{"shop", "prod-2", "postgres"})
	srcVols, _ := h.c.ListVolumes(h.ctx, testSystem, ServiceRef{"shop", "production", "postgres"})
	if len(vols) != 1 || !vols[0].Pending || vols[0].DockerName == srcVols[0].DockerName || vols[0].MountPath != srcVols[0].MountPath {
		t.Fatalf("volumes %+v, source %+v", vols, srcVols)
	}
	cands, _ := h.store.ListBackupCandidates(h.ctx)
	if len(cands) != 1 {
		t.Fatalf("backup candidates = %+v", cands)
	}
	if ds, _ := h.c.ListDomains(h.ctx, testSystem, ServiceRef{"shop", "prod-2", "web"}); len(ds) != 0 {
		t.Fatalf("domains cloned: %+v", ds)
	}
	if rels, _ := h.c.ListReleases(h.ctx, testSystem, ServiceRef{"shop", "prod-2", "postgres"}, 10); len(rels) != 0 {
		t.Fatalf("releases cloned: %+v", rels)
	}

	// The clone deploys like any environment: the volume goes live with
	// the first deploy, and the reference resolves inside the clone.
	rel := h.deploy(ServiceRef{"shop", "prod-2", "postgres"}, "postgres:17")
	if rel.Status != apitypes.ReleaseActive || len(rel.Config.Mounts) != 1 {
		t.Fatalf("first deploy: %s %+v", rel.Status, rel.Config)
	}

	if _, err := h.c.CloneEnvironment(h.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.CloneEnvironmentRequest{Name: "prod-2"}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("clone onto an existing name: %v", err)
	}
}

// Who may clone, and which values come along. A deploy token's clone, and
// by default anyone's clone into the other kind (production to
// non-production here), copies references, makes template credentials
// anew and lists the rest; only an admin with copy_values copies literal
// values across kinds, and the result names them. Read and
// environment-limited tokens can't clone. No value reaches the result.
func TestCloneFollowsPermissions(t *testing.T) {
	h := newHarness(t)
	cloneSource(h)
	prod := EnvRef{"shop", "production"}
	p, _ := h.store.GetProjectByName(h.ctx, store.GetProjectByNameParams{OrgID: 1, Name: "shop"})
	e, _ := h.store.GetEnvironmentByName(h.ctx, store.GetEnvironmentByNameParams{ProjectID: p.ID, Name: "production"})

	for _, a := range []Actor{
		{Kind: "token", OrgID: 1, Name: "ro", Scope: apitypes.ScopeRead},
		{Kind: "token", OrgID: 1, Name: "env", Scope: apitypes.ScopeDeploy, ProjectID: p.ID, ProjectName: "shop", EnvironmentID: e.ID, EnvironmentName: "production"},
		{Kind: "token", OrgID: 1, Name: "blog", Scope: apitypes.ScopeAdmin, ProjectID: p.ID + 1, ProjectName: "blog"},
	} {
		if _, err := h.c.CloneEnvironment(h.ctx, a, prod, apitypes.CloneEnvironmentRequest{Name: "copy-" + a.Name}); CodeOf(err) != apitypes.CodeForbidden {
			t.Fatalf("%s cloned: %v", a.Name, err)
		}
	}

	names := func(vs []apitypes.VariableRef) []string {
		s := make([]string, len(vs))
		for i, v := range vs {
			s[i] = v.String()
		}
		slices.Sort(s)
		return s
	}
	src := h.revealed(ServiceRef{"shop", "production", "postgres"})
	noValues := func(res apitypes.CloneResult, more ...string) {
		t.Helper()
		b, _ := json.Marshal(res)
		for _, secret := range append([]string{"sk_live_secret", "lvl-7f3a", src["POSTGRES_PASSWORD"]}, more...) {
			if strings.Contains(string(b), secret) {
				t.Fatalf("clone result contains a value: %s", b)
			}
		}
	}

	agent := Actor{Kind: "token", OrgID: 1, Name: "agent", Scope: apitypes.ScopeDeploy, ProjectID: p.ID, ProjectName: "shop"}
	for _, a := range []Actor{agent, testSystem} {
		env := "preview-" + a.Name
		res, err := h.c.CloneEnvironment(h.ctx, a, prod, apitypes.CloneEnvironmentRequest{Name: env})
		if err != nil {
			t.Fatal(err)
		}
		if got := names(res.Skipped); !slices.Equal(got, []string{"shared.LOG_LEVEL", "web.STRIPE_KEY"}) {
			t.Fatalf("%s: skipped = %v", a.Name, got)
		}
		if got := names(res.Generated); !slices.Equal(got, []string{"postgres.DATABASE_URL", "postgres.POSTGRES_DB", "postgres.POSTGRES_PASSWORD", "postgres.POSTGRES_USER"}) {
			t.Fatalf("%s: generated = %v", a.Name, got)
		}
		if res.Variables != 1 || len(res.CopiedAcrossKinds) != 0 || res.SourceKind != apitypes.KindProduction {
			t.Fatalf("%s: copied %d variables (%v), want the reference only", a.Name, res.Variables, res.CopiedAcrossKinds)
		}
		db := h.revealed(ServiceRef{"shop", env, "postgres"})
		if db["POSTGRES_PASSWORD"] == "" || db["POSTGRES_PASSWORD"] == src["POSTGRES_PASSWORD"] || db["POSTGRES_USER"] != "postgres" || db["DATABASE_URL"] != src["DATABASE_URL"] {
			t.Fatalf("%s: template variables %v (source %v)", a.Name, db, src)
		}
		if web := h.revealed(ServiceRef{"shop", env, "web"}); len(web) != 1 || web["DATABASE_URL"] != "${{postgres.DATABASE_URL}}" {
			t.Fatalf("%s: web variables = %v", a.Name, web)
		}
		noValues(res, db["POSTGRES_PASSWORD"])
	}

	// copy_values is for admins only, and then copies across kinds and
	// says what came from production.
	if _, err := h.c.CloneEnvironment(h.ctx, agent, prod, apitypes.CloneEnvironmentRequest{Name: "agent-copy", CopyValues: true}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("agent with copy_values: %v", err)
	}
	res, err := h.c.CloneEnvironment(h.ctx, testSystem, prod, apitypes.CloneEnvironmentRequest{Name: "staging", CopyValues: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(res.CopiedAcrossKinds); !slices.Equal(got, []string{
		"postgres.DATABASE_URL", "postgres.POSTGRES_DB", "postgres.POSTGRES_PASSWORD", "postgres.POSTGRES_USER", "shared.LOG_LEVEL", "web.STRIPE_KEY",
	}) || res.Variables != 7 || len(res.Skipped) != 0 || len(res.Generated) != 0 {
		t.Fatalf("copy_values: copied %d, across kinds %v, skipped %v, generated %v", res.Variables, got, res.Skipped, res.Generated)
	}
	if got := h.revealed(ServiceRef{"shop", "staging", "postgres"}); !maps.Equal(got, src) {
		t.Fatalf("copied postgres variables %v, want %v", got, src)
	}
	if web := h.revealed(ServiceRef{"shop", "staging", "web"}); web["STRIPE_KEY"] != "sk_live_secret" {
		t.Fatalf("copied web variables = %v", web)
	}
	noValues(res)
}

func TestOnlyReferences(t *testing.T) {
	for v, want := range map[string]bool{
		"${{postgres.DATABASE_URL}}":           true,
		"${{ shared.A }}${{api.B}}":            true,
		"":                                     false,
		"http://${{api.HOST}}:${{api.PORT}}":   false,
		"postgres://u:pw@${{db.HOST}}/app":     false,
		"${{postgres.DATABASE_URL}}?sslmode=1": false,
	} {
		if got := onlyReferences(v); got != want {
			t.Errorf("onlyReferences(%q) = %v", v, got)
		}
	}
}

// A clone into a new kind goes to an eligible server for that kind and
// becomes the project's placement for it; a clone of a placed kind
// follows the project; with no eligible server nothing is created.
func TestClonePlacement(t *testing.T) {
	p := newPool(t,
		poolServer{"cp", true, false, 8},
		poolServer{"vps2", true, false, 4},
		poolServer{"homelab", false, true, 32},
		poolServer{"lab2", false, true, 16},
	)
	cpName := p.c.localName()
	p.project("shop")
	p.createService("production", "web", apitypes.CreateServiceRequest{Image: "web:1"})
	prod := EnvRef{"shop", "production"}
	clone := func(name string, kind apitypes.EnvironmentKind) (apitypes.CloneResult, error) {
		return p.c.CloneEnvironment(p.ctx, testSystem, prod, apitypes.CloneEnvironmentRequest{Name: name, Kind: kind})
	}

	if got := p.server("shop", "production"); got != cpName {
		t.Fatalf("production on %s", got)
	}
	// Non-production goes where there is most room, never to a
	// production-only server.
	res, err := clone("staging", "")
	if err != nil || res.Environment.Server != "homelab" || res.Environment.Kind != apitypes.KindNonProduction {
		t.Fatalf("staging: %+v, %v", res.Environment, err)
	}
	// The project's non-production environments stay together.
	if res, err := clone("dev", ""); err != nil || res.Environment.Server != "homelab" {
		t.Fatalf("dev: %+v, %v", res.Environment, err)
	}
	// An explicit production kind follows the project's production.
	if res, err := clone("prod-eu", apitypes.KindProduction); err != nil || res.Environment.Server != cpName {
		t.Fatalf("prod-eu: %+v, %v", res.Environment, err)
	}

	// No eligible server: an error counting them, and nothing left behind.
	p.project("blog")
	if _, err := p.c.CreateService(p.ctx, testSystem, EnvRef{"blog", "production"}, apitypes.CreateServiceRequest{Name: "web"}); err != nil {
		t.Fatal(err)
	}
	p.setOffline("homelab", true)
	p.setOffline("lab2", true)
	_, err = p.c.CloneEnvironment(p.ctx, testSystem, EnvRef{"blog", "production"}, apitypes.CloneEnvironmentRequest{Name: "staging"})
	if CodeOf(err) != apitypes.CodeServerOffline || !strings.Contains(err.Error(), "2 servers offline") {
		t.Fatalf("clone with no eligible server: %v", err)
	}
	if envs, _ := p.c.ListEnvironments(p.ctx, testSystem, "blog"); len(envs) != 1 {
		t.Fatalf("blog environments = %+v", envs)
	}
	if pls, _ := p.c.ListPlacements(p.ctx, testSystem, "blog"); len(pls) != 1 {
		t.Fatalf("blog placements = %+v", pls)
	}
}

// With deploy, a clone promotes the images serving in its source: by
// digest, on the clone's own server, with the clone's variables, and each
// release takes its service's staged volume live.
func TestCloneDeploy(t *testing.T) {
	p, _ := labPool(t)
	p.withRegistry(&fakeRegistry{})
	staging := EnvRef{"shop", "staging"}
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	db := p.createService("staging", "db", apitypes.CreateServiceRequest{})
	p.createService("staging", "idle", apitypes.CreateServiceRequest{})
	if _, err := p.c.AddVolume(p.ctx, testSystem, db, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
		t.Fatal(err)
	}
	p.set(staging, "web", map[string]string{"API": "${{db.DOCK_PRIVATE_DOMAIN}}"}, false)
	built := p.deploySource(web)
	p.deploy(db, "ghcr.io/acme/db:1")

	res, err := p.c.CloneEnvironment(p.ctx, testSystem, staging, apitypes.CloneEnvironmentRequest{Name: "prod-eu", Kind: apitypes.KindProduction, Deploy: true})
	if err != nil || res.DeployError != "" || res.Deploy == nil {
		t.Fatalf("clone: %+v, %v", res, err)
	}
	if res.Environment.Server != p.c.localName() || len(res.Deploy.Promoted) != 2 || len(res.Deploy.Skipped) != 1 || res.Deploy.Skipped[0].Service != "idle" {
		t.Fatalf("deploy = %+v on %s", res.Deploy, res.Environment.Server)
	}
	for _, s := range res.Deploy.Promoted {
		r := p.wait(ServiceRef{"shop", "prod-eu", s.Service}, s.Release.Number)
		if r.Status != apitypes.ReleaseActive || r.Source != apitypes.SourcePromote {
			t.Fatalf("%s: %s %s (%s)", s.Service, r.Source, r.Status, r.Error)
		}
	}
	webRel := res.Deploy.Promoted[1].Release
	if webRel.ImageDigest != built.ImageDigest {
		t.Fatalf("web runs %s, staging built %s", webRel.ImageDigest, built.ImageDigest)
	}
	spec, ok := p.rt.Spec(p.ctr(ServiceRef{"shop", "prod-eu", "web"}, webRel.Number))
	if !ok || spec.Env["API"] != "db" || spec.Env["DOCK_ENVIRONMENT"] != "prod-eu" {
		t.Fatalf("web env = %v", spec.Env)
	}
	vols, _ := p.c.ListVolumes(p.ctx, testSystem, ServiceRef{"shop", "prod-eu", "db"})
	dbSpec, _ := p.rt.Spec(p.ctr(ServiceRef{"shop", "prod-eu", "db"}, res.Deploy.Promoted[0].Release.Number))
	if len(vols) != 1 || vols[0].Pending || len(dbSpec.Mounts) != 1 || dbSpec.Mounts[0].Volume != vols[0].DockerName {
		t.Fatalf("db volumes %+v, mounts %+v", vols, dbSpec.Mounts)
	}
	if hasOp(p.rt.Ops(), "build ") {
		t.Fatalf("clone rebuilt: %v", p.rt.Ops())
	}
}
