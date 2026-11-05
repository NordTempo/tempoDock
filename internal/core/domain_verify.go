package core

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Custom domain verification (ADR 0042). A custom hostname routes and
// gets a certificate only once the org that added it proves it controls
// the name: a TXT record at _dock-challenge.<hostname> holding the
// domain's token. Until one does, several orgs may claim a hostname; the
// first to verify holds it and the others' claims show an error. The
// domain checker (checkDomain) looks for the record with every check, and
// CheckDomainNow on request.

const (
	// verifyPrefix is the label the TXT record goes under.
	verifyPrefix = "_dock-challenge."
	// verifyValuePrefix starts the record's value, so it reads as Dock's
	// among a zone's other TXT records.
	verifyValuePrefix = "dock-verify="
	// verifyLookupTimeout bounds one TXT lookup.
	verifyLookupTimeout = 10 * time.Second
)

// newVerifyToken is a new domain's random verification token.
func newVerifyToken() string { return strings.ToLower(rand.Text()) }

// verifyRecord is the TXT record that proves control of d's hostname.
func verifyRecord(d store.Domain) apitypes.DNSRecord {
	return apitypes.DNSRecord{Type: "TXT", Name: verifyPrefix + d.Hostname, Value: verifyValuePrefix + d.VerifyToken}
}

// needsProof reports whether a's claim on host must be proven with a TXT
// record. Not when Dock knows who controls the name already: generated
// hostnames and any under the server's base domain are the platform's,
// Dock makes the DNS records of a domain from Cloudflare itself, and an
// operator runs the proxy anyway.
func needsProof(a Actor, host string, generated bool, zone *store.CloudflareZone, ing serverIngress) bool {
	platform := ing.baseDomain != "" && strings.HasSuffix(host, "."+ing.baseDomain)
	return !generated && zone == nil && !a.Operator && !platform
}

// checkClaims refuses host for ss when it is taken: by a verified claim
// anywhere, or by any claim in ss's own org. Other orgs' unverified claims
// don't block a new one.
func checkClaims(claims []store.ListHostnameClaimsRow, ss svcScope, host string) error {
	for _, cl := range claims {
		switch {
		case cl.OrgID == ss.project.OrgID:
			return errf(apitypes.CodeHostnameTaken, "%s is already in use by a service; a hostname can belong to only one service", host)
		case cl.VerifiedAt != nil:
			return errf(apitypes.CodeHostnameTaken, "%s belongs to another organization, which proved it controls it; use another hostname", host)
		}
	}
	return nil
}

// lostReason is the status reason of a claim another org verified first.
func lostReason(host string) string {
	return fmt.Sprintf("another organization proved it controls %s, so it routes there; remove this domain", host)
}

// outclaim marks the other claims on a hostname that d now holds as lost
// and returns their service IDs, for tellLosers once the transaction
// commits.
func outclaim(ctx context.Context, q *store.Queries, d store.Domain, claims []store.ListHostnameClaimsRow) ([]int64, error) {
	var lost []int64
	for _, cl := range claims {
		if cl.ID == d.ID {
			continue
		}
		if err := q.UpdateDomainStatus(ctx, store.UpdateDomainStatusParams{Status: string(apitypes.DomainError), StatusReason: lostReason(d.Hostname), ID: cl.ID}); err != nil {
			return nil, err
		}
		lost = append(lost, cl.ServiceID)
	}
	return lost, nil
}

// tellLosers tells the orgs whose claims on host lost (outclaim): an
// audit event in each one's project and the domain's new status. The
// domain error also raises their domain alert.
func (c *Core) tellLosers(ctx context.Context, host string, services []int64) {
	for _, id := range services {
		ss, err := c.scopeOf(ctx, id)
		if err != nil {
			continue
		}
		c.audit(ctx, System.InOrg(ss.project.OrgID), "domain.lost", &ss.project, &ss.env, ss.String(), lostReason(host))
		doms, err := c.domainsOf(ctx, ss)
		if err != nil {
			continue
		}
		for i := range doms {
			if doms[i].Hostname == host {
				c.publish(ss, apitypes.Event{Type: apitypes.EventDomain, Domain: &doms[i]})
			}
		}
	}
}

// proveDomain looks for the TXT record of d, a claim not yet verified,
// and verifies it when the record holds its token: from then on it
// routes, and other orgs' claims on the hostname lose. Otherwise it
// returns the status and reason d shows. ok with d.VerifiedAt set means
// it verified now.
func (c *Core) proveDomain(ctx context.Context, ss svcScope, d *store.Domain) (status apitypes.DomainStatus, reason string, ok bool) {
	if err := c.checkNewDomain(ss, d.Hostname); err != nil {
		return apitypes.DomainError, err.Error(), false
	}
	switch _, err := c.store.GetDomainByHostname(ctx, d.Hostname); {
	case err == nil:
		return apitypes.DomainError, lostReason(d.Hostname), false
	case !store.IsNotFound(err):
		return apitypes.DomainStatus(d.Status), "could not check: " + err.Error(), false
	}
	rec := verifyRecord(*d)
	lctx, cancel := context.WithTimeout(ctx, verifyLookupTimeout)
	txts, err := c.lookupTXT(lctx, rec.Name)
	cancel()
	add := fmt.Sprintf("add a TXT record %s with the value %s to prove you control %s", rec.Name, rec.Value, d.Hostname)
	if dnsErr, isDNS := errors.AsType[*net.DNSError](err); err != nil && (!isDNS || !dnsErr.IsNotFound) {
		return apitypes.DomainWaitingForDNS, fmt.Sprintf("could not look up %s (%v); %s", rec.Name, err, add), false
	}
	if !slices.ContainsFunc(txts, func(t string) bool { return strings.TrimSpace(t) == rec.Value }) {
		if len(txts) > 0 {
			add = fmt.Sprintf("the TXT record %s doesn't hold %s yet; DNS can take a few minutes", rec.Name, rec.Value)
		}
		return apitypes.DomainWaitingForDNS, add, false
	}

	t := now()
	var lost []int64
	var verified bool
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		claims, err := q.ListHostnameClaims(ctx, d.Hostname)
		if err != nil {
			return err
		}
		n, err := q.VerifyDomain(ctx, store.VerifyDomainParams{VerifiedAt: &t, ID: d.ID})
		if err != nil || n == 0 { // n == 0: a check running at once verified it
			return err
		}
		verified = true
		lost, err = outclaim(ctx, q, *d, claims)
		return err
	})
	switch {
	case store.IsUnique(err): // another org verified it a moment ago
		return apitypes.DomainError, lostReason(d.Hostname), false
	case err != nil:
		return apitypes.DomainStatus(d.Status), "could not check: " + err.Error(), false
	}
	d.VerifiedAt = &t
	if !verified {
		return apitypes.DomainStatus(d.Status), d.StatusReason, true
	}
	a := System.InOrg(ss.project.OrgID)
	c.audit(ctx, a, "domain.verify", &ss.project, &ss.env, ss.String(), "verified domain "+d.Hostname+" with its TXT record")
	c.tellLosers(ctx, d.Hostname, lost)
	// Its analytics come along now that it is the org's (AddDomain), and
	// it gets an uptime check (ADR 0059).
	if d.RedirectTo == "" {
		c.autoUptimeChecks(ctx, ss, d.Hostname)
		if moved, err := c.siteDomainAdded(ctx, a, ss, d.Hostname, ""); err != nil {
			c.log.Error("analytics: carry a site over to a domain", "domain", d.Hostname, "err", err)
		} else if moved {
			c.analyticsSitesChanged(ctx)
		}
	}
	if err := c.applyRoutes(ctx, ss.env.ServerID); err != nil {
		c.log.Error("apply routes after verifying a domain", "domain", d.Hostname, "err", err)
	}
	return apitypes.DomainStatus(d.Status), d.StatusReason, true
}

// CheckDomainNow checks a domain at once instead of at its next backoff:
// its TXT record while it isn't verified, then its DNS and certificate.
// It returns the domain as it is after the check.
func (c *Core) CheckDomainNow(ctx context.Context, a Actor, ref ServiceRef, hostname string) (apitypes.Domain, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "checking a domain")
	if err != nil {
		return apitypes.Domain{}, err
	}
	host, err := normalizeHostname(hostname)
	if err != nil {
		return apitypes.Domain{}, err
	}
	d, err := c.store.GetServiceDomain(ctx, store.GetServiceDomainParams{Hostname: host, ServiceID: ss.svc.ID})
	if store.IsNotFound(err) {
		return apitypes.Domain{}, notFound("%s is not a domain of %s", host, ss)
	}
	if err != nil {
		return apitypes.Domain{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, domainServerTimeout)
	var dnsOK time.Time
	c.checkDomain(cctx, d, &dnsOK)
	cancel()
	c.kickDomains()
	doms, err := c.domainsOf(ctx, ss)
	if err != nil {
		return apitypes.Domain{}, err
	}
	for _, dom := range doms {
		if dom.Hostname == host {
			return dom, nil
		}
	}
	return apitypes.Domain{}, notFound("%s is not a domain of %s", host, ss)
}
