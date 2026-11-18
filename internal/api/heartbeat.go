package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// The outside heartbeat (ADR 0097): one platform setting, for operators
// with platform.alerts. Its URL goes in and never comes out.

const heartbeatPath = Prefix + "/alerts/heartbeat"

func registerHeartbeat(s *Server, a huma.API) {
	tags := []string{"alerts"}
	authed(s, a, huma.Operation{
		OperationID: "get-heartbeat", Method: http.MethodGet, Path: heartbeatPath,
		Summary: "Show the outside heartbeat", Tags: tags,
		Description: "The URL's scheme and host, the interval, and how the last pings went. 404 while none is set. The URL itself is never returned. Operators.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.Heartbeat], error) {
		return reply(s.core.GetHeartbeat(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-heartbeat", Method: http.MethodPut, Path: heartbeatPath,
		Summary: "Set the outside heartbeat", Tags: tags,
		Description: "While the control plane runs it sends a GET to the URL every interval, so a monitor outside Dock raises the alarm when the pings stop. Three failed pings in a row open a platform alert. Replaces the heartbeat there was. Operators.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.SetHeartbeatRequest
	}) (*out[apitypes.Heartbeat], error) {
		return reply(s.core.SetHeartbeat(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-heartbeat", Method: http.MethodDelete, Path: heartbeatPath,
		Summary: "Remove the outside heartbeat", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "Pings stop: remove the check at its receiver too, or it raises the alarm. Operators.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*struct{}, error) {
		return nil, s.core.RemoveHeartbeat(ctx, actor)
	})
	authed(s, a, huma.Operation{
		OperationID: "test-heartbeat", Method: http.MethodPost, Path: heartbeatPath + "/test",
		Summary: "Send a heartbeat ping now", Tags: tags,
		Description: "Sends one ping and says whether it went through; the outcome is recorded like any ping's. Operators.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.HeartbeatTest], error) {
		return reply(s.core.TestHeartbeat(ctx, actor))
	})
}
