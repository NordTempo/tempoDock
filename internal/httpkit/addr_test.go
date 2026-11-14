package httpkit

import "testing"

// IPv4 clients count by address, IPv6 ones by their /64.
func TestClientKey(t *testing.T) {
	for ip, want := range map[string]string{
		"192.0.2.7":            "192.0.2.7",
		"::ffff:192.0.2.7":     "192.0.2.7",
		"2001:db8:1:2:3:4:5:6": "2001:db8:1:2::/64",
		"2001:db8:1:2::9":      "2001:db8:1:2::/64",
		"fe80::1%eth0":         "fe80::/64",
		"not an ip":            "not an ip",
	} {
		if got := ClientKey(ip); got != want {
			t.Errorf("ClientKey(%q) = %q, want %q", ip, got, want)
		}
	}
}
