package core

import (
	"fmt"
	"net/netip"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/NordTempo/tempoDock/internal/httpkit"
	"github.com/NordTempo/tempoDock/internal/node/firewall"
)

// egressGuard keeps the connections the daemon makes to user-given
// addresses (alert channels, log drains) away from the server itself and
// from link-local and cloud metadata addresses. Literal addresses are
// refused when a destination is added (hostErr); every connection is
// checked again after DNS (control, a net.Dialer's Control), so a name
// that resolves to one of them is refused too.
type egressGuard struct {
	// what names what is sent, in errors: "alerts", "logs".
	what string
	// private also refuses what the app firewall keeps apps from (RFC 1918,
	// CGNAT and the tailnet, unique local IPv6, the Docker bridges): set
	// for log drains, which any environment admin adds. Alert channels,
	// which only operators configure, may reach a homelab's LAN (ADR 0020).
	private bool
	// loopback lets connections reach this server, for tests only.
	loopback atomic.Bool
}

// addrErr says why nothing can be sent to ip, or nil. Loopback would
// reach the daemon's own loopback-only services (the internal listener,
// the registry) past the app firewall; link-local holds cloud metadata
// (169.254.169.254). Private, CGNAT and tailnet addresses are fine for
// alerts, where a homelab's ntfy or chat bridge lives (ADR 0020), and
// refused for log drains (g.private, ADR 0046). Uptime probes refuse the
// same addresses (httpkit.IsSelfAddr, IsInternalAddr).
func (g *egressGuard) addrErr(ip netip.Addr) error {
	ip = ip.Unmap()
	switch {
	case httpkit.IsSelfAddr(ip):
		if g.loopback.Load() {
			return nil
		}
		return fmt.Errorf("%s is this server itself; %s go to other hosts (use the receiver's LAN, tailnet or public address)", ip, g.what)
	case httpkit.IsInternalAddr(ip):
		return fmt.Errorf("%s is a link-local, multicast or cloud metadata address; %s can't go there", ip, g.what)
	case g.private && firewall.Blocked(ip):
		return fmt.Errorf("%s is a private address (LAN, tailnet or container network); %s go only to public addresses, so use the receiver's public address", ip, g.what)
	}
	return nil
}

// hostErr checks a destination's host when it is added: a literal
// address (or localhost) that addrErr refuses. Names are checked when
// connecting.
func (g *egressGuard) hostErr(host string) error {
	host = strings.ToLower(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		host = "127.0.0.1"
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return g.addrErr(ip)
	}
	return nil
}

// control is a net.Dialer's Control: it runs for every address a dial
// tries, after DNS, and refuses those addrErr refuses.
func (g *egressGuard) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return err
	}
	return g.addrErr(ap.Addr())
}
