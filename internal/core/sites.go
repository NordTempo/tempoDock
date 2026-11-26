package core

import (
	"context"
	"encoding/json/v2"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Sites (ADR 0037): analytics belong to a set of hostnames in one
// environment, not to a service. Turning analytics on for a hostname adds
// it to the site of the service's other hostnames, or makes a site named
// after it. A hostname stays with its site when its domain is removed;
// when the domain comes back on another service, the site follows it
// (with a marker on its charts) unless a new site is asked for.

// siteScope is a site with the environment it belongs to.
type siteScope struct {
	envScope
	site store.Site
}

// defaultSiteSettings are the settings of a site that sets nothing and an
// install that changed no default.
func defaultSiteSettings() apitypes.SiteSettings {
	f, t := false, true
	sample, events, replays, errs := 20, 396, 30, 90
	paths, ips, domain, consent := []string{}, []string{}, "", apitypes.ConsentReplay
	return apitypes.SiteSettings{
		Consent: &consent, Replay: &f, ReplaySampleRate: &sample, MaskAllText: &f, ErrorsAndVitals: &t,
		EventRetentionDays: &events, ReplayRetentionDays: &replays, ErrorRetentionDays: &errs,
		ExcludedPaths: &paths, IgnoredIPs: &ips, Public: &f, PublicDomain: &domain, PublicErrors: &f,
	}
}

// over is the field over sets, else base's.
func over[T any](base, o *T) *T {
	if o != nil {
		return o
	}
	return base
}

// mergeSettings applies o's set fields over base.
func mergeSettings(base, o apitypes.SiteSettings) apitypes.SiteSettings {
	return apitypes.SiteSettings{
		Consent: over(base.Consent, o.Consent),
		Replay:  over(base.Replay, o.Replay), ReplaySampleRate: over(base.ReplaySampleRate, o.ReplaySampleRate),
		MaskAllText: over(base.MaskAllText, o.MaskAllText), ErrorsAndVitals: over(base.ErrorsAndVitals, o.ErrorsAndVitals),
		EventRetentionDays:  over(base.EventRetentionDays, o.EventRetentionDays),
		ReplayRetentionDays: over(base.ReplayRetentionDays, o.ReplayRetentionDays),
		ErrorRetentionDays:  over(base.ErrorRetentionDays, o.ErrorRetentionDays),
		ExcludedPaths:       over(base.ExcludedPaths, o.ExcludedPaths), IgnoredIPs: over(base.IgnoredIPs, o.IgnoredIPs),
		Public: over(base.Public, o.Public), PublicDomain: over(base.PublicDomain, o.PublicDomain),
		PublicErrors: over(base.PublicErrors, o.PublicErrors),
	}
}

// resetSettings clears the named fields (JSON names) of s.
func resetSettings(s *apitypes.SiteSettings, names []string) error {
	for _, n := range names {
		switch n {
		case "consent":
			s.Consent = nil
		case "replay":
			s.Replay = nil
		case "replay_sample_rate":
			s.ReplaySampleRate = nil
		case "mask_all_text":
			s.MaskAllText = nil
		case "errors_and_vitals":
			s.ErrorsAndVitals = nil
		case "event_retention_days":
			s.EventRetentionDays = nil
		case "replay_retention_days":
			s.ReplayRetentionDays = nil
		case "error_retention_days":
			s.ErrorRetentionDays = nil
		case "excluded_paths":
			s.ExcludedPaths = nil
		case "ignored_ips":
			s.IgnoredIPs = nil
		case "public":
			s.Public = nil
		case "public_domain":
			s.PublicDomain = nil
		case "public_errors":
			s.PublicErrors = nil
		default:
			return invalid("%q is not a site setting; see `dock analytics site set --help`", n)
		}
	}
	return nil
}

// checkSettings refuses settings the ingest can't apply.
func checkSettings(s apitypes.SiteSettings) error {
	if c := s.Consent; c != nil && *c != apitypes.ConsentNone && *c != apitypes.ConsentReplay && *c != apitypes.ConsentAll {
		return invalid("consent is none, replay or all, not %q", *c)
	}
	if r := s.ReplaySampleRate; r != nil && (*r < 1 || *r > 100) {
		return invalid("the replay sample rate is a percentage from 1 to 100")
	}
	for _, d := range []*int{s.EventRetentionDays, s.ReplayRetentionDays, s.ErrorRetentionDays} {
		if d != nil && (*d < 1 || *d > 3650) {
			return invalid("retention is 1 to 3650 days")
		}
	}
	if p := s.ExcludedPaths; p != nil {
		if len(*p) > 50 {
			return invalid("at most 50 excluded paths")
		}
		for _, path := range *p {
			if !strings.HasPrefix(path, "/") || len(path) > 200 {
				return invalid("excluded path %q must start with / (and * matches anything), at most 200 characters", path)
			}
		}
	}
	if ips := s.IgnoredIPs; ips != nil {
		if len(*ips) > 50 {
			return invalid("at most 50 ignored addresses")
		}
		for _, ip := range *ips {
			if _, err := parseIPRange(ip); err != nil {
				return invalid("ignored address %q must be an IP address or a CIDR range like 203.0.113.0/24", ip)
			}
		}
	}
	return nil
}

// parseIPRange reads an address or a CIDR range.
func parseIPRange(s string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// siteDefaults are the install's defaults: the built-in ones with the
// console's changes.
func (c *Core) siteDefaults(ctx context.Context) (apitypes.SiteSettings, error) {
	defaults := defaultSiteSettings()
	st, err := c.store.GetAnalyticsSettings(ctx)
	if store.IsNotFound(err) {
		return defaults, nil
	}
	if err != nil {
		return defaults, err
	}
	var set apitypes.SiteSettings
	if err := json.Unmarshal([]byte(st.SiteDefaults), &set); err != nil {
		return defaults, err
	}
	return mergeSettings(defaults, set), nil
}

// siteOverrides reads a site's own settings.
func siteOverrides(s store.Site) apitypes.SiteSettings {
	var o apitypes.SiteSettings
	_ = json.Unmarshal([]byte(s.Settings), &o)
	return o
}

// loadSite finds a live site by name in a project and checks the actor's
// access to its environment.
func (c *Core) loadSite(ctx context.Context, a Actor, project, name string, need apitypes.Scope, action string) (siteScope, error) {
	p, err := c.projectFor(ctx, a, project, apitypes.ScopeRead, action)
	if err != nil {
		return siteScope{}, err
	}
	site, err := c.store.GetSiteByName(ctx, store.GetSiteByNameParams{ProjectID: p.ID, Name: name})
	if store.IsNotFound(err) {
		return siteScope{}, notFound("site %q not found in project %s; see `dock analytics sites`", name, p.Name)
	}
	if err != nil {
		return siteScope{}, err
	}
	env, err := c.store.GetEnvironment(ctx, site.EnvironmentID)
	if err != nil {
		return siteScope{}, err
	}
	if err := a.checkEnv(need, action, p, env); err != nil {
		return siteScope{}, err
	}
	return siteScope{envScope{project: p, env: env}, site}, nil
}

// ListSites returns a project's sites the actor may read, live first.
func (c *Core) ListSites(ctx context.Context, a Actor, project string) ([]apitypes.Site, error) {
	p, err := c.projectFor(ctx, a, project, apitypes.ScopeRead, "listing sites")
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListProjectSites(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	var sites []store.Site
	envs := map[int64]string{}
	for _, r := range rows {
		if a.checkEnv(apitypes.ScopeRead, "listing sites", p, store.Environment{ID: r.EnvironmentID, Name: r.EnvironmentName, ProjectID: p.ID}) != nil {
			continue
		}
		sites = append(sites, store.Site{ID: r.ID, EnvironmentID: r.EnvironmentID, Name: r.Name, Settings: r.Settings, ArchivedAt: r.ArchivedAt,
			CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, PublicSlug: r.PublicSlug})
		envs[r.EnvironmentID] = r.EnvironmentName
	}
	return c.toSites(ctx, sites, envs)
}

// orgSitesMax caps the Analytics area's list, and orgSitesQueries the
// store queries for its numbers running at once.
const (
	orgSitesMax     = 200
	orgSitesQueries = 4
)

// OrgSitesRanges are the ranges the organization's site list takes.
var OrgSitesRanges = []string{"24h", "7d", "30d"}

// ListOrgSites returns the organization's live sites the actor may read,
// by project, each with its totals over rng (one of OrgSitesRanges), the
// range before it and a series: the Analytics area's list. Four SQLite
// queries however many sites, and one store request per site, at most
// four at once.
func (c *Core) ListOrgSites(ctx context.Context, a Actor, rng string) ([]apitypes.OrgSite, error) {
	if err := a.require(apitypes.ScopeRead, "listing sites"); err != nil {
		return nil, err
	}
	if !slices.Contains(OrgSitesRanges, rng) {
		return nil, invalid("range %q isn't one of %s", rng, strings.Join(OrgSitesRanges, ", "))
	}
	rows, err := c.store.ListOrgSites(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	var sites []store.Site
	var projects []string
	envs := map[int64]string{}
	for _, r := range rows {
		env := store.Environment{ID: r.Site.EnvironmentID, Name: r.EnvironmentName, ProjectID: r.Project.ID}
		if a.checkEnv(apitypes.ScopeRead, "listing sites", r.Project, env) != nil || len(sites) == orgSitesMax {
			continue
		}
		sites = append(sites, r.Site)
		projects = append(projects, r.Project.Name)
		envs[env.ID] = env.Name
	}
	list, err := c.toSites(ctx, sites, envs)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.OrgSite, len(list))
	for i := range list {
		out[i] = apitypes.OrgSite{Project: projects[i], Site: list[i]}
	}
	cl, _ := c.analyticsClient()
	if cl == nil {
		return out, nil
	}
	from, to, err := StatsQuery{Range: rng}.span(now())
	if err != nil {
		return nil, err
	}
	// Hours draw a line with some shape up to a week; 30 days by day.
	bucket, step := analytics.BucketHour, time.Hour.Milliseconds()
	if rng == "30d" {
		bucket, step = analytics.BucketDay, 24*time.Hour.Milliseconds()
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(orgSitesQueries)
	for i, s := range sites {
		goGroup(g, c.log, "reading a site's numbers", func() error {
			d, err := cl.Dashboard(gctx, analytics.DashboardQuery{Query: analytics.Query{Site: s.ID, From: from, To: to}, Bucket: bucket})
			if err != nil {
				return nil // a store that can't answer leaves the numbers out
			}
			out[i].Overview, out[i].Previous = ptr(toOverview(d.Overview)), ptr(toOverview(d.Previous))
			out[i].Series = denseSeries(d.Series, from, to, step)
			return nil
		})
	}
	_ = g.Wait()
	return out, nil
}

// denseSeries is pts with every step-wide bucket from the one holding
// from up to to, zero where the store counted nothing (it leaves those
// out), so a sparkline's x axis is time.
func denseSeries(pts []analytics.Point, from, to, step int64) []apitypes.AnalyticsPoint {
	start := from - from%step
	out := make([]apitypes.AnalyticsPoint, 0, (to-start+step-1)/step)
	j := 0
	for t := start; t < to; t += step {
		for j < len(pts) && pts[j].T < t {
			j++
		}
		p := apitypes.AnalyticsPoint{T: t}
		if j < len(pts) && pts[j].T == t {
			p = apitypes.AnalyticsPoint(pts[j])
		}
		out = append(out, p)
	}
	return out
}

// GetSite returns one site.
func (c *Core) GetSite(ctx context.Context, a Actor, project, name string) (apitypes.Site, error) {
	s, err := c.loadSite(ctx, a, project, name, apitypes.ScopeRead, "reading a site")
	if err != nil {
		return apitypes.Site{}, err
	}
	out, err := c.toSites(ctx, []store.Site{s.site}, map[int64]string{s.env.ID: s.env.Name})
	if err != nil {
		return apitypes.Site{}, err
	}
	return out[0], nil
}

// toSites turns sites into API values, with their hostnames (one query).
func (c *Core) toSites(ctx context.Context, sites []store.Site, envs map[int64]string) ([]apitypes.Site, error) {
	out := make([]apitypes.Site, 0, len(sites))
	if len(sites) == 0 {
		return out, nil
	}
	ids := make([]int64, len(sites))
	for i, s := range sites {
		ids[i] = s.ID
	}
	hosts, err := c.store.ListSiteHostnames(ctx, ids)
	if err != nil {
		return nil, err
	}
	defaults, err := c.siteDefaults(ctx)
	if err != nil {
		return nil, err
	}
	base, err := c.publicBase(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range sites {
		o := siteOverrides(s)
		site := apitypes.Site{
			Name: s.Name, Environment: envs[s.EnvironmentID], Archived: s.ArchivedAt != nil, CreatedAt: s.CreatedAt,
			Settings: mergeSettings(defaults, o), Overrides: o, Hostnames: []apitypes.SiteHostname{},
		}
		site.Privacy = sitePrivacy(site.Settings)
		if s.ArchivedAt == nil {
			for _, h := range c.publicHosts(base, s.PublicSlug, site.Settings) {
				site.PublicURLs = append(site.PublicURLs, c.controlURL(h))
			}
		}
		for _, h := range hosts {
			if h.SiteID == s.ID {
				site.Hostnames = append(site.Hostnames, apitypes.SiteHostname{Hostname: h.Hostname, Service: h.ServiceName})
			}
		}
		out = append(out, site)
	}
	return out, nil
}

// domainScope finds a domain's service, environment and project by
// hostname, within the actor's org, and checks the actor's access.
func (c *Core) domainScope(ctx context.Context, a Actor, hostname string, need apitypes.Scope, action string) (store.DomainScopeRow, envScope, error) {
	if err := a.require(need, action); err != nil {
		return store.DomainScopeRow{}, envScope{}, err
	}
	host, err := normalizeHostname(hostname)
	if err != nil {
		return store.DomainScopeRow{}, envScope{}, err
	}
	d, err := c.store.DomainScope(ctx, store.DomainScopeParams{Hostname: host, OrgID: a.OrgID})
	if store.IsNotFound(err) {
		return d, envScope{}, notFound("no service has the domain %s; add it with `dock domain add %s` first", host, host)
	}
	if err != nil {
		return d, envScope{}, err
	}
	p, err := c.store.GetProject(ctx, d.ProjectID)
	if err != nil {
		return d, envScope{}, err
	}
	env, err := c.store.GetEnvironment(ctx, d.EnvironmentID)
	if err != nil {
		return d, envScope{}, err
	}
	return d, envScope{project: p, env: env}, a.checkEnv(need, action, p, env)
}

// EnableHostnameAnalytics turns analytics on for a domain's hostname: it
// joins req.Site, or the site of its service's other hostnames, or a new
// site named after the service's custom domain or it (newSiteName). The
// first site turns analytics on for the install.
// Pages then need the script: <script src="/_dock/a/s.js" defer></script>.
func (c *Core) EnableHostnameAnalytics(ctx context.Context, a Actor, hostname string, req apitypes.EnableAnalyticsRequest) (apitypes.Site, error) {
	d, es, err := c.domainScope(ctx, a, hostname, apitypes.ScopeDeploy, "turning analytics on for a domain")
	if err != nil {
		return apitypes.Site{}, err
	}
	if d.RedirectTo != "" {
		return apitypes.Site{}, invalid("%s redirects to %s; turn analytics on for that domain instead", d.Hostname, d.RedirectTo)
	}
	if c.analyticsOff() {
		return apitypes.Site{}, conflict("Dock Analytics is turned off on this platform; a platform operator turns it on in Console › Features (`dock analytics on`)")
	}
	if sh, err := c.store.GetSiteHostname(ctx, d.Hostname); err == nil {
		if site, err := c.store.GetSite(ctx, sh.SiteID); err == nil && site.ArchivedAt == nil {
			if site.EnvironmentID != es.env.ID {
				return apitypes.Site{}, conflict("%s collects for site %s in another environment; turn it off there first with `dock analytics disable %s`", d.Hostname, site.Name, d.Hostname)
			}
			return c.GetSite(ctx, a, es.project.Name, site.Name)
		}
	} else if !store.IsNotFound(err) {
		return apitypes.Site{}, err
	}
	t := now()
	var site store.Site
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		switch {
		case req.Site != "":
			site, err = q.GetSiteByName(ctx, store.GetSiteByNameParams{ProjectID: es.project.ID, Name: req.Site})
			if store.IsNotFound(err) || (err == nil && site.EnvironmentID != es.env.ID) {
				return notFound("site %q not found in %s; its hostnames must be in the same environment", req.Site, es)
			}
		default:
			site, err = q.SiteOfService(ctx, &d.ServiceID)
			if store.IsNotFound(err) || (err == nil && site.EnvironmentID != es.env.ID) {
				var name string
				if name, err = newSiteName(ctx, q, d.ServiceID, d.Hostname); err == nil {
					site, err = c.createSite(ctx, q, es.project.ID, es.env.ID, name, a.Name, t)
				}
			}
		}
		if err != nil {
			return err
		}
		if err := q.AddSiteHostname(ctx, store.AddSiteHostnameParams{Hostname: d.Hostname, SiteID: site.ID, ServiceID: &d.ServiceID, AddedAt: t}); err != nil {
			return err
		}
		return q.AddSiteMarker(ctx, store.AddSiteMarkerParams{SiteID: site.ID, At: t, Kind: "added", Message: d.Hostname + " added (" + d.ServiceName + ")"})
	})
	if err != nil {
		return apitypes.Site{}, err
	}
	if err := c.enableAnalytics(ctx, a.Name, nil); err != nil {
		return apitypes.Site{}, err
	}
	c.analyticsSitesChanged(ctx)
	if err := c.applyRoutes(ctx, d.ServerID); err != nil {
		c.log.Error("apply routes after turning analytics on", "domain", d.Hostname, "err", err)
	}
	c.audit(ctx, a, "analytics.enable", &es.project, &es.env, d.Hostname, "analytics on for "+d.Hostname+" (site "+site.Name+")")
	return c.GetSite(ctx, a, es.project.Name, site.Name)
}

// createSite makes a live site in an environment of a project, named
// name or, when the project has a live site called that, name-2, name-3:
// site names are unique in their project, as they address the site. Its
// public dashboard address is stats-<name>-<random> (newPublicSlug).
func (c *Core) createSite(ctx context.Context, q *store.Queries, projectID, envID int64, name, by string, t time.Time) (store.Site, error) {
	name, err := freeSiteName(ctx, q, projectID, name)
	if err != nil {
		return store.Site{}, err
	}
	slug, err := newPublicSlug(ctx, q, name)
	if err != nil {
		return store.Site{}, err
	}
	site, err := q.CreateSite(ctx, store.CreateSiteParams{EnvironmentID: envID, Name: name, PublicSlug: &slug, CreatedBy: by, CreatedAt: t})
	if err != nil {
		return site, err
	}
	// The org's defaults for new sites (ADR 0062) become the site's own
	// settings; the platform's defaults stay under them.
	p, err := q.GetProject(ctx, projectID)
	if err != nil {
		return site, err
	}
	od, err := q.GetOrgSiteDefaults(ctx, p.OrgID)
	if store.IsNotFound(err) {
		return site, nil
	}
	if err != nil {
		return site, err
	}
	if err := q.SetSiteSettings(ctx, store.SetSiteSettingsParams{Settings: od.Settings, ID: site.ID}); err != nil {
		return site, err
	}
	site.Settings = od.Settings
	return site, nil
}

// OrgSiteDefaults returns the org's defaults for new analytics sites:
// Own is what the org set, Effective the platform's defaults with them.
// Anyone in the org may read them.
func (c *Core) OrgSiteDefaults(ctx context.Context, a Actor) (apitypes.OrgSiteDefaults, error) {
	if err := a.require(apitypes.ScopeRead, "reading the analytics defaults"); err != nil {
		return apitypes.OrgSiteDefaults{}, err
	}
	return c.orgSiteDefaults(ctx, a.OrgID)
}

// orgSiteDefaults reads an org's site defaults over the platform's.
func (c *Core) orgSiteDefaults(ctx context.Context, orgID int64) (apitypes.OrgSiteDefaults, error) {
	platform, err := c.siteDefaults(ctx)
	if err != nil {
		return apitypes.OrgSiteDefaults{}, err
	}
	var own apitypes.SiteSettings
	od, err := c.store.GetOrgSiteDefaults(ctx, orgID)
	if err == nil {
		if err := json.Unmarshal([]byte(od.Settings), &own); err != nil {
			return apitypes.OrgSiteDefaults{}, err
		}
	} else if !store.IsNotFound(err) {
		return apitypes.OrgSiteDefaults{}, err
	}
	return apitypes.OrgSiteDefaults{Own: own, Effective: mergeSettings(platform, own)}, nil
}

// SetOrgSiteDefaults changes the org's defaults for new sites (existing
// sites keep their settings); reset names settings to set back to the
// platform's default. The org's unlimited admins.
func (c *Core) SetOrgSiteDefaults(ctx context.Context, a Actor, set apitypes.SiteSettings, reset []string) (apitypes.OrgSiteDefaults, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "changing the analytics defaults"); err != nil {
		return apitypes.OrgSiteDefaults{}, err
	}
	if set.PublicDomain != nil {
		return apitypes.OrgSiteDefaults{}, invalid("a public domain belongs to one site; set it on the site with `dock analytics site set SITE --public-domain HOST`")
	}
	cur, err := c.orgSiteDefaults(ctx, a.OrgID)
	if err != nil {
		return apitypes.OrgSiteDefaults{}, err
	}
	own := mergeSettings(cur.Own, set)
	if err := resetSettings(&own, reset); err != nil {
		return apitypes.OrgSiteDefaults{}, err
	}
	if err := checkSettings(own); err != nil {
		return apitypes.OrgSiteDefaults{}, err
	}
	b, err := json.Marshal(own)
	if err != nil {
		return apitypes.OrgSiteDefaults{}, err
	}
	if err := c.store.SetOrgSiteDefaults(ctx, store.SetOrgSiteDefaultsParams{OrgID: a.OrgID, Settings: string(b), UpdatedBy: a.Name, UpdatedAt: now()}); err != nil {
		return apitypes.OrgSiteDefaults{}, err
	}
	c.audit(ctx, a, "analytics.defaults", nil, nil, "analytics defaults", "changed the organization's analytics defaults for new sites")
	return c.orgSiteDefaults(ctx, a.OrgID)
}

// freeSiteName is name, or name-2, name-3..., whichever no live site of
// the project has.
func freeSiteName(ctx context.Context, q *store.Queries, projectID int64, name string) (string, error) {
	for i := 1; ; i++ {
		candidate := name
		if i > 1 {
			candidate += "-" + strconv.Itoa(i)
		}
		if _, err := q.GetSiteByName(ctx, store.GetSiteByNameParams{ProjectID: projectID, Name: candidate}); store.IsNotFound(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
}

// DisableHostnameAnalytics turns analytics off for a hostname. Its site
// keeps its history; a site left without hostnames is archived.
func (c *Core) DisableHostnameAnalytics(ctx context.Context, a Actor, hostname string) error {
	host, err := normalizeHostname(hostname)
	if err != nil {
		return err
	}
	sh, err := c.store.GetSiteHostname(ctx, host)
	if store.IsNotFound(err) {
		return notFound("%s doesn't collect analytics", host)
	}
	if err != nil {
		return err
	}
	site, err := c.store.GetSite(ctx, sh.SiteID)
	if err != nil {
		return err
	}
	env, err := c.store.GetEnvironment(ctx, site.EnvironmentID)
	if err != nil {
		return err
	}
	p, err := c.store.GetProject(ctx, env.ProjectID)
	if err != nil || p.OrgID != a.OrgID {
		return notFound("%s doesn't collect analytics", host)
	}
	if err := a.require(apitypes.ScopeDeploy, "turning analytics off for a domain"); err != nil {
		return err
	}
	if err := a.checkEnv(apitypes.ScopeDeploy, "turning analytics off for a domain", p, env); err != nil {
		return err
	}
	t := now()
	var renamed string
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.RemoveSiteHostname(ctx, host); err != nil {
			return err
		}
		// Named after host, it takes another hostname's name.
		var err error
		if renamed, err = renameSiteAfter(ctx, q, p.ID, site, host); err != nil {
			return err
		}
		if err := q.AddSiteMarker(ctx, store.AddSiteMarkerParams{SiteID: site.ID, At: t, Kind: "removed", Message: renamedMarker(host+" removed", renamed)}); err != nil {
			return err
		}
		n, err := q.CountSiteHostnames(ctx, site.ID)
		if err == nil && n == 0 {
			err = q.ArchiveSite(ctx, store.ArchiveSiteParams{ArchivedAt: &t, ID: site.ID})
		}
		return err
	})
	if err != nil {
		return err
	}
	c.analyticsSitesChanged(ctx)
	if err := c.applyRoutes(ctx, env.ServerID); err != nil {
		c.log.Error("apply routes after turning analytics off", "domain", host, "err", err)
	}
	summary := "analytics off for " + host + " (site " + site.Name + ")"
	if renamed != "" {
		summary += "; the site is now " + renamed
	}
	c.audit(ctx, a, "analytics.disable", &p, &env, host, summary)
	return nil
}

// siteDomainAdded carries a hostname's analytics over when its domain is
// added back, on this or another service (AddDomain). keep (the default)
// leaves it in its site and marks a move to another service; new moves it
// to a new site, archiving the old one if it is left without hostnames. A
// site in another environment comes along only when this was its only
// hostname and the actor may read it there; otherwise the hostname starts
// a new site. It reports whether the hostname's site changed.
func (c *Core) siteDomainAdded(ctx context.Context, a Actor, ss svcScope, host, mode string) (bool, error) {
	sh, err := c.store.GetSiteHostname(ctx, host)
	if store.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	site, err := c.store.GetSite(ctx, sh.SiteID)
	if err != nil {
		return false, err
	}
	t := now()
	sameService := sh.ServiceID != nil && *sh.ServiceID == ss.svc.ID
	keepHere := mode != "new" && site.ArchivedAt == nil
	if keepHere && site.EnvironmentID != ss.env.ID {
		n, err := c.store.CountSiteHostnames(ctx, site.ID)
		if err != nil {
			return false, err
		}
		old, err := c.store.GetEnvironment(ctx, site.EnvironmentID)
		if err != nil {
			return false, err
		}
		oldProject, err := c.store.GetProject(ctx, old.ProjectID)
		if err != nil {
			return false, err
		}
		keepHere = n == 1 && oldProject.OrgID == a.OrgID && a.checkEnv(apitypes.ScopeRead, "moving a site", oldProject, old) == nil
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if keepHere {
			if site.EnvironmentID != ss.env.ID {
				if err := q.SetSiteEnvironment(ctx, store.SetSiteEnvironmentParams{EnvironmentID: ss.env.ID, ID: site.ID}); err != nil {
					return err
				}
				// Into another project, where its name may be taken.
				if other, err := q.GetSiteByName(ctx, store.GetSiteByNameParams{ProjectID: ss.project.ID, Name: site.Name}); err == nil && other.ID != site.ID {
					name, err := freeSiteName(ctx, q, ss.project.ID, site.Name)
					if err != nil {
						return err
					}
					if err := q.RenameSite(ctx, store.RenameSiteParams{Name: name, ID: site.ID}); err != nil {
						return err
					}
				}
			}
			if sameService {
				return nil
			}
			if err := q.SetSiteHostnameService(ctx, store.SetSiteHostnameServiceParams{ServiceID: &ss.svc.ID, Hostname: host}); err != nil {
				return err
			}
			return q.AddSiteMarker(ctx, store.AddSiteMarkerParams{SiteID: site.ID, At: t, Kind: "moved", Message: host + " moved to " + ss.svc.Name})
		}
		if err := q.RemoveSiteHostname(ctx, host); err != nil {
			return err
		}
		if site.ArchivedAt == nil {
			// Named after host, the old site takes another hostname's name.
			oldEnv, err := q.GetEnvironment(ctx, site.EnvironmentID)
			if err != nil {
				return err
			}
			renamed, err := renameSiteAfter(ctx, q, oldEnv.ProjectID, site, host)
			if err != nil {
				return err
			}
			if err := q.AddSiteMarker(ctx, store.AddSiteMarkerParams{SiteID: site.ID, At: t, Kind: "removed", Message: renamedMarker(host+" moved to a new site", renamed)}); err != nil {
				return err
			}
			if n, err := q.CountSiteHostnames(ctx, site.ID); err != nil {
				return err
			} else if n == 0 {
				if err := q.ArchiveSite(ctx, store.ArchiveSiteParams{ArchivedAt: &t, ID: site.ID}); err != nil {
					return err
				}
			}
		}
		name, err := newSiteName(ctx, q, ss.svc.ID, host)
		if err != nil {
			return err
		}
		fresh, err := c.createSite(ctx, q, ss.project.ID, ss.env.ID, name, a.Name, t)
		if err != nil {
			return err
		}
		if err := q.AddSiteHostname(ctx, store.AddSiteHostnameParams{Hostname: host, SiteID: fresh.ID, ServiceID: &ss.svc.ID, AddedAt: t}); err != nil {
			return err
		}
		return q.AddSiteMarker(ctx, store.AddSiteMarkerParams{SiteID: fresh.ID, At: t, Kind: "added", Message: host + " added (" + ss.svc.Name + ")"})
	})
	if err != nil {
		return false, err
	}
	return !keepHere || site.EnvironmentID != ss.env.ID, nil
}

// UpdateSite renames a site or changes its settings. Its public
// dashboard address stays as it was made (ADR 0087), so a link to it
// keeps working.
func (c *Core) UpdateSite(ctx context.Context, a Actor, project, name string, req apitypes.UpdateSiteRequest) (apitypes.Site, error) {
	s, err := c.loadSite(ctx, a, project, name, apitypes.ScopeDeploy, "changing a site")
	if err != nil {
		return apitypes.Site{}, err
	}
	o := mergeSettings(siteOverrides(s.site), req.Settings)
	if err := resetSettings(&o, req.Reset); err != nil {
		return apitypes.Site{}, err
	}
	if err := checkSettings(o); err != nil {
		return apitypes.Site{}, err
	}
	newName := s.site.Name
	if req.Name != "" && req.Name != s.site.Name {
		if len(req.Name) > 253 || strings.ContainsAny(req.Name, "/ \t\n") {
			return apitypes.Site{}, invalid("site name %q may not contain spaces or slashes (at most 253 characters)", req.Name)
		}
		if _, err := c.store.GetSiteByName(ctx, store.GetSiteByNameParams{ProjectID: s.project.ID, Name: req.Name}); err == nil {
			return apitypes.Site{}, conflict("project %s already has a site called %s", s.project.Name, req.Name)
		}
		newName = req.Name
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if o.PublicDomain != nil {
			host, err := c.checkPublicDomain(ctx, q, s.site.ID, *o.PublicDomain)
			if err != nil {
				return err
			}
			o.PublicDomain = &host
		}
		b, err := json.Marshal(o)
		if err != nil {
			return err
		}
		if err := q.SetSiteSettings(ctx, store.SetSiteSettingsParams{Settings: string(b), ID: s.site.ID}); err != nil {
			return err
		}
		if newName == s.site.Name {
			return nil
		}
		return q.RenameSite(ctx, store.RenameSiteParams{Name: newName, ID: s.site.ID})
	})
	if err != nil {
		return apitypes.Site{}, err
	}
	c.analyticsSitesChanged(ctx)
	c.audit(ctx, a, "analytics.site", &s.project, &s.env, "site "+newName, "changed site "+newName)
	return c.GetSite(ctx, a, project, newName)
}

// SiteDefaults returns the platform's site defaults (operators): what
// every site gets unless its org or the site itself sets otherwise.
func (c *Core) SiteDefaults(ctx context.Context, a Actor) (apitypes.SiteSettings, error) {
	if err := a.requirePlatform(PlatformView, "reading the analytics defaults"); err != nil {
		return apitypes.SiteSettings{}, err
	}
	return c.siteDefaults(ctx)
}

// SetSiteDefaults changes the install's site defaults (operators); reset
// names settings to set back to the built-in default.
func (c *Core) SetSiteDefaults(ctx context.Context, a Actor, set apitypes.SiteSettings, reset []string) (apitypes.SiteSettings, error) {
	if err := a.requirePlatform(PlatformSettings, "changing the analytics defaults"); err != nil {
		return apitypes.SiteSettings{}, err
	}
	var cur apitypes.SiteSettings
	if st, err := c.store.GetAnalyticsSettings(ctx); err == nil {
		_ = json.Unmarshal([]byte(st.SiteDefaults), &cur)
	} else if !store.IsNotFound(err) {
		return apitypes.SiteSettings{}, err
	}
	if set.PublicDomain != nil {
		return apitypes.SiteSettings{}, invalid("a public domain belongs to one site; set it on the site with `dock analytics site set SITE --public-domain HOST`")
	}
	cur = mergeSettings(cur, set)
	if err := resetSettings(&cur, reset); err != nil {
		return apitypes.SiteSettings{}, err
	}
	if err := checkSettings(cur); err != nil {
		return apitypes.SiteSettings{}, err
	}
	b, err := json.Marshal(cur)
	if err != nil {
		return apitypes.SiteSettings{}, err
	}
	// The row exists once analytics was ever on; defaults set before
	// that make it (off).
	sealed, err := c.box.Seal([]byte(newAnalyticsToken()), analyticsTokenAAD)
	if err != nil {
		return apitypes.SiteSettings{}, err
	}
	t := now()
	if err := c.store.CreateAnalyticsSettings(ctx, store.CreateAnalyticsSettingsParams{Token: sealed, UpdatedBy: a.Name, UpdatedAt: t}); err != nil {
		return apitypes.SiteSettings{}, err
	}
	if err := c.store.SetAnalyticsSiteDefaults(ctx, store.SetAnalyticsSiteDefaultsParams{SiteDefaults: string(b), UpdatedBy: a.Name, UpdatedAt: t}); err != nil {
		return apitypes.SiteSettings{}, err
	}
	c.analyticsSitesChanged(ctx)
	c.audit(ctx, a.platform(), "analytics.defaults", nil, nil, "analytics defaults", "changed the analytics defaults for sites")
	return c.siteDefaults(ctx)
}
