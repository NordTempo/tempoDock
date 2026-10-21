package core

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/platform/internal/apitypes"
	"github.com/NordTempo/platform/internal/store"
)

var labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// normalizeHostname lowercases and validates a hostname.
func normalizeHostname(h string) (string, error) {
	h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
	labels := strings.Split(h, ".")
	if len(h) > 253 || len(labels) < 2 {
		return "", invalid("%q is not a valid hostname; use a full name like app.example.com", h)
	}
	for _, l := range labels {
		if !labelRe.MatchString(l) {
			return "", invalid("%q is not a valid hostname (label %q)", h, l)
		}
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
	out := make([]apitypes.Domain, 0, len(rows))
	for _, d := range rows {
		out = append(out, c.toDomain(ss, d, rows))
	}
	return out, nil
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

// GenerateDomain gives a service a hostname under the configured base
// domain: <service>-<project>[-<env>].<base>, with -2, -3… on collisions.
func (c *Core) GenerateDomain(ctx context.Context, a Actor, ref ServiceRef) (apitypes.DomainChange, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "adding a domain")
	if err != nil {
		return apitypes.DomainChange{}, err
	}
	if c.cfg.BaseDomain == "" {
		return apitypes.DomainChange{}, invalid("this server has no base_domain configured, so it can't generate hostnames; add your own domain instead")
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
		host := candidate + "." + strings.TrimPrefix(c.cfg.BaseDomain, "*.")
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
	c.audit(ctx, a, "domain.add", &ss.project, &ss.env, host, "added to "+ss.String())

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
	out.Domain = c.toDomain(ss, d, rows)
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
	c.audit(ctx, a, "domain.remove", &ss.project, &ss.env, host, "removed from "+ss.String())
	dom := c.toDomain(ss, d, nil)
	c.publish(ss, apitypes.Event{Type: apitypes.EventDomain, Domain: &dom, Message: "removed"})
	return nil
}

// AllowCertificate answers Caddy's on-demand TLS `ask`: only hostnames of
// non-archived services get certificates.
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
	s, err := c.store.GetService(ctx, d.ServiceID)
	if err != nil {
		return false, err
	}
	return s.ArchivedAt == nil, nil
}

// toDomain converts a domain; siblings (the service's other domains) are
// used to offer a CNAME to a generated domain.
func (c *Core) toDomain(ss svcScope, d store.Domain, siblings []store.Domain) apitypes.Domain {
	out := apitypes.Domain{
		Hostname: d.Hostname, Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name,
		Generated: d.Generated, RedirectTo: d.RedirectTo, Status: apitypes.DomainStatus(d.Status),
		StatusReason: d.StatusReason, DNSRecords: []apitypes.DNSRecord{}, CreatedAt: d.CreatedAt,
	}
	if d.Generated {
		return out // covered by the wildcard record for the base domain
	}
	if c.cfg.PublicIP != "" {
		out.DNSRecords = append(out.DNSRecords, apitypes.DNSRecord{Type: "A", Name: d.Hostname, Value: c.cfg.PublicIP})
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
				c.checkDomain(c.ctx, d)
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

func (c *Core) checkDomain(ctx context.Context, d store.Domain) {
	ss, err := c.scopeOf(ctx, d.ServiceID)
	if err != nil || ss.svc.ArchivedAt != nil {
		return
	}
	check, err := c.runtime(ss.env.ServerID).CheckDomain(ctx, d.Hostname)
	status := apitypes.DomainStatus(d.Status)
	var reason string
	switch {
	case errors.Is(err, context.Canceled):
		return
	case err != nil:
		reason = "could not check: " + err.Error()
	case !check.DNSOK:
		status = apitypes.DomainWaitingForDNS
		reason = check.Error
		if reason == "" {
			reason = fmt.Sprintf("%s resolves to %v; expected %v", d.Hostname, check.ResolvedIPs, check.ExpectedIPs)
		}
	case c.cfg.Caddy.DisableTLS || check.CertificateOK:
		status, reason = apitypes.DomainActive, ""
	case check.Error != "":
		status, reason = apitypes.DomainError, check.Error
	default:
		status, reason = apitypes.DomainIssuingCertificate, ""
	}
	if string(status) == d.Status && reason == d.StatusReason {
		return
	}
	if err := c.store.UpdateDomainStatus(ctx, store.UpdateDomainStatusParams{Status: string(status), StatusReason: reason, ID: d.ID}); err != nil {
		c.log.Error("update domain status", "domain", d.Hostname, "err", err)
		return
	}
	d.Status, d.StatusReason = string(status), reason
	siblings, _ := c.store.ListDomainsByService(ctx, d.ServiceID)
	dom := c.toDomain(ss, d, siblings)
	c.publish(ss, apitypes.Event{Type: apitypes.EventDomain, Domain: &dom})
}
