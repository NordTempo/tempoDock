package core

import (
	"context"
	"fmt"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// applyRoutes renders the server's complete routing table from the DB and
// loads it atomically (ADR 0009). Every domain of a running service routes
// to its active release's host port, with the service's access protection
// (ADR 0030), its requests counted (ADR 0033) and, for hostnames of an
// analytics site, /_dock/a/* sent to the server's ingest tagged with the
// site, service and release (ADR 0037); redirect domains redirect, and
// those of a stopped service answer 503 saying so (ADR 0050). On a
// direct server, domains Dock routes through its Cloudflare tunnel are
// served to the tunnel only (ADR 0036).
func (c *Core) applyRoutes(ctx context.Context, serverID int64) error {
	c.routesMu.Lock()
	defer c.routesMu.Unlock()
	return c.applyRoutesLocked(ctx, serverID, nil)
}

// routeOverride is a release a cutover switches a service to before the
// DB records it as active: its host port, and its ID for analytics tags.
type routeOverride struct {
	port, release int64
}

// applyRoutesLocked is applyRoutes for callers holding routesMu. override
// maps service IDs to the release that replaces the active one, so a
// cutover can switch traffic (and analytics tags) before the DB records
// the new release as active.
func (c *Core) applyRoutesLocked(ctx context.Context, serverID int64, override map[int64]routeOverride) error {
	rows, err := c.store.ListRoutableDomains(ctx, serverID)
	if err != nil {
		return err
	}
	// Services that moved away are forwarded from their old port (ADR
	// 0063), and those of a move in its hold wait for their upstream.
	fwd, err := c.store.ListForwardedDomains(ctx, serverID)
	if err != nil {
		return err
	}
	held := c.heldServices()
	for _, f := range fwd {
		held[f.ServiceID] = true
		rows = append(rows, store.ListRoutableDomainsRow(f))
	}
	viaTunnel, err := c.tunnelOnlyHosts(ctx, serverID)
	if err != nil {
		return err
	}
	hosts, err := c.store.ListAnalyticsHosts(ctx, serverID)
	if err != nil {
		return err
	}
	siteOf := make(map[string]int64, len(hosts))
	for _, h := range hosts {
		siteOf[h.Hostname] = h.SiteID
	}
	routes := []node.Route{}
	// A service's domains share a route, one for those served to the
	// tunnel only and one for the rest.
	type svcRoute struct {
		id         int64
		tunnelOnly bool
	}
	bySvc := map[svcRoute]int{} // → index in routes
	for _, d := range rows {
		tunnelOnly := viaTunnel[d.Hostname]
		port, release, stopped := d.HostPort, d.ReleaseID, d.Stopped
		if o, ok := override[d.ServiceID]; ok {
			// The release going live runs, whether the service was
			// stopped or not.
			port, release, stopped = o.port, o.release, false
		}
		switch {
		case d.RedirectTo != "":
			routes = append(routes, node.Route{Hostnames: []string{d.Hostname}, RedirectTo: d.RedirectTo, TunnelOnly: tunnelOnly})
		case port == 0:
			// Not running yet: Caddy has nothing to proxy to.
		default:
			key := svcRoute{d.ServiceID, tunnelOnly}
			i, ok := bySvc[key]
			if !ok {
				acc, err := c.routeAccess(d)
				if err != nil {
					// Never serve a protected service unprotected.
					c.log.Error("service left out of routing", "domain", d.Hostname, "err", err)
					continue
				}
				i = len(routes)
				bySvc[key] = i
				routes = append(routes, node.Route{Upstream: fmt.Sprintf("127.0.0.1:%d", port), Access: acc, Traffic: true, TunnelOnly: tunnelOnly, Hold: held[d.ServiceID]})
				if stopped {
					routes[i].Stopped = d.ServiceName
				}
			}
			r := &routes[i]
			r.Hostnames = append(r.Hostnames, d.Hostname)
			// A stopped service's 503 page has no analytics script.
			if site, ok := siteOf[d.Hostname]; ok && !stopped {
				addAnalyticsHost(r, d.Hostname, fmt.Sprintf("%d.%d.%d", site, d.ServiceID, release))
			}
		}
	}
	return c.runtime(serverID).ApplyRoutes(ctx, c.withControlRoutes(serverID, routes))
}

// tunnelOnlyHosts are the hostnames a direct server serves to its
// Cloudflare tunnel only: the domains Dock routes through it (ADR 0036).
// None on a tunnel server, where everything comes through the tunnel.
func (c *Core) tunnelOnlyHosts(ctx context.Context, serverID int64) (map[string]bool, error) {
	c.cf.mu.Lock()
	connected := len(c.cf.conns) > 0
	c.cf.mu.Unlock()
	if !connected {
		return nil, nil // no Cloudflare connection: nothing to look up
	}
	rows, err := c.store.ListServerCloudflareRecords(ctx, &serverID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	srv, err := c.store.GetServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if c.ingressOf(srv).mode == apitypes.IngressCloudflareTunnel {
		return nil, nil
	}
	hosts := make(map[string]bool, len(rows))
	for _, r := range rows {
		hosts[r.Hostname] = true
	}
	return hosts, nil
}

// addAnalyticsHost adds hostname to r's analytics group with tag, making
// one when there is none.
func addAnalyticsHost(r *node.Route, hostname, tag string) {
	for i := range r.Analytics {
		if r.Analytics[i].Tag == tag {
			r.Analytics[i].Hostnames = append(r.Analytics[i].Hostnames, hostname)
			return
		}
	}
	r.Analytics = append(r.Analytics, node.AnalyticsHosts{Hostnames: []string{hostname}, Tag: tag})
}
