// Package caddy implements node.Proxy on top of a Caddy process.
//
// Caddy runs as its own systemd service. The node is the only thing that
// configures it: it renders the complete config from the routing table and
// POSTs it to Caddy's admin API on a Unix socket. Caddy validates the config
// and swaps it in atomically, or rejects it and keeps the old one.
//
// The ingress mode decides how public traffic arrives (ADR 0013):
//
//   - direct: Caddy listens on ports 80 and 443 of the public IP and issues
//     certificates on demand, the first time a hostname is requested over
//     HTTPS, and only after the `ask` endpoint approves it. On a node that
//     endpoint is AskHandler, which answers from the last applied routes.
//   - cloudflare_tunnel: cloudflared dials out to Cloudflare and forwards to
//     Caddy on 127.0.0.1 in plain HTTP. Cloudflare's edge terminates TLS,
//     and Caddy trusts forwarding headers from loopback only.
//
// Every response carries a Dock-Server header with the server's marker, so
// CheckDomain on a tunnel server can tell that a public request reached it.
//
// Reloads are close to, but not exactly, lossless: a request on a brand-new
// connection that races a reload can be dropped, and an HTTP/2 connection
// can keep using the old routes for a while. See
// TestIntegrationSwapUnderTraffic for details. Batch route changes rather
// than reloading in a tight loop.
package caddy

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Ingress is how public traffic reaches a server's Caddy (ADR 0013).
type Ingress string

const (
	// IngressDirect serves the public IP on HTTPPort and HTTPSPort with
	// on-demand certificates. The zero Ingress means direct.
	IngressDirect Ingress = "direct"
	// IngressCloudflareTunnel serves plain HTTP on 127.0.0.1:HTTPPort to the
	// local cloudflared, which carries traffic from Cloudflare's edge.
	IngressCloudflareTunnel Ingress = "cloudflare_tunnel"
)

// MarkerHeader is the response header carrying Options.ServerMarker.
const MarkerHeader = "Dock-Server"

// Options configure the rendered Caddy config and how to reach Caddy.
type Options struct {
	// AdminSocket is the path of Caddy's admin Unix socket.
	AdminSocket string
	// Ingress is direct (the default) or cloudflare_tunnel.
	Ingress Ingress
	// HTTPPort is the plain HTTP port. In tunnel mode it is the only port,
	// bound on 127.0.0.1, and cloudflared forwards to
	// http://127.0.0.1:<HTTPPort>.
	HTTPPort int
	// HTTPSPort is the TLS port. Direct mode only.
	HTTPSPort int
	// TunnelPort, in direct mode, adds a plain-HTTP listener on
	// 127.0.0.1:<TunnelPort> for a Cloudflare tunnel the control plane
	// runs on this server too (ADR 0036): every route, forwarding headers
	// trusted from loopback, and routes with TunnelOnly only there. It is
	// rendered while some route is TunnelOnly; 0 leaves it out. Tunnel
	// mode uses HTTPPort.
	TunnelPort int
	// ACMEEmail is given to the certificate authority. Optional.
	ACMEEmail string
	// DisableTLS serves every route over plain HTTP on HTTPPort. Direct mode
	// only (development); tunnel mode is always plain HTTP.
	DisableTLS bool
	// AskURL is the endpoint Caddy asks before issuing a certificate, e.g.
	// http://127.0.0.1:7071/internal/caddy/ask. Required in direct mode
	// unless DisableTLS is set.
	AskURL string
	// AccessURL is the access check (AccessHandler) Caddy asks on every
	// request to a route that needs a Dock login, e.g.
	// http://127.0.0.1:7071/internal/caddy/access. Required for such
	// routes.
	AccessURL string
	// WakeURL is the wake check Caddy asks before proxying a request to
	// an asleep environment's route (Route.Wake), e.g.
	// http://127.0.0.1:7071/internal/caddy/wake. Required for such
	// routes.
	WakeURL string
	// TrafficAddr is the loopback host:port the node's traffic counter
	// listens on (traffic.Listen). When set, Caddy sends it the access log
	// of routes with Traffic set (ADR 0033); empty turns counting off.
	TrafficAddr string
	// AnalyticsAddr is the loopback host:port of the node's analytics
	// ingest (ADR 0037). Routes' Analytics hostnames send /_dock/a/* there;
	// empty leaves them out.
	AnalyticsAddr string
	// TraceIngestAddr is the loopback host:port of the node's trace ingest
	// (OTLP/HTTP), which a route for the dock-otlp hostname forwards apps'
	// exports to, from private addresses only; empty is none. Direct
	// servers only: in tunnel mode Caddy listens on loopback, which
	// containers can't reach.
	TraceIngestAddr string
	// PublicIPs are the addresses DNS records should point at, used by
	// CheckDomain in direct mode.
	PublicIPs []string
	// ServerMarker is a token unique to this server (not a secret), sent on
	// every response as the Dock-Server header. Letters, digits, '.', '_'
	// and '-', at most 128 characters. Required in tunnel mode, where
	// CheckDomain looks for it; optional in direct mode.
	ServerMarker string
	// TunnelHostname is the CNAME target of the server's tunnel,
	// <tunnel-id>.cfargotunnel.com. Optional; tunnel-mode CheckDomain names
	// it in its instructions.
	TunnelHostname string
	// UpstreamsDir, when set, keeps keyed routes' upstreams (Route.Key)
	// out of the config, in files there that Caddy reads per request
	// (ADR 0091, upstreams.go), so a switch changes a file and no config.
	// Caddy must be able to read it; the daemon checks dock can write it
	// (CheckUpstreamsDir). Empty puts every upstream in the config.
	UpstreamsDir string

	// accessCheck is AccessURL taken apart, wakeCheck WakeURL; set by
	// validate.
	accessCheck accessCheck
	wakeCheck   accessCheck

	// internalCA makes Caddy issue certificates from its own local CA
	// instead of ACME. Tests only, so they never contact a real CA.
	internalCA bool
	// gracePeriod overrides defaultGracePeriod. Tests only.
	gracePeriod time.Duration
	// tunnelListenHost, when set, replaces the tunnel-mode listen host
	// (127.0.0.1). Tests only, to reach Caddy from a non-loopback address.
	tunnelListenHost string
}

// tunnel reports whether the server is in cloudflare_tunnel mode.
func (o Options) tunnel() bool { return o.Ingress == IngressCloudflareTunnel }

// plainHTTP reports whether Caddy serves plain HTTP only, with no
// certificates of its own.
func (o Options) plainHTTP() bool { return o.DisableTLS || o.tunnel() }

// accessCheck is where Caddy reaches the access check (or the wake
// check): a host:port to dial and the path to ask.
type accessCheck struct{ dial, path string }

// validate checks the options and sets accessCheck from AccessURL and
// wakeCheck from WakeURL.
func (o *Options) validate() error {
	for _, c := range []struct {
		url, example string
		to           *accessCheck
	}{{o.AccessURL, "access", &o.accessCheck}, {o.WakeURL, "wake", &o.wakeCheck}} {
		if c.url == "" {
			continue
		}
		u, err := url.Parse(c.url)
		if err != nil || u.Scheme != "http" || u.Host == "" || !strings.HasPrefix(u.Path, "/") || u.RawQuery != "" {
			return fmt.Errorf("caddy: %s URL %q must look like http://127.0.0.1:7071/internal/caddy/%s", c.example, c.url, c.example)
		}
		*c.to = accessCheck{dial: u.Host, path: u.Path}
	}
	switch {
	case o.AdminSocket == "":
		return errors.New("caddy: admin socket path is required")
	case o.Ingress != "" && o.Ingress != IngressDirect && !o.tunnel():
		return fmt.Errorf("caddy: unknown ingress %q; use direct or cloudflare_tunnel", o.Ingress)
	case o.HTTPPort <= 0:
		return errors.New("caddy: an HTTP port is required")
	case o.TrafficAddr != "" && !validTrafficAddr(o.TrafficAddr):
		return fmt.Errorf("caddy: traffic address %q must be a loopback host:port like 127.0.0.1:7074", o.TrafficAddr)
	case o.TraceIngestAddr != "" && !validTrafficAddr(o.TraceIngestAddr):
		return fmt.Errorf("caddy: trace ingest address %q must be a loopback host:port like 127.0.0.1:4318", o.TraceIngestAddr)
	case o.AnalyticsAddr != "" && !validTrafficAddr(o.AnalyticsAddr):
		return fmt.Errorf("caddy: analytics address %q must be a loopback host:port like 127.0.0.1:7077", o.AnalyticsAddr)
	case o.ServerMarker != "" && !validMarker(o.ServerMarker):
		return fmt.Errorf("caddy: server marker %q may only use letters, digits, '.', '_' and '-' (at most 128)", o.ServerMarker)
	case o.UpstreamsDir != "" && !validUpstreamsDir(o.UpstreamsDir):
		return fmt.Errorf("caddy: upstreams directory %q must be an absolute path of letters, digits, '/', '.', '_' and '-'", o.UpstreamsDir)
	case o.tunnel() && o.ServerMarker == "":
		return errors.New("caddy: a server marker is required in cloudflare_tunnel mode")
	case o.tunnel():
		return nil
	case o.HTTPSPort <= 0:
		return errors.New("caddy: an HTTPS port is required in direct mode")
	case !o.DisableTLS && o.AskURL == "":
		return errors.New("caddy: an ask URL is required when TLS is enabled")
	}
	return nil
}

// validTrafficAddr accepts a loopback IP and port: access log lines carry
// client addresses and must not leave the server.
func validTrafficAddr(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	ip := net.ParseIP(host)
	n, perr := strconv.Atoi(port)
	return err == nil && ip != nil && ip.IsLoopback() && perr == nil && n > 0 && n < 1<<16
}

// validMarker keeps the marker a plain header value: no spaces, and no "{"
// that Caddy would expand as a placeholder.
func validMarker(m string) bool {
	if len(m) > 128 {
		return false
	}
	for _, c := range m {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// Proxy drives one Caddy process through its admin socket.
type Proxy struct {
	opts   Options
	client *http.Client

	// hosts are the hostnames of the last applied routes and the names
	// its loaded certificates cover, for AskHandler. nil until the first
	// ApplyRoutes or read of Caddy's running config.
	hosts  atomic.Pointer[askSet]
	seedMu sync.Mutex // one read of Caddy's running config at a time
	// tunnel is the tunnel hostname the control plane runs cloudflared
	// with (SetTunnelHostname); it wins over Options.TunnelHostname.
	tunnel atomic.Pointer[string]
	// wake holds the asleep environments of the last applied routes, for
	// a node's wake check (NodeWaker).
	wake wakeState
	// traced maps the hostnames of the last applied traced routes to
	// their trace tokens (TraceToken).
	traced atomic.Pointer[map[string]string]

	// applyMu makes route applies one at a time: each orders its writes
	// to the upstreams directory around its load (upstreams.go).
	applyMu sync.Mutex
	// files is what the upstreams directory holds; nil until the first
	// apply, which also sweeps files no route reads.
	files upstreamFiles
	// dials maps the upstream addresses of the last applied keyed routes
	// to their dial placeholders, which is what Caddy's upstream list
	// shows for them (OpenRequests).
	dials atomic.Pointer[map[string]string]

	// Overridable in tests.
	lookupIP   func(ctx context.Context, host string) ([]net.IPAddr, error)
	rootCAs    *x509.CertPool // nil means the system roots
	dialPublic func(ctx context.Context, network, addr string) (net.Conn, error)
}

var _ node.Proxy = (*Proxy)(nil)

// New returns a Proxy for the Caddy listening on opts.AdminSocket. It does
// not contact Caddy.
func New(opts Options) (*Proxy, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	var d net.Dialer
	return &Proxy{
		opts: opts,
		client: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return d.DialContext(ctx, "unix", opts.AdminSocket)
				},
				// Every /load replaces Caddy's admin server, so a kept-alive
				// connection belongs to a server that is shutting down and
				// may drop the next request on it. A Unix dial is cheap.
				DisableKeepAlives: true,
			},
			// A load waits for the new config to start and the old one to
			// stop, which can take a moment; never hang forever.
			Timeout: time.Minute,
		},
		lookupIP:   net.DefaultResolver.LookupIPAddr,
		dialPublic: d.DialContext,
	}, nil
}

// ApplyRoutes renders the full config for routes and certs and loads it
// into Caddy. If Caddy rejects it, the previous config stays in effect.
// Certificates are left out where Caddy serves plain HTTP.
//
// Keyed routes' upstream files (ADR 0091) are written around the load: new
// ones before it, changed ones after it, so a switch that only changes a
// file loads the running config again, which Caddy answers without a
// reload, and then moves traffic with the rename.
func (p *Proxy) ApplyRoutes(ctx context.Context, routes []node.Route, certs []node.Certificate) error {
	if p.opts.plainHTTP() {
		certs = nil
	}
	cfg, err := Render(p.opts, routes, certs)
	if err != nil {
		return err
	}
	files, err := keyedFiles(p.opts, routes)
	if err != nil {
		return err
	}
	p.applyMu.Lock()
	defer p.applyMu.Unlock()
	sweep := p.files == nil
	if sweep {
		p.files = upstreamFiles{}
	}
	if err := p.files.before(files); err != nil {
		return err
	}
	if err := p.load(ctx, cfg); err != nil {
		return err
	}
	// The config runs either way: a file that couldn't change keeps its
	// old upstream until the next apply, which tries again.
	filesErr := p.files.after(p.opts.UpstreamsDir, files, sweep)
	dials := map[string]string{}
	for _, r := range routes {
		if keyed(p.opts, r) {
			dials[r.Upstream] = keyedDial(p.opts.UpstreamsDir, r.Key)
		}
	}
	p.dials.Store(&dials)
	p.hosts.Store(&askSet{hosts: *routeHosts(routes), covered: certNames(certs)})
	p.setWakeKeys(routes)
	traced := map[string]string{}
	for _, r := range routes {
		if r.Trace != "" && r.RedirectTo == "" && r.Stopped == "" {
			for _, h := range r.Hostnames {
				traced[strings.ToLower(h)] = r.Trace
			}
		}
	}
	p.traced.Store(&traced)
	return filesErr
}

// TraceToken is the trace token of the traced route serving host, or ""
// when the last applied routes don't trace it: the trace ingest's edge
// spans are attributed with it (ADR 0075).
func (p *Proxy) TraceToken(host string) string {
	if m := p.traced.Load(); m != nil {
		return (*m)[host]
	}
	return ""
}

// load POSTs cfg to Caddy's /load endpoint, which validates it and swaps
// it in atomically.
//
// A load whose connection dropped may or may not have been applied; admin
// sends it again, and Caddy answers a config identical to the running one
// with 200 without reloading. So a 200 on any attempt means cfg is running.
func (p *Proxy) load(ctx context.Context, cfg []byte) error {
	resp, err := p.admin(ctx, http.MethodPost, "/load", cfg)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return fmt.Errorf("caddy rejected the config: %s", e.Error)
	}
	return fmt.Errorf("caddy rejected the config: %s: %s", resp.Status, bytes.TrimSpace(body))
}

// adminRetryWaits are the pauses before each resend of an admin request
// that failed in transport: four resends over 1.5 s. Every /load replaces
// Caddy's admin server, and a connection the old server accepted just
// before shutting down is closed unanswered (EOF); a restarting Caddy
// refuses connections for a moment. Both clear well within that.
var adminRetryWaits = []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond}

// admin sends a request to Caddy's admin socket (the URL host is ignored)
// and returns Caddy's response, whatever its status. Callers only send
// idempotent requests (GETs, and POST /load, which replaces the whole
// config), so it resends on transport errors, but never on an HTTP
// response: a 4xx or 5xx is Caddy's answer. A cancelled or timed-out
// request is not resent either.
func (p *Proxy) admin(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, "http://caddy"+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := p.client.Do(req)
		if err == nil {
			return resp, nil
		}
		ne, isNet := errors.AsType[net.Error](err)
		if attempt == len(adminRetryWaits) || errors.Is(err, context.Canceled) || isNet && ne.Timeout() {
			return nil, fmt.Errorf("caddy: reach admin socket %s: %w", p.opts.AdminSocket, err)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("caddy: reach admin socket %s: %w", p.opts.AdminSocket, err)
		case <-time.After(adminRetryWaits[attempt]):
		}
	}
}

// OpenRequests implements node.Proxy: the requests Caddy proxies to
// upstreams right now, from its admin API's upstream list
// (num_requests counts a websocket or a stream until it closes). An
// upstream no route names has no entry and counts 0. A keyed route's
// address counts all its route's requests, whatever its file held when
// they were sent (ADR 0091).
func (p *Proxy) OpenRequests(ctx context.Context, upstreams []string) (int, error) {
	resp, err := p.admin(ctx, http.MethodGet, "/reverse_proxy/upstreams", nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("caddy: read upstreams: %s", resp.Status)
	}
	var list []struct {
		Address     string `json:"address"`
		NumRequests int    `json:"num_requests"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&list); err != nil {
		return 0, fmt.Errorf("caddy: read upstreams: %w", err)
	}
	// A keyed route's requests are listed under its dial placeholder.
	if d := p.dials.Load(); d != nil {
		upstreams = slices.Clone(upstreams)
		for _, u := range upstreams {
			if dial, ok := (*d)[u]; ok {
				upstreams = append(upstreams, dial)
			}
		}
	}
	n := 0
	for _, u := range list {
		if slices.Contains(upstreams, u.Address) {
			n += u.NumRequests
		}
	}
	return n, nil
}

// Forward implements node.Proxy: a plain loopback listener, which Caddy
// on this host dials as a route's upstream.
func (p *Proxy) Forward(ctx context.Context, port int) (net.Listener, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("forward port %d: %w", port, err)
	}
	return ln, nil
}
