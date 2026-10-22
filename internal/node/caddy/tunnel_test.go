package caddy

import (
	"context"
	"crypto/x509"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCheckDomainTunnel runs tunnel-mode CheckDomain against a local TLS
// server that answers like Cloudflare would in each situation. The
// httptest certificate is valid for example.com.
func TestCheckDomainTunnel(t *testing.T) {
	var answer http.HandlerFunc
	cf := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { answer(w, r) }))
	cf.Config.ErrorLog = log.New(io.Discard, "", 0) // the invalid certificate case fails a handshake on purpose
	cf.StartTLS()
	t.Cleanup(cf.Close)
	trusted := x509.NewCertPool()
	trusted.AddCert(cf.Certificate())

	cloudflare := func(status int, marker, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("CF-Ray", "8a1b2c3d4e5f-OSL")
			w.Header().Set("Server", "cloudflare")
			if marker != "" {
				w.Header().Set(MarkerHeader, marker)
			}
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		}
	}
	for name, tc := range map[string]struct {
		answer    http.HandlerFunc
		dialErr   error
		untrusted bool
		ok        bool
		errHas    string
	}{
		"this server":            {answer: cloudflare(200, "srv-1", "hi"), ok: true},
		"this server, redirect":  {answer: cloudflare(308, "srv-1", ""), ok: true},
		"this server, no route":  {answer: cloudflare(404, "srv-1", notFoundPage), ok: true},
		"another dock server":    {answer: cloudflare(200, "srv-2", ""), errHas: "served by another Dock server"},
		"tunnel down":            {answer: cloudflare(530, "", "error code: 1033"), errHas: "cloudflared is running"},
		"origin dns error page":  {answer: cloudflare(530, "", "<title>Error 1016</title>"), errHas: "error 1016"},
		"caddy down":             {answer: cloudflare(502, "", "Bad Gateway"), errHas: "forwards to http://127.0.0.1:8080"},
		"some other site on cf":  {answer: cloudflare(200, "", "<html>"), errHas: "not to this server"},
		"not through cloudflare": {answer: func(http.ResponseWriter, *http.Request) {}, errHas: "does not go through Cloudflare"},
		"no dns records":         {dialErr: &net.DNSError{Err: "no such host", Name: "example.com", IsNotFound: true}, errHas: "no DNS records yet; add a proxied CNAME record pointing at this server's tunnel, abc.cfargotunnel.com"},
		"invalid certificate":    {answer: cloudflare(200, "srv-1", ""), untrusted: true, errHas: "certificate served for example.com is not valid"},
	} {
		t.Run(name, func(t *testing.T) {
			answer = tc.answer
			p, err := New(Options{AdminSocket: "/x", Ingress: IngressCloudflareTunnel, HTTPPort: 8080, ServerMarker: "srv-1", TunnelHostname: "abc.cfargotunnel.com"})
			if err != nil {
				t.Fatal(err)
			}
			p.rootCAs = trusted
			if tc.untrusted {
				p.rootCAs = x509.NewCertPool()
			}
			p.dialPublic = func(ctx context.Context, network, _ string) (net.Conn, error) {
				if tc.dialErr != nil {
					return nil, tc.dialErr
				}
				var d net.Dialer
				return d.DialContext(ctx, network, cf.Listener.Addr().String())
			}

			dc, err := p.CheckDomain(context.Background(), "example.com")
			if err != nil {
				t.Fatal(err)
			}
			if dc.DNSOK != tc.ok || dc.CertificateOK != tc.ok {
				t.Errorf("DNSOK=%v CertificateOK=%v, want both %v (%+v)", dc.DNSOK, dc.CertificateOK, tc.ok, dc)
			}
			if tc.errHas == "" && dc.Error != "" || !strings.Contains(dc.Error, tc.errHas) {
				t.Errorf("Error = %q, want it to contain %q", dc.Error, tc.errHas)
			}
		})
	}
}
