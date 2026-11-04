package core

import (
	"context"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Account changes while environments are locked (ADR 0044). The lock stops
// tokens changing a locked environment; an admin or operator token could
// still escape it by managing accounts: creating a user or resetting a
// password and signing in to the UI as that person, inviting one, raising a
// member's role, or minting an admin token. While any environment holds its
// lock, those changes are for a person in the web UI. Changes that only
// take power away (revoking, disabling, removing, deploy and read tokens)
// stay open to tokens.

// checkAccountLock refuses a token action ("creating user accounts")
// while a locked environment holds: one in the actor's org, or in any
// org when platform is set (user accounts are the platform's, so a
// password reset reaches every org the user is in).
// People and the daemon pass; tokens cost one query.
func (c *Core) checkAccountLock(ctx context.Context, a Actor, action string, platform bool) error {
	if a.Kind != "token" {
		return nil
	}
	locks, err := c.store.ListEnvironmentLocks(ctx)
	if err != nil {
		return err
	}
	t := now()
	for _, l := range locks {
		if (platform || l.OrgID == a.OrgID) && lockHolds(store.Environment{LockedAt: l.LockedAt, OpenUntil: l.OpenUntil}, t) {
			return errf(apitypes.CodeForbidden, "%s while environments are locked is for a person signed in to the web UI, not %s; ask one, or ask an admin to unlock the environments", action, a)
		}
	}
	return nil
}
