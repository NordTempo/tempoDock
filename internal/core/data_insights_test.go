package core

import (
	"context"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// pressureEngine is pageEngine with Pressure: a database at its limit.
type pressureEngine struct{ *pageEngine }

func (pressureEngine) Pressure(context.Context, data.Target) (apitypes.DataPressure, error) {
	return apitypes.DataPressure{Used: 50, Max: 50, Reserved: 3, IdleInTransaction: 12}, nil
}

// TestDatabasePressure: Dock reads a database's connection counts for
// itself (deploy diagnosis) without an actor, and a service that isn't a
// database is not found.
func TestDatabasePressure(t *testing.T) {
	h, eng, db, _ := fakeDatabase(t)
	h.opts.DataEngines = []data.Engine{pressureEngine{eng}}
	h.restart()
	ss, err := h.c.loadService(h.ctx, 1, db)
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.c.databasePressure(h.ctx, ss)
	if err != nil || p.Used != 50 || p.IdleInTransaction != 12 {
		t.Fatalf("pressure: %+v %v", p, err)
	}

	app := h.service("app", apitypes.CreateServiceRequest{Image: "nginx"})
	appSS, err := h.c.loadService(h.ctx, 1, app)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.databasePressure(h.ctx, appSS); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("pressure of an app: %v", err)
	}
}

// TestWithQueryStats: pg_stat_statements joins an existing preload list
// or follows docker-entrypoint.sh postgres; other commands aren't touched.
func TestWithQueryStats(t *testing.T) {
	for _, c := range []struct{ cmd, want string }{
		{"exec docker-entrypoint.sh postgres -c max_connections=50", "exec docker-entrypoint.sh postgres -c max_connections=50 " + queryStatsFlags},
		{"postgres -c shared_preload_libraries=timescaledb", "postgres -c shared_preload_libraries=pg_stat_statements,timescaledb -c pg_stat_statements.track_utility=off"},
		{"postgres -c shared_preload_libraries='timescaledb'", "postgres -c shared_preload_libraries='pg_stat_statements,timescaledb' -c pg_stat_statements.track_utility=off"},
		{"/start.sh", ""},
	} {
		got, ok := withQueryStats(c.cmd)
		if (c.want == "") == ok || (ok && got != c.want) {
			t.Errorf("withQueryStats(%q) = %q, %v; want %q", c.cmd, got, ok, c.want)
		}
	}
}

// TestEnableQueryStatsStages: a template database from before query
// stats gets the start command change staged, never applied; asking
// again says it already waits.
func TestEnableQueryStatsStages(t *testing.T) {
	h := newHarness(t)
	db := h.service("db", apitypes.CreateServiceRequest{Template: apitypes.TemplatePostgres})
	h.wait(db, 1)
	old := strings.TrimSuffix(templates[apitypes.TemplatePostgres].cmd, " "+queryStatsFlags)
	if _, err := h.c.UpdateService(h.ctx, testSystem, db, apitypes.UpdateServiceRequest{StartCommand: &old, Apply: true}); err != nil {
		t.Fatal(err)
	}
	h.wait(db, 2)

	res, err := h.c.EnableDataQueryStats(h.ctx, testSystem, db)
	if err != nil || res.State != "staged" {
		t.Fatalf("enable: %+v %v", res, err)
	}
	ss, err := h.c.loadService(h.ctx, 1, db)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := pendingSettings(ss.svc)
	if err != nil || pending == nil || pending.StartCommand != old+" "+queryStatsFlags || loadsQueryStats(ss.svc.StartCommand) {
		t.Fatalf("pending %+v, live %q, %v", pending, ss.svc.StartCommand, err)
	}
	if res, err := h.c.EnableDataQueryStats(h.ctx, testSystem, db); err != nil || res.State != "staged" || !strings.Contains(res.Detail, "already waits") {
		t.Fatalf("enable again: %+v %v", res, err)
	}
}
