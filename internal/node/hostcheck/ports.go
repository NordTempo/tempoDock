package hostcheck

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/NordTempo/tempoDock/internal/node"
)

// maxPortFindings caps open_port's findings: a host with more has bigger
// problems than a list shows.
const maxPortFindings = 50

// dockerAPIPorts are Docker's API ports: docker_tcp reports them, so
// open_port leaves them out.
var dockerAPIPorts = []int{2375, 2376}

// listener is one listening TCP socket.
type listener struct {
	addr netip.Addr
	port int
}

// listeners reads the host's listening TCP sockets from /proc/net/tcp and
// tcp6. The daemon runs in the host's network namespace, so these are the
// host's.
func (c Checker) listeners() ([]listener, error) {
	var out []listener
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := c.read(f)
		if isNotExist(err) && f == "/proc/net/tcp6" {
			continue // IPv6 is off
		}
		if err != nil {
			return nil, err
		}
		out = append(out, parseProcNet(data)...)
	}
	return out, nil
}

// parseProcNet reads the LISTEN sockets of a /proc/net/tcp{,6} table.
func parseProcNet(data []byte) []listener {
	var out []listener
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Scan() // the header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[3] != "0A" { // 0A: TCP_LISTEN
			continue
		}
		host, portHex, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		port, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil {
			continue
		}
		addr, ok := procAddr(host)
		if !ok {
			continue
		}
		out = append(out, listener{addr: addr, port: int(port)})
	}
	return out
}

// procAddr decodes /proc/net's address: 32-bit words in host (little
// endian) order.
func procAddr(h string) (netip.Addr, bool) {
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 4 && len(b) != 16 {
		return netip.Addr{}, false
	}
	for i := 0; i < len(b); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	a, _ := netip.AddrFromSlice(b)
	return a.Unmap(), true
}

// tailnet holds Tailscale's addresses: 100.64.0.0/10 (CGNAT) and its
// IPv6 range, fd7a:115c:a1e0::/48. Only the tailnet reaches them.
var tailnet = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("fd7a:115c:a1e0::/48")}

// onTailnet reports whether a is a Tailscale address.
func onTailnet(a netip.Addr) bool {
	return slices.ContainsFunc(tailnet, func(p netip.Prefix) bool { return p.Contains(a) })
}

// public reports whether a socket bound to a can be reached from the
// internet directly: every address, or a global one that isn't private.
// A private address (RFC 1918, ULA) may be too, where the provider maps a
// public address to it (EC2, GCP): openPorts reports those as info.
// Tailscale's, loopback and link-local addresses can't.
func public(a netip.Addr) bool {
	if a.IsUnspecified() {
		return true
	}
	return a.IsGlobalUnicast() && !a.IsPrivate() && !onTailnet(a)
}

// publishes reports whether a container's publication p is listener l:
// the same port on the same address, an unspecified address standing for
// both families (Docker lists 0.0.0.0 and :: for one -p).
func publishes(p netip.AddrPort, l listener) bool {
	if int(p.Port()) != l.port {
		return false
	}
	a := p.Addr().Unmap()
	return a == l.addr || a.IsUnspecified() && l.addr.IsUnspecified()
}

// openPorts is open_port: a finding per TCP port listening on a public
// address that neither Dock nor sshd opened.
func (c Checker) openPorts(ssh sshSettings, containers []Container, containersErr error) []node.HostCheck {
	ls, err := c.listeners()
	if err != nil {
		return []node.HostCheck{unknown("open_port", "Can't tell which ports are open", "Dock can't read the host's sockets: "+err.Error())}
	}
	// Dock's config names its own ports, on whatever address they bind.
	known := slices.Concat(c.DockPorts, ssh.ports, dockerAPIPorts)
	// A container's publication is matched by address and port, so one
	// on loopback says nothing of another program on a public address.
	// publisher is the container a listener is the publication of.
	publisher := func(l listener) (Container, bool) {
		for _, ct := range containers {
			if slices.ContainsFunc(ct.Ports, func(p netip.AddrPort) bool { return publishes(p, l) }) {
				return ct, true
			}
		}
		return Container{}, false
	}
	// publishedBy names the container that published each reported port,
	// "" where some listener on it isn't that container's (another
	// container's or a program's); open says a port listens on a public
	// address, not only private ones.
	publishedBy := map[int]string{}
	mixed := map[int]bool{}
	open := map[int]bool{}
	bound := map[int][]string{}
	var ports []int
	for _, l := range ls {
		if !public(l.addr) && (!l.addr.IsPrivate() || onTailnet(l.addr)) || slices.Contains(known, l.port) {
			continue
		}
		ct, ok := publisher(l)
		if ok && ct.Dock {
			continue
		}
		switch prev, seen := publishedBy[l.port]; {
		case !ok || seen && prev != ct.Name || mixed[l.port]:
			mixed[l.port] = true
		case !seen && len(bound[l.port]) > 0:
			mixed[l.port] = true // an earlier listener was nobody's
		default:
			publishedBy[l.port] = ct.Name
		}
		if _, ok := bound[l.port]; !ok {
			ports = append(ports, l.port)
		}
		open[l.port] = open[l.port] || public(l.addr)
		if s := l.addr.String(); !slices.Contains(bound[l.port], s) {
			bound[l.port] = append(bound[l.port], s)
		}
	}
	// With Docker's userland proxy off, a published port has no listener
	// here (iptables forwards it): another container's publication on a
	// public address counts by itself.
	for _, ct := range containers {
		for _, p := range ct.Ports {
			port, a := int(p.Port()), p.Addr().Unmap()
			if ct.Dock || !public(a) || slices.Contains(known, port) || slices.Contains(bound[port], a.String()) {
				continue
			}
			if _, ok := bound[port]; !ok {
				ports = append(ports, port)
				publishedBy[port] = ct.Name
			} else if publishedBy[port] != ct.Name {
				mixed[port] = true
			}
			open[port] = true
			bound[port] = append(bound[port], a.String())
		}
	}
	if len(ports) == 0 {
		return []node.HostCheck{pass("open_port", "No port is open to the internet but SSH and Dock's")}
	}
	slices.Sort(ports)
	var out []node.HostCheck
	for _, p := range ports[:min(len(ports), maxPortFindings)] {
		on := "on " + strings.Join(bound[p], " and ")
		if slices.ContainsFunc(bound[p], func(a string) bool { return a == "0.0.0.0" || a == "::" }) {
			on = "on every address"
		}
		title := fmt.Sprintf("Port %d is open %s", p, on)
		severity := node.HostWarning
		detail := "Dock didn't open it, and a public address can reach it unless a firewall in front of the server blocks it."
		if !open[p] {
			severity = node.HostInfo
			title = fmt.Sprintf("Port %d is open on private address %s", p, strings.Join(bound[p], " and "))
			detail = "Dock didn't open it. The private network reaches it, and so does the internet where the provider maps a public address to this one (as EC2 and GCP do) and its firewall lets the port in."
		}
		fix := fmt.Sprintf("Find what listens with `sudo ss -tlnp 'sport = :%d'`, then stop it, bind it to 127.0.0.1 or block the port in a firewall", p)
		if name, ok := publishedBy[p]; ok && !mixed[p] {
			detail = "Published by container " + name + ", which Dock didn't start. Docker's published ports go past ufw and firewalld."
			fix = fmt.Sprintf("Publish it on 127.0.0.1 only (`-p 127.0.0.1:%d:...`), or remove the container if nothing needs it: `docker rm -f %s`", p, name)
		}
		out = append(out, finding("open_port", strconv.Itoa(p), severity, title, detail, fix))
	}
	if containersErr != nil {
		out[0].Detail += " (Dock couldn't list the host's containers to say which published it: " + containersErr.Error() + ")"
	}
	return out
}
