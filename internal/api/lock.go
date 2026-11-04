package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerLocks registers locking and unlocking environments and the
// org's lock settings (ADR 0044).
func registerLocks(s *Server, a huma.API) {
	tags := []string{"environments"}
	authed(s, a, huma.Operation{
		OperationID: "lock-environment", Method: http.MethodPost, Path: envPathT + "/lock",
		Summary: "Lock an environment",
		Description: "Tokens can't change anything in a locked environment; what they try becomes an access request an admin approves in the UI. " +
			"Locking an open environment closes it again. Scope: admin.",
		Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		EnvPath
		Body apitypes.LockEnvironmentRequest
	}) (*out[apitypes.Environment], error) {
		return reply(s.core.LockEnvironment(ctx, actor, in.ref(), in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "unlock-environment", Method: http.MethodPost, Path: envPathT + "/unlock",
		Summary: "Unlock an environment",
		Description: "Unlocks it, or with `minutes` opens it for that long, after which it locks again by itself. " +
			"The lock applies: a token asking to unlock gets an access request. Scope: admin.",
		Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		EnvPath
		Body apitypes.UnlockEnvironmentRequest
	}) (*out[apitypes.Environment], error) {
		return reply(s.core.UnlockEnvironment(ctx, actor, in.ref(), in.Body))
	})
	orgTags := []string{"organizations"}
	authed(s, a, huma.Operation{
		OperationID: "get-org-lock-settings", Method: http.MethodGet, Path: Prefix + "/org/lock-settings",
		Summary: "Lock settings", Description: "Whether new production environments start locked. Scope: read.", Tags: orgTags,
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.OrgLockSettings], error) {
		return reply(s.core.GetOrgLockSettings(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-org-lock-settings", Method: http.MethodPut, Path: Prefix + "/org/lock-settings",
		Summary: "Change lock settings", Description: "Existing environments keep their lock. Scope: admin, unlimited.", Tags: orgTags,
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.OrgLockSettings }) (*out[apitypes.OrgLockSettings], error) {
		return reply(s.core.SetOrgLockSettings(ctx, actor, in.Body))
	})
}
