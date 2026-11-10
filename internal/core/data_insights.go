package core

import (
	"context"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
)

// DataInsights reads how a database service is doing: connections,
// cache, size, slow and frequent queries, sessions and what blocks them,
// table and index sizes and use, with hints. Overview, tables and indexes
// need data access level schema; queries and sessions read (texts
// redacted for masked callers). Read on demand, nothing stored
// (datapanel.Panel.Insights).
func (c *Core) DataInsights(ctx context.Context, a Actor, ref ServiceRef, req apitypes.DataInsightsRequest) (apitypes.DataInsights, error) {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return apitypes.DataInsights{}, err
	}
	return c.panel.Insights(ctx, panelActor(a), db, req)
}

// databasePressure reads a database service's connections against its
// limit, for Dock itself (deploy diagnosis: "postgres was at 50 of 50
// connections"). Counts only, no statement texts, so it needs no actor
// and isn't audited. One query on the monitor's connection; a database
// refusing logins because it is full returns Full.
func (c *Core) databasePressure(ctx context.Context, ss svcScope) (apitypes.DataPressure, error) {
	engine, by := detectDatabase(ss.svc)
	if engine == "" {
		return apitypes.DataPressure{}, notFound("%s is not a database", ss)
	}
	return c.panel.Pressure(ctx, &datapanel.Database[*svcData]{
		ID: ss.svc.ID, Name: ss.String(), Engine: engine, DetectedBy: by, ConfirmName: ss.env.Name, Host: &svcData{ss},
	})
}

// queryStatsFlags make Postgres load pg_stat_statements and leave out
// utility statements (BEGIN, SET, DDL), which are noise in a slow-query
// list and carry passwords in clear (ALTER ROLE ... PASSWORD).
const queryStatsFlags = "-c shared_preload_libraries=pg_stat_statements -c pg_stat_statements.track_utility=off"

// loadsQueryStats reports whether a start command loads pg_stat_statements.
func loadsQueryStats(cmd string) bool {
	return strings.Contains(cmd, "pg_stat_statements")
}

// withQueryStats adds pg_stat_statements to a Postgres start command: to
// its shared_preload_libraries when it has one, else as queryStatsFlags
// after the docker-entrypoint.sh postgres it runs. Other commands aren't
// guessed at.
func withQueryStats(cmd string) (string, bool) {
	if i := strings.Index(cmd, "shared_preload_libraries="); i >= 0 {
		at := i + len("shared_preload_libraries=")
		if at < len(cmd) && (cmd[at] == '\'' || cmd[at] == '"') {
			at++ // inside a quoted list
		}
		return cmd[:at] + "pg_stat_statements," + cmd[at:] + " -c pg_stat_statements.track_utility=off", true
	}
	if strings.Contains(cmd, "docker-entrypoint.sh postgres") {
		return strings.TrimSpace(cmd) + " " + queryStatsFlags, true
	}
	return cmd, false
}

// EnableDataQueryStats turns on query stats. For a Postgres template
// database whose start command doesn't load pg_stat_statements yet, it
// stages the start command change (deploy scope, like any settings
// change): it waits to apply and goes live with the next deploy, which
// restarts the database; Dock creates the extension when that release
// goes active. Otherwise it creates the extension as the owner (admins).
func (c *Core) EnableDataQueryStats(ctx context.Context, a Actor, ref ServiceRef) (apitypes.DataQueryStatsResult, error) {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return apitypes.DataQueryStatsResult{}, err
	}
	ss := db.Host.ss
	cmd := ss.svc.StartCommand
	if p, err := pendingSettings(ss.svc); err != nil {
		return apitypes.DataQueryStatsResult{}, err
	} else if p != nil {
		cmd = p.StartCommand
	}
	if t, ok := templateOf(ss.svc); ok && t.database == apitypes.DatabasePostgres && !loadsQueryStats(cmd) {
		next, ok := withQueryStats(cmd)
		if !ok {
			return apitypes.DataQueryStatsResult{}, invalid("%s's start command doesn't run docker-entrypoint.sh postgres, so Dock won't change it; add `%s` to it yourself and deploy", ss, queryStatsFlags)
		}
		if _, err := c.UpdateService(ctx, a, ref, apitypes.UpdateServiceRequest{StartCommand: &next}); err != nil {
			return apitypes.DataQueryStatsResult{}, err
		}
		return apitypes.DataQueryStatsResult{State: "staged",
			Detail: "The start command change waits to apply. It goes live with the next deploy, which restarts the database for a few seconds; then query stats are on."}, nil
	}
	if !loadsQueryStats(ss.svc.StartCommand) && loadsQueryStats(cmd) {
		return apitypes.DataQueryStatsResult{State: "staged",
			Detail: "The start command change already waits to apply; query stats turn on with the next deploy."}, nil
	}
	return c.panel.EnableQueryStats(ctx, panelActor(a), db)
}

// ResetDataQueryStats sets the database's query stats back to zero.
// Admins; audited as data.stats_reset.
func (c *Core) ResetDataQueryStats(ctx context.Context, a Actor, ref ServiceRef) error {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return err
	}
	return c.panel.ResetQueryStats(ctx, panelActor(a), db)
}

// CancelDataSession cancels a session's query or ends the session (level
// write; production confirms ending one with the environment's name).
// Audited as data.cancel or data.terminate.
func (c *Core) CancelDataSession(ctx context.Context, a Actor, ref ServiceRef, session string, req apitypes.DataCancelRequest) (apitypes.DataCancelResult, error) {
	db, err := c.dataDB(ctx, a, ref)
	if err != nil {
		return apitypes.DataCancelResult{}, err
	}
	return c.panel.CancelSession(ctx, panelActor(a), db, session, req)
}
