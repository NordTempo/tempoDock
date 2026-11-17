// Package uptime runs uptime probes (ADR 0055): one GET to a site's URL,
// judged by its status. node.Local uses it on the control plane's own
// server and the remote Node on every other one.
package uptime

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"syscall"
	"time"

	"github.com/NordTempo/tempoDock/internal/httpkit"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/version"
)

// Prober is node.Prober over HTTP. It holds one client for every probe,
// which keeps no connections: each probe dials anew, as a visitor would.
type Prober struct {
	client *http.Client
}

var _ node.Prober = (*Prober)(nil)

// New returns a Prober. allowLoopback lets probes reach this server's own
// addresses, which they never may in production: for tests whose sites
// listen on 127.0.0.1.
func New(allowLoopback bool) *Prober {
	control := func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return err
		}
		if why := refused(ap.Addr().Unmap(), ap.Port(), allowLoopback); why != "" {
			return &refusedError{why: why}
		}
		return nil
	}
	return &Prober{client: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			// Straight to the site: a proxy would answer for it.
			Proxy:             nil,
			DialContext:       (&net.Dialer{Control: control}).DialContext,
			DisableKeepAlives: true,
			// The site's own certificate is part of being up.
			TLSClientConfig:    &tls.Config{MinVersion: tls.VersionTLS12},
			ForceAttemptHTTP2:  true,
			DisableCompression: true,
		},
	}}
}

// refused says why a probe may not connect to ip:port, or "". Probes
// reach public sites as a visitor would, so only public unicast
// addresses on 80 and 443 are dialed: never this server's loopback,
// link-local or metadata addresses, private (RFC 1918, ULA), CGNAT or
// tailnet addresses, which hold Docker's bridges, other apps' containers
// and LANs that a domain's DNS could point at. This server's own public
// address stays allowed: on 80 and 443 it is Caddy, what a visitor
// reaches, and the control plane probes its own sites when it is the
// only server (ADR 0055). allowLoopback (tests) admits loopback on any
// port.
func refused(ip netip.Addr, port uint16, allowLoopback bool) string {
	switch {
	case allowLoopback && ip.IsLoopback():
		return ""
	case !httpkit.IsPublicAddr(ip):
		return "the domain resolves to an address that isn't public"
	case port != 80 && port != 443:
		return "only ports 80 and 443 are probed"
	}
	return ""
}

// refusedError is a dial the address checks stopped. Its text never
// names the address: what a probe reports goes back to the check's
// owner, who must not learn what lives on private networks.
type refusedError struct{ why string }

func (e *refusedError) Error() string { return e.why }

// Probe implements node.Prober.
func (p *Prober) Probe(ctx context.Context, req node.ProbeRequest) (node.ProbeResult, error) {
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return node.ProbeResult{}, fmt.Errorf("probe %q: not an http or https URL", req.URL)
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return node.ProbeResult{}, fmt.Errorf("probe %q: %w", req.URL, err)
	}
	hr.Header.Set("User-Agent", "dock-uptime/"+version.Version)
	if req.Cookie != "" {
		// Safe on this one request: the client follows no redirects.
		hr.Header.Set("Cookie", req.Cookie)
	}
	start := time.Now()
	resp, err := p.client.Do(hr)
	res := node.ProbeResult{Latency: time.Since(start)}
	if err != nil {
		res.Error = describe(err, timeout)
		return res, nil
	}
	// Only the status matters: the body is never read.
	_ = resp.Body.Close()
	res.Status = resp.StatusCode
	res.Up = node.StatusMatches(req.ExpectStatus, resp.StatusCode)
	if !res.Up {
		res.Error = "answered " + strconv.Itoa(resp.StatusCode) + ", expected " + expected(req.ExpectStatus)
	}
	return res, nil
}

// expected names the status a probe wanted.
func expected(expect int) string {
	switch expect {
	case 0:
		return "200"
	case node.ExpectClass2xx, node.ExpectClass3xx:
		return strconv.Itoa(expect) + "xx"
	}
	return strconv.Itoa(expect)
}

// describe says in plain, fixed words why a request got no answer
// (httpkit.NetErrorText). It never passes on raw dial or TLS errors:
// those can name addresses and certificate names of whatever answered
// (the probe's result goes back to the check's owner).
func describe(err error, timeout time.Duration) string {
	if r, ok := errors.AsType[*refusedError](err); ok {
		return r.Error()
	}
	if text := httpkit.NetErrorText(err, timeout); text != "" {
		return text
	}
	return "request failed"
}
