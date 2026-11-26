package remote

import (
	"net"
	"net/netip"
	"slices"
)

// Where nodes dial the control plane (ADR 0102). The control plane may
// listen on every interface (":7443"); join tokens and every hello then
// carry the addresses nodes should try, in order: its Tailscale address
// first, then its public one. The link pins keys on both sides, so the
// address only decides the path, never whom a node talks to.

// Via is the path a node's connection came over, as the control plane
// sees its source address.
type Via string

const (
	ViaTailscale Via = "tailscale"
	ViaLAN       Via = "lan"
	ViaInternet  Via = "internet"
)

var (
	// tailnetV4 is 100.64.0.0/10, the CGNAT range Tailscale hands out.
	tailnetV4 = netip.MustParsePrefix("100.64.0.0/10")
	// tailnetV6 is Tailscale's IPv6 range.
	tailnetV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// ViaOf classifies a node connection's source address. A 100.64.0.0/10
// address counts as Tailscale: carrier-grade NAT uses the same range, but
// a carrier's address never reaches a server as a connection's source.
func ViaOf(addr net.Addr) Via {
	ap, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return ViaInternet
	}
	ip := ap.Addr().Unmap()
	switch {
	case tailnetV4.Contains(ip) || tailnetV6.Contains(ip):
		return ViaTailscale
	case ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast():
		return ViaLAN
	}
	return ViaInternet
}

// ListenAddrs returns what join tokens and hellos tell nodes to dial:
// explicit ([node] addrs) when set; else listen itself when it names an
// address; else, for a listen on every interface, this machine's
// Tailscale address and then publicIP, both on listen's port. The last
// is read on every call, so a Tailscale that comes up after the daemon
// is picked up by the next token or connection.
func ListenAddrs(listen, publicIP string, explicit []string) func() []string {
	if len(explicit) > 0 {
		fixed := slices.Clone(explicit)
		return func() []string { return fixed }
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return func() []string { return nil }
	}
	if ip, err := netip.ParseAddr(host); host != "" && (err != nil || !ip.IsUnspecified()) {
		return func() []string { return []string{listen} }
	}
	return func() []string {
		var out []string
		if ts, ok := tailscaleAddr(); ok {
			out = append(out, net.JoinHostPort(ts.String(), port))
		}
		if publicIP != "" {
			out = append(out, net.JoinHostPort(publicIP, port))
		}
		return out
	}
}

// tailscaleAddr is this machine's Tailscale IPv4 address, if it has one.
func tailscaleAddr() (netip.Addr, bool) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return netip.Addr{}, false
	}
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil && tailnetV4.Contains(p.Addr()) {
			return p.Addr(), true
		}
	}
	return netip.Addr{}, false
}

// mergeAddrs is the address list a node keeps after a hello: the control
// plane's list, plus the address this connection came over when it isn't
// in it (one set by hand with `dock node set-control-plane`, say), so a
// path that works is never dropped.
func mergeAddrs(pushed []string, used string) []string {
	out := slices.Clone(pushed)
	if used != "" && !slices.Contains(out, used) {
		out = append(out, used)
	}
	return out
}
