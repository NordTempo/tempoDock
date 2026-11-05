package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// Single sign-on with OpenID Connect (ADR 0058): an org's providers and
// sso-only switch under /org, and signing in through them under
// /auth/sso.

// ssoCookie binds a single sign-on in flight to the browser that started
// it. SameSite=Lax: the provider sends the browser back with a cross-site
// top-level GET, which Strict cookies would miss.
const ssoCookie = "dock_sso"

// ssoCookie sets (value) or clears ("") the browser binding cookie, sent
// only to the single sign-on endpoints.
func (c client) ssoCookie(value string) http.Cookie {
	ck := http.Cookie{
		Name: ssoCookie, Value: value, Path: Prefix + "/auth/sso", Expires: time.Now().Add(core.SSOTTL),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: c.secure,
	}
	if value == "" {
		ck.Expires, ck.MaxAge = time.Time{}, -1
	}
	return ck
}

type ssoStartInput struct {
	Body   apitypes.SSOStartRequest
	client client
}

// Resolve records how the request arrived.
func (in *ssoStartInput) Resolve(ctx huma.Context) []error {
	in.client = clientOf(ctx)
	return nil
}

type ssoStartOutput struct {
	SetCookie []http.Cookie `header:"Set-Cookie"`
	Body      apitypes.SSOStart
}

type ssoCallbackInput struct {
	State   string `query:"state"`
	Code    string `query:"code"`
	Error   string `query:"error"`
	Binding string `cookie:"dock_sso"`
	client  client
}

// Resolve records how the request arrived.
func (in *ssoCallbackInput) Resolve(ctx huma.Context) []error {
	in.client = clientOf(ctx)
	return nil
}

// ssoCallbackOutput sends the browser on: into Dock with its session, to
// the second factor step, or back to the sign-in page with the reason.
type ssoCallbackOutput struct {
	Status    int
	Location  string        `header:"Location"`
	SetCookie []http.Cookie `header:"Set-Cookie"`
}

// ssoLanding is where a finished single sign-on sends the browser, with
// its cookies: the session (or the pending sign-in waiting for a second
// factor), and the binding cookie cleared either way.
func (c client) ssoLanding(res core.SSOResult) *ssoCallbackOutput {
	out := &ssoCallbackOutput{Status: http.StatusSeeOther, Location: res.To}
	if res.Pending {
		q := url.Values{"second": {"1"}}
		if res.To != "/" {
			q.Set("redirect", res.To)
		}
		out.Location = "/login?" + q.Encode()
		out.SetCookie = []http.Cookie{c.pendingCookie(res.Secret, res.Expires), c.ssoCookie("")}
		return out
	}
	out.SetCookie = []http.Cookie{c.sessionCookie(res.Secret, res.Expires), c.pendingCookie("", time.Time{}), c.ssoCookie("")}
	return out
}

func registerSSO(s *Server, a huma.API) {
	public(a, huma.Operation{
		OperationID: "sso-start", Method: http.MethodPost, Path: Prefix + "/auth/sso/start",
		Summary: "Start signing in with single sign-on",
		Description: "Finds the organization's identity provider by the email's domain (or the org and provider named) and returns its sign-in URL to send the browser to, " +
			"with a cookie binding the sign-in to this browser. When several organizations sign that domain in, returns them to choose from instead; start again naming one.",
		Tags: []string{"auth"}, Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusTooManyRequests, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *ssoStartInput) (*ssoStartOutput, error) {
		res, err := s.core.StartSSO(ctx, in.Body, in.client.ip)
		if err != nil {
			return nil, s.problem(err)
		}
		out := &ssoStartOutput{Body: res.SSOStart}
		if res.Binding != "" {
			out.SetCookie = []http.Cookie{in.client.ssoCookie(res.Binding)}
		}
		return out, nil
	})
	public(a, huma.Operation{
		OperationID: "sso-callback", Method: http.MethodGet, Path: core.SSOCallbackPath,
		Summary: "Finish signing in with single sign-on",
		Description: "The identity provider sends the browser here (the redirect URL to register at it). Redirects to the page the sign-in started from with the session cookie, " +
			"to /login?second=1 when the user has a second factor, or to /login?error=<reason>.",
		Tags: []string{"auth"}, DefaultStatus: http.StatusSeeOther,
	}, func(ctx context.Context, in *ssoCallbackInput) (*ssoCallbackOutput, error) {
		res, err := s.core.FinishSSO(ctx, core.SSOCallback{State: in.State, Code: in.Code, Error: in.Error, Binding: in.Binding}, in.client.ip)
		if err != nil {
			reason := "signing in failed; try again"
			if ae, ok := errors.AsType[*apitypes.Error](s.problem(err)); ok && ae.Code != apitypes.CodeInternal {
				reason = ae.Detail
			}
			return &ssoCallbackOutput{
				Status: http.StatusSeeOther, Location: "/login?" + url.Values{"error": {reason}}.Encode(),
				SetCookie: []http.Cookie{in.client.ssoCookie("")},
			}, nil
		}
		return in.client.ssoLanding(res), nil
	})

	tags := []string{"organizations"}
	path := Prefix + "/org/sso-providers"
	authed(s, a, huma.Operation{
		OperationID: "list-sso-providers", Method: http.MethodGet, Path: path,
		Summary: "List the organization's single sign-on providers", Description: "Client secrets are never returned. Scope: admin, unlimited.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.SSOProvider], error) {
		return reply(s.core.ListSSOProviders(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-sso-provider", Method: http.MethodPost, Path: path,
		Summary: "Add a single sign-on provider",
		Description: "An OpenID Connect provider for people with the allowed email domains. Its issuer must answer discovery. " +
			"Register the returned redirect_url at the provider. Scope: admin, unlimited, recently confirmed.",
		Tags: tags, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.CreateSSOProviderRequest
	}) (*out[apitypes.SSOProvider], error) {
		return reply(s.core.CreateSSOProvider(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-sso-provider", Method: http.MethodPatch, Path: path + "/{name}",
		Summary: "Change a single sign-on provider", Description: "Fields left out stay. Scope: admin, unlimited, recently confirmed.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Name string `path:"name"`
		Body apitypes.UpdateSSOProviderRequest
	}) (*out[apitypes.SSOProvider], error) {
		return reply(s.core.UpdateSSOProvider(ctx, actor, in.Name, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "delete-sso-provider", Method: http.MethodDelete, Path: path + "/{name}",
		Summary: "Remove a single sign-on provider",
		Description: "With the identities linked through it; people keep their accounts and memberships. The last provider of an sso-only organization stays until that is off. " +
			"Scope: admin, unlimited, recently confirmed.",
		Tags: tags, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Name string `path:"name"`
	}) (*struct{}, error) {
		return nil, s.core.DeleteSSOProvider(ctx, actor, in.Name)
	})
	authed(s, a, huma.Operation{
		OperationID: "set-org-sso-only", Method: http.MethodPut, Path: Prefix + "/org/sso-only",
		Summary: "Require members to sign in with single sign-on",
		Description: "Members act in the organization only with a sign-in through one of its providers; API tokens are not affected. " +
			"Turning it on needs your own sign-in to have gone through one. Scope: admin, unlimited, recently confirmed.",
		Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.SetSSOOnlyRequest }) (*out[apitypes.Org], error) {
		return reply(s.core.SetOrgSSOOnly(ctx, actor, in.Body))
	})
}
