//go:build integration

package caddy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// TestIntegrationWildcard loads a base domain's wildcard (ADR 0090) into
// a real Caddy: names under the base get it with no on-demand issuance,
// routed or not (an unknown one gets the 404 page, so the handshake
// doesn't tell them apart), it wins over an on-demand certificate Caddy
// already holds for the exact name, and other hostnames still issue on
// demand.
func TestIntegrationWildcard(t *testing.T) {
	ask := newAskServer(t, "old.apps.test", "custom.test", "web.apps.test")
	tc := startCaddy(t, ask.url, false)
	tc.proxy.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}}, nil
	}

	// Before the wildcard: old.apps.test has a certificate of its own.
	tc.applyRoutes(t, []node.Route{{Hostnames: []string{"old.apps.test"}, Upstream: backend(t, "OLD")}})
	tc.trustInternalCA(t)
	if code, _, body := tc.get(t, tc.url("https", "old.apps.test", "/")); code != 200 || body != "OLD" {
		t.Fatalf("old.apps.test before the wildcard: %d %q", code, body)
	}

	ca, wild := testWildcard(t, "apps.test")
	pool := tc.client.Transport.(*http.Transport).TLSClientConfig.RootCAs
	pool.AddCert(ca)
	tc.proxy.rootCAs = pool
	routes := []node.Route{
		{Hostnames: []string{"old.apps.test"}, Upstream: backend(t, "OLD")},
		{Hostnames: []string{"web.apps.test"}, Upstream: backend(t, "WEB")},
		{Hostnames: []string{"custom.test"}, Upstream: backend(t, "CUSTOM")},
	}
	if err := tc.proxy.ApplyRoutes(t.Context(), routes, []node.Certificate{wild}); err != nil {
		t.Fatal(err)
	}
	before := len(ask.questions())

	for host, want := range map[string]string{"web.apps.test": "WEB", "old.apps.test": "OLD", "apps.test": "Nothing is deployed", "nope.apps.test": "Nothing is deployed"} {
		code, issuer, body := tc.getTLS(t, host)
		if !strings.Contains(body, want) || issuer != ca.Subject.CommonName {
			t.Errorf("%s: %d %q from %q, want %q with the wildcard", host, code, body, issuer, want)
		}
	}
	if q := ask.questions()[before:]; len(q) != 0 {
		t.Errorf("names under the base were asked about: %v", q)
	}
	if code, issuer, body := tc.getTLS(t, "custom.test"); code != 200 || body != "CUSTOM" || issuer == ca.Subject.CommonName {
		t.Errorf("custom.test: %d %q from %q, want its own certificate", code, body, issuer)
	}
	if !slices.Contains(ask.questions(), "custom.test") {
		t.Error("custom.test wasn't asked about")
	}

	dc, err := tc.proxy.CheckDomain(t.Context(), "web.apps.test")
	if err != nil || !dc.CertificateOK {
		t.Errorf("CheckDomain(web.apps.test) = %+v, %v", dc, err)
	}

	// The node's own ask endpoint refuses what the wildcard covers, also
	// when it reads Caddy's running config after a restart.
	fresh, err := New(tc.opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []*Proxy{tc.proxy, fresh} {
		for host, want := range map[string]int{"web.apps.test": 404, "custom.test": 200} {
			rec := httptest.NewRecorder()
			p.AskHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ask?domain="+host, nil))
			if rec.Code != want {
				t.Errorf("ask %s: %d, want %d", host, rec.Code, want)
			}
		}
	}

	// Without it again, names under the base go back to on demand.
	tc.applyRoutes(t, routes)
	if code, issuer, _ := tc.getTLS(t, "web.apps.test"); code != 200 || issuer == ca.Subject.CommonName {
		t.Errorf("web.apps.test without the wildcard: %d from %q", code, issuer)
	}
}

// getTLS GETs https://host/ over a new connection and returns the
// status, the served certificate's issuer and the body. A pooled one
// would keep the certificate of its own handshake: right after a load,
// the previous server answers on it during its shutdown grace period.
func (tc *testCaddy) getTLS(t *testing.T, host string) (int, string, string) {
	t.Helper()
	tc.client.CloseIdleConnections()
	deadline := time.Now().Add(5 * time.Second)
	resp, err := tc.client.Get(tc.url("https", host, "/"))
	for err != nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		resp, err = tc.client.Get(tc.url("https", host, "/"))
	}
	if err != nil {
		t.Fatalf("GET https://%s: %v", host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.TLS.PeerCertificates[0].Issuer.CommonName, string(b)
}

// testWildcard makes a CA and a certificate for *.<base> and <base> it
// signed.
func testWildcard(t *testing.T, base string) (*x509.Certificate, node.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Wildcard Test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	names := []string{"*." + base, base}
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2), DNSNames: names,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature,
	}, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	c := node.Certificate{
		Names:    names,
		ChainPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		KeyPEM:   pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}
	if _, err := tls.X509KeyPair(c.ChainPEM, c.KeyPEM); err != nil {
		t.Fatal(err)
	}
	return ca, c
}
