package core

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Database panel (ADR 0016). These are the operations the API, CLI and
// MCP endpoint call. Each resolves the service to a datapanel.Database
// (dataDB: visibility, detection and the actor's data permission from the
// policy in data_policy.go) and hands it to the shared panel (ADR 0024),
// which checks the level, talks to the database through the engine and
// audits what it did. Dock's side of the panel (dialing the container,
// the store) is data_host.go and data_target.go.

// DataWords are Dock's words in the data panel's messages and API docs.
var DataWords = datapanel.Words{
	Product:          "Dock",
	SetupHint:        "an admin can run `dock db setup`",
	LoginFailedHint:  "If POSTGRES_PASSWORD changed after the database was created, set database_url_variable to a variable with the current postgres:// URL",
	SetupAction:      "Set up data access (`dock db setup`)",
	ConfirmWhat:      "the environment's name",
	Database:         "service",
	QueryStatsHint:   "add `-c shared_preload_libraries=pg_stat_statements` to the service's start command (Build & run) and deploy it, which restarts the database",
	EnableQueryStats: "For a Postgres template database whose start command doesn't load it yet, it stages that change instead (state staged; needs deploy): it applies with the next deploy, which restarts the database, and Dock creates the extension then. Otherwise admin.",
}

// ErrDataNotImplemented was returned by the phase 1 stubs. Nothing returns
// it any more; it stays for code written against them.
var ErrDataNotImplemented error = &Error{Code: apitypes.CodeValidation, Msg: "the database panel is not available yet"}

// DataInfo returns a database service's panel state and the caller's
// access. Services that aren't databases are not_found. Anyone who can see
// the service gets it; the database is only contacted for callers with
// level schema or admins, others get the state Dock knows without it.
func (c *Core) DataInfo(ctx context.Context, a Actor, ref ServiceRef) (apitypes.DataInfo, error) {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return apitypes.DataInfo{}, err
	}
	info, err := c.panel.Info(ctx, panelActor(a), db)
	if err != nil {
		return info, err
	}
	info.UsedBy, err = c.usedBy(ctx, db.Host.ss)
	return info, err
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
		val, err := c.openValue(v.Value, variableAAD(v.EnvironmentID, v.ServiceID, v.Key), v.Key)
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
		case apitypes.DatabaseDetect, apitypes.DatabasePostgres, apitypes.DatabaseMySQL, apitypes.DatabaseNone:
			params.Database = string(*req.Database)
		default:
			return apitypes.Service{}, invalid("database must be empty (detect from the image), postgres, mysql or none")
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
	c.panel.Forget(ss.svc.ID)
	c.audit(ctx, a, "service.database", &ss.project, &ss.env, ss.String(),
		fmt.Sprintf("set database to %q and database_url_variable to %q", params.Database, params.DatabaseUrlVariable))
	return c.GetService(ctx, a, ref)
}

// SetupData creates or rotates Dock's roles in the database with the
// owner's credentials ("Set up data access"), stores their passwords
// sealed and syncs the roles' grants. Admin only.
func (c *Core) SetupData(ctx context.Context, a Actor, ref ServiceRef) (apitypes.DataInfo, error) {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return apitypes.DataInfo{}, err
	}
	if !db.Perm.Manage {
		return apitypes.DataInfo{}, errf(apitypes.CodeForbidden, "setting up data access is for admins; %s has scope %s", a, a.Scope)
	}
	info, err := c.panel.Setup(ctx, panelActor(a), db)
	if err != nil {
		return info, err
	}
	info.UsedBy, err = c.usedBy(ctx, db.Host.ss)
	return info, err
}

// DataSchema returns the database's structure, sensitive columns marked.
// Needs level schema.
func (c *Core) DataSchema(ctx context.Context, a Actor, ref ServiceRef) (apitypes.DataSchema, error) {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return apitypes.DataSchema{}, err
	}
	return c.panel.Schema(ctx, panelActor(a), db)
}

// DataRows returns one keyset page of a table. Needs level read; cursors
// are sealed for the caller, sensitive columns masked unless revealed
// (audited as data.reveal), and browsing audited as data.browse
// (datapanel.Panel.Rows).
func (c *Core) DataRows(ctx context.Context, a Actor, ref ServiceRef, req apitypes.DataPageRequest) (apitypes.DataPage, error) {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return apitypes.DataPage{}, err
	}
	return c.panel.Rows(ctx, panelActor(a), db, req)
}

// DataReferences returns the rows referencing one row. Needs level read.
func (c *Core) DataReferences(ctx context.Context, a Actor, ref ServiceRef, req apitypes.DataReferencesRequest) ([]apitypes.DataReferences, error) {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return nil, err
	}
	return c.panel.References(ctx, panelActor(a), db, req)
}

// DataQuery runs one read-only statement, streamed as frames, audited as
// data.query. Needs level read (datapanel.Panel.Query).
func (c *Core) DataQuery(ctx context.Context, a Actor, ref ServiceRef, req apitypes.DataQueryRequest) (Stream[apitypes.DataQueryFrame], error) {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return nil, err
	}
	return c.panel.Query(ctx, panelActor(a), db, req)
}

// DataHistory returns the caller's own queries on the database, newest
// first, before the given audit ID (0: from the newest). Needs level read.
func (c *Core) DataHistory(ctx context.Context, a Actor, ref ServiceRef, before int64, limit int) ([]apitypes.DataHistoryEntry, error) {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return nil, err
	}
	return c.panel.History(ctx, panelActor(a), db, before, limit)
}

// ListSavedQueries returns the caller's saved queries for the database,
// by name. They belong to the user, or the user a `dock login` token acts
// for.
func (c *Core) ListSavedQueries(ctx context.Context, a Actor, ref ServiceRef) ([]apitypes.DataSavedQuery, error) {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return nil, err
	}
	return c.panel.SavedQueries(ctx, panelActor(a), db)
}

// SaveDataQuery creates (id 0) or replaces one of the caller's saved
// queries.
func (c *Core) SaveDataQuery(ctx context.Context, a Actor, ref ServiceRef, id int64, req apitypes.SaveDataQueryRequest) (apitypes.DataSavedQuery, error) {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return apitypes.DataSavedQuery{}, err
	}
	return c.panel.SaveQuery(ctx, panelActor(a), db, id, req)
}

// DeleteSavedQuery deletes one of the caller's saved queries.
func (c *Core) DeleteSavedQuery(ctx context.Context, a Actor, ref ServiceRef, id int64) error {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return err
	}
	return c.panel.DeleteSavedQuery(ctx, panelActor(a), db, id)
}

// SetSensitiveColumns replaces the database's sensitive columns and syncs
// the roles' grants when it can. Admin only.
func (c *Core) SetSensitiveColumns(ctx context.Context, a Actor, ref ServiceRef, req apitypes.SetSensitiveColumnsRequest) ([]apitypes.DataColumnRef, error) {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return nil, err
	}
	if !db.Perm.Manage {
		return nil, errf(apitypes.CodeForbidden, "marking sensitive columns is for admins; %s has scope %s", a, a.Scope)
	}
	return c.panel.SetSensitiveColumns(ctx, panelActor(a), db, req)
}
