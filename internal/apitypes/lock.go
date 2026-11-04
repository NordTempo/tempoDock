package apitypes

import (
	"encoding/json"
	"time"
)

// Locked environments (ADR 0044): tokens can't change anything in a
// locked environment; what they try becomes an access request an admin
// approves in the UI.

// EnvironmentLock is a locked environment's lock.
type EnvironmentLock struct {
	LockedAt time.Time `json:"locked_at"`
	LockedBy string    `json:"locked_by"`
	Reason   string    `json:"reason,omitempty"`
	// OpenUntil is set while the environment is opened for a while: until
	// then tokens act in it as if it were unlocked.
	OpenUntil *time.Time `json:"open_until,omitempty"`
}

// Holds reports whether the lock applies at t (it isn't opened then).
func (l *EnvironmentLock) Holds(t time.Time) bool {
	return l != nil && (l.OpenUntil == nil || !t.Before(*l.OpenUntil))
}

// LockEnvironmentRequest locks an environment.
type LockEnvironmentRequest struct {
	Reason string `json:"reason,omitempty" maxLength:"200" doc:"Why it is locked, shown with the lock"`
}

// UnlockEnvironmentRequest unlocks an environment, or opens it for a while.
type UnlockEnvironmentRequest struct {
	Minutes int `json:"minutes,omitempty" minimum:"0" maximum:"10080" doc:"Open the locked environment for this many minutes, then lock it again by itself. 0 unlocks it."`
}

// OrgLockSettings are an org's settings for locked environments.
type OrgLockSettings struct {
	LockProduction bool `json:"lock_production" doc:"Lock new production environments as they are created"`
	// RequestTTLHours is how long an access request waits for a decision.
	RequestTTLHours int `json:"request_ttl_hours,omitempty" minimum:"0" maximum:"720" doc:"Hours an access request waits for a decision before it expires (1 to 720). Default 24; 0 keeps the current value."`
}

// AccessRequestStatus is where an access request is.
type AccessRequestStatus string

// Access request statuses: pending → approved (running) → done or failed;
// pending → rejected or expired.
const (
	RequestPending  AccessRequestStatus = "pending"
	RequestApproved AccessRequestStatus = "approved"
	RequestRejected AccessRequestStatus = "rejected"
	RequestExpired  AccessRequestStatus = "expired"
	RequestDone     AccessRequestStatus = "done"
	RequestFailed   AccessRequestStatus = "failed"
)

// Decided reports whether nothing more will happen to the request.
func (s AccessRequestStatus) Decided() bool { return s != RequestPending && s != RequestApproved }

// AccessRequest is a token's change that a locked environment refused,
// waiting for an admin (ADR 0044). Approved, it runs again as the token.
type AccessRequest struct {
	ID          int64               `json:"id"`
	Status      AccessRequestStatus `json:"status" enum:"pending,approved,rejected,expired,done,failed"`
	Project     string              `json:"project"`
	Environment string              `json:"environment"`
	// Via is how it was asked and runs again: the API (and so the CLI)
	// or MCP. Operation is the API's operation ID or MCP's tool name;
	// Title is its name for people ("Set variables").
	Via       string `json:"via" enum:"api,mcp"`
	Operation string `json:"operation"`
	Title     string `json:"title"`
	// Summary is one line: "token claude wants to set variables in
	// shop/production".
	Summary     string     `json:"summary"`
	Reason      string     `json:"reason,omitempty"`
	RequestedBy string     `json:"requested_by"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	DecidedBy   string     `json:"decided_by,omitempty"`
	DecidedAt   *time.Time `json:"decided_at,omitempty"`
	Comment     string     `json:"comment,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	// Error is why a run failed; Result what a done run returned.
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	// Args are the call's arguments, exactly as they will run. Admins
	// only: they can hold variable values.
	Args json.RawMessage `json:"args,omitempty"`
	// ChangesSince counts the environment's audited changes after the
	// request was asked (single requests only).
	ChangesSince *int `json:"changes_since,omitempty"`
}

// DecideAccessRequest approves or rejects a request.
type DecideAccessRequest struct {
	Comment string `json:"comment,omitempty" maxLength:"500" doc:"Shown to the requester and in the audit log"`
}
