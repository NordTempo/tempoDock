package core

import (
	"context"
	"fmt"
	"maps"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

var labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

var numericRe = regexp.MustCompile(`^[0-9]+$`)

// normalizeHostname lowercases and validates a hostname. IP addresses,
// localhost and single-label names are refused: they aren't public names,
// and an IP "domain" would route the server's own address (making a page
// on it same-site with the dashboard).
func normalizeHostname(h string) (string, error) {
	h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
	if _, err := netip.ParseAddr(strings.Trim(h, "[]")); err == nil {
		return "", invalid("%q is an IP address; domains must be hostnames like app.example.com", h)
	}
	labels := strings.Split(h, ".")
	if len(h) > 253 || len(labels) < 2 {
		return "", invalid("%q is not a valid hostname; use a full name like app.example.com", h)
	}
	for _, l := range labels {
		if !labelRe.MatchString(l) {
			return "", invalid("%q is not a valid hostname (label %q)", h, l)
		}
	}
	switch tld := labels[len(labels)-1]; {
	case tld == "localhost":
		return "", invalid("%q is a localhost name; domains must be public hostnames", h)
	case numericRe.MatchString(tld):
		return "", invalid("%q is not a valid hostname (all-numeric top-level label %q)", h, tld)
	}
	return h, nil
}

// ListDomains returns a service's domains with their status and the DNS
// records to create.
func (c *Core) ListDomains(ctx context.Context, a Actor, ref ServiceRef) ([]apitypes.Domain, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "listing domains")
	if err != nil {
		return nil, err
	}
	return c.domainsOf(ctx, ss)
}

// domainsOf returns a service's domains with their status, the DNS
// records to create and, for those Dock routes through a tunnel, how far
// it is: three queries however many domains.
func (c *Core) domainsOf(ctx context.Context, ss svcScope) ([]apitypes.Domain, error) {
	rows, err := c.store.ListDomainsByService(ctx, ss.svc.ID)
	if err != nil {
		return nil, err
	}
	ing, err := c.envIngress(ctx, ss.envScope)
	if err != nil {
		return nil, err
	}
	records, err := c.store.ListServiceCloudflareRecords(ctx, ss.svc.ID)
	if err != nil {
		return nil, err
	}
	byDomain := make(map[int64]*store.CloudflareRecord, len(records))
	for i, r := range records {
		if r.DomainID != nil {
			byDomain[*r.DomainID] = &records[i]
		}
	}
	out := make([]apitypes.Domain, 0, len(rows))
	for _, d := range rows {
		dom := c.toDomain(ss, d, rows, ing)
		c.withRoute(&dom, byDomain[d.ID])
		out = append(out, dom)
	}
	return out, nil
}

// envIngress is the ingress of the server an environment runs on.
func (c *Core) envIngress(ctx context.Context, es envScope) (serverIngress, error) {
	srv, err := c.store.GetServer(ctx, es.env.ServerID)
	if err != nil {
		return serverIngress{}, err
	}
	return c.ingressOf(srv), nil
}

// AddDomain adds any hostname to a service. It routes as soon as the
// service is running and the domain is verified (ADR 0042): a custom
// hostname once its TXT record proves the org controls it. DNS can be
// set up before or after.
func (c *Core) AddDomain(ctx context.Context, a Actor, ref ServiceRef, req apitypes.AddDomainRequest) (apitypes.DomainChange, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "adding a domain")
	if err != nil {
		return apitypes.DomainChange{}, err
	}
	host, err := normalizeHostname(req.Hostname)
	if err != nil {
		return apitypes.DomainChange{}, err
	}
	redirect := ""
	if req.RedirectTo != "" {
		if redirect, err = normalizeHostname(req.RedirectTo); err != nil {
			return apitypes.DomainChange{}, err
		}
		if redirect == host {
			return apitypes.DomainChange{}, invalid("a domain can't redirect to itself")
		}
	}
	if req.Analytics != "" && req.Analytics != "keep" && req.Analytics != "new" {
		return apitypes.DomainChange{}, invalid("analytics is keep or new, not %q", req.Analytics)
	}
	var zone *store.CloudflareZone
	if req.Cloudflare {
		if zone, err = c.cloudflareZoneFor(ctx, a, ss, host); err != nil {
			return apitypes.DomainChange{}, err
		}
	}
	change, err := c.addDomain(ctx, a, ss, host, redirect, false, zone)
	if err != nil || redirect != "" || !change.Domain.Verified {
		return change, err
	}
	// A hostname that collected analytics before keeps its site's history
	// here, or starts a new site (ADR 0037). An unverified one waits until
	// it verifies (proveDomain): it might not be this org's.
	if moved, err := c.siteDomainAdded(ctx, a, ss, host, req.Analytics); err != nil {
		c.log.Error("analytics: carry a site over to a domain", "domain", host, "err", err)
	} else if moved {
		c.analyticsSitesChanged(ctx)
		if err := c.applyRoutes(ctx, ss.env.ServerID); err != nil {
			c.log.Error("apply routes after a site change", "domain", host, "err", err)
		}
	}
	return change, nil
}

// cloudflareZoneFor gets a service's server ready for host to be added
// from Cloudflare (prepareCloudflareDomain) and returns its zone. An
// archived service is refused first, before anything changes.
func (c *Core) cloudflareZoneFor(ctx context.Context, a Actor, ss svcScope, host string) (*store.CloudflareZone, error) {
	if err := c.checkNewDomain(ss, host); err != nil {
		return nil, err
	}
	claims, err := c.store.ListHostnameClaims(ctx, host)
	if err != nil {
		return nil, err
	}
	if err := checkClaims(claims, ss.project.OrgID, host); err != nil {
		return nil, err
	}
	srv, err := c.store.GetServer(ctx, ss.env.ServerID)
	if err != nil {
		return nil, err
	}
	z, err := c.prepareCloudflareDomain(ctx, a, srv, ss.project.OrgID, host)
	if err != nil {
		return nil, err
	}
	return &z, nil
}

// GenerateDomain gives a service a hostname under the base domain of its
// environment's server: <service>-<project>[-<env>]-<random>.<base>
// (ADR 0087), a new random part on the rare clash. On a tunnel server
// whose base domain is in a zone Dock manages, it is routed through the
// tunnel like a domain from Cloudflare.
func (c *Core) GenerateDomain(ctx context.Context, a Actor, ref ServiceRef) (apitypes.DomainChange, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "adding a domain")
	if err != nil {
		return apitypes.DomainChange{}, err
	}
	srv, err := c.store.GetServer(ctx, ss.env.ServerID)
	if err != nil {
		return apitypes.DomainChange{}, err
	}
	base := c.ingressOf(srv).baseDomain
	if base == "" {
		return apitypes.DomainChange{}, invalid("the server has no base domain, so it can't generate hostnames; add your own domain, or ask a platform operator to set one")
	}
	prefix := ss.svc.Name + "-" + ss.project.Name
	if ss.env.Name != "production" {
		prefix += "-" + ss.env.Name
	}
	for range hostKeyTries {
		host := hostLabel(prefix, newHostKey()) + "." + base
		exists, err := c.store.HostnameExists(ctx, host)
		if err != nil {
			return apitypes.DomainChange{}, err
		}
		if exists {
			continue
		}
		// Under a platform zone, on a tunnel server, Dock routes it
		// through the tunnel like a domain from Cloudflare (no wildcard).
		var zone *store.CloudflareZone
		if _, managed := c.zoneFor(0, host); managed && c.ingressOf(srv).mode == apitypes.IngressCloudflareTunnel {
			if zone, err = c.cloudflareZoneFor(ctx, a, ss, host); err != nil {
				return apitypes.DomainChange{}, err
			}
		}
		return c.addDomain(ctx, a, ss, host, "", true, zone)
	}
	return apitypes.DomainChange{}, conflict("no free generated hostname under %s after %d tries; try again", base, hostKeyTries)
}

// checkNewDomain refuses host for a service before anything is set up
// for it: an archived service, or one of Dock's own names (checkOwnHost).
func (c *Core) checkNewDomain(ss svcScope, host string) error {
	if ss.svc.ArchivedAt != nil {
		return invalid("service %s is archived", ss)
	}
	return c.checkOwnHost(host)
}

// checkOwnHost refuses a hostname Dock answers itself: the webhook
// hostname, Dock's address, a public analytics dashboard or a status page.
func (c *Core) checkOwnHost(host string) error {
	switch {
	case host == c.cfg.Hooks.PublicHost:
		return errf(apitypes.CodeHostnameTaken, "%s is this server's webhook hostname ([hooks] public_host); use another hostname", host)
	case host == c.publicHost():
		return errf(apitypes.CodeHostnameTaken, "%s is Dock's own address (`dock address`); use another hostname", host)
	case c.publicSite(host) != 0:
		return errf(apitypes.CodeHostnameTaken, "%s is a site's public dashboard (`dock analytics site`); use another hostname", host)
	}
	if _, ok := c.statusPageHost(host); ok {
		return errf(apitypes.CodeHostnameTaken, "%s is a status page's address (`dock status-page list`); use another hostname", host)
	}
	return nil
}

// addDomain adds host to a service; zone, when set, is the Cloudflare
// zone Dock routes it through the server's tunnel from (ADR 0036). A
// claim that needs no proof (needsProof) is verified at once, and other
// orgs' unverified claims on host lose; any other waits for its TXT
// record (proveDomain).
func (c *Core) addDomain(ctx context.Context, a Actor, ss svcScope, host, redirect string, generated bool, zone *store.CloudflareZone) (apitypes.DomainChange, error) {
	if err := c.checkNewDomain(ss, host); err != nil {
		return apitypes.DomainChange{}, err
	}
	ing, err := c.envIngress(ctx, ss.envScope)
	if err != nil {
		return apitypes.DomainChange{}, err
	}
	params := store.CreateDomainParams{Hostname: host, ServiceID: ss.svc.ID, Generated: generated, RedirectTo: redirect}
	if needsProof(a, host, generated, zone, ing) {
		params.VerifyToken = newVerifyToken()
	} else {
		t := now()
		params.VerifiedAt = &t
	}
	var d store.Domain
	var lost []int64
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		claims, err := q.ListHostnameClaims(ctx, host)
		if err != nil {
			return err
		}
		if err := checkClaims(claims, ss.project.OrgID, host); err != nil {
			return err
		}
		if redirect, err := q.RedirectHostnameExists(ctx, host); err != nil {
			return err
		} else if redirect {
			return errf(apitypes.CodeHostnameTaken, "%s is a Cloudflare redirect; remove the redirect first (its level's Cloudflare page, or `dock cloudflare redirects remove %s`)", host, host)
		}
		if d, err = q.CreateDomain(ctx, params); err != nil {
			return err
		}
		if d.VerifiedAt != nil {
			if lost, err = outclaim(ctx, q, d, claims); err != nil {
				return err
			}
		}
		if zone == nil {
			return nil
		}
		// Dock routes it through the server's tunnel (ADR 0036). A
		// live domain's record with another connection, maybe another
		// org's, is never taken over.
		saved, err := q.SaveCloudflareRecord(ctx, store.SaveCloudflareRecordParams{
			Hostname: host, DomainID: &d.ID, ServerID: &ss.env.ServerID,
			ConnectionID: zone.ConnectionID, ZoneID: zone.ZoneID, UpdatedAt: now(),
		})
		if err == nil && saved == 0 {
			return errf(apitypes.CodeHostnameTaken, "Dock routes %s through Cloudflare for another domain already; use another hostname", host)
		}
		return err
	})
	if store.IsUnique(err) {
		return apitypes.DomainChange{}, errf(apitypes.CodeHostnameTaken, "%s is already in use by a service; a hostname can belong to only one service", host)
	}
	if err != nil {
		return apitypes.DomainChange{}, err
	}
	c.audit(ctx, a, "domain.add", &ss.project, &ss.env, ss.String(), "added domain "+host)
	c.tellLosers(ctx, host, lost)
	if zone != nil {
		c.kickCloudflare()
	}

	var out apitypes.DomainChange
	// A running service whose port isn't published yet needs a release that
	// publishes it before the domain can route.
	if active, err := c.store.GetActiveRelease(ctx, ss.svc.ID); err == nil && active.HostPort == 0 && redirect == "" {
		rel, err := c.startRelease(ctx, a, ss, releaseSpec{source: apitypes.SourceConfig})
		switch {
		case err == nil:
			r, err := c.toRelease(ctx, ss, rel, false)
			if err != nil {
				return out, err
			}
			out.Release = &r
		case CodeOf(err) == apitypes.CodeDeployInProgress:
			// The in-flight deploy resolves config after this insert only if
			// it hasn't yet; the next deploy publishes the port either way.
			// Its cutover may have rendered routes before this insert while
			// its release didn't read as active yet: routes are applied
			// here too, under routesMu, so they wait for that cutover and
			// include the domain once its release has a port.
			if err := c.applyRoutes(ctx, ss.env.ServerID); err != nil {
				c.log.Error("apply routes after adding domain", "domain", host, "err", err)
			}
		default:
			return out, err
		}
	} else if err := c.applyRoutes(ctx, ss.env.ServerID); err != nil {
		c.log.Error("apply routes after adding domain", "domain", host, "err", err)
	}
	c.kickDomains()
	if d.VerifiedAt != nil && redirect == "" {
		c.autoUptimeChecks(ctx, ss, host)
	}
	rows, err := c.store.ListDomainsByService(ctx, ss.svc.ID)
	if err != nil {
		return out, err
	}
	out.Domain = c.toDomain(ss, d, rows, ing)
	if r, err := c.store.GetCloudflareRecord(ctx, host); err == nil {
		c.withRoute(&out.Domain, &r)
	}
	c.publish(ss, apitypes.Event{Type: apitypes.EventDomain, Domain: &out.Domain})
	return out, nil
}

// RemoveDomain removes a hostname from a service and its routes, and the
// DNS record Dock made for it.
func (c *Core) RemoveDomain(ctx context.Context, a Actor, ref ServiceRef, hostname string) error {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "removing a domain")
	if err != nil {
		return err
	}
	host, err := normalizeHostname(hostname)
	if err != nil {
		return err
	}
	d, err := c.store.GetServiceDomain(ctx, store.GetServiceDomainParams{Hostname: host, ServiceID: ss.svc.ID})
	if store.IsNotFound(err) {
		return notFound("%s is not a domain of %s", host, ss)
	}
	if err != nil {
		return err
	}
	// Dock deletes the DNS record it made for it (ADR 0036).
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.SetDomainRecordAbsent(ctx, store.SetDomainRecordAbsentParams{UpdatedAt: now(), DomainID: &d.ID}); err != nil {
			return err
		}
		return q.DeleteDomain(ctx, d.ID)
	})
	if err != nil {
		return err
	}
	c.kickCloudflare()
	if err := c.applyRoutes(ctx, ss.env.ServerID); err != nil {
		c.log.Error("apply routes after removing domain", "domain", host, "err", err)
	}
	c.audit(ctx, a, "domain.remove", &ss.project, &ss.env, ss.String(), "removed domain "+host)
	c.dropUptimeChecks(ctx, ss, host)
	dom := c.toDomain(ss, d, nil, serverIngress{})
	c.checksMu.Lock()
	delete(c.checks, d.ID)
	c.checksMu.Unlock()
	c.publish(ss, apitypes.Event{Type: apitypes.EventDomain, Domain: &dom, Message: "removed"})
	return nil
}

// AllowCertificate answers the local Caddy's on-demand TLS `ask`: only
// verified hostnames of non-archived services on this server get
// certificates.
// Remote servers answer their own Caddy from their last routes.
func (c *Core) AllowCertificate(ctx context.Context, hostname string) (bool, error) {
	host, err := normalizeHostname(hostname)
	if err != nil {
		return false, nil
	}
	if host == c.cfg.Hooks.PublicHost || host == c.publicHost() || c.publicSite(host) != 0 {
		return true, nil // the webhook route (ADR 0018), Dock's address (ADR 0029), a public dashboard (ADR 0037)
	}
	if p, ok := c.statusPageHost(host); ok {
		return p.published && c.cfg.Status.Listen != "", nil // a status page (ADR 0073)
	}
	d, err := c.store.GetDomainByHostname(ctx, host)
	if store.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ss, err := c.scopeOf(ctx, d.ServiceID)
	if err != nil {
		return false, err
	}
	if ss.svc.ArchivedAt != nil || ss.env.ServerID != c.localID {
		return false, nil
	}
	// Served to the Cloudflare tunnel only: Cloudflare's edge has its
	// certificate (ADR 0036).
	if r, err := c.store.GetCloudflareRecord(ctx, host); err == nil && r.Want == cfPresent {
		return false, nil
	} else if err != nil && !store.IsNotFound(err) {
		return false, err
	}
	return true, nil
}

// toDomain converts a domain served by a server with ingress ing;
// siblings (the service's other domains) are used to offer a CNAME to a
// generated domain. Domains are https:// except on a direct server with
// TLS disabled; on a tunnel server Cloudflare's edge terminates TLS, and
// DNS is a CNAME to the tunnel.
func (c *Core) toDomain(ss svcScope, d store.Domain, siblings []store.Domain, ing serverIngress) apitypes.Domain {
	scheme := "https://"
	if ing.plainHTTP {
		scheme = "http://"
	}
	out := apitypes.Domain{
		Hostname: d.Hostname, URL: scheme + d.Hostname, Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name,
		Generated: d.Generated, RedirectTo: d.RedirectTo, Status: apitypes.DomainStatus(d.Status),
		StatusReason: d.StatusReason, DNSRecords: []apitypes.DNSRecord{}, CreatedAt: d.CreatedAt,
		Verified: d.VerifiedAt != nil,
	}
	if !out.Verified {
		rec := verifyRecord(d)
		out.Verification = &rec
	}
	c.checksMu.Lock()
	if check, ok := c.checks[d.ID]; ok {
		out.CheckedAt, out.ResolvedIPs = &check.at, check.resolved
	}
	c.checksMu.Unlock()
	if d.Generated && ing.baseDomain != "" && strings.HasSuffix(d.Hostname, "."+ing.baseDomain) {
		return out // covered by the wildcard record for the base domain
	}
	if ing.mode == apitypes.IngressCloudflareTunnel {
		if ing.tunnel != "" {
			out.DNSRecords = append(out.DNSRecords, apitypes.DNSRecord{Type: "CNAME", Name: d.Hostname, Value: ing.tunnel})
		}
	} else {
		for _, ip := range ing.publicIPs {
			out.DNSRecords = append(out.DNSRecords, apitypes.DNSRecord{Type: ipRecordType(ip), Name: d.Hostname, Value: ip})
		}
	}
	// Subdomains can CNAME to a generated domain instead (apexes can't).
	if strings.Count(d.Hostname, ".") >= 2 {
		for _, s := range siblings {
			if s.Generated {
				out.DNSRecords = append(out.DNSRecords, apitypes.DNSRecord{Type: "CNAME", Name: d.Hostname, Value: s.Hostname})
				break
			}
		}
	}
	return out
}

func (c *Core) kickDomains() {
	select {
	case c.domainKick <- struct{}{}:
	default:
	}
}

// Domain checks back off from 5s to 5min per domain. With nothing pending
// the checker sleeps until a domain is added.
const (
	domainCheckMin = 5 * time.Second
	domainCheckMax = 5 * time.Minute
	// domainServerTimeout bounds one round of a server's checks; the
	// ones it didn't reach run in the next round.
	domainServerTimeout = time.Minute
)

// checkDomains moves domains through waiting_for_dns → issuing_certificate
// → active: a custom domain's TXT record first (proveDomain), then the
// runtime's DNS and certificate checks.
func (c *Core) checkDomains() {
	defer c.wg.Done()
	type backoff struct {
		next  time.Time
		delay time.Duration
		// dnsOK is when DNS was first seen right, i.e. when the first
		// HTTPS handshake asked Caddy to issue a certificate.
		dnsOK time.Time
		// verified is whether the domain was verified when last seen: one
		// verified since (CheckDomainNow) starts over at the minimum.
		verified bool
	}
	type check struct {
		d store.Domain
		b *backoff
	}
	state := map[int64]*backoff{}
	for {
		pending, err := c.store.ListPendingDomains(c.ctx)
		if err != nil && c.ctx.Err() == nil {
			c.log.Error("list pending domains", "err", err)
		}
		seen := map[int64]bool{}
		// Due checks by server, each server's in turn and the servers at
		// once (fanOut): one that doesn't answer delays only its own.
		byServer := map[int64][]check{}
		for _, d := range pending {
			seen[d.ID] = true
			b := state[d.ID]
			if b == nil {
				b = &backoff{delay: domainCheckMin}
				state[d.ID] = b
			}
			if v := d.VerifiedAt != nil; v != b.verified {
				b.verified, b.delay, b.next = v, domainCheckMin, time.Time{}
			}
			if !time.Now().Before(b.next) {
				var server int64
				if ss, err := c.scopeOf(c.ctx, d.ServiceID); err == nil {
					server = ss.env.ServerID
				}
				byServer[server] = append(byServer[server], check{d, b})
			}
		}
		groups := slices.Collect(maps.Values(byServer))
		fanOut(c.ctx, len(groups), domainServerTimeout, func(ctx context.Context, i int) {
			for _, ch := range groups[i] {
				c.checkDomain(ctx, ch.d, &ch.b.dnsOK)
				ch.b.next = time.Now().Add(ch.b.delay)
				ch.b.delay = min(ch.b.delay*2, domainCheckMax)
			}
		})
		var wake time.Time
		for _, d := range pending {
			if b := state[d.ID]; wake.IsZero() || b.next.Before(wake) {
				wake = b.next
			}
		}
		for id := range state {
			if !seen[id] {
				delete(state, id)
			}
		}
		var timer <-chan time.Time
		var t *time.Timer
		if !wake.IsZero() {
			t = time.NewTimer(time.Until(wake))
			timer = t.C
		}
		select {
		case <-c.ctx.Done():
		case <-c.domainKick:
		case <-timer:
		}
		if t != nil {
			t.Stop()
		}
		if c.ctx.Err() != nil {
			return
		}
	}
}

// holdDomainCheck waits until no other check of domain id runs and
// claims it; release ends the claim. False if ctx ends first.
func (c *Core) holdDomainCheck(ctx context.Context, id int64) (release func(), ok bool) {
	for {
		c.checksMu.Lock()
		running, busy := c.checking[id]
		if !busy {
			done := make(chan struct{})
			c.checking[id] = done
			c.checksMu.Unlock()
			return func() {
				c.checksMu.Lock()
				delete(c.checking, id)
				c.checksMu.Unlock()
				close(done)
			}, true
		}
		c.checksMu.Unlock()
		select {
		case <-running:
		case <-ctx.Done():
			return nil, false
		}
	}
}

// domainCheck is when a domain was last checked and what its hostname
// resolved to.
type domainCheck struct {
	at       time.Time
	resolved []string
}

// certPatience is how long a certificate may stay pending after DNS is
// right before the domain shows an error.
const certPatience = 10 * time.Minute

// checkDomain updates one domain's status. dnsOK tracks when its DNS was
// first seen right, to tell a pending certificate from one that never comes.
// A domain not yet verified is checked for its TXT record only, and goes
// on to the DNS and certificate check once it verifies.
func (c *Core) checkDomain(ctx context.Context, d store.Domain, dnsOK *time.Time) {
	// One check of a domain at a time, each reading the row as the one
	// before left it: the checker lists its domains at the start of a
	// round, and a check from that round would otherwise save what DNS
	// said before a CheckDomainNow that ran meanwhile.
	release, ok := c.holdDomainCheck(ctx, d.ID)
	if !ok {
		return
	}
	defer release()
	d, err := c.store.GetServiceDomain(ctx, store.GetServiceDomainParams{Hostname: d.Hostname, ServiceID: d.ServiceID})
	if err != nil {
		return // removed meanwhile, or shutting down
	}
	ss, err := c.scopeOf(ctx, d.ServiceID)
	if err != nil || ss.svc.ArchivedAt != nil {
		return
	}
	ing, err := c.envIngress(ctx, ss.envScope)
	if err != nil {
		return
	}
	if d.VerifiedAt == nil {
		status, reason, ok := c.proveDomain(ctx, ss, &d)
		if ctx.Err() != nil {
			return
		}
		if !ok {
			c.saveDomainStatus(ctx, ss, ing, d, status, reason, nil)
			return
		}
	}
	check, err := c.runtime(ss.env.ServerID).CheckDomain(ctx, d.Hostname)
	// A domain Dock routes through the server's tunnel (ADR 0036).
	var route *store.CloudflareRecord
	if r, rerr := c.store.GetCloudflareRecord(ctx, d.Hostname); rerr == nil && r.Want == cfPresent {
		route = &r
	}
	status := apitypes.DomainStatus(d.Status)
	var reason string
	switch {
	case ctx.Err() != nil: // shutting down, or the round's time is up
		return
	case err != nil:
		reason = "could not check: " + c.nodeErr(ss.env.ServerID, err).Error()
	case !check.DNSOK:
		*dnsOK = time.Time{}
		status = apitypes.DomainWaitingForDNS
		reason = check.Error
		switch {
		case route != nil && route.Status == "pending":
			reason = "Dock is setting up its route through the server's Cloudflare tunnel"
		case route != nil:
			// The node's advice is for records to its IP; this one goes
			// through the tunnel.
			reason = fmt.Sprintf("%s doesn't reach this server through its Cloudflare tunnel yet; DNS can take a minute, else check that dock-cloudflared runs on it", d.Hostname)
		case reason == "":
			reason = fmt.Sprintf("%s resolves to %v; expected %v", d.Hostname, check.ResolvedIPs, check.ExpectedIPs)
		}
	case ing.plainHTTP || check.CertificateOK:
		status, reason = apitypes.DomainActive, ""
	case check.Error != "":
		status, reason = apitypes.DomainError, check.Error
	case dnsOK.IsZero() || time.Since(*dnsOK) < certPatience:
		if dnsOK.IsZero() {
			*dnsOK = time.Now()
		}
		status, reason = apitypes.DomainIssuingCertificate, ""
	default:
		status, reason = apitypes.DomainError, fmt.Sprintf("no certificate after %s; check that ports 80 and 443 reach this server and see Caddy's log", certPatience)
	}
	if err == nil {
		c.checksMu.Lock()
		c.checks[d.ID] = domainCheck{at: now(), resolved: slices.Clone(check.ResolvedIPs)}
		c.checksMu.Unlock()
	}
	c.saveDomainStatus(ctx, ss, ing, d, status, reason, route)
}

// saveDomainStatus stores a checked domain's status when it changed and
// publishes the domain; route is its Cloudflare record, nil for none.
func (c *Core) saveDomainStatus(ctx context.Context, ss svcScope, ing serverIngress, d store.Domain, status apitypes.DomainStatus, reason string, route *store.CloudflareRecord) {
	if string(status) != d.Status || reason != d.StatusReason {
		if err := c.store.UpdateDomainStatus(ctx, store.UpdateDomainStatusParams{Status: string(status), StatusReason: reason, ID: d.ID}); err != nil {
			c.log.Error("update domain status", "domain", d.Hostname, "err", err)
			return
		}
		d.Status, d.StatusReason = string(status), reason
	}
	// Published after every check, so clients see when it last ran.
	siblings, _ := c.store.ListDomainsByService(ctx, d.ServiceID)
	dom := c.toDomain(ss, d, siblings, ing)
	c.withRoute(&dom, route)
	c.publish(ss, apitypes.Event{Type: apitypes.EventDomain, Domain: &dom})
}

// ipRecordType is the DNS record type pointing at ip: AAAA for IPv6, A
// otherwise.
func ipRecordType(ip string) string {
	if strings.Contains(ip, ":") {
		return "AAAA"
	}
	return "A"
}
