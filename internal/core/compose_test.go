package core

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A Compose file with a database, a cache and an app plans as a new
// project; applying it creates the templates and the app, each running,
// with the Compose file's secret staged (never returned) and the app's
// URLs pointing at the templates.
func TestImportCompose(t *testing.T) {
	h := newHarness(t)
	compose := `services:
  db:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: pw-123
  cache:
    image: redis:7
  web:
    image: ghcr.io/acme/web:1
    depends_on: [db, cache]
    ports: ["8080:3000"]
    volumes: ["uploads:/app/uploads", "./config:/config"]
    environment:
      DATABASE_URL: postgres://postgres:pw-123@db:5432/postgres
      REDIS_URL: redis://cache:6379
      SECRET_KEY: sk-from-compose
      MODE: production
volumes:
  uploads:
`
	res, err := h.c.ImportCompose(h.ctx, testSystem, "shop", apitypes.ComposeImportRequest{Compose: compose})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Secrets) != 1 || res.Secrets[0].Key != "SECRET_KEY" || !res.Secrets[0].HasValue {
		t.Fatalf("secrets = %+v", res.Secrets)
	}
	if !slices.ContainsFunc(res.Warnings, func(w apitypes.ComposeWarning) bool {
		return w.Service == "web" && strings.Contains(w.Message, "./config")
	}) {
		t.Fatalf("no warning for the bind mount: %+v", res.Warnings)
	}
	if res.Plan.Changes[0].Kind != apitypes.PlanProject || len(res.Plan.Manual) != 0 {
		t.Fatalf("plan: %+v", res.Plan)
	}
	if _, err := h.c.GetProject(h.ctx, testSystem, "shop"); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("planning created the project: %v", err)
	}

	res, err = h.c.ImportCompose(h.ctx, testSystem, "shop", apitypes.ComposeImportRequest{Compose: compose, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res); strings.Contains(string(b), "sk-from-compose") || strings.Contains(string(b), "pw-123") {
		t.Fatalf("the import shows a secret: %s", b)
	}
	var started []string
	for _, r := range res.Result.Applied[0].Releases {
		started = append(started, r.Service)
	}
	if !slices.Equal(started, []string{"cache", "db", "web"}) {
		t.Fatalf("started %v", started)
	}
	web := ServiceRef{"shop", "production", "web"}
	for _, svc := range []string{"cache", "db", "web"} {
		if rel := h.wait(ServiceRef{"shop", "production", svc}, 1); rel.Status != apitypes.ReleaseActive {
			t.Fatalf("%s: %s %s", svc, rel.Status, rel.Error)
		}
	}
	vars := h.revealed(web)
	if vars["SECRET_KEY"] != "sk-from-compose" || vars["DATABASE_URL"] != "${{db.DATABASE_URL}}" || vars["REDIS_URL"] != "${{cache.REDIS_URL}}" || vars["MODE"] != "production" {
		t.Fatalf("web's variables = %v", vars)
	}
	if vols, _ := h.c.ListVolumes(h.ctx, testSystem, web); len(vols) != 1 || vols[0].Name != "uploads" {
		t.Fatalf("web's volumes = %+v", vols)
	}
	// The project now matches: importing again plans nothing.
	again, err := h.c.ImportCompose(h.ctx, testSystem, "shop", apitypes.ComposeImportRequest{Compose: compose})
	if err != nil || len(again.Plan.Changes) != 0 {
		t.Fatalf("import again: %+v, %v", again.Plan, err)
	}
}

// Updating an environment from a newer Compose file: a service it drops
// is kept, and with prune staged for archiving (admin only), while a
// domain and a variable added in Dock stay and aren't listed, since a
// Compose file has no word for domains and prune never removes variables.
// StageOnly leaves the changes for review; Environment picks where the
// services go.
func TestImportComposeUpdate(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "web:1"})
	h.service("worker", apitypes.CreateServiceRequest{Image: "worker:1"})
	h.set(web.Env(), "web", map[string]string{"FROM_DOCK": "x"}, true)
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	compose := `services:
  web:
    image: web:2
  api:
    image: api:1
`
	res, err := h.c.ImportCompose(h.ctx, testSystem, "shop", apitypes.ComposeImportRequest{Compose: compose})
	if err != nil {
		t.Fatal(err)
	}
	if got := lines(res.Plan.Changes); !slices.Equal(got, []string{"production api service add api", "production api setting add image", "production web setting update image"}) {
		t.Fatalf("changes %v", got)
	}
	if !slices.ContainsFunc(res.Plan.Kept, func(c apitypes.PlanChange) bool {
		return c.Service == "worker" && c.Note == "not in the Compose file; kept (--prune archives it)"
	}) || slices.ContainsFunc(res.Plan.Kept, func(c apitypes.PlanChange) bool { return c.Kind == apitypes.PlanDomain }) {
		t.Fatalf("kept %+v", res.Plan.Kept)
	}

	deployer := Actor{Name: "ci", Kind: "token", OrgID: 1, Scope: apitypes.ScopeDeploy}
	pruned := apitypes.ComposeImportRequest{Compose: compose, Prune: true, Apply: true, StageOnly: true}
	if _, err := h.c.ImportCompose(h.ctx, deployer, "shop", pruned); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("deploy token archived a service: %v", err)
	}
	res, err = h.c.ImportCompose(h.ctx, testSystem, "shop", pruned)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Result.Applied) != 0 {
		t.Fatalf("stage only applied: %+v", res.Result.Applied)
	}
	staged := res.Result.Staged[0]
	if !slices.ContainsFunc(staged.Services, func(s apitypes.ServiceStaging) bool {
		return s.Service == "worker" && s.Action == apitypes.ChangeRemove
	}) || len(staged.Variables) != 0 || len(staged.Domains) != 0 {
		t.Fatalf("staged %+v", staged)
	}
	if _, err := h.c.ApplyChanges(h.ctx, testSystem, web.Env()); err != nil {
		t.Fatal(err)
	}
	if s, _ := h.c.GetService(h.ctx, testSystem, ServiceRef{"shop", "production", "worker"}); s.ArchivedAt == nil {
		t.Fatal("worker wasn't archived")
	}
	if vars := h.revealed(web); vars["FROM_DOCK"] != "x" {
		t.Fatalf("web's variables = %v", vars)
	}

	// Another environment: created with its name's kind.
	res, err = h.c.ImportCompose(h.ctx, testSystem, "shop", apitypes.ComposeImportRequest{Compose: compose, Environment: "staging", Apply: true, StageOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan.Changes[0].Kind != apitypes.PlanEnvironment || res.Plan.Changes[0].New != string(apitypes.KindNonProduction) {
		t.Fatalf("staging plan %v", lines(res.Plan.Changes))
	}
}
