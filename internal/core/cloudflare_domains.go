package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cloudflare"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Domains through tunnels (ADR 0036): a domain added "from Cloudflare"
// (or generated under a base domain in a managed zone on a tunnel server)
// is routed through the server's tunnel: a proxied CNAME to it,
// and on an adopted tunnel whose catch-all doesn't go to Caddy a hostname
// route. Dock never points DNS at an IP. The rows of cloudflare_records
// are worked on by the Cloudflare worker (cloudflare_queue.go).

// cfNameCheckTimeout bounds checking a name in Cloudflare when a domain
// is added from it.
const cfNameCheckTimeout = 15 * time.Second

// ListCloudflareZones returns the zones the actor's org may add domains
// from: its own connections' and the platform's shared ones, never
// another org's. Anyone who can add domains may list them.
func (c *Core) ListCloudflareZones(_ context.Context, a Actor) ([]apitypes.CloudflareZone, error) {
	if !a.Operator {
		if err := a.require(apitypes.ScopeDeploy, "listing Cloudflare zones"); err != nil {
			return nil, err
		}
	}
	return c.usableZones(a.OrgID), nil
}

// usableZones are the zones org's domains may use (cfZone.usableBy), by
// name. A zone on several connections is listed once, with the one its
// domains use (pickZone).
func (c *Core) usableZones(org int64) []apitypes.CloudflareZone {
	c.cf.mu.Lock()
	defer c.cf.mu.Unlock()
	out := make([]apitypes.CloudflareZone, 0, len(c.cf.zones))
	for _, z := range c.cf.zones {
		if !z.usableBy(org) || slices.ContainsFunc(out, func(o apitypes.CloudflareZone) bool { return o.Name == z.Name }) {
			continue
		}
		if z, ok := pickZone(c.cf.zones, org, func(o cfZone) bool { return o.Name == z.Name }); ok {
			out = append(out, apitypes.CloudflareZone{Name: z.Name, Connection: c.cf.conns[z.ConnectionID], Platform: z.org == 0})
		}
	}
	slices.SortFunc(out, func(a, b apitypes.CloudflareZone) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// atOrUnderBase reports whether host is a server's base domain or under
// it: the platform's names, which may use any platform zone.
func atOrUnderBase(host, base string) bool {
	return base != "" && (host == base || strings.HasSuffix(host, "."+base))
}

// prepareCloudflareDomain checks that host can be added from Cloudflare
// to a service of org's on srv, and gets srv's tunnel ready for it
// (adopting the one it runs, or creating one): host is in a zone org may
// use (zoneFor; under srv's base domain, the platform's), srv can take a
// tunnel (tunnel mode, or a direct server with a tunnel listener), and
// no record Dock didn't make is in the way. It returns the zone.
func (c *Core) prepareCloudflareDomain(ctx context.Context, a Actor, srv store.Server, org int64, host string) (store.CloudflareZone, error) {
	if atOrUnderBase(host, c.ingressOf(srv).baseDomain) {
		org = 0
	}
	z, ok := c.zoneFor(org, host)
	if !ok {
		zones := c.usableZones(org)
		names := make([]string, len(zones))
		for i, z := range zones {
			names[i] = z.Name
		}
		if len(names) == 0 {
			return z, invalid("no Cloudflare zone is connected for this organization; its admins add a connection with `dock cloudflare add`, or the platform's operator shares one")
		}
		return z, invalid("%s isn't in a Cloudflare zone this organization may use (%s)", host, strings.Join(names, ", "))
	}
	if c.tunnelPort(srv) == 0 {
		return z, invalid("domains from Cloudflare go through a tunnel, and %s", c.noTunnelListener(srv))
	}
	conn, err := c.store.GetCloudflareConnectionByID(ctx, z.ConnectionID)
	if err != nil {
		return z, err
	}
	if conn.Status != string(apitypes.CloudflareOK) {
		who := "the organization's admins give"
		if conn.OrgID == nil {
			who = "an operator gives"
		}
		return z, invalid("Cloudflare connection %s doesn't work (%s); %s it a new token with `dock cloudflare set %s%s --token-stdin`", conn.Name, conn.StatusReason, who, conn.Name, cfFlag(conn.OrgID))
	}
	cl, err := c.cfClient(conn)
	if err != nil {
		return z, err
	}
	cctx, cancel := context.WithTimeout(ctx, cfNameCheckTimeout)
	defer cancel()
	existing, err := cl.RecordsNamed(cctx, z.ZoneID, host)
	if err != nil {
		return z, cfCheckErr(err, "look up "+host+" in Cloudflare")
	}
	// The tunnel Dock runs, or the one it is about to adopt.
	if inWay := c.recordsInWay(existing, c.ingressOf(srv).tunnel); len(inWay) > 0 {
		return z, errf(apitypes.CodeHostnameTaken, "%s already has %s in Cloudflare that Dock didn't make; pick another name, or remove it in Cloudflare", host, describeRecords(inWay))
	}
	return z, c.serverTunnelFor(ctx, a, srv, conn)
}

// recordsInWay are the A, AAAA and CNAME records Dock didn't make,
// unless the only one is a proxied CNAME to target (the tunnel; "" when
// not known yet), which Dock uses as it is.
func (c *Core) recordsInWay(existing []cloudflare.Record, target string) []cloudflare.Record {
	var inWay []cloudflare.Record
	for _, e := range existing {
		if e.Comment != c.recordComment() && (e.Type == "A" || e.Type == "AAAA" || e.Type == "CNAME") {
			inWay = append(inWay, e)
		}
	}
	if len(inWay) == 1 && target != "" && pointsAt(inWay[0], target) {
		return nil
	}
	return inWay
}

// pointsAt reports whether r is a proxied CNAME to target.
func pointsAt(r cloudflare.Record, target string) bool {
	return r.Type == "CNAME" && r.Proxied && strings.EqualFold(strings.TrimSuffix(r.Content, "."), target)
}

// describeRecords names records for a message: "an A record to 1.2.3.4".
func describeRecords(rs []cloudflare.Record) string {
	parts := make([]string, len(rs))
	for i, r := range rs {
		article := "a"
		if strings.HasPrefix(r.Type, "A") {
			article = "an"
		}
		parts[i] = fmt.Sprintf("%s %s record to %s", article, r.Type, r.Content)
	}
	return strings.Join(parts, " and ")
}

// cloudflareRecords works through the due records, in batches.
func (c *Core) cloudflareRecords(ctx context.Context, conns map[int64]*cfConn) error {
	for range cfMaxBatches {
		due, err := c.store.ListDueCloudflareRecords(ctx, store.ListDueCloudflareRecordsParams{Now: ptr(now()), Max: cfBatch})
		if err != nil {
			return err
		}
		for _, r := range due {
			cc, err := c.conn(ctx, conns, r.ConnectionID)
			if err != nil {
				return err
			}
			if cc.row.Status != string(apitypes.CloudflareOK) {
				continue
			}
			if !c.holdsZone(r.ConnectionID, r.ZoneID) {
				// Retried after its connection lost the zone: never worked
				// on with a connection that doesn't hold it.
				if err := c.store.FailCloudflareRecord(ctx, store.FailCloudflareRecordParams{
					Reason: strandedReason(cc.row.Name, r.Hostname), UpdatedAt: now(), ID: r.ID,
				}); err != nil {
					return err
				}
				c.publishRecord(ctx, r.Hostname)
				continue
			}
			if err := c.recordStep(ctx, cc, r); err != nil {
				return err
			}
		}
		if len(due) < cfBatch {
			return nil
		}
	}
	return nil
}

// recordStep makes Cloudflare match one row and saves the result. Only a
// failure to save (or a rate limit) is returned.
func (c *Core) recordStep(ctx context.Context, cc *cfConn, r store.CloudflareRecord) error {
	cctx, cancel := context.WithTimeout(ctx, cfCallTimeout)
	defer cancel()
	if r.Want == cfAbsent {
		if err := c.unrouteDomain(cctx, cc, r); err != nil {
			return c.recordFailed(ctx, cc, r, err)
		}
		if err := c.store.DeleteCloudflareRecord(ctx, store.DeleteCloudflareRecordParams{ID: r.ID, Gen: r.Gen}); err != nil {
			return err
		}
		c.publishRecord(ctx, r.Hostname)
		return nil
	}
	res, err := c.routeDomain(cctx, cc, r)
	if err != nil {
		return c.recordFailed(ctx, cc, r, err)
	}
	saved, err := c.store.UpdateCloudflareRecord(ctx, store.UpdateCloudflareRecordParams{
		RecordID: res.RecordID, Content: res.Content, RouteTunnel: res.RouteTunnel, Status: res.Status,
		Reason: res.Reason, Attempts: 0, UpdatedAt: now(), ID: r.ID, Gen: r.Gen,
	})
	if err != nil || saved == 0 {
		return err // saved == 0: asked for again meanwhile; the next step redoes it
	}
	if res.Status == "ok" || res.Status == "external" {
		c.kickDomains() // its DNS check can pass now
	}
	c.publishRecord(ctx, r.Hostname)
	return nil
}

// recordFailed saves what a failed record step means.
func (c *Core) recordFailed(ctx context.Context, cc *cfConn, r store.CloudflareRecord, stepErr error) error {
	zone := c.zoneName(r.ZoneID)
	out, err := c.failed(ctx, cc, r.Attempts, stepErr, "Zone › DNS › Edit on "+zone+" and Account › Cloudflare Tunnel › Edit")
	if out.status == "" {
		return err
	}
	if _, uerr := c.store.UpdateCloudflareRecord(ctx, store.UpdateCloudflareRecordParams{
		RecordID: r.RecordID, Content: r.Content, RouteTunnel: r.RouteTunnel, Status: out.status, Reason: out.reason,
		Attempts: out.attempts, RetryAt: out.retryAt, UpdatedAt: now(), ID: r.ID, Gen: r.Gen,
	}); uerr != nil {
		return uerr
	}
	c.publishRecord(ctx, r.Hostname)
	return err
}

// waitingForTunnel is a routed domain's reason while its server's tunnel
// isn't ready yet: a passing state, not a failure. syncServerRecords
// routes the domain again once the tunnel is ready.
const waitingForTunnel = "waiting for the server's tunnel"

// routeResult is what routeDomain found or did.
type routeResult struct {
	RecordID, Content, RouteTunnel string
	Status, Reason                 string
}

// routeDomain routes r.Hostname through its server's tunnel: a hostname
// route on an adopted tunnel whose catch-all doesn't go to Caddy, then
// the proxied CNAME. Records Dock made (by comment) are changed or
// replaced; a CNAME someone else made that already points at the tunnel
// is used as it is (external); any other A, AAAA or CNAME is in the way,
// and Dock never touches it.
func (c *Core) routeDomain(ctx context.Context, cc *cfConn, r store.CloudflareRecord) (routeResult, error) {
	keep := routeResult{RecordID: r.RecordID, Content: r.Content, RouteTunnel: r.RouteTunnel}
	fail := func(reason string) (routeResult, error) {
		keep.Status, keep.Reason = "error", reason
		return keep, nil
	}
	if r.ServerID == nil {
		return fail("its server was removed")
	}
	srv, err := c.store.GetServer(ctx, *r.ServerID)
	if err != nil {
		return routeResult{}, err
	}
	t, err := c.store.GetCloudflareTunnelByServer(ctx, &srv.ID)
	switch {
	case store.IsNotFound(err):
		return fail("the server has no tunnel Dock runs; add the domain from Cloudflare again")
	case err != nil:
		return routeResult{}, err
	case t.Status != "ok" || t.TunnelID == "":
		return fail(waitingForTunnel)
	}
	target := cloudflare.Tunnel{ID: t.TunnelID}.Hostname()
	res := routeResult{Content: target, RouteTunnel: r.RouteTunnel}
	if t.Adopted {
		added, err := c.addRoute(ctx, cc, t.TunnelID, r.Hostname, catchAll(c.tunnelPort(srv)).Ingress[0].Service)
		if err != nil {
			if e, ok := errors.AsType[routeTaken](err); ok {
				return fail(string(e))
			}
			return routeResult{}, err
		}
		if added {
			res.RouteTunnel = t.TunnelID
		}
	}
	existing, err := cc.client.RecordsNamed(ctx, r.ZoneID, r.Hostname)
	if err != nil {
		return routeResult{}, err
	}
	var ours []cloudflare.Record
	for _, e := range existing {
		if e.Comment == c.recordComment() {
			ours = append(ours, e)
		}
	}
	inWay := c.recordsInWay(existing, target)
	switch {
	case len(ours) == 0 && len(inWay) == 0 && len(existing) > 0 && slices.ContainsFunc(existing, func(e cloudflare.Record) bool { return pointsAt(e, target) }):
		res.Status, res.Reason = "external", "a CNAME made outside Dock already points at the tunnel; Dock leaves it alone"
		return res, nil
	case len(inWay) > 0:
		res.Status, res.Reason = "error", fmt.Sprintf("%s already has %s that Dock didn't make; remove it in Cloudflare, then retry", r.Hostname, describeRecords(inWay))
		return res, nil
	}
	want := cloudflare.Record{Type: "CNAME", Name: r.Hostname, Content: target, Proxied: true, TTL: 1, Comment: c.recordComment()}
	var kept *cloudflare.Record
	for i, e := range ours {
		if kept == nil && e.Type == "CNAME" {
			kept = &ours[i]
			continue
		}
		if err := cc.client.DeleteRecord(ctx, r.ZoneID, e.ID); err != nil {
			return routeResult{}, err
		}
	}
	switch {
	case kept == nil:
		made, err := cc.client.CreateRecord(ctx, r.ZoneID, want)
		if err != nil {
			return routeResult{}, err
		}
		res.RecordID = made.ID
	case !pointsAt(*kept, target):
		want.ID = kept.ID
		if _, err := cc.client.UpdateRecord(ctx, r.ZoneID, want); err != nil {
			return routeResult{}, err
		}
		res.RecordID = kept.ID
	default:
		res.RecordID = kept.ID
	}
	res.Status = "ok"
	return res, nil
}

// unrouteDomain deletes what Dock made for r.Hostname: every record of
// that name with Dock's comment (records someone else made, or took over
// by changing the comment, stay) and the hostname route it added.
func (c *Core) unrouteDomain(ctx context.Context, cc *cfConn, r store.CloudflareRecord) error {
	existing, err := cc.client.RecordsNamed(ctx, r.ZoneID, r.Hostname)
	if err != nil {
		return err
	}
	for _, e := range existing {
		if e.Comment == c.recordComment() {
			if err := cc.client.DeleteRecord(ctx, r.ZoneID, e.ID); err != nil {
				return err
			}
		}
	}
	if r.RouteTunnel == "" {
		return nil
	}
	cfg, err := cc.client.TunnelConfig(ctx, cc.row.AccountID, r.RouteTunnel)
	if e, ok := cloudflare.AsError(err); ok && e.NotFound() {
		return nil // the tunnel is gone
	}
	if err != nil {
		return err
	}
	rules := slices.DeleteFunc(slices.Clone(cfg.Ingress), func(rule cloudflare.IngressRule) bool { return rule.Hostname == r.Hostname })
	if len(rules) == len(cfg.Ingress) || len(rules) == 0 {
		return nil
	}
	return cc.client.SetTunnelConfig(ctx, cc.row.AccountID, r.RouteTunnel, cloudflare.TunnelConfig{Ingress: rules})
}

// routeTaken is a tunnel that already routes a hostname elsewhere.
type routeTaken string

func (e routeTaken) Error() string { return string(e) }

// addRoute makes sure an adopted tunnel sends host to service (the
// server's Caddy): the first rule that matches host decides. When that is
// the catch-all, or a wildcard, going elsewhere, a rule for host goes
// first, and added is true. A rule for host itself going elsewhere is
// someone's route: routeTaken.
func (c *Core) addRoute(ctx context.Context, cc *cfConn, tunnelID, host, service string) (added bool, err error) {
	cfg, err := cc.client.TunnelConfig(ctx, cc.row.AccountID, tunnelID)
	if err != nil {
		return false, err
	}
	if i := slices.IndexFunc(cfg.Ingress, func(r cloudflare.IngressRule) bool { return matchesHost(r.Hostname, host) }); i >= 0 {
		switch rule := cfg.Ingress[i]; {
		case rule.Service == service:
			return rule.Hostname == host, nil
		case rule.Hostname == host:
			return false, routeTaken(fmt.Sprintf("the tunnel routes %s to %s already; remove that route in Cloudflare, then retry", host, rule.Service))
		}
	}
	rules := append([]cloudflare.IngressRule{{Hostname: host, Service: service}}, cfg.Ingress...)
	if len(cfg.Ingress) == 0 {
		rules = append(rules, cloudflare.IngressRule{Service: "http_status:404"}) // a catch-all is required
	}
	return true, cc.client.SetTunnelConfig(ctx, cc.row.AccountID, tunnelID, cloudflare.TunnelConfig{Ingress: rules})
}

// matchesHost reports whether a tunnel rule's hostname (empty for the
// catch-all, or *.zone) covers host.
func matchesHost(rule, host string) bool {
	switch {
	case rule == "" || strings.EqualFold(rule, host):
		return true
	case strings.HasPrefix(rule, "*."):
		suffix := rule[1:]
		return strings.HasSuffix(host, suffix) && !strings.Contains(strings.TrimSuffix(host, suffix), ".")
	}
	return false
}

// syncServerRecords looks at a server's domains again after its tunnel
// may have changed (it got one, or reconnected): those that failed, or
// whose CNAME points elsewhere, are worked on again. Nothing is asked of
// Cloudflare for domains that still point at the right tunnel.
func (c *Core) syncServerRecords(ctx context.Context, srv store.Server) {
	rows, err := c.store.ListServerCloudflareRecords(ctx, &srv.ID)
	if err != nil {
		c.log.Error("list a server's domains in Cloudflare", "server", srv.Name, "err", err)
		return
	}
	target := c.managedTunnel(srv.ID)
	kick := false
	for _, r := range rows {
		stale := (r.Status == "ok" || r.Status == "external") && r.Content != target
		if r.Status != "error" && !stale {
			continue
		}
		if err := c.store.RetryCloudflareRecord(ctx, store.RetryCloudflareRecordParams{UpdatedAt: now(), ID: r.ID}); err != nil {
			c.log.Error("retry a domain in Cloudflare", "hostname", r.Hostname, "err", err)
			return
		}
		kick = true
	}
	if t, err := c.store.GetCloudflareTunnelByServer(ctx, &srv.ID); err == nil && t.Status == "error" {
		if err := c.store.RetryCloudflareTunnel(ctx, store.RetryCloudflareTunnelParams{UpdatedAt: now(), ID: t.ID}); err == nil {
			kick = true
		}
	}
	if kick {
		c.kickCloudflare()
	}
}

// publishRecords sends the domains of hostnames, whose routes changed, to
// clients.
func (c *Core) publishRecords(ctx context.Context, hostnames []string) {
	for _, h := range hostnames {
		c.publishRecord(ctx, h)
	}
}

// publishRecord sends a domain whose route changed to clients.
func (c *Core) publishRecord(ctx context.Context, hostname string) {
	d, err := c.store.GetDomainByHostname(ctx, hostname)
	if err != nil {
		return
	}
	ss, err := c.scopeOf(ctx, d.ServiceID)
	if err != nil {
		return
	}
	doms, err := c.domainsOf(ctx, ss)
	if err != nil {
		return
	}
	for i := range doms {
		if doms[i].Hostname == d.Hostname {
			c.publish(ss, apitypes.Event{Type: apitypes.EventDomain, Domain: &doms[i]})
		}
	}
}

// cfRecordsCap caps the console's list of domains through tunnels.
const cfRecordsCap = 500

// ListCloudflareRecords returns the domains Dock routes through tunnels,
// failed ones first, at most cfRecordsCap. Operators only.
func (c *Core) ListCloudflareRecords(ctx context.Context, a Actor) ([]apitypes.CloudflareRecord, error) {
	if err := a.requireOperator("listing domains in Cloudflare"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListCloudflareRecords(ctx, cfRecordsCap)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.CloudflareRecord, len(rows))
	for i, r := range rows {
		rec := r.CloudflareRecord
		out[i] = apitypes.CloudflareRecord{
			Hostname: rec.Hostname, Connection: r.ConnectionName, Server: r.ServerName,
			Status: recordStatus(rec), Reason: rec.Reason, Tunnel: rec.Content, UpdatedAt: rec.UpdatedAt,
		}
	}
	return out, nil
}

// recordStatus is a row's status as the API shows it.
func recordStatus(r store.CloudflareRecord) apitypes.DNSRecordStatus {
	switch {
	case r.Want == cfAbsent:
		return apitypes.DNSDeleting
	case r.Status == "ok":
		return apitypes.DNSReady
	}
	return apitypes.DNSRecordStatus(r.Status)
}

// withRoute shows how Dock routes a domain through its server's tunnel:
// its route's state, and the record it has (the CNAME to the tunnel)
// instead of records to the server's IP. r nil leaves dom as it is.
func (c *Core) withRoute(dom *apitypes.Domain, r *store.CloudflareRecord) {
	dom.DNS = c.domainDNS(r)
	if dom.DNS == nil {
		return
	}
	dom.DNSRecords = []apitypes.DNSRecord{}
	if dom.DNS.Tunnel != "" {
		dom.DNSRecords = append(dom.DNSRecords, apitypes.DNSRecord{Type: "CNAME", Name: dom.Hostname, Value: dom.DNS.Tunnel})
	}
}

// domainDNS is a domain's route through its tunnel for the API; nil for
// a domain Dock doesn't route.
func (c *Core) domainDNS(r *store.CloudflareRecord) *apitypes.DomainDNS {
	if r == nil || r.Want == cfAbsent {
		return nil
	}
	return &apitypes.DomainDNS{
		Connection: c.connectionName(r.ConnectionID), Zone: c.zoneName(r.ZoneID), Status: recordStatus(*r),
		Reason: r.Reason, Tunnel: r.Content,
	}
}
