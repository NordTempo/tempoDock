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

// renameSiteNamedAfter renames the project's live site named host, which
// just went away, after another of its hostnames (SiteNameCandidate).
// only, when set, is the one site host belonged to: turning analytics off
// for a hostname leaves alone another site that happens to be named
// after it. Failures are logged; the name is cosmetic.
func (c *Core) renameSiteNamedAfter(ctx context.Context, a Actor, p store.Project, e store.Environment, host string, only int64) {
	var to string
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		site, err := q.GetSiteByName(ctx, store.GetSiteByNameParams{ProjectID: p.ID, Name: host})
		if store.IsNotFound(err) || (err == nil && only != 0 && site.ID != only) {
			return nil
		}
		if err != nil {
			return err
		}
		better, err := q.SiteNameCandidate(ctx, site.ID)
		if store.IsNotFound(err) || (err == nil && better == host) {
			return nil
		}
		if err != nil {
			return err
		}
		if to, err = freeSiteName(ctx, q, p.ID, better); err != nil {
			return err
		}
		return q.RenameSite(ctx, store.RenameSiteParams{Name: to, ID: site.ID})
	})
	if err != nil {
		c.log.Error("analytics: rename a site after its hostname", "site", host, "err", err)
		return
	}
	if to == "" {
		return
	}
	c.analyticsSitesChanged(ctx)
	c.audit(ctx, a, "analytics.site", &p, &e, "site "+to, "renamed site "+host+" to "+to+": "+host+" is gone")
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
