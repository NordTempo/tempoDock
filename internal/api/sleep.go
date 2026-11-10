package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerSleep registers putting environments to sleep, waking them and
// when they sleep on their own (ADR 0070).
func registerSleep(s *Server, a huma.API) {
	tags := []string{"environments"}
	authed(s, a, huma.Operation{
		OperationID: "sleep-environment", Method: http.MethodPost, Path: envPathT + "/sleep",
		Summary: "Put an environment to sleep",
		Description: "Stops its services' containers, keeping them; the next request to its domains, a deploy or a run wakes it. " +
			"Production environments never sleep. Scope: deploy.",
		Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *EnvPath) (*out[apitypes.Environment], error) {
		return reply(s.core.SleepEnvironment(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "wake-environment", Method: http.MethodPost, Path: envPathT + "/wake",
		Summary: "Wake an environment",
		Description: "Starts an asleep environment's services again and answers once each passed its health check. " +
			"Anyone who can see it can wake it, as opening its address does. Scope: read.",
		Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *EnvPath) (*out[apitypes.Environment], error) {
		return reply(s.core.WakeEnvironment(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-environment-sleep", Method: http.MethodPut, Path: envPathT + "/sleep",
		Summary: "Change when an environment sleeps",
		Description: "How many minutes without requests a non-production environment may go before it sleeps (5 to 1440); 0 turns it off. " +
			"Previews take their project's preview setting. Scope: admin.",
		Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		EnvPath
		Body apitypes.SetSleepRequest
	}) (*out[apitypes.Environment], error) {
		return reply(s.core.SetEnvironmentSleep(ctx, actor, in.ref(), in.Body))
	})
}
