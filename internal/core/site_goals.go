package core

import (
	"context"
	"encoding/json/v2"
	"strings"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Goals and funnels (ADR 0037, phase A4): Dock keeps what they are, per
// site; the analytics store counts them over a range's events, with the
// dashboard's filters. Reading takes project read, changing them deploy.

// maxFunnels caps a site's funnels.
const maxFunnels = 20

// checkGoalStep refuses a step the store can't match.
func checkGoalStep(s apitypes.GoalStep) error {
	switch s.Kind {
	case string(analytics.GoalPage):
		if !strings.HasPrefix(s.Match, "/") || len(s.Match) > 200 {
			return invalid("a page goal is a path starting with / (* matches anything), at most 200 characters, not %q", s.Match)
		}
	case string(analytics.GoalEvent):
		if s.Match == "" || len(s.Match) > 120 {
			return invalid("an event goal is the event's name, as dock.track sends it: 1 to 120 characters")
		}
	default:
		return invalid("a goal is a page or an event, not %q", s.Kind)
	}
	return nil
}

// checkGoalName refuses a name that can't name a goal or funnel.
func checkGoalName(name string) error {
	if name == "" || len(name) > 60 || strings.TrimSpace(name) != name {
		return invalid("a goal or funnel's name is 1 to 60 characters, without spaces around it")
	}
	return nil
}

// SiteGoals returns a site's goals with their conversions in a range. An
// unreachable store gives the goals without counts and StoreOnline false.
func (c *Core) SiteGoals(ctx context.Context, a Actor, project, name string, q StatsQuery) (apitypes.SiteGoals, error) {
	s, err := c.loadSite(ctx, a, project, name, apitypes.ScopeRead, "reading a site's goals")
	if err != nil {
		return apitypes.SiteGoals{}, err
	}
	query, err := c.statsQuery(ctx, s, q)
	if err != nil {
		return apitypes.SiteGoals{}, err
	}
	rows, err := c.store.ListSiteGoals(ctx, s.site.ID)
	if err != nil {
		return apitypes.SiteGoals{}, err
	}
	out := apitypes.SiteGoals{Site: s.site.Name, From: query.From, To: query.To, Goals: make([]apitypes.Goal, len(rows))}
	specs := make([]analytics.GoalSpec, len(rows))
	for i, r := range rows {
		out.Goals[i] = apitypes.Goal{Name: r.Name, GoalStep: apitypes.GoalStep{Kind: r.Kind, Match: r.Match}}
		specs[i] = analytics.GoalSpec{Kind: analytics.GoalKind(r.Kind), Match: r.Match}
	}
	cl, _ := c.analyticsClient()
	if cl == nil {
		out.StoreError = analyticsOff
		return out, nil
	}
	if len(rows) == 0 {
		out.StoreOnline = true
		return out, nil
	}
	res, err := cl.Goals(ctx, analytics.GoalsQuery{Query: query, Goals: specs})
	var o analytics.Overview
	if err == nil {
		o, err = cl.Overview(ctx, query)
	}
	if out.StoreOnline, out.StoreError, err = storeStatus(err); err != nil || !out.StoreOnline {
		return out, err
	}
	out.Visitors = o.Visitors
	for i, r := range res {
		g := &out.Goals[i]
		g.Visitors, g.Sessions, g.Count = r.Visitors, r.Sessions, r.Count
		if o.Visitors > 0 {
			g.Rate = min(1, float64(r.Visitors)/float64(o.Visitors))
		}
	}
	return out, nil
}

// statsQuery checks a dashboard query's filters and range for a site.
func (c *Core) statsQuery(ctx context.Context, s siteScope, q StatsQuery) (analytics.Query, error) {
	if err := checkFilters(q.Filters); err != nil {
		return analytics.Query{}, err
	}
	filters, err := c.serviceFilters(ctx, s, q.Filters)
	if err != nil {
		return analytics.Query{}, err
	}
	from, to, err := q.span(now())
	if err != nil {
		return analytics.Query{}, err
	}
	return analytics.Query{Site: s.site.ID, From: from, To: to, Filters: filters}, nil
}

// AddSiteGoal adds a goal to a site.
func (c *Core) AddSiteGoal(ctx context.Context, a Actor, project, name string, req apitypes.CreateGoalRequest) (apitypes.Goal, error) {
	s, err := c.loadSite(ctx, a, project, name, apitypes.ScopeDeploy, "adding a goal")
	if err != nil {
		return apitypes.Goal{}, err
	}
	if err := checkGoalName(req.Name); err != nil {
		return apitypes.Goal{}, err
	}
	if err := checkGoalStep(req.GoalStep); err != nil {
		return apitypes.Goal{}, err
	}
	var row store.SiteGoal
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		goals, err := q.ListSiteGoals(ctx, s.site.ID)
		if err != nil {
			return err
		}
		if len(goals) >= analytics.MaxGoals {
			return conflict("site %s has %d goals, the most a site can have; remove one first", s.site.Name, analytics.MaxGoals)
		}
		for _, g := range goals {
			if g.Name == req.Name {
				return conflict("site %s has a goal named %q", s.site.Name, req.Name)
			}
		}
		row, err = q.AddSiteGoal(ctx, store.AddSiteGoalParams{SiteID: s.site.ID, Name: req.Name, Kind: req.Kind, Match: req.Match,
			CreatedBy: a.Name, CreatedAt: now()})
		return err
	})
	if err != nil {
		return apitypes.Goal{}, err
	}
	c.audit(ctx, a, "analytics.goal_add", &s.project, &s.env, s.site.Name, "goal "+row.Name+": "+row.Kind+" "+row.Match)
	return apitypes.Goal{Name: row.Name, GoalStep: apitypes.GoalStep{Kind: row.Kind, Match: row.Match}}, nil
}

// RemoveSiteGoal removes a site's goal.
func (c *Core) RemoveSiteGoal(ctx context.Context, a Actor, project, name, goal string) error {
	s, err := c.loadSite(ctx, a, project, name, apitypes.ScopeDeploy, "removing a goal")
	if err != nil {
		return err
	}
	n, err := c.store.RemoveSiteGoal(ctx, store.RemoveSiteGoalParams{SiteID: s.site.ID, Name: goal})
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("site %s has no goal %q; see `dock analytics goals %s`", s.site.Name, goal, s.site.Name)
	}
	c.audit(ctx, a, "analytics.goal_remove", &s.project, &s.env, s.site.Name, "goal "+goal)
	return nil
}

// SiteFunnels lists a site's funnels.
func (c *Core) SiteFunnels(ctx context.Context, a Actor, project, name string) ([]apitypes.Funnel, error) {
	s, err := c.loadSite(ctx, a, project, name, apitypes.ScopeRead, "reading a site's funnels")
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListSiteFunnels(ctx, s.site.ID)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.Funnel, 0, len(rows))
	for _, r := range rows {
		out = append(out, toFunnel(r))
	}
	return out, nil
}

// toFunnel converts a funnel's row.
func toFunnel(r store.SiteFunnel) apitypes.Funnel {
	f := apitypes.Funnel{Name: r.Name}
	_ = json.Unmarshal([]byte(r.Steps), &f.Steps)
	return f
}

// AddSiteFunnel adds a funnel to a site.
func (c *Core) AddSiteFunnel(ctx context.Context, a Actor, project, name string, req apitypes.CreateFunnelRequest) (apitypes.Funnel, error) {
	s, err := c.loadSite(ctx, a, project, name, apitypes.ScopeDeploy, "adding a funnel")
	if err != nil {
		return apitypes.Funnel{}, err
	}
	if err := checkGoalName(req.Name); err != nil {
		return apitypes.Funnel{}, err
	}
	if len(req.Steps) < 2 || len(req.Steps) > analytics.MaxFunnelSteps {
		return apitypes.Funnel{}, invalid("a funnel has 2 to %d steps", analytics.MaxFunnelSteps)
	}
	for _, st := range req.Steps {
		if err := checkGoalStep(st); err != nil {
			return apitypes.Funnel{}, err
		}
	}
	steps, err := json.Marshal(req.Steps)
	if err != nil {
		return apitypes.Funnel{}, err
	}
	var row store.SiteFunnel
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		funnels, err := q.ListSiteFunnels(ctx, s.site.ID)
		if err != nil {
			return err
		}
		if len(funnels) >= maxFunnels {
			return conflict("site %s has %d funnels, the most a site can have; remove one first", s.site.Name, maxFunnels)
		}
		for _, f := range funnels {
			if f.Name == req.Name {
				return conflict("site %s has a funnel named %q", s.site.Name, req.Name)
			}
		}
		row, err = q.AddSiteFunnel(ctx, store.AddSiteFunnelParams{SiteID: s.site.ID, Name: req.Name, Steps: string(steps), CreatedBy: a.Name, CreatedAt: now()})
		return err
	})
	if err != nil {
		return apitypes.Funnel{}, err
	}
	c.audit(ctx, a, "analytics.funnel_add", &s.project, &s.env, s.site.Name, "funnel "+row.Name)
	return toFunnel(row), nil
}

// RemoveSiteFunnel removes a site's funnel.
func (c *Core) RemoveSiteFunnel(ctx context.Context, a Actor, project, name, funnel string) error {
	s, err := c.loadSite(ctx, a, project, name, apitypes.ScopeDeploy, "removing a funnel")
	if err != nil {
		return err
	}
	n, err := c.store.RemoveSiteFunnel(ctx, store.RemoveSiteFunnelParams{SiteID: s.site.ID, Name: funnel})
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("site %s has no funnel %q; see `dock analytics funnels %s`", s.site.Name, funnel, s.site.Name)
	}
	c.audit(ctx, a, "analytics.funnel_remove", &s.project, &s.env, s.site.Name, "funnel "+funnel)
	return nil
}

// SiteFunnel counts a funnel's steps over a range: the sessions that
// reached each after the ones before it.
func (c *Core) SiteFunnel(ctx context.Context, a Actor, project, name, funnel string, q StatsQuery) (apitypes.SiteFunnel, error) {
	s, err := c.loadSite(ctx, a, project, name, apitypes.ScopeRead, "reading a site's funnels")
	if err != nil {
		return apitypes.SiteFunnel{}, err
	}
	row, err := c.store.GetSiteFunnel(ctx, store.GetSiteFunnelParams{SiteID: s.site.ID, Name: funnel})
	if store.IsNotFound(err) {
		return apitypes.SiteFunnel{}, notFound("site %s has no funnel %q; see `dock analytics funnels %s`", s.site.Name, funnel, s.site.Name)
	}
	if err != nil {
		return apitypes.SiteFunnel{}, err
	}
	query, err := c.statsQuery(ctx, s, q)
	if err != nil {
		return apitypes.SiteFunnel{}, err
	}
	f := toFunnel(row)
	out := apitypes.SiteFunnel{Site: s.site.Name, Name: f.Name, From: query.From, To: query.To, Steps: make([]apitypes.FunnelStep, len(f.Steps))}
	specs := make([]analytics.GoalSpec, len(f.Steps))
	for i, st := range f.Steps {
		out.Steps[i].GoalStep = st
		specs[i] = analytics.GoalSpec{Kind: analytics.GoalKind(st.Kind), Match: st.Match}
	}
	cl, _ := c.analyticsClient()
	if cl == nil {
		out.StoreError = analyticsOff
		return out, nil
	}
	res, err := cl.Funnel(ctx, analytics.FunnelQuery{Query: query, Steps: specs})
	if out.StoreOnline, out.StoreError, err = storeStatus(err); err != nil || !out.StoreOnline {
		return out, err
	}
	for i, r := range res {
		st := &out.Steps[i]
		st.Sessions, st.Visitors = r.Sessions, r.Visitors
		if res[0].Sessions > 0 {
			st.Conversion = float64(r.Sessions) / float64(res[0].Sessions)
		}
	}
	return out, nil
}
