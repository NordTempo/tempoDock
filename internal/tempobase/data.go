package tempobase

import (
	"context"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbstore"
)

// The data panel over saved connections (ADR 0024): each operation
// resolves the connection's name to a datapanel.Database for the caller
// (invisible connections are not found; the permission comes from
// Decide) and calls the shared Panel, which checks the level it needs,
// talks to the database and audits. The API and the MCP data tools call
// these through datapanel.Ops.

var _ datapanel.Ops[Actor, string] = (*App)(nil)

// dataDB loads the connection named name as the panel's Database, with
// act's permission there: two queries.
func (a *App) dataDB(ctx context.Context, act Actor, name string) (*datapanel.Database[*tbstore.Connection], error) {
	c, perm, err := a.connFor(ctx, act, name)
	if err != nil {
		return nil, err
	}
	return &datapanel.Database[*tbstore.Connection]{
		ID: c.ID, Name: c.Name, Engine: apitypes.DataEngine(c.Engine), DetectedBy: apitypes.DetectedBySetting,
		ConfirmName: c.Name, Perm: perm, Host: &c,
	}, nil
}

// DataInfo returns a connection's panel state and the caller's access.
// The database is contacted only for callers with level schema or admins.
func (a *App) DataInfo(ctx context.Context, act Actor, name string) (apitypes.DataInfo, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return apitypes.DataInfo{}, err
	}
	info, err := a.panel.Info(ctx, act.panelActor(), db)
	if info.UsedBy == nil {
		info.UsedBy = []string{}
	}
	return info, err
}

// SetupData creates or rotates tempoBase's roles in the database as the
// URL's user and syncs their grants. Admin only.
func (a *App) SetupData(ctx context.Context, act Actor, name string) (apitypes.DataInfo, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return apitypes.DataInfo{}, err
	}
	info, err := a.panel.Setup(ctx, act.panelActor(), db)
	if info.UsedBy == nil {
		info.UsedBy = []string{}
	}
	return info, err
}

// DataSchema returns the database's structure. Needs level schema.
func (a *App) DataSchema(ctx context.Context, act Actor, name string) (apitypes.DataSchema, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return apitypes.DataSchema{}, err
	}
	return a.panel.Schema(ctx, act.panelActor(), db)
}

// DataRows returns one keyset page of a table. Needs level read.
func (a *App) DataRows(ctx context.Context, act Actor, name string, req apitypes.DataPageRequest) (apitypes.DataPage, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return apitypes.DataPage{}, err
	}
	return a.panel.Rows(ctx, act.panelActor(), db, req)
}

// DataKeys returns one page of a key-value database's keys. Needs level
// read; tempoBase's connections are Postgres, so the panel refuses it.
func (a *App) DataKeys(ctx context.Context, act Actor, name string, req apitypes.DataKeysRequest) (apitypes.DataKeyPage, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return apitypes.DataKeyPage{}, err
	}
	return a.panel.Keys(ctx, act.panelActor(), db, req)
}

// DataKey returns one key and a page of its value (see DataKeys).
func (a *App) DataKey(ctx context.Context, act Actor, name string, req apitypes.DataKeyRequest) (apitypes.DataKeyValue, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return apitypes.DataKeyValue{}, err
	}
	return a.panel.Key(ctx, act.panelActor(), db, req)
}

// DataReferences returns the rows referencing one row. Needs level read.
func (a *App) DataReferences(ctx context.Context, act Actor, name string, req apitypes.DataReferencesRequest) ([]apitypes.DataReferences, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return nil, err
	}
	return a.panel.References(ctx, act.panelActor(), db, req)
}

// DataQuery runs one read-only statement, streamed as frames and audited
// as data.query. Needs level read.
func (a *App) DataQuery(ctx context.Context, act Actor, name string, req apitypes.DataQueryRequest) (datapanel.Stream[apitypes.DataQueryFrame], error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return nil, err
	}
	return a.panel.Query(ctx, act.panelActor(), db, req)
}

// DataHistory returns the caller's own queries on the connection, newest
// first, before an audit ID (0: from the newest). Needs level read.
func (a *App) DataHistory(ctx context.Context, act Actor, name string, before int64, limit int) ([]apitypes.DataHistoryEntry, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return nil, err
	}
	return a.panel.History(ctx, act.panelActor(), db, before, limit)
}

// ListSavedQueries returns the person's saved queries for the connection.
// Tokens have none.
func (a *App) ListSavedQueries(ctx context.Context, act Actor, name string) ([]apitypes.DataSavedQuery, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return nil, err
	}
	return a.panel.SavedQueries(ctx, act.panelActor(), db)
}

// SaveDataQuery creates (id 0) or replaces one of the person's saved
// queries.
func (a *App) SaveDataQuery(ctx context.Context, act Actor, name string, id int64, req apitypes.SaveDataQueryRequest) (apitypes.DataSavedQuery, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return apitypes.DataSavedQuery{}, err
	}
	return a.panel.SaveQuery(ctx, act.panelActor(), db, id, req)
}

// DeleteSavedQuery deletes one of the person's saved queries.
func (a *App) DeleteSavedQuery(ctx context.Context, act Actor, name string, id int64) error {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return err
	}
	return a.panel.DeleteSavedQuery(ctx, act.panelActor(), db, id)
}

// ListDataChangesets returns the connection's changesets, newest first;
// status "" means all. Needs level read.
func (a *App) ListDataChangesets(ctx context.Context, act Actor, name string, status apitypes.DataChangesetStatus) ([]apitypes.DataChangeset, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return nil, err
	}
	return a.panel.Changesets(ctx, act.panelActor(), db, status)
}

// GetDataChangeset returns one changeset. Needs level read.
func (a *App) GetDataChangeset(ctx context.Context, act Actor, name string, id int64) (apitypes.DataChangeset, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	return a.panel.Changeset(ctx, act.panelActor(), db, id)
}

// CreateDataChangeset previews (dry run) or stores a changeset as
// pending. Needs level propose.
func (a *App) CreateDataChangeset(ctx context.Context, act Actor, name string, req apitypes.DataChangesetRequest) (apitypes.DataChangeset, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	return a.panel.CreateChangeset(ctx, act.panelActor(), db, req)
}

// CommitDataChangeset commits a pending changeset: one's own with level
// write, anyone's (approving) as admin. On a production connection
// req.Confirm must be the connection's name.
func (a *App) CommitDataChangeset(ctx context.Context, act Actor, name string, id int64, req apitypes.DataCommitRequest) (apitypes.DataChangeset, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	return a.panel.Commit(ctx, act.panelActor(), db, id, req)
}

// RejectDataChangeset rejects a pending changeset: admins, or its author
// withdrawing it.
func (a *App) RejectDataChangeset(ctx context.Context, act Actor, name string, id int64, req apitypes.DataRejectRequest) (apitypes.DataChangeset, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	return a.panel.Reject(ctx, act.panelActor(), db, id, req)
}

// SetSensitiveColumns replaces the connection's sensitive columns and
// syncs the roles' grants when it can. Admin only.
func (a *App) SetSensitiveColumns(ctx context.Context, act Actor, name string, req apitypes.SetSensitiveColumnsRequest) ([]apitypes.DataColumnRef, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return nil, err
	}
	return a.panel.SetSensitiveColumns(ctx, act.panelActor(), db, req)
}

// DataInsights reads how the connection's database is doing. Overview,
// tables and indexes need level schema; queries and sessions read.
func (a *App) DataInsights(ctx context.Context, act Actor, name string, req apitypes.DataInsightsRequest) (apitypes.DataInsights, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return apitypes.DataInsights{}, err
	}
	return a.panel.Insights(ctx, act.panelActor(), db, req)
}

// CancelDataSession cancels a session's query or ends the session (level
// write; a production connection confirms ending one with its name).
func (a *App) CancelDataSession(ctx context.Context, act Actor, name, session string, req apitypes.DataCancelRequest) (apitypes.DataCancelResult, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return apitypes.DataCancelResult{}, err
	}
	return a.panel.CancelSession(ctx, act.panelActor(), db, session, req)
}

// ResetDataQueryStats sets the connection's query stats back to zero.
// Admins.
func (a *App) ResetDataQueryStats(ctx context.Context, act Actor, name string) error {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return err
	}
	return a.panel.ResetQueryStats(ctx, act.panelActor(), db)
}

// EnableDataQueryStats creates the query stats extension when the server
// loads it. Admins; a server that doesn't load it says what to change.
func (a *App) EnableDataQueryStats(ctx context.Context, act Actor, name string) (apitypes.DataQueryStatsResult, error) {
	db, err := a.dataDB(ctx, act, name)
	if err != nil {
		return apitypes.DataQueryStatsResult{}, err
	}
	return a.panel.EnableQueryStats(ctx, act.panelActor(), db)
}
