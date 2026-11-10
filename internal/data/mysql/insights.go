package mysql

import (
	"context"
	"fmt"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// errNoHealth refuses the Health tab's calls until MySQL's insights land
// (ADR 0072, phase 3).
var errNoHealth = fmt.Errorf("%w: database health isn't available for MySQL and MariaDB yet", data.ErrInvalid)

// Insights isn't available yet.
func (e *Engine) Insights(context.Context, data.Target, data.Call, apitypes.DataInsightsRequest) (apitypes.DataInsights, error) {
	return apitypes.DataInsights{}, errNoHealth
}

// Pressure isn't available yet.
func (e *Engine) Pressure(context.Context, data.Target) (apitypes.DataPressure, error) {
	return apitypes.DataPressure{}, errNoHealth
}

// EnableQueryStats isn't available yet.
func (e *Engine) EnableQueryStats(context.Context, data.Target, data.Credentials) (apitypes.DataQueryStatsState, bool, error) {
	return "", false, errNoHealth
}

// ResetQueryStats isn't available yet.
func (e *Engine) ResetQueryStats(context.Context, data.Target, data.Credentials) error {
	return errNoHealth
}

// Cancel isn't available yet.
func (e *Engine) Cancel(context.Context, data.Target, data.Credentials, string, *time.Time, bool) (data.CancelResult, error) {
	return data.CancelResult{}, errNoHealth
}

// EnsureMonitor has nothing to create yet.
func (e *Engine) EnsureMonitor(context.Context, data.Target, data.Credentials) error { return nil }
