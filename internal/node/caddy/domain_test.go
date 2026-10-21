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

// With DNS right and no certificate served yet, the check reports no error,
// so the domain shows as issuing_certificate rather than error.
func TestCheckDomainNoCertificateYet(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close() // nothing serves TLS there
	p, err := New(Options{AdminSocket: "/x", HTTPPort: 80, HTTPSPort: port, AskURL: "http://127.0.0.1:1/ask", PublicIPs: []string{"203.0.113.7"}})
	if err != nil {
		t.Fatal(err)
	}
	p.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.7")}}, nil
	}
	dc, err := p.CheckDomain(context.Background(), "app.example.com")
	if err != nil || !dc.DNSOK || dc.CertificateOK || dc.Error != "" {
		t.Fatalf("check = %+v, %v", dc, err)
	}
}
