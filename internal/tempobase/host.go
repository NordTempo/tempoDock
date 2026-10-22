package tempobase

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbstore"
)

// host is tempoBase's datapanel.Host: a database is a saved connection
// (the handle is its row), the panel's state lives in tbstore, and data
// events go to tempoBase's audit log. Every method runs a fixed number of
// queries.
type host struct{ app *App }

var _ datapanel.Host[*tbstore.Connection] = host{}

// Host returns the App as the data panel's host.
func (a *App) Host() datapanel.Host[*tbstore.Connection] { return host{a} }

// Connect dials the connection's host and port with its TLS mode, as the
// URL's user.
func (h host) Connect(_ context.Context, c *tbstore.Connection) (datapanel.Conn, error) {
	return h.app.conn(c)
}

func (h host) Access(ctx context.Context, id int64) (*datapanel.Access, error) {
	r, err := h.app.store.GetDataAccess(ctx, id)
	if tbstore.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &datapanel.Access{
		Engine: apitypes.DataEngine(r.Engine), ServerVersion: r.ServerVersion, ReaderSecret: r.ReaderSecret,
		WriterSecret: r.WriterSecret, GrantsSyncedHash: r.GrantsSyncedHash, SetUpAt: r.SetUpAt, SetUpBy: r.SetUpBy,
	}, nil
}

func (h host) PutAccess(ctx context.Context, id int64, acc datapanel.Access) error {
	return h.app.store.UpsertDataAccess(ctx, tbstore.UpsertDataAccessParams{
		ConnectionID: id, Engine: string(acc.Engine), ServerVersion: acc.ServerVersion, ReaderSecret: acc.ReaderSecret,
		WriterSecret: acc.WriterSecret, GrantsSyncedHash: acc.GrantsSyncedHash, SetUpAt: acc.SetUpAt, SetUpBy: acc.SetUpBy,
	})
}

func (h host) SetGrantsHash(ctx context.Context, id int64, hash string) error {
	return h.app.store.SetGrantsSyncedHash(ctx, tbstore.SetGrantsSyncedHashParams{GrantsSyncedHash: hash, ConnectionID: id})
}

func (h host) SensitiveColumns(ctx context.Context, id int64) ([]apitypes.DataColumnRef, error) {
	rows, err := h.app.store.ListSensitiveColumns(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.DataColumnRef, len(rows))
	for i, r := range rows {
		out[i] = apitypes.DataColumnRef{Schema: r.SchemaName, Table: r.TableName, Column: r.ColumnName}
	}
	return out, nil
}

func (h host) ReplaceSensitiveColumns(ctx context.Context, id int64, cols []apitypes.DataColumnRef) error {
	return h.app.store.Tx(ctx, func(q *tbstore.Queries) error {
		if err := q.DeleteSensitiveColumns(ctx, id); err != nil {
			return err
		}
		for _, c := range cols {
			if err := q.AddSensitiveColumn(ctx, tbstore.AddSensitiveColumnParams{
				ConnectionID: id, SchemaName: c.Schema, TableName: c.Table, ColumnName: c.Column,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// SavedQueryOwner is the person; tokens are agents and have none.
func (h host) SavedQueryOwner(_ context.Context, a datapanel.Actor) (int64, error) {
	if a.Kind == "user" {
		return a.UserID, nil
	}
	return 0, nil
}

func (h host) SavedQueries(ctx context.Context, id, user int64) ([]apitypes.DataSavedQuery, error) {
	rows, err := h.app.store.ListSavedQueries(ctx, tbstore.ListSavedQueriesParams{ConnectionID: id, UserID: user})
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.DataSavedQuery, len(rows))
	for i, r := range rows {
		out[i] = toSavedQuery(r)
	}
	return out, nil
}

func (h host) SaveQuery(ctx context.Context, id, user, queryID int64, name, sql string, at time.Time) (apitypes.DataSavedQuery, error) {
	var (
		q   tbstore.SavedQuery
		err error
	)
	if queryID == 0 {
		q, err = h.app.store.SaveQuery(ctx, tbstore.SaveQueryParams{ConnectionID: id, UserID: user, Name: name, Sql: sql, Now: at})
	} else {
		q, err = h.app.store.UpdateSavedQuery(ctx, tbstore.UpdateSavedQueryParams{Name: name, Sql: sql, Now: at, ID: queryID, ConnectionID: id, UserID: user})
	}
	switch {
	case tbstore.IsNotFound(err):
		return apitypes.DataSavedQuery{}, fmt.Errorf("saved query %d: %w", queryID, datapanel.ErrNotFound)
	case tbstore.IsUnique(err):
		return apitypes.DataSavedQuery{}, fmt.Errorf("saved query %q: %w", name, datapanel.ErrDuplicate)
	case err != nil:
		return apitypes.DataSavedQuery{}, err
	}
	return toSavedQuery(q), nil
}

func (h host) DeleteSavedQuery(ctx context.Context, id, user, queryID int64) (bool, error) {
	n, err := h.app.store.DeleteSavedQuery(ctx, tbstore.DeleteSavedQueryParams{ID: queryID, ConnectionID: id, UserID: user})
	return n > 0, err
}

func (h host) CreateChangeset(ctx context.Context, cs datapanel.Changeset) (datapanel.Changeset, error) {
	changes, err := json.Marshal(cs.Changes)
	if err != nil {
		return cs, err
	}
	row, err := h.app.store.CreateChangeset(ctx, tbstore.CreateChangesetParams{
		ConnectionID: cs.Database, Author: cs.Author, AuthorKind: cs.AuthorKind, UserID: nonZero(cs.UserID), TokenID: nonZero(cs.TokenID),
		Message: cs.Message, Changes: string(changes), Sql: cs.SQL, CreatedAt: cs.CreatedAt, ExpiresAt: cs.ExpiresAt,
	})
	if err != nil {
		return cs, err
	}
	return toChangeset(row)
}

func (h host) Changeset(ctx context.Context, id, changesetID int64) (datapanel.Changeset, error) {
	row, err := h.app.store.GetChangeset(ctx, tbstore.GetChangesetParams{ID: changesetID, ConnectionID: id})
	if tbstore.IsNotFound(err) {
		return datapanel.Changeset{}, fmt.Errorf("changeset %d: %w", changesetID, datapanel.ErrNotFound)
	}
	if err != nil {
		return datapanel.Changeset{}, err
	}
	return toChangeset(row)
}

func (h host) Changesets(ctx context.Context, id int64, pendingOnly bool, limit int) ([]datapanel.Changeset, error) {
	var (
		rows []tbstore.Changeset
		err  error
	)
	if pendingOnly {
		rows, err = h.app.store.ListPendingChangesets(ctx, id)
	} else {
		rows, err = h.app.store.ListChangesets(ctx, tbstore.ListChangesetsParams{ConnectionID: id, MaxRows: int64(limit)})
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

func (h host) DecideChangeset(ctx context.Context, d datapanel.Decision) (datapanel.Changeset, error) {
	params := tbstore.DecideChangesetParams{
		Status: string(d.Status), DecidedAt: &d.DecidedAt, DecidedBy: d.DecidedBy, Reason: d.Reason, ID: d.ID, ConnectionID: d.Database,
	}
	if d.RowsAffected != nil {
		b, err := json.Marshal(d.RowsAffected)
		if err != nil {
			return datapanel.Changeset{}, err
		}
		params.RowsAffected = string(b)
	}
	if d.Conflicts != nil {
		b, err := json.Marshal(d.Conflicts)
		if err != nil {
			return datapanel.Changeset{}, err
		}
		params.Conflicts = string(b)
	}
	row, err := h.app.store.DecideChangeset(ctx, params)
	if tbstore.IsNotFound(err) {
		return datapanel.Changeset{}, fmt.Errorf("changeset %d is no longer pending: %w", d.ID, datapanel.ErrNotFound)
	}
	if err != nil {
		return datapanel.Changeset{}, err
	}
	return toChangeset(row)
}

func (h host) ExpireChangesets(ctx context.Context, now time.Time) error {
	return h.app.store.ExpireChangesets(ctx, now)
}

// Audit writes a data event about c to tempoBase's audit log.
func (h host) Audit(ctx context.Context, a datapanel.Actor, c *tbstore.Connection, e datapanel.AuditEntry) {
	act := Actor{Kind: a.Kind, Name: a.Name, UserID: a.UserID, TokenID: a.TokenID}
	h.app.writeAudit(ctx, act, e.Action, &c.ID, "connection "+c.Name, e.Summary, e.Detail)
}

// QueryHistory reads the actor's data.query events on c.
func (h host) QueryHistory(ctx context.Context, a datapanel.Actor, c *tbstore.Connection, before int64, limit int) ([]apitypes.DataHistoryEntry, error) {
	if before <= 0 {
		before = math.MaxInt64
	}
	type entry struct {
		id     int64
		at     time.Time
		detail tbstore.AuditDetail
	}
	var entries []entry
	switch {
	case a.Kind == "user":
		rows, err := h.app.store.ListUserQueryHistory(ctx, tbstore.ListUserQueryHistoryParams{ConnectionID: &c.ID, UserID: &a.UserID, BeforeID: before, MaxRows: int64(limit)})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			entries = append(entries, entry{r.AuditID, r.CreatedAt, r.AuditDetail})
		}
	case a.TokenID != 0:
		rows, err := h.app.store.ListTokenQueryHistory(ctx, tbstore.ListTokenQueryHistoryParams{ConnectionID: &c.ID, TokenID: &a.TokenID, BeforeID: before, MaxRows: int64(limit)})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			entries = append(entries, entry{r.AuditID, r.CreatedAt, r.AuditDetail})
		}
	}
	out := make([]apitypes.DataHistoryEntry, len(entries))
	for i, e := range entries {
		out[i] = apitypes.DataHistoryEntry{AuditID: e.id, CreatedAt: e.at, AuditDetail: toAuditDetail(e.detail)}
	}
	return out, nil
}

func toSavedQuery(q tbstore.SavedQuery) apitypes.DataSavedQuery {
	return apitypes.DataSavedQuery{ID: q.ID, Name: q.Name, SQL: q.Sql, CreatedAt: q.CreatedAt, UpdatedAt: q.UpdatedAt}
}

// toChangeset decodes a stored changeset.
func toChangeset(r tbstore.Changeset) (datapanel.Changeset, error) {
	cs := datapanel.Changeset{
		ID: r.ID, Database: r.ConnectionID, Author: r.Author, AuthorKind: r.AuthorKind, Status: apitypes.DataChangesetStatus(r.Status),
		Message: r.Message, SQL: r.Sql, Reason: r.Reason, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt,
		DecidedAt: r.DecidedAt, DecidedBy: r.DecidedBy,
	}
	if r.UserID != nil {
		cs.UserID = *r.UserID
	}
	if r.TokenID != nil {
		cs.TokenID = *r.TokenID
	}
	if err := json.Unmarshal([]byte(r.Changes), &cs.Changes); err != nil {
		return cs, fmt.Errorf("changeset %d changes: %w", r.ID, err)
	}
	if r.RowsAffected != "" {
		if err := json.Unmarshal([]byte(r.RowsAffected), &cs.RowsAffected); err != nil {
			return cs, fmt.Errorf("changeset %d rows affected: %w", r.ID, err)
		}
	}
	if r.Conflicts != "" {
		if err := json.Unmarshal([]byte(r.Conflicts), &cs.Conflicts); err != nil {
			return cs, fmt.Errorf("changeset %d conflicts: %w", r.ID, err)
		}
	}
	return cs, nil
}

// nonZero is nil for 0, else a pointer to id.
func nonZero(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}
