package core

import (
	"context"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Database panel (ADR 0016). These are the operations the API and CLI call;
// the bodies land in phase 3 (Core track) and phase 4 (Editing track).
// Every operation checks the actor's effective data permission (the data
// access policy, the token's override and its caps) before touching the
// database, and audits what it did.

// ErrDataNotImplemented is returned by database panel operations that
// aren't built yet.
var ErrDataNotImplemented error = &Error{Code: apitypes.CodeValidation, Msg: "the database panel is not available yet"}

// DataInfo returns a database service's panel state and the caller's
// access. Services that aren't databases are not_found.
func (c *Core) DataInfo(ctx context.Context, a Actor, ref ServiceRef) (apitypes.DataInfo, error) {
	return apitypes.DataInfo{}, ErrDataNotImplemented
}

// UpdateDatabaseSettings changes a service's database settings, which
// apply immediately. Needs deploy, like other service settings.
func (c *Core) UpdateDatabaseSettings(ctx context.Context, a Actor, ref ServiceRef, req apitypes.UpdateDatabaseSettingsRequest) (apitypes.Service, error) {
	return apitypes.Service{}, ErrDataNotImplemented
}

// SetupData creates or rotates Dock's roles in the database with the
// owner's credentials ("Set up data access"). Admin only.
func (c *Core) SetupData(ctx context.Context, a Actor, ref ServiceRef) (apitypes.DataInfo, error) {
	return apitypes.DataInfo{}, ErrDataNotImplemented
}

// DataSchema returns the database's structure. Needs level schema.
func (c *Core) DataSchema(ctx context.Context, a Actor, ref ServiceRef) (apitypes.DataSchema, error) {
	return apitypes.DataSchema{}, ErrDataNotImplemented
}

// DataRows returns one keyset page of a table. Needs level read.
func (c *Core) DataRows(ctx context.Context, a Actor, ref ServiceRef, req apitypes.DataPageRequest) (apitypes.DataPage, error) {
	return apitypes.DataPage{}, ErrDataNotImplemented
}

// DataReferences returns the rows referencing one row. Needs level read.
func (c *Core) DataReferences(ctx context.Context, a Actor, ref ServiceRef, req apitypes.DataReferencesRequest) ([]apitypes.DataReferences, error) {
	return nil, ErrDataNotImplemented
}

// DataQuery runs one read-only statement, streamed as frames. Needs level
// read and, until masking is enforced in Postgres, sensitive visible.
func (c *Core) DataQuery(ctx context.Context, a Actor, ref ServiceRef, req apitypes.DataQueryRequest) (Stream[apitypes.DataQueryFrame], error) {
	return nil, ErrDataNotImplemented
}

// DataHistory returns the caller's own queries on the database, newest
// first, before the given audit ID (0: from the newest).
func (c *Core) DataHistory(ctx context.Context, a Actor, ref ServiceRef, before int64, limit int) ([]apitypes.DataHistoryEntry, error) {
	return nil, ErrDataNotImplemented
}

// ListDataChangesets returns the database's changesets, newest first;
// status "" means all.
func (c *Core) ListDataChangesets(ctx context.Context, a Actor, ref ServiceRef, status apitypes.DataChangesetStatus) ([]apitypes.DataChangeset, error) {
	return nil, ErrDataNotImplemented
}

// GetDataChangeset returns one changeset.
func (c *Core) GetDataChangeset(ctx context.Context, a Actor, ref ServiceRef, id int64) (apitypes.DataChangeset, error) {
	return apitypes.DataChangeset{}, ErrDataNotImplemented
}

// CreateDataChangeset previews (dry run) or stores a changeset as pending.
// Needs level propose.
func (c *Core) CreateDataChangeset(ctx context.Context, a Actor, ref ServiceRef, req apitypes.DataChangesetRequest) (apitypes.DataChangeset, error) {
	return apitypes.DataChangeset{}, ErrDataNotImplemented
}

// CommitDataChangeset revalidates and commits a pending changeset: one's
// own with level write, anyone's (approving) as admin. Production needs
// req.Confirm to be the environment's name.
func (c *Core) CommitDataChangeset(ctx context.Context, a Actor, ref ServiceRef, id int64, req apitypes.DataCommitRequest) (apitypes.DataChangeset, error) {
	return apitypes.DataChangeset{}, ErrDataNotImplemented
}

// RejectDataChangeset rejects a pending changeset: admins, or its author
// withdrawing it.
func (c *Core) RejectDataChangeset(ctx context.Context, a Actor, ref ServiceRef, id int64, req apitypes.DataRejectRequest) (apitypes.DataChangeset, error) {
	return apitypes.DataChangeset{}, ErrDataNotImplemented
}

// ListSavedQueries returns the caller's saved queries for the database.
func (c *Core) ListSavedQueries(ctx context.Context, a Actor, ref ServiceRef) ([]apitypes.DataSavedQuery, error) {
	return nil, ErrDataNotImplemented
}

// SaveDataQuery creates (id 0) or replaces one of the caller's saved
// queries.
func (c *Core) SaveDataQuery(ctx context.Context, a Actor, ref ServiceRef, id int64, req apitypes.SaveDataQueryRequest) (apitypes.DataSavedQuery, error) {
	return apitypes.DataSavedQuery{}, ErrDataNotImplemented
}

// DeleteSavedQuery deletes one of the caller's saved queries.
func (c *Core) DeleteSavedQuery(ctx context.Context, a Actor, ref ServiceRef, id int64) error {
	return ErrDataNotImplemented
}

// SetSensitiveColumns replaces the database's sensitive columns. Admin
// only.
func (c *Core) SetSensitiveColumns(ctx context.Context, a Actor, ref ServiceRef, req apitypes.SetSensitiveColumnsRequest) ([]apitypes.DataColumnRef, error) {
	return nil, ErrDataNotImplemented
}

// GetDataPolicy returns the data access policy at one scope: the default
// for an empty ref, a project's for ref.Project alone, an environment's
// for both.
func (c *Core) GetDataPolicy(ctx context.Context, a Actor, ref EnvRef) (apitypes.DataPolicy, error) {
	return apitypes.DataPolicy{}, ErrDataNotImplemented
}

// SetDataPolicy replaces the rules at one scope (see GetDataPolicy).
// Admin only.
func (c *Core) SetDataPolicy(ctx context.Context, a Actor, ref EnvRef, req apitypes.UpdateDataPolicyRequest) (apitypes.DataPolicy, error) {
	return apitypes.DataPolicy{}, ErrDataNotImplemented
}

// UpdateToken changes a token's data access override. Admin only.
func (c *Core) UpdateToken(ctx context.Context, a Actor, id int64, req apitypes.UpdateTokenRequest) (apitypes.Token, error) {
	return apitypes.Token{}, ErrDataNotImplemented
}

// GetAuditDetail returns an audit event's detail, to anyone who may see the
// event.
func (c *Core) GetAuditDetail(ctx context.Context, a Actor, id int64) (apitypes.AuditDetail, error) {
	return apitypes.AuditDetail{}, ErrDataNotImplemented
}
