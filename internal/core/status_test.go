package core

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// statusHarness is a harness with a base domain and status pages served,
// service shop/production/web with two uptime checks and
// shop/production/worker with none.
func statusHarness(t *testing.T) (*harness, []apitypes.UptimeCheck) {
	t.Helper()
	h := newHarness(t)
	h.c.cfg.BaseDomain = "dock.example.net"
	h.c.cfg.Status.Listen = "127.0.0.1:7079"
	web := h.uptimeService()
	h.service("worker", apitypes.CreateServiceRequest{})
	var checks []apitypes.UptimeCheck
	for _, path := range []string{"/", "/api"} {
		chk, err := h.c.AddUptimeCheck(h.ctx, testSystem, web, apitypes.AddUptimeCheckRequest{URL: "https://shop.example.com" + path})
		if err != nil {
			t.Fatal(err)
		}
		checks = append(checks, chk)
	}
	return h, checks
}

// setCheck puts a check's state in memory, as its probes would.
func (h *harness) setCheck(id int64, state apitypes.UptimeState) {
	h.c.uptime.mu.Lock()
	h.c.uptime.checks[id] = &checkState{state: state}
	h.c.uptime.mu.Unlock()
}

// A component's status comes from its checks and its services' health:
// some checks down is a partial outage, all of them a major one, a crash
// loop partial and unhealthy degraded. An asleep environment is idle,
// never down, and a component with auto off says operational.
func TestStatusComponentStatus(t *testing.T) {
	h, checks := statusHarness(t)
	if _, err := h.c.CreateStatusPage(h.ctx, testSystem, apitypes.CreateStatusPageRequest{Slug: "acme", Title: "Acme"}); err != nil {
		t.Fatal(err)
	}
	off := false
	page, err := h.c.SetStatusComponents(h.ctx, testSystem, "acme", apitypes.SetStatusComponentsRequest{Components: []apitypes.StatusComponentInput{
		{Name: "Web app", Group: "Core", Sources: []apitypes.StatusSourceRef{{Service: "shop/production/web"}}},
		{Name: "Front page", Group: "Core", Sources: []apitypes.StatusSourceRef{{Check: checks[0].ID}, {Check: checks[0].ID}}},
		{Name: "Jobs", Sources: []apitypes.StatusSourceRef{{Service: "shop/production/worker"}}},
		{Name: "Billing", Auto: &off, Sources: []apitypes.StatusSourceRef{{Service: "shop/production/web"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if page.Status != apitypes.StatusOperational || len(page.Components) != 4 || page.Components[1].Sources[1].Checks != 1 ||
		page.Components[0].Sources[0].Checks != 2 || len(page.Components[0].Days) != apitypes.StatusDays {
		t.Fatalf("page = %+v", page)
	}
	statuses := func() []apitypes.ComponentStatus {
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
	want := func(what string, w ...apitypes.ComponentStatus) {
		t.Helper()
		if got := statuses(); !slices.Equal(got, w) {
			t.Errorf("%s: %v, want %v", what, got, w)
		}
	}
	op, part, major := apitypes.StatusOperational, apitypes.StatusPartialOutage, apitypes.StatusMajorOutage

	h.setCheck(checks[1].ID, apitypes.UptimeDown)
	want("one of two checks down", part, op, op, op, part)
	h.setCheck(checks[0].ID, apitypes.UptimeDown)
	// Front page names check 0 twice: counted once.
	want("both down", major, major, op, op, major)
	h.setCheck(checks[0].ID, apitypes.UptimeUp)
	h.setCheck(checks[1].ID, apitypes.UptimeUnknown)
	want("up again, one unknown after a restart", op, op, op, op, op)

	var svc store.Service
	if err := h.store.DB.QueryRowContext(h.ctx, `SELECT id, environment_id FROM services WHERE name = 'worker'`).Scan(&svc.ID, &svc.EnvironmentID); err != nil {
		t.Fatal(err)
	}
	h.c.alerts.mu.Lock()
	h.c.alerts.open[healthAlertKey(svc.ID)] = store.Alert{}
	h.c.alerts.mu.Unlock()
	want("worker unhealthy", op, op, apitypes.StatusDegraded, op, apitypes.StatusDegraded)
	h.c.alerts.mu.Lock()
	h.c.alerts.open["crash:"+alertServiceKey(svc.ID, "", "", "")] = store.Alert{}
	h.c.alerts.mu.Unlock()
	want("worker crash looping", op, op, part, op, part)

	h.c.sleep.mu.Lock()
	h.c.sleep.envs[svc.EnvironmentID] = &sleepEnv{asleep: true}
	h.c.sleep.mu.Unlock()
	h.setCheck(checks[0].ID, apitypes.UptimeDown)
	idle := apitypes.StatusIdle
	want("environment asleep", idle, idle, idle, op, op)
}

// Sources are the org's live services and their checks: never a preview,
// another org's service or a check of one.
func TestStatusComponentSources(t *testing.T) {
	h, checks := statusHarness(t)
	if _, err := h.c.CreateStatusPage(h.ctx, testSystem, apitypes.CreateStatusPageRequest{Slug: "acme"}); err != nil {
		t.Fatal(err)
	}
	org, err := h.store.CreateOrg(h.ctx, store.CreateOrgParams{Slug: "other", Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	them := System.InOrg(org.ID)
	if _, err := h.c.CreateProject(h.ctx, them, apitypes.CreateProjectRequest{Name: "theirs"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CreateService(h.ctx, them, EnvRef{"theirs", "production"}, apitypes.CreateServiceRequest{Name: "web", Port: 80}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CreateStatusPage(h.ctx, them, apitypes.CreateStatusPageRequest{Slug: "theirs"}); err != nil {
		t.Fatal(err)
	}
	p, err := h.store.GetProjectByName(h.ctx, store.GetProjectByNameParams{OrgID: 1, Name: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "pr-7"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CreateService(h.ctx, testSystem, EnvRef{"shop", "pr-7"}, apitypes.CreateServiceRequest{Name: "web", Port: 80}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB.ExecContext(h.ctx, `INSERT INTO previews (project_id, environment_id, base_environment_id, repo, number, head_branch, head_sha, pr_updated_at, created_at, updated_at)
		SELECT ?, ?, id, 'acme/shop', 7, 'feature', 'abc', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP FROM environments WHERE project_id = ? AND name = 'production'`, p.ID, pr.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	set := func(src apitypes.StatusSourceRef) error {
		_, err := h.c.SetStatusComponents(h.ctx, testSystem, "acme", apitypes.SetStatusComponentsRequest{Components: []apitypes.StatusComponentInput{{Name: "Web", Sources: []apitypes.StatusSourceRef{src}}}})
		return err
	}
	for _, src := range []apitypes.StatusSourceRef{{Service: "shop/pr-7/web"}, {Service: "theirs/production/web"}, {Service: "shop/production/nope"}, {Check: 9999}} {
		if err := set(src); CodeOf(err) != apitypes.CodeNotFound {
			t.Errorf("source %+v: %v, want not found", src, err)
		}
	}
	if err := set(apitypes.StatusSourceRef{Service: "shop/production/web", Check: checks[0].ID}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("a source naming both: %v", err)
	}
	if _, err := h.c.SetStatusComponents(h.ctx, them, "theirs", apitypes.SetStatusComponentsRequest{Components: []apitypes.StatusComponentInput{
		{Name: "Web", Sources: []apitypes.StatusSourceRef{{Check: checks[0].ID}}}}}); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("another org named our check: %v", err)
	}
	if _, err := h.c.SetStatusComponents(h.ctx, testSystem, "acme", apitypes.SetStatusComponentsRequest{Components: []apitypes.StatusComponentInput{
		{Name: "Web"}, {Name: "web"}}}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("two components with one name: %v", err)
	}
	cands, err := h.c.ListStatusSourceCandidates(h.ctx, testSystem)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range cands {
		names = append(names, c.Service)
	}
	if !slices.Equal(names, []string{"shop/production/web", "shop/production/worker"}) || len(cands[0].Checks) != 2 {
		t.Errorf("candidates = %+v", cands)
	}
}

// A page's address is one page's in the whole install, served only once
// published, on its own listener with certificates; services and other
// orgs can't take it. Its public view holds the names people chose and
// nothing about services or checks.
func TestStatusPagePublished(t *testing.T) {
	h, checks := statusHarness(t)
	if _, err := h.c.CreateStatusPage(h.ctx, testSystem, apitypes.CreateStatusPageRequest{Slug: "acme", Title: "Acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SetStatusComponents(h.ctx, testSystem, "acme", apitypes.SetStatusComponentsRequest{Components: []apitypes.StatusComponentInput{
		{Name: "API", Group: "Core", Sources: []apitypes.StatusSourceRef{{Check: checks[1].ID}}},
	}}); err != nil {
		t.Fatal(err)
	}
	host := h.pageAddress("acme")
	if _, _, ok := h.c.StatusPageAt(host); ok {
		t.Error("an unpublished page is served")
	}
	if ok, _ := h.c.AllowCertificate(h.ctx, host); ok {
		t.Error("an unpublished page gets a certificate")
	}
	org, err := h.store.CreateOrg(h.ctx, store.CreateOrgParams{Slug: "other", Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	// Names are the organization's: another may use the same, at an
	// address of its own; this one may not.
	if theirs, err := h.c.CreateStatusPage(h.ctx, System.InOrg(org.ID), apitypes.CreateStatusPageRequest{Slug: "acme"}); err != nil || theirs.Address == host {
		t.Errorf("another org's page called acme: %q, %v", theirs.Address, err)
	}
	if _, err := h.c.CreateStatusPage(h.ctx, testSystem, apitypes.CreateStatusPageRequest{Slug: "acme"}); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("a second page called acme in the org: %v", err)
	}
	if _, err := h.c.AddDomain(h.ctx, testSystem, ServiceRef{"shop", "production", "web"}, apitypes.AddDomainRequest{Hostname: host}); CodeOf(err) != apitypes.CodeHostnameTaken {
		t.Errorf("a service took the page's address: %v", err)
	}

	on := true
	page, err := h.c.UpdateStatusPage(h.ctx, testSystem, "acme", apitypes.UpdateStatusPageRequest{Published: &on})
	if err != nil {
		t.Fatal(err)
	}
	if page.URL != "https://"+host || !page.Published {
		t.Fatalf("published page = %+v", page.StatusPageSummary)
	}
	id, _, ok := h.c.StatusPageAt(host)
	if !ok {
		t.Fatal("a published page isn't served")
	}
	if ok, _ := h.c.AllowCertificate(h.ctx, host); !ok {
		t.Error("a published page gets no certificate")
	}
	routes := h.rt.Routes()
	if !slices.ContainsFunc(routes, func(r node.Route) bool {
		return r.Upstream == "127.0.0.1:7079" && slices.Equal(r.Hostnames, []string{host}) && r.Access == nil && r.ClientIPHeader == ClientIPHeader
	}) {
		t.Errorf("no route to the status listener: %+v", routes)
	}

	h.setCheck(checks[1].ID, apitypes.UptimeDown)
	pub, err := h.c.PublicStatusPage(h.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if pub.Title != "Acme" || pub.Status != apitypes.StatusMajorOutage || len(pub.Groups) != 1 || pub.Groups[0].Name != "Core" ||
		pub.Groups[0].Components[0].Name != "API" || len(pub.Groups[0].Components[0].Days) != apitypes.StatusDays {
		t.Fatalf("public page = %+v", pub)
	}
	b, _ := json.Marshal(pub)
	for _, private := range []string{"shop", "production", "web", "example.com", "/api"} {
		if strings.Contains(string(b), private) {
			t.Errorf("the public page says %q: %s", private, b)
		}
	}

	if _, err := h.c.UpdateStatusPage(h.ctx, testSystem, "acme", apitypes.UpdateStatusPageRequest{Published: new(false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.PublicStatusPage(h.ctx, id); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("an unpublished page's public view: %v", err)
	}
	if err := h.c.DeleteStatusPage(h.ctx, testSystem, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.c.statusPageHost(host); ok {
		t.Error("a removed page still holds its address")
	}
}

// Renaming a page moves its address, keeping its random part; the old
// name keeps finding the page and its address redirects, routed with a
// certificate, until another of the org's pages takes the name. A page
// keeps its statusOldSlugs newest old names.
func TestStatusPageRename(t *testing.T) {
	h, _ := statusHarness(t)
	admin := h.user("admin@example.com", apitypes.ScopeAdmin)
	if _, err := h.c.CreateStatusPage(h.ctx, admin, apitypes.CreateStatusPageRequest{Slug: "api", Title: "API"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SetStatusComponents(h.ctx, admin, "api", apitypes.SetStatusComponentsRequest{Components: []apitypes.StatusComponentInput{
		{Name: "Web", Sources: []apitypes.StatusSourceRef{{Service: "shop/production/web"}}}}}); err != nil {
		t.Fatal(err)
	}
	oldHost := h.pageAddress("api")
	key := strings.TrimSuffix(strings.TrimPrefix(oldHost, "status-api-"), ".dock.example.net")
	newHost := "status-tempo-api-" + key + ".dock.example.net"
	p, err := h.c.UpdateStatusPage(h.ctx, admin, "api", apitypes.UpdateStatusPageRequest{Slug: new("tempo-api"), Title: new("Tempo API"), Published: new(true)})
	if err != nil {
		t.Fatal(err)
	}
	if p.Slug != "tempo-api" || p.Title != "Tempo API" || p.URL != "https://"+newHost || p.Address != newHost || len(p.Components) != 1 {
		t.Fatalf("renamed page = %+v at %s", p.StatusPageSummary, p.Address)
	}
	if _, _, ok := h.c.StatusPageAt(newHost); !ok || !h.routed(newHost) {
		t.Error("the new address isn't served")
	}
	if _, _, ok := h.c.StatusPageAt(oldHost); ok {
		t.Error("the old address serves the page instead of redirecting")
	}
	if to, ok := h.c.StatusPageMoved(oldHost); !ok || to != "https://"+newHost || !h.routed(oldHost) {
		t.Errorf("old address moved to %q, %v, routed %v", to, ok, h.routed(oldHost))
	}
	if ok, _ := h.c.AllowCertificate(h.ctx, oldHost); !ok {
		t.Error("the old address gets no certificate")
	}
	if got, err := h.c.GetStatusPage(h.ctx, admin, "api"); err != nil || got.Slug != "tempo-api" {
		t.Errorf("the old name finds %+v, %v", got.StatusPageSummary, err)
	}
	if evs, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: "status_page.rename"}); len(evs) != 1 ||
		evs[0].Summary != "renamed status page api to tempo-api; "+oldHost+" redirects to its new address" {
		t.Errorf("rename audit: %+v", evs)
	}

	// The rules of a new page's name.
	if _, err := h.c.CreateStatusPage(h.ctx, admin, apitypes.CreateStatusPageRequest{Slug: "other"}); err != nil {
		t.Fatal(err)
	}
	for slug, code := range map[string]apitypes.ErrorCode{
		"tempo-api": apitypes.CodeConflict, // another of the org's pages
		"Not_Valid": apitypes.CodeValidation,
	} {
		if _, err := h.c.UpdateStatusPage(h.ctx, admin, "other", apitypes.UpdateStatusPageRequest{Slug: new(slug)}); CodeOf(err) != code {
			t.Errorf("renamed to %s: %v, want %s", slug, err, code)
		}
	}
	if _, err := h.c.AddDomain(h.ctx, testSystem, ServiceRef{"shop", "production", "web"}, apitypes.AddDomainRequest{Hostname: oldHost}); CodeOf(err) != apitypes.CodeHostnameTaken {
		t.Errorf("a service took a redirecting address: %v", err)
	}
	// Another org's page may have the name, and its old name is no
	// business of ours.
	org, err := h.store.CreateOrg(h.ctx, store.CreateOrgParams{Slug: "elsewhere", Name: "elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	them := System.InOrg(org.ID)
	if _, err := h.c.CreateStatusPage(h.ctx, them, apitypes.CreateStatusPageRequest{Slug: "api"}); err != nil {
		t.Fatalf("another org's page called api: %v", err)
	}
	if _, ok := h.c.StatusPageMoved(oldHost); !ok {
		t.Error("another org's page took our old address")
	}
	if got, err := h.c.GetStatusPage(h.ctx, them, "api"); err != nil || got.Slug != "api" || got.Address == oldHost {
		t.Errorf("another org's api: %+v at %s, %v", got.StatusPageSummary, got.Address, err)
	}

	// Another of our pages may take the old name; the redirect stops.
	if p, err = h.c.UpdateStatusPage(h.ctx, admin, "other", apitypes.UpdateStatusPageRequest{Slug: new("api")}); err != nil || p.Slug != "api" {
		t.Fatalf("took the old name: %+v, %v", p.StatusPageSummary, err)
	}
	if _, ok := h.c.StatusPageMoved(oldHost); ok {
		t.Error("a taken old name still redirects")
	}
	if got, err := h.c.GetStatusPage(h.ctx, admin, "api"); err != nil || got.Title != "other" || got.Address == oldHost {
		t.Errorf("the taken name finds %+v at %s, %v", got.StatusPageSummary, got.Address, err)
	}

	slug := "tempo-api"
	for i := range statusOldSlugs + 2 {
		next := fmt.Sprintf("tempo-%d", i)
		if _, err := h.c.UpdateStatusPage(h.ctx, admin, slug, apitypes.UpdateStatusPageRequest{Slug: &next}); err != nil {
			t.Fatal(err)
		}
		slug = next
	}
	moved := 0
	for host := range *h.c.statusPages.hosts.Load() {
		if _, ok := h.c.StatusPageMoved(host); ok {
			moved++
		}
	}
	if _, ok := h.c.StatusPageMoved("status-tempo-0-" + key + ".dock.example.net"); moved != statusOldSlugs || ok {
		t.Errorf("%d old addresses redirect, tempo-0 too: %v; want the newest %d", moved, ok, statusOldSlugs)
	}
	// Renamed back, it takes its old address again.
	if p, err := h.c.UpdateStatusPage(h.ctx, admin, slug, apitypes.UpdateStatusPageRequest{Slug: new("tempo-3")}); err != nil || p.Address != "status-tempo-3-"+key+".dock.example.net" {
		t.Errorf("renamed back: %q, %v", p.Address, err)
	}
}

// A page from before random parts keeps its address, status-<slug>, until
// it is renamed; then it gets a random part and the old address
// redirects (ADR 0087).
func TestStatusPageFromBeforeKeys(t *testing.T) {
	h, _ := statusHarness(t)
	if _, err := h.c.CreateStatusPage(h.ctx, testSystem, apitypes.CreateStatusPageRequest{Slug: "status"}); err != nil {
		t.Fatal(err)
	}
	// As migration 0120 leaves it.
	if _, err := h.store.DB.ExecContext(h.ctx, `UPDATE status_pages SET address_key = '', published = 1`); err != nil {
		t.Fatal(err)
	}
	h.c.statusPagesChanged(h.ctx)
	const legacy = "status-status.dock.example.net"
	if _, _, ok := h.c.StatusPageAt(legacy); !ok {
		t.Fatal("the page isn't served at its old address")
	}
	// Saile's report: another org names its page the same.
	org, err := h.store.CreateOrg(h.ctx, store.CreateOrgParams{Slug: "other", Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if theirs, err := h.c.CreateStatusPage(h.ctx, System.InOrg(org.ID), apitypes.CreateStatusPageRequest{Slug: "status"}); err != nil || theirs.Address == legacy {
		t.Fatalf("another org's page called status: %q, %v", theirs.Address, err)
	}
	p, err := h.c.UpdateStatusPage(h.ctx, testSystem, "status", apitypes.UpdateStatusPageRequest{Slug: new("uptime")})
	if err != nil || !generatedLike("status-uptime", "dock.example.net").MatchString(p.Address) {
		t.Fatalf("renamed: %q, %v", p.Address, err)
	}
	if to, ok := h.c.StatusPageMoved(legacy); !ok || to != "https://"+p.Address {
		t.Errorf("the old address moved to %q, %v", to, ok)
	}
}

// Members read pages; only admins acting for the whole org change them.
// A token limited to a project sees other projects' sources unnamed.
func TestStatusPagePermissions(t *testing.T) {
	h, _ := statusHarness(t)
	if _, err := h.c.CreateProject(h.ctx, testSystem, apitypes.CreateProjectRequest{Name: "blog"}); err != nil {
		t.Fatal(err)
	}
	blog, err := h.store.GetProjectByName(h.ctx, store.GetProjectByNameParams{OrgID: 1, Name: "blog"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CreateStatusPage(h.ctx, testSystem, apitypes.CreateStatusPageRequest{Slug: "acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SetStatusComponents(h.ctx, testSystem, "acme", apitypes.SetStatusComponentsRequest{Components: []apitypes.StatusComponentInput{
		{Name: "Web", Sources: []apitypes.StatusSourceRef{{Service: "shop/production/web"}}}}}); err != nil {
		t.Fatal(err)
	}
	limited := Actor{Kind: "token", OrgID: 1, Name: "blog-ci", Scope: apitypes.ScopeAdmin, ProjectID: blog.ID, ProjectName: "blog"}
	for _, a := range []Actor{h.user("reader@example.com", apitypes.ScopeRead), h.user("dev@example.com", apitypes.ScopeDeploy), limited} {
		page, err := h.c.GetStatusPage(h.ctx, a, "acme")
		if err != nil {
			t.Fatalf("%s reads the page: %v", a.Name, err)
		}
		if src := page.Components[0].Sources[0]; (a.ProjectID != 0) != (src.Hidden && src.Service == "") {
			t.Errorf("%s sees source %+v", a.Name, src)
		}
		if _, err := h.c.CreateStatusPage(h.ctx, a, apitypes.CreateStatusPageRequest{Slug: "mine"}); CodeOf(err) != apitypes.CodeForbidden {
			t.Errorf("%s made a page: %v", a.Name, err)
		}
		if _, err := h.c.UpdateStatusPage(h.ctx, a, "acme", apitypes.UpdateStatusPageRequest{Title: new("Mine")}); CodeOf(err) != apitypes.CodeForbidden {
			t.Errorf("%s changed the page: %v", a.Name, err)
		}
		if _, err := h.c.UpdateStatusPage(h.ctx, a, "acme", apitypes.UpdateStatusPageRequest{Slug: new("mine")}); CodeOf(err) != apitypes.CodeForbidden {
			t.Errorf("%s renamed the page: %v", a.Name, err)
		}
		if _, err := h.c.SetStatusComponents(h.ctx, a, "acme", apitypes.SetStatusComponentsRequest{}); CodeOf(err) != apitypes.CodeForbidden {
			t.Errorf("%s set components: %v", a.Name, err)
		}
		if err := h.c.DeleteStatusPage(h.ctx, a, "acme"); CodeOf(err) != apitypes.CodeForbidden {
			t.Errorf("%s removed the page: %v", a.Name, err)
		}
	}
	evs, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(evs, func(e apitypes.AuditEvent) bool { return e.Action == "status_page.components" }) {
		t.Error("no status_page.components audit event")
	}
}

// Day rows sum a UTC day's hour rows once the day's last hour is rolled
// up, rolling again changes nothing, and a page's history reads the day
// the same whichever rows the reader takes it from. The statistics tick
// runs in the harness too and may roll or move where reads switch from
// day to hour rows at any moment, so the test reads only once both kinds
// hold the day.
func TestStatusHistoryDays(t *testing.T) {
	h, checks := statusHarness(t)
	if _, err := h.c.CreateStatusPage(h.ctx, testSystem, apitypes.CreateStatusPageRequest{Slug: "acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SetStatusComponents(h.ctx, testSystem, "acme", apitypes.SetStatusComponentsRequest{Components: []apitypes.StatusComponentInput{
		{Name: "Web", Sources: []apitypes.StatusSourceRef{{Service: "shop/production/web"}}}}}); err != nil {
		t.Fatal(err)
	}
	p, err := h.store.GetStatusPage(h.ctx, store.GetStatusPageParams{OrgID: 1, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	day := at.Truncate(24 * time.Hour)
	// The day before yesterday (before the last day, which reads come
	// from minute rows until the first roll-up after a start): 3 of 4
	// probes up across two hours of check 0, 1 of 1 of check 1. Hour
	// rows, as the hourly roll-up leaves them.
	// One statement: the uptime scheduler rolls days up too, and one of
	// its roll-ups between separate inserts would leave a partial day row
	// that rollUptimeDays then carries on after.
	before := day.Add(-24 * time.Hour)
	if _, err := h.store.DB.ExecContext(h.ctx, `INSERT INTO uptime_stats (check_id, res, at, up, total, latency_ms_sum) VALUES
		(?, 3600, ?, 1, 2, 0), (?, 3600, ?, 2, 2, 0), (?, 3600, ?, 1, 1, 0)`,
		checks[0].ID, before.Unix()-7200, checks[0].ID, before.Unix()-3600, checks[1].ID, before.Unix()-3600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		next, err := h.c.rollUptimeDays(h.ctx, at, day.Unix(), 0)
		if err != nil || next != day.Unix() {
			t.Fatalf("rolled days to %d, %v; want %d", next, err, day.Unix())
		}
		var n, up, total int64
		if err := h.store.DB.QueryRowContext(h.ctx, `SELECT count(*), sum(up), sum(total) FROM uptime_stats WHERE res = 86400 AND at = ?`,
			before.Unix()-86400).Scan(&n, &up, &total); err != nil || n != 2 || up != 4 || total != 5 {
			t.Fatalf("the day's day rows: %d rows, %d of %d up, %v; want one per check, 4 of 5", n, up, total, err)
		}
	}
	comps, err := h.c.computeStatus(h.ctx, p, nil, at, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, want := comps[0].days[apitypes.StatusDays-3], before.Add(-24*time.Hour).Format(time.DateOnly)
	if d.Date != want || d.Uptime == nil || *d.Uptime != 80 {
		t.Fatalf("day = %s %v, want %s at 80%%", d.Date, d.Uptime, want)
	}
}

// pageAddress is the org's page's generated address, checked to be
// status-<slug>-<random>.<base> (ADR 0087).
func (h *harness) pageAddress(slug string) string {
	h.t.Helper()
	p, err := h.c.GetStatusPage(h.ctx, testSystem, slug)
	if err != nil {
		h.t.Fatal(err)
	}
	if !generatedLike("status-"+p.Slug, p.BaseDomain).MatchString(p.Address) {
		h.t.Fatalf("page %s has address %q, want status-%s-<random>.%s", slug, p.Address, p.Slug, p.BaseDomain)
	}
	return p.Address
}
