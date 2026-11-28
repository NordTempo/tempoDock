package core

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Incidents on status pages (ADR 0073, phase 2): what people say about
// an outage, update by update, and what it does to each component. An
// open published incident makes its components read at least its
// impact, and the days it ran are coloured on their bars. Drafts are
// never public: Dock opens one when an uptime_down or crash loop alert
// opens about a component's sources (statusAlertOpened), and drops it if
// the alert resolves before anyone publishes it.
//
// People who deploy are on call, so opening and updating incidents takes
// deploy scope; a token limited to a project or environment may name
// only components built entirely from it. Deleting one is for the
// page's admins.

const (
	statusMaxTitle   = 120
	statusMaxMessage = 5000
	// statusHistoryMax bounds a page's /history; statusFeedMax its feed.
	statusHistoryMax = 500
	statusFeedMax    = 50
	// statusDraftBy is who Dock's own drafts are by.
	statusDraftBy = "Dock"
)

// statusIncident is an incident with what it names and says.
type statusIncident struct {
	row store.StatusIncident
	// alert is the alert a draft came from, alertProject its project (0:
	// none), which decides who may see it (toStatusIncident).
	alert        *apitypes.IncidentAlert
	alertProject int64
	impacts      []statusImpact
	updates      []store.StatusUpdate
}

// statusImpact is what an incident does to one component.
type statusImpact struct {
	component int64
	name      string
	impact    apitypes.ComponentStatus
}

// published says whether the incident is public.
func (i statusIncident) published() bool { return i.row.State != string(apitypes.IncidentDraft) }

// newPublicID is an incident's public ID: 10 random lowercase base32
// characters, which count nothing.
func newPublicID() string { return strings.ToLower(rand.Text()[:10]) }

// ValidPublicID says whether s could be an incident's public ID, so the
// public listener answers garbage without a query.
func ValidPublicID(s string) bool {
	if len(s) != 10 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '2' || r > '7') {
			return false
		}
	}
	return true
}

// loadStatusIncidents reads a page's incidents open or resolved since
// since, newest first: three queries.
func (c *Core) loadStatusIncidents(ctx context.Context, pageID int64, since time.Time) ([]statusIncident, error) {
	rows, err := c.store.ListStatusIncidents(ctx, store.ListStatusIncidentsParams{PageID: pageID, Since: &since})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	impacts, err := c.store.ListStatusImpacts(ctx, store.ListStatusImpactsParams{PageID: pageID, Since: &since})
	if err != nil {
		return nil, err
	}
	updates, err := c.store.ListStatusUpdates(ctx, store.ListStatusUpdatesParams{PageID: pageID, Since: &since})
	if err != nil {
		return nil, err
	}
	out := make([]statusIncident, len(rows))
	index := make(map[int64]int, len(rows))
	for i, r := range rows {
		out[i] = statusIncident{row: r.StatusIncident}
		if r.AlertKind != "" {
			out[i].alert = &apitypes.IncidentAlert{Kind: apitypes.AlertKind(r.AlertKind), Title: r.AlertTitle}
			if r.AlertProjectID != nil {
				out[i].alertProject = *r.AlertProjectID
			}
			if r.AlertService != "" {
				out[i].alert.Service = serviceKey(r.AlertProject, r.AlertEnvironment, r.AlertService)
			}
		}
		index[r.StatusIncident.ID] = i
	}
	for _, im := range impacts {
		if i, ok := index[im.IncidentID]; ok {
			out[i].impacts = append(out[i].impacts, statusImpact{im.ComponentID, im.Name, apitypes.ComponentStatus(im.Impact)})
		}
	}
	for _, u := range updates {
		if i, ok := index[u.IncidentID]; ok {
			out[i].updates = append(out[i].updates, u)
		}
	}
	return out, nil
}

// loadIncident reads one incident with what it names and says.
func (c *Core) loadIncident(ctx context.Context, row store.StatusIncident) (statusIncident, error) {
	out := statusIncident{row: row}
	impacts, err := c.store.ListIncidentImpacts(ctx, row.ID)
	if err != nil {
		return out, err
	}
	for _, im := range impacts {
		out.impacts = append(out.impacts, statusImpact{im.ComponentID, im.Name, apitypes.ComponentStatus(im.Impact)})
	}
	if out.updates, err = c.store.ListIncidentUpdates(ctx, row.ID); err != nil {
		return out, err
	}
	if row.AlertID != nil {
		switch a, err := c.store.GetAlert(ctx, *row.AlertID); {
		case err == nil:
			out.alert = &apitypes.IncidentAlert{Kind: apitypes.AlertKind(a.Kind), Title: a.Title}
			out.alertProject = alertProjectID(a)
			if a.Service != "" {
				out.alert.Service = serviceKey(a.Project, a.Environment, a.Service)
			}
		case !store.IsNotFound(err):
			return out, err
		}
	}
	return out, nil
}

// applyIncidents adds a page's published incidents to its components:
// an open one makes each component it names read at least its impact,
// and every one colours the days it ran.
func applyIncidents(out []statusComputed, incs []statusIncident, since int64, at time.Time) {
	index := make(map[int64]int, len(out))
	for i, cc := range out {
		index[cc.comp.ID] = i
	}
	var maint []statusIncident
	for _, inc := range incs {
		if inc.row.Kind == string(apitypes.KindMaintenance) {
			maint = append(maint, inc)
			continue
		}
		if !inc.published() {
			continue
		}
		open := inc.row.ResolvedAt == nil
		end := at.Unix()
		if !open {
			end = inc.row.ResolvedAt.Unix()
		}
		if end < since {
			continue
		}
		first := max(0, (inc.row.StartsAt.Unix()-since)/uptimeDay)
		last := min(apitypes.StatusDays-1, (end-since)/uptimeDay)
		for _, im := range inc.impacts {
			i, ok := index[im.component]
			if !ok {
				continue
			}
			if open {
				out[i].status = worseStatus(out[i].status, im.impact)
			}
			for d := first; d <= last; d++ {
				day := &out[i].days[d]
				if statusRank[im.impact] > statusRank[day.Impact] {
					day.Impact, day.Incident = im.impact, inc.row.Title
				}
			}
		}
	}
	// A window in progress shows on its components whatever else they
	// say; its days, and a completed one's, are marked.
	for _, w := range maint {
		var end int64
		switch apitypes.IncidentState(w.row.State) {
		case apitypes.MaintenanceInProgress:
			end = at.Unix()
		case apitypes.MaintenanceCompleted:
			end = w.row.ResolvedAt.Unix()
		default:
			continue
		}
		if end < since {
			continue
		}
		first := max(0, (w.row.StartsAt.Unix()-since)/uptimeDay)
		last := min(apitypes.StatusDays-1, (end-since)/uptimeDay)
		for _, im := range w.impacts {
			i, ok := index[im.component]
			if !ok {
				continue
			}
			if w.row.State == string(apitypes.MaintenanceInProgress) {
				out[i].status = apitypes.StatusMaintenance
			}
			for d := first; d <= last; d++ {
				out[i].days[d].Maintenance = true
			}
		}
	}
}

// toStatusIncident is an incident as a shows it in the editor; url is its
// public page, "" when it has none. The alert a draft came from is left
// out for a token that can't see the alert's project, as alert lists do.
func toStatusIncident(a Actor, inc statusIncident, slug, url string) apitypes.StatusIncident {
	r := inc.row
	out := apitypes.StatusIncident{ID: r.PublicID, Page: slug, Kind: apitypes.IncidentKind(r.Kind), EndsAt: r.EndsAt, Title: r.Title, State: apitypes.IncidentState(r.State),
		Components: toImpacts(inc.impacts), Draft: r.Draft, StartedAt: r.StartsAt, PublishedAt: r.PublishedAt,
		ResolvedAt: r.ResolvedAt, CreatedBy: r.CreatedBy, Alert: inc.alert, Updates: make([]apitypes.IncidentUpdate, len(inc.updates))}
	if inc.alert != nil && !a.canSee(a.OrgID, inc.alertProject, 0) {
		out.Alert = nil
	}
	if inc.published() && url != "" {
		out.URL = url + "/incidents/" + r.PublicID
	}
	for i, u := range inc.updates {
		out.Updates[i] = apitypes.IncidentUpdate{ID: u.ID, State: apitypes.IncidentState(u.State), Message: u.Body, At: u.At, By: u.CreatedBy, EditedAt: u.EditedAt}
	}
	return out
}

// toImpacts is what an incident does, by component name.
func toImpacts(ims []statusImpact) []apitypes.StatusImpact {
	out := make([]apitypes.StatusImpact, len(ims))
	for i, im := range ims {
		out[i] = apitypes.StatusImpact{Component: im.name, Impact: im.impact}
	}
	return out
}

// toPublicIncident is an incident as anyone sees it: its words and the
// components' public names, updates newest first; nothing about who
// wrote it or which alert drafted it.
func toPublicIncident(inc statusIncident) apitypes.PublicIncident {
	r := inc.row
	out := apitypes.PublicIncident{ID: r.PublicID, Title: r.Title, Kind: apitypes.IncidentKind(r.Kind), EndsAt: r.EndsAt, State: apitypes.IncidentState(r.State),
		Components: toImpacts(inc.impacts), StartedAt: r.StartsAt, ResolvedAt: r.ResolvedAt,
		Updates: make([]apitypes.PublicUpdate, len(inc.updates))}
	for i, u := range inc.updates {
		out.Updates[len(inc.updates)-1-i] = apitypes.PublicUpdate{State: apitypes.IncidentState(u.State), Message: u.Body, At: u.At, Edited: u.EditedAt != nil}
	}
	return out
}

// incidentsIn checks the actor may open and update incidents: deploy
// scope, since the people who deploy are on call. An actor that asks
// first is refused, so an agent that can't post opens a draft.
func incidentsIn(a Actor, action string) error {
	return a.require(apitypes.ScopeDeploy, action)
}

// loadIncidentFor is the org's incident with public ID id, for an actor
// with need, and its page.
func (c *Core) loadIncidentFor(ctx context.Context, a Actor, id string, need apitypes.Scope, action string) (store.StatusIncident, store.StatusPage, error) {
	// Admin as statusPagesIn asks, deploy as incidentsIn; the scope is
	// passed on so each check names its own permission.
	var err error
	if need == apitypes.ScopeAdmin {
		err = a.requireUnlimited(need, action)
	} else {
		err = a.require(need, action)
	}
	if err != nil {
		return store.StatusIncident{}, store.StatusPage{}, err
	}
	row, err := c.store.GetStatusIncident(ctx, strings.ToLower(id))
	if store.IsNotFound(err) || (err == nil && row.StatusPage.OrgID != a.OrgID) {
		return store.StatusIncident{}, store.StatusPage{}, notFound("incident %s not found; `dock incident list` shows the organization's", id)
	}
	if err != nil {
		return store.StatusIncident{}, store.StatusPage{}, err
	}
	return row.StatusIncident, row.StatusPage, nil
}

// resolveImpacts finds the components a request names on a page, with
// the impact on each, and checks the actor may speak for them.
func (c *Core) resolveImpacts(ctx context.Context, a Actor, p store.StatusPage, in []apitypes.StatusImpact, kind apitypes.IncidentKind) ([]statusImpact, error) {
	if len(in) == 0 {
		return nil, c.checkIncidentScope(ctx, a, p, nil)
	}
	comps, err := c.store.ListStatusComponents(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]store.StatusComponent, len(comps))
	for _, comp := range comps {
		byName[strings.ToLower(comp.Name)] = comp
	}
	out := make([]statusImpact, 0, len(in))
	seen := map[int64]bool{}
	for _, im := range in {
		comp, ok := byName[strings.ToLower(strings.TrimSpace(im.Component))]
		if !ok {
			return nil, notFound("status page %s has no component %q; `dock status-page show %s` lists them", p.Slug, im.Component, p.Slug)
		}
		switch {
		case kind == apitypes.KindMaintenance:
			// A window's components are under maintenance, whatever was asked.
			im.Impact = apitypes.StatusMaintenance
		case im.Impact == apitypes.StatusDegraded || im.Impact == apitypes.StatusPartialOutage || im.Impact == apitypes.StatusMajorOutage:
		default:
			return nil, invalid("%q isn't an impact; use degraded, partial_outage or major_outage", im.Impact)
		}
		if seen[comp.ID] {
			return nil, invalid("component %q is named twice", comp.Name)
		}
		seen[comp.ID] = true
		out = append(out, statusImpact{comp.ID, comp.Name, im.Impact})
	}
	return out, c.checkIncidentScope(ctx, a, p, out)
}

// checkIncidentScope lets a token limited to a project or environment
// speak only for components built entirely from what it can see: none
// that is manual, and never for the whole page.
func (c *Core) checkIncidentScope(ctx context.Context, a Actor, p store.StatusPage, ims []statusImpact) error {
	// A person whose grants post on less than the whole org is limited
	// like a token (ADR 0080): to what they post for.
	granted := a.hasGrants && !a.covers(1<<PermStatusPagesPost, 0, 0, uint8(allKinds))
	if !a.limited() && !granted {
		return nil
	}
	if len(ims) == 0 {
		return errf(apitypes.CodeForbidden, "%s posts for only part of the organization; an incident that names no component speaks for the whole page, which needs Status pages post on the whole organization", a)
	}
	srcs, err := c.store.ListStatusPageSources(ctx, p.ID)
	if err != nil {
		return err
	}
	kinds, err := c.envKinds(ctx, a)
	if err != nil {
		return err
	}
	ok := map[int64]bool{}
	for _, s := range srcs {
		v, seen := ok[s.ComponentID]
		// An outside check (project 0) is the organization's: only those
		// who post for all of it speak for it.
		ok[s.ComponentID] = (v || !seen) && s.ProjectID != 0 && a.canSee(s.OrgID, s.ProjectID, s.EnvironmentID) &&
			a.holdsAt(PermStatusPagesPost, s.ProjectID, s.EnvironmentID, kinds[s.EnvironmentID])
	}
	for _, im := range ims {
		if !ok[im.component] {
			return errf(apitypes.CodeForbidden, "%s can't speak for component %q: it isn't built only from what %s posts for; ask someone who posts for all of it", a, im.name, a)
		}
	}
	return nil
}

// cleanMessage is an update's words as stored: plain text with Unix line
// ends and no control characters but newlines and tabs.
func cleanMessage(s string) (string, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\r\n", "\n")
	s = strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if n := utf8.RuneCountInString(s); n > statusMaxMessage {
		return "", invalid("an update has at most %d characters; this one has %d", statusMaxMessage, n)
	}
	return s, nil
}

// cleanTitle is an incident's title as stored.
func cleanTitle(s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	switch n := utf8.RuneCountInString(s); {
	case n == 0:
		return "", invalid("an incident needs a title: what the public reads, e.g. \"API errors for some requests\"")
	case n > statusMaxTitle:
		return "", invalid("an incident's title has at most %d characters", statusMaxTitle)
	}
	return s, nil
}

// postable says whether st is a state an update may post.
func postable(st apitypes.IncidentState) bool {
	for _, s := range apitypes.IncidentStates {
		if s == st {
			return true
		}
	}
	return false
}

// incidentChanged marks the pages' renders stale after an incident
// changed.
func (c *Core) incidentChanged() { c.bus.statusGen.Add(1) }

// auditIncident records an incident's change in the page's org.
func (c *Core) auditIncident(ctx context.Context, a Actor, action string, p store.StatusPage, id, summary string) {
	if a.Kind == "system" {
		a = a.InOrg(p.OrgID)
	}
	c.audit(ctx, a, action, nil, nil, "incident "+id, "status page "+p.Slug+": "+summary)
}

// OpenIncident opens an incident on a page: published with its first
// update, or a draft (State draft) nobody outside Dock sees until it is
// published. Deploy scope.
func (c *Core) OpenIncident(ctx context.Context, a Actor, slug string, req apitypes.OpenIncidentRequest) (apitypes.StatusIncident, error) {
	if err := incidentsIn(a, "opening an incident"); err != nil {
		return apitypes.StatusIncident{}, err
	}
	p, err := c.loadStatusPage(ctx, a, slug, apitypes.ScopeRead, "opening an incident")
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	title, err := cleanTitle(req.Title)
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	msg, err := cleanMessage(req.Message)
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	state := req.State
	if state == "" {
		state = apitypes.IncidentInvestigating
	}
	draft := state == apitypes.IncidentDraft
	switch {
	case !draft && (!postable(state) || state == apitypes.IncidentResolved):
		return apitypes.StatusIncident{}, invalid("an incident opens as investigating, identified, monitoring or draft, not %q", state)
	case !draft && msg == "":
		return apitypes.StatusIncident{}, invalid("an incident's first update says what is going on: pass a message, or open it as a draft")
	}
	ims, err := c.resolveImpacts(ctx, a, p, req.Components, apitypes.KindIncident)
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	t := now()
	params := store.CreateStatusIncidentParams{PageID: p.ID, PublicID: newPublicID(), Kind: string(apitypes.KindIncident), Title: title, State: string(state),
		StartsAt: t, CreatedAt: t, CreatedBy: a.Name}
	if draft {
		params.Draft = msg
	} else {
		params.PublishedAt = &t
	}
	var row store.StatusIncident
	var first store.StatusUpdate
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		if row, err = q.CreateStatusIncident(ctx, params); err != nil {
			return err
		}
		if err := writeImpacts(ctx, q, row.ID, ims); err != nil {
			return err
		}
		if draft {
			return nil
		}
		first, err = q.CreateStatusUpdate(ctx, store.CreateStatusUpdateParams{IncidentID: row.ID, State: string(state), Body: msg, At: t, CreatedBy: a.Name})
		return err
	})
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	what := "opened incident"
	if draft {
		what = "drafted incident"
	}
	c.auditIncident(ctx, a, "incident.open", p, row.PublicID, fmt.Sprintf("%s %s %q (%s)", what, row.PublicID, title, state))
	c.incidentChanged()
	if !draft {
		c.statusUpdatePosted(ctx, p, row, first)
	}
	return c.incidentView(ctx, a, p, row)
}

// incidentNext is an incident's row as UpdateStatusIncident takes it, for
// a change to start from.
func incidentNext(r store.StatusIncident) store.UpdateStatusIncidentParams {
	return store.UpdateStatusIncidentParams{ID: r.ID, Title: r.Title, State: r.State, Draft: r.Draft, StartsAt: r.StartsAt,
		EndsAt: r.EndsAt, PublishedAt: r.PublishedAt, ResolvedAt: r.ResolvedAt}
}

// writeImpacts replaces what an incident does to components.
func writeImpacts(ctx context.Context, q *store.Queries, id int64, ims []statusImpact) error {
	if err := q.DeleteIncidentImpacts(ctx, id); err != nil {
		return err
	}
	for _, im := range ims {
		if err := q.CreateIncidentImpact(ctx, store.CreateIncidentImpactParams{IncidentID: id, ComponentID: im.component, Impact: string(im.impact)}); err != nil {
			return err
		}
	}
	return nil
}

// incidentView is an incident as the editor shows it to a.
func (c *Core) incidentView(ctx context.Context, a Actor, p store.StatusPage, row store.StatusIncident) (apitypes.StatusIncident, error) {
	inc, err := c.loadIncident(ctx, row)
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	return toStatusIncident(a, inc, p.Slug, c.statusPageURL(ctx, p)), nil
}

// statusPageURL is a published page's address (statusAddress), "" for
// none.
func (c *Core) statusPageURL(ctx context.Context, p store.StatusPage) string {
	if !p.Published {
		return ""
	}
	base, err := c.publicBase(ctx)
	if err != nil {
		return ""
	}
	return c.statusAddress(p, base)
}

// GetIncident is an incident with its updates. Read scope.
func (c *Core) GetIncident(ctx context.Context, a Actor, id string) (apitypes.StatusIncident, error) {
	row, p, err := c.loadIncidentFor(ctx, a, id, apitypes.ScopeRead, "reading incidents")
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	return c.incidentView(ctx, a, p, row)
}

// UpdateIncident changes an incident's title, the components it names
// or a draft's words, without posting an update. Deploy scope.
func (c *Core) UpdateIncident(ctx context.Context, a Actor, id string, req apitypes.UpdateIncidentRequest) (apitypes.StatusIncident, error) {
	row, p, err := c.loadIncidentFor(ctx, a, id, apitypes.ScopeDeploy, "changing an incident")
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	inc, err := c.loadIncident(ctx, row)
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	if err := c.checkIncidentScope(ctx, a, p, inc.impacts); err != nil {
		return apitypes.StatusIncident{}, err
	}
	next := incidentNext(row)
	maint := row.Kind == string(apitypes.KindMaintenance)
	var changes []string
	if req.StartsAt != nil || req.EndsAt != nil {
		if !maint || row.ResolvedAt != nil {
			return apitypes.StatusIncident{}, conflict("only a maintenance window not finished yet has times to change")
		}
		if req.StartsAt != nil {
			if row.State != string(apitypes.MaintenanceScheduled) {
				return apitypes.StatusIncident{}, conflict("maintenance %s has started; change its end instead", row.PublicID)
			}
			next.StartsAt = req.StartsAt.UTC()
			changes = append(changes, "starts "+next.StartsAt.Format(time.RFC3339))
		}
		if req.EndsAt != nil {
			end := req.EndsAt.UTC()
			next.EndsAt = &end
			changes = append(changes, "ends "+end.Format(time.RFC3339))
		}
		end := next.StartsAt
		if next.EndsAt != nil {
			end = *next.EndsAt
		}
		if err := checkWindow(next.StartsAt, end, now()); err != nil {
			return apitypes.StatusIncident{}, err
		}
	}
	if req.Title != nil {
		t, err := cleanTitle(*req.Title)
		if err != nil {
			return apitypes.StatusIncident{}, err
		}
		if t != row.Title {
			next.Title = t
			changes = append(changes, fmt.Sprintf("title %q", t))
		}
	}
	if req.Draft != nil {
		if inc.published() {
			return apitypes.StatusIncident{}, conflict("incident %s is published, so it has no draft; post an update instead", row.PublicID)
		}
		d, err := cleanMessage(*req.Draft)
		if err != nil {
			return apitypes.StatusIncident{}, err
		}
		if d != row.Draft {
			next.Draft = d
			changes = append(changes, "draft words")
		}
	}
	var ims []statusImpact
	if req.Components != nil {
		if ims, err = c.resolveImpacts(ctx, a, p, *req.Components, apitypes.IncidentKind(row.Kind)); err != nil {
			return apitypes.StatusIncident{}, err
		}
		changes = append(changes, "components "+impactsText(ims))
	}
	// Moving or extending a window, or changing what it covers, changes
	// what it mutes.
	if maint && (req.StartsAt != nil || req.EndsAt != nil || req.Components != nil) {
		covers := inc.impacts
		if req.Components != nil {
			covers = ims
		}
		if err := c.checkMaintenanceLocks(ctx, a, p, covers, "changing maintenance"); err != nil {
			return apitypes.StatusIncident{}, err
		}
	}
	if len(changes) == 0 {
		return toStatusIncident(a, inc, p.Slug, c.statusPageURL(ctx, p)), nil
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if _, err := q.UpdateStatusIncident(ctx, next); err != nil {
			return err
		}
		if req.Components != nil {
			return writeImpacts(ctx, q, row.ID, ims)
		}
		return nil
	})
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	action := "incident.edit"
	if maint {
		action = "maintenance.edit"
	}
	c.auditIncident(ctx, a, action, p, row.PublicID, row.Kind+" "+row.PublicID+": "+strings.Join(changes, ", "))
	if maint {
		c.maintenanceChanged(ctx)
		c.maintenanceTick(ctx, now())
	} else {
		c.incidentChanged()
	}
	return c.GetIncident(ctx, a, row.PublicID)
}

// impactsText is what an incident does, for the audit log.
func impactsText(ims []statusImpact) string {
	if len(ims) == 0 {
		return "none"
	}
	parts := make([]string, len(ims))
	for i, im := range ims {
		parts[i] = im.name + " " + string(im.impact)
	}
	return strings.Join(parts, ", ")
}

// PostIncidentUpdate posts an update, public at once: on a draft it
// publishes the incident, resolved resolves it. Deploy scope.
func (c *Core) PostIncidentUpdate(ctx context.Context, a Actor, id string, req apitypes.PostIncidentUpdateRequest) (apitypes.StatusIncident, error) {
	row, p, err := c.loadIncidentFor(ctx, a, id, apitypes.ScopeDeploy, "posting an incident update")
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	maint := row.Kind == string(apitypes.KindMaintenance)
	switch {
	case row.ResolvedAt != nil && maint:
		return apitypes.StatusIncident{}, conflict("maintenance %s is %s; schedule a new window (`dock maintenance schedule`)", row.PublicID, row.State)
	case row.ResolvedAt != nil:
		return apitypes.StatusIncident{}, conflict("incident %s is resolved; open a new one (`dock incident open`)", row.PublicID)
	case !maint && !postable(req.State):
		return apitypes.StatusIncident{}, invalid("an update's state is investigating, identified, monitoring or resolved, not %q", req.State)
	}
	action := ""
	if maint {
		if action, err = maintTransition(row.PublicID, apitypes.IncidentState(row.State), req.State); err != nil {
			return apitypes.StatusIncident{}, err
		}
	}
	msg, err := cleanMessage(req.Message)
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	if msg == "" {
		return apitypes.StatusIncident{}, invalid("an update needs a message: what the public reads")
	}
	inc, err := c.loadIncident(ctx, row)
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	if err := c.checkIncidentScope(ctx, a, p, inc.impacts); err != nil {
		return apitypes.StatusIncident{}, err
	}
	var ims []statusImpact
	if req.Components != nil {
		if ims, err = c.resolveImpacts(ctx, a, p, *req.Components, apitypes.IncidentKind(row.Kind)); err != nil {
			return apitypes.StatusIncident{}, err
		}
	}
	// Starting a window, or changing what it covers, mutes alerts;
	// completing or cancelling it doesn't.
	if maint && (req.State == apitypes.MaintenanceInProgress || req.State == apitypes.MaintenanceScheduled) &&
		(action == "maintenance.start" || req.Components != nil) {
		covers := inc.impacts
		if req.Components != nil {
			covers = ims
		}
		if err := c.checkMaintenanceLocks(ctx, a, p, covers, "starting or changing maintenance"); err != nil {
			return apitypes.StatusIncident{}, err
		}
	}
	t := now()
	wasDraft := !inc.published()
	next := incidentNext(row)
	next.State, next.Draft = string(req.State), ""
	if wasDraft {
		next.PublishedAt = &t
	}
	switch req.State {
	case apitypes.IncidentResolved, apitypes.MaintenanceCancelled:
		next.ResolvedAt = &t
	case apitypes.MaintenanceCompleted:
		next.ResolvedAt, next.EndsAt = &t, &t
	case apitypes.MaintenanceInProgress:
		if row.State == string(apitypes.MaintenanceScheduled) {
			next.StartsAt = t
		}
	}
	var posted store.StatusUpdate
	var updated store.StatusIncident
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		if updated, err = q.UpdateStatusIncident(ctx, next); err != nil {
			return err
		}
		if req.Components != nil {
			if err := writeImpacts(ctx, q, row.ID, ims); err != nil {
				return err
			}
		}
		posted, err = q.CreateStatusUpdate(ctx, store.CreateStatusUpdateParams{IncidentID: row.ID, State: string(req.State), Body: msg, At: t, CreatedBy: a.Name})
		return err
	})
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	what := "posted " + string(req.State)
	switch {
	case maint:
		what = strings.TrimPrefix(action, "maintenance.")
	case wasDraft:
		action, what = "incident.publish", "published as "+string(req.State)
	case req.State == apitypes.IncidentResolved:
		action, what = "incident.resolve", "resolved"
	default:
		action = "incident.update"
	}
	if req.Components != nil {
		what += "; components " + impactsText(ims)
	}
	c.auditIncident(ctx, a, action, p, row.PublicID, row.Kind+" "+row.PublicID+" "+what)
	if maint {
		c.maintenanceChanged(ctx)
	} else {
		c.incidentChanged()
	}
	c.statusUpdatePosted(ctx, p, updated, posted)
	return c.GetIncident(ctx, a, row.PublicID)
}

// EditIncidentUpdate fixes an update's words: the page says it was
// edited, and the audit log keeps what it said. Deploy scope.
func (c *Core) EditIncidentUpdate(ctx context.Context, a Actor, id string, update int64, req apitypes.EditIncidentUpdateRequest) (apitypes.StatusIncident, error) {
	row, p, err := c.loadIncidentFor(ctx, a, id, apitypes.ScopeDeploy, "editing an incident update")
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	u, err := c.store.GetStatusUpdate(ctx, store.GetStatusUpdateParams{ID: update, IncidentID: row.ID})
	if store.IsNotFound(err) {
		return apitypes.StatusIncident{}, notFound("incident %s has no update %d; `dock incident show %s` lists them", row.PublicID, update, row.PublicID)
	}
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	inc, err := c.loadIncident(ctx, row)
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	if err := c.checkIncidentScope(ctx, a, p, inc.impacts); err != nil {
		return apitypes.StatusIncident{}, err
	}
	msg, err := cleanMessage(req.Message)
	if err != nil {
		return apitypes.StatusIncident{}, err
	}
	if msg == "" {
		return apitypes.StatusIncident{}, invalid("an update needs a message: what the public reads")
	}
	if msg == u.Body {
		return toStatusIncident(a, inc, p.Slug, c.statusPageURL(ctx, p)), nil
	}
	t := now()
	if _, err := c.store.EditStatusUpdate(ctx, store.EditStatusUpdateParams{ID: u.ID, Body: msg, EditedAt: &t}); err != nil {
		return apitypes.StatusIncident{}, err
	}
	c.auditIncident(ctx, a, "incident.edit_update", p, row.PublicID,
		fmt.Sprintf("incident %s: edited update %d, which said: %s", row.PublicID, u.ID, clip(u.Body, 1000)))
	c.incidentChanged()
	return c.GetIncident(ctx, a, row.PublicID)
}

// clip shortens s to at most n runes for a log line.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// DeleteIncident removes an incident and its updates from the page and
// its history. The page's admins: it rewrites the public record.
func (c *Core) DeleteIncident(ctx context.Context, a Actor, id string) error {
	row, p, err := c.loadIncidentFor(ctx, a, id, apitypes.ScopeAdmin, "removing an incident")
	if err != nil {
		return err
	}
	if err := c.store.DeleteStatusIncident(ctx, row.ID); err != nil {
		return err
	}
	c.auditIncident(ctx, a, "incident.delete", p, row.PublicID, fmt.Sprintf("removed %s %s %q", row.Kind, row.PublicID, row.Title))
	if row.Kind == string(apitypes.KindMaintenance) {
		c.maintenanceChanged(ctx)
	} else {
		c.incidentChanged()
	}
	return nil
}

// statusAlertOpened drafts an incident on each published page that takes
// drafts when an uptime_down or crash loop alert opens about the sources
// of its components and no open incident names them yet. The draft
// says a fixed phrase with the components' public names, never the
// alert's words. One indexed query, when such an alert opens.
func (c *Core) statusAlertOpened(ctx context.Context, al store.Alert) {
	kind := apitypes.AlertKind(al.Kind)
	// An outside check's uptime_down alert names no service (ADR 0055).
	if (kind != apitypes.AlertUptimeDown && kind != apitypes.AlertCrashLoop) || (al.ServiceID == nil && kind != apitypes.AlertUptimeDown) || al.MutedBy != nil {
		// A held alert is planned work's: no draft.
		return
	}
	var check int64
	impact, title := apitypes.StatusPartialOutage, "Errors on %s"
	if kind == apitypes.AlertUptimeDown {
		check, _ = strconv.ParseInt(strings.TrimPrefix(al.Key, "uptime:"), 10, 64)
		impact, title = apitypes.StatusMajorOutage, "%s unavailable"
	}
	targets, err := c.store.StatusDraftTargets(ctx, store.StatusDraftTargetsParams{ServiceID: al.ServiceID, CheckID: &check})
	if err != nil {
		c.log.Error("status pages: find the components an alert is about", "alert", al.Key, "err", err)
		return
	}
	type draft struct {
		page  store.StatusDraftTargetsRow
		ims   []statusImpact
		names []string
	}
	var drafts []*draft
	seen := map[int64]bool{}
	for _, t := range targets {
		if t.Covered || seen[t.ComponentID] {
			continue
		}
		seen[t.ComponentID] = true
		if n := len(drafts); n == 0 || drafts[n-1].page.PageID != t.PageID {
			drafts = append(drafts, &draft{page: t})
		}
		d := drafts[len(drafts)-1]
		d.ims = append(d.ims, statusImpact{t.ComponentID, t.Name, impact})
		d.names = append(d.names, t.Name)
	}
	for _, d := range drafts {
		names := andList(d.names)
		params := store.CreateStatusIncidentParams{PageID: d.page.PageID, PublicID: newPublicID(), Kind: string(apitypes.KindIncident), Title: clip(fmt.Sprintf(title, names), statusMaxTitle),
			State: string(apitypes.IncidentDraft), Draft: "We are investigating problems with " + names + ".",
			StartsAt: al.OpenedAt, AlertID: &al.ID, CreatedAt: now(), CreatedBy: statusDraftBy}
		var row store.StatusIncident
		err := c.store.Tx(ctx, func(q *store.Queries) error {
			var err error
			if row, err = q.CreateStatusIncident(ctx, params); err != nil {
				return err
			}
			return writeImpacts(ctx, q, row.ID, d.ims)
		})
		if err != nil {
			c.log.Error("status pages: draft an incident", "alert", al.Key, "page", d.page.Slug, "err", err)
			continue
		}
		p := store.StatusPage{ID: d.page.PageID, OrgID: d.page.OrgID, Slug: d.page.Slug}
		c.auditIncident(ctx, System, "incident.draft", p, row.PublicID,
			fmt.Sprintf("Dock drafted incident %s %q from alert %s %q", row.PublicID, row.Title, al.Kind, al.Title))
	}
}

// andList lists names as "A", "A and B" or "A, B and C".
func andList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// statusAlertResolved drops the drafts Dock opened from an alert that
// resolved before anyone published them.
func (c *Core) statusAlertResolved(ctx context.Context, al store.Alert) {
	kind := apitypes.AlertKind(al.Kind)
	if kind != apitypes.AlertUptimeDown && kind != apitypes.AlertCrashLoop {
		return
	}
	rows, err := c.store.DeleteAlertDrafts(ctx, &al.ID)
	if err != nil {
		c.log.Error("status pages: remove an alert's drafts", "alert", al.Key, "err", err)
		return
	}
	for _, r := range rows {
		p, err := c.store.GetStatusPageByID(ctx, r.PageID)
		if err != nil {
			continue
		}
		c.auditIncident(ctx, System, "incident.delete", p, r.PublicID,
			fmt.Sprintf("Dock removed draft incident %s %q: its alert resolved first", r.PublicID, r.Title))
	}
}

// PublicStatusIncident is one published incident of the published page
// with ID page, as anyone sees it. CodeNotFound for anything else.
func (c *Core) PublicStatusIncident(ctx context.Context, page int64, id string) (apitypes.PublicIncident, string, error) {
	gone := notFound("no status page here")
	if !ValidPublicID(id) {
		return apitypes.PublicIncident{}, "", gone
	}
	row, err := c.store.GetStatusIncident(ctx, id)
	switch {
	case store.IsNotFound(err):
		return apitypes.PublicIncident{}, "", gone
	case err != nil:
		return apitypes.PublicIncident{}, "", err
	case row.StatusPage.ID != page || !row.StatusPage.Published || row.StatusIncident.State == string(apitypes.IncidentDraft):
		return apitypes.PublicIncident{}, "", gone
	}
	inc, err := c.loadIncident(ctx, row.StatusIncident)
	if err != nil {
		return apitypes.PublicIncident{}, "", err
	}
	return toPublicIncident(inc), row.StatusPage.Title, nil
}

// PublicStatusHistory is every published incident of the published page
// with ID page, newest first, up to statusHistoryMax.
func (c *Core) PublicStatusHistory(ctx context.Context, page int64) (apitypes.PublicStatusHistory, error) {
	p, err := c.publishedPage(ctx, page)
	if err != nil {
		return apitypes.PublicStatusHistory{}, err
	}
	rows, err := c.store.ListStatusHistory(ctx, store.ListStatusHistoryParams{PageID: p.ID, Max: statusHistoryMax + 1})
	if err != nil {
		return apitypes.PublicStatusHistory{}, err
	}
	out := apitypes.PublicStatusHistory{Title: p.Title, More: len(rows) > statusHistoryMax}
	for _, r := range rows[:min(len(rows), statusHistoryMax)] {
		out.Incidents = append(out.Incidents, apitypes.PublicIncident{ID: r.PublicID, Title: r.Title,
			State: apitypes.IncidentState(r.State), StartedAt: r.StartsAt, ResolvedAt: r.ResolvedAt})
	}
	return out, nil
}

// PublicStatusFeed is the newest posted updates of the published page
// with ID page, for its Atom feed.
func (c *Core) PublicStatusFeed(ctx context.Context, page int64) (apitypes.PublicStatusFeed, error) {
	p, err := c.publishedPage(ctx, page)
	if err != nil {
		return apitypes.PublicStatusFeed{}, err
	}
	rows, err := c.store.ListStatusFeed(ctx, store.ListStatusFeedParams{PageID: p.ID, Max: statusFeedMax})
	if err != nil {
		return apitypes.PublicStatusFeed{}, err
	}
	out := apitypes.PublicStatusFeed{Title: p.Title, Updated: p.CreatedAt}
	for _, r := range rows {
		e := apitypes.PublicFeedEntry{Incident: r.PublicID, Title: r.Title, Update: r.ID,
			PublicUpdate: apitypes.PublicUpdate{State: apitypes.IncidentState(r.State), Message: r.Body, At: r.At, Edited: r.EditedAt != nil}}
		out.Entries = append(out.Entries, e)
		updated := r.At
		if r.EditedAt != nil {
			updated = *r.EditedAt
		}
		if updated.After(out.Updated) {
			out.Updated = updated
		}
	}
	return out, nil
}

// publishedPage is the page with ID id while it is published.
func (c *Core) publishedPage(ctx context.Context, id int64) (store.StatusPage, error) {
	p, err := c.store.GetStatusPageByID(ctx, id)
	if store.IsNotFound(err) || (err == nil && !p.Published) {
		return p, notFound("no status page here")
	}
	return p, err
}
