package core

import (
	"context"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Promote starts promoting the images running in req.From to req.To in
// the same project (ADR 0014 section 4): per service, a release in To with
// the same image (by registry digest) and To's own variables and settings,
// deployed through the normal health-checked path, one at a time. Nothing
// is rebuilt. Services missing in To, without an active release in From,
// or whose image can't reach To's server are skipped and listed. Requires
// deploy on To (and read on From).
func (c *Core) Promote(ctx context.Context, a Actor, project string, req apitypes.PromoteRequest) (apitypes.PromoteResult, error) {
	return apitypes.PromoteResult{}, errf(apitypes.CodeInternal, "promote is not implemented yet")
}
