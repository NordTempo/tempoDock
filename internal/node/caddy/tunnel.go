package caddy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// visitTimeout bounds one visit request.
const visitTimeout = 15 * time.Second

// cloudflareCode finds Cloudflare's error code in its error pages: "error
// code: 1033" in plain-text bodies, "Error 1033" in HTML ones.
var cloudflareCode = regexp.MustCompile(`[Ee]rror(?: code:)? ?(1\d{3})\b`)

// checkTunnel requests https://<hostname>/ the way a visitor would, through
// DNS, Cloudflare's edge and the tunnel, and looks for this server's marker
// header on the response. A match means DNS and the edge certificate both
// work, so DNSOK and CertificateOK are set together; anything else explains
// what answered instead. Redirects are not followed: they come from Caddy
// and carry the marker too.
func (p *Proxy) checkTunnel(ctx context.Context, hostname string) (node.DomainCheck, error) {
	var dc node.DomainCheck
	reqCtx, cancel := context.WithTimeout(ctx, visitTimeout)
	defer cancel()
	resp, err := p.visit(reqCtx, hostname)
	if err != nil {
		if ctx.Err() != nil {
			return dc, ctx.Err()
		}
		dc.Error = p.tunnelRequestError(hostname, err)
		return dc, nil
	}
	defer func() { _ = resp.Body.Close() }()

	marker := resp.Header.Get(MarkerHeader)
	if marker == p.opts.ServerMarker {
		dc.DNSOK, dc.CertificateOK = true, true
		return dc, nil
	}
	target := p.tunnelTarget()
	if marker != "" {
		dc.Error = fmt.Sprintf("%s is served by another Dock server; point it at %s", hostname, target)
		return dc, nil
	}
	if resp.Header.Get("CF-Ray") == "" && resp.Header.Get("Server") != "cloudflare" {
		dc.Error = fmt.Sprintf("%s does not go through Cloudflare; make it a proxied CNAME to %s", hostname, target)
		return dc, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if ctx.Err() != nil {
		return dc, ctx.Err()
	}
	var code string
	if m := cloudflareCode.FindSubmatch(body); m != nil {
		code = string(m[1])
	}
	switch {
	case code == "1033":
		dc.Error = fmt.Sprintf("Cloudflare can't reach a running tunnel for %s (error 1033); check that cloudflared is running on this server and that %s is a CNAME to %s", hostname, hostname, target)
	case code == "1016":
		dc.Error = fmt.Sprintf("Cloudflare can't resolve the origin of %s (error 1016); make it a CNAME to %s", hostname, target)
	case code != "":
		dc.Error = fmt.Sprintf("Cloudflare answered %s with error %s; make it a proxied CNAME to %s", hostname, code, target)
	case resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout:
		dc.Error = fmt.Sprintf("Cloudflare reached a tunnel for %s, but no web server answered (%s); check that Caddy is running and cloudflared forwards to http://127.0.0.1:%d",
			hostname, resp.Status, p.opts.HTTPPort)
	default:
		dc.Error = fmt.Sprintf("%s goes through Cloudflare but not to this server (%s without this server's %s header); point it at %s",
			hostname, resp.Status, MarkerHeader, target)
	}
	return dc, nil
}

// visit requests https://<hostname>/ the way a visitor would, through
// public DNS and whatever proxy sits in front, so CheckDomain can look for
// this server's marker header on the response. Redirects are not followed.
// The caller closes the body.
func (p *Proxy) visit(ctx context.Context, hostname string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+hostname+"/", nil)
	if err != nil {
		return nil, fmt.Errorf("%s is not a valid hostname: %w", hostname, err)
	}
	req.Header.Set("User-Agent", "dock-domain-check")
	client := &http.Client{
		Transport: &http.Transport{
			DialContext:     p.dialPublic,
			TLSClientConfig: &tls.Config{RootCAs: p.rootCAs},
			// Checks are rare; keep no idle connection around.
			DisableKeepAlives: true,
			ForceAttemptHTTP2: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return client.Do(req)
}

// tunnelRequestError explains why the request to hostname failed.
func (p *Proxy) tunnelRequestError(hostname string, err error) string {
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	var opErr *net.OpError
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return fmt.Sprintf("%s has no DNS records yet; add a proxied CNAME record pointing at %s", hostname, p.tunnelTarget())
	case errors.As(err, &certErr):
		return fmt.Sprintf("the certificate served for %s is not valid: %v", hostname, certErr.Err)
	case errors.As(err, &opErr) && opErr.Op == "remote error":
		// crypto/tls reports a TLS alert from the server this way, e.g. a
		// handshake failure when Cloudflare has no certificate for the name.
		return fmt.Sprintf("Cloudflare has no certificate for %s (%v); its free certificates cover one level under the zone, like web-staging.<zone>", hostname, err)
	default:
		return fmt.Sprintf("could not reach https://%s: %v", hostname, err)
	}
}

// SetTunnelHostname records the hostname of the tunnel the control plane
// manages for this server (ADR 0035), for CheckDomain's instructions.
func (p *Proxy) SetTunnelHostname(hostname string) {
	p.tunnel.Store(&hostname)
}

// tunnelTarget names the CNAME target for instructions.
func (p *Proxy) tunnelTarget() string {
	host := p.opts.TunnelHostname
	if t := p.tunnel.Load(); t != nil && *t != "" {
		host = *t
	}
	if host != "" {
		return "this server's tunnel, " + host
	}
	return "this server's Cloudflare tunnel"
}
