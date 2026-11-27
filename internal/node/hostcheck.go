package node

import "context"

// HostChecker reads a server's host for security problems (ADR 0109):
// SSH that takes passwords, ports open to the world that Dock didn't
// open, Docker's API on TCP, containers holding the Docker socket,
// security updates and reboots waiting, and missing firewalls.
type HostChecker interface {
	// CheckHost runs every check once and returns at least one result
	// per check it runs: a host with no containers to list or app
	// firewall to expect (outside systemd) leaves those checks out. It
	// only reads: files, /proc, one container list and a simulated apt
	// upgrade from the local package lists. It never
	// changes the host or uses the network, and takes seconds at most.
	// A check that can't tell is a result (HostUnknown), not an error.
	CheckHost(ctx context.Context) ([]HostCheck, error)
}

// HostCheckStatus is how one check came out.
type HostCheckStatus string

// Check statuses.
const (
	// HostPass: nothing to do.
	HostPass HostCheckStatus = "pass"
	// HostFinding: something to fix; Severity says how much it matters.
	HostFinding HostCheckStatus = "finding"
	// HostUnknown: the check couldn't read what it needs; Detail says why.
	HostUnknown HostCheckStatus = "unknown"
	// HostSkipped: the check doesn't apply here (no SSH server, not an
	// apt system).
	HostSkipped HostCheckStatus = "skipped"
)

// Severities of a finding.
const (
	HostInfo     = "info"
	HostWarning  = "warning"
	HostCritical = "critical"
)

// HostCheck is one result of CheckHost.
type HostCheck struct {
	// Check names the check: ssh_password, ssh_root, open_port,
	// docker_tcp, docker_socket, security_updates, reboot_required,
	// auto_updates, host_firewall, app_firewall.
	Check string
	// Subject tells a check's findings apart where it can have several:
	// the port of open_port, the container of docker_socket. Check and
	// Subject together name a finding for as long as it lasts.
	Subject string
	Status  HostCheckStatus
	// Severity of a finding: HostInfo, HostWarning or HostCritical.
	Severity string
	// Title says what was found (or that the check passed), Detail where
	// and why it matters, Fix what to run or change.
	Title, Detail, Fix string
}

// Key names the finding a result is about: Check, then Subject after a
// colon when there is one.
func (h HostCheck) Key() string {
	if h.Subject == "" {
		return h.Check
	}
	return h.Check + ":" + h.Subject
}
