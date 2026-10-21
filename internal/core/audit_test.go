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

	// An action filter is a prefix, in every query shape.
	for _, tc := range []struct {
		f    AuditFilter
		want int
	}{
		{AuditFilter{Action: "Release."}, 2},
		{AuditFilter{Project: "shop", Action: "release."}, 2},
		{AuditFilter{Project: "shop", Environment: "production", Service: "web", Action: "release."}, 1},
	} {
		f := tc.f
		events, err := h.c.ListAudit(h.ctx, System, f)
		if err != nil {
			t.Fatal(err)
		}
		var services []string
		for _, e := range events {
			if e.Action != "release.deploy" {
				t.Errorf("%+v: action %s", f, e.Action)
			}
			services = append(services, e.Target)
		}
		if len(events) != tc.want {
			t.Errorf("%+v: events for %v, want %d", f, services, tc.want)
		}
	}
}
