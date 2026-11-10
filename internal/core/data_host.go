package core

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Dock as the database panel's host (ADR 0024): a database is a service,
// and the panel's state lives in Dock's store, keyed by the service ID.
// Core's data operations resolve a ServiceRef to a datapanel.Database
// (dataDB) and call c.panel; dataHost is what the panel calls back.

// svcData is the host handle of a database service: its scope, for
// dialing its container and auditing against its project and environment.
type svcData struct {
	ss svcScope
}

// dataPanel is Dock's database panel.
type dataPanel = datapanel.Panel[*svcData]

// newDataPanel builds the panel over Dock's store and node runtime.
func newDataPanel(c *Core) *dataPanel {
	return datapanel.New(datapanel.Options[*svcData]{
		Host: dataHost{c}, Engines: c.data, Box: c.box, Words: DataWords, Logger: c.log,
	})
}

// panelActor is the actor as the panel sees it.
func panelActor(a Actor) datapanel.Actor {
	return datapanel.Actor{Kind: a.Kind, Name: a.Name, UserID: a.UserID, TokenID: a.TokenID}
}

// dataDB loads a database service the actor can see, with its detection
// and the actor's permission. Services that aren't databases are
// not_found.
func (c *Core) dataDB(ctx context.Context, a Actor, ref ServiceRef) (*datapanel.Database[*svcData], error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "using the database panel")
	if err != nil {
		return nil, err
	}
	engine, by := detectDatabase(ss.svc)
	if engine == "" {
		return nil, notFound("%s is not a database; if it is Postgres or MySQL, set its database setting to postgres or mysql", ss)
	}
	perm, err := c.dataPermission(ctx, a, ss.envScope)
	if err != nil {
		return nil, err
	}
	return &datapanel.Database[*svcData]{
		ID: ss.svc.ID, Name: ss.String(), Engine: engine, DetectedBy: by,
		ConfirmName: ss.env.Name, Perm: perm, Host: &svcData{ss},
	}, nil
}

// dataHost implements datapanel.Host over Dock's store.
type dataHost struct{ c *Core }

var _ datapanel.Host[*svcData] = dataHost{}

// Connect derives the connection from the service's active release
// (dbConnection) and dials its container through the node runtime.
func (h dataHost) Connect(ctx context.Context, d *svcData) (datapanel.Conn, error) {
	conn, err := h.c.dbConnection(ctx, d.ss)
	if err != nil {
		return datapanel.Conn{}, err
	}
	return datapanel.Conn{Dial: h.c.dial(conn), Database: conn.database, Owner: conn.owner}, nil
}

func (h dataHost) Access(ctx context.Context, id int64) (*datapanel.Access, error) {
	acc, err := h.c.store.GetDataAccess(ctx, id)
	if store.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &datapanel.Access{
		Engine: apitypes.DataEngine(acc.Engine), ServerVersion: acc.ServerVersion, ReaderSecret: acc.ReaderSecret,
		WriterSecret: acc.WriterSecret, GrantsSyncedHash: acc.GrantsSyncedHash, SetUpAt: acc.SetUpAt, SetUpBy: acc.SetUpBy,
	}, nil
}

// PutAccess records a setup; the store resets the grants hash with it.
func (h dataHost) PutAccess(ctx context.Context, id int64, acc datapanel.Access) error {
	_, err := h.c.store.UpsertDataAccess(ctx, store.UpsertDataAccessParams{
		ServiceID: id, Engine: string(acc.Engine), ServerVersion: acc.ServerVersion,
		ReaderSecret: acc.ReaderSecret, WriterSecret: acc.WriterSecret, SetUpAt: acc.SetUpAt, SetUpBy: acc.SetUpBy,
	})
	return err
}

func (h dataHost) SetGrantsHash(ctx context.Context, id int64, hash string) error {
	return h.c.store.SetDataGrantsSyncedHash(ctx, store.SetDataGrantsSyncedHashParams{GrantsSyncedHash: hash, ServiceID: id})
}

func (h dataHost) SensitiveColumns(ctx context.Context, id int64) ([]apitypes.DataColumnRef, error) {
	rows, err := h.c.store.ListSensitiveColumns(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.DataColumnRef, len(rows))
	for i, r := range rows {
		out[i] = apitypes.DataColumnRef{Schema: r.SchemaName, Table: r.TableName, Column: r.ColumnName}
	}
	return out, nil
}

func (h dataHost) ReplaceSensitiveColumns(ctx context.Context, id int64, cols []apitypes.DataColumnRef) error {
	return h.c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteSensitiveColumns(ctx, id); err != nil {
			return err
		}
		for _, col := range cols {
			if err := q.AddSensitiveColumn(ctx, store.AddSensitiveColumnParams{ServiceID: id, SchemaName: col.Schema, TableName: col.Table, ColumnName: col.Column}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (h dataHost) SensitiveKeys(ctx context.Context, id int64) ([]string, error) {
	return h.c.store.ListSensitiveKeys(ctx, id)
}

func (h dataHost) ReplaceSensitiveKeys(ctx context.Context, id int64, patterns []string) error {
	return h.c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteSensitiveKeys(ctx, id); err != nil {
			return err
		}
		for _, p := range patterns {
			if err := q.AddSensitiveKey(ctx, store.AddSensitiveKeyParams{ServiceID: id, Pattern: p}); err != nil {
				return err
			}
		}
		return nil
	})
}

// SavedQueryOwner is the user, or the user a `dock login` token acts for.
// Other tokens have none and are told to log in.
func (h dataHost) SavedQueryOwner(ctx context.Context, a datapanel.Actor) (int64, error) {
	if a.UserID != 0 {
		return a.UserID, nil
	}
	if a.TokenID != 0 {
		t, err := h.c.store.GetToken(ctx, a.TokenID)
		if err != nil && !store.IsNotFound(err) {
			return 0, err
		}
		if err == nil && t.Origin == "cli_login" && t.UserID != nil {
			return *t.UserID, nil
		}
	}
	return 0, invalid("saved queries belong to users; log in with `dock login` to use them")
}

func toSavedQuery(q store.SavedQuery) apitypes.DataSavedQuery {
	return apitypes.DataSavedQuery{ID: q.ID, Name: q.Name, SQL: q.Sql, CreatedAt: q.CreatedAt, UpdatedAt: q.UpdatedAt}
}

func (h dataHost) SavedQueries(ctx context.Context, id, user int64) ([]apitypes.DataSavedQuery, error) {
	rows, err := h.c.store.ListSavedQueries(ctx, store.ListSavedQueriesParams{ServiceID: id, UserID: user})
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.DataSavedQuery, len(rows))
	for i, r := range rows {
		out[i] = toSavedQuery(r)
	}
	return out, nil
}

func (h dataHost) SaveQuery(ctx context.Context, id, user, queryID int64, name, sql string, at time.Time) (apitypes.DataSavedQuery, error) {
	var q store.SavedQuery
	var err error
	if queryID == 0 {
		q, err = h.c.store.SaveQuery(ctx, store.SaveQueryParams{ServiceID: id, UserID: user, Name: name, Sql: sql, Now: at})
	} else {
		q, err = h.c.store.UpdateSavedQuery(ctx, store.UpdateSavedQueryParams{Name: name, Sql: sql, Now: at, ID: queryID, ServiceID: id, UserID: user})
	}
	switch {
	case store.IsNotFound(err):
		return apitypes.DataSavedQuery{}, datapanel.ErrNotFound
	case store.IsUnique(err):
		return apitypes.DataSavedQuery{}, datapanel.ErrDuplicate
	case err != nil:
		return apitypes.DataSavedQuery{}, err
	}
	return toSavedQuery(q), nil
}

func (h dataHost) DeleteSavedQuery(ctx context.Context, id, user, queryID int64) (bool, error) {
	n, err := h.c.store.DeleteSavedQuery(ctx, store.DeleteSavedQueryParams{ID: queryID, ServiceID: id, UserID: user})
	return n > 0, err
}

// toChangeset decodes a stored changeset.
func toChangeset(cs store.Changeset) (datapanel.Changeset, error) {
	out := datapanel.Changeset{
		ID: cs.ID, Database: cs.ServiceID, Author: cs.Author, AuthorKind: cs.AuthorKind,
		Status: apitypes.DataChangesetStatus(cs.Status), Message: cs.Message, SQL: cs.Sql, Reason: cs.Reason,
		CreatedAt: cs.CreatedAt, ExpiresAt: cs.ExpiresAt, DecidedAt: cs.DecidedAt, DecidedBy: cs.DecidedBy,
	}
	if cs.UserID != nil {
		out.UserID = *cs.UserID
	}
	if cs.TokenID != nil {
		out.TokenID = *cs.TokenID
	}
	if err := json.Unmarshal([]byte(cs.Changes), &out.Changes); err != nil {
		return out, fmt.Errorf("changeset %d changes: %w", cs.ID, err)
	}
	if cs.RowsAffected != "" {
		if err := json.Unmarshal([]byte(cs.RowsAffected), &out.RowsAffected); err != nil {
			return out, fmt.Errorf("changeset %d rows: %w", cs.ID, err)
		}
	}
	if cs.Conflicts != "" {
		if err := json.Unmarshal([]byte(cs.Conflicts), &out.Conflicts); err != nil {
			return out, fmt.Errorf("changeset %d conflicts: %w", cs.ID, err)
		}
	}
	return out, nil
}

func (h dataHost) CreateChangeset(ctx context.Context, cs datapanel.Changeset) (datapanel.Changeset, error) {
	changes, err := json.Marshal(cs.Changes)
	if err != nil {
		return datapanel.Changeset{}, err
	}
	params := store.CreateChangesetParams{
		ServiceID: cs.Database, Author: cs.Author, AuthorKind: cs.AuthorKind, Message: cs.Message,
		Changes: string(changes), Sql: cs.SQL, CreatedAt: cs.CreatedAt, ExpiresAt: cs.ExpiresAt,
	}
	if cs.UserID != 0 {
		params.UserID = &cs.UserID
	}
	if cs.TokenID != 0 {
		params.TokenID = &cs.TokenID
	}
	row, err := h.c.store.CreateChangeset(ctx, params)
	if err != nil {
		return datapanel.Changeset{}, err
	}
	return toChangeset(row)
}

func (h dataHost) Changeset(ctx context.Context, id, changesetID int64) (datapanel.Changeset, error) {
	row, err := h.c.store.GetChangeset(ctx, store.GetChangesetParams{ID: changesetID, ServiceID: id})
	if store.IsNotFound(err) {
		return datapanel.Changeset{}, datapanel.ErrNotFound
	}
	if err != nil {
		return datapanel.Changeset{}, err
	}
	return toChangeset(row)
}

func (h dataHost) Changesets(ctx context.Context, id int64, pendingOnly bool, limit int) ([]datapanel.Changeset, error) {
	var rows []store.Changeset
	var err error
	if pendingOnly {
		rows, err = h.c.store.ListPendingChangesets(ctx, id)
		slices.Reverse(rows)
	} else {
		rows, err = h.c.store.ListChangesets(ctx, store.ListChangesetsParams{ServiceID: id, BeforeID: math.MaxInt64, MaxRows: int64(limit)})
	}
	if err != nil {
		return nil, err
	}
	out := make([]datapanel.Changeset, len(rows))
	for i, r := range rows {
		if out[i], err = toChangeset(r); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (h dataHost) DecideChangeset(ctx context.Context, d datapanel.Decision) (datapanel.Changeset, error) {
	params := store.DecideChangesetParams{ID: d.ID, Status: string(d.Status), DecidedAt: &d.DecidedAt, DecidedBy: d.DecidedBy, Reason: d.Reason}
	if d.RowsAffected != nil {
		b, err := json.Marshal(d.RowsAffected)
		if err != nil {
			return datapanel.Changeset{}, err
		}
		params.RowsAffected = string(b)
	}
	if len(d.Conflicts) > 0 {
		b, err := json.Marshal(d.Conflicts)
		if err != nil {
			return datapanel.Changeset{}, err
		}
		params.Conflicts = string(b)
	}
	row, err := h.c.store.DecideChangeset(ctx, params)
	if store.IsNotFound(err) {
		return datapanel.Changeset{}, datapanel.ErrNotFound
	}
	if err != nil {
		return datapanel.Changeset{}, err
	}
	return toChangeset(row)
}

func (h dataHost) ExpireChangesets(ctx context.Context, at time.Time) error {
	_, err := h.c.store.ExpireChangesets(ctx, at)
	return err
}

// Audit records a data event against the service's project and
// environment and publishes it; with a detail, both rows go in one
// transaction. Failures are logged.
func (h dataHost) Audit(ctx context.Context, pa datapanel.Actor, d *svcData, e datapanel.AuditEntry) {
	c, ss := h.c, d.ss
	a := Actor{Kind: pa.Kind, Name: pa.Name, UserID: pa.UserID, TokenID: pa.TokenID}
	if e.Detail == nil {
		c.audit(ctx, a, e.Action, &ss.project, &ss.env, ss.String(), e.Summary)
		return
	}
	params := store.AddAuditEventWithDetailParams{
		Actor: a.Name, ActorKind: a.Kind, Action: e.Action, OrgID: &ss.project.OrgID, ProjectID: &ss.project.ID, EnvironmentID: &ss.env.ID,
		Target: ss.String(), Summary: e.Summary, CreatedAt: now(),
	}
	if a.UserID != 0 {
		params.UserID = &a.UserID
	}
	if a.TokenID != 0 {
		params.TokenID = &a.TokenID
	}
	var row store.AuditEvent
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		id, err := q.AddAuditDetail(ctx, store.AddAuditDetailParams{
			Sql: e.Detail.SQL, Rows: e.Detail.Rows, DurationMs: e.Detail.Duration.Milliseconds(), Outcome: string(e.Detail.Outcome),
		})
		if err != nil {
			return err
		}
		params.DetailID = &id
		row, err = q.AddAuditEventWithDetail(ctx, params)
		return err
	})
	if err != nil {
		c.log.Error("write audit event", "action", e.Action, "err", err)
		return
	}
	ae := apitypes.AuditEvent{
		ID: row.ID, Actor: row.Actor, ActorKind: row.ActorKind, Action: row.Action, Project: ss.project.Name,
		Environment: ss.env.Name, Target: row.Target, Summary: row.Summary, DetailID: row.DetailID, CreatedAt: row.CreatedAt,
	}
	c.bus.publish(busEvent{orgID: ss.project.OrgID, projectID: ss.project.ID, envID: ss.env.ID, ev: apitypes.Event{
		Type: apitypes.EventAudit, Project: ae.Project, Environment: ae.Environment, Audit: &ae,
	}})
}

// QueryHistory reads the actor's data.query events on the service.
func (h dataHost) QueryHistory(ctx context.Context, a datapanel.Actor, d *svcData, before int64, limit int) ([]apitypes.DataHistoryEntry, error) {
	params := store.ListDataQueryHistoryParams{EnvironmentID: &d.ss.env.ID, Target: d.ss.env.Name + "/" + d.ss.svc.Name, BeforeID: before, MaxRows: int64(limit)}
	if params.BeforeID <= 0 {
		params.BeforeID = math.MaxInt64
	}
	if a.UserID != 0 {
		params.UserID = &a.UserID
	}
	if a.TokenID != 0 {
		params.TokenID = &a.TokenID
	}
	rows, err := h.c.store.ListDataQueryHistory(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.DataHistoryEntry, len(rows))
	for i, r := range rows {
		out[i] = apitypes.DataHistoryEntry{AuditID: r.AuditID, CreatedAt: r.CreatedAt, AuditDetail: toAuditDetail(r.AuditDetail)}
	}
	return out, nil
}
