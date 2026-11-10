package core

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync/atomic"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Public status pages (ADR 0073). An organization has up to
// statusMaxPages pages; each lists components with public names, built
// from services (their uptime checks and health) and single uptime
// checks. A published page is served at status-<slug>.<base domain> by
// the control plane's Caddy, through [status] listen (api.StatusPages),
// which renders it when it is viewed after a change and serves the
// cached bytes otherwise.
//
// A component's status is worked out from memory: the checks' states
// (checkStatus), the open crash loop and unhealthy alerts, and whether
// the environment is asleep. Its history is the checks' rows in
// uptime_stats, whose day rows cover 90 days.

const (
	statusMaxPages      = 5
	statusMaxComponents = 100
	statusMaxSources    = 10
)

// statusState is what Dock holds in memory for status pages: which
// hostname is which page.
type statusState struct {
	hosts atomic.Pointer[map[string]statusHost]
}

// statusHost is a page a hostname belongs to.
type statusHost struct {
	id        int64
	published bool
}

// statusHostname is a page's address under the control plane's base
// domain, "" without one.
func statusHostname(slug, base string) string {
	if base == "" {
		return ""
	}
	return "status-" + slug + "." + base
}

// statusPageHost is the page a hostname holds, published or not.
func (c *Core) statusPageHost(host string) (statusHost, bool) {
	m := c.statusPages.hosts.Load()
	if m == nil {
		return statusHost{}, false
	}
	h, ok := (*m)[host]
	return h, ok
}

// StatusPageAt is the published page served at host, for the public
// listener, with the generation of what pages show: a page rendered
// under an older one is stale.
func (c *Core) StatusPageAt(host string) (id, gen int64, ok bool) {
	h, ok := c.statusPageHost(host)
	return h.id, c.bus.statusGen.Load(), ok && h.published && c.cfg.Status.Listen != ""
}

// statusPagesChanged rebuilds the pages' hostnames and, when the
// published ones changed, routes them on the control plane's server. It
// also makes every rendered page stale.
func (c *Core) statusPagesChanged(ctx context.Context) {
	c.bus.statusGen.Add(1)
	hosts, err := c.loadStatusHosts(ctx)
	if err != nil {
		c.log.Error("status pages: read the pages; their addresses stay as they were", "err", err)
		return
	}
	old := c.statusPages.hosts.Swap(&hosts)
	if old != nil && maps.Equal(*old, hosts) {
		return
	}
	if err := c.applyRoutes(ctx, c.localID); err != nil {
		c.log.Error("status pages: route the pages", "err", err)
	}
}

// loadStatusHosts reads every page's hostname: none without a base
// domain.
func (c *Core) loadStatusHosts(ctx context.Context) (map[string]statusHost, error) {
	hosts := map[string]statusHost{}
	base, err := c.publicBase(ctx)
	if err != nil || base == "" {
		return hosts, err
	}
	rows, err := c.store.ListStatusPageHosts(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		hosts[statusHostname(r.Slug, base)] = statusHost{id: r.ID, published: r.Published}
	}
	return hosts, nil
}

// publishedStatusHosts are the hostnames of published pages, for the
// control plane's routes.
func (c *Core) publishedStatusHosts() []string {
	m := c.statusPages.hosts.Load()
	if m == nil || c.cfg.Status.Listen == "" {
		return nil
	}
	var out []string
	for h, p := range *m {
		if p.published {
			out = append(out, h)
		}
	}
	return out
}

// statusPagesIn checks the actor may change the org's status pages:
// admins with an unlimited token or session, since a page spans
// projects and speaks for the org.
func statusPagesIn(a Actor, action string) error {
	return a.requireUnlimited(apitypes.ScopeAdmin, action)
}

// loadStatusPage is the org's page by slug, for an actor with need.
func (c *Core) loadStatusPage(ctx context.Context, a Actor, slug string, need apitypes.Scope, action string) (store.StatusPage, error) {
	var err error
	if need == apitypes.ScopeAdmin {
		err = statusPagesIn(a, action)
	} else {
		err = a.require(need, action)
	}
	if err != nil {
		return store.StatusPage{}, err
	}
	p, err := c.store.GetStatusPage(ctx, store.GetStatusPageParams{OrgID: a.OrgID, Slug: slug})
	if store.IsNotFound(err) {
		return p, notFound("status page %q not found; `dock status-page list` shows the organization's", slug)
	}
	return p, err
}

// ListStatusPages lists the org's status pages. Read scope.
func (c *Core) ListStatusPages(ctx context.Context, a Actor) ([]apitypes.StatusPageSummary, error) {
	if err := a.require(apitypes.ScopeRead, "reading status pages"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListStatusPages(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	base, err := c.publicBase(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := c.store.CountStatusComponents(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	n := make(map[int64]int, len(counts))
	for _, r := range counts {
		n[r.PageID] = int(r.Components)
	}
	out := make([]apitypes.StatusPageSummary, len(rows))
	for i, p := range rows {
		out[i] = c.toStatusSummary(p, base, n[p.ID])
	}
	return out, nil
}

// toStatusSummary is a page in lists.
func (c *Core) toStatusSummary(p store.StatusPage, base string, components int) apitypes.StatusPageSummary {
	out := apitypes.StatusPageSummary{Slug: p.Slug, Title: p.Title, Published: p.Published, Components: components,
		CreatedAt: p.CreatedAt, CreatedBy: p.CreatedBy}
	if h := statusHostname(p.Slug, base); h != "" {
		out.URL = c.controlURL(h)
	}
	return out
}

// CreateStatusPage makes an unpublished page. Organization admins.
func (c *Core) CreateStatusPage(ctx context.Context, a Actor, req apitypes.CreateStatusPageRequest) (apitypes.StatusPage, error) {
	if err := statusPagesIn(a, "making a status page"); err != nil {
		return apitypes.StatusPage{}, err
	}
	if err := validName("status page", req.Slug); err != nil {
		return apitypes.StatusPage{}, err
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = req.Slug
	}
	base, err := c.publicBase(ctx)
	if err != nil {
		return apitypes.StatusPage{}, err
	}
	host := statusHostname(req.Slug, base)
	if err := c.checkStatusHost(ctx, host); err != nil {
		return apitypes.StatusPage{}, err
	}
	var p store.StatusPage
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		n, err := q.CountOrgStatusPages(ctx, a.OrgID)
		if err != nil {
			return err
		}
		if n >= statusMaxPages {
			return conflict("the organization has %d status pages, the most it may have; remove one first (`dock status-page rm`)", n)
		}
		if taken, err := q.StatusSlugTaken(ctx, req.Slug); err != nil {
			return err
		} else if taken {
			return errf(apitypes.CodeHostnameTaken, "a status page called %s exists, here or in another organization; its address would be the same, so pick another name", req.Slug)
		}
		p, err = q.CreateStatusPage(ctx, store.CreateStatusPageParams{OrgID: a.OrgID, Slug: req.Slug, Title: title, CreatedAt: now(), CreatedBy: a.Name})
		return err
	})
	if err != nil {
		return apitypes.StatusPage{}, err
	}
	c.audit(ctx, a, "status_page.create", nil, nil, "status page "+p.Slug, "made status page "+p.Slug)
	c.statusPagesChanged(ctx)
	return c.statusPage(ctx, a, p)
}

// checkStatusHost refuses a page address something else answers on: a
// service's domain, Dock's own hostnames, a public dashboard.
func (c *Core) checkStatusHost(ctx context.Context, host string) error {
	if host == "" {
		return nil
	}
	if host == c.cfg.Hooks.PublicHost || host == c.publicHost() || c.publicSite(host) != 0 {
		return errf(apitypes.CodeHostnameTaken, "%s is taken by Dock itself; pick another name", host)
	}
	switch _, err := c.store.GetDomainByHostname(ctx, host); {
	case err == nil:
		return errf(apitypes.CodeHostnameTaken, "%s is a service's domain; pick another name", host)
	case !store.IsNotFound(err):
		return err
	}
	return nil
}

// GetStatusPage is a page with its components' live status and history.
// Read scope.
func (c *Core) GetStatusPage(ctx context.Context, a Actor, slug string) (apitypes.StatusPage, error) {
	p, err := c.loadStatusPage(ctx, a, slug, apitypes.ScopeRead, "reading status pages")
	if err != nil {
		return apitypes.StatusPage{}, err
	}
	return c.statusPage(ctx, a, p)
}

// UpdateStatusPage changes a page's title and settings, and publishes
// or unpublishes it. Organization admins.
func (c *Core) UpdateStatusPage(ctx context.Context, a Actor, slug string, req apitypes.UpdateStatusPageRequest) (apitypes.StatusPage, error) {
	p, err := c.loadStatusPage(ctx, a, slug, apitypes.ScopeAdmin, "changing a status page")
	if err != nil {
		return apitypes.StatusPage{}, err
	}
	next := store.UpdateStatusPageParams{ID: p.ID, Title: p.Title, Published: p.Published, ShowUptime: p.ShowUptime}
	var changes []string
	if req.Title != nil {
		t := strings.TrimSpace(*req.Title)
		if t == "" {
			return apitypes.StatusPage{}, invalid("a status page's title can't be empty")
		}
		if t != p.Title {
			next.Title = t
			changes = append(changes, fmt.Sprintf("title %q", t))
		}
	}
	if req.ShowUptime != nil && *req.ShowUptime != p.ShowUptime {
		next.ShowUptime = *req.ShowUptime
		changes = append(changes, map[bool]string{true: "shows uptime", false: "hides uptime"}[next.ShowUptime])
	}
	action := "status_page.update"
	if req.Published != nil && *req.Published != p.Published {
		next.Published = *req.Published
		if next.Published {
			base, err := c.publicBase(ctx)
			if err != nil {
				return apitypes.StatusPage{}, err
			}
			if base == "" {
				return apitypes.StatusPage{}, conflict("the control plane's server has no base domain, so a status page has no address; set one with `dock server update --base-domain`")
			}
			if err := c.checkStatusHost(ctx, statusHostname(p.Slug, base)); err != nil {
				return apitypes.StatusPage{}, err
			}
			action = "status_page.publish"
		} else {
			action = "status_page.unpublish"
		}
		changes = append(changes, strings.TrimPrefix(action, "status_page.")+"ed")
	}
	if len(changes) == 0 {
		return c.statusPage(ctx, a, p)
	}
	if p, err = c.store.UpdateStatusPage(ctx, next); err != nil {
		return apitypes.StatusPage{}, err
	}
	c.audit(ctx, a, action, nil, nil, "status page "+p.Slug, "status page "+p.Slug+": "+strings.Join(changes, ", "))
	c.statusPagesChanged(ctx)
	return c.statusPage(ctx, a, p)
}

// DeleteStatusPage removes a page and its components; its address
// answers 404 at once. Organization admins.
func (c *Core) DeleteStatusPage(ctx context.Context, a Actor, slug string) error {
	p, err := c.loadStatusPage(ctx, a, slug, apitypes.ScopeAdmin, "removing a status page")
	if err != nil {
		return err
	}
	if err := c.store.DeleteStatusPage(ctx, p.ID); err != nil {
		return err
	}
	c.audit(ctx, a, "status_page.delete", nil, nil, "status page "+p.Slug, "removed status page "+p.Slug)
	c.statusPagesChanged(ctx)
	return nil
}

// statusCandidate is a service a component may be built from.
type statusCandidate struct {
	serviceID, projectID, envID int64
	checks                      []int64
}

// SetStatusComponents replaces a page's components with the list given,
// in order. Sources name services (project/environment/service) or
// uptime checks of the org; previews are refused. Organization admins.
func (c *Core) SetStatusComponents(ctx context.Context, a Actor, slug string, req apitypes.SetStatusComponentsRequest) (apitypes.StatusPage, error) {
	p, err := c.loadStatusPage(ctx, a, slug, apitypes.ScopeAdmin, "changing a status page's components")
	if err != nil {
		return apitypes.StatusPage{}, err
	}
	if len(req.Components) > statusMaxComponents {
		return apitypes.StatusPage{}, invalid("a status page has at most %d components", statusMaxComponents)
	}
	rows, err := c.store.ListStatusSourceCandidates(ctx, a.OrgID)
	if err != nil {
		return apitypes.StatusPage{}, err
	}
	byName := map[string]*statusCandidate{}
	checkSvc := map[int64]int64{}
	for _, r := range rows {
		key := r.Project + "/" + r.Environment + "/" + r.Service
		cand := byName[key]
		if cand == nil {
			cand = &statusCandidate{serviceID: r.ServiceID, projectID: r.ProjectID, envID: r.EnvironmentID}
			byName[key] = cand
		}
		if r.CheckID != 0 {
			cand.checks = append(cand.checks, r.CheckID)
			checkSvc[r.CheckID] = r.ServiceID
		}
	}
	type source struct{ service, check *int64 }
	comps := make([][]source, len(req.Components))
	seen := map[string]bool{}
	for i, in := range req.Components {
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return apitypes.StatusPage{}, invalid("component %d has no name; give it the name the public should see", i+1)
		}
		if seen[strings.ToLower(name)] {
			return apitypes.StatusPage{}, invalid("two components are called %q; names on a page are unique", name)
		}
		seen[strings.ToLower(name)] = true
		if len(in.Sources) > statusMaxSources {
			return apitypes.StatusPage{}, invalid("component %q has %d sources; at most %d", name, len(in.Sources), statusMaxSources)
		}
		for _, s := range in.Sources {
			switch {
			case (s.Service == "") == (s.Check == 0):
				return apitypes.StatusPage{}, invalid("a source of %q names a service (project/environment/service) or an uptime check, one of the two", name)
			case s.Service != "":
				cand := byName[s.Service]
				if cand == nil {
					return apitypes.StatusPage{}, notFound("component %q: no live service %s in the organization (preview environments can't be on a status page); name it project/environment/service", name, s.Service)
				}
				id := cand.serviceID
				comps[i] = append(comps[i], source{service: &id})
			default:
				if _, ok := checkSvc[s.Check]; !ok {
					return apitypes.StatusPage{}, notFound("component %q: no uptime check %d on a live service of the organization (preview environments can't be on a status page); `dock uptime list` shows a service's", name, s.Check)
				}
				id := s.Check
				comps[i] = append(comps[i], source{check: &id})
			}
		}
	}
	old, err := c.store.ListStatusComponents(ctx, p.ID)
	if err != nil {
		return apitypes.StatusPage{}, err
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteStatusComponents(ctx, p.ID); err != nil {
			return err
		}
		for i, in := range req.Components {
			auto := in.Auto == nil || *in.Auto
			comp, err := q.CreateStatusComponent(ctx, store.CreateStatusComponentParams{PageID: p.ID, Position: int64(i),
				Grp: strings.TrimSpace(in.Group), Name: strings.TrimSpace(in.Name), Description: strings.TrimSpace(in.Description), Auto: auto})
			if err != nil {
				return err
			}
			for _, s := range comps[i] {
				if err := q.CreateStatusSource(ctx, store.CreateStatusSourceParams{ComponentID: comp.ID, ServiceID: s.service, CheckID: s.check}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return apitypes.StatusPage{}, err
	}
	c.audit(ctx, a, "status_page.components", nil, nil, "status page "+p.Slug, "status page "+p.Slug+": "+componentsDiff(old, req.Components))
	c.statusPagesChanged(ctx)
	return c.statusPage(ctx, a, p)
}

// componentsDiff says how a page's components changed, for the audit log.
func componentsDiff(old []store.StatusComponent, next []apitypes.StatusComponentInput) string {
	was := map[string]bool{}
	for _, c := range old {
		was[c.Name] = true
	}
	var added, kept []string
	for _, c := range next {
		n := strings.TrimSpace(c.Name)
		if was[n] {
			kept = append(kept, n)
			delete(was, n)
		} else {
			added = append(added, n)
		}
	}
	parts := []string{fmt.Sprintf("%d components", len(next))}
	if len(added) > 0 {
		parts = append(parts, "added "+strings.Join(added, ", "))
	}
	if len(was) > 0 {
		var removed []string
		for _, c := range old {
			if was[c.Name] {
				removed = append(removed, c.Name)
			}
		}
		parts = append(parts, "removed "+strings.Join(removed, ", "))
	}
	if len(kept) > 0 && len(added) == 0 && len(was) == 0 {
		parts = append(parts, "changed or reordered")
	}
	return strings.Join(parts, "; ")
}

// ListStatusSourceCandidates are the services and uptime checks a
// component may be built from: the org's live services outside
// previews, those the actor can see. Read scope.
func (c *Core) ListStatusSourceCandidates(ctx context.Context, a Actor) ([]apitypes.StatusSourceCandidate, error) {
	if err := a.require(apitypes.ScopeRead, "reading status pages"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListStatusSourceCandidates(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	out := []apitypes.StatusSourceCandidate{}
	for _, r := range rows {
		if !a.canSee(a.OrgID, r.ProjectID, r.EnvironmentID) {
			continue
		}
		name := r.Project + "/" + r.Environment + "/" + r.Service
		if n := len(out); n == 0 || out[n-1].Service != name {
			out = append(out, apitypes.StatusSourceCandidate{Service: name, Checks: []apitypes.StatusCheckCandidate{}})
		}
		if r.CheckID != 0 {
			last := &out[len(out)-1]
			last.Checks = append(last.Checks, apitypes.StatusCheckCandidate{ID: r.CheckID, URL: r.CheckUrl})
		}
	}
	return out, nil
}

// statusComputed is a component's status and history, worked out for
// the editor and the public page alike.
type statusComputed struct {
	comp    store.StatusComponent
	status  apitypes.ComponentStatus
	sources []apitypes.StatusSource
	uptime  *float64
	days    []apitypes.StatusDay
}

// computeStatus works out a page's components: three queries
// (components, sources, history), the rest from memory. a, when set, is
// whom the sources are shown to; nil for the public page.
func (c *Core) computeStatus(ctx context.Context, p store.StatusPage, a *Actor, at time.Time) ([]statusComputed, error) {
	comps, err := c.store.ListStatusComponents(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	srcs, err := c.store.ListStatusPageSources(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	today := at.Unix() - at.Unix()%uptimeDay
	since := today - (apitypes.StatusDays-1)*uptimeDay
	dayRows, err := c.store.StatusPageDays(ctx, store.StatusPageDaysParams{PageID: p.ID, Since: since,
		DayFrom: c.uptimeDayFrom(at), MinuteFrom: c.uptimeMinuteFrom(at)})
	if err != nil {
		return nil, err
	}
	type sums struct{ up, total int64 }
	days := map[int64]map[int64]sums{}
	for _, r := range dayRows {
		if r.Day < since {
			continue
		}
		if days[r.ComponentID] == nil {
			days[r.ComponentID] = map[int64]sums{}
		}
		d := days[r.ComponentID][r.Day]
		days[r.ComponentID][r.Day] = sums{d.up + r.Up, d.total + r.Total}
	}
	bySrc := map[int64][]store.ListStatusPageSourcesRow{}
	var order []int64
	for _, s := range srcs {
		if _, ok := bySrc[s.SourceID]; !ok {
			order = append(order, s.SourceID)
		}
		bySrc[s.SourceID] = append(bySrc[s.SourceID], s)
	}
	out := make([]statusComputed, len(comps))
	index := make(map[int64]int, len(comps))
	for i, comp := range comps {
		out[i] = statusComputed{comp: comp, sources: []apitypes.StatusSource{}}
		index[comp.ID] = i
	}
	// Per component: its checks (counted once), how many are down, its
	// services' health and whether everything it shows is asleep.
	type tally struct {
		checks, down    int
		seen            map[int64]bool
		worst           apitypes.ComponentStatus
		asleep, sources int
	}
	tallies := make([]tally, len(comps))
	for _, id := range order {
		rows := bySrc[id]
		r := rows[0]
		i, ok := index[r.ComponentID]
		if !ok || r.OrgID != p.OrgID {
			continue
		}
		src := apitypes.StatusSource{}
		if r.SourceCheck != nil {
			src.Check = *r.SourceCheck
		} else {
			src.Service = r.Project + "/" + r.Environment + "/" + r.Service
		}
		t := &tallies[i]
		gone := r.ArchivedAt != nil || r.ProjectArchivedAt != nil || r.Preview
		asleep := !gone && c.isAsleep(r.EnvironmentID)
		switch {
		case r.ArchivedAt != nil || r.ProjectArchivedAt != nil:
			src.Problem = "archived"
		case r.Preview:
			src.Problem = "a preview"
		case asleep:
			src.Problem = "asleep"
		}
		for _, row := range rows {
			if row.CheckID != 0 {
				src.Checks++
			}
		}
		if a != nil && !a.canSee(p.OrgID, r.ProjectID, r.EnvironmentID) {
			src = apitypes.StatusSource{Hidden: true, Checks: src.Checks, Problem: src.Problem}
		}
		out[i].sources = append(out[i].sources, src)
		if gone {
			continue
		}
		t.sources++
		if asleep {
			t.asleep++
			continue
		}
		if t.seen == nil {
			t.seen = map[int64]bool{}
		}
		for _, row := range rows {
			if row.CheckID == 0 || t.seen[row.CheckID] {
				continue
			}
			t.seen[row.CheckID] = true
			t.checks++
			if state, _ := c.checkStatus(row.CheckID); state == apitypes.UptimeDown {
				t.down++
			}
		}
		if r.SourceCheck == nil {
			t.worst = worseStatus(t.worst, c.serviceHealth(r.ServiceID))
		}
	}
	dates := make([]string, apitypes.StatusDays)
	for d := range dates {
		dates[d] = time.Unix(since+int64(d)*uptimeDay, 0).UTC().Format(time.DateOnly)
	}
	for i := range out {
		t := tallies[i]
		st := apitypes.StatusOperational
		switch {
		case !out[i].comp.Auto:
		case t.sources > 0 && t.asleep == t.sources:
			st = apitypes.StatusIdle
		case t.checks > 0 && t.down == t.checks:
			st = apitypes.StatusMajorOutage
		case t.down > 0:
			st = apitypes.StatusPartialOutage
		}
		if out[i].comp.Auto && st != apitypes.StatusIdle {
			st = worseStatus(st, t.worst)
		}
		out[i].status = st
		var up, total int64
		out[i].days = make([]apitypes.StatusDay, apitypes.StatusDays)
		for d := range apitypes.StatusDays {
			day := since + int64(d)*uptimeDay
			sum := days[out[i].comp.ID][day]
			up, total = up+sum.up, total+sum.total
			out[i].days[d] = apitypes.StatusDay{Date: dates[d], Uptime: percent(sum.up, sum.total)}
		}
		out[i].uptime = percent(up, total)
	}
	return out, nil
}

// statusRank orders statuses from good to bad; idle ranks with
// operational for a page's overall status.
var statusRank = map[apitypes.ComponentStatus]int{
	"": 0, apitypes.StatusOperational: 1, apitypes.StatusIdle: 1, apitypes.StatusDegraded: 2,
	apitypes.StatusPartialOutage: 3, apitypes.StatusMajorOutage: 4,
}

// worseStatus is the worse of two statuses, a when they rank the same.
func worseStatus(a, b apitypes.ComponentStatus) apitypes.ComponentStatus {
	if statusRank[b] > statusRank[a] {
		return b
	}
	return a
}

// serviceHealth is what a service's open health alerts say: a crash loop
// is a partial outage, unhealthy is degraded. "" when neither is open.
func (c *Core) serviceHealth(serviceID int64) apitypes.ComponentStatus {
	c.alerts.mu.Lock()
	defer c.alerts.mu.Unlock()
	if _, open := c.alerts.open["crash:"+alertServiceKey(serviceID, "", "", "")]; open {
		return apitypes.StatusPartialOutage
	}
	if _, open := c.alerts.open[healthAlertKey(serviceID)]; open {
		return apitypes.StatusDegraded
	}
	return ""
}

// pageStatus is a page's overall status: its worst component, idle ones
// aside.
func pageStatus(comps []statusComputed) apitypes.ComponentStatus {
	st := apitypes.StatusOperational
	for _, c := range comps {
		if c.status != apitypes.StatusIdle {
			st = worseStatus(st, c.status)
		}
	}
	return st
}

// statusPage is the editor's view of a page.
func (c *Core) statusPage(ctx context.Context, a Actor, p store.StatusPage) (apitypes.StatusPage, error) {
	base, err := c.publicBase(ctx)
	if err != nil {
		return apitypes.StatusPage{}, err
	}
	comps, err := c.computeStatus(ctx, p, &a, now())
	if err != nil {
		return apitypes.StatusPage{}, err
	}
	out := apitypes.StatusPage{StatusPageSummary: c.toStatusSummary(p, base, len(comps)), ShowUptime: p.ShowUptime,
		Status: pageStatus(comps), Components: make([]apitypes.StatusComponent, len(comps))}
	for i, cc := range comps {
		out.Components[i] = apitypes.StatusComponent{Name: cc.comp.Name, Group: cc.comp.Grp, Description: cc.comp.Description,
			Auto: cc.comp.Auto, Status: cc.status, Sources: cc.sources, Uptime: cc.uptime, Days: cc.days}
	}
	return out, nil
}

// PublicStatusPage is what the published page with ID id shows anyone:
// public names, statuses and percentages, nothing else. CodeNotFound
// once it isn't published (any more). Anyone may ask; api.StatusPages
// caches the answer.
func (c *Core) PublicStatusPage(ctx context.Context, id int64) (apitypes.PublicStatusPage, error) {
	p, err := c.store.GetStatusPageByID(ctx, id)
	if store.IsNotFound(err) || (err == nil && !p.Published) {
		return apitypes.PublicStatusPage{}, notFound("no status page here")
	}
	if err != nil {
		return apitypes.PublicStatusPage{}, err
	}
	t := now()
	comps, err := c.computeStatus(ctx, p, nil, t)
	if err != nil {
		return apitypes.PublicStatusPage{}, err
	}
	out := apitypes.PublicStatusPage{Title: p.Title, Status: pageStatus(comps), ShowUptime: p.ShowUptime, GeneratedAt: t}
	for _, cc := range comps {
		if n := len(out.Groups); n == 0 || out.Groups[n-1].Name != cc.comp.Grp {
			out.Groups = append(out.Groups, apitypes.PublicStatusGroup{Name: cc.comp.Grp})
		}
		pc := apitypes.PublicStatusComponent{Name: cc.comp.Name, Description: cc.comp.Description, Status: cc.status, Days: cc.days}
		if p.ShowUptime {
			pc.Uptime = cc.uptime
		} else {
			pc.Days = make([]apitypes.StatusDay, len(cc.days))
			for i, d := range cc.days {
				// The colour of a day stays; its number goes.
				pc.Days[i] = apitypes.StatusDay{Date: d.Date, Uptime: roundedDay(d.Uptime)}
			}
		}
		g := &out.Groups[len(out.Groups)-1]
		g.Components = append(g.Components, pc)
	}
	return out, nil
}

// roundedDay is a day's uptime reduced to the band its colour shows
// (100, 99 or 0), for a page that hides percentages.
func roundedDay(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := 0.0
	switch {
	case *p >= 100:
		v = 100
	case *p >= 99:
		v = 99
	}
	return &v
}
