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
