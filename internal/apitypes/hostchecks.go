package apitypes

import "time"

// Host checks (ADR 0109): read-only security checks of each server's
// host, for platform operators and viewers only.

// HostCheckStatus is how one check came out.
type HostCheckStatus string

// Check statuses.
const (
	HostCheckPass    HostCheckStatus = "pass"
	HostCheckFinding HostCheckStatus = "finding"
	HostCheckUnknown HostCheckStatus = "unknown"
	HostCheckSkipped HostCheckStatus = "skipped"
)

// HostCheckSeverity is how much a finding matters.
type HostCheckSeverity string

// Finding severities. Warning and critical findings raise a platform
// alert; info ones only show.
const (
	HostCheckInfo     HostCheckSeverity = "info"
	HostCheckWarning  HostCheckSeverity = "warning"
	HostCheckCritical HostCheckSeverity = "critical"
)

// HostCheck is one result of a server's host check.
type HostCheck struct {
	// Key names the finding for as long as it lasts: the check, then its
	// subject after a colon where it can have several. Dismiss takes it.
	Key   string `json:"key" example:"open_port:5432"`
	Check string `json:"check" doc:"ssh_password, ssh_root, open_port, docker_tcp, docker_socket, security_updates, reboot_required, auto_updates, host_firewall, app_firewall; more may be added" example:"open_port"`
	// Subject is the port of open_port, the container of docker_socket.
	Subject  string            `json:"subject,omitempty" example:"5432"`
	Status   HostCheckStatus   `json:"status" enum:"pass,finding,unknown,skipped"`
	Severity HostCheckSeverity `json:"severity,omitempty" enum:"info,warning,critical"`
	// Title says what was found (or that the check passed), Detail where
	// and why it matters, Fix what to run or change.
	Title  string `json:"title" example:"Port 5432 is open on every address"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
	// FirstSeenAt is when the check first found this, for a finding.
	FirstSeenAt *time.Time `json:"first_seen_at,omitempty"`
	// Dismissed is set when an operator dismissed the finding.
	Dismissed *HostCheckDismissal `json:"dismissed,omitempty"`
}

// HostCheckDismissal is who dismissed a finding, when and why.
type HostCheckDismissal struct {
	Reason string    `json:"reason"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
}

// HostCheckCounts are a server's open findings (not dismissed) by
// severity.
type HostCheckCounts struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

// HostChecks is a server's last host check.
type HostChecks struct {
	Server string `json:"server"`
	// CheckedAt is when the last check ran; nil before the first.
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	// Error is why the last check failed (the server offline, an older
	// dock); Checks are then from the check before it.
	Error  string          `json:"error,omitempty"`
	Counts HostCheckCounts `json:"counts"`
	// Checks are the results, findings first, worst first.
	Checks []HostCheck `json:"checks"`
}

// DismissHostFindingRequest dismisses one of a server's findings.
type DismissHostFindingRequest struct {
	Finding string `json:"finding" minLength:"1" maxLength:"300" doc:"The finding's key, as a host check lists it" example:"host_firewall"`
	Reason  string `json:"reason" minLength:"1" maxLength:"500" doc:"Why it doesn't need fixing; other operators see it" example:"Hetzner's firewall allows 22, 80 and 443 only"`
}

// RestoreHostFindingRequest undoes a dismissal.
type RestoreHostFindingRequest struct {
	Finding string `json:"finding" minLength:"1" maxLength:"300" example:"host_firewall"`
}
