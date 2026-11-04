package apitypes

import "time"

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
}
