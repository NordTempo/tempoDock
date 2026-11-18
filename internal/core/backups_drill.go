package core

import (
	"context"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Restore drills (ADR 0096): a template database's newest snapshot
// restored into a throwaway service, queried, and removed.

// drillPass runs every restore drill that is due (verifyDue), one at a
// time, and returns when the next one is due (zero: none). force runs
// every database's drill now. The backup scheduler's pass calls it after
// the repository checks.
func (c *Core) drillPass(ctx context.Context, set store.BackupSetting, force bool) (time.Time, error) {
	return time.Time{}, nil
}

// RunBackupDrill starts a restore drill of a template database now
// (operators) and answers with its running row; it finishes in the
// background.
func (c *Core) RunBackupDrill(ctx context.Context, a Actor, ref ServiceRef) (apitypes.BackupCheck, error) {
	return apitypes.BackupCheck{}, invalid("restore drills aren't built yet")
}

// latestDrill is a service's latest restore drill, nil if it never had
// one.
func (c *Core) latestDrill(ctx context.Context, ss svcScope) (*apitypes.BackupCheck, error) {
	return nil, nil
}
