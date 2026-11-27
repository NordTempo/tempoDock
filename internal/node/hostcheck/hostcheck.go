// Package hostcheck reads a server's host for security problems (ADR
// 0109): what node.HostChecker.CheckHost returns. Every check only reads,
// as the unprivileged user the daemon runs as: config files, /proc, the
// host's running containers and a simulated apt upgrade over the package
// lists the host's own apt timer keeps fresh. Nothing changes the host or
// uses the network.
package hostcheck

import (
	"context"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Checker runs the checks on one host. The zero value checks nothing
// useful: build it with the host's root and Dock's ports.
type Checker struct {
	// Root is the host's filesystem root, "/" on a server. Tests point it
	// at a directory laid out like one.
	Root string
	// DockPorts are the TCP ports Dock itself listens on or has Caddy
	// listen on (the API, the node link, HTTP and HTTPS, the tunnel
	// listener, the registry): open on purpose, so never a finding.
	DockPorts []int
	// Containers lists the host's running containers, Dock's and
	// anyone else's; nil skips the container checks.
	Containers func(ctx context.Context) ([]Container, error)
	// FirewallApplied reports whether Dock's app firewall is installed
	// (firewall.Applied); nil skips the check.
	FirewallApplied func() (bool, error)
	// Apt runs `apt-get --simulate dist-upgrade` and returns its output;
	// nil runs the host's apt-get.
	Apt func(ctx context.Context) ([]byte, error)
	// AptConfig reads one APT setting ("" when unset); nil runs the
	// host's apt-config.
	AptConfig func(ctx context.Context, key string) (string, error)
	// Now is the clock for ages (a reboot waiting); nil is time.Now.
	Now func() time.Time
}

// Container is a running container as the checks see it.
type Container struct {
	Name string
	// Dock is a container Dock manages (labelled dock.managed).
	Dock bool
	// Mounts are the host paths mounted into it.
	Mounts []string
	// Ports are the host addresses it is published on.
	Ports []netip.AddrPort
}

// CheckHost implements node.HostChecker.
func (c Checker) CheckHost(ctx context.Context) ([]node.HostCheck, error) {
	var containers []Container
	var containersErr error
	if c.Containers != nil {
		containers, containersErr = c.Containers(ctx)
	}
	ssh := c.readSSH()
	var out []node.HostCheck
	out = append(out, ssh.checks()...)
	out = append(out, c.openPorts(ssh, containers, containersErr)...)
	out = append(out, c.dockerTCP())
	if c.Containers != nil {
		out = append(out, dockerSocket(containers, containersErr)...)
	}
	out = append(out, c.packages(ctx)...)
	out = append(out, c.hostFirewall(ssh))
	if c.FirewallApplied != nil {
		out = append(out, appFirewall(c.FirewallApplied))
	}
	return out, ctx.Err()
}

// path is p on the host.
func (c Checker) path(p string) string { return filepath.Join(c.Root, p) }

// read reads a file on the host.
func (c Checker) read(p string) ([]byte, error) { return os.ReadFile(c.path(p)) }

// exists reports whether p is on the host.
func (c Checker) exists(p string) bool {
	_, err := os.Stat(c.path(p))
	return err == nil
}

// stat stats p on the host.
func (c Checker) stat(p string) (fs.FileInfo, error) { return os.Stat(c.path(p)) }

// now is the checker's clock.
func (c Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// pass is a check that found nothing.
func pass(check, title string) node.HostCheck {
	return node.HostCheck{Check: check, Status: node.HostPass, Title: title}
}

// unknown is a check that couldn't tell.
func unknown(check, title, detail string) node.HostCheck {
	return node.HostCheck{Check: check, Status: node.HostUnknown, Title: title, Detail: detail}
}

// skipped is a check that doesn't apply.
func skipped(check, title string) node.HostCheck {
	return node.HostCheck{Check: check, Status: node.HostSkipped, Title: title}
}

// finding is something to fix.
func finding(check, subject, severity, title, detail, fix string) node.HostCheck {
	return node.HostCheck{Check: check, Subject: subject, Status: node.HostFinding, Severity: severity, Title: title, Detail: detail, Fix: fix}
}
