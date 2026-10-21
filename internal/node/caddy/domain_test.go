package caddy

import (
	"context"
	"net"
	"strings"
	"testing"
)

// TestCheckDomainDNS covers the DNS half of CheckDomain. The certificate
// half needs a running Caddy and is covered by the integration tests.
func TestCheckDomainDNS(t *testing.T) {
	notFound := &net.DNSError{Err: "no such host", Name: "x", IsNotFound: true}
	for name, tc := range map[string]struct {
		publicIPs []string
		resolved  []string
		lookupErr error
		dnsOK     bool
		errHas    string // empty means no error expected
	}{
		"points here":        {publicIPs: []string{"203.0.113.7"}, resolved: []string{"203.0.113.7"}, dnsOK: true},
		"ipv6 points here":   {publicIPs: []string{"203.0.113.7", "2001:db8::7"}, resolved: []string{"2001:db8::7"}, dnsOK: true},
		"points elsewhere":   {publicIPs: []string{"203.0.113.7"}, resolved: []string{"198.51.100.1"}, errHas: "should point at 203.0.113.7"},
		"extra stray record": {publicIPs: []string{"203.0.113.7"}, resolved: []string{"203.0.113.7", "2001:db8::1"}, dnsOK: true, errHas: "also points at 2001:db8::1"},
		"no records":         {publicIPs: []string{"203.0.113.7"}, lookupErr: notFound, errHas: "no DNS records yet"},
		"no public IP":       {resolved: []string{"203.0.113.7"}, errHas: "no public IP configured"},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := New(Options{AdminSocket: "/x", HTTPPort: 80, HTTPSPort: 443, DisableTLS: true, PublicIPs: tc.publicIPs})
			if err != nil {
				t.Fatal(err)
			}
			p.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
				var addrs []net.IPAddr
				for _, s := range tc.resolved {
					addrs = append(addrs, net.IPAddr{IP: net.ParseIP(s)})
				}
				return addrs, tc.lookupErr
			}

			dc, err := p.CheckDomain(context.Background(), "app.example.com")
			if err != nil {
				t.Fatal(err)
			}
			if dc.DNSOK != tc.dnsOK {
				t.Errorf("DNSOK = %v, want %v (%+v)", dc.DNSOK, tc.dnsOK, dc)
			}
			if dc.CertificateOK {
				t.Error("CertificateOK must be false with TLS disabled")
			}
			if tc.errHas == "" && dc.Error != "" || !strings.Contains(dc.Error, tc.errHas) {
				t.Errorf("Error = %q, want it to contain %q", dc.Error, tc.errHas)
			}
		})
	}
}
