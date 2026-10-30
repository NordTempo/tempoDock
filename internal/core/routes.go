package core

import (
	"context"
	"fmt"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// applyRoutes renders the server's complete routing table from the DB and
// loads it atomically (ADR 0009). Every domain of a running service routes
// to its active release's host port, with the service's access protection
// (ADR 0030) and its requests counted (ADR 0033); redirect domains
// redirect. On a direct server, domains Dock routes through its Cloudflare
// tunnel are served to the tunnel only (ADR 0036).
func (c *Core) applyRoutes(ctx context.Context, serverID int64) error {
	c.routesMu.Lock()
	defer c.routesMu.Unlock()
	return c.applyRoutesLocked(ctx, serverID, nil)
}

// applyRoutesLocked is applyRoutes for callers holding routesMu. override
// maps service IDs to host ports that replace the active release's, so a
// cutover can switch traffic before the DB records the new release as
// active.
func (c *Core) applyRoutesLocked(ctx context.Context, serverID int64, override map[int64]int64) error {
	rows, err := c.store.ListRoutableDomains(ctx, serverID)
	if err != nil {
		return err
	}
	viaTunnel, err := c.tunnelOnlyHosts(ctx, serverID)
	if err != nil {
		return err
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
		port := d.HostPort
		if p, ok := override[d.ServiceID]; ok {
			port = p
		}
		switch {
		case d.RedirectTo != "":
			routes = append(routes, node.Route{Hostnames: []string{d.Hostname}, RedirectTo: d.RedirectTo, TunnelOnly: tunnelOnly})
		case port == 0:
			// Not running yet: Caddy has nothing to proxy to.
		default:
			key := svcRoute{d.ServiceID, tunnelOnly}
			if i, ok := bySvc[key]; ok {
				routes[i].Hostnames = append(routes[i].Hostnames, d.Hostname)
				continue
			}
			acc, err := c.routeAccess(d)
			if err != nil {
				// Never serve a protected service unprotected.
				c.log.Error("service left out of routing", "domain", d.Hostname, "err", err)
				continue
			}
			bySvc[key] = len(routes)
			routes = append(routes, node.Route{Hostnames: []string{d.Hostname}, Upstream: fmt.Sprintf("127.0.0.1:%d", port), Access: acc, Traffic: true, TunnelOnly: tunnelOnly})
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
