package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A service's audit log has its releases, settings and domains, and no
// other service's events.
func TestAuditFilterByService(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	worker := h.service("worker", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	h.deploy(worker, "app:1")
	if _, err := h.c.AddDomain(h.ctx, System, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}

	events, err := h.c.ListAudit(h.ctx, System, AuditFilter{Project: "shop", Environment: "production", Service: "web"})
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range events {
		if e.Target != "shop/production/web" || e.Environment != "production" {
			t.Errorf("event of another target: %+v", e)
		}
		actions = append(actions, e.Action)
	}
	want := []string{"domain.add", "release.deploy", "service.create"}
	if len(actions) != len(want) || actions[0] != want[0] || actions[1] != want[1] || actions[2] != want[2] {
		t.Fatalf("actions = %v, want %v", actions, want)
	}

	if _, err := h.c.ListAudit(h.ctx, System, AuditFilter{Project: "shop", Service: "web"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("service without environment: %v", err)
	}
}
