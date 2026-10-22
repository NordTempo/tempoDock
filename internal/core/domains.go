package core

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
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

func (c *Core) domainsOf(ctx context.Context, ss svcScope) ([]apitypes.Domain, error) {
	rows, err := c.store.ListDomainsByService(ctx, ss.svc.ID)
	if err != nil {
		return nil, err
	}
	ing, err := c.envIngress(ctx, ss.envScope)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.Domain, 0, len(rows))
	for _, d := range rows {
		out = append(out, c.toDomain(ss, d, rows, ing))
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
// service is running; DNS can be set up before or after.
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
	return c.addDomain(ctx, a, ss, host, redirect, false)
}

// GenerateDomain gives a service a hostname under the base domain of its
// environment's server: <service>-<project>[-<env>].<base>, with -2, -3…
// on collisions.
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
		return apitypes.DomainChange{}, invalid("server %s has no base domain, so it can't generate hostnames; set one with `dock server set %s --base-domain <domain>`, or add your own domain", srv.Name, srv.Name)
	}
	label := ss.svc.Name + "-" + ss.project.Name
	if ss.env.Name != "production" {
		label += "-" + ss.env.Name
	}
	label = strings.Trim(label[:min(len(label), 58)], "-")
	for i := 1; ; i++ {
		candidate := label
		if i > 1 {
			candidate += "-" + strconv.Itoa(i)
		}
		host := candidate + "." + base
		exists, err := c.store.HostnameExists(ctx, host)
		if err != nil {
			return apitypes.DomainChange{}, err
		}
		if !exists {
			return c.addDomain(ctx, a, ss, host, "", true)
		}
	}
}

func (c *Core) addDomain(ctx context.Context, a Actor, ss svcScope, host, redirect string, generated bool) (apitypes.DomainChange, error) {
	if ss.svc.ArchivedAt != nil {
		return apitypes.DomainChange{}, invalid("service %s is archived", ss)
	}
	d, err := c.store.CreateDomain(ctx, store.CreateDomainParams{Hostname: host, ServiceID: ss.svc.ID, Generated: generated, RedirectTo: redirect})
	if store.IsUnique(err) {
		return apitypes.DomainChange{}, errf(apitypes.CodeHostnameTaken, "%s is already in use by a service; a hostname can belong to only one service", host)
	}
	if err != nil {
		return apitypes.DomainChange{}, err
	}
	c.audit(ctx, a, "domain.add", &ss.project, &ss.env, ss.String(), "added domain "+host)

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
		default:
			return out, err
		}
	} else if err := c.applyRoutes(ctx, ss.env.ServerID); err != nil {
		c.log.Error("apply routes after adding domain", "domain", host, "err", err)
	}
	c.kickDomains()
	rows, err := c.store.ListDomainsByService(ctx, ss.svc.ID)
	if err != nil {
		return out, err
	}
	ing, err := c.envIngress(ctx, ss.envScope)
	if err != nil {
		return out, err
	}
	out.Domain = c.toDomain(ss, d, rows, ing)
	c.publish(ss, apitypes.Event{Type: apitypes.EventDomain, Domain: &out.Domain})
	return out, nil
}

// RemoveDomain removes a hostname from a service and its routes.
func (c *Core) RemoveDomain(ctx context.Context, a Actor, ref ServiceRef, hostname string) error {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "removing a domain")
	if err != nil {
		return err
	}
	host, err := normalizeHostname(hostname)
	if err != nil {
		return err
	}
	d, err := c.store.GetDomainByHostname(ctx, host)
	if store.IsNotFound(err) || (err == nil && d.ServiceID != ss.svc.ID) {
		return notFound("%s is not a domain of %s", host, ss)
	}
	if err != nil {
		return err
	}
	if err := c.store.DeleteDomain(ctx, d.ID); err != nil {
		return err
	}
	if err := c.applyRoutes(ctx, ss.env.ServerID); err != nil {
		c.log.Error("apply routes after removing domain", "domain", host, "err", err)
	}
	c.audit(ctx, a, "domain.remove", &ss.project, &ss.env, ss.String(), "removed domain "+host)
	dom := c.toDomain(ss, d, nil, serverIngress{})
	c.checksMu.Lock()
	delete(c.checks, d.ID)
	c.checksMu.Unlock()
	c.publish(ss, apitypes.Event{Type: apitypes.EventDomain, Domain: &dom, Message: "removed"})
	return nil
}

// AllowCertificate answers the local Caddy's on-demand TLS `ask`: only
// hostnames of non-archived services on this server get certificates.
// Remote servers answer their own Caddy from their last routes.
func (c *Core) AllowCertificate(ctx context.Context, hostname string) (bool, error) {
	host, err := normalizeHostname(hostname)
	if err != nil {
		return false, nil
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
	return ss.svc.ArchivedAt == nil && ss.env.ServerID == c.local.ID, nil
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
	}
	c.checksMu.Lock()
	if check, ok := c.checks[d.ID]; ok {
		out.CheckedAt, out.ResolvedIPs = &check.at, check.resolved
	}
	c.checksMu.Unlock()
	if d.Generated {
		return out // covered by the wildcard record for the base domain
	}
	if ing.mode == apitypes.IngressCloudflareTunnel {
		if ing.tunnel != "" {
			out.DNSRecords = append(out.DNSRecords, apitypes.DNSRecord{Type: "CNAME", Name: d.Hostname, Value: ing.tunnel})
		}
	} else {
		for _, ip := range ing.publicIPs {
			typ := "A"
			if strings.Contains(ip, ":") {
				typ = "AAAA"
			}
			out.DNSRecords = append(out.DNSRecords, apitypes.DNSRecord{Type: typ, Name: d.Hostname, Value: ip})
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
)

// checkDomains moves domains through waiting_for_dns → issuing_certificate
// → active using the runtime's DNS and certificate checks.
func (c *Core) checkDomains() {
	defer c.wg.Done()
	type backoff struct {
		next  time.Time
		delay time.Duration
		// dnsOK is when DNS was first seen right, i.e. when the first
		// HTTPS handshake asked Caddy to issue a certificate.
		dnsOK time.Time
	}
	state := map[int64]*backoff{}
	for {
		pending, err := c.store.ListPendingDomains(c.ctx)
		if err != nil && c.ctx.Err() == nil {
			c.log.Error("list pending domains", "err", err)
		}
		seen := map[int64]bool{}
		var wake time.Time
		for _, d := range pending {
			seen[d.ID] = true
			b := state[d.ID]
			if b == nil {
				b = &backoff{delay: domainCheckMin}
				state[d.ID] = b
			}
			if !time.Now().Before(b.next) {
				c.checkDomain(c.ctx, d, &b.dnsOK)
				b.next = time.Now().Add(b.delay)
				b.delay = min(b.delay*2, domainCheckMax)
			}
			if wake.IsZero() || b.next.Before(wake) {
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
func (c *Core) checkDomain(ctx context.Context, d store.Domain, dnsOK *time.Time) {
	ss, err := c.scopeOf(ctx, d.ServiceID)
	if err != nil || ss.svc.ArchivedAt != nil {
		return
	}
	ing, err := c.envIngress(ctx, ss.envScope)
	if err != nil {
		return
	}
	check, err := c.runtime(ss.env.ServerID).CheckDomain(ctx, d.Hostname)
	status := apitypes.DomainStatus(d.Status)
	var reason string
	switch {
	case errors.Is(err, context.Canceled):
		return
	case err != nil:
		reason = "could not check: " + c.nodeErr(ss.env.ServerID, err).Error()
	case !check.DNSOK:
		*dnsOK = time.Time{}
		status = apitypes.DomainWaitingForDNS
		reason = check.Error
		if reason == "" {
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
	c.publish(ss, apitypes.Event{Type: apitypes.EventDomain, Domain: &dom})
}
