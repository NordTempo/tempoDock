package core

import (
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A pull request that changes nothing in a previewed service's directory
// gets no preview, as a push wouldn't deploy it; one that does gets one.
func TestPreviewOnlyWhenItsDirectoryChanges(t *testing.T) {
	h, gh, _, appHook := previewHarness(t)
	t0 := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	gh.mu.Lock()
	gh.changed = []string{"docs/readme.md", "apps/api/main.go"}
	gh.mu.Unlock()
	if msg, err := h.deliver(appHook, "c1", prEvent{action: "opened", number: 7, sha: shaA, updated: t0}); err != nil || !strings.Contains(msg, "ignored, nothing changed in apps/web/") {
		t.Fatalf("docs only: %q, %v", msg, err)
	}
	gh.mu.Lock()
	gh.changed = []string{"apps/web/page.tsx"}
	gh.mu.Unlock()
	if msg, err := h.deliver(appHook, "c2", prEvent{action: "synchronize", number: 7, sha: shaB, updated: t0.Add(time.Minute)}); err != nil || !strings.Contains(msg, "preview pr-7 created") {
		t.Fatalf("web changed: %q, %v", msg, err)
	}
}

// One repository and branch previews from one environment, building pull
// requests needs a GitHub source, and disconnecting turns them off.
func TestServicePreviewRules(t *testing.T) {
	h, _, _, _ := previewHarness(t)
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "dev"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CreateService(h.ctx, testSystem, EnvRef{"shop", "dev"}, apitypes.CreateServiceRequest{Name: "web", Port: 3000}); err != nil {
		t.Fatal(err)
	}
	dev := ServiceRef{"shop", "dev", "web"}
	if _, err := h.c.SetServicePreviews(h.ctx, testSystem, dev, apitypes.SetServicePreviewsRequest{Build: true}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("build without a source: %v", err)
	}
	if _, err := h.c.GitConnect(h.ctx, testSystem, dev, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main", Dir: "apps/web"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SetServicePreviews(h.ctx, testSystem, dev, apitypes.SetServicePreviewsRequest{Build: true}); CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "already preview from staging") {
		t.Fatalf("a second base for main: %v", err)
	}

	staging := ServiceRef{"shop", "staging", "web"}
	set, err := h.c.GetPreviews(h.ctx, testSystem, "shop")
	if err != nil || !set.Enabled || set.Base != "staging" || len(set.Environments) != 1 || set.Environments[0].MaxPreviews != 2 ||
		len(set.Services) != 1 || set.Services[0].Dir != "apps/web" || strings.Join(set.CopyVariables, ",") != "web.API_URL" {
		t.Fatalf("previews = %+v, %v", set, err)
	}
	if err := h.c.GitDisconnect(h.ctx, testSystem, staging); err != nil {
		t.Fatal(err)
	}
	if sp, err := h.c.GetServicePreviews(h.ctx, testSystem, staging); err != nil || sp.Build || strings.Join(sp.CopyVariables, ",") != "API_URL" {
		t.Fatalf("after disconnecting: %+v, %v", sp, err)
	}
	if set, err := h.c.GetPreviews(h.ctx, testSystem, "shop"); err != nil || set.Enabled {
		t.Fatalf("previews after disconnecting: %+v, %v", set, err)
	}
	// With staging's gone, dev may preview main.
	h.servicePreviews(dev, apitypes.SetServicePreviewsRequest{Build: true})
}
