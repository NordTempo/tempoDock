package httpkit

import "net/netip"

// Address checks for requests Dock makes to addresses a user gave it:
// alert channels and log drains (core's egressGuard) and uptime probes
// (internal/node/uptime). Each runs them in a dialer's Control, after
// DNS, so a hostname can't resolve its way past them. Private, CGNAT and
// tailnet addresses are fine: a homelab's services live there.

// Cloud metadata services outside 169.254.0.0/16: AWS over IPv6, Alibaba
// Cloud, and Azure's WireServer, which answers only from inside a VM and
// holds its goal state and extension settings.
var (
	awsMetadataV6   = netip.MustParseAddr("fd00:ec2::254")
	alibabaMetadata = netip.MustParseAddr("100.100.100.200")
	azureWireServer = netip.MustParseAddr("168.63.129.16")
)

// IsSelfAddr reports whether ip is the dialing server itself: loopback or
// unspecified. Those reach the daemon's loopback-only services (the
// internal listener, the registry) past the app firewall.
func IsSelfAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsUnspecified()
}

// cgnat is 100.64.0.0/10: carrier-grade NAT and tailnets.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// IsPublicAddr reports whether ip is a public unicast address: not this
// server's loopback, link-local, multicast or metadata, private (RFC
// 1918, ULA), CGNAT or tailnet. Uptime probes dial only these (ADR 0055).
func IsPublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return !IsSelfAddr(ip) && !IsInternalAddr(ip) && !ip.IsPrivate() && !cgnat.Contains(ip) && ip.IsGlobalUnicast()
}

// IsInternalAddr reports whether ip is link-local, multicast or a cloud
// metadata address (169.254.169.254 is link-local): never a destination
// for a request a user configured.
func IsInternalAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip == awsMetadataV6 || ip == alibabaMetadata || ip == azureWireServer
}

// ClientKey is what per-client limits count a client IP as: an IPv4
// address (IPv4-mapped IPv6 too) as itself, an IPv6 address as its /48:
// the site prefix one subscriber is commonly assigned (a /56 or a /64 is
// a part of one), and can pick any address in. A value that isn't an IP
// is its own key.
func ClientKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	if a = a.Unmap(); a.Is4() {
		return a.String()
	}
	p, err := a.WithZone("").Prefix(48)
	if err != nil {
		return ip
	}
	return p.String()
}
