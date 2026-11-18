package httpkit

import (
	"net/netip"
	"testing"
)

// An IPv6 address that carries an IPv4 one is judged as that IPv4
// address: private, loopback and metadata ones are refused in every
// form, and a public one stays allowed.
func TestEmbeddedIPv4Addresses(t *testing.T) {
	for _, tc := range []struct {
		ip, effective          string
		self, internal, public bool
	}{
		{"64:ff9b::7f00:1", "127.0.0.1", true, false, false},
		{"64:ff9b::a9fe:a9fe", "169.254.169.254", false, true, false},
		{"64:ff9b:1::a9fe:a9fe", "169.254.169.254", false, true, false},
		{"64:ff9b::c0a8:101", "192.168.1.1", false, false, false},
		// The local-use NAT64 prefix in each RFC 6052 layout: /64, /56
		// and /48 put the IPv4 address elsewhere than the last 32 bits.
		{"64:ff9b:1:0:a9:fea9:fe00:0", "169.254.169.254", false, true, false},
		{"64:ff9b:1:a9:fe:a9fe::", "169.254.169.254", false, true, false},
		{"64:ff9b:1:a9fe:a9:fe00::", "169.254.0.0", false, true, false}, // /48 layout; its /64 reading is refused first
		{"64:ff9b:1:7f00:0:100::", "127.0.0.1", true, false, false},
		{"64:ff9b:1::808:808", "8.8.8.8", false, false, true},
		{"2002:7f00:1::", "127.0.0.1", true, false, false},
		{"2002:c0a8:1::1", "192.168.0.1", false, false, false},
		{"2002:a9fe:a9fe::1", "169.254.169.254", false, true, false},
		{"2001:0:4136:e378:8000:63bf:80ff:fffe", "127.0.0.1", true, false, false},
		{"2001:0:4136:e378:8000:63bf:f5ff:fffe", "10.0.0.1", false, false, false},
		{"::7f00:1", "127.0.0.1", true, false, false},
		{"::ffff:10.0.0.1", "10.0.0.1", false, false, false},
		{"::1", "::1", true, false, false},
		{"::", "::", true, false, false},
		// A public IPv4 address inside any of them is still public.
		{"64:ff9b::808:808", "8.8.8.8", false, false, true},
		{"2002:808:808::1", "8.8.8.8", false, false, true},
		{"2001:0:4136:e378:8000:63bf:f7f7:f7f7", "8.8.8.8", false, false, true},
		// Addresses outside those ranges are judged as themselves.
		{"2606:4700:4700::1111", "2606:4700:4700::1111", false, false, true},
		{"8.8.8.8", "8.8.8.8", false, false, true},
	} {
		ip := netip.MustParseAddr(tc.ip)
		if got := EffectiveAddr(ip).String(); got != tc.effective {
			t.Errorf("EffectiveAddr(%s) = %s, want %s", tc.ip, got, tc.effective)
		}
		if got := IsSelfAddr(ip); got != tc.self {
			t.Errorf("IsSelfAddr(%s) = %v, want %v", tc.ip, got, tc.self)
		}
		if got := IsInternalAddr(ip); got != tc.internal {
			t.Errorf("IsInternalAddr(%s) = %v, want %v", tc.ip, got, tc.internal)
		}
		if got := IsPublicAddr(ip); got != tc.public {
			t.Errorf("IsPublicAddr(%s) = %v, want %v", tc.ip, got, tc.public)
		}
	}
}
