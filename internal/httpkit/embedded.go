package httpkit

import (
	"encoding/binary"
	"net/netip"
)

// IPv6 ranges whose addresses carry an IPv4 address that a translator or
// tunnel delivers the packets to.
var (
	// nat64 is the well-known NAT64 prefix (RFC 6052) and nat64Local the
	// local-use one (RFC 8215); the IPv4 address is the last 32 bits of a
	// /96, the form translators are set up with.
	nat64      = netip.MustParsePrefix("64:ff9b::/96")
	nat64Local = netip.MustParsePrefix("64:ff9b:1::/48")
	// sixToFour is 6to4 (RFC 3056): the IPv4 address is bits 16 to 48.
	sixToFour = netip.MustParsePrefix("2002::/16")
	// teredo (RFC 4380) ends in the client's IPv4 address, each bit flipped.
	teredo = netip.MustParsePrefix("2001::/32")
	// v4Compatible is the deprecated ::a.b.c.d form (RFC 4291).
	v4Compatible = netip.MustParsePrefix("::/96")
)

// EffectiveAddr is the address a connection to ip ends up at, which the
// address checks here judge: ip itself, or the IPv4 address inside it
// when it is IPv4-mapped, IPv4-compatible, NAT64, 6to4 or Teredo. So
// 64:ff9b::7f00:1 and 2002:7f00:1:: are judged as 127.0.0.1, and one
// that carries a public IPv4 address stays allowed.
func EffectiveAddr(ip netip.Addr) netip.Addr {
	ip = ip.Unmap().WithZone("")
	if !ip.Is6() {
		return ip
	}
	b := ip.As16()
	switch {
	case nat64.Contains(ip), nat64Local.Contains(ip):
		return netip.AddrFrom4([4]byte(b[12:16]))
	case sixToFour.Contains(ip):
		return netip.AddrFrom4([4]byte(b[2:6]))
	case teredo.Contains(ip):
		var v4 [4]byte
		binary.BigEndian.PutUint32(v4[:], ^binary.BigEndian.Uint32(b[12:16]))
		return netip.AddrFrom4(v4)
	case v4Compatible.Contains(ip) && !ip.IsUnspecified() && !ip.IsLoopback():
		return netip.AddrFrom4([4]byte(b[12:16]))
	}
	return ip
}
