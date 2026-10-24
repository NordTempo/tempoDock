package core

import (
	"context"
	"fmt"

	"github.com/NordTempo/tempoDock/internal/node"
)

// applyRoutes renders the server's complete routing table from the DB and
// loads it atomically (ADR 0009). Every domain of a running service routes
// to its active release's host port, with the service's access protection
// (ADR 0030) and its requests counted (ADR 0033); redirect domains
// redirect.
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
	routes := []node.Route{}
	bySvc := map[int64]int{} // service ID → index in routes
	for _, d := range rows {
		port := d.HostPort
		if p, ok := override[d.ServiceID]; ok {
			port = p
		}
		switch {
		case d.RedirectTo != "":
			routes = append(routes, node.Route{Hostnames: []string{d.Hostname}, RedirectTo: d.RedirectTo})
		case port == 0:
			// Not running yet: Caddy has nothing to proxy to.
		default:
			if i, ok := bySvc[d.ServiceID]; ok {
				routes[i].Hostnames = append(routes[i].Hostnames, d.Hostname)
				continue
			}
			acc, err := c.routeAccess(d)
			if err != nil {
				// Never serve a protected service unprotected.
				c.log.Error("service left out of routing", "domain", d.Hostname, "err", err)
				continue
			}
			bySvc[d.ServiceID] = len(routes)
			routes = append(routes, node.Route{Hostnames: []string{d.Hostname}, Upstream: fmt.Sprintf("127.0.0.1:%d", port), Access: acc, Traffic: true})
		}
	}
	return c.runtime(serverID).ApplyRoutes(ctx, c.withControlRoutes(serverID, routes))
}
