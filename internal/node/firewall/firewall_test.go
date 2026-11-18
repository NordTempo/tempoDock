package firewall

import (
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var spec = Spec{
	Bridges:   []string{"dk-+", "br-0123456789ab", "docker0"},
	Internal:  prefixes("10.213.0.0/16", "172.18.0.0/16"),
	Allow:     prefixes("192.168.10.20/32", "fd12::/64"),
	Resolvers: []netip.Addr{netip.MustParseAddr("192.168.10.1")},
	HostTCP:   []int{443, 80, 80},
	HostUDP:   []int{443},
}

// lines returns the rules of chain, without the "-A chain " prefix.
func lines(rules, chain string) []string {
	var out []string
	for l := range strings.Lines(rules) {
		if rest, ok := strings.CutPrefix(l, "-A "+chain+" "); ok {
			out = append(out, strings.TrimSpace(rest))
		}
	}
	return out
}

func TestRulesIPv4(t *testing.T) {
	r := spec.rules(ipv4)
	if !strings.HasPrefix(r, "*mangle\n:DOCK-FORWARD - [0:0]\n") || !strings.HasSuffix(r, "COMMIT\n") {
		t.Errorf("not a mangle table restore file:\n%s", r)
	}
	if got, want := lines(r, chainForward), []string{
		"-i dk-+ -j DOCK-EGRESS", "-i br-0123456789ab -j DOCK-EGRESS", "-i docker0 -j DOCK-EGRESS",
	}; !slices.Equal(got, want) {
		t.Errorf("DOCK-FORWARD = %q, want %q", got, want)
	}
	if got := lines(r, chainInput); len(got) != 3 || got[2] != "-i docker0 -j DOCK-HOST" {
		t.Errorf("DOCK-INPUT = %q", got)
	}

	// Order matters: established and allowed traffic returns before the drops.
	if got, want := lines(r, chainEgress), []string{
		"-m conntrack --ctstate RELATED,ESTABLISHED -j RETURN",
		"-m conntrack --ctorigdst 10.213.0.0/16 -j RETURN",
		"-m conntrack --ctorigdst 172.18.0.0/16 -j RETURN",
		"-m conntrack --ctorigdst 192.168.10.20/32 -j RETURN",
		"-d 192.168.10.1/32 -p udp --dport 53 -j RETURN",
		"-d 192.168.10.1/32 -p tcp --dport 53 -j RETURN",
		"-m conntrack --ctorigdst 10.0.0.0/8 -j DROP",
		"-m conntrack --ctorigdst 172.16.0.0/12 -j DROP",
		"-m conntrack --ctorigdst 192.168.0.0/16 -j DROP",
		"-m conntrack --ctorigdst 100.64.0.0/10 -j DROP",
		"-m conntrack --ctorigdst 169.254.0.0/16 -j DROP",
	}; !slices.Equal(got, want) {
		t.Errorf("DOCK-EGRESS =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got, want := lines(r, chainHost), []string{
		"-m conntrack --ctstate RELATED,ESTABLISHED -j RETURN",
		"-p tcp -m multiport --dports 443,80 -j RETURN",
		"-p udp -m multiport --dports 443 -j RETURN",
		"-d 192.168.10.20/32 -j RETURN",
		"-d 192.168.10.1/32 -p udp --dport 53 -j RETURN",
		"-d 192.168.10.1/32 -p tcp --dport 53 -j RETURN",
		"-j DROP",
	}; !slices.Equal(got, want) {
		t.Errorf("DOCK-HOST =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestRulesIPv6(t *testing.T) {
	r := spec.rules(ipv6)
	egress := lines(r, chainEgress)
	if want := []string{
		"-m conntrack --ctstate RELATED,ESTABLISHED -j RETURN",
		"-m conntrack --ctorigdst fd12::/64 -j RETURN",
		"-m conntrack --ctorigdst fc00::/7 -j DROP",
		"-m conntrack --ctorigdst fe80::/10 -j DROP",
		// The IPv4 ranges again, inside NAT64 and 6to4.
		"-m conntrack --ctorigdst 64:ff9b::a00:0/104 -j DROP",
		"-m conntrack --ctorigdst 64:ff9b::ac10:0/108 -j DROP",
		"-m conntrack --ctorigdst 64:ff9b::c0a8:0/112 -j DROP",
		"-m conntrack --ctorigdst 64:ff9b::6440:0/106 -j DROP",
		"-m conntrack --ctorigdst 64:ff9b::a9fe:0/112 -j DROP",
		"-m conntrack --ctorigdst 2002:a00::/24 -j DROP",
		"-m conntrack --ctorigdst 2002:ac10::/28 -j DROP",
		"-m conntrack --ctorigdst 2002:c0a8::/32 -j DROP",
		"-m conntrack --ctorigdst 2002:6440::/26 -j DROP",
		"-m conntrack --ctorigdst 2002:a9fe::/32 -j DROP",
		// Dropped whole.
		"-m conntrack --ctorigdst 64:ff9b:1::/48 -j DROP",
		"-m conntrack --ctorigdst 2001::/32 -j DROP",
		"-m conntrack --ctorigdst ::/96 -j DROP",
		"-m conntrack --ctorigdst ::ffff:0.0.0.0/96 -j DROP",
	}; !slices.Equal(egress, want) {
		t.Errorf("DOCK-EGRESS =\n%s\nwant\n%s", strings.Join(egress, "\n"), strings.Join(want, "\n"))
	}
	host := lines(r, chainHost)
	if !slices.Contains(host, "-p ipv6-icmp -j RETURN") || host[len(host)-1] != "-j DROP" {
		t.Errorf("DOCK-HOST = %q", host)
	}
	if strings.Contains(r, "192.168.") || strings.Contains(r, "10.213.") {
		t.Errorf("IPv4 addresses in IPv6 rules:\n%s", r)
	}
}

// An IPv6 destination that carries a blocked IPv4 address is blocked, one
// that carries a public IPv4 address through NAT64 or 6to4 is not: a
// server on an IPv6-only network reaches the IPv4 internet that way.
func TestBlockedCarriedIPv4(t *testing.T) {
	for ip, want := range map[string]bool{
		"64:ff9b::a9fe:a9fe":   true,  // 169.254.169.254
		"64:ff9b::c0a8:a01":    true,  // 192.168.10.1
		"64:ff9b::6464:6464":   true,  // 100.100.100.100
		"64:ff9b::808:808":     false, // 8.8.8.8
		"2002:a9fe:a9fe::1":    true,  // 169.254.169.254
		"2002:ac1f:1::1":       true,  // 172.31.0.1
		"2002:ac20:1::1":       false, // 172.32.0.1
		"2002:808:808::1":      false, // 8.8.8.8
		"64:ff9b:1::808:808":   true,  // local-use NAT64, whatever it carries
		"64:ff9b:2::808:808":   false, // outside both NAT64 prefixes
		"2001:0:4136:e378::1":  true,  // Teredo
		"2001:db8::1":          false, // not Teredo
		"::a00:1":              true,  // IPv4-compatible
		"2606:4700:4700::1111": false,
	} {
		if got := Blocked(netip.MustParseAddr(ip)); got != want {
			t.Errorf("Blocked(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestNameservers(t *testing.T) {
	got, err := nameservers(strings.NewReader(`# generated
search lan
nameserver 192.168.10.1
nameserver 127.0.0.53
nameserver ::1
nameserver fe80::1%eth0
nameserver 2001:db8::53
nameserver not-an-ip
options edns0
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Addr{netip.MustParseAddr("192.168.10.1"), netip.MustParseAddr("2001:db8::53")}
	if !slices.Equal(got, want) {
		t.Errorf("nameservers = %v, want %v", got, want)
	}
}

// The marker says applied from MarkApplied until ClearApplied, and
// clearing one that isn't there is fine.
func TestAppliedMarker(t *testing.T) {
	appliedPath = filepath.Join(t.TempDir(), "dock-firewall", "applied")
	t.Cleanup(func() { appliedPath = AppliedPath })

	if ok, err := Applied(); ok || err != nil {
		t.Fatalf("before apply: applied %v, err %v", ok, err)
	}
	if err := ClearApplied(); err != nil {
		t.Fatal(err)
	}
	for range 2 { // applying again replaces the marker
		if err := MarkApplied(); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := Applied(); !ok || err != nil {
		t.Fatalf("after apply: applied %v, err %v", ok, err)
	}
	if err := ClearApplied(); err != nil {
		t.Fatal(err)
	}
	if ok, err := Applied(); ok || err != nil {
		t.Fatalf("after remove: applied %v, err %v", ok, err)
	}
}
