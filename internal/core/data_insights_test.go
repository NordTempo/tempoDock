package core

import (
	"context"
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
	h.opts.Data = pressureEngine{eng}
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
