package redis

import (
	"context"
	"fmt"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// errNoRows: row changes never apply, Redis has keys.
var errNoRows = fmt.Errorf("%w: Redis has keys, not rows: send key_changes", data.ErrInvalid)

// Render refuses row changes (see RenderKeys).
func (e *Engine) Render(context.Context, data.Target, []apitypes.DataChange) (string, error) {
	return "", errNoRows
}

// Commit refuses row changes (see CommitKeys).
func (e *Engine) Commit(context.Context, data.Target, data.Call, []apitypes.DataChange) (data.CommitResult, error) {
	return data.CommitResult{}, errNoRows
}
