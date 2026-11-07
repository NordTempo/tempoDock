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

// CloudflarePlatform picks the platform's Cloudflare connections instead
// of the org's (ADR 0062).
type CloudflarePlatform struct {
	Platform bool `query:"platform" doc:"The platform's connections (server tunnels, Dock's address, zones shared with organizations) instead of the organization's own. Operators only."`
}

// CloudflareInput addresses a connection of the org or, with platform,
// the platform's.
type CloudflareInput struct {
	CloudflarePath
	CloudflarePlatform
}

// registerCloudflare registers the Cloudflare connections' endpoints
// (ADR 0036). Connections are the org's, for its admins, or with
// platform=true the platform's, for operators (ADR 0062).
func registerCloudflare(s *Server, a huma.API) {
	tags := []string{"cloudflare"}
	const one = Prefix + "/cloudflare/{cloudflare}"
	const who = " Organization admins; operators for the platform's."
	authed(s, a, huma.Operation{
		OperationID: "list-cloudflare", Method: http.MethodGet, Path: Prefix + "/cloudflare",
		Summary: "List Cloudflare connections", Tags: tags,
		Description: "The organization's own API tokens Dock routes domains with (with platform, the platform's, which run server tunnels too), their zones and, for the platform's, the servers whose tunnels they run. Tokens are never returned." + who,
	}, func(ctx context.Context, actor core.Actor, in *CloudflarePlatform) (*out[[]apitypes.Cloudflare], error) {
		return reply(s.core.ListCloudflare(ctx, actor, in.Platform))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-cloudflare", Method: http.MethodPost, Path: Prefix + "/cloudflare",
		Summary: "Add a Cloudflare connection", Tags: tags, DefaultStatus: http.StatusCreated,
		Description: "Dock checks the token (active, reaches the account and its tunnels, lists zones) before saving it, so a mistake fails here (400, saying what to fix). " +
			"Zones whose DNS records the token can't read are left out, and for an organization's own connection zones Cloudflare doesn't serve yet; a zone connected elsewhere is refused (409). " +
			"A platform connection may be shared: organizations' domains may then use its zones." + who,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		CloudflarePlatform
		Body apitypes.CreateCloudflareRequest
	}) (*out[apitypes.Cloudflare], error) {
		return reply(s.core.AddCloudflare(ctx, actor, in.Platform, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-cloudflare", Method: http.MethodPatch, Path: one,
		Summary: "Give a Cloudflare connection a new token, or share it", Tags: tags,
		Description: "A new token is checked like a new one; its zones are listed again and its failed domains tried again. A connection whose token Cloudflare refused works again. " +
			"shared (platform connections only) lets organizations' domains use its zones; turned off, domains already in them keep working." + who,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		CloudflareInput
		Body apitypes.UpdateCloudflareRequest
	}) (*out[apitypes.Cloudflare], error) {
		return reply(s.core.UpdateCloudflare(ctx, actor, in.Platform, in.Cloudflare, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "refresh-cloudflare", Method: http.MethodPost, Path: one + "/refresh",
		Summary: "List a Cloudflare connection's zones again", Tags: tags,
		Description: "After zones were added to or taken from its token in Cloudflare, or a permission was fixed; its failed domains are tried again." + who,
	}, func(ctx context.Context, actor core.Actor, in *CloudflareInput) (*out[apitypes.Cloudflare], error) {
		return reply(s.core.RefreshCloudflare(ctx, actor, in.Platform, in.Cloudflare))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-cloudflare", Method: http.MethodDelete, Path: one,
		Summary: "Remove a Cloudflare connection", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "Dock forgets the domains it routed with it; their records stay in Cloudflare. 409 while a server's tunnel uses it." + who,
	}, func(ctx context.Context, actor core.Actor, in *CloudflareInput) (*struct{}, error) {
		return nil, s.core.RemoveCloudflare(ctx, actor, in.Platform, in.Cloudflare)
	})
	authed(s, a, huma.Operation{
		OperationID: "list-cloudflare-zones", Method: http.MethodGet, Path: Prefix + "/cloudflare/zones",
		Summary: "List the zones domains can be added from", Tags: tags,
		Description: "The zones of the organization's own Cloudflare connections and of those the platform shares, for adding a domain from Cloudflare. Scope: deploy.",
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
