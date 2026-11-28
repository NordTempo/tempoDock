package core

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Public status pages (ADR 0073). An organization has up to
// statusMaxPages pages; each lists components with public names, built
// from services (their uptime checks and health) and single uptime
// checks. A page's name is unique in its organization; a published page
// is served at status-<slug>-<random>.<base domain> (ADR 0087; pages from
// before have no random part until renamed) by the control plane's
// Caddy, through [status] listen (api.StatusPages), which renders it
// when it is viewed after a change and serves the cached bytes
// otherwise. A renamed page's old generated addresses answer 301 to its
// address (StatusPageMoved) until another of the organization's pages
// takes the name.
//
// A component's status is worked out from memory: the checks' states
// (checkStatus), the open crash loop and unhealthy alerts, and whether
// the environment is asleep. Its history is the checks' rows in
// uptime_stats, whose day rows cover 90 days.

const (
	statusMaxPages      = 5
	statusMaxComponents = 100
	statusMaxSources    = 10
	// statusMaxHooks bounds the posts one update fans out to.
	statusMaxHooks = 10
	// statusOldSlugs is how many old names a page keeps: each is a route
	// and a certificate, so a page renamed over and over holds a few.
	statusOldSlugs = 5
)

// statusState is what Dock holds in memory for status pages: which
// hostname is which page.
type statusState struct {
	hosts atomic.Pointer[map[string]statusHost]
	// syncing claims syncStatusRecords' passes, which run one at a
	// time so one never deletes what a later one added; rerun asks the
	// pass holding it for another. Flags, not a mutex: passes write to
	// the store.
	syncing, rerun atomic.Bool
	// loadMu makes reading the pages and publishing hosts one step, so
	// a map read earlier is never published over a newer one. Held over
	// a read only, no store write.
	loadMu sync.Mutex
}

// statusHost is a page a hostname belongs to. moved is set for an old
// name's address: the page's address it redirects to.
type statusHost struct {
	id        int64
	published bool
	moved     string
}

// statusHostname is a page's address under the control plane's base
// domain: status-<slug>-<key>, or status-<slug> for a page from before
// keys (key ""); "" without a base domain.
func statusHostname(slug, key, base string) string {
	switch {
	case base == "":
		return ""
	case key == "":
		return "status-" + slug + "." + base
	}
	return hostLabel("status-"+slug, key) + "." + base
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
	return h.id, c.bus.statusGen.Load(), ok && h.published && h.moved == "" && c.cfg.Status.Listen != ""
}

// StatusPageMoved is where a published page's old address redirects, for
// the public listener: the page's address now.
func (c *Core) StatusPageMoved(host string) (string, bool) {
	h, ok := c.statusPageHost(host)
	return h.moved, ok && h.published && h.moved != "" && c.cfg.Status.Listen != ""
}

// statusPagesChanged rebuilds the pages' hostnames and, when the
// published ones changed, routes them on the control plane's server. It
// also makes every rendered page stale.
func (c *Core) statusPagesChanged(ctx context.Context) {
	c.bus.statusGen.Add(1)
	c.statusPages.loadMu.Lock()
	hosts, err := c.loadStatusHosts(ctx)
	if err != nil {
		c.statusPages.loadMu.Unlock()
		c.log.Error("status pages: read the pages; their addresses stay as they were", "err", err)
		return
	}
	old := c.statusPages.hosts.Swap(&hosts)
	c.statusPages.loadMu.Unlock()
	c.syncStatusRecords()
	if old != nil && maps.Equal(*old, hosts) {
		return
	}
	if err := c.applyRoutes(ctx, c.localID); err != nil {
		c.log.Error("status pages: route the pages", "err", err)
	}
}

// loadStatusHosts reads every page's hostnames, its old names'
// addresses too: none without a base domain.
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
	olds, err := c.store.ListStatusOldSlugs(ctx)
	if err != nil {
		return nil, err
	}
	at := make(map[int64]statusHost, len(rows))
	for _, r := range rows {
		h := statusHost{id: r.ID, published: r.Published}
		own := statusHostname(r.Slug, r.AddressKey, base)
		hosts[own] = h
		h.moved = c.controlURL(own)
		if r.Hostname != "" {
			hosts[r.Hostname] = statusHost{id: r.ID, published: r.Published}
			h.moved = c.controlURL(r.Hostname)
		}
		at[r.ID] = h
	}
	for _, o := range olds {
		page, ok := at[o.PageID]
		if h := statusHostname(o.Slug, o.AddressKey, base); ok && hosts[h].id == 0 {
			hosts[h] = page
		}
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
		err = a.requireUnlimited(need, action) // as statusPagesIn
	} else {
		err = a.require(need, action)
	}
	if err != nil {
		return store.StatusPage{}, err
	}
	p, err := c.store.GetStatusPage(ctx, store.GetStatusPageParams{OrgID: a.OrgID, Slug: slug})
	if store.IsNotFound(err) {
		// A name the page had before keeps naming it, for links and scripts.
		p, err = c.store.GetStatusPageByOldSlug(ctx, store.GetStatusPageByOldSlugParams{OrgID: a.OrgID, Slug: slug})
	}
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
	return apitypes.StatusPageSummary{Slug: p.Slug, Title: p.Title, Published: p.Published, Components: components,
		URL: c.statusAddress(p, base), CreatedAt: p.CreatedAt, CreatedBy: p.CreatedBy}
}

// statusAddress is where a page is served: its custom hostname once
// verified, otherwise its generated address under base, "" with neither.
// Lists, the editor's Open page, incident links and update hooks all use
// it, so they point where the organization chose.
func (c *Core) statusAddress(p store.StatusPage, base string) string {
	if p.Hostname != nil && p.VerifiedAt != nil {
		return c.controlURL(*p.Hostname)
	}
	if h := statusHostname(p.Slug, p.AddressKey, base); h != "" {
		return c.controlURL(h)
	}
	return ""
}

// CreateStatusPage makes an unpublished page. Organization admins.
func (c *Core) CreateStatusPage(ctx context.Context, a Actor, req apitypes.CreateStatusPageRequest) (apitypes.StatusPage, error) {
	if err := statusPagesIn(a, "making a status page"); err != nil {
		return apitypes.StatusPage{}, err
	}
	key, err := c.newStatusKey(ctx, 0, req.Slug, "")
	if err != nil {
		return apitypes.StatusPage{}, err
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = req.Slug
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
		if err := takeStatusSlug(ctx, q, a.OrgID, req.Slug); err != nil {
			return err
		}
		p, err = q.CreateStatusPage(ctx, store.CreateStatusPageParams{OrgID: a.OrgID, Slug: req.Slug, AddressKey: key, Title: title, CreatedAt: now(), CreatedBy: a.Name})
		return err
	})
	if err != nil {
		return apitypes.StatusPage{}, err
	}
	c.audit(ctx, a, "status_page.create", nil, nil, "status page "+p.Slug, "made status page "+p.Slug)
	c.statusPagesChanged(ctx)
	return c.statusPage(ctx, a, p)
}

// newStatusKey is the random part of the address page (0 for a new one)
// gets as slug: keep when it is free (a rename keeps the page's, and a
// page renamed back takes its old address again), otherwise a new one
// whose address nothing else answers on (checkStatusHost) and no other
// page holds. slug must be a valid name.
func (c *Core) newStatusKey(ctx context.Context, page int64, slug, keep string) (string, error) {
	if err := validName("status page", slug); err != nil {
		return "", err
	}
	base, err := c.publicBase(ctx)
	if err != nil {
		return "", err
	}
	for i := range hostKeyTries {
		key := keep
		if i > 0 || key == "" {
			key = newHostKey()
		}
		host := statusHostname(slug, key, base)
		if host == "" {
			return key, nil // no address until there is a base domain
		}
		if err := c.checkStatusHost(ctx, host); CodeOf(err) == apitypes.CodeHostnameTaken {
			continue
		} else if err != nil {
			return "", err
		}
		if h, taken := c.statusPageHost(host); !taken || h.id == page {
			return key, nil
		}
	}
	return "", conflict("no free status page address after %d tries; try again", hostKeyTries)
}

// takeStatusSlug claims slug for one of org's pages in q's transaction:
// refused when the organization has a page called that, and no longer an
// old name of its pages, whose old address then stops redirecting.
func takeStatusSlug(ctx context.Context, q *store.Queries, org int64, slug string) error {
	if taken, err := q.StatusSlugTaken(ctx, store.StatusSlugTakenParams{OrgID: org, Slug: slug}); err != nil {
		return err
	} else if taken {
		return conflict("the organization has a status page called %s; pick another name", slug)
	}
	return q.ReleaseStatusOldSlug(ctx, store.ReleaseStatusOldSlugParams{OrgID: org, Slug: slug})
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

// UpdateStatusPage renames a page, changes its title and settings, and
// publishes or unpublishes it. Organization admins.
func (c *Core) UpdateStatusPage(ctx context.Context, a Actor, slug string, req apitypes.UpdateStatusPageRequest) (apitypes.StatusPage, error) {
	p, err := c.loadStatusPage(ctx, a, slug, apitypes.ScopeAdmin, "changing a status page")
	if err != nil {
		return apitypes.StatusPage{}, err
	}
	if req.Slug != nil && *req.Slug != p.Slug {
		if p, err = c.renameStatusPage(ctx, a, p, *req.Slug); err != nil {
			return apitypes.StatusPage{}, err
		}
	}
	next := store.UpdateStatusPageParams{ID: p.ID, Title: p.Title, Published: p.Published, ShowUptime: p.ShowUptime, Drafts: p.Drafts}
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
	if req.Drafts != nil && *req.Drafts != p.Drafts {
		next.Drafts = *req.Drafts
		changes = append(changes, map[bool]string{true: "alerts draft incidents", false: "alerts draft no incidents"}[next.Drafts])
	}
	if req.Hostname != nil {
		what, err := c.setStatusHostname(ctx, a, p, *req.Hostname)
		if err != nil {
			return apitypes.StatusPage{}, err
		}
		if what != "" {
			changes = append(changes, what)
			if p, err = c.store.GetStatusPageByID(ctx, p.ID); err != nil {
				return apitypes.StatusPage{}, err
			}
			next.Title, next.Published, next.ShowUptime, next.Drafts = p.Title, p.Published, p.ShowUptime, p.Drafts
			if req.Title != nil {
				next.Title = strings.TrimSpace(*req.Title)
			}
			if req.ShowUptime != nil {
				next.ShowUptime = *req.ShowUptime
			}
			if req.Drafts != nil {
				next.Drafts = *req.Drafts
			}
			// The TXT record may be there already.
			if _, _, err := c.proveStatusHostname(ctx, p); err != nil {
				return apitypes.StatusPage{}, err
			}
		}
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
			if err := c.checkStatusHost(ctx, statusHostname(p.Slug, p.AddressKey, base)); err != nil {
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

// renameStatusPage gives a page a new name, checked like a new page's,
// and so a new generated address. The old name keeps naming the page in
// the API and its address redirects to the page's (ADR 0073, renaming),
// until another page takes it; a page keeps its statusOldSlugs newest.
// The custom hostname, components, incidents and hooks stay.
func (c *Core) renameStatusPage(ctx context.Context, a Actor, p store.StatusPage, slug string) (store.StatusPage, error) {
	// The page keeps its random part; one from before keys gets one.
	key, err := c.newStatusKey(ctx, p.ID, slug, p.AddressKey)
	if err != nil {
		return p, err
	}
	old, oldKey := p.Slug, p.AddressKey
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := takeStatusSlug(ctx, q, p.OrgID, slug); err != nil {
			return err
		}
		var err error
		if p, err = q.SetStatusPageSlug(ctx, store.SetStatusPageSlugParams{Slug: slug, AddressKey: key, ID: p.ID}); err != nil {
			return err
		}
		if err := q.AddStatusOldSlug(ctx, store.AddStatusOldSlugParams{OrgID: p.OrgID, Slug: old, PageID: p.ID, AddressKey: oldKey, ChangedAt: now()}); err != nil {
			return err
		}
		return q.TrimStatusOldSlugs(ctx, store.TrimStatusOldSlugsParams{PageID: p.ID, Keep: statusOldSlugs})
	})
	if store.IsUnique(err) {
		return p, conflict("the organization has a status page called %s; pick another name", slug)
	}
	if err != nil {
		return p, err
	}
	summary := fmt.Sprintf("renamed status page %s to %s", old, slug)
	if base, err := c.publicBase(ctx); err == nil && base != "" {
		summary += fmt.Sprintf("; %s redirects to its new address", statusHostname(old, oldKey, base))
	}
	c.audit(ctx, a, "status_page.rename", nil, nil, "status page "+slug, summary)
	c.statusPagesChanged(ctx)
	return p, nil
}

// DeleteStatusPage removes a page and its components; its address
// answers 404 at once. Organization admins.
func (c *Core) DeleteStatusPage(ctx context.Context, a Actor, slug string) error {
	p, err := c.loadStatusPage(ctx, a, slug, apitypes.ScopeAdmin, "removing a status page")
	if err != nil {
		return err
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.SetStatusPageRecordAbsent(ctx, store.SetStatusPageRecordAbsentParams{UpdatedAt: now(), StatusPageID: &p.ID}); err != nil {
			return err
		}
		return q.DeleteStatusPage(ctx, p.ID)
	})
	if err != nil {
		return err
	}
	c.kickCloudflare() // its records go, its own address's too
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
	// An organization's outside checks may be sources too (service 0).
	outside, err := c.store.ListOutsideUptimeChecks(ctx, a.OrgID)
	if err != nil {
		return apitypes.StatusPage{}, err
	}
	for _, r := range outside {
		checkSvc[r.ID] = 0
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
					return apitypes.StatusPage{}, notFound("component %q: no uptime check %d on a live service of the organization or outside Dock (preview environments can't be on a status page); `dock uptime list` shows a service's, `dock uptime list --outside` the organization's", name, s.Check)
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
	keep, err := keptComponents(old, req.Components, p.Slug)
	if err != nil {
		return apitypes.StatusPage{}, err
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		for _, o := range old {
			if !slices.Contains(keep, o.ID) {
				if err := q.DeleteStatusComponent(ctx, o.ID); err != nil {
					return err
				}
			}
		}
		for i, in := range req.Components {
			auto := in.Auto == nil || *in.Auto
			grp, name, desc := strings.TrimSpace(in.Group), strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
			id := keep[i]
			if id != 0 {
				if err := q.UpdateStatusComponent(ctx, store.UpdateStatusComponentParams{ID: id, Position: int64(i),
					Grp: grp, Name: name, Description: desc, Auto: auto}); err != nil {
					return err
				}
				if err := q.DeleteStatusSources(ctx, id); err != nil {
					return err
				}
			} else {
				comp, err := q.CreateStatusComponent(ctx, store.CreateStatusComponentParams{PageID: p.ID, Position: int64(i),
					Grp: grp, Name: name, Description: desc, Auto: auto})
				if err != nil {
					return err
				}
				id = comp.ID
			}
			for _, s := range comps[i] {
				if err := q.CreateStatusSource(ctx, store.CreateStatusSourceParams{ComponentID: id, ServiceID: s.service, CheckID: s.check}); err != nil {
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

// keptComponents is, for each component of a page's new list, the
// existing component it keeps (0 for a new one): the one its ID names,
// else the one of its name. Kept components keep the incidents that
// name them.
func keptComponents(old []store.StatusComponent, next []apitypes.StatusComponentInput, slug string) ([]int64, error) {
	byID := make(map[int64]bool, len(old))
	byName := make(map[string]int64, len(old))
	for _, o := range old {
		byID[o.ID] = true
		byName[strings.ToLower(o.Name)] = o.ID
	}
	keep := make([]int64, len(next))
	used := map[int64]bool{}
	for i, in := range next {
		id := in.ID
		switch {
		case id != 0 && !byID[id]:
			return nil, notFound("status page %s has no component %d; leave the id out to add one", slug, id)
		case id == 0:
			if n, ok := byName[strings.ToLower(strings.TrimSpace(in.Name))]; ok && !used[n] {
				id = n
			}
		}
		if id != 0 && used[id] {
			return nil, invalid("component %d is listed twice", id)
		}
		used[id] = id != 0
		keep[i] = id
	}
	return keep, nil
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

// statusSince is the first second of the days a page shows at at.
func statusSince(at time.Time) int64 {
	today := at.Unix() - at.Unix()%uptimeDay
	return today - (apitypes.StatusDays-1)*uptimeDay
}

// computeStatus works out a page's components: four queries
// (components, sources, history, outages), the rest from memory, and the
// incidents' impact (incs, from loadStatusIncidents since statusSince).
// a, when set, is whom the sources are shown to; nil for the public
// page.
func (c *Core) computeStatus(ctx context.Context, p store.StatusPage, a *Actor, at time.Time, incs []statusIncident) ([]statusComputed, error) {
	comps, err := c.store.ListStatusComponents(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	srcs, err := c.store.ListStatusPageSources(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	since := statusSince(at)
	dayRows, err := c.store.StatusPageDays(ctx, store.StatusPageDaysParams{PageID: p.ID, Since: since,
		DayFrom: c.uptimeDayFrom(at), MinuteFrom: c.uptimeMinuteFrom(at)})
	if err != nil {
		return nil, err
	}
	// A day with a confirmed outage (an uptime_down alert, two failed
	// probes in a row) is told from one with a failed probe by the
	// alerts' history, not the stats: hour and day rows don't keep the
	// probes' order.
	outages, err := c.store.StatusPageOutages(ctx, store.StatusPageOutagesParams{PageID: p.ID, Since: ptr(time.Unix(since, 0))})
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
		// An outside check (no project) is the organization's: anyone
		// who reads the page sees it.
		if a != nil && r.ProjectID != 0 && !a.canSee(p.OrgID, r.ProjectID, r.EnvironmentID) {
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
	for _, o := range outages {
		i, ok := index[o.ComponentID]
		if !ok {
			continue
		}
		markOutage(out[i].days, since, o.OpenedAt, o.ResolvedAt, at)
	}
	applyIncidents(out, incs, since, at)
	return out, nil
}

// markOutage marks the days of days (since its first) that the outage
// from opened to resolved (nil: still down at at) overlaps.
func markOutage(days []apitypes.StatusDay, since int64, opened time.Time, resolved *time.Time, at time.Time) {
	end := at
	if resolved != nil {
		end = *resolved
	}
	first := max(int((opened.Unix()-since)/uptimeDay), 0)
	last := min(int((end.Unix()-since)/uptimeDay), len(days)-1)
	for d := first; d <= last; d++ {
		days[d].Outage = true
	}
}

// statusRank orders statuses from good to bad; idle ranks with
// operational for a page's overall status.
var statusRank = map[apitypes.ComponentStatus]int{
	"": 0, apitypes.StatusOperational: 1, apitypes.StatusIdle: 1, apitypes.StatusMaintenance: 2, apitypes.StatusDegraded: 3,
	apitypes.StatusPartialOutage: 4, apitypes.StatusMajorOutage: 5,
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
	t := now()
	incs, err := c.loadStatusIncidents(ctx, p.ID, time.Unix(statusSince(t), 0))
	if err != nil {
		return apitypes.StatusPage{}, err
	}
	comps, err := c.computeStatus(ctx, p, &a, t, incs)
	if err != nil {
		return apitypes.StatusPage{}, err
	}
	out := apitypes.StatusPage{StatusPageSummary: c.toStatusSummary(p, base, len(comps)), BaseDomain: base, Address: statusHostname(p.Slug, p.AddressKey, base), ShowUptime: p.ShowUptime, Drafts: p.Drafts,
		Status: pageStatus(comps), Components: make([]apitypes.StatusComponent, len(comps)), Incidents: make([]apitypes.StatusIncident, len(incs))}
	if out.Hostname, err = c.statusHostnameView(ctx, p, base); err != nil {
		return apitypes.StatusPage{}, err
	}
	url := ""
	if p.Published {
		url = out.URL
	}
	for i, inc := range incs {
		out.Incidents[i] = toStatusIncident(a, inc, p.Slug, url)
	}
	for i, cc := range comps {
		out.Components[i] = apitypes.StatusComponent{ID: cc.comp.ID, Name: cc.comp.Name, Group: cc.comp.Grp, Description: cc.comp.Description,
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
	incs, err := c.loadStatusIncidents(ctx, p.ID, time.Unix(statusSince(t), 0))
	if err != nil {
		return apitypes.PublicStatusPage{}, err
	}
	comps, err := c.computeStatus(ctx, p, nil, t, incs)
	if err != nil {
		return apitypes.PublicStatusPage{}, err
	}
	out := apitypes.PublicStatusPage{Title: p.Title, Status: pageStatus(comps), ShowUptime: p.ShowUptime, GeneratedAt: t}
	recent := t.AddDate(0, 0, -apitypes.StatusRecentDays)
	for _, inc := range incs {
		maint := inc.row.Kind == string(apitypes.KindMaintenance)
		switch {
		case !inc.published() || inc.row.State == string(apitypes.MaintenanceCancelled):
		case maint && inc.row.ResolvedAt == nil:
			out.Maintenance = append(out.Maintenance, toPublicIncident(inc))
		case inc.row.ResolvedAt == nil:
			out.Open = append(out.Open, toPublicIncident(inc))
		case inc.row.ResolvedAt.After(recent):
			out.Recent = append(out.Recent, toPublicIncident(inc))
		}
	}
	// Windows soonest first.
	slices.Reverse(out.Maintenance)
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
				pc.Days[i] = apitypes.StatusDay{Date: d.Date, Uptime: roundedDay(d.Uptime), Impact: d.Impact, Incident: d.Incident, Maintenance: d.Maintenance, Outage: d.Outage}
			}
		}
		g := &out.Groups[len(out.Groups)-1]
		g.Components = append(g.Components, pc)
	}
	return out, nil
}

// roundedDay is a day's uptime reduced to what its colour shows, for a
// page that hides percentages: 100 when every probe passed, else 99 (a
// confirmed outage is the day's Outage, not its number).
func roundedDay(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := 99.0
	if *p >= 100 {
		v = 100
	}
	return &v
}
