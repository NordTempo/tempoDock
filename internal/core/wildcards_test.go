package core

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/acmedns"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cloudflare/cloudflaretest"
	"github.com/NordTempo/tempoDock/internal/store"
)

// fakeCA stands in for the ACME CA (ADR 0090): it answers through the
// request's DNS as acmedns.Obtain does (both challenge records, one wait,
// cleaned up after) and signs what it issues with a CA of its own.
type fakeCA struct {
	t   *testing.T
	ca  *x509.Certificate
	key *ecdsa.PrivateKey

	mu sync.Mutex
	// calls counts obtains; succeed, when not negative, is how many more
	// succeed before the rest fail with errCAFailed.
	calls, succeed int
	// lifetime and backdate shape issued certificates: valid from
	// now-backdate for lifetime.
	lifetime, backdate time.Duration
	// waited are the zones waitTXT was asked about.
	waited []string
}

var errCAFailed = errors.New("acme: 500 urn:ietf:params:acme:error:serverInternal: the CA is down")

func newFakeCA(t *testing.T) *fakeCA {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Fake ACME CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(der)
	return &fakeCA{t: t, ca: ca, key: key, succeed: -1, lifetime: 90 * 24 * time.Hour}
}

func (f *fakeCA) obtain(ctx context.Context, r acmedns.Request) (cert acmedns.Certificate, err error) {
	f.mu.Lock()
	f.calls++
	fail := f.succeed == 0
	if f.succeed > 0 {
		f.succeed--
	}
	lifetime, backdate := f.lifetime, f.backdate
	f.mu.Unlock()

	name := acmedns.ChallengeName(r.Domain)
	defer func() {
		if cerr := r.DNS.CleanUp(context.WithoutCancel(ctx), name); cerr != nil && err == nil {
			err = cerr
		}
	}()
	values := []string{"wild-" + r.Domain, "apex-" + r.Domain}
	for _, v := range values {
		if err := r.DNS.Present(ctx, name, v); err != nil {
			return acmedns.Certificate{}, err
		}
	}
	if err := r.DNS.Wait(ctx, name, values); err != nil {
		return acmedns.Certificate{}, err
	}
	if fail {
		return acmedns.Certificate{}, errCAFailed
	}

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	from := time.Now().Add(-backdate).Truncate(time.Second)
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), DNSNames: acmedns.Names(r.Domain),
		NotBefore: from, NotAfter: from.Add(lifetime),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature,
	}, f.ca, &key.PublicKey, f.key)
	if err != nil {
		return acmedns.Certificate{}, err
	}
	leaf, _ := x509.ParseCertificate(der)
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.ca.Raw})...)
	return acmedns.Certificate{
		ChainPEM: chain, KeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter,
	}, nil
}

func (f *fakeCA) waitTXT(_ context.Context, zone, _ string, _ []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waited = append(f.waited, zone)
	return nil
}

func (f *fakeCA) obtained() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// wildPool is a direct control plane ("cp", base domain apps.test) and a
// direct remote server vps2 without a base domain, with a fake Cloudflare
// holding zones; connect adds the platform connection to it.
func wildPool(t *testing.T, zones ...string) (*pool, *cloudflaretest.Server, func()) {
	t.Helper()
	p := newPool(t, poolServer{"cp", true, false, 8}, poolServer{"vps2", true, false, 8})
	cf := cloudflaretest.New(t, zones...)
	p.opts.CloudflareAPI = cf.URL
	p.restart()
	return p, cf, func() {
		t.Helper()
		if _, err := p.c.AddCloudflare(p.ctx, testSystem, true, apitypes.CreateCloudflareRequest{Name: "home", Token: cloudflaretest.Token}); err != nil {
			t.Fatal(err)
		}
	}
}

// wildcardOf waits until the Address page's wildcard for base passes
// done, and returns it.
func (p *pool) wildcardOf(base string, done func(apitypes.Wildcard) bool) apitypes.Wildcard {
	p.t.Helper()
	var got apitypes.Wildcard
	waitFor(p.t, func() bool {
		addr, err := p.c.Address(p.ctx, testSystem)
		if err != nil {
			p.t.Fatal(err)
		}
		i := slices.IndexFunc(addr.Wildcards, func(w apitypes.Wildcard) bool { return w.BaseDomain == base })
		if i < 0 {
			return false
		}
		got = addr.Wildcards[i]
		return done(got)
	})
	return got
}

// TestWildcardCertificate: a direct server's base domain in a platform
// zone gets one wildcard, through TXT records Dock makes and removes, and
// every server with that base domain gets it with its routes. It is kept
// sealed across restarts, not issued again.
func TestWildcardCertificate(t *testing.T) {
	p, cf, connect := wildPool(t, "apps.test")
	connect()
	waitFor(t, func() bool { return len(p.rt.Certificates()) == 1 })
	got := p.rt.Certificates()[0]
	if !slices.Equal(got.Names, []string{"*.apps.test", "apps.test"}) {
		t.Fatalf("certificate names %v", got.Names)
	}
	if _, err := tls.X509KeyPair(got.ChainPEM, got.KeyPEM); err != nil {
		t.Fatalf("served chain and key: %v", err)
	}
	if left := cf.Named("_acme-challenge.apps.test"); len(left) != 0 {
		t.Errorf("challenge records left behind: %+v", left)
	}
	cf.Mu.Lock()
	creates := 0
	for _, c := range cf.Calls {
		if strings.HasPrefix(c, "POST /zones/") && strings.HasSuffix(c, "/dns_records") {
			creates++
		}
	}
	cf.Mu.Unlock()
	if creates != 2 || p.ca.obtained() != 1 || !slices.Equal(p.ca.waited, []string{"apps.test"}) {
		t.Errorf("%d records made, %d obtains, waited in %v", creates, p.ca.obtained(), p.ca.waited)
	}

	vps := p.rts[p.ids["vps2"]]
	if len(vps.Certificates()) != 0 {
		t.Fatal("vps2 has no base domain but got the certificate")
	}
	base := "apps.test"
	if _, err := p.c.UpdateServer(p.ctx, testSystem, "vps2", apitypes.UpdateServerRequest{BaseDomain: &base}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(vps.Certificates()) == 1 })
	if p.ca.obtained() != 1 {
		t.Errorf("a second server on the base domain made %d obtains", p.ca.obtained())
	}

	w := p.wildcardOf("apps.test", func(w apitypes.Wildcard) bool { return len(w.Servers) == 2 })
	if w.Status != apitypes.WildcardActive || w.Reason != "" || w.NotAfter == nil || w.RenewAt == nil || !w.RenewAt.Equal(w.NotAfter.Add(-30*24*time.Hour)) {
		t.Errorf("Address shows %+v", w)
	}

	// Every op since the restart: background work (a log prune) may run
	// after the routes are set.
	before := len(p.rt.Ops())
	p.restart()
	if since := p.rt.Ops()[before:]; !slices.ContainsFunc(since, func(op string) bool { return strings.Contains(op, "certs *.apps.test,apps.test") }) {
		t.Errorf("routes after a restart don't carry the certificate: %v", since)
	}
	if p.ca.obtained() != 1 {
		t.Errorf("a restart obtained again (%d)", p.ca.obtained())
	}
}

// TestWildcardFallback: without a zone for the base domain there is no
// wildcard and no attempt; with a token that can't write DNS records the
// attempt fails, waits for a change and raises the platform alert; the
// names keep on-demand certificates meanwhile. A retry after the token is
// fixed issues it and resolves the alert.
func TestWildcardFallback(t *testing.T) {
	p, cf, connect := wildPool(t, "apps.test")
	w := p.wildcardOf("apps.test", func(apitypes.Wildcard) bool { return true })
	if w.Status != apitypes.WildcardUnavailable || !strings.Contains(w.Reason, "isn't in a zone") {
		t.Errorf("without a zone: %+v", w)
	}

	cf.Mu.Lock()
	cf.Fail = func(r *http.Request) (int, int) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/dns_records") {
			return http.StatusForbidden, 0
		}
		return 0, 0
	}
	cf.Mu.Unlock()
	connect()
	w = p.wildcardOf("apps.test", func(w apitypes.Wildcard) bool { return w.Status == apitypes.WildcardFailed })
	if !strings.Contains(w.Reason, dnsEditPerm) || w.RetryAt != nil {
		t.Errorf("without DNS permission: %+v", w)
	}
	a := p.openAlert(wildcardAlertKey("apps.test"), true)
	if a.Kind != string(apitypes.AlertCertificate) || !strings.Contains(a.Message, "certificate-transparency") {
		t.Errorf("alert %+v", a)
	}
	if len(p.rt.Certificates()) != 0 {
		t.Error("routes carry a certificate that wasn't issued")
	}

	cf.Mu.Lock()
	cf.Fail = nil
	cf.Mu.Unlock()
	if _, err := p.c.RetryCloudflare(p.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	p.wildcardOf("apps.test", func(w apitypes.Wildcard) bool { return w.Status == apitypes.WildcardActive })
	p.openAlert(wildcardAlertKey("apps.test"), false)
	waitFor(t, func() bool { return len(p.rt.Certificates()) == 1 })
}

// TestWildcardExpires: a certificate whose renewals fail is served until
// it expires, then routes go out without it, back to on demand.
func TestWildcardExpires(t *testing.T) {
	p, _, connect := wildPool(t, "apps.test")
	p.ca.mu.Lock()
	// Valid for 3 more seconds, so renewal is due at once; only the first
	// issuance works.
	p.ca.backdate, p.ca.succeed = 90*24*time.Hour-3*time.Second, 1
	p.ca.mu.Unlock()
	connect()
	waitFor(t, func() bool { return len(p.rt.Certificates()) == 1 })
	deadline := time.Now().Add(10 * time.Second)
	for len(p.rt.Certificates()) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the expired certificate is still served")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p.ca.obtained() < 2 {
		t.Errorf("no renewal was tried (%d obtains)", p.ca.obtained())
	}
	w := p.wildcardOf("apps.test", func(apitypes.Wildcard) bool { return true })
	if w.Status != apitypes.WildcardFailed || w.RetryAt == nil || !strings.Contains(w.Reason, "the CA is down") {
		t.Errorf("after expiry: %+v", w)
	}
}

// TestWildcardTiming: renewal at a third of the lifetime before the end,
// what is due when, and the backoff.
func TestWildcardTiming(t *testing.T) {
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	from, until := at.Add(-10*24*time.Hour), at.Add(80*24*time.Hour)
	if got := renewAt(from, until); !got.Equal(until.Add(-30 * 24 * time.Hour)) {
		t.Errorf("renewAt of a 90-day certificate = %v", got)
	}
	if got := renewAt(at, at.Add(6*24*time.Hour)); !got.Equal(at.Add(4 * 24 * time.Hour)) {
		t.Errorf("renewAt of a 6-day certificate = %v", got)
	}

	soon, past := at.Add(time.Minute), at.Add(-time.Minute)
	issued := store.WildcardCertificate{NotBefore: &from, NotAfter: &until}
	for _, c := range []struct {
		name   string
		r      store.WildcardCertificate
		kicked bool
		due    bool
		next   time.Time
	}{
		{"never tried", store.WildcardCertificate{}, false, true, time.Time{}},
		{"issued", issued, false, false, renewAt(from, until)},
		{"renewal due", store.WildcardCertificate{NotBefore: new(at.Add(-70 * 24 * time.Hour)), NotAfter: new(at.Add(20 * 24 * time.Hour))}, false, true, at.Add(-10 * 24 * time.Hour)},
		{"retry later", store.WildcardCertificate{Error: "x", RetryAt: &soon, NotBefore: &from, NotAfter: &until}, true, false, soon},
		{"retry due", store.WildcardCertificate{Error: "x", RetryAt: &past}, false, true, past},
		{"waiting, timer", store.WildcardCertificate{Error: "x", NotBefore: &from, NotAfter: &until}, false, false, until},
		{"waiting, kicked", store.WildcardCertificate{Error: "x"}, true, true, time.Time{}},
	} {
		if got := wildcardDue(c.r, c.kicked, at); got != c.due {
			t.Errorf("%s: due = %v", c.name, got)
		}
		if got := wildcardNext(c.r, at); !got.Equal(c.next) {
			t.Errorf("%s: next = %v, want %v", c.name, got, c.next)
		}
	}

	for attempts, want := range map[int64]time.Duration{1: 5 * time.Minute, 2: 10 * time.Minute, 4: 40 * time.Minute, 7: 5*time.Hour + 20*time.Minute, 8: 6 * time.Hour, 30: 6 * time.Hour} {
		if got := wildcardBackoff(attempts); got != want {
			t.Errorf("backoff after %d = %v, want %v", attempts, got, want)
		}
	}
}

// serverCertificates leaves a certificate out where Caddy can't use it:
// tunnel and plain-HTTP servers, and other base domains; what it covers
// gets no on-demand certificate on the control plane.
func TestServerCertificatesOnlyForTheirBase(t *testing.T) {
	h := newHarness(t)
	h.c.setWildcard("other.test", []byte("C"), []byte("K"), now().Add(time.Hour))
	if certs, err := h.c.serverCertificates(h.ctx, h.c.localID); err != nil || len(certs) != 0 {
		t.Fatalf("apps.test server got %v, %v", certs, err)
	}
	h.c.setWildcard("apps.test", []byte("C"), []byte("K"), now().Add(-time.Second))
	if certs, _ := h.c.serverCertificates(h.ctx, h.c.localID); len(certs) != 0 {
		t.Fatal("an expired certificate went out")
	}
	h.c.setWildcard("apps.test", []byte("C"), []byte("K"), now().Add(time.Hour))
	if certs, _ := h.c.serverCertificates(h.ctx, h.c.localID); len(certs) != 1 || !slices.Equal(certs[0].Names, []string{"*.apps.test", "apps.test"}) {
		t.Fatalf("got %+v", certs)
	}
	// The control plane's ask endpoint issues nothing on demand under it.
	for host, want := range map[string]bool{"hooks.apps.test": false, "hooks.other.test": true} {
		h.c.cfg.Hooks.PublicHost = host
		if ok, err := h.c.AllowCertificate(h.ctx, host); err != nil || ok != want {
			t.Errorf("AllowCertificate(%s) = %v, %v; want %v", host, ok, err, want)
		}
	}
	for _, ing := range []serverIngress{
		{mode: apitypes.IngressCloudflareTunnel, baseDomain: "apps.test"},
		{mode: apitypes.IngressDirect, baseDomain: "apps.test", plainHTTP: true},
		{mode: apitypes.IngressDirect},
	} {
		if _, ok := wildcardBase(ing); ok {
			t.Errorf("%+v wants a wildcard", ing)
		}
	}
}
