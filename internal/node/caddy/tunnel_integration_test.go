//go:build integration

package caddy

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// echoBackend answers with the forwarding headers it received, and tries
// to set its own marker header, which Caddy must override.
func echoBackend(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(MarkerHeader, "fake")
		_, _ = fmt.Fprintf(w, "xff=%s proto=%s cf=%s", r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Forwarded-Proto"), r.Header.Get("CF-Connecting-IP"))
	}))
	t.Cleanup(s.Close)
	return strings.TrimPrefix(s.URL, "http://")
}

// cloudflaredRequest is a request as cloudflared forwards it from the edge.
func cloudflaredRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("CF-Connecting-IP", "203.0.113.9")
	return req
}

func do(t *testing.T, c *http.Client, req *http.Request) (int, http.Header, string) {
	t.Helper()
	// Like testCaddy.get, retry transport errors for a few seconds: right
	// after a config load Caddy may still be closing the old listener (EOF,
	// reset). The requests here have no body, so they can be sent again.
	deadline := time.Now().Add(5 * time.Second)
	resp, err := c.Do(req)
	for err != nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		resp, err = c.Do(req)
	}
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

// nonLoopbackIP is an IPv4 address of this machine other than loopback.
func nonLoopbackIP(t *testing.T) string {
	t.Helper()
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			return n.IP.String()
		}
	}
	t.Skip("no non-loopback IPv4 address")
	return ""
}

func startTunnelCaddy(t *testing.T, mod func(*Options)) *testCaddy {
	t.Helper()
	return startCaddyWith(t, func(o *Options) {
		o.Ingress, o.ServerMarker, o.HTTPSPort, o.PublicIPs = IngressCloudflareTunnel, "srv-1", 0, nil
		if mod != nil {
			mod(o)
		}
	})
}

func TestIntegrationTunnel(t *testing.T) {
	tc := startTunnelCaddy(t, nil)
	err := tc.proxy.ApplyRoutes(context.Background(), []node.Route{
		{Hostnames: []string{"a.test"}, Upstream: echoBackend(t)},
		{Hostnames: []string{"www.a.test"}, RedirectTo: "a.test"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// cloudflared connects from loopback, so its forwarding headers are kept.
	code, h, body := do(t, tc.client, cloudflaredRequest(t, tc.url("http", "a.test", "/")))
	if want := "xff=203.0.113.9, 127.0.0.1 proto=https cf=203.0.113.9"; code != 200 || body != want {
		t.Errorf("a.test: got %d %q, want 200 %q", code, body, want)
	}
	if got := h.Values(MarkerHeader); len(got) != 1 || got[0] != "srv-1" {
		t.Errorf("%s = %v, want only srv-1", MarkerHeader, got)
	}

	// Visitors are on Cloudflare's HTTPS edge: redirect there, not to Caddy's port.
	code, h, _ = do(t, tc.client, cloudflaredRequest(t, tc.url("http", "www.a.test", "/q")))
	if code != 308 || h.Get("Location") != "https://a.test/q" || h.Get(MarkerHeader) != "srv-1" {
		t.Errorf("www redirect: got %d %q marker %q", code, h.Get("Location"), h.Get(MarkerHeader))
	}
	code, h, _ = do(t, tc.client, cloudflaredRequest(t, tc.url("http", "unknown.test", "/")))
	if code != 404 || h.Get(MarkerHeader) != "srv-1" {
		t.Errorf("unknown host: got %d marker %q", code, h.Get(MarkerHeader))
	}

	// Nothing listens beyond loopback.
	if c, err := net.DialTimeout("tcp", net.JoinHostPort(nonLoopbackIP(t), strconv.Itoa(tc.opts.HTTPPort)), time.Second); err == nil {
		_ = c.Close()
		t.Error("tunnel-mode Caddy accepted a connection on a non-loopback address")
	}
}

// TestIntegrationTunnelUntrustedPeer binds Caddy beyond loopback (tests
// only) to show forwarding headers from any other peer are replaced.
func TestIntegrationTunnelUntrustedPeer(t *testing.T) {
	ip := nonLoopbackIP(t)
	tc := startTunnelCaddy(t, func(o *Options) { o.tunnelListenHost = "0.0.0.0" })
	if err := tc.proxy.ApplyRoutes(context.Background(), []node.Route{{Hostnames: []string{"a.test"}, Upstream: echoBackend(t)}}); err != nil {
		t.Fatal(err)
	}
	var d net.Dialer
	c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, net.JoinHostPort(ip, strconv.Itoa(tc.opts.HTTPPort)))
		},
	}}
	_, _, body := do(t, c, cloudflaredRequest(t, tc.url("http", "a.test", "/")))
	if !strings.HasPrefix(body, "xff="+ip+" proto=http ") {
		t.Errorf("untrusted peer: upstream saw %q, want its own address and proto=http", body)
	}
}

// TestIntegrationTunnelCheckDomain runs CheckDomain through a fake
// Cloudflare edge: a TLS reverse proxy to the tunnel-mode Caddy, as
// cloudflared would forward. The httptest certificate covers example.com.
func TestIntegrationTunnelCheckDomain(t *testing.T) {
	tc := startTunnelCaddy(t, nil)
	if err := tc.proxy.ApplyRoutes(context.Background(), []node.Route{{Hostnames: []string{"example.com"}, Upstream: backend(t, "E")}}); err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse(tc.url("http", "127.0.0.1", ""))
	edge := httptest.NewTLSServer(&httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(origin)
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
			pr.Out.Header.Set("CF-Connecting-IP", "203.0.113.9")
		},
		ModifyResponse: func(r *http.Response) error {
			r.Header.Set("CF-Ray", "8a1b2c3d4e5f-OSL")
			return nil
		},
	})
	t.Cleanup(edge.Close)
	roots := x509.NewCertPool()
	roots.AddCert(edge.Certificate())
	viaEdge := func(p *Proxy) {
		p.rootCAs = roots
		p.dialPublic = func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, edge.Listener.Addr().String())
		}
	}

	viaEdge(tc.proxy)
	dc, err := tc.proxy.CheckDomain(context.Background(), "example.com")
	if err != nil || !dc.DNSOK || !dc.CertificateOK || dc.Error != "" {
		t.Errorf("CheckDomain on this server = %+v, %v", dc, err)
	}

	// Another server's check of the same hostname sees a foreign marker.
	opts := tc.opts
	opts.ServerMarker = "srv-2"
	other, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	viaEdge(other)
	dc, err = other.CheckDomain(context.Background(), "example.com")
	if err != nil || dc.DNSOK || dc.CertificateOK || !strings.Contains(dc.Error, "another Dock server") {
		t.Errorf("CheckDomain on another server = %+v, %v", dc, err)
	}
}

// TestIntegrationAskFromLastRoutes has Caddy ask the Proxy's AskHandler
// before issuing certificates, also after a node restart, when a fresh
// Proxy reads the routes from Caddy's running config.
func TestIntegrationAskFromLastRoutes(t *testing.T) {
	var current atomic.Pointer[Proxy]
	ask := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current.Load().AskHandler().ServeHTTP(w, r)
	}))
	t.Cleanup(ask.Close)
	tc := startCaddy(t, ask.URL+"/internal/caddy/ask", false)
	current.Store(tc.proxy)

	err := tc.proxy.ApplyRoutes(context.Background(), []node.Route{
		{Hostnames: []string{"ok.test"}, Upstream: backend(t, "OK")},
		{Hostnames: []string{"later.test"}, Upstream: backend(t, "L")},
	})
	if err != nil {
		t.Fatal(err)
	}
	tc.trustInternalCA(t)

	if code, _, body := tc.get(t, tc.url("https", "ok.test", "/")); code != 200 || body != "OK" {
		t.Errorf("ok.test: got %d %q", code, body)
	}
	if resp, err := tc.client.Get(tc.url("https", "unrouted.test", "/")); err == nil {
		_ = resp.Body.Close()
		t.Error("unrouted.test: TLS handshake succeeded, want no certificate")
	}

	fresh, err := New(tc.opts)
	if err != nil {
		t.Fatal(err)
	}
	current.Store(fresh)
	if code, _, body := tc.get(t, tc.url("https", "later.test", "/")); code != 200 || body != "L" {
		t.Errorf("later.test after restart: got %d %q", code, body)
	}
}
