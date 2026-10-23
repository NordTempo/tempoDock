package api

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerAddress registers Dock's public address (ADR 0029).
func registerAddress(s *Server, a huma.API) {
	tags := []string{"address"}
	authed(s, a, huma.Operation{
		OperationID: "get-address", Method: http.MethodGet, Path: Prefix + "/address",
		Summary: "Show Dock's address", Tags: tags,
		Description: "Dock's public address, if one is set, with its DNS records and status, checked now (DNS, then the certificate, like a domain's); and its private address, which always works. Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.Address], error) {
		return reply(s.core.Address(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-address", Method: http.MethodPut, Path: Prefix + "/address",
		Summary: "Set Dock's public address", Tags: tags,
		Description: "Serves the API and UI at https://<hostname> through the control plane's Caddy, which gets a certificate once the hostname's DNS points at the server. 409 `hostname_taken` if a service's domain or the webhook hostname uses it. Operators only.",
		Errors:      []int{http.StatusBadRequest, http.StatusConflict},
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.SetAddressRequest }) (*out[apitypes.Address], error) {
		return reply(s.core.SetAddress(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-address", Method: http.MethodDelete, Path: Prefix + "/address",
		Summary: "Remove Dock's public address", Tags: tags,
		Description: "Stops serving Dock at its public address; sessions there end. The private address keeps working. Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.Address], error) {
		return reply(s.core.RemoveAddress(ctx, actor))
	})
}

// Public wraps the API and UI handler for [public_listen], where only the
// control plane's Caddy connects, proxying Dock's public address. Caddy
// sets core.ClientIPHeader to the client's address, so login rate limits
// and tunnels see the client, not Caddy. Other listeners never read it.
// GitHub's webhooks (/hooks/github/) go to hooks, so a public address is
// all push-to-deploy needs (ADR 0031).
func Public(h, hooks http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ip := net.ParseIP(r.Header.Get(core.ClientIPHeader)); ip != nil {
			r.RemoteAddr = net.JoinHostPort(ip.String(), "0")
		}
		r.Header.Del(core.ClientIPHeader)
		if strings.HasPrefix(r.URL.Path, hooksPath) {
			hooks.ServeHTTP(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}
