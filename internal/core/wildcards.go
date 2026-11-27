package core

import (
	"cmp"
	"context"
	"crypto"
	"crypto/tls"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/acmedns"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cloudflare"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Wildcard certificates (ADR 0090). Every direct server with TLS serves
// its generated hostnames under its base domain. With a certificate per
// hostname, each one is published in certificate-transparency logs, so
// anyone can list them. Instead Dock obtains one certificate for
// *.<base> and <base> over ACME DNS-01, writing the challenge records
// through the platform's Cloudflare connection whose zone holds the base,
// keeps it sealed in wildcard_certificates and hands it to the base's
// servers with their routes. Caddy then serves it for every name one
// label under the base, routed or not, and issues nothing on demand
// there. Without such a zone, or before the first certificate, those
// names get on-demand certificates as before.
//
// One worker obtains and renews them, one at a time.

const (
	// wildcardTimeout bounds one issuance: DNS propagation and the CA's
	// validation take a minute or two.
	wildcardTimeout = 10 * time.Minute
	// wildcardRetryMin and wildcardRetryMax bound the backoff of failed
	// attempts: 5 min, 10, 20 … 6 h. Let's Encrypt allows 5 failed
	// validations per hostname an hour.
	wildcardRetryMin = 5 * time.Minute
	wildcardRetryMax = 6 * time.Hour
	// wildcardAlertAfter is how many failures in a row raise the base's
	// alert; one that waits for a change raises it at once.
	wildcardAlertAfter = 3
	// dnsEditPerm is the token permission DNS-01 needs.
	dnsEditPerm = "Zone › DNS › Edit"
)

// wildState is the worker's state in memory.
type wildState struct {
	// kick wakes the worker.
	kick chan struct{}

	mu sync.Mutex
	// certs are the issued certificates, by base domain, until they
	// expire. Bounded by base domains.
	certs map[string]wildCert
}

// wildCert is an issued certificate as servers get it.
type wildCert struct {
	node.Certificate
	notAfter time.Time
}

func newWildState() wildState {
	return wildState{kick: make(chan struct{}, 1), certs: map[string]wildCert{}}
}

// kickWildcards wakes the worker: a server's base domain, ingress or the
// platform's Cloudflare zones changed, or an org's uploaded certificates. A kicked pass also retries what
// waits for a change (a token without DNS permission).
func (c *Core) kickWildcards() {
	select {
	case c.wild.kick <- struct{}{}:
	default:
	}
}

// wantWildcard kicks the worker when srv, just connected, serves a base
// domain that has no wildcard yet: a new server, or one whose ingress
// changed.
func (c *Core) wantWildcard(srv store.Server) {
	base, ok := wildcardBase(c.installedIngress(srv))
	if !ok {
		return
	}
	c.wild.mu.Lock()
	_, have := c.wild.certs[base]
	c.wild.mu.Unlock()
	if !have {
		c.kickWildcards()
	}
}

// loadWildcards reads the issued certificates into memory, before the
// first routes go out.
func (c *Core) loadWildcards(ctx context.Context) error {
	rows, err := c.store.ListWildcardCertificates(ctx)
	if err != nil {
		return fmt.Errorf("load wildcard certificates: %w", err)
	}
	for _, r := range rows {
		if r.NotAfter == nil || !now().Before(*r.NotAfter) {
			continue
		}
		key, err := c.box.Open(r.Key, sealedWildcardKey.AAD(r.BaseDomain))
		if err != nil {
			// Its names go back to on demand; the worker issues a new one.
			c.log.Error("open a wildcard certificate's key", "base_domain", r.BaseDomain, "err", err)
			continue
		}
		c.setWildcard(r.BaseDomain, []byte(r.Chain), key, *r.NotAfter)
	}
	return nil
}

// setWildcard keeps base's certificate in memory.
func (c *Core) setWildcard(base string, chain, key []byte, notAfter time.Time) {
	c.wild.mu.Lock()
	defer c.wild.mu.Unlock()
	c.wild.certs[base] = wildCert{Certificate: node.Certificate{Names: acmedns.Names(base), ChainPEM: chain, KeyPEM: key}, notAfter: notAfter}
}

// dropWildcard forgets base's certificate, reporting whether there was one.
func (c *Core) dropWildcard(base string) bool {
	c.wild.mu.Lock()
	defer c.wild.mu.Unlock()
	_, ok := c.wild.certs[base]
	delete(c.wild.certs, base)
	return ok
}

// serverCertificates are the certificates a server's routes carry: its
// base domain's wildcard while valid, on a direct server with TLS. Without
// any wildcard it is a map lookup and no query.
func (c *Core) serverCertificates(ctx context.Context, serverID int64) ([]node.Certificate, error) {
	c.wild.mu.Lock()
	none := len(c.wild.certs) == 0
	c.wild.mu.Unlock()
	if none {
		return nil, nil
	}
	srv, err := c.store.GetServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	base, ok := wildcardBase(c.installedIngress(srv))
	if !ok {
		return nil, nil
	}
	c.wild.mu.Lock()
	defer c.wild.mu.Unlock()
	w, ok := c.wild.certs[base]
	if !ok || !now().Before(w.notAfter) {
		return nil, nil
	}
	return []node.Certificate{w.Certificate}, nil
}

// localWildcardCovers reports whether the control plane's own server
// serves a wildcard covering host (its base domain, or one label under
// it), so its ask endpoint refuses on-demand certificates there.
func (c *Core) localWildcardCovers(ctx context.Context, host string) (bool, error) {
	certs, err := c.serverCertificates(ctx, c.localID)
	if err != nil || len(certs) == 0 {
		return false, err
	}
	base := certs[0].Names[len(certs[0].Names)-1]
	label, rest, _ := strings.Cut(host, ".")
	return host == base || label != "" && rest == base, nil
}

// wildcardBase is the base domain a server with ing would serve a
// wildcard for: a direct server's with TLS. A tunnel server's names are
// served by Cloudflare's edge, with its certificate. Callers pass the
// installed ingress (installedIngress): a direct server sent through its
// tunnel keeps its wildcard for the names that still reach its IP.
func wildcardBase(ing serverIngress) (string, bool) {
	return ing.baseDomain, ing.baseDomain != "" && ing.mode == apitypes.IngressDirect && !ing.plainHTTP
}

// wildcardBases are the base domains that want a wildcard, with the
// servers serving each.
func (c *Core) wildcardBases(ctx context.Context) (map[string][]store.Server, error) {
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	bases := map[string][]store.Server{}
	for _, s := range servers {
		if base, ok := wildcardBase(c.installedIngress(s)); ok {
			bases[base] = append(bases[base], s)
		}
	}
	return bases, nil
}

// runWildcards is the certificate worker. It runs from Start until Close,
// for the wildcards and the orgs' uploaded certificates (ownCertPass,
// ADR 0112).
//
// Its timer (a doc comment is required for every timer, see
// docs/performance.md) exists while a certificate is issued or uploaded,
// or an attempt failed: it fires at the earliest wildcard renewal (a
// third of the certificate's lifetime before it ends: 30 days for Let's
// Encrypt's 90), retry or expiry, or an upload's next expiry stage (30
// and 7 days before it ends, and the end), weeks apart once certificates
// are issued. Without a wildcard to get or an upload there is none.
func (c *Core) runWildcards() {
	defer c.wg.Done()
	kicked := true // the first pass retries what waited for a change
	var timer *time.Timer
	for {
		// A panic in a pass ends that pass: the next comes after wildcardRetryMin.
		next, err := caught(c.log, "a pass over the wildcard certificates", func() (time.Time, error) { return c.wildcardPass(c.ctx, kicked), nil })
		if err != nil {
			next = now().Add(wildcardRetryMin)
		}
		own, err := caught(c.log, "a pass over the uploaded certificates", func() (time.Time, error) { return c.ownCertPass(c.ctx), nil })
		if err != nil {
			own = now().Add(wildcardRetryMin)
		}
		if !own.IsZero() && (next.IsZero() || own.Before(next)) {
			next = own
		}
		if timer != nil {
			timer.Stop()
		}
		var wake <-chan time.Time
		if !next.IsZero() {
			timer = time.NewTimer(max(time.Until(next), time.Second))
			wake = timer.C
		}
		select {
		case <-c.ctx.Done():
			return
		case <-c.wild.kick:
			kicked = true
		case <-wake:
			kicked = false
		}
	}
}

// wildcardPass obtains or renews what is due, drops expired certificates
// and rows of base domains no server uses, and returns when the next
// thing is due (zero for nothing).
func (c *Core) wildcardPass(ctx context.Context, kicked bool) time.Time {
	bases, err := c.wildcardBases(ctx)
	if err != nil {
		if ctx.Err() == nil {
			c.log.Error("wildcard certificates", "err", err)
		}
		return now().Add(wildcardRetryMin)
	}
	rows, err := c.store.ListWildcardCertificates(ctx)
	if err != nil {
		if ctx.Err() == nil {
			c.log.Error("wildcard certificates", "err", err)
		}
		return now().Add(wildcardRetryMin)
	}
	byBase := map[string]store.WildcardCertificate{}
	for _, r := range rows {
		byBase[r.BaseDomain] = r
	}
	for _, r := range rows {
		_, used := bases[r.BaseDomain]
		_, zoned := c.zoneFor(0, r.BaseDomain)
		if !used || !zoned && r.NotAfter == nil {
			// Nothing to keep: no server uses it, or it was never
			// issued and can't be.
			c.forgetWildcard(ctx, r.BaseDomain)
			delete(byBase, r.BaseDomain)
		}
	}

	var next time.Time
	soonest := func(t time.Time) {
		if !t.IsZero() && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	for _, base := range slices.Sorted(maps.Keys(bases)) {
		if ctx.Err() != nil {
			return time.Time{}
		}
		r, ok := byBase[base]
		if !ok {
			if _, zoned := c.zoneFor(0, base); !zoned {
				continue // on demand, as without a wildcard; nothing to try
			}
			r = store.WildcardCertificate{BaseDomain: base}
		}
		if r.NotAfter != nil && !now().Before(*r.NotAfter) && c.dropWildcard(base) {
			// Expired without a renewal: back to on demand.
			c.applyServers(ctx, bases[base])
		}
		if wildcardDue(r, kicked, now()) {
			r = c.issueWildcard(ctx, r, bases[base])
		}
		soonest(wildcardNext(r, now()))
	}
	return next
}

// renewAt is when a certificate is renewed: a third of its lifetime
// before it ends.
func renewAt(notBefore, notAfter time.Time) time.Time {
	return notAfter.Add(-notAfter.Sub(notBefore) / 3)
}

// wildcardDue reports whether r's base domain should be tried now: a
// retry that is due, one waiting for a change on a kicked pass, a base
// never tried, or a certificate due for renewal.
func wildcardDue(r store.WildcardCertificate, kicked bool, at time.Time) bool {
	switch {
	case r.RetryAt != nil:
		return !at.Before(*r.RetryAt)
	case r.Error != "":
		return kicked
	case r.NotAfter == nil:
		return true
	default:
		return !at.Before(renewAt(*r.NotBefore, *r.NotAfter))
	}
}

// wildcardNext is when r is next due on a timer: its retry, or its
// renewal, or (still serving after failed renewals) its expiry, whichever
// comes first; zero for nothing.
func wildcardNext(r store.WildcardCertificate, at time.Time) time.Time {
	var next time.Time
	if r.RetryAt != nil {
		next = *r.RetryAt
	}
	if r.NotAfter != nil && at.Before(*r.NotAfter) {
		t := *r.NotAfter
		if r.Error == "" {
			t = renewAt(*r.NotBefore, *r.NotAfter)
		}
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	return next
}

// issueWildcard obtains base's certificate and records the outcome,
// returning the row as it now is. Success hands it to servers at once.
func (c *Core) issueWildcard(ctx context.Context, r store.WildcardCertificate, servers []store.Server) store.WildcardCertificate {
	base := r.BaseDomain
	zone, ok := c.zoneFor(0, base)
	if !ok {
		return c.wildcardFailed(ctx, r, true, fmt.Sprintf("%s isn't in a zone of the platform's Cloudflare connections, so its certificate can't be renewed; add the zone, or its names go back to a certificate each when it expires", base))
	}
	c.cf.mu.Lock()
	pause := c.cf.pauseUntil
	c.cf.mu.Unlock()
	if now().Before(pause) {
		r.RetryAt = &pause // the Cloudflare worker's rate limit holds here too
		return r
	}
	cc, err := c.conn(ctx, map[int64]*cfConn{}, zone.ConnectionID)
	if err != nil {
		return c.wildcardFailed(ctx, r, false, "read the Cloudflare connection: "+err.Error())
	}
	if cc.row.Status == string(apitypes.CloudflareInvalid) {
		return c.wildcardFailed(ctx, r, true, fmt.Sprintf("Cloudflare connection %s has no working token: %s", cc.row.Name, cc.row.StatusReason))
	}
	key, err := c.acmeAccount(ctx)
	if err != nil {
		return c.wildcardFailed(ctx, r, false, err.Error())
	}

	ictx, cancel := context.WithTimeout(ctx, wildcardTimeout)
	defer cancel()
	dns := &cfDNS{c: c, cc: cc, zone: zone}
	// Records a crash left behind go first.
	err = dns.CleanUp(ictx, acmedns.ChallengeName(base))
	var cert acmedns.Certificate
	if err == nil {
		cert, err = c.obtain(ictx, acmedns.Request{
			Directory: c.cfg.Caddy.ACMEDirectory, AccountKey: key, Email: c.cfg.Caddy.ACMEEmail,
			Domain: base, DNS: dns, HTTPClient: c.acmeHTTP,
		})
	}
	if err == nil {
		if _, perr := tls.X509KeyPair(cert.ChainPEM, cert.KeyPEM); perr != nil {
			err = fmt.Errorf("the CA's certificate doesn't pair with its key: %w", perr)
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return r // shutting down: not the certificate's fault
		}
		if _, isCF := cloudflare.AsError(err); isCF {
			out, ferr := c.failed(ctx, cc, r.Attempts, err, dnsEditPerm)
			if ferr != nil && !errors.Is(ferr, errRateLimited) {
				return c.wildcardFailed(ctx, r, false, err.Error())
			}
			return c.wildcardFailed(ctx, r, out.retryAt == nil, "Cloudflare: "+out.reason)
		}
		return c.wildcardFailed(ctx, r, false, err.Error())
	}

	sealed, err := c.box.Seal(cert.KeyPEM, sealedWildcardKey.AAD(base))
	if err != nil {
		return c.wildcardFailed(ctx, r, false, err.Error())
	}
	at := now()
	if err := c.store.SaveWildcardCertificate(ctx, store.SaveWildcardCertificateParams{
		BaseDomain: base, Chain: string(cert.ChainPEM), Key: sealed, NotBefore: &cert.NotBefore, NotAfter: &cert.NotAfter, UpdatedAt: at,
	}); err != nil {
		c.log.Error("save a wildcard certificate", "base_domain", base, "err", err)
		return c.wildcardFailed(ctx, r, false, "save the certificate: "+err.Error())
	}
	renewed := r.NotAfter != nil
	c.setWildcard(base, cert.ChainPEM, cert.KeyPEM, cert.NotAfter)
	c.applyServers(ctx, servers)
	c.signalAlert(ctx, alertSignal{Key: wildcardAlertKey(base), Resolve: true})
	action, verb := "certificate.issue", "issued"
	if renewed {
		action, verb = "certificate.renew", "renewed"
	}
	c.audit(ctx, System, action, nil, nil, "*."+base,
		fmt.Sprintf("%s the certificate for *.%s and %s, valid until %s", verb, base, base, cert.NotAfter.UTC().Format(time.DateOnly)))
	c.log.Info("wildcard certificate "+verb, "base_domain", base, "not_after", cert.NotAfter)
	return store.WildcardCertificate{BaseDomain: base, Chain: string(cert.ChainPEM), Key: sealed, NotBefore: &cert.NotBefore, NotAfter: &cert.NotAfter, UpdatedAt: at}
}

// wildcardFailed records a failed attempt: retried with backoff, or, when
// waiting, on the next kicked pass (a token or zone change). It raises
// the base's alert when waiting or after wildcardAlertAfter failures.
func (c *Core) wildcardFailed(ctx context.Context, r store.WildcardCertificate, waiting bool, reason string) store.WildcardCertificate {
	r.Attempts++
	r.Error = reason
	r.RetryAt = nil
	if !waiting {
		at := now().Add(wildcardBackoff(r.Attempts))
		r.RetryAt = &at
	}
	r.UpdatedAt = now()
	if err := c.store.FailWildcardCertificate(ctx, store.FailWildcardCertificateParams{
		BaseDomain: r.BaseDomain, Error: r.Error, Attempts: r.Attempts, RetryAt: r.RetryAt, UpdatedAt: r.UpdatedAt,
	}); err != nil {
		c.log.Error("record a wildcard certificate's failure", "base_domain", r.BaseDomain, "err", err)
	}
	c.log.Warn("wildcard certificate failed", "base_domain", r.BaseDomain, "attempts", r.Attempts, "err", reason)
	if waiting || r.Attempts >= wildcardAlertAfter {
		title := "Certificate for *." + r.BaseDomain + " can't be issued"
		msg := reason + ". Until it is, every generated hostname under it gets a certificate of its own, published in certificate-transparency logs."
		if r.NotAfter != nil {
			title = "Certificate for *." + r.BaseDomain + " isn't renewing"
			msg = fmt.Sprintf("%s. It expires on %s; then its names go back to a certificate each.", reason, r.NotAfter.UTC().Format(time.DateOnly))
		}
		c.signalAlert(ctx, alertSignal{
			Key: wildcardAlertKey(r.BaseDomain), Kind: apitypes.AlertCertificate, Severity: apitypes.SeverityWarning,
			Title: title, Message: msg, Command: "dock address",
		})
	}
	return r
}

// wildcardBackoff is how long to wait after the attempts-th failure in a
// row: 5 min, doubling, at most 6 h.
func wildcardBackoff(attempts int64) time.Duration {
	return min(wildcardRetryMin<<min(max(attempts-1, 0), 10), wildcardRetryMax)
}

// forgetWildcard deletes base's row and certificate, and resolves its
// alert: no server uses the base, or it can't have a wildcard.
func (c *Core) forgetWildcard(ctx context.Context, base string) {
	if err := c.store.DeleteWildcardCertificate(ctx, base); err != nil {
		c.log.Error("delete a wildcard certificate", "base_domain", base, "err", err)
		return
	}
	c.dropWildcard(base)
	c.signalAlert(ctx, alertSignal{Key: wildcardAlertKey(base), Resolve: true})
}

// applyServers re-applies the routes of servers, which carry their
// base's certificate (or no longer do) and the uploads for their domains,
// reporting whether every online one took them.
func (c *Core) applyServers(ctx context.Context, servers []store.Server) bool {
	ok := true
	for _, s := range servers {
		if s.ID != c.localID && !c.online(s.ID) {
			continue // it gets them when it connects
		}
		if err := c.applyRoutes(ctx, s.ID); err != nil {
			c.log.Error("apply routes with their certificates", "server", s.Name, "err", err)
			ok = false
		}
	}
	return ok
}

// wildcardAlertKey names a base domain's certificate alert.
func wildcardAlertKey(base string) string { return "wildcard:" + base }

// acmeAccount is the account key for the configured CA, made the first
// time.
func (c *Core) acmeAccount(ctx context.Context) (crypto.Signer, error) {
	dir := cmp.Or(c.cfg.Caddy.ACMEDirectory, acmedns.LetsEncrypt)
	sealed, err := c.store.GetACMEAccount(ctx, dir)
	if store.IsNotFound(err) {
		_, keyPEM, kerr := acmedns.NewAccountKey()
		if kerr != nil {
			return nil, kerr
		}
		if sealed, err = c.box.Seal(keyPEM, sealedACMEKey.AAD(dir)); err != nil {
			return nil, err
		}
		if err = c.store.CreateACMEAccount(ctx, store.CreateACMEAccountParams{Directory: dir, Key: sealed, CreatedAt: now()}); err == nil {
			sealed, err = c.store.GetACMEAccount(ctx, dir) // another process's, if it was first
		}
	}
	if err != nil {
		return nil, fmt.Errorf("read the ACME account: %w", err)
	}
	keyPEM, err := c.box.Open(sealed, sealedACMEKey.AAD(dir))
	if err != nil {
		return nil, fmt.Errorf("open the ACME account's key: %w", err)
	}
	return acmedns.ParseKey(keyPEM)
}

// cfDNS answers DNS-01 challenges in a Cloudflare zone, with TXT records
// marked as Dock's. It only ever deletes its own.
type cfDNS struct {
	c    *Core
	cc   *cfConn
	zone store.CloudflareZone
}

func (d *cfDNS) Present(ctx context.Context, name, value string) error {
	_, err := d.cc.client.CreateRecord(ctx, d.zone.ZoneID, cloudflare.Record{Type: "TXT", Name: name, Content: value, TTL: 60, Comment: d.c.recordComment()})
	return err
}

func (d *cfDNS) Wait(ctx context.Context, name string, values []string) error {
	return d.c.waitTXT(ctx, d.zone.Name, name, values)
}

func (d *cfDNS) CleanUp(ctx context.Context, name string) error {
	recs, err := d.cc.client.RecordsNamed(ctx, d.zone.ZoneID, name)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if r.Type == "TXT" && r.Comment == d.c.recordComment() {
			if err := d.cc.client.DeleteRecord(ctx, d.zone.ZoneID, r.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// Wildcards describes each base domain's certificate for the Address
// page: issued, being issued, failing, or why there is none.
func (c *Core) wildcards(ctx context.Context) ([]apitypes.Wildcard, error) {
	bases, err := c.wildcardBases(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListWildcardCertificates(ctx)
	if err != nil {
		return nil, err
	}
	byBase := map[string]store.WildcardCertificate{}
	for _, r := range rows {
		byBase[r.BaseDomain] = r
	}
	out := []apitypes.Wildcard{}
	for _, base := range slices.Sorted(maps.Keys(bases)) {
		w := apitypes.Wildcard{BaseDomain: base}
		for _, s := range bases[base] {
			w.Servers = append(w.Servers, s.Name)
		}
		r, ok := byBase[base]
		_, zoned := c.zoneFor(0, base)
		valid := ok && r.NotAfter != nil && now().Before(*r.NotAfter)
		switch {
		case valid:
			w.Status, w.NotAfter, w.Reason = apitypes.WildcardActive, r.NotAfter, r.Error
			at := renewAt(*r.NotBefore, *r.NotAfter)
			w.RenewAt = &at
		case !zoned:
			w.Status = apitypes.WildcardUnavailable
			w.Reason = base + " isn't in a zone of the platform's Cloudflare connections; add one whose token has " + dnsEditPerm + " to get a wildcard"
		case ok && r.Error != "":
			w.Status, w.Reason = apitypes.WildcardFailed, r.Error
		default:
			w.Status = apitypes.WildcardIssuing
		}
		if ok && r.RetryAt != nil {
			w.RetryAt = r.RetryAt
		}
		out = append(out, w)
	}
	return out, nil
}
