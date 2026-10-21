package core

import (
	"errors"
	"reflect"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// The overview reads a whole project in bulk. Each service must match the
// per-service reads, and carry its active, in-flight and latest releases.
func TestOverviewMatchesPerServiceReads(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 3000})
	api := h.service("api", apitypes.CreateServiceRequest{Port: 3000})
	if _, err := h.c.AddDomain(h.ctx, System, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	h.deploy(web, "web:1")
	h.rt.FailHealthCheck(func(node.ContainerSpec) error { return errors.New("not ready") })
	failed := h.deploy(web, "web:2")
	h.rt.FailHealthCheck(nil)
	release := h.rt.HoldHealthChecks()
	defer release()
	inFlight, err := h.c.DeployImage(h.ctx, System, api, apitypes.DeployImageRequest{Image: "api:1"})
	if err != nil {
		t.Fatal(err)
	}

	ov, err := h.c.Overview(h.ctx, System, "shop")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]apitypes.ServiceOverview{}
	for _, so := range ov.Environments[0].Services {
		got[so.Service.Name] = so
	}

	w := got["web"]
	if w.ActiveRelease == nil || w.ActiveRelease.Number != 1 || w.Deploying != nil {
		t.Fatalf("web active %+v, deploying %+v", w.ActiveRelease, w.Deploying)
	}
	if l := w.LatestRelease; l == nil || l.Number != failed.Number || l.Status != apitypes.ReleaseFailed ||
		l.ErrorCode != failed.ErrorCode || l.Error != failed.Error {
		t.Fatalf("web latest %+v, want failed release %d", l, failed.Number)
	}
	a := got["api"]
	if a.ActiveRelease != nil || a.Deploying == nil || a.Deploying.Number != inFlight.Number ||
		a.LatestRelease == nil || a.LatestRelease.Number != inFlight.Number {
		t.Fatalf("api active %+v, deploying %+v, latest %+v", a.ActiveRelease, a.Deploying, a.LatestRelease)
	}

	for _, ref := range []ServiceRef{web, api} {
		svc, err := h.c.GetService(h.ctx, System, ref)
		if err != nil {
			t.Fatal(err)
		}
		domains, err := h.c.ListDomains(h.ctx, System, ref)
		if err != nil {
			t.Fatal(err)
		}
		so := got[ref.Service]
		// Status is live runtime state, not read from the database.
		svc.Status, so.Service.Status = apitypes.ServiceStatus{}, apitypes.ServiceStatus{}
		if !reflect.DeepEqual(so.Service, svc) || !reflect.DeepEqual(so.Domains, domains) {
			t.Fatalf("%s: overview %+v %+v\nper-service %+v %+v", ref.Service, so.Service, so.Domains, svc, domains)
		}
	}
}

// Audit reads filter by project through their own query, carry project and
// environment names, and page backwards.
func TestListAuditFiltersAndPages(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{})
	if _, err := h.c.CreateProject(h.ctx, System, apitypes.CreateProjectRequest{Name: "blog"}); err != nil {
		t.Fatal(err)
	}

	all, err := h.c.ListAudit(h.ctx, System, AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	shop, err := h.c.ListAudit(h.ctx, System, AuditFilter{Project: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	var want []apitypes.AuditEvent
	for _, e := range all {
		if e.Project == "shop" {
			want = append(want, e)
		}
	}
	if len(want) == 0 || len(want) == len(all) || !reflect.DeepEqual(shop, want) {
		t.Fatalf("shop events %+v, want %+v (of %d)", shop, want, len(all))
	}
	named := false
	for _, e := range shop {
		named = named || e.Environment == "production"
	}
	if !named {
		t.Fatalf("no shop event names its environment: %+v", shop)
	}

	page, err := h.c.ListAudit(h.ctx, System, AuditFilter{Before: all[0].ID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ID != all[1].ID {
		t.Fatalf("page before %d = %+v, want event %d", all[0].ID, page, all[1].ID)
	}
}
