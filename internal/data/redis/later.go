package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// The Health tab arrives with ADR 0072's last PR; until then it is
// refused with these words. Row changes never apply: Redis has keys.
var (
	errNoRows   = fmt.Errorf("%w: Redis has keys, not rows: send key_changes", data.ErrInvalid)
	errNoHealth = fmt.Errorf("%w: database health isn't available for Redis and Valkey yet", data.ErrInvalid)
)

// Render refuses row changes (see RenderKeys).
func (e *Engine) Render(context.Context, data.Target, []apitypes.DataChange) (string, error) {
	return "", errNoRows
}

// Commit refuses row changes (see CommitKeys).
func (e *Engine) Commit(context.Context, data.Target, data.Call, []apitypes.DataChange) (data.CommitResult, error) {
	return data.CommitResult{}, errNoRows
}

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
