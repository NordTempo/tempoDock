package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// Alerts and notification channels (ADR 0020). All of it is the
// platform's, for operators (ADR 0027).

// ChannelPath addresses an alert channel.
type ChannelPath struct {
	Channel string `path:"channel" doc:"Channel name"`
}

const channelPathT = Prefix + "/alerts/channels/{channel}"

func registerAlerts(s *Server, a huma.API) {
	tags := []string{"alerts"}
	authed(s, a, huma.Operation{
		OperationID: "list-alerts", Method: http.MethodGet, Path: Prefix + "/alerts",
		Summary: "List alerts", Tags: tags,
		Description: "The open alerts, critical first, then newest first. With all, every alert including resolved ones, newest first and paged with before. Live changes arrive as `alert` events. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		All    bool  `query:"all" doc:"Include resolved alerts"`
		Before int64 `query:"before" doc:"With all: only alerts with a smaller ID (paging)"`
		Limit  int   `query:"limit" default:"100" minimum:"1" maximum:"500" doc:"With all: how many"`
	}) (*out[[]apitypes.Alert], error) {
		return reply(s.core.ListAlerts(ctx, actor, in.All, in.Limit, in.Before))
	})
	authed(s, a, huma.Operation{
		OperationID: "resolve-alert", Method: http.MethodPost, Path: Prefix + "/alerts/{id}/resolve",
		Summary: "Resolve an alert", Tags: tags,
		Description: "Closes an open alert by hand, for one whose condition won't clear by itself. If the condition is still there, a new alert opens. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID int64 `path:"id"`
	}) (*out[apitypes.Alert], error) {
		return reply(s.core.ResolveAlert(ctx, actor, in.ID))
	})

	authed(s, a, huma.Operation{
		OperationID: "list-alert-channels", Method: http.MethodGet, Path: Prefix + "/alerts/channels",
		Summary: "List alert channels", Tags: tags,
		Description: "Where alerts are sent, with each channel's last delivery and last failure. URLs, signing secrets and SMTP passwords are never returned. Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.AlertChannel], error) {
		return reply(s.core.ListAlertChannels(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-alert-channel", Method: http.MethodPost, Path: Prefix + "/alerts/channels",
		Summary: "Add an alert channel", Tags: tags, DefaultStatus: http.StatusCreated,
		Description: "A webhook (JSON, signed with HMAC-SHA256 in the Dock-Signature header), a Slack-compatible incoming webhook, or email over SMTP. A webhook's generated signing secret is returned once. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.CreateAlertChannelRequest
	}) (*out[apitypes.CreatedAlertChannel], error) {
		return reply(s.core.CreateAlertChannel(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-alert-channel", Method: http.MethodDelete, Path: channelPathT,
		Summary: "Remove an alert channel", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *ChannelPath) (*struct{}, error) {
		return nil, s.core.RemoveAlertChannel(ctx, actor, in.Channel)
	})
	authed(s, a, huma.Operation{
		OperationID: "test-alert-channel", Method: http.MethodPost, Path: channelPathT + "/test",
		Summary: "Send a test alert", Tags: tags,
		Description: "Sends a test notification once, without retries, and says whether it went through. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *ChannelPath) (*out[apitypes.AlertChannelTest], error) {
		return reply(s.core.TestAlertChannel(ctx, actor, in.Channel))
	})
}
