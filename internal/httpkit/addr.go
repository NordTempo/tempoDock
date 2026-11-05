package httpkit

import "net/netip"

// Address checks for requests Dock makes to addresses a user gave it:
// alert channels and log drains (core's egressGuard) and uptime probes
// (internal/node/uptime). Each runs them in a dialer's Control, after
// DNS, so a hostname can't resolve its way past them. Private, CGNAT and
// tailnet addresses are fine: a homelab's services live there.

// Cloud metadata services outside 169.254.0.0/16: AWS over IPv6 and
// Alibaba Cloud.
var (
	awsMetadataV6   = netip.MustParseAddr("fd00:ec2::254")
	alibabaMetadata = netip.MustParseAddr("100.100.100.200")
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
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip == awsMetadataV6 || ip == alibabaMetadata
}
