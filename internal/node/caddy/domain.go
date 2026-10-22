package caddy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// certCheckTimeout bounds the TLS handshake in CheckDomain. The first
// handshake for a hostname triggers issuance, which can take several seconds.
const certCheckTimeout = 30 * time.Second

// CheckDomain reports whether hostname reaches this server and has a valid
// certificate.
//
// In direct mode it resolves hostname and compares it with the server's
// public IPs. When TLS is enabled and DNS points here, it also handshakes
// with the local Caddy using hostname as SNI and verifies the certificate
// against the system roots. That handshake is what triggers on-demand
// issuance, so it is skipped while DNS is wrong to avoid burning CA rate
// limits.
//
// In tunnel mode it requests https://<hostname>/ through Cloudflare; see
// checkTunnel.
//
// Problems are reported in DomainCheck.Error; the returned error is only
// set when ctx ends.
func (p *Proxy) CheckDomain(ctx context.Context, hostname string) (node.DomainCheck, error) {
	if p.opts.tunnel() {
		return p.checkTunnel(ctx, hostname)
	}
	dc := node.DomainCheck{ExpectedIPs: p.opts.PublicIPs}

	addrs, err := p.lookupIP(ctx, hostname)
	if ctx.Err() != nil {
		return dc, ctx.Err()
	}
	if err != nil {
		dc.Error = dnsError(hostname, err)
		return dc, nil
	}

	expected := map[netip.Addr]bool{}
	for _, s := range p.opts.PublicIPs {
		if a, err := netip.ParseAddr(s); err == nil {
			expected[a.Unmap()] = true
		}
	}
	var stray []string
	for _, a := range addrs {
		ip, ok := netip.AddrFromSlice(a.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		dc.ResolvedIPs = append(dc.ResolvedIPs, ip.String())
		if expected[ip] {
			dc.DNSOK = true
		} else {
			stray = append(stray, ip.String())
		}
	}

	switch {
	case len(dc.ResolvedIPs) == 0:
		dc.Error = noRecords(hostname)
		return dc, nil
	case len(expected) == 0:
		dc.Error = "this server has no public IP configured, so DNS can't be checked"
		return dc, nil
	case !dc.DNSOK:
		dc.Error = fmt.Sprintf("%s points at %s, but should point at %s",
			hostname, strings.Join(dc.ResolvedIPs, ", "), strings.Join(p.opts.PublicIPs, ", "))
		return dc, nil
	case len(stray) > 0:
		// Still DNSOK, but a CA may validate against the wrong address.
		dc.Error = fmt.Sprintf("%s also points at %s, which is not this server; remove that record",
			hostname, strings.Join(stray, ", "))
	}

	if p.opts.DisableTLS {
		return dc, nil
	}
	if err := p.checkCertificate(ctx, hostname); err != nil {
		if ctx.Err() != nil {
			return dc, ctx.Err()
		}
		if errors.Is(err, errNoCertYet) {
			// Not an error: Caddy issues it on the first HTTPS request.
			return dc, nil
		}
		dc.Error = strings.TrimPrefix(dc.Error+"; "+err.Error(), "; ")
		return dc, nil
	}
	dc.CertificateOK = true
	return dc, nil
}

// errNoCertYet means the handshake failed without a certificate to judge:
// with DNS OK that means issuance is still pending.
var errNoCertYet = errors.New("no certificate yet")

// checkCertificate handshakes with the local Caddy as hostname.
func (p *Proxy) checkCertificate(ctx context.Context, hostname string) error {
	ctx, cancel := context.WithTimeout(ctx, certCheckTimeout)
	defer cancel()
	d := tls.Dialer{Config: &tls.Config{ServerName: hostname, RootCAs: p.rootCAs}}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p.opts.HTTPSPort)))
	if err != nil {
		var unknownAuthority x509.UnknownAuthorityError
		var invalid x509.CertificateInvalidError
		var hostErr x509.HostnameError
		switch {
		case errors.As(err, &unknownAuthority), errors.As(err, &invalid), errors.As(err, &hostErr):
			return fmt.Errorf("the certificate served for %s is not valid: %w", hostname, err)
		default:
			return fmt.Errorf("%w for %s: %w", errNoCertYet, hostname, err)
		}
	}
	return conn.Close()
}

func dnsError(hostname string, err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return noRecords(hostname)
	}
	return fmt.Sprintf("could not look up %s: %v", hostname, err)
}

func noRecords(hostname string) string {
	return fmt.Sprintf("%s has no DNS records yet; add an A record pointing at this server", hostname)
}
