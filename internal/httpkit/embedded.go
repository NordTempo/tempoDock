package httpkit

import (
	"encoding/binary"
	"net/netip"
)

// IPv6 ranges whose addresses carry an IPv4 address that a translator or
// tunnel delivers the packets to.
var (
	// nat64 is the well-known NAT64 prefix (RFC 6052): the IPv4 address
	// is the last 32 bits.
	nat64 = netip.MustParsePrefix("64:ff9b::/96")
	// nat64Local is the local-use NAT64 prefix (RFC 8215). A network
	// carves its own translation prefix out of it, of any RFC 6052
	// length, and the length decides where the IPv4 address sits
	// (nat64Layouts).
	nat64Local = netip.MustParsePrefix("64:ff9b:1::/48")
	// sixToFour is 6to4 (RFC 3056): the IPv4 address is bits 16 to 48.
	sixToFour = netip.MustParsePrefix("2002::/16")
	// teredo (RFC 4380) ends in the client's IPv4 address, each bit flipped.
	teredo = netip.MustParsePrefix("2001::/32")
	// v4Compatible is the deprecated ::a.b.c.d form (RFC 4291).
	v4Compatible = netip.MustParsePrefix("::/96")
)

// nat64Layouts are where RFC 6052 puts the IPv4 address's four bytes for
// each prefix length that fits inside nat64Local: /96, /64, /56 and /48.
// Byte 8 of the address is reserved and skipped.
var nat64Layouts = [][4]int{{12, 13, 14, 15}, {9, 10, 11, 12}, {7, 9, 10, 11}, {6, 7, 9, 10}}

// localNAT64 is the IPv4 address a local-use NAT64 address is judged as.
// Nothing in the address says which layout its network uses, so it is
// the worst of the four readings: one that is this server or an internal
// address, else one that isn't public, else the /96 reading. A reading
// of 0.0.0.0 is passed over: nothing is delivered there, and the unused
// bits of every other layout read as it.
func localNAT64(b [16]byte) netip.Addr {
	var notPublic netip.Addr
	for _, l := range nat64Layouts {
		v4 := netip.AddrFrom4([4]byte{b[l[0]], b[l[1]], b[l[2]], b[l[3]]})
		if v4.IsUnspecified() {
			continue
		}
		if IsSelfAddr(v4) || IsInternalAddr(v4) {
			return v4
		}
		if !notPublic.IsValid() && !IsPublicAddr(v4) {
			notPublic = v4
		}
	}
	if notPublic.IsValid() {
		return notPublic
	}
	return netip.AddrFrom4([4]byte(b[12:16]))
}

// EffectiveAddr is the address a connection to ip ends up at, which the
// address checks here judge: ip itself, or the IPv4 address inside it
// when it is IPv4-mapped, IPv4-compatible, NAT64, 6to4 or Teredo. So
// 64:ff9b::7f00:1 and 2002:7f00:1:: are judged as 127.0.0.1, and one
// that carries a public IPv4 address stays allowed. A local-use NAT64
// address is judged by the worst of its possible readings (localNAT64).
func EffectiveAddr(ip netip.Addr) netip.Addr {
	ip = ip.Unmap().WithZone("")
	if !ip.Is6() {
		return ip
	}
	b := ip.As16()
	switch {
	case nat64.Contains(ip):
		return netip.AddrFrom4([4]byte(b[12:16]))
	case nat64Local.Contains(ip):
		return localNAT64(b)
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
