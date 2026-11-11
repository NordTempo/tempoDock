package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// incidentPage is a published page acme with API (built from web's
// first check) and Jobs (manual), and the page's ID.
func incidentPage(t *testing.T) (*harness, []apitypes.UptimeCheck, int64) {
	t.Helper()
	h, checks := statusHarness(t)
	if _, err := h.c.CreateStatusPage(h.ctx, testSystem, apitypes.CreateStatusPageRequest{Slug: "acme", Title: "Acme"}); err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := h.c.SetStatusComponents(h.ctx, testSystem, "acme", apitypes.SetStatusComponentsRequest{Components: []apitypes.StatusComponentInput{
		{Name: "API", Sources: []apitypes.StatusSourceRef{{Service: "shop/production/web"}}},
		{Name: "Jobs", Auto: &off},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.UpdateStatusPage(h.ctx, testSystem, "acme", apitypes.UpdateStatusPageRequest{Published: new(true)}); err != nil {
		t.Fatal(err)
	}
	id, _, ok := h.c.StatusPageAt("status-acme.dock.example.net")
	if !ok {
		t.Fatal("the page isn't served")
	}
	return h, checks, id
}

// componentStatuses are a page's components' statuses and its own.
func (h *harness) componentStatuses(t *testing.T) []apitypes.ComponentStatus {
	t.Helper()
	p, err := h.c.GetStatusPage(h.ctx, testSystem, "acme")
	if err != nil {
		t.Fatal(err)
	}
	var out []apitypes.ComponentStatus
	for _, c := range p.Components {
		out = append(out, c.Status)
	}
	return append(out, p.Status)
}

// An incident is public from its first update: its open impact sets its
// components' status, its updates (edited ones marked) show on the page,
// its own page and the feed, and resolving it leaves its days coloured.
// A draft shows nowhere outside Dock. Components a page keeps keep the
// incidents that name them.
func TestStatusIncidents(t *testing.T) {
	h, _, page := incidentPage(t)
	op, deg, part := apitypes.StatusOperational, apitypes.StatusDegraded, apitypes.StatusPartialOutage

	draft, err := h.c.OpenIncident(h.ctx, testSystem, "acme", apitypes.OpenIncidentRequest{Title: "Slow", State: apitypes.IncidentDraft,
		Message: "Looking.", Components: []apitypes.StatusImpact{{Component: "api", Impact: apitypes.StatusMajorOutage}}})
	if err != nil {
		t.Fatal(err)
	}
	if draft.State != apitypes.IncidentDraft || draft.Draft != "Looking." || len(draft.Updates) != 0 || draft.URL != "" || !ValidPublicID(draft.ID) {
		t.Fatalf("draft = %+v", draft)
	}
	if got := h.componentStatuses(t); got[0] != op || got[2] != op {
		t.Errorf("a draft changed the page: %v", got)
	}

	inc, err := h.c.OpenIncident(h.ctx, testSystem, "acme", apitypes.OpenIncidentRequest{Title: "  API   errors ",
		Message: "We are investigating.\r\nMore soon.", Components: []apitypes.StatusImpact{{Component: "API", Impact: part}}})
	if err != nil {
		t.Fatal(err)
	}
	if inc.Title != "API errors" || inc.State != apitypes.IncidentInvestigating || len(inc.Updates) != 1 ||
		inc.Updates[0].Message != "We are investigating.\nMore soon." || inc.URL != "https://status-acme.dock.example.net/incidents/"+inc.ID {
		t.Fatalf("incident = %+v", inc)
	}
	if got := h.componentStatuses(t); got[0] != part || got[1] != op || got[2] != part {
		t.Errorf("open incident: %v", got)
	}

	if _, err := h.c.PostIncidentUpdate(h.ctx, testSystem, inc.ID, apitypes.PostIncidentUpdateRequest{State: apitypes.IncidentIdentified,
		Message: "Found it.", Components: &[]apitypes.StatusImpact{{Component: "API", Impact: part}, {Component: "Jobs", Impact: deg}}}); err != nil {
		t.Fatal(err)
	}
	if got := h.componentStatuses(t); got[1] != deg {
		t.Errorf("a manual component named by an update: %v", got)
	}
	inc, err = h.c.GetIncident(h.ctx, testSystem, inc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.EditIncidentUpdate(h.ctx, testSystem, inc.ID, inc.Updates[1].ID, apitypes.EditIncidentUpdateRequest{Message: "Found the cause."}); err != nil {
		t.Fatal(err)
	}

	pub, err := h.c.PublicStatusPage(h.ctx, page)
	if err != nil {
		t.Fatal(err)
	}
	if len(pub.Open) != 1 || pub.Open[0].ID != inc.ID || pub.Open[0].State != apitypes.IncidentIdentified || len(pub.Open[0].Updates) != 2 ||
		pub.Open[0].Updates[0].Message != "Found the cause." || !pub.Open[0].Updates[0].Edited || pub.Open[0].Updates[1].Edited {
		t.Fatalf("public open incidents = %+v", pub.Open)
	}
	if _, _, err := h.c.PublicStatusIncident(h.ctx, page, draft.ID); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("a draft's public page: %v", err)
	}

	// The page renames API, keeping it by its ID.
	p, err := h.c.GetStatusPage(h.ctx, testSystem, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SetStatusComponents(h.ctx, testSystem, "acme", apitypes.SetStatusComponentsRequest{Components: []apitypes.StatusComponentInput{
		{ID: p.Components[0].ID, Name: "Public API", Sources: []apitypes.StatusSourceRef{{Service: "shop/production/web"}}},
		{Name: "Jobs", Auto: new(false)},
	}}); err != nil {
		t.Fatal(err)
	}
	if inc, err = h.c.GetIncident(h.ctx, testSystem, inc.ID); err != nil || len(inc.Components) != 2 || inc.Components[0].Component != "Public API" {
		t.Fatalf("after the rename: %+v, %v", inc.Components, err)
	}

	if _, err := h.c.PostIncidentUpdate(h.ctx, testSystem, inc.ID, apitypes.PostIncidentUpdateRequest{State: apitypes.IncidentResolved, Message: "Fixed."}); err != nil {
		t.Fatal(err)
	}
	if got := h.componentStatuses(t); got[0] != op || got[1] != op || got[2] != op {
		t.Errorf("resolved: %v", got)
	}
	if _, err := h.c.PostIncidentUpdate(h.ctx, testSystem, inc.ID, apitypes.PostIncidentUpdateRequest{State: apitypes.IncidentMonitoring, Message: "Again?"}); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("an update on a resolved incident: %v", err)
	}
	if pub, err = h.c.PublicStatusPage(h.ctx, page); err != nil {
		t.Fatal(err)
	}
	today := pub.Groups[0].Components[0].Days[apitypes.StatusDays-1]
	if len(pub.Open) != 0 || len(pub.Recent) != 1 || today.Impact != part || today.Incident != "API errors" ||
		pub.Groups[0].Components[1].Days[apitypes.StatusDays-1].Impact != deg {
		t.Errorf("after resolving: open %d, recent %d, today %+v", len(pub.Open), len(pub.Recent), today)
	}

	one, title, err := h.c.PublicStatusIncident(h.ctx, page, inc.ID)
	if err != nil || title != "Acme" || one.State != apitypes.IncidentResolved || len(one.Updates) != 3 || one.Updates[0].Message != "Fixed." {
		t.Errorf("public incident = %+v, %v", one, err)
	}
	hist, err := h.c.PublicStatusHistory(h.ctx, page)
	if err != nil || len(hist.Incidents) != 1 || hist.Incidents[0].ID != inc.ID {
		t.Errorf("history = %+v, %v", hist, err)
	}
	feed, err := h.c.PublicStatusFeed(h.ctx, page)
	if err != nil || len(feed.Entries) != 3 || feed.Entries[0].Message != "Fixed." || feed.Entries[2].State != apitypes.IncidentInvestigating {
		t.Errorf("feed = %+v, %v", feed, err)
	}
	b, _ := json.Marshal([]any{pub, one, hist, feed})
	for _, private := range []string{"shop", "production", "system", "example.com"} {
		if strings.Contains(string(b), private) {
			t.Errorf("the public side says %q", private)
		}
	}

	if err := h.c.DeleteIncident(h.ctx, h.user("dev@example.com", apitypes.ScopeDeploy), inc.ID); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("a deployer removed an incident: %v", err)
	}
	if err := h.c.DeleteIncident(h.ctx, testSystem, inc.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.c.PublicStatusIncident(h.ctx, page, inc.ID); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("a removed incident's page: %v", err)
	}
}

// An uptime_down alert about a component's sources drafts an incident
// on a published page that takes drafts, once per component while one
// is open, and the draft goes if the alert resolves first. A published
// one stays.
func TestStatusIncidentDrafts(t *testing.T) {
	h, checks, _ := incidentPage(t)
	var svc, project int64
	if err := h.store.DB.QueryRowContext(h.ctx, `SELECT s.id, e.project_id FROM services s JOIN environments e ON e.id = s.environment_id WHERE s.name = 'web'`).Scan(&svc, &project); err != nil {
		t.Fatal(err)
	}
	down := func(chk int64) alertSignal {
		return alertSignal{Key: uptimeKey(chk), Kind: apitypes.AlertUptimeDown, Severity: apitypes.SeverityCritical, Title: "shop.example.com is down",
			Message: "Probes keep failing (secret-token)", Project: "shop", Environment: "production", Service: "web", projectID: project, serviceID: svc}
	}
	incidents := func() []apitypes.StatusIncident {
		t.Helper()
		p, err := h.c.GetStatusPage(h.ctx, testSystem, "acme")
		if err != nil {
			t.Fatal(err)
		}
		return p.Incidents
	}

	h.c.raiseAlert(h.ctx, down(checks[0].ID), time.Now())
	got := incidents()
	if len(got) != 1 || got[0].State != apitypes.IncidentDraft || got[0].Title != "API unavailable" || got[0].CreatedBy != "Dock" ||
		got[0].Alert == nil || got[0].Alert.Service != "shop/production/web" || len(got[0].Components) != 1 || got[0].Components[0].Impact != apitypes.StatusMajorOutage ||
		strings.Contains(got[0].Draft, "secret") {
		t.Fatalf("draft = %+v", got)
	}
	// A token limited to another project doesn't see the alert's details.
	for want, a := range map[bool]Actor{
		true:  {Kind: "token", OrgID: 1, Name: "ci", Scope: apitypes.ScopeRead, ProjectID: project, ProjectName: "shop"},
		false: {Kind: "token", OrgID: 1, Name: "ci", Scope: apitypes.ScopeRead, ProjectID: project + 100, ProjectName: "other"},
	} {
		p, err := h.c.GetStatusPage(h.ctx, a, "acme")
		if err != nil || len(p.Incidents) != 1 || (p.Incidents[0].Alert != nil) != want {
			t.Errorf("%s sees the draft's alert: %+v %v, want %t", a.ProjectName, p.Incidents, err, want)
		}
	}
	h.c.raiseAlert(h.ctx, down(checks[1].ID), time.Now())
	if got := incidents(); len(got) != 1 {
		t.Errorf("a second alert on the same component drafted again: %d incidents", len(got))
	}
	h.c.resolveAlert(h.ctx, uptimeKey(checks[1].ID), time.Now())
	h.c.resolveAlert(h.ctx, uptimeKey(checks[0].ID), time.Now())
	if got := incidents(); len(got) != 0 {
		t.Errorf("the draft outlived its alert: %+v", got)
	}

	h.c.raiseAlert(h.ctx, down(checks[0].ID), time.Now())
	d := incidents()[0]
	if _, err := h.c.PostIncidentUpdate(h.ctx, testSystem, d.ID, apitypes.PostIncidentUpdateRequest{State: apitypes.IncidentInvestigating, Message: d.Draft}); err != nil {
		t.Fatal(err)
	}
	h.c.resolveAlert(h.ctx, uptimeKey(checks[0].ID), time.Now())
	if got := incidents(); len(got) != 1 || got[0].State != apitypes.IncidentInvestigating || got[0].Updates[0].Message != "We are investigating problems with API." {
		t.Errorf("a published draft after its alert resolved: %+v", got)
	}

	if _, err := h.c.UpdateStatusPage(h.ctx, testSystem, "acme", apitypes.UpdateStatusPageRequest{Drafts: new(false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.PostIncidentUpdate(h.ctx, testSystem, d.ID, apitypes.PostIncidentUpdateRequest{State: apitypes.IncidentResolved, Message: "Fixed."}); err != nil {
		t.Fatal(err)
	}
	h.c.raiseAlert(h.ctx, down(checks[1].ID), time.Now())
	if got := incidents(); len(got) != 1 {
		t.Errorf("a page without drafts got one: %+v", got)
	}
}

// People who deploy open and update incidents; a token limited to a
// project only for components built entirely from it; an actor asking
// first not at all. Readers read.
func TestStatusIncidentPermissions(t *testing.T) {
	h, _, _ := incidentPage(t)
	if _, err := h.c.CreateProject(h.ctx, testSystem, apitypes.CreateProjectRequest{Name: "blog"}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, name := range []string{"shop", "blog"} {
		p, err := h.store.GetProjectByName(h.ctx, store.GetProjectByNameParams{OrgID: 1, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = p.ID
	}
	api := []apitypes.StatusImpact{{Component: "API", Impact: apitypes.StatusDegraded}}
	open := func(a Actor, ims []apitypes.StatusImpact) error {
		_, err := h.c.OpenIncident(h.ctx, a, "acme", apitypes.OpenIncidentRequest{Title: "Slow", Message: "Looking.", Components: ims})
		return err
	}
	reader := h.user("reader@example.com", apitypes.ScopeRead)
	if err := open(reader, api); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("a reader opened an incident: %v", err)
	}
	if err := open(h.user("dev@example.com", apitypes.ScopeDeploy).AskFirst(), api); err == nil {
		t.Error("an actor asking first opened an incident")
	}
	blog := Actor{Kind: "token", OrgID: 1, Name: "blog-ci", Scope: apitypes.ScopeDeploy, ProjectID: ids["blog"], ProjectName: "blog"}
	shop := Actor{Kind: "token", OrgID: 1, Name: "shop-ci", Scope: apitypes.ScopeDeploy, ProjectID: ids["shop"], ProjectName: "shop"}
	for _, tc := range []struct {
		a   Actor
		ims []apitypes.StatusImpact
	}{{blog, api}, {shop, nil}, {shop, []apitypes.StatusImpact{{Component: "Jobs", Impact: apitypes.StatusDegraded}}}} {
		if err := open(tc.a, tc.ims); CodeOf(err) != apitypes.CodeForbidden {
			t.Errorf("%s named %v: %v", tc.a.Name, tc.ims, err)
		}
	}
	if err := open(shop, api); err != nil {
		t.Errorf("shop's token on shop's component: %v", err)
	}
	p, err := h.c.GetStatusPage(h.ctx, reader, "acme")
	if err != nil || len(p.Incidents) != 1 {
		t.Fatalf("a reader's page: %+v, %v", p.Incidents, err)
	}
	if _, err := h.c.PostIncidentUpdate(h.ctx, blog, p.Incidents[0].ID, apitypes.PostIncidentUpdateRequest{State: apitypes.IncidentResolved, Message: "Not ours."}); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("blog's token resolved shop's incident: %v", err)
	}
	other, err := h.store.CreateOrg(h.ctx, store.CreateOrgParams{Slug: "other", Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.GetIncident(h.ctx, System.InOrg(other.ID), p.Incidents[0].ID); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("another org read the incident: %v", err)
	}
}
