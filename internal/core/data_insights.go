package core

import (
	"context"

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
