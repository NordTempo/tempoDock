// Package uptime runs uptime probes (ADR 0052): one GET to a site's URL,
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
		ip := ap.Addr().Unmap()
		switch {
		case httpkit.IsSelfAddr(ip) && !allowLoopback:
			return &refusedError{ip: ip, why: "this server itself"}
		case httpkit.IsInternalAddr(ip):
			return &refusedError{ip: ip, why: "a link-local, multicast or cloud metadata address"}
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

// refusedError is a dial the address checks stopped.
type refusedError struct {
	ip  netip.Addr
	why string
}

func (e *refusedError) Error() string {
	return fmt.Sprintf("%s is %s; uptime checks don't probe it", e.ip, e.why)
}

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

// describe says in plain words why a request got no answer.
func describe(err error, timeout time.Duration) string {
	if r, ok := errors.AsType[*refusedError](err); ok {
		return r.Error()
	}
	if d, ok := errors.AsType[*net.DNSError](err); ok {
		if d.IsNotFound {
			return "DNS: " + d.Name + " not found"
		}
		return "DNS: " + d.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "no answer within " + timeout.String()
	}
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return "no answer within " + timeout.String()
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "connection refused"
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return "connection reset"
	}
	if v, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return "TLS: " + v.Err.Error()
	}
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return ue.Err.Error()
	}
	return err.Error()
}
