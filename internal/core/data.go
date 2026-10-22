package core

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Database panel (ADR 0016). These are the operations the API and CLI call.
// Every operation checks the actor's effective data permission (the data
// access policy, the token's override and its caps; data_policy.go) before
// touching the database, and audits what it did (data_audit.go). The
// engine (Core.data) talks to the database through the service's running
// container (data_target.go); changesets are in data_changesets.go.

// ErrDataNotImplemented was returned by the phase 1 stubs. Nothing returns
// it any more; it stays for code written against them.
var ErrDataNotImplemented error = &Error{Code: apitypes.CodeValidation, Msg: "the database panel is not available yet"}

// DataInfo returns a database service's panel state and the caller's
// access. Services that aren't databases are not_found. Anyone who can see
// the service gets it; the database is only contacted for callers with
// level schema or admins, others get the state Dock knows without it.
func (c *Core) DataInfo(ctx context.Context, a Actor, ref ServiceRef) (apitypes.DataInfo, error) {
	dt, err := c.dataService(ctx, a, ref)
	if err != nil {
		return apitypes.DataInfo{}, err
	}
	return c.dataInfo(ctx, dt)
}

// dataInfo works out a loaded database's status.
func (c *Core) dataInfo(ctx context.Context, dt *dbTarget) (apitypes.DataInfo, error) {
	info := apitypes.DataInfo{Engine: dt.engine, DetectedBy: dt.by, Access: dt.perm}
	var err error
	if info.UsedBy, err = c.usedBy(ctx, dt.ss); err != nil {
		return info, err
	}
	if dt.access != nil {
		info.SetUpAt, info.SetUpBy, info.ServerVersion = &dt.access.SetUpAt, dt.access.SetUpBy, dt.access.ServerVersion
	}
	conn, err := c.dbConnection(ctx, dt.ss)
	switch {
	case errors.Is(err, errNotRunning):
		info.Status, info.Reason = apitypes.DataNotRunning, fmt.Sprintf("%s is not running; deploy or restart it", dt.ss.svc.Name)
		return info, nil
	case CodeOf(err) == apitypes.CodeValidation:
		info.Status, info.Reason = apitypes.DataUnreachable, err.Error()
		return info, nil
	case err != nil:
		return info, err
	case dt.access == nil:
		info.Status, info.Reason = apitypes.DataSetupNeeded, "Not set up. An admin can run `dock db setup`."
		if dt.perm.Manage {
			info.Reason = "Not set up. Set up data access (`dock db setup`) to create Dock's roles in the database."
		}
		return info, nil
	case !dt.perm.Level.Includes(apitypes.LevelSchema) && !dt.perm.Manage:
		info.Status = apitypes.DataReady
		return info, nil
	}
	if err := c.useConn(ctx, dt, conn); err != nil {
		return info, err
	}
	srv, err := c.data.Status(ctx, dt.t)
	switch {
	case err == nil:
		info.Status, info.ServerVersion = apitypes.DataReady, srv.Version
	case errors.Is(err, data.ErrNotSetUp):
		info.Status, info.Reason = apitypes.DataSetupNeeded, dataError(dt, err).Error()
	case errors.Is(err, data.ErrUnsupported):
		info.Status, info.Reason = apitypes.DataUnsupported, dataError(dt, err).Error()
	case ctx.Err() != nil:
		return info, ctx.Err()
	default:
		info.Status, info.Reason = apitypes.DataUnreachable, dataError(dt, err).Error()
	}
	return info, nil
}

// usedBy names the other services in the environment whose own variables
// reference the database (${{db.KEY}}) or a URL with its hostname. One
// query; values are decrypted in memory.
func (c *Core) usedBy(ctx context.Context, ss svcScope) ([]string, error) {
	svcs, err := c.store.ListServices(ctx, ss.env.ID)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, s := range svcs {
		if s.ArchivedAt == nil && s.ID != ss.svc.ID {
			names[s.ID] = s.Name
		}
	}
	vars, err := c.store.ListVariables(ctx, ss.env.ID)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, v := range vars {
		if v.ServiceID == nil || names[*v.ServiceID] == "" || slices.Contains(out, names[*v.ServiceID]) {
			continue
		}
		val, err := c.openValue(v.Value)
		if err != nil {
			return nil, err
		}
		if val != nil && references(*val, ss.svc.Name) {
			out = append(out, names[*v.ServiceID])
		}
	}
	slices.Sort(out)
	return out, nil
}

// references reports whether a variable value points at a service: a
// ${{service.KEY}} reference or a URL with the service as host.
func references(val, service string) bool {
	for _, m := range refRe.FindAllStringSubmatch(val, -1) {
		if m[1] == service {
			return true
		}
	}
	if strings.Contains(val, "://") {
		if u, err := url.Parse(val); err == nil && u.Hostname() == service {
			return true
		}
	}
	return false
}

// urlVariableRe matches the database_url_variable setting: KEY, or
// service.KEY for another service's (shared.KEY for a shared one).
var urlVariableRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?\.)?[A-Za-z_][A-Za-z0-9_]*$`)

// UpdateDatabaseSettings changes a service's database settings, which
// apply immediately. Needs deploy, like other service settings.
func (c *Core) UpdateDatabaseSettings(ctx context.Context, a Actor, ref ServiceRef, req apitypes.UpdateDatabaseSettingsRequest) (apitypes.Service, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "changing database settings")
	if err != nil {
		return apitypes.Service{}, err
	}
	params := store.SetServiceDatabaseSettingsParams{ID: ss.svc.ID, Database: ss.svc.Database, DatabaseUrlVariable: ss.svc.DatabaseUrlVariable}
	if req.Database != nil {
		switch *req.Database {
		case apitypes.DatabaseDetect, apitypes.DatabasePostgres, apitypes.DatabaseNone:
			params.Database = string(*req.Database)
		default:
			return apitypes.Service{}, invalid("database must be empty (detect from the image), postgres or none")
		}
	}
	if req.DatabaseURLVariable != nil {
		v := strings.TrimSpace(*req.DatabaseURLVariable)
		if v != "" && !urlVariableRe.MatchString(v) {
			return apitypes.Service{}, invalid("database_url_variable %q must be a variable name like DATABASE_URL, or another service's like api.DATABASE_URL", v)
		}
		params.DatabaseUrlVariable = v
	}
	if _, err := c.store.SetServiceDatabaseSettings(ctx, params); err != nil {
		return apitypes.Service{}, err
	}
	c.data.Forget(ss.svc.ID)
	c.audit(ctx, a, "service.database", &ss.project, &ss.env, ss.String(),
		fmt.Sprintf("set database to %q and database_url_variable to %q", params.Database, params.DatabaseUrlVariable))
	return c.GetService(ctx, a, ref)
}

// SetupData creates or rotates Dock's roles in the database with the
// owner's credentials ("Set up data access"), stores their passwords
// sealed and syncs the masked reader's grants. Admin only.
func (c *Core) SetupData(ctx context.Context, a Actor, ref ServiceRef) (apitypes.DataInfo, error) {
	dt, err := c.dataService(ctx, a, ref)
	if err != nil {
		return apitypes.DataInfo{}, err
	}
	if !dt.perm.Manage {
		return apitypes.DataInfo{}, errf(apitypes.CodeForbidden, "setting up data access is for admins; %s has scope %s", a, a.Scope)
	}
	if err := c.connect(ctx, dt); err != nil {
		return apitypes.DataInfo{}, err
	}
	reader := newRolePassword()
	dt.t.Reader = data.Credentials{User: string(data.RoleReader), Password: reader}
	dt.t.Writer = data.Credentials{User: string(data.RoleWriter), Password: newRolePassword()}
	dt.t.ReaderMasked = data.Credentials{User: string(data.RoleReaderMasked), Password: maskedPassword(reader)}
	srv, err := c.data.Setup(ctx, dt.t, dt.conn.owner, dt.t.Reader, dt.t.Writer)
	if err != nil {
		return apitypes.DataInfo{}, dataError(dt, err)
	}
	readerSecret, err := c.box.Seal([]byte(dt.t.Reader.Password))
	if err != nil {
		return apitypes.DataInfo{}, err
	}
	writerSecret, err := c.box.Seal([]byte(dt.t.Writer.Password))
	if err != nil {
		return apitypes.DataInfo{}, err
	}
	acc, err := c.store.UpsertDataAccess(ctx, store.UpsertDataAccessParams{
		ServiceID: dt.ss.svc.ID, Engine: string(dt.engine), ServerVersion: srv.Version,
		ReaderSecret: readerSecret, WriterSecret: writerSecret, SetUpAt: now(), SetUpBy: a.Name,
	})
	if err != nil {
		return apitypes.DataInfo{}, err
	}
	dt.access = &acc
	c.audit(ctx, a, apitypes.AuditDataSetup, &dt.ss.project, &dt.ss.env, dt.ss.String(),
		fmt.Sprintf("set up data access on Postgres %s: created or rotated %s, %s and %s", srv.Version, data.RoleReader, data.RoleWriter, data.RoleReaderMasked))
	if err := c.syncMasked(ctx, dt, ""); err != nil {
		return apitypes.DataInfo{}, err
	}
	return c.dataInfo(ctx, dt)
}

// syncMasked brings the masked reader's grants up to date when the schema
// or the sensitive columns changed since the last sync (current is their
// MaskedHash, "" to sync regardless), as the owner. A failure is an error:
// masked callers must not run SQL on stale grants.
func (c *Core) syncMasked(ctx context.Context, dt *dbTarget, current string) error {
	if current != "" && current == dt.access.MaskedSyncedHash {
		return nil
	}
	hash, err := c.data.SyncMasked(ctx, dt.t, dt.conn.owner, dt.sensitive)
	if err != nil {
		return errf(apitypes.CodeConflict, "couldn't update the masked reader's grants in %s (%v); an admin can run `dock db setup` again", dt.ss, dataError(dt, err))
	}
	if err := c.store.SetDataMaskedSyncedHash(ctx, store.SetDataMaskedSyncedHashParams{MaskedSyncedHash: hash, ServiceID: dt.ss.svc.ID}); err != nil {
		return err
	}
	dt.access.MaskedSyncedHash = hash
	return nil
}

// DataSchema returns the database's structure, sensitive columns marked.
// Needs level schema.
func (c *Core) DataSchema(ctx context.Context, a Actor, ref ServiceRef) (apitypes.DataSchema, error) {
	dt, err := c.dataTarget(ctx, a, ref, apitypes.LevelSchema, "reading the schema")
	if err != nil {
		return apitypes.DataSchema{}, err
	}
	s, err := c.data.Schema(ctx, dt.t, dt.call(a, false), dt.sensitive)
	return s, dataError(dt, err)
}

// DataRows returns one keyset page of a table. Needs level read.
// Sensitive columns are masked unless the request reveals them, which
// needs sensitive visible and is audited as data.reveal before the values
// go out. The first page per caller and table, then at most every 10
// minutes, is audited as data.browse; reveals are throttled the same way.
func (c *Core) DataRows(ctx context.Context, a Actor, ref ServiceRef, req apitypes.DataPageRequest) (apitypes.DataPage, error) {
	switch {
	case req.Limit < 0 || req.Limit > apitypes.DataPageMaxLimit:
		return apitypes.DataPage{}, invalid("limit must be between 1 and %d", apitypes.DataPageMaxLimit)
	case req.Limit == 0:
		req.Limit = apitypes.DataPageLimit
	}
	if req.Full && req.Limit != 1 {
		return apitypes.DataPage{}, invalid("full values are only returned one row at a time; set limit to 1")
	}
	dt, err := c.dataTarget(ctx, a, ref, apitypes.LevelRead, "reading rows")
	if err != nil {
		return apitypes.DataPage{}, err
	}
	if req.Reveal && dt.perm.Sensitive != apitypes.SensitiveVisible {
		return apitypes.DataPage{}, errf(apitypes.CodeForbidden, "revealing sensitive columns needs sensitive visible; yours are masked (%s)", whyWords(dt.perm))
	}
	page, err := c.data.Page(ctx, dt.t, dt.call(a, req.Reveal), req)
	if err != nil {
		return page, dataError(dt, err)
	}
	c.auditBrowse(ctx, a, dt.ss, req)
	if req.Reveal {
		c.auditReveal(ctx, a, dt, req.Table)
	}
	return page, nil
}

// DataReferences returns the rows referencing one row. Needs level read.
func (c *Core) DataReferences(ctx context.Context, a Actor, ref ServiceRef, req apitypes.DataReferencesRequest) ([]apitypes.DataReferences, error) {
	switch {
	case req.Limit < 0 || req.Limit > apitypes.DataReferencesMaxLimit:
		return nil, invalid("limit must be between 1 and %d", apitypes.DataReferencesMaxLimit)
	case req.Limit == 0:
		req.Limit = apitypes.DataReferencesLimit
	}
	dt, err := c.dataTarget(ctx, a, ref, apitypes.LevelRead, "reading rows")
	if err != nil {
		return nil, err
	}
	refs, err := c.data.References(ctx, dt.t, dt.call(a, false), req)
	if err != nil {
		return nil, dataError(dt, err)
	}
	c.auditBrowse(ctx, a, dt.ss, apitypes.DataPageRequest{Table: req.Table})
	return refs, nil
}

// DataQuery runs one read-only statement, streamed as frames. Needs level
// read. Masked callers' SQL runs as the masked reader, whose grants are
// brought up to date first. Every statement is audited as data.query with
// its SQL, rows, duration and outcome. The stream calls ready with its
// first frame, so an error before any frame (dial, login, a rejected
// statement) is a plain error.
func (c *Core) DataQuery(ctx context.Context, a Actor, ref ServiceRef, req apitypes.DataQueryRequest) (Stream[apitypes.DataQueryFrame], error) {
	switch {
	case strings.TrimSpace(req.SQL) == "":
		return nil, invalid("the SQL is empty")
	case len(req.SQL) > apitypes.DataSQLMaxBytes:
		return nil, errf(apitypes.CodeTooLarge, "the SQL is longer than %d bytes", apitypes.DataSQLMaxBytes)
	case req.Limit < 0 || req.Limit > apitypes.DataQueryMaxLimit:
		return nil, invalid("limit must be between 1 and %d", apitypes.DataQueryMaxLimit)
	case req.TimeoutSeconds < 0 || req.TimeoutSeconds > int(apitypes.DataQueryMaxTimeout.Seconds()):
		return nil, invalid("timeout must be between 1 and %d seconds", int(apitypes.DataQueryMaxTimeout.Seconds()))
	}
	dt, err := c.dataTarget(ctx, a, ref, apitypes.LevelRead, "running SQL")
	if err != nil {
		return nil, err
	}
	call := dt.call(a, true)
	if call.Masked {
		s, err := c.data.Schema(ctx, dt.t, call, dt.sensitive)
		if err != nil {
			return nil, dataError(dt, err)
		}
		if err := c.syncMasked(ctx, dt, data.MaskedHash(s.Tables)); err != nil {
			return nil, err
		}
	}
	return func(ctx context.Context, ready func(), send func(...apitypes.DataQueryFrame) error) error {
		started := false
		stats, err := c.data.Query(ctx, dt.t, call, req, func(f apitypes.DataQueryFrame) error {
			if !started {
				ready()
				started = true
			}
			return send(f)
		})
		c.auditQuery(ctx, a, dt.ss, req.SQL, stats, err)
		return dataError(dt, err)
	}, nil
}

// DataHistory returns the caller's own queries on the database, newest
// first, before the given audit ID (0: from the newest). Needs level read.
func (c *Core) DataHistory(ctx context.Context, a Actor, ref ServiceRef, before int64, limit int) ([]apitypes.DataHistoryEntry, error) {
	dt, err := c.dataService(ctx, a, ref)
	if err != nil {
		return nil, err
	}
	if err := requireLevel(dt.perm, apitypes.LevelRead, "reading query history"); err != nil {
		return nil, err
	}
	params := store.ListDataQueryHistoryParams{Target: dt.ss.String(), BeforeID: before, MaxRows: int64(limit)}
	if params.BeforeID <= 0 {
		params.BeforeID = math.MaxInt64
	}
	if params.MaxRows <= 0 || params.MaxRows > 200 {
		params.MaxRows = 50
	}
	if a.UserID != 0 {
		params.UserID = &a.UserID
	}
	if a.TokenID != 0 {
		params.TokenID = &a.TokenID
	}
	rows, err := c.store.ListDataQueryHistory(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.DataHistoryEntry, len(rows))
	for i, r := range rows {
		out[i] = apitypes.DataHistoryEntry{AuditID: r.AuditID, CreatedAt: r.CreatedAt, AuditDetail: toAuditDetail(r.AuditDetail)}
	}
	return out, nil
}

// savedQueryScope loads the database and the user saved queries belong
// to: the caller, or the user a `dock login` token acts for. Needs level
// read.
func (c *Core) savedQueryScope(ctx context.Context, a Actor, ref ServiceRef) (*dbTarget, int64, error) {
	userID := a.UserID
	if userID == 0 && a.Email != "" {
		u, err := c.store.GetUserByEmail(ctx, a.Email)
		if err != nil && !store.IsNotFound(err) {
			return nil, 0, err
		}
		userID = u.ID
	}
	if userID == 0 {
		return nil, 0, invalid("saved queries belong to users; log in with `dock login` to use them")
	}
	dt, err := c.dataService(ctx, a, ref)
	if err != nil {
		return nil, 0, err
	}
	return dt, userID, requireLevel(dt.perm, apitypes.LevelRead, "using saved queries")
}

func toSavedQuery(q store.SavedQuery) apitypes.DataSavedQuery {
	return apitypes.DataSavedQuery{ID: q.ID, Name: q.Name, SQL: q.Sql, CreatedAt: q.CreatedAt, UpdatedAt: q.UpdatedAt}
}

// ListSavedQueries returns the caller's saved queries for the database,
// by name.
func (c *Core) ListSavedQueries(ctx context.Context, a Actor, ref ServiceRef) ([]apitypes.DataSavedQuery, error) {
	dt, userID, err := c.savedQueryScope(ctx, a, ref)
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListSavedQueries(ctx, store.ListSavedQueriesParams{ServiceID: dt.ss.svc.ID, UserID: userID})
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.DataSavedQuery, len(rows))
	for i, r := range rows {
		out[i] = toSavedQuery(r)
	}
	return out, nil
}

// SaveDataQuery creates (id 0) or replaces one of the caller's saved
// queries. Creating with a name the caller already uses replaces that
// query's SQL.
func (c *Core) SaveDataQuery(ctx context.Context, a Actor, ref ServiceRef, id int64, req apitypes.SaveDataQueryRequest) (apitypes.DataSavedQuery, error) {
	req.Name = strings.TrimSpace(req.Name)
	switch {
	case req.Name == "" || len(req.Name) > 100:
		return apitypes.DataSavedQuery{}, invalid("a saved query needs a name of 1-100 characters")
	case strings.TrimSpace(req.SQL) == "":
		return apitypes.DataSavedQuery{}, invalid("the SQL is empty")
	case len(req.SQL) > apitypes.DataSQLMaxBytes:
		return apitypes.DataSavedQuery{}, errf(apitypes.CodeTooLarge, "the SQL is longer than %d bytes", apitypes.DataSQLMaxBytes)
	}
	dt, userID, err := c.savedQueryScope(ctx, a, ref)
	if err != nil {
		return apitypes.DataSavedQuery{}, err
	}
	var q store.SavedQuery
	if id == 0 {
		q, err = c.store.SaveQuery(ctx, store.SaveQueryParams{ServiceID: dt.ss.svc.ID, UserID: userID, Name: req.Name, Sql: req.SQL, Now: now()})
	} else {
		q, err = c.store.UpdateSavedQuery(ctx, store.UpdateSavedQueryParams{Name: req.Name, Sql: req.SQL, Now: now(), ID: id, ServiceID: dt.ss.svc.ID, UserID: userID})
	}
	switch {
	case store.IsNotFound(err):
		return apitypes.DataSavedQuery{}, notFound("saved query %d not found", id)
	case store.IsUnique(err):
		return apitypes.DataSavedQuery{}, conflict("you already have a saved query named %q", req.Name)
	case err != nil:
		return apitypes.DataSavedQuery{}, err
	}
	return toSavedQuery(q), nil
}

// DeleteSavedQuery deletes one of the caller's saved queries.
func (c *Core) DeleteSavedQuery(ctx context.Context, a Actor, ref ServiceRef, id int64) error {
	dt, userID, err := c.savedQueryScope(ctx, a, ref)
	if err != nil {
		return err
	}
	n, err := c.store.DeleteSavedQuery(ctx, store.DeleteSavedQueryParams{ID: id, ServiceID: dt.ss.svc.ID, UserID: userID})
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("saved query %d not found", id)
	}
	return nil
}

// SetSensitiveColumns replaces the database's sensitive columns and, when
// the database is set up and running, syncs the masked reader's grants
// (otherwise the next masked query does, or refuses). Admin only.
func (c *Core) SetSensitiveColumns(ctx context.Context, a Actor, ref ServiceRef, req apitypes.SetSensitiveColumnsRequest) ([]apitypes.DataColumnRef, error) {
	dt, err := c.dataService(ctx, a, ref)
	if err != nil {
		return nil, err
	}
	if !dt.perm.Manage {
		return nil, errf(apitypes.CodeForbidden, "marking sensitive columns is for admins; %s has scope %s", a, a.Scope)
	}
	for _, col := range req.Columns {
		if col.Schema == "" || col.Table == "" || col.Column == "" {
			return nil, invalid("a sensitive column needs its schema, table and column")
		}
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteSensitiveColumns(ctx, dt.ss.svc.ID); err != nil {
			return err
		}
		for _, col := range req.Columns {
			if err := q.AddSensitiveColumn(ctx, store.AddSensitiveColumnParams{ServiceID: dt.ss.svc.ID, SchemaName: col.Schema, TableName: col.Table, ColumnName: col.Column}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := c.loadSensitive(ctx, dt); err != nil {
		return nil, err
	}
	if dt.access != nil {
		// Best effort: if it fails, the next masked query syncs or refuses.
		conn, err := c.dbConnection(ctx, dt.ss)
		if err == nil {
			if err = c.useConn(ctx, dt, conn); err == nil {
				err = c.syncMasked(ctx, dt, "")
			}
		}
		if err != nil && !errors.Is(err, errNotRunning) {
			c.log.Warn("sync masked reader after marking sensitive columns", "service", dt.ss.String(), "err", err)
		}
	}
	names := make([]string, len(dt.sensitive))
	for i, col := range dt.sensitive {
		names[i] = col.Schema + "." + col.Table + "." + col.Column
	}
	summary := "cleared the sensitive columns"
	if len(names) > 0 {
		summary = "marked sensitive: " + strings.Join(names, ", ")
	}
	c.audit(ctx, a, apitypes.AuditDataPolicy, &dt.ss.project, &dt.ss.env, dt.ss.String(), summary)
	return dt.sensitive, nil
}
