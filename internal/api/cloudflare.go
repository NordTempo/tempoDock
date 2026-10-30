package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// CloudflarePath names a Cloudflare connection.
type CloudflarePath struct {
	Cloudflare string `path:"cloudflare" doc:"Cloudflare connection name"`
}

// registerCloudflare registers the Cloudflare connections' endpoints
// (ADR 0035).
func registerCloudflare(s *Server, a huma.API) {
	tags := []string{"cloudflare"}
	const one = Prefix + "/cloudflare/{cloudflare}"
	authed(s, a, huma.Operation{
		OperationID: "list-cloudflare", Method: http.MethodGet, Path: Prefix + "/cloudflare",
		Summary: "List Cloudflare connections", Tags: tags,
		Description: "The API tokens Dock runs tunnels and routes domains with, their zones and the servers whose tunnels they run. Tokens are never returned. Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.Cloudflare], error) {
		return reply(s.core.ListCloudflare(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-cloudflare", Method: http.MethodPost, Path: Prefix + "/cloudflare",
		Summary: "Add a Cloudflare connection", Tags: tags, DefaultStatus: http.StatusCreated,
		Description: "Dock checks the token (active, reaches the account and its tunnels, lists zones) before saving it, so a mistake fails here (400, saying what to fix). " +
			"Zones whose DNS records the token can't read are left out. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.CreateCloudflareRequest
	}) (*out[apitypes.Cloudflare], error) {
		return reply(s.core.AddCloudflare(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-cloudflare", Method: http.MethodPatch, Path: one,
		Summary: "Give a Cloudflare connection a new token", Tags: tags,
		Description: "Checked like a new one; its zones are listed again. A connection whose token Cloudflare refused works again. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		CloudflarePath
		Body apitypes.UpdateCloudflareRequest
	}) (*out[apitypes.Cloudflare], error) {
		return reply(s.core.UpdateCloudflare(ctx, actor, in.Cloudflare, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "refresh-cloudflare", Method: http.MethodPost, Path: one + "/refresh",
		Summary: "List a Cloudflare connection's zones again", Tags: tags,
		Description: "After zones were added to or taken from its token in Cloudflare. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *CloudflarePath) (*out[apitypes.Cloudflare], error) {
		return reply(s.core.RefreshCloudflare(ctx, actor, in.Cloudflare))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-cloudflare", Method: http.MethodDelete, Path: one,
		Summary: "Remove a Cloudflare connection", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "Dock forgets the domains it routed with it; their records stay in Cloudflare. 409 while a server's tunnel uses it. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *CloudflarePath) (*struct{}, error) {
		return nil, s.core.RemoveCloudflare(ctx, actor, in.Cloudflare)
	})
	authed(s, a, huma.Operation{
		OperationID: "list-cloudflare-zones", Method: http.MethodGet, Path: Prefix + "/cloudflare/zones",
		Summary: "List the zones domains can be added from", Tags: tags,
		Description: "The zones of working Cloudflare connections, for adding a domain from Cloudflare. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.CloudflareZone], error) {
		return reply(s.core.ListCloudflareZones(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "list-cloudflare-records", Method: http.MethodGet, Path: Prefix + "/cloudflare/records",
		Summary: "List the domains Dock routes through tunnels", Tags: tags,
		Description: "Failed ones first; at most 500. Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.CloudflareRecord], error) {
		return reply(s.core.ListCloudflareRecords(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "retry-cloudflare", Method: http.MethodPost, Path: Prefix + "/cloudflare/retry",
		Summary: "Retry failed Cloudflare changes", Tags: tags,
		Description: "Every domain and tunnel that failed is tried again, in the background. Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.CloudflareRetried], error) {
		return reply(s.core.RetryCloudflare(ctx, actor))
	})
}
