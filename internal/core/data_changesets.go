package core

import (
	"context"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Changesets (ADR 0016 section 5), as datapanel.Panel implements them:
// previewed, stored as pending, committed by their author with level write
// or approved by an admin, or rejected. Production commits confirm with
// the environment's name.

// CreateDataChangeset previews (dry run) or stores a changeset as pending.
// Needs level propose; audited as data.propose.
func (c *Core) CreateDataChangeset(ctx context.Context, a Actor, ref ServiceRef, req apitypes.DataChangesetRequest) (apitypes.DataChangeset, error) {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	return c.panel.CreateChangeset(ctx, panelActor(a), db, req)
}

// ListDataChangesets returns the database's changesets, newest first (at
// most 200); status "" means all. Needs level read.
func (c *Core) ListDataChangesets(ctx context.Context, a Actor, ref ServiceRef, status apitypes.DataChangesetStatus) ([]apitypes.DataChangeset, error) {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return nil, err
	}
	return c.panel.Changesets(ctx, panelActor(a), db, status)
}

// GetDataChangeset returns one changeset. Needs level read.
func (c *Core) GetDataChangeset(ctx context.Context, a Actor, ref ServiceRef, id int64) (apitypes.DataChangeset, error) {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	return c.panel.Changeset(ctx, panelActor(a), db, id)
}

// CommitDataChangeset commits a pending changeset: one's own with level
// write, anyone's (approving) as admin. Production needs req.Confirm to be
// the environment's name. Audited as data.commit and, for someone else's,
// data.approve.
func (c *Core) CommitDataChangeset(ctx context.Context, a Actor, ref ServiceRef, id int64, req apitypes.DataCommitRequest) (apitypes.DataChangeset, error) {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	return c.panel.Commit(ctx, panelActor(a), db, id, req)
}

// RejectDataChangeset rejects a pending changeset: admins, or its author
// withdrawing it. Audited as data.reject.
func (c *Core) RejectDataChangeset(ctx context.Context, a Actor, ref ServiceRef, id int64, req apitypes.DataRejectRequest) (apitypes.DataChangeset, error) {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	return c.panel.Reject(ctx, panelActor(a), db, id, req)
}
