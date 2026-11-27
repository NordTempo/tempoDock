package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerCompose registers an environment's followed Compose file
// (ADR 0108): what it follows, following and unfollowing, and a sync now.
func registerCompose(s *Server, a huma.API) {
	tags := []string{"compose"}
	authed(s, a, huma.Operation{
		OperationID: "get-compose-source", Method: http.MethodGet, Path: envPathT + "/compose",
		Summary: "The Compose file an environment follows", Tags: tags,
		Description: "The repository, branch and path, and the last sync: its commit, when, and what came of it. `source` is null when the environment follows none. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *EnvPath) (*out[apitypes.ComposeSourceStatus], error) {
		return reply(s.core.ComposeSource(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "follow-compose", Method: http.MethodPut, Path: envPathT + "/compose",
		Summary: "Follow a Compose file in a GitHub repository", Tags: tags,
		Description: "The environment follows the file through the organization's GitHub App: a push to the branch that changes it is converted, planned and staged for review, as `import/compose` with `apply` and `stage_only`. Syncs the branch's head now and returns its plan. " +
			"`prune` stages an archive for each service the file drops (admin); `auto_apply` applies a sync that removes nothing when nothing else is staged and no secret needs a value. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		EnvPath
		Body apitypes.ComposeSourceRequest
	}) (*out[apitypes.ComposeSyncResult], error) {
		return reply(s.core.FollowCompose(ctx, actor, in.ref(), in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "unfollow-compose", Method: http.MethodDelete, Path: envPathT + "/compose",
		Summary: "Stop following a Compose file", Tags: tags,
		Description: "What earlier syncs staged stays staged. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *EnvPath) (*struct{}, error) {
		return nil, s.core.UnfollowCompose(ctx, actor, in.ref())
	})
	authed(s, a, huma.Operation{
		OperationID: "sync-compose", Method: http.MethodPost, Path: envPathT + "/compose/sync",
		Summary: "Sync a followed Compose file now", Tags: tags,
		Description: "Converts and plans the file at its branch's head and stages the result, as a push would. A failure is the sync's outcome, not an error. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *EnvPath) (*out[apitypes.ComposeSyncResult], error) {
		return reply(s.core.SyncCompose(ctx, actor, in.ref()))
	})
}
