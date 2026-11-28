package core

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Outside checks need no domain but keep every other guard, and only
// organization admins add, change or remove them.
func TestOutsideUptimeChecksValidateAndCheckTheirActor(t *testing.T) {
	h := newHarness(t)
	web := h.uptimeService()
	for _, tc := range []struct {
		req  apitypes.AddUptimeCheckRequest
		code apitypes.ErrorCode
	}{
		{apitypes.AddUptimeCheckRequest{URL: "https://status.vendor.example/", IntervalSeconds: 30}, apitypes.CodeValidation},
		{apitypes.AddUptimeCheckRequest{URL: "http://10.0.0.5/"}, apitypes.CodeValidation},
		{apitypes.AddUptimeCheckRequest{URL: "http://db/"}, apitypes.CodeValidation},
		{apitypes.AddUptimeCheckRequest{URL: "https://status.vendor.example:8443/"}, apitypes.CodeValidation},
		{apitypes.AddUptimeCheckRequest{URL: "https://user:pw@status.vendor.example/"}, apitypes.CodeValidation},
	} {
		if _, err := h.c.AddOutsideUptimeCheck(h.ctx, testSystem, tc.req); CodeOf(err) != tc.code {
			t.Errorf("add %+v: %v, want %s", tc.req, err, tc.code)
		}
	}
	chk, err := h.c.AddOutsideUptimeCheck(h.ctx, testSystem, apitypes.AddUptimeCheckRequest{URL: "https://Status.Vendor.example"})
	if err != nil {
		t.Fatal(err)
	}
	if chk.URL != "https://status.vendor.example/" || chk.IntervalSeconds != 60 || chk.ExpectStatus != "200" {
		t.Fatalf("check = %+v", chk)
	}
	if _, err := h.c.AddOutsideUptimeCheck(h.ctx, testSystem, apitypes.AddUptimeCheckRequest{URL: chk.URL}); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("the same URL twice: %v, want conflict", err)
	}
	if _, err := h.c.UpdateOutsideUptimeCheck(h.ctx, testSystem, chk.ID, apitypes.UpdateUptimeCheckRequest{IntervalSeconds: new(30)}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("an interval under a minute: %v, want invalid", err)
	}

	// Readers see them; deployers and a project's admin token don't change them.
	reader, dev := h.user("reader@example.com", apitypes.ScopeRead), h.user("dev@example.com", apitypes.ScopeDeploy)
	if got, err := h.c.ListOutsideUptimeChecks(h.ctx, reader); err != nil || len(got) != 1 {
		t.Errorf("reader lists %v, %v", got, err)
	}
	shopAdmin := h.token(testSystem, apitypes.CreateTokenRequest{Name: "shop", Scope: apitypes.ScopeAdmin, Project: "shop"})
	for _, a := range []Actor{reader, dev, shopAdmin} {
		if _, err := h.c.AddOutsideUptimeCheck(h.ctx, a, apitypes.AddUptimeCheckRequest{URL: "https://other.example/"}); CodeOf(err) != apitypes.CodeForbidden {
			t.Errorf("%s adds: %v, want forbidden", a.Name, err)
		}
		if err := h.c.RemoveOutsideUptimeCheck(h.ctx, a, chk.ID); CodeOf(err) != apitypes.CodeForbidden {
			t.Errorf("%s removes: %v, want forbidden", a.Name, err)
		}
	}
	// Another organization neither sees nor removes it.
	org, err := h.store.CreateOrg(h.ctx, store.CreateOrgParams{Slug: "other", Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	them := System.InOrg(org.ID)
	if got, err := h.c.ListOutsideUptimeChecks(h.ctx, them); err != nil || len(got) != 0 {
		t.Errorf("another org lists %v, %v", got, err)
	}
	if err := h.c.RemoveOutsideUptimeCheck(h.ctx, them, chk.ID); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("another org removes: %v, want not found", err)
	}
	// A service's check isn't an outside one.
	svcChk, err := h.c.AddUptimeCheck(h.ctx, testSystem, web, apitypes.AddUptimeCheckRequest{URL: "https://shop.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.c.RemoveOutsideUptimeCheck(h.ctx, testSystem, svcChk.ID); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("removing a service's check as outside: %v, want not found", err)
	}
}

// An outside check is probed like a service's, alerts to the org, feeds
// the status page components that name it, and removing it takes it out
// of them.
func TestOutsideUptimeCheckOnAStatusPage(t *testing.T) {
	h := newHarness(t)
	h.fastUptime()
	const url = "https://status.vendor.example/"
	h.rt.SetProbe(url, node.ProbeResult{Status: 503, Error: "answered 503, expected 200"})
	chk, err := h.c.AddOutsideUptimeCheck(h.ctx, testSystem, apitypes.AddUptimeCheckRequest{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CreateStatusPage(h.ctx, testSystem, apitypes.CreateStatusPageRequest{Slug: "acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SetStatusComponents(h.ctx, testSystem, "acme", apitypes.SetStatusComponentsRequest{Components: []apitypes.StatusComponentInput{
		{Name: "Vendor", Sources: []apitypes.StatusSourceRef{{Check: chk.ID}}},
	}}); err != nil {
		t.Fatal(err)
	}

	a := h.openAlert(uptimeKey(chk.ID), true)
	if a.Kind != string(apitypes.AlertUptimeDown) || a.OrgID == nil || *a.OrgID != 1 || a.ServiceID != nil || a.Title != "status.vendor.example is down" {
		t.Fatalf("alert = %+v", a)
	}
	page := h.eventuallyStatus("acme", apitypes.StatusMajorOutage)
	if src := page.Components[0].Sources; len(src) != 1 || src[0].Check != chk.ID || src[0].Hidden || src[0].Problem != "" {
		t.Fatalf("sources = %+v", src)
	}
	// Its alert makes today a confirmed outage (the day's colour).
	if days := page.Components[0].Days; !days[len(days)-1].Outage || days[len(days)-2].Outage {
		t.Fatalf("days end %+v, want only today an outage", days[len(days)-2:])
	}
	got, err := h.c.ListOutsideUptimeChecks(h.ctx, testSystem)
	if err != nil || len(got) != 1 || got[0].State != apitypes.UptimeDown ||
		!slices.Equal(got[0].UsedBy, []apitypes.UptimeCheckUse{{Page: "acme", Component: "Vendor"}}) {
		t.Fatalf("outside checks = %+v, %v", got, err)
	}

	if err := h.c.RemoveOutsideUptimeCheck(h.ctx, testSystem, chk.ID); err != nil {
		t.Fatal(err)
	}
	h.openAlert(uptimeKey(chk.ID), false)
	p, err := h.c.GetStatusPage(h.ctx, testSystem, "acme")
	if err != nil || len(p.Components[0].Sources) != 0 {
		t.Fatalf("page after removing = %+v, %v", p, err)
	}
	evs, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(evs, func(e apitypes.AuditEvent) bool {
		return e.Action == "uptime.remove" && strings.Contains(e.Summary, "acme/Vendor")
	}) {
		t.Error("no uptime.remove audit event naming acme/Vendor")
	}
}

// eventuallyStatus waits for a status page's overall status.
func (h *harness) eventuallyStatus(slug string, want apitypes.ComponentStatus) apitypes.StatusPage {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p, err := h.c.GetStatusPage(h.ctx, testSystem, slug)
		if err != nil {
			h.t.Fatal(err)
		}
		if p.Status == want {
			return p
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("status page %s is %s, want %s", slug, p.Status, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
