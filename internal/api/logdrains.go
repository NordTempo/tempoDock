package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// Log drains (ADR 0046): where an environment's app output is sent.

// LogDrainPath addresses one of an environment's log drains.
type LogDrainPath struct {
	EnvPath
	ID int64 `path:"id" doc:"Log drain ID"`
}

const logDrainsPathT = envPathT + "/log-drains"

func registerLogDrains(s *Server, a huma.API) {
	tags := []string{"logs"}
	authed(s, a, huma.Operation{
		OperationID: "list-log-drains", Method: http.MethodGet, Path: logDrainsPathT,
		Summary: "List log drains", Tags: tags,
		Description: "Where the environment's app output is sent, with lines sent and dropped, the last error and the last delivery since the daemon started. URLs, headers and tokens are never returned.",
	}, func(ctx context.Context, actor core.Actor, in *EnvPath) (*out[[]apitypes.LogDrain], error) {
		return reply(s.core.ListLogDrains(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "add-log-drain", Method: http.MethodPost, Path: logDrainsPathT,
		Summary: "Add a log drain", Tags: tags, DefaultStatus: http.StatusCreated,
		Description: "Sends the output of every service in the environment to an HTTP endpoint (JSON lines), a syslog server (RFC 5424 over tcp, tls or udp) or Loki, from now on. Admin.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		EnvPath
		Body apitypes.AddLogDrainRequest
	}) (*out[apitypes.LogDrain], error) {
		return reply(s.core.AddLogDrain(ctx, actor, in.ref(), in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-log-drain", Method: http.MethodDelete, Path: logDrainsPathT + "/{id}",
		Summary: "Remove a log drain", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "Lines it had queued are dropped. Admin.",
	}, func(ctx context.Context, actor core.Actor, in *LogDrainPath) (*struct{}, error) {
		return nil, s.core.RemoveLogDrain(ctx, actor, in.ref(), in.ID)
	})
	authed(s, a, huma.Operation{
		OperationID: "test-log-drain", Method: http.MethodPost, Path: logDrainsPathT + "/{id}/test",
		Summary: "Send a test line to a log drain", Tags: tags,
		Description: "Sends one line once, without retries, and says whether it went through. Admin.",
	}, func(ctx context.Context, actor core.Actor, in *LogDrainPath) (*out[apitypes.LogDrainTest], error) {
		return reply(s.core.TestLogDrain(ctx, actor, in.ref(), in.ID))
	})
}
