package core

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// applyRoutes renders the server's complete routing table from the DB and
// loads it atomically (ADR 0009). Every domain of a running service routes
// to its active release's host port, with the service's access protection
// (ADR 0030), its requests counted (ADR 0033) and, for hostnames of an
// analytics site, /_dock/a/* sent to the server's ingest tagged with the
// site, service and release (ADR 0037) and, unless the site's auto_script
// is off, the script added to their pages (ADR 0105); services that moved away are
// forwarded with their domains, except generated ones under another
// server's base (ADR 0063); redirect domains redirect, and
// those of a stopped service answer 503 saying so (ADR 0050). Those of an
// asleep environment ask the wake check first, and in environments that
// sleep Dock's uptime probes aren't counted (sleep.go). On a direct
// server, domains Dock routes through its Cloudflare tunnel are served
// to the tunnel only (ADR 0036). The server's base domain's wildcard
// certificate goes with them (ADR 0090, wildcards.go).
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
	holdPorts := c.holdPorts(serverID)
	var base string
	if len(fwd) > 0 {
		srv, err := c.store.GetServer(ctx, serverID)
		if err != nil {
			return err
		}
		base = c.ingressOf(srv).baseDomain
	}
	for _, f := range fwd {
		// A generated name under another server's base never reaches
		// this one. Routing it would only change this config when a move
		// makes one (a later move's hold then reloads Caddy, and a reload
		// drops requests on idle keep-alive connections).
		if f.Generated && !underBase(f.Hostname, base) {
			continue
		}
		held[f.ServiceID] = true
		rows = append(rows, store.ListRoutableDomainsRow(f))
	}
	// In service order, forwarded ones included: a move's switch turns a
	// held service into a forwarded one, and its route must render where
	// it was. A config that changes makes Caddy reload, and a reload ends
	// every held request with a 502.
	slices.SortStableFunc(rows, func(a, b store.ListRoutableDomainsRow) int { return cmp.Compare(a.ServiceID, b.ServiceID) })
	viaTunnel, err := c.tunnelOnlyHosts(ctx, serverID)
	if err != nil {
		return err
	}
	counted, err := c.store.ListAnalyticsServices(ctx, serverID)
	if err != nil {
		return err
	}
	// A counted service's site, by service (ADR 0104), and whether its
	// pages get the script from Caddy (ADR 0105).
	type countedSite struct {
		id     int64
		script bool
	}
	siteOf := make(map[int64]countedSite, len(counted))
	if len(counted) > 0 {
		defaults, err := c.siteDefaults(ctx)
		if err != nil {
			return err
		}
		for _, s := range counted {
			if s.ServiceID != nil {
				o := siteOverrides(store.Site{Settings: s.Settings})
				siteOf[*s.ServiceID] = countedSite{s.SiteID, *mergeSettings(defaults, o).AutoScript}
			}
		}
	}
	traced, err := c.edgeTraced(ctx, rows, override)
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
		port, release, stopped, asleep := d.HostPort, d.ReleaseID, d.Stopped, d.Asleep
		if o, ok := override[d.ServiceID]; ok {
			// The release going live runs, whether the service was
			// stopped or not.
			port, release, stopped, asleep = o.port, o.release, false, false
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
				if p, ok := holdPorts[d.ServiceID]; ok && held[d.ServiceID] {
					// A move's hold: the source waits for the switch
					// on a port of its own (forwards.go).
					port = p
				}
				routes = append(routes, node.Route{Upstream: fmt.Sprintf("127.0.0.1:%d", port), Key: routeKey(d.ServiceID), Access: acc, Traffic: true, TunnelOnly: tunnelOnly, Hold: held[d.ServiceID], QuietProbes: d.Sleepable})
				routes[i].Trace = traced[d.ServiceID]
				switch {
				case stopped:
					routes[i].Stopped = d.ServiceName
				case asleep && !held[d.ServiceID]:
					routes[i].Wake = strconv.FormatInt(d.EnvironmentID, 10)
				}
			}
			r := &routes[i]
			r.Hostnames = append(r.Hostnames, d.Hostname)
			// A stopped service's 503 page has no analytics script.
			if site, ok := siteOf[d.ServiceID]; ok && !stopped {
				addAnalyticsHost(r, d.Hostname, fmt.Sprintf("%d.%d.%d", site.id, d.ServiceID, release), site.script)
			}
		}
	}
	certs, err := c.serverCertificates(ctx, serverID)
	if err != nil {
		return err
	}
	return c.runtime(serverID).ApplyRoutes(ctx, c.withControlRoutes(serverID, routes), certs)
}

// routeKey names a service's routes across its releases (node.Route.Key),
// so a switch to a new release repoints the route without reloading
// Caddy (ADR 0091).
func routeKey(serviceID int64) string { return "s" + strconv.FormatInt(serviceID, 10) }

// tunnelOnlyHosts are the hostnames a direct server serves to its
// Cloudflare tunnel only: the domains Dock routes through it (ADR 0036),
// also when it is sent through its tunnel (ingressOf), where the rest
// stay on its public IP too. A hostname under its base domain reaches
// the IP through the base's wildcard record until its own record is in
// place, so it goes to the tunnel only once that is ready
// (routeReadyRecords). None on a server installed in tunnel mode, where
// everything comes through the tunnel.
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
	ing := c.ingressOf(srv)
	if ing.mode == apitypes.IngressCloudflareTunnel && !ing.listener {
		return nil, nil
	}
	hosts := make(map[string]bool, len(rows))
	for _, r := range rows {
		if atOrUnderBase(r.Hostname, ing.baseDomain) && !recordReady(r.Status) {
			continue
		}
		hosts[r.Hostname] = true
	}
	return hosts, nil
}

// addAnalyticsHost adds hostname to r's analytics group with tag, making
// one when there is none; script is the group's site's auto_script.
func addAnalyticsHost(r *node.Route, hostname, tag string, script bool) {
	for i := range r.Analytics {
		if r.Analytics[i].Tag == tag {
			r.Analytics[i].Hostnames = append(r.Analytics[i].Hostnames, hostname)
			return
		}
	}
	r.Analytics = append(r.Analytics, node.AnalyticsHosts{Hostnames: []string{hostname}, Tag: tag, Script: script})
}

// applyAllRoutes applies the routes of every online server, for a change
// that touches all of them (tracing on or off). A server that fails keeps
// its routes until its next change, and says so in the log.
func (c *Core) applyAllRoutes(ctx context.Context) {
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		c.log.Error("apply routes on every server", "err", err)
		return
	}
	for _, s := range servers {
		if !c.online(s.ID) {
			continue
		}
		if err := c.applyRoutes(ctx, s.ID); err != nil {
			c.log.Error("apply routes", "server", s.Name, "err", err)
		}
	}
}
