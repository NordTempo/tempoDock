package firewall

import (
	"net/netip"
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
