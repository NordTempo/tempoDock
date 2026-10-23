package core

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// An export lists live settings, variable names, volumes, domains and git
// connections, sorted by name whatever the creation order, and never a
// value, a token or a webhook secret. Environment-limited readers see
// their environment only.
func TestExportProject(t *testing.T) {
	h := newHarness(t)
	cloneSource(h) // web, postgres, archived old; a domain; a staged variable
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "dev"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"zeta", "alpha"} {
		if _, err := h.c.CreateService(h.ctx, testSystem, EnvRef{"shop", "dev"}, apitypes.CreateServiceRequest{Name: name, Image: name + ":1"}); err != nil {
			t.Fatal(err)
		}
	}
	h.set(EnvRef{"shop", "dev"}, "zeta", map[string]string{"B": "b-secret", "A": "a-secret"}, true)
	web := ServiceRef{"shop", "production", "web"}
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "a.example.com", RedirectTo: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	token := "github_pat_never_exported"
	conn, err := h.c.GitConnect(h.ctx, testSystem, web, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main", Dir: "apps/web", Token: &token})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.AddVolume(h.ctx, testSystem, web, apitypes.AddVolumeRequest{Name: "cache", MountPath: "/cache"}); err != nil {
		t.Fatal(err) // staged: not exported
	}

	ex, err := h.c.ExportProject(h.ctx, testSystem, "shop")
	if err != nil {
		t.Fatal(err)
	}
	pg := templates[apitypes.TemplatePostgres]
	want := apitypes.ProjectExport{Version: 1, Project: "shop", Environments: []apitypes.EnvironmentExport{
		{Name: "dev", Kind: apitypes.KindNonProduction, Services: []apitypes.ServiceExport{
			{Name: "alpha", Image: "alpha:1"},
			{Name: "zeta", Image: "zeta:1", Variables: []string{"A", "B"}},
		}},
		{Name: "production", Kind: apitypes.KindProduction, SharedVariables: []string{"LOG_LEVEL"}, Services: []apitypes.ServiceExport{
			{
				Name: "postgres", Template: apitypes.TemplatePostgres, Image: pg.image, StartCommand: pg.cmd, MemoryMB: pg.memoryMB,
				Database:  apitypes.DatabasePostgres,
				Variables: []string{"DATABASE_URL", "POSTGRES_DB", "POSTGRES_PASSWORD", "POSTGRES_USER"},
				Volumes:   []apitypes.VolumeExport{{Name: "data", MountPath: pg.mountPath}},
			},
			{
				Name: "web", Port: 3000, HealthCheckPath: "/up", StartCommand: "node server.js", MemoryMB: 256, CPUs: 0.5,
				Variables: []string{"DATABASE_URL", "STRIPE_KEY"},
				Domains:   []apitypes.DomainExport{{Hostname: "a.example.com", RedirectTo: "shop.example.com"}, {Hostname: "shop.example.com"}},
				Git:       &apitypes.GitExport{Repository: "acme/shop", Branch: "main", Directory: "apps/web"},
			},
		}},
	}}
	if !reflect.DeepEqual(ex, want) {
		a, _ := json.MarshalIndent(ex, "", "  ")
		t.Fatalf("export:\n%s", a)
	}
	b, _ := json.Marshal(ex)
	for _, secret := range []string{"sk_live_secret", "lvl-7f3a", "a-secret", token, conn.Secret, h.revealed(ServiceRef{"shop", "production", "postgres"})["POSTGRES_PASSWORD"]} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("export contains a secret: %s", b)
		}
	}

	// The same config exports the same, byte for byte.
	again, _ := h.c.ExportProject(h.ctx, testSystem, "shop")
	if b2, _ := json.Marshal(again); string(b2) != string(b) {
		t.Fatalf("export changed:\n%s\n%s", b, b2)
	}

	// Readers may export; an environment-limited one gets its environment.
	p, _ := h.store.GetProjectByName(h.ctx, store.GetProjectByNameParams{OrgID: 1, Name: "shop"})
	e, _ := h.store.GetEnvironmentByName(h.ctx, store.GetEnvironmentByNameParams{ProjectID: p.ID, Name: "dev"})
	devOnly := Actor{Kind: "token", OrgID: 1, Name: "dev", Scope: apitypes.ScopeRead, ProjectID: p.ID, ProjectName: "shop", EnvironmentID: e.ID, EnvironmentName: "dev"}
	if ex, err := h.c.ExportProject(h.ctx, devOnly, "shop"); err != nil || len(ex.Environments) != 1 || ex.Environments[0].Name != "dev" {
		t.Fatalf("dev-only export: %+v, %v", ex, err)
	}
	other := Actor{Kind: "token", OrgID: 1, Name: "blog", Scope: apitypes.ScopeAdmin, ProjectID: p.ID + 1, ProjectName: "blog"}
	if _, err := h.c.ExportProject(h.ctx, other, "shop"); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("other project's token: %v", err)
	}
}
