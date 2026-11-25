package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// Alerts and notification channels (ADR 0020). An alert about a project or
// service is its org's, for its members; channels are an org's, for its
// admins. With platform=true, the platform's alerts and channels, for
// operators (ADR 0062).

// ChannelPath addresses an alert channel.
type ChannelPath struct {
	Channel string `path:"channel" doc:"Channel name"`
}

const channelPathT = Prefix + "/alerts/channels/{channel}"

// PlatformQuery picks the platform's alerts or channels instead of the
// org's.
type PlatformQuery struct {
	Platform bool `query:"platform" doc:"The platform's alerts or channels (servers, Dock itself) instead of the organization's. Operators only."`
}

// ChannelInput addresses a channel of the org or the platform.
type ChannelInput struct {
	ChannelPath
	PlatformQuery
}

func registerAlerts(s *Server, a huma.API) {
	tags := []string{"alerts"}
	authed(s, a, huma.Operation{
		OperationID: "list-alerts", Method: http.MethodGet, Path: Prefix + "/alerts",
		Summary: "List alerts", Tags: tags,
		Description: "The organization's open alerts (its projects and services), critical first, then newest first; with platform, the platform's (servers, Dock itself), for operators. With all, every alert including resolved ones, newest first and paged with before. Live changes arrive as `alert` events.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		PlatformQuery
		All    bool  `query:"all" doc:"Include resolved alerts"`
		Before int64 `query:"before" doc:"With all: only alerts with a smaller ID (paging)"`
		Limit  int   `query:"limit" default:"100" minimum:"1" maximum:"500" doc:"With all: how many"`
	}) (*out[[]apitypes.Alert], error) {
		return reply(s.core.ListAlerts(ctx, actor, in.Platform, in.All, in.Limit, in.Before))
	})
	authed(s, a, huma.Operation{
		OperationID: "resolve-alert", Method: http.MethodPost, Path: Prefix + "/alerts/{id}/resolve",
		Summary: "Resolve an alert", Tags: tags,
		Description: "Closes an open alert by hand, for one whose condition won't clear by itself. If the condition is still there, a new alert opens. An organization's alert needs deploy there; a platform alert an operator.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID int64 `path:"id"`
	}) (*out[apitypes.Alert], error) {
		return reply(s.core.ResolveAlert(ctx, actor, in.ID))
	})

	authed(s, a, huma.Operation{
		OperationID: "list-alert-channels", Method: http.MethodGet, Path: Prefix + "/alerts/channels",
		Summary: "List alert channels", Tags: tags,
		Description: "Where the organization's alerts are sent (with platform, the platform's), with each channel's last delivery and last failure. URLs, signing secrets and SMTP passwords are never returned. Organization admins; operators for the platform's.",
	}, func(ctx context.Context, actor core.Actor, in *PlatformQuery) (*out[[]apitypes.AlertChannel], error) {
		return reply(s.core.ListAlertChannels(ctx, actor, in.Platform))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-alert-channel", Method: http.MethodPost, Path: Prefix + "/alerts/channels",
		Summary: "Add an alert channel", Tags: tags, DefaultStatus: http.StatusCreated,
		Description: "A webhook (JSON, signed with HMAC-SHA256 in the Dock-Signature header), a Slack-compatible incoming webhook, a Discord webhook, Pushover (a user or group key and an application token), or email over SMTP, for the organization's alerts (with platform, the platform's). A webhook's generated signing secret is returned once. Organization admins; operators for the platform's.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		PlatformQuery
		Body apitypes.CreateAlertChannelRequest
	}) (*out[apitypes.CreatedAlertChannel], error) {
		return reply(s.core.CreateAlertChannel(ctx, actor, in.Platform, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-alert-channel", Method: http.MethodDelete, Path: channelPathT,
		Summary: "Remove an alert channel", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "Organization admins; operators for the platform's.",
	}, func(ctx context.Context, actor core.Actor, in *ChannelInput) (*struct{}, error) {
		return nil, s.core.RemoveAlertChannel(ctx, actor, in.Platform, in.Channel)
	})
	authed(s, a, huma.Operation{
		OperationID: "test-alert-channel", Method: http.MethodPost, Path: channelPathT + "/test",
		Summary: "Send a test alert", Tags: tags,
		Description: "Sends a test notification once, without retries, and says whether it went through. Organization admins; operators for the platform's.",
	}, func(ctx context.Context, actor core.Actor, in *ChannelInput) (*out[apitypes.AlertChannelTest], error) {
		return reply(s.core.TestAlertChannel(ctx, actor, in.Platform, in.Channel))
	})
}
