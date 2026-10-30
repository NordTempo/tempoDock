package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

const (
	servicesPath = envPathT + "/services"
	servicePathT = servicesPath + "/{service}"
)

func registerServices(s *Server, a huma.API) {
	tags := []string{"services"}
	authed(s, a, huma.Operation{
		OperationID: "list-services", Method: http.MethodGet, Path: servicesPath,
		Summary: "List services", Description: "Includes archived services.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *EnvPath) (*out[[]apitypes.Service], error) {
		return reply(s.core.ListServices(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-service", Method: http.MethodPost, Path: servicesPath,
		Summary: "Create a service", Description: "Creates the service without deploying it. Scope: deploy.",
		Tags: tags, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		EnvPath
		Body apitypes.CreateServiceRequest
	}) (*out[apitypes.Service], error) {
		return reply(s.core.CreateService(ctx, actor, in.ref(), in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-service", Method: http.MethodGet, Path: servicePathT,
		Summary: "Get a service", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.Service], error) {
		return reply(s.core.GetService(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-service", Method: http.MethodPatch, Path: servicePathT,
		Summary: "Stage settings changes", Description: "Changed settings are staged; they go live on the next deploy of the service or when the environment's changes are applied (`apply: true` does that now). Scope: deploy.",
		Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.UpdateServiceRequest
	}) (*out[apitypes.ChangeResult], error) {
		return reply(s.core.UpdateService(ctx, actor, in.ref(), in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "archive-service", Method: http.MethodPost, Path: servicePathT + "/archive",
		Summary: "Archive a service", Description: "Removes its routes, then stops its container in the background. Volumes and history are kept. Scope: admin.",
		Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.Service], error) {
		return reply(s.core.ArchiveService(ctx, actor, in.ref()))
	})

	tags = []string{"volumes"}
	authed(s, a, huma.Operation{
		OperationID: "list-volumes", Method: http.MethodGet, Path: servicePathT + "/volumes",
		Summary: "List volumes", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[[]apitypes.Volume], error) {
		return reply(s.core.ListVolumes(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "add-volume", Method: http.MethodPost, Path: servicePathT + "/volumes",
		Summary: "Stage a volume", Description: "A service with a volume is stateful: deploys stop the old container before starting the new one. Volumes are never deleted as a side effect. Scope: deploy.",
		Tags: tags, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.AddVolumeRequest
	}) (*out[apitypes.ChangeResult], error) {
		return reply(s.core.AddVolume(ctx, actor, in.ref(), in.Body))
	})
}

func registerDomains(s *Server, a huma.API) {
	tags := []string{"domains"}
	authed(s, a, huma.Operation{
		OperationID: "list-domains", Method: http.MethodGet, Path: servicePathT + "/domains",
		Summary: "List domains", Description: "With status and the exact DNS records to create.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[[]apitypes.Domain], error) {
		return reply(s.core.ListDomains(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "add-domain", Method: http.MethodPost, Path: servicePathT + "/domains",
		Summary: "Add a domain", Description: "Any hostname, unique across all services. DNS can be set up before or after. With cloudflare, a hostname in a zone connected to Dock, which Dock routes through the server's Cloudflare tunnel (creating or adopting it) with a proxied CNAME (ADR 0036): on a direct server it is served to the tunnel only, next to the public IP; a record Dock didn't make at that name is refused (hostname_taken). Scope: deploy.",
		Tags: tags, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.AddDomainRequest
	}) (*out[apitypes.DomainChange], error) {
		return reply(s.core.AddDomain(ctx, actor, in.ref(), in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "generate-domain", Method: http.MethodPost, Path: servicePathT + "/domains/generate",
		Summary: "Generate a domain", Description: "A hostname under the server's base domain; no DNS work needed. Scope: deploy.",
		Tags: tags, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.DomainChange], error) {
		return reply(s.core.GenerateDomain(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-domain", Method: http.MethodDelete, Path: servicePathT + "/domains/{hostname}",
		Summary: "Remove a domain", Description: "Scope: deploy.", Tags: tags, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Hostname string `path:"hostname"`
	}) (*struct{}, error) {
		return nil, s.core.RemoveDomain(ctx, actor, in.ref(), in.Hostname)
	})

	tags = []string{"access"}
	authed(s, a, huma.Operation{
		OperationID: "get-access", Method: http.MethodGet, Path: servicePathT + "/access",
		Summary: "Who can open the service", Description: "Covers all of the service's domains (ADR 0030).", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.Access], error) {
		return reply(s.core.ServiceAccess(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-access", Method: http.MethodPut, Path: servicePathT + "/access",
		Summary: "Change who can open the service",
		Description: "public: anyone. password: HTTP basic auth with a username and password. " +
			"login: members signed in to Dock who can read the service. Takes effect at once on every domain of the service. Scope: deploy.",
		Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.SetAccessRequest
	}) (*out[apitypes.Access], error) {
		return reply(s.core.SetServiceAccess(ctx, actor, in.ref(), in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "grant-access", Method: http.MethodPost, Path: Prefix + "/access/grants",
		Summary:     "Open a login-protected domain",
		Description: "Returns the domain's sign-in URL with a one-minute grant for the caller. Dock's /access page sends the browser there. Scope: read on the service's environment.",
		Tags:        tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.AccessGrantRequest
	}) (*out[apitypes.AccessGrant], error) {
		return reply(s.core.GrantAccess(ctx, actor, in.Body))
	})
}
