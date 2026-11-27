package core

import (
	"archive/tar"
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Security headers (ADR 0107) are on for a new service and follow its
// active release: a staged change routes nothing until it is applied,
// and a rollback serves the old release's setting again.
func TestSecurityHeadersFollowTheRelease(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1", Port: 8080})
	h.deploy(web, "app:1")
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	if !h.routeOf("shop.example.com").SecurityHeaders {
		t.Fatal("a new service's route has no security headers")
	}

	if _, err := h.c.UpdateService(h.ctx, testSystem, web, apitypes.UpdateServiceRequest{SecurityHeaders: new(false)}); err != nil {
		t.Fatal(err)
	}
	if !h.routeOf("shop.example.com").SecurityHeaders {
		t.Fatal("a staged change reached the route")
	}
	if _, err := h.c.ApplyChanges(h.ctx, testSystem, web.Env()); err != nil {
		t.Fatal(err)
	}
	if r := h.wait(web, 2); r.Status != apitypes.ReleaseActive || r.Config.SecurityHeaders {
		t.Fatalf("release 2: %s, security headers %v", r.Status, r.Config.SecurityHeaders)
	}
	if h.routeOf("shop.example.com").SecurityHeaders {
		t.Fatal("turned off, the route still has security headers")
	}

	if _, err := h.c.Rollback(h.ctx, testSystem, web, 1); err != nil {
		t.Fatal(err)
	}
	h.wait(web, 3)
	if !h.routeOf("shop.example.com").SecurityHeaders {
		t.Fatal("rolled back to release 1, the route has no security headers")
	}
}

// A project file writes security_headers only when off. Left out, a new
// service gets them and an existing service keeps its setting, so files
// from before ADR 0107 turn nothing on.
func TestApplySecurityHeaders(t *testing.T) {
	h := newHarness(t)
	h.service("old", apitypes.CreateServiceRequest{Image: "old:1", SecurityHeaders: new(false)})
	h.service("web", apitypes.CreateServiceRequest{Image: "web:1", PreDeployCommand: "./migrate", PreDeploySnapshot: true})
	ex, file := h.exportTOML("shop")
	for _, s := range ex.Environments[0].Services {
		if want := s.Name == "old"; (s.SecurityHeaders != nil) != want {
			t.Fatalf("export of %s: security_headers %v", s.Name, s.SecurityHeaders)
		}
	}
	// The export applies as is: pre_deploy_snapshot included.
	plan, err := h.c.PlanProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 {
		t.Fatalf("plan of the export: %q\n%s", lines(plan.Changes), file)
	}

	// Without the key: the existing service stays off, a new one is on.
	file = strings.Replace(file, "security_headers = false\n", "", 1) + "\n[[environments.services]]\nname = \"api\"\nimage = \"api:1\"\n"
	plan, err = h.c.PlanProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"production api service add api", "production api setting add image"}; !slices.Equal(lines(plan.Changes), want) {
		t.Fatalf("plan: %q, want %q\n%s", lines(plan.Changes), want, file)
	}
	if _, err := h.c.ApplyProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"old": false, "web": true, "api": true} {
		s, err := h.c.GetService(h.ctx, testSystem, ServiceRef{"shop", "production", name})
		if err != nil {
			t.Fatal(err)
		}
		if s.SecurityHeaders != want {
			t.Errorf("%s: security headers %v, want %v", name, s.SecurityHeaders, want)
		}
	}
}

// dock.toml's security_headers wins over the service's setting for the
// releases built from that source.
func TestSecurityHeadersFromRepoFile(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := "[deploy]\nsecurity_headers = false\n"
	_ = tw.WriteHeader(&tar.Header{Name: "dock.toml", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	rel, err := h.c.DeploySource(h.ctx, testSystem, web, &buf, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if r := h.wait(web, rel.Number); r.Status != apitypes.ReleaseActive || r.Config.SecurityHeaders || !slices.Equal(r.Config.FromRepoFile, []string{"security_headers"}) {
		t.Fatalf("release = %+v", r)
	}
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	if h.routeOf("shop.example.com").SecurityHeaders {
		t.Fatal("dock.toml turned them off, the route has them")
	}
}
