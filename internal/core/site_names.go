package core

import (
	"context"

	"github.com/NordTempo/tempoDock/internal/store"
)

// Automatic site names. A site Dock makes is named after a hostname: the
// service's custom domain when it has one, otherwise the hostname
// analytics was turned on for. When that hostname goes away (its domain
// removed, or analytics turned off for it), the site takes the name of
// another of its hostnames that is still a domain, custom ones first. A
// name someone gave the site by hand is no hostname that went away, so it
// stays as it is.

// newSiteName is the name of a new site for host, a domain of service:
// host if it is a custom domain, else the service's first verified custom
// domain that doesn't redirect, else host (a generated one).
func newSiteName(ctx context.Context, q *store.Queries, service int64, host string) (string, error) {
	doms, err := q.ListDomainsByService(ctx, service)
	if err != nil {
		return "", err
	}
	for _, d := range doms {
		if d.Hostname == host && !d.Generated {
			return host, nil
		}
	}
	for _, d := range doms {
		if !d.Generated && d.RedirectTo == "" && d.VerifiedAt != nil {
			return d.Hostname, nil
		}
	}
	return host, nil
}

// renameSiteAfter renames site, named after host, which just left it or
// stopped being a domain, after its best other live hostname
// (SiteNameCandidate), in q's transaction. It returns the new name: ""
// when the site isn't named after host (a name given by hand) or has no
// other live hostname. Its public address stays (ADR 0087).
func renameSiteAfter(ctx context.Context, q *store.Queries, project int64, site store.Site, host string) (string, error) {
	if site.Name != host {
		return "", nil
	}
	better, err := q.SiteNameCandidate(ctx, site.ID)
	if store.IsNotFound(err) || (err == nil && better == host) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	name, err := freeSiteName(ctx, q, project, better)
	if err != nil {
		return "", err
	}
	return name, q.RenameSite(ctx, store.RenameSiteParams{Name: name, ID: site.ID})
}

// renamedMarker is a site marker's message for host leaving, saying the
// site's new name when it took one.
func renamedMarker(what, to string) string {
	if to == "" {
		return what
	}
	return what + "; the site is now " + to
}

// siteDomainRemoved renames the project's site named after host, a
// domain just removed (RemoveDomain), and marks it on the site's charts.
// The hostname stays in the site (its history comes back with the
// domain). Failures are logged; the name is cosmetic.
func (c *Core) siteDomainRemoved(ctx context.Context, a Actor, p store.Project, e store.Environment, host string) {
	var to string
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		site, err := q.GetSiteByName(ctx, store.GetSiteByNameParams{ProjectID: p.ID, Name: host})
		if store.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if to, err = renameSiteAfter(ctx, q, p.ID, site, host); err != nil || to == "" {
			return err
		}
		return q.AddSiteMarker(ctx, store.AddSiteMarkerParams{SiteID: site.ID, At: now(), Kind: "removed", Message: renamedMarker(host+" removed", to)})
	})
	if err != nil {
		c.log.Error("analytics: rename a site after its removed domain", "site", host, "err", err)
		return
	}
	if to != "" {
		c.analyticsSitesChanged(ctx)
		c.audit(ctx, a, "analytics.site", &p, &e, "site "+to, "renamed site "+host+" to "+to+": "+host+" was removed")
	}
}

// renameSitesAfterGoneHostnames catches up the sites named after a
// hostname that went away before Dock renamed sites itself. Run when
// analytics starts: one query, and nothing to do once every site is
// named after a live hostname or has none left.
func (c *Core) renameSitesAfterGoneHostnames(ctx context.Context) error {
	rows, err := c.store.ListSitesNamedAfterGoneHostnames(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Better == "" {
			continue
		}
		var name string
		err := c.store.Tx(ctx, func(q *store.Queries) error {
			var err error
			if name, err = freeSiteName(ctx, q, r.ProjectID, r.Better); err != nil {
				return err
			}
			return q.RenameSite(ctx, store.RenameSiteParams{Name: name, ID: r.ID})
		})
		if err != nil {
			return err
		}
		c.log.Info("analytics: renamed a site after a live hostname", "site", r.Name, "to", name)
	}
	return nil
}
