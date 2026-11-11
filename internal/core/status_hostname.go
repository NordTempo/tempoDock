package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Custom hostnames for status pages (ADR 0073, phase 4). A page may also
// answer on a hostname of its org's, such as status.acme.com, pointed at
// its own address with a CNAME. Like a service's custom domain (ADR
// 0042), it is served and gets certificates only once the org proves it
// controls the name with a TXT record, unless Dock makes its record
// itself in a Cloudflare zone the org may use (ADR 0036): then it goes
// through the control plane's tunnel and is verified at once. Unverified
// claims don't block each other; the first page to verify holds it.
//
// There is no checker for it: Dock looks for the TXT record when the
// hostname is set and whenever someone asks (CheckStatusHostname).

// setStatusHostname changes a page's custom hostname to host ("" removes
// it) within UpdateStatusPage, and says what changed ("" for nothing).
func (c *Core) setStatusHostname(ctx context.Context, a Actor, p store.StatusPage, host string) (string, error) {
	cur := ""
	if p.Hostname != nil {
		cur = *p.Hostname
	}
	if host == "" {
		if cur == "" {
			return "", nil
		}
		err := c.store.Tx(ctx, func(q *store.Queries) error {
			if err := q.SetStatusPageRecordAbsent(ctx, store.SetStatusPageRecordAbsentParams{UpdatedAt: now(), StatusPageID: &p.ID}); err != nil {
				return err
			}
			_, err := q.SetStatusPageHostname(ctx, store.SetStatusPageHostnameParams{ID: p.ID})
			return err
		})
		if err != nil {
			return "", err
		}
		c.kickCloudflare()
		return "removed hostname " + cur, nil
	}
	host, err := normalizeHostname(host)
	if err != nil {
		return "", err
	}
	if host == cur {
		return "", nil
	}
	srv, err := c.store.GetLocalServer(ctx)
	if err != nil {
		return "", err
	}
	ing := c.ingressOf(srv)
	if host == statusHostname(p.Slug, ing.baseDomain) {
		return "", invalid("%s is the page's own address already; a custom hostname is one of yours, like status.example.com", host)
	}
	if ing.baseDomain != "" && strings.HasPrefix(host, "status-") && strings.HasSuffix(host, "."+ing.baseDomain) {
		// Pages' own addresses, taken or not: another org's page may get it.
		return "", errf(apitypes.CodeHostnameTaken, "%s is a status page's own address; a custom hostname is one of yours, like status.example.com", host)
	}
	if err := c.checkStatusHost(ctx, host); err != nil {
		return "", err
	}
	if _, ok := c.statusPageHost(host); ok {
		return "", errf(apitypes.CodeHostnameTaken, "%s is another status page's address; pick another hostname", host)
	}
	// Dock makes the record itself in a zone the org may use, when the
	// control plane can take a tunnel; otherwise the org adds it.
	var zone *store.CloudflareZone
	if _, ok := c.zoneFor(a.OrgID, host); ok && c.tunnelPort(srv) != 0 {
		z, err := c.prepareCloudflareDomain(ctx, a, srv, a.OrgID, host)
		if err != nil {
			return "", err
		}
		zone = &z
	}
	token := newVerifyToken()
	var verified bool
	t := now()
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if taken, err := q.RedirectHostnameExists(ctx, host); err != nil {
			return err
		} else if taken {
			return errf(apitypes.CodeHostnameTaken, "%s is a Cloudflare redirect; pick another hostname", host)
		}
		if err := q.SetStatusPageRecordAbsent(ctx, store.SetStatusPageRecordAbsentParams{UpdatedAt: t, StatusPageID: &p.ID}); err != nil {
			return err
		}
		params := store.SetStatusPageHostnameParams{Hostname: &host, VerifyToken: token, ID: p.ID}
		if !needsProof(a, host, false, zone, ing) {
			params.VerifiedAt, verified = &t, true
		}
		if _, err := q.SetStatusPageHostname(ctx, params); err != nil {
			return err
		}
		if zone == nil {
			return nil
		}
		saved, err := q.SaveStatusPageRecord(ctx, store.SaveStatusPageRecordParams{Hostname: host, StatusPageID: &p.ID, ServerID: &srv.ID,
			ConnectionID: zone.ConnectionID, ZoneID: zone.ZoneID, UpdatedAt: t})
		if err == nil && saved == 0 {
			return errf(apitypes.CodeHostnameTaken, "Dock routes %s through Cloudflare for something else already; pick another hostname", host)
		}
		return err
	})
	if store.IsUnique(err) {
		return "", errf(apitypes.CodeHostnameTaken, "another status page holds %s; pick another hostname", host)
	}
	if err != nil {
		return "", err
	}
	if zone != nil {
		c.kickCloudflare()
	}
	what := "hostname " + host
	if !verified {
		what += " (waiting for its TXT record)"
	}
	return what, nil
}

// statusVerifyRecord is the TXT record that proves control of a page's
// hostname: the same as a domain's.
func statusVerifyRecord(p store.StatusPage) apitypes.DNSRecord {
	return apitypes.DNSRecord{Type: "TXT", Name: verifyPrefix + *p.Hostname, Value: verifyValuePrefix + p.VerifyToken}
}

// proveStatusHostname looks for the TXT record of p's unverified hostname
// and, when it holds the page's token, verifies it. It returns what is
// missing otherwise, and whether the hostname is verified now.
func (c *Core) proveStatusHostname(ctx context.Context, p store.StatusPage) (string, bool, error) {
	if p.Hostname == nil {
		return "", false, nil
	}
	if p.VerifiedAt != nil {
		return "", true, nil
	}
	host := *p.Hostname
	if err := c.checkStatusHost(ctx, host); err != nil {
		return err.Error(), false, nil
	}
	if taken, err := c.store.StatusHostnameVerified(ctx, &host); err != nil {
		return "", false, err
	} else if taken {
		return "another status page proved it controls " + host + " first", false, nil
	}
	rec := statusVerifyRecord(p)
	lctx, cancel := context.WithTimeout(ctx, verifyLookupTimeout)
	txts, err := c.lookupTXT(lctx, rec.Name)
	cancel()
	add := fmt.Sprintf("add a TXT record %s with the value %s to prove you control %s", rec.Name, rec.Value, host)
	if dnsErr, isDNS := errors.AsType[*net.DNSError](err); err != nil && (!isDNS || !dnsErr.IsNotFound) {
		return fmt.Sprintf("could not look up %s (%v); %s", rec.Name, err, add), false, nil
	}
	if !slices.ContainsFunc(txts, func(t string) bool { return strings.TrimSpace(t) == rec.Value }) {
		if len(txts) > 0 {
			add = fmt.Sprintf("the TXT record %s doesn't hold %s yet; DNS can take a few minutes", rec.Name, rec.Value)
		}
		return add, false, nil
	}
	t := now()
	n, err := c.store.VerifyStatusPageHostname(ctx, store.VerifyStatusPageHostnameParams{VerifiedAt: &t, ID: p.ID, Hostname: &host})
	switch {
	case store.IsUnique(err):
		return "another status page proved it controls " + host + " first", false, nil
	case err != nil:
		return "", false, err
	}
	if n > 0 {
		c.audit(ctx, System.InOrg(p.OrgID), "status_page.verify", nil, nil, "status page "+p.Slug, "status page "+p.Slug+": verified hostname "+host+" with its TXT record")
		c.statusPagesChanged(ctx)
	}
	return "", true, nil
}

// CheckStatusHostname looks for the TXT record of a page's hostname now
// and returns the page as it is after. Organization admins.
func (c *Core) CheckStatusHostname(ctx context.Context, a Actor, slug string) (apitypes.StatusPage, error) {
	p, err := c.loadStatusPage(ctx, a, slug, apitypes.ScopeAdmin, "checking a status page's hostname")
	if err != nil {
		return apitypes.StatusPage{}, err
	}
	if p.Hostname == nil {
		return apitypes.StatusPage{}, conflict("status page %s has no custom hostname; set one with `dock status-page hostname %s <hostname>`", slug, slug)
	}
	if _, _, err := c.proveStatusHostname(ctx, p); err != nil {
		return apitypes.StatusPage{}, err
	}
	return c.GetStatusPage(ctx, a, slug)
}

// statusHostnameView is a page's custom hostname for the API: whether it
// is verified, the records to add, and the record Dock makes in
// Cloudflare.
func (c *Core) statusHostnameView(ctx context.Context, p store.StatusPage, base string) (*apitypes.StatusHostname, error) {
	if p.Hostname == nil {
		return nil, nil
	}
	host := *p.Hostname
	out := &apitypes.StatusHostname{Hostname: host, Verified: p.VerifiedAt != nil, Records: []apitypes.DNSRecord{}}
	switch r, err := c.store.GetStatusPageRecord(ctx, &p.ID); {
	case err == nil:
		out.DNS = c.domainDNS(&r)
	case !store.IsNotFound(err):
		return nil, err
	}
	if out.DNS == nil {
		if !out.Verified {
			out.Records = append(out.Records, statusVerifyRecord(p))
		}
		if def := statusHostname(p.Slug, base); def != "" {
			out.Records = append(out.Records, apitypes.DNSRecord{Type: "CNAME", Name: host, Value: def})
		}
	}
	if out.Verified {
		out.URL = c.controlURL(host)
	} else {
		rec := statusVerifyRecord(p)
		out.Reason = fmt.Sprintf("add a TXT record %s with the value %s, then check it", rec.Name, rec.Value)
	}
	return out, nil
}
