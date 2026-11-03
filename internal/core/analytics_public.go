package core

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Public dashboards (ADR 0037, phase A4). A site with its public setting
// on has a read-only dashboard at stats-<site>.<base domain> and at its
// own public_domain, if set. Both are one live site's across every org
// (unique indexes), so no org can take or shadow another's. The control
// plane's Caddy routes those hostnames to [analytics] stats_listen, a
// listener serving nothing else (api.PublicStats), with certificates on
// demand like Dock's address. The page shows only the site's aggregates:
// no services, releases, visitor IDs or replays, and error groups
// (without stacks) only when the site says so.
//
// The control plane keeps which hostname is which site in memory, built
// from the sites when it starts and whenever a site's settings change:
// one map, as many entries as public hostnames.

// publicIndex maps public dashboard hostnames to their sites. gen counts
// changes to sites' settings, which may change what a dashboard shows.
type publicIndex struct {
	hosts atomic.Pointer[map[string]int64]
	gen   atomic.Int64
}

// publicSlug is a site's name as a hostname label: "shop.example.com" is
// "shop-example-com", and a name with no letters or digits is "site". A
// site holds its label (sites.public_slug), unique across every org, so
// its address is its own: see freePublicSlug.
func publicSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	slug := b.String()
	if len(slug) > 50 {
		slug = slug[:50]
	}
	if slug = strings.TrimSuffix(slug, "-"); slug == "" {
		return "site"
	}
	return slug
}

// freePublicSlug is slug, or slug-2, slug-3..., whichever no live site
// but siteID (0 for one being made) holds in any org: a site made, or
// given an address from before they were stored, never takes another's.
func freePublicSlug(ctx context.Context, q *store.Queries, siteID int64, slug string) (string, error) {
	for i := 1; ; i++ {
		candidate := slug
		if i > 1 {
			candidate += "-" + strconv.Itoa(i)
		}
		switch holder, err := q.PublicSlugHolder(ctx, &candidate); {
		case store.IsNotFound(err) || (err == nil && holder == siteID):
			return candidate, nil
		case err != nil:
			return "", err
		}
	}
}

// assignPublicSlugs gives the live sites from before public addresses
// were stored (migration 0037) theirs, oldest first, so of two that
// share one the oldest keeps it. Run when analytics starts; one query
// once every site has its address.
func (c *Core) assignPublicSlugs(ctx context.Context) error {
	sites, err := c.store.ListSitesWithoutPublicSlug(ctx)
	if err != nil || len(sites) == 0 {
		return err
	}
	return c.store.Tx(ctx, func(q *store.Queries) error {
		for _, s := range sites {
			slug, err := freePublicSlug(ctx, q, s.ID, publicSlug(s.Name))
			if err != nil {
				return err
			}
			if err := q.SetSitePublicSlug(ctx, store.SetSitePublicSlugParams{PublicSlug: &slug, ID: s.ID}); err != nil {
				return err
			}
		}
		return nil
	})
}

// publicBase is the base domain public dashboards are under: the
// control plane's server's, "" for none.
func (c *Core) publicBase(ctx context.Context) (string, error) {
	srv, err := c.store.GetLocalServer(ctx)
	if err != nil {
		return "", err
	}
	return c.ingressOf(srv).baseDomain, nil
}

// publicHosts are a site's public dashboard hostnames under its address
// (sites.public_slug), its settings and the base domain: none while it
// isn't public.
func (c *Core) publicHosts(base string, slug *string, set apitypes.SiteSettings) []string {
	if set.Public == nil || !*set.Public || c.cfg.Analytics.StatsListen == "" {
		return nil
	}
	var out []string
	if base != "" && slug != nil && *slug != "" {
		out = append(out, "stats-"+*slug+"."+base)
	}
	if set.PublicDomain != nil && *set.PublicDomain != "" {
		out = append(out, *set.PublicDomain)
	}
	return out
}

// publicSite is the site a public dashboard hostname shows, 0 for none.
func (c *Core) publicSite(host string) int64 {
	m := c.analytics.public.hosts.Load()
	if m == nil {
		return 0
	}
	return (*m)[host]
}

// PublicDashboard says whether host is a public dashboard, and the
// generation of the sites' settings: a page read under an older one may
// show what a site no longer allows.
func (c *Core) PublicDashboard(host string) (gen int64, ok bool) {
	return c.analytics.public.gen.Load(), c.publicSite(host) != 0
}

// publicSitesChanged rebuilds the public hostnames from the sites and,
// when they changed, routes them on the control plane's server.
func (c *Core) publicSitesChanged(ctx context.Context) {
	c.analytics.public.gen.Add(1)
	hosts, err := c.loadPublicHosts(ctx)
	if err != nil {
		c.log.Error("analytics: read the public dashboards; they stay as they were", "err", err)
		return
	}
	old := c.analytics.public.hosts.Swap(&hosts)
	if old != nil && maps.Equal(*old, hosts) {
		return
	}
	if err := c.applyRoutes(ctx, c.local.ID); err != nil {
		c.log.Error("analytics: route the public dashboards", "err", err)
	}
}

// loadPublicHosts reads every live site's public hostnames. Addresses
// and public domains are each one site's (unique indexes); a public
// domain that became a stats-<site> address when the base domain changed
// stays the older site's.
func (c *Core) loadPublicHosts(ctx context.Context) (map[string]int64, error) {
	hosts := map[string]int64{}
	if c.cfg.Analytics.StatsListen == "" {
		return hosts, nil
	}
	sites, err := c.store.ListPublicSites(ctx)
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
		set := mergeSettings(defaults, siteOverrides(store.Site{Settings: s.Settings}))
		for _, h := range c.publicHosts(base, s.PublicSlug, set) {
			if _, taken := hosts[h]; !taken {
				hosts[h] = s.ID
			}
		}
	}
	return hosts, nil
}

// checkPublicDomain normalizes a site's public domain ("" for none) and
// refuses one that something else has: an app's domain, Dock's address or
// webhook hostname, a public dashboard's address (stats-<site>.<base
// domain>), or another site's public domain, shown or not, in any org.
// q is the transaction the domain is saved in, so no other site takes it
// meanwhile.
func (c *Core) checkPublicDomain(ctx context.Context, q *store.Queries, siteID int64, host string) (string, error) {
	if host == "" {
		return "", nil
	}
	h, err := normalizeHostname(host)
	if err != nil {
		return "", invalid("public domain %q isn't a hostname, like stats.example.com", host)
	}
	switch _, err := q.GetDomainByHostname(ctx, h); {
	case err == nil:
		return "", errf(apitypes.CodeHostnameTaken, "%s is a service's domain; a public dashboard needs a hostname of its own", h)
	case !store.IsNotFound(err):
		return "", err
	}
	if h == c.cfg.Hooks.PublicHost || h == c.publicHost() {
		return "", errf(apitypes.CodeHostnameTaken, "%s is Dock's own hostname; use another one", h)
	}
	base, err := c.publicBase(ctx)
	if err != nil {
		return "", err
	}
	if label, ok := strings.CutSuffix(h, "."+base); base != "" && ok && strings.HasPrefix(label, "stats-") {
		return "", errf(apitypes.CodeHostnameTaken, "%s is a Dock address for public dashboards; use a hostname of your own, like stats.example.com", h)
	}
	switch holder, err := q.PublicDomainHolder(ctx, h); {
	case err == nil && holder != siteID:
		return "", errf(apitypes.CodeHostnameTaken, "%s is another site's public domain; use another hostname", h)
	case err != nil && !store.IsNotFound(err):
		return "", err
	}
	if other := c.publicSite(h); other != 0 && other != siteID {
		return "", errf(apitypes.CodeHostnameTaken, "%s is another site's public dashboard", h)
	}
	return h, nil
}

// PublicRanges are the ranges a public dashboard offers.
var PublicRanges = []string{"7d", "30d", "12m"}

// publicDimensions are the breakdowns a public dashboard shows (pages,
// sources, countries): what visitors see of a site anyway, nothing about
// how Dock runs it.
var publicDimensions = []analytics.Dimension{analytics.DimPage, analytics.DimReferrer, analytics.DimCountry}

// publicRows is how many values each public breakdown shows.
const publicRows = 8

// PublicStats is the public dashboard of the site a hostname shows, for
// a range (7d, 30d or 12m; 30d if none). Anyone may read it: it holds only the
// site's aggregates. CodeNotFound when the hostname shows no site (any
// more); a store that can't be reached gives StoreOnline false.
func (c *Core) PublicStats(ctx context.Context, host, rangeName string) (apitypes.PublicStats, error) {
	id := c.publicSite(host)
	if id == 0 {
		return apitypes.PublicStats{}, notFound("no public dashboard here")
	}
	if rangeName == "" {
		rangeName = "30d"
	}
	if !slices.Contains(PublicRanges, rangeName) {
		return apitypes.PublicStats{}, invalid("range is one of 7d, 30d, 12m")
	}
	site, err := c.store.GetSite(ctx, id)
	if err != nil {
		return apitypes.PublicStats{}, err
	}
	defaults, err := c.siteDefaults(ctx)
	if err != nil {
		return apitypes.PublicStats{}, err
	}
	set := mergeSettings(defaults, siteOverrides(site))
	if site.ArchivedAt != nil || !*set.Public {
		return apitypes.PublicStats{}, notFound("no public dashboard here")
	}
	t := now()
	from, to, _ := StatsQuery{Range: rangeName}.span(t)
	out := apitypes.PublicStats{Site: site.Name, Range: rangeName, From: from, To: to, Series: []apitypes.AnalyticsPoint{},
		Breakdowns: map[string][]apitypes.PublicRow{}, Errors: []apitypes.PublicError{}, GeneratedAt: t.UTC()}
	cl, _ := c.analyticsClient()
	if cl == nil {
		out.StoreError = "analytics is off"
		return out, nil
	}
	q := analytics.Query{Site: id, From: from, To: to}
	prev := q
	prev.From, prev.To = from-(to-from), from
	o, err := cl.Overview(ctx, q)
	var p analytics.Overview
	if err == nil {
		p, err = cl.Overview(ctx, prev)
	}
	var pts []analytics.Point
	if err == nil {
		pts, err = cl.Series(ctx, analytics.SeriesQuery{Query: q, Bucket: analytics.BucketDay})
	}
	for _, d := range publicDimensions {
		if err != nil {
			break
		}
		var rows []analytics.Row
		rows, err = cl.Breakdown(ctx, analytics.BreakdownQuery{Query: q, Dimension: d, Limit: publicRows})
		out.Breakdowns[string(d)] = make([]apitypes.PublicRow, 0, len(rows))
		for _, r := range rows {
			out.Breakdowns[string(d)] = append(out.Breakdowns[string(d)], apitypes.PublicRow{Value: r.Value, Visitors: r.Visitors, Count: r.Count})
		}
	}
	if err == nil && *set.PublicErrors {
		var res analytics.ErrorsResult
		res, err = cl.Errors(ctx, analytics.ErrorsQuery{Query: q, Limit: publicRows})
		for _, g := range res.Groups {
			out.Errors = append(out.Errors, apitypes.PublicError{Type: g.Type, Message: g.Message, Count: g.Count, Visitors: g.Visitors,
				LastSeen: time.UnixMilli(g.LastSeen).UTC()})
		}
	}
	if err == nil {
		var live analytics.Live
		live, err = cl.Live(ctx, analytics.LiveQuery{Site: id, Since: t.Add(-liveWindow).UnixMilli()})
		out.LiveVisitors = len(live.Visitors)
	}
	if out.StoreOnline, out.StoreError, err = storeStatus(err); err != nil || !out.StoreOnline {
		return out, err
	}
	out.Overview, out.Previous = toOverview(o), toOverview(p)
	for _, pt := range pts {
		out.Series = append(out.Series, apitypes.AnalyticsPoint(pt))
	}
	out.ShowErrors = *set.PublicErrors
	return out, nil
}

// SitePublic is where a site's public dashboard is and whether each
// address works yet: DNS, then the certificate, checked now (a lookup and
// a TLS handshake per address). A domain of the site's own is best a
// CNAME to its Dock address, when there is one. Read permission.
func (c *Core) SitePublic(ctx context.Context, a Actor, project, name string) (apitypes.SitePublic, error) {
	s, err := c.loadSite(ctx, a, project, name, apitypes.ScopeRead, "reading a site's public dashboard")
	if err != nil {
		return apitypes.SitePublic{}, err
	}
	defaults, err := c.siteDefaults(ctx)
	if err != nil {
		return apitypes.SitePublic{}, err
	}
	srv, err := c.store.GetLocalServer(ctx)
	if err != nil {
		return apitypes.SitePublic{}, err
	}
	ing := c.ingressOf(srv)
	set := mergeSettings(defaults, siteOverrides(s.site))
	out := apitypes.SitePublic{Public: *set.Public, Addresses: []apitypes.PublicAddress{}}
	hosts := c.publicHosts(ing.baseDomain, s.site.PublicSlug, set)
	if s.site.ArchivedAt != nil {
		hosts = nil
	}
	for _, h := range hosts {
		own := set.PublicDomain != nil && h == *set.PublicDomain
		addr := apitypes.PublicAddress{Hostname: h, URL: c.controlURL(h), Own: own, DNSRecords: hostRecords(h, ing)}
		if own && len(hosts) > 1 {
			addr.DNSRecords = append([]apitypes.DNSRecord{{Type: "CNAME", Name: h, Value: hosts[0]}}, addr.DNSRecords...)
		}
		addr.Status, addr.StatusReason, _ = c.checkHost(ctx, h, ing)
		out.Addresses = append(out.Addresses, addr)
	}
	return out, nil
}
