package core

import (
	"bytes"
	"cmp"
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Uploaded certificates (ADR 0110). An org uploads a chain and key for
// names ACME can't cover: a Cloudflare origin certificate (Full strict),
// an internal or corporate CA's for LAN or Tailscale names, a CA the org
// must use. A certificate serves every verified domain of its org that
// one of its names covers, never another org's, instead of an on-demand
// certificate: it goes to the domain's server with its routes, as a
// wildcard does (ADR 0090). They don't renew, so the certificate worker
// (runWildcards) alerts 30 and 7 days before one expires; once it has,
// it is no longer sent and its domains get on-demand certificates again.

const (
	// maxCertChain and maxCertKey bound an upload: a chain of a few
	// certificates is under 10 KB, an RSA 4096 key about 3 KB.
	maxCertChain = 64 << 10
	maxCertKey   = 16 << 10
	// maxOrgCertificates bounds an org's uploads, which are all held in
	// memory with their keys and matched against its domains.
	maxOrgCertificates = 100
	// certWarnBefore and certCriticalBefore are when an uploaded
	// certificate's expiry alert opens and when it turns critical.
	certWarnBefore     = 30 * 24 * time.Hour
	certCriticalBefore = 7 * 24 * time.Hour
)

// Expiry stages of an uploaded certificate: which alert it has had
// (custom_certificates.alerted).
const (
	certFine int64 = iota
	certWarned
	certCritical
	certExpired
)

// ownCerts are the uploaded certificates that haven't expired, in memory
// with their keys opened, by org: at most maxOrgCertificates each.
type ownCerts struct {
	// load serializes reloads from the store with the route applies that
	// follow, so a reload that started after a write reads it, and an
	// older one can't land last.
	load sync.Mutex
	mu   sync.Mutex
	// byOrg is the set, under mu.
	byOrg map[int64][]ownCert
	// stale, under mu, says a reload or a server's route apply failed
	// since the last change: the worker's next pass tries again.
	stale bool
	// sent, under mu, is which upload each server's last applied routes
	// carried for which hostname (setSent): what a domain shows it is
	// served with. Bounded by servers and their routed hostnames.
	sent map[int64]map[string]int64
}

// ownCert is an uploaded certificate as servers get it.
type ownCert struct {
	id       int64
	names    []string
	chain    []byte
	key      []byte
	issuer   string
	public   bool
	notAfter time.Time
}

// covers reports whether one of the certificate's names covers host: the
// name itself, or *.<rest> for one label under rest.
func (o ownCert) covers(host string) bool {
	return slices.ContainsFunc(o.names, func(n string) bool { return nameCovers(n, host) })
}

// nameCovers reports whether a certificate's DNS name covers host.
func nameCovers(name, host string) bool {
	if base, ok := strings.CutPrefix(name, "*."); ok {
		label, rest, found := strings.Cut(host, ".")
		return found && label != "" && rest == base
	}
	return name == host
}

// bestCert is the certificate host is served with among certs: the valid
// one that expires last (the newest upload on a tie), so a renewal takes
// over the moment it is uploaded. With expired, an expired one counts
// too, below every valid one: whose domains are left without one.
func bestCert(certs []ownCert, host string, at time.Time, expired bool) (ownCert, bool) {
	var best ownCert
	found := false
	for _, o := range certs {
		valid := at.Before(o.notAfter)
		if !valid && !expired || !o.covers(host) {
			continue
		}
		if !found || compareCerts(o, best, at) > 0 {
			best, found = o, true
		}
	}
	return best, found
}

// compareCerts orders certificates for bestCert: valid above expired,
// then the later expiry, then the later upload.
func compareCerts(a, b ownCert, at time.Time) int {
	valid := func(o ownCert) int {
		if at.Before(o.notAfter) {
			return 1
		}
		return 0
	}
	return cmp.Or(cmp.Compare(valid(a), valid(b)), a.notAfter.Compare(b.notAfter), cmp.Compare(a.id, b.id))
}

// loadOwnCerts reads the uploaded certificates into memory, before the
// first routes go out.
func (c *Core) loadOwnCerts(ctx context.Context) error {
	c.own.load.Lock()
	defer c.own.load.Unlock()
	rows, err := c.store.ListCustomCertificates(ctx)
	if err != nil {
		return fmt.Errorf("load uploaded certificates: %w", err)
	}
	c.setOwnCerts(c.openOwnCerts(rows))
	return nil
}

// refreshOwnCerts reloads the uploaded certificates from the store as it
// is now and re-applies every server's routes (an unchanged config
// doesn't reload Caddy): after an upload or a removal, and when one
// expired. It reports whether all of it worked; if not, the worker's
// next pass tries again (stale).
func (c *Core) refreshOwnCerts(ctx context.Context) bool {
	c.own.load.Lock()
	defer c.own.load.Unlock()
	ok := false
	defer func() {
		c.own.mu.Lock()
		c.own.stale = !ok
		c.own.mu.Unlock()
	}()
	rows, err := c.store.ListCustomCertificates(ctx)
	if err != nil {
		c.log.Error("reload uploaded certificates", "err", err)
		return false
	}
	c.setOwnCerts(c.openOwnCerts(rows))
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		c.log.Error("apply routes with uploaded certificates", "err", err)
		return false
	}
	ok = c.applyServers(ctx, servers)
	return ok
}

// openOwnCerts opens the keys of rows that haven't expired, by org. A key that doesn't open is left
// out (its domains get on-demand certificates) and logged.
func (c *Core) openOwnCerts(rows []store.CustomCertificate) map[int64][]ownCert {
	by := map[int64][]ownCert{}
	at := now()
	for _, r := range rows {
		if !at.Before(r.NotAfter) {
			continue // expired: no longer served
		}
		key, err := c.box.Open(r.Key, certificateKeyAAD(r.OrgID, r.Fingerprint))
		if err != nil {
			c.log.Error("open an uploaded certificate's key", "certificate", r.ID, "err", err)
			continue
		}
		by[r.OrgID] = append(by[r.OrgID], ownCert{
			id: r.ID, names: strings.Fields(r.Names), chain: []byte(r.Chain), key: key,
			issuer: r.Issuer, public: r.Public, notAfter: r.NotAfter,
		})
	}
	return by
}

func (c *Core) setOwnCerts(by map[int64][]ownCert) {
	c.own.mu.Lock()
	c.own.byOrg = by
	c.own.mu.Unlock()
}

// ownCertFor is the uploaded certificate org's host is served with now.
func (c *Core) ownCertFor(org int64, host string) (ownCert, bool) {
	c.own.mu.Lock()
	defer c.own.mu.Unlock()
	return bestCert(c.own.byOrg[org], host, now(), false)
}

// routeOwnCerts are the uploaded certificates a server's routes carry:
// for each hostname of rows an org's certificate covers, that one, with
// the hostnames it serves there as its names. Only exact hostnames go
// out, so a wildcard serves its org's domains and nothing else on a
// shared server. Hostnames served to a Cloudflare tunnel only are left
// out: Cloudflare's edge has their certificate. Without any upload it is
// a map lookup.
//
// It also returns which upload went for which hostname, for setSent once
// the server took the routes.
func (c *Core) routeOwnCerts(rows []store.ListRoutableDomainsRow, tunnelOnly map[string]bool, serves bool) ([]node.Certificate, map[string]int64) {
	c.own.mu.Lock()
	defer c.own.mu.Unlock()
	if len(c.own.byOrg) == 0 || !serves {
		return nil, nil
	}
	sent := map[string]int64{}
	at := now()
	var out []node.Certificate
	index := map[int64]int{} // certificate ID → index in out
	seen := map[string]bool{}
	for _, d := range rows {
		if tunnelOnly[d.Hostname] || seen[d.Hostname] {
			continue
		}
		o, ok := bestCert(c.own.byOrg[d.OrgID], d.Hostname, at, false)
		if !ok {
			continue
		}
		seen[d.Hostname] = true
		sent[d.Hostname] = o.id
		i, ok := index[o.id]
		if !ok {
			i = len(out)
			index[o.id] = i
			out = append(out, node.Certificate{ChainPEM: o.chain, KeyPEM: o.key, Uploaded: true})
		}
		out[i].Names = append(out[i].Names, d.Hostname)
	}
	return out, sent
}

// setSent records which upload serverID's routes, just applied, carry
// for which hostname (nil for none).
func (c *Core) setSent(serverID int64, sent map[string]int64) {
	c.own.mu.Lock()
	defer c.own.mu.Unlock()
	if len(sent) == 0 {
		delete(c.own.sent, serverID)
		return
	}
	if c.own.sent == nil {
		c.own.sent = map[int64]map[string]int64{}
	}
	c.own.sent[serverID] = sent
}

// sentCert is the upload serverID's last routes carried for host, if it
// is still in memory.
func (c *Core) sentCert(serverID int64, org int64, host string) (ownCert, bool) {
	c.own.mu.Lock()
	defer c.own.mu.Unlock()
	id, ok := c.own.sent[serverID][host]
	if !ok {
		return ownCert{}, false
	}
	i := slices.IndexFunc(c.own.byOrg[org], func(o ownCert) bool { return o.id == id })
	if i < 0 {
		return ownCert{}, false
	}
	return c.own.byOrg[org][i], true
}

// parsedCert is an upload that passed the checks: the chain re-encoded
// leaf first and the key as PKCS#8. notAfter is when the chain stops
// working: the leaf's end or an intermediate's, whichever comes first.
type parsedCert struct {
	leaf        *x509.Certificate
	notAfter    time.Time
	names       []string
	chainPEM    []byte
	keyPEM      []byte
	fingerprint string
	issuer      string
	public      bool
}

// parseUpload checks an uploaded chain and key: PEM certificates and a
// key that belongs to one of them (the leaf), the rest of them the
// leaf's issuers (put in order, leaf first), DNS names to serve, and
// valid at at. roots are the CAs that make it public (nil: the system's).
func parseUpload(chainPEM, keyPEM string, at time.Time, roots *x509.CertPool) (parsedCert, error) {
	switch {
	case strings.TrimSpace(chainPEM) == "":
		return parsedCert{}, invalid("the certificate chain is empty; give the PEM file with the certificate and its intermediates (often fullchain.pem)")
	case strings.TrimSpace(keyPEM) == "":
		return parsedCert{}, invalid("the private key is empty; give the PEM file with the certificate's key (often privkey.pem or key.pem)")
	case len(chainPEM) > maxCertChain:
		return parsedCert{}, invalid("the certificate chain is larger than %d KB; give the leaf and its intermediates only", maxCertChain>>10)
	case len(keyPEM) > maxCertKey:
		return parsedCert{}, invalid("the private key is larger than %d KB; give one key in PEM", maxCertKey>>10)
	}
	var certs []*x509.Certificate
	rest := []byte(chainPEM)
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			if strings.Contains(b.Type, "PRIVATE KEY") {
				return parsedCert{}, invalid("the certificate chain holds a private key; give the key separately (--key) and only certificates in the chain")
			}
			return parsedCert{}, invalid("the certificate chain holds a %q block; give only CERTIFICATE blocks", b.Type)
		}
		cert, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return parsedCert{}, invalid("certificate %d of the chain doesn't parse: %v", len(certs)+1, err)
		}
		certs = append(certs, cert)
	}
	if len(certs) == 0 {
		return parsedCert{}, invalid("the certificate chain holds no PEM certificate; it should start with -----BEGIN CERTIFICATE-----")
	}

	key, err := parseKey([]byte(keyPEM))
	if err != nil {
		return parsedCert{}, err
	}
	pub, ok := key.Public().(interface{ Equal(crypto.PublicKey) bool })
	if !ok {
		return parsedCert{}, invalid("the private key's type isn't supported; use an RSA, ECDSA or Ed25519 key")
	}
	li := slices.IndexFunc(certs, func(c *x509.Certificate) bool { return pub.Equal(c.PublicKey) })
	if li < 0 {
		return parsedCert{}, invalid("the private key doesn't belong to any certificate in the chain; check that the key and the certificate come from the same request")
	}
	leaf := certs[li]
	chain := []*x509.Certificate{leaf}
	left := slices.Delete(slices.Clone(certs), li, li+1)
	for cur := leaf; len(left) > 0; {
		i := slices.IndexFunc(left, func(p *x509.Certificate) bool {
			return bytes.Equal(cur.RawIssuer, p.RawSubject) && cur.CheckSignatureFrom(p) == nil
		})
		if i < 0 {
			break
		}
		cur = left[i]
		chain = append(chain, cur)
		left = slices.Delete(left, i, i+1)
	}
	if len(left) > 0 {
		return parsedCert{}, invalid("the chain holds %s, which didn't issue the certificate or any of its issuers; remove it", certName(left[0]))
	}
	// Clients that need an intermediate refuse it expired, whether or not
	// Dock trusts the CA. A self-signed root at the end is the client's own
	// to trust; it is only along for the ride.
	for i, ic := range chain[1:] {
		root := i == len(chain)-2 && bytes.Equal(ic.RawIssuer, ic.RawSubject) && ic.CheckSignatureFrom(ic) == nil
		if !root && (!at.Before(ic.NotAfter) || at.Before(ic.NotBefore)) {
			return parsedCert{}, invalid("the chain's %s isn't valid now (%s to %s); get the current intermediates from the CA", certName(ic)[4:], ic.NotBefore.UTC().Format(time.DateOnly), ic.NotAfter.UTC().Format(time.DateOnly))
		}
	}

	var names []string
	for _, n := range leaf.DNSNames {
		if strings.ContainsFunc(n, unicode.IsSpace) {
			continue // no client matches it; serving it would claim a name it doesn't hold
		}
		n = strings.ToLower(strings.TrimSuffix(n, "."))
		if _, err := normalizeHostname(strings.TrimPrefix(n, "*.")); err == nil && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return parsedCert{}, invalid("the certificate names no DNS hostname (subject alternative name); Dock serves certificates by hostname, so request one with the domains in it")
	}
	switch {
	case !at.Before(leaf.NotAfter):
		return parsedCert{}, invalid("the certificate expired on %s; upload its renewal", leaf.NotAfter.UTC().Format(time.DateOnly))
	case at.Before(leaf.NotBefore):
		return parsedCert{}, invalid("the certificate isn't valid until %s; upload it then, or check the server's clock", leaf.NotBefore.UTC().Format(time.DateTime))
	}

	var out bytes.Buffer
	for _, c := range chain {
		_ = pem.Encode(&out, &pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return parsedCert{}, invalid("the private key can't be stored: %v", err)
	}
	keyOut := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, err := tls.X509KeyPair(out.Bytes(), keyOut); err != nil {
		return parsedCert{}, invalid("the certificate and key don't make a working pair: %v", err)
	}
	inter := x509.NewCertPool()
	for _, c := range chain[1:] {
		inter.AddCert(c)
	}
	_, verr := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: at})
	sum := sha256.Sum256(leaf.Raw)
	end := leaf.NotAfter
	for i, ic := range chain[1:] {
		root := i == len(chain)-2 && bytes.Equal(ic.RawIssuer, ic.RawSubject) && ic.CheckSignatureFrom(ic) == nil
		if !root && ic.NotAfter.Before(end) {
			end = ic.NotAfter
		}
	}
	return parsedCert{
		leaf: leaf, notAfter: end, names: names, chainPEM: out.Bytes(), keyPEM: keyOut,
		fingerprint: hex.EncodeToString(sum[:]), issuer: issuerName(leaf), public: verr == nil,
	}, nil
}

// parseKey reads a PEM private key: PKCS#8, PKCS#1 (RSA) or SEC 1 (EC).
// Other blocks around it (EC PARAMETERS) are skipped.
func parseKey(data []byte) (crypto.Signer, error) {
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			return nil, invalid("the private key holds no PEM key; it should start with -----BEGIN PRIVATE KEY----- (or RSA or EC PRIVATE KEY)")
		}
		var key any
		var err error
		switch b.Type {
		case "PRIVATE KEY":
			key, err = x509.ParsePKCS8PrivateKey(b.Bytes)
		case "RSA PRIVATE KEY":
			key, err = x509.ParsePKCS1PrivateKey(b.Bytes)
		case "EC PRIVATE KEY":
			key, err = x509.ParseECPrivateKey(b.Bytes)
		case "ENCRYPTED PRIVATE KEY":
			return nil, invalid("the private key is encrypted; decrypt it first (openssl pkey -in key.pem -out plain.pem) and upload that")
		case "CERTIFICATE":
			return nil, invalid("the key file holds a certificate; give the private key there and the certificates as the chain")
		default:
			continue
		}
		if err != nil {
			return nil, invalid("the private key doesn't parse: %v", err)
		}
		s, ok := key.(crypto.Signer)
		if !ok {
			return nil, invalid("the private key's type isn't supported; use an RSA, ECDSA or Ed25519 key")
		}
		return s, nil
	}
}

// issuerName names a certificate's issuer for people: its common name,
// else its organization, else the whole name. Control characters an
// issuer put there are dropped, so it can't steer a terminal that lists
// it.
func issuerName(c *x509.Certificate) string {
	name := c.Issuer.String()
	switch {
	case c.Issuer.CommonName != "":
		name = c.Issuer.CommonName
	case len(c.Issuer.Organization) > 0:
		name = c.Issuer.Organization[0]
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
}

// certName names a certificate in an error.
func certName(c *x509.Certificate) string {
	return "the certificate for " + cmp.Or(c.Subject.CommonName, c.Subject.String(), "an unnamed subject")
}

// AddCertificate uploads a certificate for the org's domains. It serves
// the ones it covers at once (unless one that expires later covers them
// too). It takes certificates.manage, which only a grant on the whole
// org holds (domains edit there): it changes how every project's
// domains are served.
func (c *Core) AddCertificate(ctx context.Context, a Actor, req apitypes.AddCertificateRequest) (apitypes.Certificate, error) {
	if err := a.requireUnlimited(apitypes.ScopeDeploy, "uploading a certificate"); err != nil {
		return apitypes.Certificate{}, err
	}
	p, err := parseUpload(req.Chain, req.Key, now(), nil)
	if err != nil {
		return apitypes.Certificate{}, err
	}
	if req.Domain != "" {
		host, err := normalizeHostname(req.Domain)
		if err != nil {
			return apitypes.Certificate{}, err
		}
		if !(ownCert{names: p.names}).covers(host) {
			return apitypes.Certificate{}, invalid("the certificate is for %s, not %s; upload the one issued for %s", strings.Join(p.names, ", "), host, host)
		}
	}
	sealed, err := c.box.Seal(p.keyPEM, certificateKeyAAD(a.OrgID, p.fingerprint))
	if err != nil {
		return apitypes.Certificate{}, err
	}
	// Read before the write, so a failed read never answers an upload
	// that was made.
	domains, err := c.orgDomains(ctx, a.OrgID)
	if err != nil {
		return apitypes.Certificate{}, err
	}
	var row store.CustomCertificate
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		n, err := q.CountOrgCustomCertificates(ctx, a.OrgID)
		if err != nil {
			return err
		}
		if n >= maxOrgCertificates {
			return invalid("the organization has %d uploaded certificates, the most it can keep; remove one it no longer needs (`dock certificate list`, `dock certificate remove`)", n)
		}
		row, err = q.CreateCustomCertificate(ctx, store.CreateCustomCertificateParams{
			OrgID: a.OrgID, Names: strings.Join(p.names, " "), Chain: string(p.chainPEM), Key: sealed,
			Fingerprint: p.fingerprint, Issuer: p.issuer, Public: p.public, NotBefore: p.leaf.NotBefore, NotAfter: p.notAfter,
			CreatedBy: a.Name, CreatedAt: now(),
		})
		return err
	})
	if store.IsUnique(err) {
		return apitypes.Certificate{}, conflict("this certificate is uploaded already (`dock certificate list`)")
	}
	if err != nil {
		return apitypes.Certificate{}, err
	}
	c.ownCertsChanged(ctx)
	c.audit(ctx, a, "certificate.upload", nil, nil, p.names[0],
		fmt.Sprintf("uploaded a certificate for %s from %s, valid until %s", strings.Join(p.names, ", "), p.issuer, p.notAfter.UTC().Format(time.DateOnly)))
	return c.toCertificates(a.OrgID, []store.CustomCertificate{row}, domains)[0], nil
}

// ListCertificates returns the org's uploaded certificates, newest first,
// with the domains each serves, in every project: for those who manage
// them. Everyone else sees a domain's certificate on the domain.
func (c *Core) ListCertificates(ctx context.Context, a Actor) ([]apitypes.Certificate, error) {
	if err := a.requireUnlimited(apitypes.ScopeDeploy, "listing certificates"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListOrgCustomCertificates(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	return c.orgCertificates(ctx, a.OrgID, rows)
}

// RemoveCertificate deletes an uploaded certificate. Its domains get
// another upload that covers them, or on-demand certificates again.
func (c *Core) RemoveCertificate(ctx context.Context, a Actor, id int64) error {
	if err := a.requireUnlimited(apitypes.ScopeDeploy, "removing a certificate"); err != nil {
		return err
	}
	row, err := c.store.GetCustomCertificate(ctx, store.GetCustomCertificateParams{ID: id, OrgID: a.OrgID})
	if store.IsNotFound(err) {
		return notFound("certificate %d not found; see `dock certificate list`", id)
	}
	if err != nil {
		return err
	}
	n, err := c.store.DeleteCustomCertificate(ctx, store.DeleteCustomCertificateParams{ID: id, OrgID: a.OrgID})
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("certificate %d not found; see `dock certificate list`", id) // removed meanwhile
	}
	c.ownCertsChanged(ctx)
	c.signalAlert(ctx, alertSignal{Key: certAlertKey(id), Resolve: true, Gone: true})
	c.audit(ctx, a, "certificate.remove", nil, nil, strings.Fields(row.Names)[0],
		fmt.Sprintf("removed the certificate for %s from %s", strings.ReplaceAll(row.Names, " ", ", "), row.Issuer))
	return nil
}

// ownCertsChanged follows a committed upload or removal: it reloads the
// certificates and applies routes (refreshOwnCerts) and wakes the
// certificate worker for the alerts and its next wake, which also tries
// the reload again if it failed here. The change is made either way, so
// its caller goes on to record it.
func (c *Core) ownCertsChanged(ctx context.Context) {
	c.refreshOwnCerts(ctx)
	c.kickWildcards()
}

// orgDomains are an org's verified domains that uploads can reach
// (uploadReach): two queries, the org's domains and the servers.
func (c *Core) orgDomains(ctx context.Context, org int64) ([]store.ListOrgVerifiedDomainsRow, error) {
	domains, err := c.store.ListOrgVerifiedDomains(ctx, org)
	if err != nil || len(domains) == 0 {
		return domains, err
	}
	reach, err := c.uploadReach(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(domains, func(d store.ListOrgVerifiedDomainsRow) bool {
		r, ok := reach[d.ServerID]
		return !ok || tunnelOnly(d.Hostname, d.TunnelStatus, r.base)
	}), nil
}

// serverReach is a server whose routes carry uploads, with the base
// domain tunnelOnly needs.
type serverReach struct{ base string }

// uploadReach are the servers whose routes carry uploads (routeOwnCerts):
// direct ones with TLS, the control plane's own or a node from
// UploadedCertProtocol on.
func (c *Core) uploadReach(ctx context.Context) (map[int64]serverReach, error) {
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	reach := make(map[int64]serverReach, len(servers))
	for _, s := range servers {
		if c.servesUploads(s) {
			reach[s.ID] = serverReach{base: c.ingressOf(s).baseDomain}
		}
	}
	return reach, nil
}

// servesUploads reports whether srv's routes carry uploaded certificates:
// a direct server with TLS that takes them. Others never get the keys.
func (c *Core) servesUploads(srv store.Server) bool {
	ing := c.installedIngress(srv)
	return ing.mode == apitypes.IngressDirect && !ing.plainHTTP && c.takesUploads(srv.ID)
}

// anyOwnCerts reports whether any org has an upload in memory, so route
// applies without one skip the rest at the cost of a lock.
func (c *Core) anyOwnCerts() bool {
	c.own.mu.Lock()
	defer c.own.mu.Unlock()
	return len(c.own.byOrg) > 0
}

// tunnelOnly reports whether a direct server serves host to its
// Cloudflare tunnel only, as tunnelOnlyHosts decides it from host's
// record (status, "" for none): such a domain gets no upload, its
// certificate is Cloudflare's. A name under the base domain still
// reaches the server's IP until its record is ready.
func tunnelOnly(host, status, base string) bool {
	return status != "" && (!atOrUnderBase(host, base) || recordReady(status))
}

// takesUploads reports whether a server's Proxy gets uploaded
// certificates: the control plane's own, or a node whose domain check
// accepts them (remote.UploadedCertProtocol).
func (c *Core) takesUploads(serverID int64) bool {
	return serverID == c.localID || c.nodes != nil && c.nodes.State(serverID).Hello.Protocol >= remote.UploadedCertProtocol
}

// orgCertificates converts rows of org's certificates, with the domains
// each serves now (orgDomains).
func (c *Core) orgCertificates(ctx context.Context, org int64, rows []store.CustomCertificate) ([]apitypes.Certificate, error) {
	domains, err := c.orgDomains(ctx, org)
	if err != nil {
		return nil, err
	}
	return c.toCertificates(org, rows, domains), nil
}

// toCertificates converts rows of org's certificates, each with the
// domains of domains it is the one for now.
func (c *Core) toCertificates(org int64, rows []store.CustomCertificate, domains []store.ListOrgVerifiedDomainsRow) []apitypes.Certificate {
	c.own.mu.Lock()
	certs := slices.Clone(c.own.byOrg[org])
	c.own.mu.Unlock()
	at := now()
	served := map[int64][]apitypes.CertificateDomain{}
	for _, d := range domains {
		if o, ok := bestCert(certs, d.Hostname, at, false); ok {
			served[o.id] = append(served[o.id], apitypes.CertificateDomain{Hostname: d.Hostname, Project: d.Project, Environment: d.Environment, Service: d.Service})
		}
	}
	out := make([]apitypes.Certificate, len(rows))
	for i, r := range rows {
		cert := apitypes.Certificate{
			ID: r.ID, Names: strings.Fields(r.Names), Issuer: r.Issuer, Public: r.Public, Fingerprint: r.Fingerprint,
			NotBefore: r.NotBefore, NotAfter: r.NotAfter, Domains: append([]apitypes.CertificateDomain{}, served[r.ID]...),
			CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt,
		}
		switch {
		case !at.Before(r.NotAfter):
			cert.Status = apitypes.CertificateExpired
		case len(served[r.ID]) == 0:
			cert.Status = apitypes.CertificateUnused
		case r.NotAfter.Sub(at) <= certWarnBefore:
			cert.Status = apitypes.CertificateExpiring
		default:
			cert.Status = apitypes.CertificateActive
		}
		out[i] = cert
	}
	return out
}

// certStage is the expiry stage of a certificate ending at notAfter.
func certStage(notAfter, at time.Time) int64 {
	switch left := notAfter.Sub(at); {
	case left <= 0:
		return certExpired
	case left <= certCriticalBefore:
		return certCritical
	case left <= certWarnBefore:
		return certWarned
	}
	return certFine
}

// certNextStage is when a certificate ending at notAfter reaches its
// next stage after at; zero once it has expired.
func certNextStage(notAfter, at time.Time) time.Time {
	for _, t := range []time.Time{notAfter.Add(-certWarnBefore), notAfter.Add(-certCriticalBefore), notAfter} {
		if at.Before(t) {
			return t
		}
	}
	return time.Time{}
}

// ownCertPass is the certificate worker's pass over the uploads: it
// raises, escalates or resolves their expiry alerts, stops serving one
// that expired, and returns when the next one reaches its next stage
// (zero for none). An alert is about a certificate that serves domains,
// or expired while serving them: a renewal that covers them resolves
// it. A reload or route apply that failed since the last change is
// tried again (5 minutes later if it fails once more). Without uploads it
// is one query; with them, two more per org that has some (orgDomains).
func (c *Core) ownCertPass(ctx context.Context) time.Time {
	at := now()
	// Expired ones still in memory stop being sent now, from the store as
	// it is now; a reload or route apply that failed is tried again.
	c.own.mu.Lock()
	stale := c.own.stale
	for _, certs := range c.own.byOrg {
		stale = stale || slices.ContainsFunc(certs, func(o ownCert) bool { return !at.Before(o.notAfter) })
	}
	c.own.mu.Unlock()
	var next time.Time
	if stale && !c.refreshOwnCerts(ctx) {
		next = at.Add(wildcardRetryMin)
	}

	rows, err := c.store.ListCustomCertificates(ctx)
	if err != nil {
		if ctx.Err() == nil {
			c.log.Error("uploaded certificates", "err", err)
		}
		return at.Add(wildcardRetryMin)
	}
	if !c.resolveGoneCertAlerts(ctx, rows) && next.IsZero() {
		next = at.Add(wildcardRetryMin) // one stayed open: try again
	}
	if len(rows) == 0 {
		return next
	}
	byOrg := map[int64][]ownCert{}
	for _, r := range rows {
		byOrg[r.OrgID] = append(byOrg[r.OrgID], ownCert{id: r.ID, names: strings.Fields(r.Names), notAfter: r.NotAfter})
	}
	// The domains each certificate is (or, expired, was last) the one for:
	// a query per org with uploads, in this background pass.
	serves := map[int64][]string{}
	for org, certs := range byOrg {
		domains, err := c.orgDomains(ctx, org)
		if err != nil {
			if ctx.Err() == nil {
				c.log.Error("uploaded certificates", "err", err)
			}
			return now().Add(wildcardRetryMin)
		}
		for _, d := range domains {
			if o, ok := bestCert(certs, d.Hostname, at, true); ok {
				serves[o.id] = append(serves[o.id], d.Hostname)
			}
		}
	}

	for _, r := range rows {
		want := certFine
		if len(serves[r.ID]) > 0 {
			want = certStage(r.NotAfter, at)
		}
		if want != r.Alerted && !c.certAlert(ctx, r, want, serves[r.ID]) {
			soon := at.Add(wildcardRetryMin) // the stage wasn't recorded: again soon
			if next.IsZero() || soon.Before(next) {
				next = soon
			}
		}
		if t := certNextStage(r.NotAfter, at); !t.IsZero() && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	return next
}

// certAlert moves certificate r's alert to stage, serving hosts: opened
// as a warning 30 days before it ends, critical 7 days before (the one
// escalation that notifies again), said to have expired at the end
// without another notification, resolved when nothing needs it.
//
// The alert goes first and the stage is recorded only once the alert
// engine holds it, so a failed alert write is tried again by a later
// pass, while a warning someone resolved by hand stays resolved at its
// stage. It reports false when either didn't take, for the pass to try
// again soon; a certificate removed meanwhile counts as done.
func (c *Core) certAlert(ctx context.Context, r store.CustomCertificate, stage int64, hosts []string) bool {
	key := certAlertKey(r.ID)
	var want alertSignal
	if stage == certFine {
		c.signalAlert(ctx, alertSignal{Key: key, Resolve: true, Gone: true})
	} else {
		want = certAlertSignal(r, stage, hosts)
		c.signalAlert(ctx, want)
	}
	// Took: resolved, or open as this stage says (a failed update leaves
	// the previous stage's alert open).
	c.alerts.mu.Lock()
	cur, open := c.alerts.open[key]
	c.alerts.mu.Unlock()
	took := !open
	if stage != certFine {
		took = open && cur.Severity == string(want.Severity) && cur.Title == want.Title
	}
	if !took {
		// The engine logged why. A removal that resolved it in between
		// lands here too; the next pass doesn't see that row.
		return false
	}
	n, err := c.store.SetCustomCertificateAlerted(ctx, store.SetCustomCertificateAlertedParams{Alerted: stage, ID: r.ID})
	if err != nil {
		c.log.Error("record an uploaded certificate's alert", "certificate", r.ID, "err", err)
		return false
	}
	if n == 0 && stage != certFine {
		// Removed after the raise; its removal's resolve may have come
		// first. IDs aren't reused, so this alert is that certificate's.
		// (Had this write failed instead, the next pass's sweep of alerts
		// without a certificate resolves it: resolveGoneCertAlerts.)
		c.signalAlert(ctx, alertSignal{Key: key, Resolve: true, Gone: true})
	}
	return true
}

// resolveGoneCertAlerts resolves the open expiry alerts of certificates
// rows no longer hold: one whose raise crossed its removal (certAlert)
// or whose org was deleted. The open alerts are in memory, so this costs
// no query. It reports whether all of them closed.
func (c *Core) resolveGoneCertAlerts(ctx context.Context, rows []store.CustomCertificate) bool {
	have := make(map[string]bool, len(rows))
	for _, r := range rows {
		have[certAlertKey(r.ID)] = true
	}
	var gone []string
	c.alerts.mu.Lock()
	for key := range c.alerts.open {
		if strings.HasPrefix(key, certAlertPrefix) && !have[key] {
			gone = append(gone, key)
		}
	}
	c.alerts.mu.Unlock()
	for _, key := range gone {
		c.signalAlert(ctx, alertSignal{Key: key, Resolve: true, Gone: true})
	}
	c.alerts.mu.Lock()
	defer c.alerts.mu.Unlock()
	return !slices.ContainsFunc(gone, func(key string) bool { _, open := c.alerts.open[key]; return open })
}

// certAlertSignal is certificate r's expiry alert at stage.
func certAlertSignal(r store.CustomCertificate, stage int64, hosts []string) alertSignal {
	name := strings.Fields(r.Names)[0]
	end := r.NotAfter.UTC().Format(time.DateOnly)
	serving := hostList(hosts)
	sig := alertSignal{
		Key: certAlertKey(r.ID), Kind: apitypes.AlertCertificate, Severity: apitypes.SeverityWarning,
		Command: "dock certificate list", orgID: r.OrgID,
		Title:   fmt.Sprintf("Certificate for %s expires on %s", name, end),
		Message: fmt.Sprintf("The certificate from %s that serves %s doesn't renew. Upload its renewal (`dock certificate add`): it takes over at once. Once it expires, Dock stops serving it and asks Let's Encrypt for these domains, which works only for public names.", r.Issuer, serving),
	}
	switch stage {
	case certCritical:
		sig.Severity = apitypes.SeverityCritical
	case certExpired:
		sig.Severity = apitypes.SeverityCritical
		sig.Title = fmt.Sprintf("Certificate for %s expired on %s", name, end)
		sig.Message = fmt.Sprintf("The certificate from %s is no longer served. %s get certificates from Let's Encrypt where it can issue them (public names only). Upload a renewed certificate (`dock certificate add`), or remove this one (`dock certificate remove %d`).", r.Issuer, serving, r.ID)
	}
	return sig
}

// hostList names hosts in a sentence: the first five, then how many more,
// so a wildcard serving hundreds of domains keeps its alert short.
func hostList(hosts []string) string {
	const shown = 5
	if len(hosts) <= shown {
		return strings.Join(hosts, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(hosts[:shown], ", "), len(hosts)-shown)
}

// certAlertPrefix starts every uploaded certificate's alert key.
const certAlertPrefix = "certificate:"

// certAlertKey names an uploaded certificate's expiry alert.
func certAlertKey(id int64) string { return certAlertPrefix + strconv.FormatInt(id, 10) }
