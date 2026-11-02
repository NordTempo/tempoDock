package core

import (
	"context"
	"encoding/json/v2"
	"net/netip"
	"strconv"
	"strings"
	"time"

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
	paths, ips, domain := []string{}, []string{}, ""
	return apitypes.SiteSettings{
		Replay: &f, ReplaySampleRate: &sample, MaskAllText: &f, ErrorsAndVitals: &t,
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
		Replay: over(base.Replay, o.Replay), ReplaySampleRate: over(base.ReplaySampleRate, o.ReplaySampleRate),
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
		sites = append(sites, store.Site{ID: r.ID, EnvironmentID: r.EnvironmentID, Name: r.Name, Settings: r.Settings, ArchivedAt: r.ArchivedAt, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt})
		envs[r.EnvironmentID] = r.EnvironmentName
	}
	return c.toSites(ctx, sites, envs)
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
		if s.ArchivedAt == nil {
			for _, h := range c.publicHosts(base, s.Name, site.Settings) {
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
	d, err := c.store.DomainScope(ctx, host)
	if store.IsNotFound(err) || (err == nil && d.OrgID != a.OrgID) {
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
// site named after it. The first site turns analytics on for the install.
// Pages then need the script: <script src="/_dock/a/s.js" defer></script>.
func (c *Core) EnableHostnameAnalytics(ctx context.Context, a Actor, hostname string, req apitypes.EnableAnalyticsRequest) (apitypes.Site, error) {
	d, es, err := c.domainScope(ctx, a, hostname, apitypes.ScopeDeploy, "turning analytics on for a domain")
	if err != nil {
		return apitypes.Site{}, err
	}
	if d.RedirectTo != "" {
		return apitypes.Site{}, invalid("%s redirects to %s; turn analytics on for that domain instead", d.Hostname, d.RedirectTo)
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
				site, err = c.createSite(ctx, q, es.project.ID, es.env.ID, d.Hostname, a.Name, t)
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
// site names are unique in their project, as they address the site.
func (c *Core) createSite(ctx context.Context, q *store.Queries, projectID, envID int64, name, by string, t time.Time) (store.Site, error) {
	name, err := freeSiteName(ctx, q, projectID, name)
	if err != nil {
		return store.Site{}, err
	}
	return q.CreateSite(ctx, store.CreateSiteParams{EnvironmentID: envID, Name: name, CreatedBy: by, CreatedAt: t})
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
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.RemoveSiteHostname(ctx, host); err != nil {
			return err
		}
		if err := q.AddSiteMarker(ctx, store.AddSiteMarkerParams{SiteID: site.ID, At: t, Kind: "removed", Message: host + " removed"}); err != nil {
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
	c.audit(ctx, a, "analytics.disable", &p, &env, host, "analytics off for "+host+" (site "+site.Name+")")
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
			if err := q.AddSiteMarker(ctx, store.AddSiteMarkerParams{SiteID: site.ID, At: t, Kind: "removed", Message: host + " moved to a new site"}); err != nil {
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
		fresh, err := c.createSite(ctx, q, ss.project.ID, ss.env.ID, host, a.Name, t)
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

// UpdateSite renames a site or changes its settings.
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
	if o.PublicDomain != nil {
		host, err := c.checkPublicDomain(ctx, s.site.ID, *o.PublicDomain)
		if err != nil {
			return apitypes.Site{}, err
		}
		o.PublicDomain = &host
	}
	b, err := json.Marshal(o)
	if err != nil {
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
		if err := q.SetSiteSettings(ctx, store.SetSiteSettingsParams{Settings: string(b), ID: s.site.ID}); err != nil {
			return err
		}
		if newName != s.site.Name {
			return q.RenameSite(ctx, store.RenameSiteParams{Name: newName, ID: s.site.ID})
		}
		return nil
	})
	if err != nil {
		return apitypes.Site{}, err
	}
	c.analyticsSitesChanged(ctx)
	c.audit(ctx, a, "analytics.site", &s.project, &s.env, "site "+newName, "changed site "+newName)
	return c.GetSite(ctx, a, project, newName)
}

// SiteDefaults returns the install's site defaults (operators).
func (c *Core) SiteDefaults(ctx context.Context, a Actor) (apitypes.SiteSettings, error) {
	if err := a.requireOperator("reading the analytics defaults"); err != nil {
		return apitypes.SiteSettings{}, err
	}
	return c.siteDefaults(ctx)
}

// SetSiteDefaults changes the install's site defaults (operators); reset
// names settings to set back to the built-in default.
func (c *Core) SetSiteDefaults(ctx context.Context, a Actor, set apitypes.SiteSettings, reset []string) (apitypes.SiteSettings, error) {
	if err := a.requireOperator("changing the analytics defaults"); err != nil {
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
	sealed, err := c.box.Seal([]byte(newAnalyticsToken()))
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
