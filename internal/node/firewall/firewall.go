// Package firewall keeps app containers and builds off the server's private
// networks: the LAN, the tailnet, link-local addresses and the server
// itself, also when the destination is an IPv6 address that carries one
// of those IPv4 addresses (NAT64, 6to4). The public internet stays
// reachable.
//
// The rules live in iptables' mangle table, in chains named DOCK-*, hooked
// from mangle FORWARD (traffic leaving through the server) and mangle INPUT
// (traffic to the server). Mangle runs before the filter table, so nothing
// there (Docker's DOCKER-USER, Tailscale's ts-forward, ufw) can accept a
// packet before these rules see it, whatever order those tools inserted
// their rules in. Rules match the bridge a packet arrives on, not its source
// address, so a container can't dodge them by spoofing.
//
// Root installs them (`dock admin firewall apply`, run by
// dock-firewall.service); the daemon itself has no firewall access. It
// learns whether they are in from a marker file (AppliedPath).
package firewall

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Spec is everything the rules depend on.
type Spec struct {
	// Bridges are the host interfaces app containers and builds send from:
	// interface names or iptables wildcards ("dk-+").
	Bridges []string
	// Internal are the subnets of dock's own networks. Traffic to them is
	// left to Docker, which already isolates networks from each other.
	Internal []netip.Prefix
	// Allow are private destinations that stay reachable, on any port
	// ([network] allow_private).
	Allow []netip.Prefix
	// Resolvers are the DNS servers Docker's embedded DNS forwards queries
	// to. It does that from inside the container's network namespace, so
	// port 53 to them must stay open even when they are on the LAN.
	Resolvers []netip.Addr
	// HostTCP and HostUDP are ports on the server itself that containers may
	// reach: Caddy's, so apps can call each other through their domains.
	HostTCP, HostUDP []int
}

// Chains are the iptables chains the firewall owns, in the mangle table.
const (
	chainForward = "DOCK-FORWARD" // hooked from FORWARD; dispatches by bridge
	chainInput   = "DOCK-INPUT"   // hooked from INPUT; dispatches by bridge
	chainEgress  = "DOCK-EGRESS"  // what app traffic leaving the server may reach
	chainHost    = "DOCK-HOST"    // what app traffic to the server may reach
)

// hooks maps each built-in mangle chain to the DOCK chain it jumps to.
var hooks = [][2]string{{"FORWARD", chainForward}, {"INPUT", chainInput}}

// family is one IP version and its iptables commands.
type family struct {
	name    string
	cmd     string // iptables or ip6tables
	restore string // iptables-restore or ip6tables-restore
	is6     bool
	// blocked are the private destinations app traffic may not reach.
	blocked []netip.Prefix
}

var (
	ipv4 = family{
		name: "IPv4", cmd: "iptables", restore: "iptables-restore",
		blocked: prefixes(
			"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", // RFC 1918: the LAN
			"100.64.0.0/10",  // CGNAT: the Tailscale tailnet and its DNS, 100.100.100.100
			"169.254.0.0/16", // link-local: cloud metadata endpoints
		),
	}
	ipv6 = family{
		name: "IPv6", cmd: "ip6tables", restore: "ip6tables-restore", is6: true,
		blocked: append(prefixes(
			"fc00::/7",  // unique local, includes the tailnet's fd7a:115c:a1e0::/48
			"fe80::/10", // link-local
		), carried(ipv4.blocked)...),
	}
	families = []family{ipv4, ipv6}
)

// IPv6 ranges whose addresses carry an IPv4 address: a translator or a
// tunnel on the way (the server's own, or its network's) delivers the
// packet to that IPv4 address, past the IPv4 rules.
var (
	// nat64 is the well-known NAT64 prefix (RFC 6052): the IPv4 address
	// is the last 32 bits. A server on an IPv6-only network reaches the
	// IPv4 internet through it, so only the blocked ranges inside it are
	// dropped.
	nat64 = netip.MustParsePrefix("64:ff9b::/96")
	// sixToFour is 6to4 (RFC 3056): the IPv4 address follows the first 16
	// bits. Again only the blocked ranges inside it are dropped.
	sixToFour = netip.MustParsePrefix("2002::/16")
	// carriedWhole are dropped whole, since no prefix match can pick the
	// private addresses out of them and nothing public is served there:
	// the local-use NAT64 prefix (RFC 8215), where each network chooses
	// where in the address the IPv4 one sits; Teredo (RFC 4380), which
	// ends in the IPv4 address with its bits flipped; and the
	// IPv4-compatible and IPv4-mapped forms (RFC 4291), which are never
	// valid destinations on the wire. A network that does run its own
	// NAT64 on the local-use prefix lists it in [network] allow_private.
	carriedWhole = prefixes("64:ff9b:1::/48", "2001::/32", "::/96", "::ffff:0:0/96")
)

// carried returns the IPv6 forms of the IPv4 ranges v4: each range
// inside the NAT64 and 6to4 prefixes, then the ranges dropped whole.
func carried(v4 []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, outer := range []netip.Prefix{nat64, sixToFour} {
		for _, p := range v4 {
			out = append(out, embed(outer, p))
		}
	}
	return append(out, carriedWhole...)
}

// embed returns the IPv6 prefix of the addresses in outer that carry an
// IPv4 address in v4 right after outer's bits: 10.0.0.0/8 in 2002::/16 is
// 2002:a00::/24.
func embed(outer, v4 netip.Prefix) netip.Prefix {
	b := outer.Addr().As16()
	a := v4.Masked().Addr().As4()
	copy(b[outer.Bits()/8:], a[:])
	return netip.PrefixFrom(netip.AddrFrom16(b), outer.Bits()+v4.Bits())
}

// Blocked reports whether ip is one of the private destinations app
// traffic may not reach (the LAN, the tailnet, link-local, and the IPv6
// addresses that carry one of those). Log drains
// refuse the same addresses (core's egressGuard), since the control plane
// sends to them from the host, where these rules don't apply.
func Blocked(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, f := range families {
		for _, p := range f.blocked {
			if p.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// prefixes parses ss; they are constants, so a bad one panics at start.
func prefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// Rules returns the iptables-restore input for both IP versions, for
// `--dry-run` and tests.
func Rules(s Spec) string {
	var b strings.Builder
	for _, f := range families {
		fmt.Fprintf(&b, "# %s (%s)\n%s", f.name, f.restore, s.rules(f))
	}
	return b.String()
}

// rules renders s for one IP version as iptables-restore input. Declaring a
// chain flushes it, so loading this with --noflush swaps the DOCK chains'
// contents atomically and leaves every other rule alone.
func (s Spec) rules(f family) string {
	var b strings.Builder
	add := func(chain string, args ...string) {
		b.WriteString("-A " + chain + " " + strings.Join(args, " ") + "\n")
	}
	b.WriteString("*mangle\n")
	for _, c := range []string{chainForward, chainInput, chainEgress, chainHost} {
		b.WriteString(":" + c + " - [0:0]\n")
	}
	for _, br := range s.Bridges {
		add(chainForward, "-i", br, "-j", chainEgress)
		add(chainInput, "-i", br, "-j", chainHost)
	}

	// Leaving the server: anything public, but no private destinations
	// beyond dock's own networks, the allowlist and DNS. --ctorigdst is the
	// address before Docker's port forwarding rewrote it.
	add(chainEgress, "-m conntrack --ctstate RELATED,ESTABLISHED -j RETURN")
	for _, p := range f.prefixes(s.Internal) {
		add(chainEgress, "-m conntrack --ctorigdst", p.String(), "-j RETURN")
	}
	for _, p := range f.prefixes(s.Allow) {
		add(chainEgress, "-m conntrack --ctorigdst", p.String(), "-j RETURN")
	}
	for _, a := range f.addrs(s.Resolvers) {
		for _, proto := range []string{"udp", "tcp"} {
			add(chainEgress, "-d", host(a), "-p", proto, "--dport 53 -j RETURN")
		}
	}
	for _, p := range f.blocked {
		add(chainEgress, "-m conntrack --ctorigdst", p.String(), "-j DROP")
	}

	// To the server: only Caddy's ports, the allowlist and DNS.
	add(chainHost, "-m conntrack --ctstate RELATED,ESTABLISHED -j RETURN")
	if f.is6 {
		add(chainHost, "-p ipv6-icmp -j RETURN") // neighbour discovery
	}
	if len(s.HostTCP) > 0 {
		add(chainHost, "-p tcp -m multiport --dports", ports(s.HostTCP), "-j RETURN")
	}
	if len(s.HostUDP) > 0 {
		add(chainHost, "-p udp -m multiport --dports", ports(s.HostUDP), "-j RETURN")
	}
	for _, p := range f.prefixes(s.Allow) {
		add(chainHost, "-d", p.String(), "-j RETURN")
	}
	for _, a := range f.addrs(s.Resolvers) {
		for _, proto := range []string{"udp", "tcp"} {
			add(chainHost, "-d", host(a), "-p", proto, "--dport 53 -j RETURN")
		}
	}
	add(chainHost, "-j DROP")
	b.WriteString("COMMIT\n")
	return b.String()
}

// prefixes and addrs keep the entries of f's IP version.
func (f family) prefixes(ps []netip.Prefix) []netip.Prefix {
	return slices.DeleteFunc(slices.Clone(ps), func(p netip.Prefix) bool { return p.Addr().Is6() != f.is6 })
}

func (f family) addrs(as []netip.Addr) []netip.Addr {
	return slices.DeleteFunc(slices.Clone(as), func(a netip.Addr) bool { return a.Is6() != f.is6 })
}

func host(a netip.Addr) string { return netip.PrefixFrom(a, a.BitLen()).String() }

func ports(ps []int) string {
	ss := make([]string, len(ps))
	for i, p := range ps {
		ss[i] = strconv.Itoa(p)
	}
	return strings.Join(slices.Compact(slices.Sorted(slices.Values(ss))), ",")
}

// Apply installs s for IPv4, and for IPv6 when ip6tables exists. Running it
// again replaces the rules; it never touches rules outside the DOCK chains.
// It returns a note per IP version it skipped.
func Apply(ctx context.Context, s Spec) (skipped []string, err error) {
	for _, f := range families {
		if _, err := exec.LookPath(f.cmd); err != nil {
			if !f.is6 {
				return nil, fmt.Errorf("%s not found: install the iptables package", f.cmd)
			}
			skipped = append(skipped, fmt.Sprintf("%s: %s not found", f.name, f.cmd))
			continue
		}
		if err := run(ctx, s.rules(f), f.restore, "-w", "--noflush"); err != nil {
			return skipped, fmt.Errorf("load %s rules: %w", f.name, err)
		}
		for _, h := range hooks {
			if run(ctx, "", f.cmd, "-w", "-t", "mangle", "-C", h[0], "-j", h[1]) == nil {
				continue // already hooked
			}
			if err := run(ctx, "", f.cmd, "-w", "-t", "mangle", "-I", h[0], "1", "-j", h[1]); err != nil {
				return skipped, fmt.Errorf("hook %s %s: %w", f.name, h[1], err)
			}
		}
	}
	return skipped, nil
}

// Remove deletes the DOCK chains and their hooks, leaving app traffic
// unfiltered. Missing rules and chains are not an error.
func Remove(ctx context.Context) error {
	for _, f := range families {
		if _, err := exec.LookPath(f.cmd); err != nil {
			continue
		}
		for _, h := range hooks {
			for range 10 { // every copy, should someone have added more
				if run(ctx, "", f.cmd, "-w", "-t", "mangle", "-D", h[0], "-j", h[1]) != nil {
					break
				}
			}
		}
		chains := []string{chainForward, chainInput, chainEgress, chainHost}
		for _, c := range chains {
			_ = run(ctx, "", f.cmd, "-w", "-t", "mangle", "-F", c)
		}
		for _, c := range chains {
			_ = run(ctx, "", f.cmd, "-w", "-t", "mangle", "-X", c)
		}
	}
	return nil
}

// run executes name with stdin, folding its stderr into the error.
func run(ctx context.Context, stdin, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%s: %w: %s", name, err, msg)
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// resolvConfs are where Docker finds the host's DNS servers. On
// systemd-resolved hosts /etc/resolv.conf only lists the 127.0.0.53 stub and
// Docker uses the real upstreams from the second file.
var resolvConfs = []string{"/etc/resolv.conf", "/run/systemd/resolve/resolv.conf"}

// Resolvers returns the host's non-loopback DNS servers. Loopback ones need
// no rule: Docker forwards to those from the host's own namespace.
func Resolvers() ([]netip.Addr, error) {
	var out []netip.Addr
	for _, path := range resolvConfs {
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		addrs, err := nameservers(f)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		for _, a := range addrs {
			if !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	return out, nil
}

// nameservers parses resolv.conf, keeping servers containers can reach
// through a bridge: not loopback, not link-local.
func nameservers(r io.Reader) ([]netip.Addr, error) {
	var out []netip.Addr
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		a, err := netip.ParseAddr(fields[1])
		if err != nil || a.IsLoopback() || a.IsLinkLocalUnicast() || a.Zone() != "" {
			continue
		}
		out = append(out, a.Unmap())
	}
	return out, sc.Err()
}

// AppliedPath is the marker `dock admin firewall apply` leaves once the
// rules are in. /run is a tmpfs, so like the rules it is gone after a
// reboot: the file says "applied since this boot". The daemon, which has
// no iptables access, reads it to tell whether its server is protected.
const AppliedPath = "/run/dock-firewall/applied"

// appliedPath is AppliedPath; tests point it at a temporary directory.
var appliedPath = AppliedPath

// MarkApplied writes the marker (the time, RFC 3339) after Apply worked.
// Root calls it; everyone may read the file.
func MarkApplied() error {
	if err := os.MkdirAll(filepath.Dir(appliedPath), 0o755); err != nil {
		return fmt.Errorf("mark the firewall applied: %w", err)
	}
	// Written whole: a reader sees the old marker or the new one.
	tmp := appliedPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return fmt.Errorf("mark the firewall applied: %w", err)
	}
	if err := os.Rename(tmp, appliedPath); err != nil {
		return fmt.Errorf("mark the firewall applied: %w", err)
	}
	return nil
}

// ClearApplied removes the marker: after Remove, and after an Apply that
// failed. A marker that isn't there is not an error.
func ClearApplied() error {
	if err := os.Remove(appliedPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clear the firewall's applied marker: %w", err)
	}
	return nil
}

// Applied reports whether the rules were applied since this boot (the
// marker exists). An error means it couldn't tell.
func Applied() (bool, error) {
	if _, err := os.Stat(appliedPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read the firewall's applied marker: %w", err)
	}
	return true, nil
}
