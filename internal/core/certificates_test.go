package core

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// testPKI issues certificates for the upload tests: a root and an
// intermediate under it, as an internal CA would.
type testPKI struct {
	t                 *testing.T
	root, inter       *x509.Certificate
	rootKey, interKey *ecdsa.PrivateKey
}

func newTestPKI(t *testing.T) *testPKI {
	p := &testPKI{t: t}
	p.root, p.rootKey = p.sign("Test Root CA", nil, true, nil, nil, time.Now().Add(-time.Hour), time.Now().Add(10*365*24*time.Hour))
	p.inter, p.interKey = p.sign("Test Issuing CA", nil, true, p.root, p.rootKey, time.Now().Add(-time.Hour), time.Now().Add(5*365*24*time.Hour))
	return p
}

// sign makes a certificate for names, a CA's or a leaf's, signed by
// parent (nil: itself).
func (p *testPKI) sign(cn string, names []string, ca bool, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, from, until time.Time) (*x509.Certificate, *ecdsa.PrivateKey) {
	p.t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn}, DNSNames: names,
		NotBefore: from, NotAfter: until, BasicConstraintsValid: true, IsCA: ca,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature,
	}
	if ca {
		tmpl.KeyUsage, tmpl.ExtKeyUsage = x509.KeyUsageCertSign, nil
	}
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		p.t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c, key
}

// issue makes a leaf for names, signed by the intermediate.
func (p *testPKI) issue(names []string, from, until time.Time) (*x509.Certificate, *ecdsa.PrivateKey) {
	cn := "nameless"
	if len(names) > 0 {
		cn = names[0]
	}
	return p.sign(cn, names, false, p.inter, p.interKey, from, until)
}

// leaf issues a leaf for names valid from..until and returns its chain
// (leaf, intermediate) and key in PEM.
func (p *testPKI) leaf(names []string, from, until time.Time) (chain, key string) {
	c, k := p.issue(names, from, until)
	return pemCerts(c, p.inter), pemKey(p.t, k)
}

func pemCerts(cs ...*x509.Certificate) string {
	var b strings.Builder
	for _, c := range cs {
		b.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}))
	}
	return b.String()
}

func pemKey(t *testing.T, k *ecdsa.PrivateKey) string {
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
}

// TestParseUpload: what an upload must be, the chain put in order, and
// whether it is public.
func TestParseUpload(t *testing.T) {
	p := newTestPKI(t)
	at := time.Now()
	leaf, key := p.issue([]string{"App.Example.com", "*.example.com"}, at.Add(-time.Hour), at.Add(90*24*time.Hour))
	keyPEM := pemKey(t, key)

	// Out of order, root included: leaf first after, every certificate kept.
	got, err := parseUpload(pemCerts(p.root, p.inter, leaf), keyPEM, at, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := pemCerts(leaf, p.inter, p.root); string(got.chainPEM) != want {
		t.Error("chain not put in order leaf, intermediate, root")
	}
	if !slices.Equal(got.names, []string{"app.example.com", "*.example.com"}) || got.issuer != "Test Issuing CA" || got.public {
		t.Errorf("parsed %v from %q, public %v", got.names, got.issuer, got.public)
	}
	// An issuer's control characters never reach a terminal.
	evil := &testPKI{t: t}
	evil.inter, evil.interKey = p.sign("Evil\x1b[2J CA\n", nil, true, nil, nil, at.Add(-time.Hour), at.Add(time.Hour))
	evilLeaf, evilKey := evil.issue([]string{"evil.example.com"}, at.Add(-time.Hour), at.Add(time.Hour))
	if got, err := parseUpload(pemCerts(evilLeaf), pemKey(t, evilKey), at, nil); err != nil || got.issuer != "Evil[2J CA" {
		t.Errorf("issuer with control characters: %q, %v", got.issuer, err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(p.root)
	if got, err := parseUpload(pemCerts(leaf, p.inter), keyPEM, at, roots); err != nil || !got.public {
		t.Errorf("chaining to a trusted root: public %v, %v", got.public, err)
	}

	other, otherKey := p.issue([]string{"other.example.com"}, at.Add(-time.Hour), at.Add(time.Hour))
	expired, expiredKey := p.issue([]string{"old.example.com"}, at.Add(-48*time.Hour), at.Add(-time.Hour))
	future, futureKey := p.issue([]string{"new.example.com"}, at.Add(time.Hour), at.Add(48*time.Hour))
	nameless, namelessKey := p.issue(nil, at.Add(-time.Hour), at.Add(time.Hour))
	dotted, dottedKey := p.issue([]string{"shop.example.com."}, at.Add(-time.Hour), at.Add(time.Hour))
	oldCA, oldKey := p.sign("Old Issuing CA", nil, true, p.root, p.rootKey, at.Add(-48*time.Hour), at.Add(-time.Hour))
	underOld, underOldKey := p.sign("under.example.com", []string{"under.example.com"}, false, oldCA, oldKey, at.Add(-time.Hour), at.Add(time.Hour))
	// Named like a self-signed root, but signed by another CA of that name:
	// still an intermediate, and expired.
	twinRoot, twinRootKey := p.sign("Twin CA", nil, true, nil, nil, at.Add(-48*time.Hour), at.Add(48*time.Hour))
	twin, twinKey := p.sign("Twin CA", nil, true, twinRoot, twinRootKey, at.Add(-48*time.Hour), at.Add(-time.Hour))
	underTwin, underTwinKey := p.sign("twin.example.com", []string{"twin.example.com"}, false, twin, twinKey, at.Add(-time.Hour), at.Add(time.Hour))
	for _, c := range []struct{ name, chain, key, want string }{
		{"an expired intermediate", pemCerts(underOld, oldCA), pemKey(t, underOldKey), "Old Issuing CA isn't valid now"},
		{"an expired intermediate named like its issuer", pemCerts(underTwin, twin), pemKey(t, underTwinKey), "Twin CA isn't valid now"},
		{"key of another certificate", pemCerts(leaf, p.inter), pemKey(t, otherKey), "doesn't belong to any certificate"},
		{"a certificate outside the chain", pemCerts(leaf, other), keyPEM, "the certificate for other.example.com, which didn't issue"},
		{"expired", pemCerts(expired), pemKey(t, expiredKey), "expired on"},
		{"not valid yet", pemCerts(future), pemKey(t, futureKey), "isn't valid until"},
		{"no DNS names", pemCerts(nameless), pemKey(t, namelessKey), "names no DNS hostname"},
		{"only a name with a trailing dot", pemCerts(dotted), pemKey(t, dottedKey), "names no DNS hostname"},
		{"a key in the chain", pemCerts(leaf) + keyPEM, keyPEM, "holds a private key"},
		{"no certificate", "hello", keyPEM, "no PEM certificate"},
		{"encrypted key", pemCerts(leaf), "-----BEGIN ENCRYPTED PRIVATE KEY-----\nAAAA\n-----END ENCRYPTED PRIVATE KEY-----\n", "is encrypted"},
		{"a certificate as the key", pemCerts(leaf), pemCerts(leaf), "holds a certificate"},
	} {
		if _, err := parseUpload(c.chain, c.key, at, nil); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// uploaded is the uploaded certificate the fake server loaded for host.
func uploaded(rt interface{ Certificates() []node.Certificate }, host string) (node.Certificate, bool) {
	for _, c := range rt.Certificates() {
		if c.Uploaded && slices.Contains(c.Names, host) {
			return c, true
		}
	}
	return node.Certificate{}, false
}

// TestUploadedCertificate: an upload serves the org's verified domains its
// names cover, by exact hostname, and nothing else; the control plane's
// Caddy gets no on-demand certificate for them; a renewal takes over at
// once and removing it goes back to the old one.
func TestUploadedCertificate(t *testing.T) {
	h := newHarness(t)
	p := newTestPKI(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1", Port: 8080})
	h.deploy(web, "app:1")
	for _, host := range []string{"shop.example.com", "deep.shop.example.com", "elsewhere.test"} {
		if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: host}); err != nil {
			t.Fatal(err)
		}
	}
	chain, key := p.leaf([]string{"*.example.com"}, time.Now().Add(-time.Hour), time.Now().Add(200*24*time.Hour))
	if _, err := h.c.AddCertificate(h.ctx, testSystem, apitypes.AddCertificateRequest{Chain: chain, Key: key, Domain: "elsewhere.test"}); err == nil || !strings.Contains(err.Error(), "not elsewhere.test") {
		t.Fatalf("upload checked against another domain: %v", err)
	}
	first, err := h.c.AddCertificate(h.ctx, testSystem, apitypes.AddCertificateRequest{Chain: chain, Key: key, Domain: "shop.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != apitypes.CertificateActive || len(first.Domains) != 1 || first.Domains[0].Hostname != "shop.example.com" || first.Domains[0].Service != "web" {
		t.Fatalf("uploaded %+v", first)
	}
	got, ok := uploaded(h.rt, "shop.example.com")
	if !ok || !slices.Equal(got.Names, []string{"shop.example.com"}) || !strings.Contains(string(got.KeyPEM), "PRIVATE KEY") {
		t.Fatalf("served %+v (%v)", got.Names, ok)
	}
	if allow, err := h.c.AllowCertificate(h.ctx, "shop.example.com"); err != nil || allow {
		t.Errorf("on demand for a covered domain: %v %v", allow, err)
	}
	if allow, _ := h.c.AllowCertificate(h.ctx, "deep.shop.example.com"); !allow {
		t.Error("a name two labels under the wildcard lost its on-demand certificate")
	}
	ds, err := h.c.ListDomains(h.ctx, testSystem, web)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if (d.Certificate != nil) != (d.Hostname == "shop.example.com") {
			t.Errorf("%s shows certificate %+v", d.Hostname, d.Certificate)
		}
	}
	if _, err := h.c.AddCertificate(h.ctx, testSystem, apitypes.AddCertificateRequest{Chain: chain, Key: key}); err == nil || !strings.Contains(err.Error(), "uploaded already") {
		t.Errorf("the same certificate twice: %v", err)
	}

	// A renewal that ends later takes over; the first is then unused.
	chain2, key2 := p.leaf([]string{"shop.example.com"}, time.Now().Add(-time.Hour), time.Now().Add(400*24*time.Hour))
	second, err := h.c.AddCertificate(h.ctx, testSystem, apitypes.AddCertificateRequest{Chain: chain2, Key: key2})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := uploaded(h.rt, "shop.example.com"); string(got.ChainPEM) != chain2 {
		t.Error("the renewal isn't served")
	}
	list, err := h.c.ListCertificates(h.ctx, testSystem)
	if err != nil || len(list) != 2 || list[0].ID != second.ID || list[1].Status != apitypes.CertificateUnused {
		t.Fatalf("list %+v %v", list, err)
	}
	if err := h.c.RemoveCertificate(h.ctx, testSystem, second.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := uploaded(h.rt, "shop.example.com"); string(got.ChainPEM) != chain {
		t.Error("removing the renewal didn't go back to the first")
	}

	// A renewal whose route apply fails isn't shown on the domain until a
	// later apply carries it: the worker tries again.
	shown := func() int64 {
		t.Helper()
		ds, err := h.c.ListDomains(h.ctx, testSystem, web)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range ds {
			if d.Hostname == "shop.example.com" && d.Certificate != nil {
				return d.Certificate.ID
			}
		}
		return 0
	}
	h.rt.FailApplyRoutes(errors.New("caddy: load refused"))
	chain3, key3 := p.leaf([]string{"shop.example.com"}, time.Now().Add(-time.Hour), time.Now().Add(500*24*time.Hour))
	third, err := h.c.AddCertificate(h.ctx, testSystem, apitypes.AddCertificateRequest{Chain: chain3, Key: key3})
	if err != nil {
		t.Fatal(err)
	}
	if got := shown(); got != first.ID {
		t.Errorf("after a failed apply the domain shows certificate %d, want %d (what the server has)", got, first.ID)
	}
	h.rt.FailApplyRoutes(nil)
	h.c.kickWildcards()
	eventually(t, "the renewal after the apply is retried", func() bool { return shown() == third.ID })
	if err := h.c.RemoveCertificate(h.ctx, testSystem, third.ID); err != nil {
		t.Fatal(err)
	}

	// Kept across a restart, key opened again.
	h.restart()
	eventually(t, "the upload after a restart", func() bool { _, ok := uploaded(h.rt, "shop.example.com"); return ok })
	if err := h.c.RemoveCertificate(h.ctx, testSystem, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := uploaded(h.rt, "shop.example.com"); ok {
		t.Error("a removed certificate is still served")
	}
}

// TestUploadedCertificateExpiry: expiry alerts open only for a
// certificate that serves domains, critical within 7 days, and resolve
// when a renewal covers its domains; an expired one stops being served
// and says so.
func TestUploadedCertificateExpiry(t *testing.T) {
	h := newHarness(t)
	p := newTestPKI(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1", Port: 8080})
	h.deploy(web, "app:1")
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	add := func(names []string, until time.Time) apitypes.Certificate {
		t.Helper()
		chain, key := p.leaf(names, time.Now().Add(-time.Hour), until)
		c, err := h.c.AddCertificate(h.ctx, testSystem, apitypes.AddCertificateRequest{Chain: chain, Key: key})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	unused := add([]string{"nothing.example.org"}, time.Now().Add(5*24*time.Hour))
	soon := add([]string{"shop.example.com"}, time.Now().Add(3*24*time.Hour))
	if soon.Status != apitypes.CertificateExpiring {
		t.Errorf("status %s", soon.Status)
	}
	a := h.openAlert(certAlertKey(soon.ID), true)
	if a.Severity != string(apitypes.SeverityCritical) || a.OrgID == nil || !strings.Contains(a.Message, "shop.example.com") {
		t.Errorf("alert %+v", a)
	}
	h.openAlert(certAlertKey(unused.ID), false)

	// A renewal covers it: resolved, and the renewal is far from expiry.
	renewal := add([]string{"shop.example.com"}, time.Now().Add(300*24*time.Hour))
	h.openAlert(certAlertKey(soon.ID), false)
	h.openAlert(certAlertKey(renewal.ID), false)
	if err := h.c.RemoveCertificate(h.ctx, testSystem, renewal.ID); err != nil {
		t.Fatal(err)
	}
	h.openAlert(certAlertKey(soon.ID), true)
	if err := h.c.RemoveCertificate(h.ctx, testSystem, soon.ID); err != nil {
		t.Fatal(err)
	}
	h.openAlert(certAlertKey(soon.ID), false)

	// One that ends in three seconds (as TestWildcardExpires) stops being
	// served when it does.
	short := add([]string{"shop.example.com"}, time.Now().Add(3*time.Second))
	if _, ok := uploaded(h.rt, "shop.example.com"); !ok {
		t.Fatal("not served before it expires")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := uploaded(h.rt, "shop.example.com"); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the expired certificate is still served")
		}
		time.Sleep(10 * time.Millisecond)
	}
	eventually(t, "the expired alert", func() bool {
		h.c.alerts.mu.Lock()
		defer h.c.alerts.mu.Unlock()
		a, ok := h.c.alerts.open[certAlertKey(short.ID)]
		return ok && strings.Contains(a.Title, "expired")
	})
	if allow, _ := h.c.AllowCertificate(h.ctx, "shop.example.com"); !allow {
		t.Error("an expired upload still keeps its domain from on-demand certificates")
	}
}

// TestCertStages: when the alerts are due.
func TestCertStages(t *testing.T) {
	at := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	for _, c := range []struct {
		left  time.Duration
		stage int64
		next  time.Time
	}{
		{90 * day, certFine, at.Add(60 * day)},
		{30 * day, certWarned, at.Add(23 * day)},
		{7 * day, certCritical, at.Add(7 * day)},
		{time.Second, certCritical, at.Add(time.Second)},
		{0, certExpired, time.Time{}},
	} {
		end := at.Add(c.left)
		if got := certStage(end, at); got != c.stage {
			t.Errorf("%v left: stage %d, want %d", c.left, got, c.stage)
		}
		if got := certNextStage(end, at); !got.Equal(c.next) {
			t.Errorf("%v left: next %v, want %v", c.left, got, c.next)
		}
	}
}

// Uploads serve every project's domains, so they take domains edit on
// the whole org (certificates.manage): one project's is not enough, and
// a viewer can't.
func TestCertificatesNeedTheWholeOrg(t *testing.T) {
	h := newTakeHarness(t)
	p := newTestPKI(t)
	chain, key := p.leaf([]string{"shop.example.com"}, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour))
	add := func(a Actor) error {
		_, err := h.c.AddCertificate(h.ctx, a, apitypes.AddCertificateRequest{Chain: chain, Key: key})
		return err
	}
	if _, err := h.c.GrantMember(h.ctx, h.ada, h.vi.Email, apitypes.GrantRequest{Role: "domains:edit", Project: "shop"}); err != nil {
		t.Fatal(err)
	}
	if err := add(h.again(h.vi)); err == nil {
		t.Fatal("domains edit on one project uploaded a certificate")
	}
	if _, err := h.c.ListCertificates(h.ctx, h.again(h.vi)); err == nil {
		t.Fatal("domains edit on one project listed the org's certificates")
	}
	if _, err := h.c.GrantMember(h.ctx, h.ada, h.vi.Email, apitypes.GrantRequest{Role: "domains:edit"}); err != nil {
		t.Fatal(err)
	}
	if err := add(h.again(h.vi)); err != nil {
		t.Fatalf("domains edit on the org: %v", err)
	}
}

// The worker resolves an expiry alert whose certificate is gone, as when
// its raise crossed its removal.
func TestGoneCertificateAlertResolves(t *testing.T) {
	h := newHarness(t)
	h.c.signalAlert(h.ctx, alertSignal{Key: certAlertKey(999), Kind: apitypes.AlertCertificate, Severity: apitypes.SeverityWarning, Title: "Certificate for gone.example.com expires", orgID: 1})
	// The worker's first pass may already have swept it; kicked, the next
	// pass surely does. Either way it ends resolved.
	h.c.kickWildcards()
	h.openAlert(certAlertKey(999), false)
}

// A chain ends when a needed intermediate does, before its leaf: that end
// is what is stored, shown and alerted on (stages run from not_after).
func TestCertificateEndsWithItsChain(t *testing.T) {
	h := newHarness(t)
	p := newTestPKI(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1", Port: 8080})
	h.deploy(web, "app:1")
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	soon := at.Add(5 * 24 * time.Hour).Truncate(time.Second)
	inter, interKey := p.sign("Short Issuing CA", nil, true, p.root, p.rootKey, at.Add(-time.Hour), soon)
	leaf, key := p.sign("shop.example.com", []string{"shop.example.com"}, false, inter, interKey, at.Add(-time.Hour), at.Add(300*24*time.Hour))
	parsed, err := parseUpload(pemCerts(leaf, inter), pemKey(t, key), at, nil)
	if err != nil || !parsed.notAfter.Equal(soon) {
		t.Fatalf("parsed end %v, %v; want the intermediate's %v", parsed.notAfter, err, soon)
	}
	c, err := h.c.AddCertificate(h.ctx, testSystem, apitypes.AddCertificateRequest{Chain: pemCerts(leaf, inter), Key: pemKey(t, key)})
	if err != nil {
		t.Fatal(err)
	}
	if !c.NotAfter.Equal(soon) || c.Status != apitypes.CertificateExpiring {
		t.Errorf("uploaded: ends %v, %s; want %v, expiring", c.NotAfter, c.Status, soon)
	}
	// Within 7 days of the intermediate's end: critical, though the leaf
	// has 300 days.
	if a := h.openAlert(certAlertKey(c.ID), true); a.Severity != string(apitypes.SeverityCritical) {
		t.Errorf("alert severity %s, want critical", a.Severity)
	}
	if got := certNextStage(c.NotAfter, at); !got.Equal(soon) {
		t.Errorf("next stage %v, want the end %v: the route refresh happens there", got, soon)
	}
}
