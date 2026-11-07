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

// Single sign-on (ADR 0058, 0065): the platform's identity providers and
// sign-in switches under /platform, what the sign-in page offers at
// /auth/methods, and signing in through a provider under /auth/sso.

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

// blockedLanding is where an invite-only Dock sends a person a provider
// signed in who has no account and no invite: a page that says so.
func blockedLanding(b *core.SSOBlocked) string {
	return "/invite-only?" + url.Values{"provider": {b.Provider}, "email": {b.Email}}.Encode()
}

func registerSSO(s *Server, a huma.API) {
	public(a, huma.Operation{
		OperationID: "sign-in-methods", Method: http.MethodGet, Path: Prefix + "/auth/methods",
		Summary:     "How people sign in",
		Description: "The sign-in page's buttons, one per identity provider, and whether it takes passwords.",
		Tags:        []string{"auth"},
	}, func(ctx context.Context, _ *struct{}) (*out[apitypes.SignInMethods], error) {
		res, err := s.core.SignInMethods(ctx)
		if err != nil {
			return nil, s.problem(err)
		}
		return &out[apitypes.SignInMethods]{Body: res}, nil
	})
	public(a, huma.Operation{
		OperationID: "sso-start", Method: http.MethodPost, Path: Prefix + "/auth/sso/start",
		Summary:     "Start signing in through an identity provider",
		Description: "Returns the provider's sign-in URL to send the browser to, with a cookie binding the sign-in to this browser.",
		Tags:        []string{"auth"}, Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusTooManyRequests, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *ssoStartInput) (*ssoStartOutput, error) {
		res, err := s.core.StartSSO(ctx, in.Body, in.client.ip)
		if err != nil {
			return nil, s.problem(err)
		}
		return &ssoStartOutput{Body: res.SSOStart, SetCookie: []http.Cookie{in.client.ssoCookie(res.Binding)}}, nil
	})
	public(a, huma.Operation{
		OperationID: "sso-callback", Method: http.MethodGet, Path: core.SSOCallbackPath,
		Summary: "Finish signing in through an identity provider",
		Description: "The identity provider sends the browser here (the redirect URL to register at it). Redirects to the page the sign-in started from with the session cookie, " +
			"to /login?second=1 when the user has a second factor, to /invite-only when this Dock has no account for them and they have no invite, or to /login?error=<reason>.",
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
		if res.Blocked != nil {
			return &ssoCallbackOutput{Status: http.StatusSeeOther, Location: blockedLanding(res.Blocked), SetCookie: []http.Cookie{in.client.ssoCookie("")}}, nil
		}
		return in.client.ssoLanding(res), nil
	})

	tags := []string{"platform"}
	path := Prefix + "/platform/sso-providers"
	authed(s, a, huma.Operation{
		OperationID: "list-sso-providers", Method: http.MethodGet, Path: path,
		Summary:     "List the identity providers",
		Description: "With the redirect URL to register at each. Client secrets are never returned. Operators only.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.SSOProviders], error) {
		return reply(s.core.ListSSOProviders(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-sso-provider", Method: http.MethodPost, Path: path,
		Summary: "Add an identity provider",
		Description: "Google, GitHub, or any OpenID Connect provider by its issuer, which must answer discovery. " +
			"Register the redirect URL at the provider. Operators only, recently confirmed.",
		Tags: tags, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.CreateSSOProviderRequest
	}) (*out[apitypes.SSOProvider], error) {
		return reply(s.core.CreateSSOProvider(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-sso-provider", Method: http.MethodPatch, Path: path + "/{name}",
		Summary: "Change an identity provider", Description: "Fields left out stay. Operators only, recently confirmed.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Name string `path:"name"`
		Body apitypes.UpdateSSOProviderRequest
	}) (*out[apitypes.SSOProvider], error) {
		return reply(s.core.UpdateSSOProvider(ctx, actor, in.Name, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "delete-sso-provider", Method: http.MethodDelete, Path: path + "/{name}",
		Summary: "Remove an identity provider",
		Description: "With the identities linked through it and the sessions signed in through it; people keep their accounts and memberships. " +
			"The last provider stays while password sign-in is off. Operators only, recently confirmed.",
		Tags: tags, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Name string `path:"name"`
	}) (*struct{}, error) {
		return nil, s.core.DeleteSSOProvider(ctx, actor, in.Name)
	})
	authed(s, a, huma.Operation{
		OperationID: "get-sign-in-settings", Method: http.MethodGet, Path: Prefix + "/platform/sign-in",
		Summary: "The platform's sign-in switches", Description: "Password sign-in and invite only. Operators only.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.SignInSettings], error) {
		return reply(s.core.SignInSettings(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-sign-in-settings", Method: http.MethodPatch, Path: Prefix + "/platform/sign-in",
		Summary: "Change the platform's sign-in switches",
		Description: "Fields left out stay. Turning password sign-in off needs an identity provider and your own sign-in to have gone through one. " +
			"Operators only, recently confirmed.",
		Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.SignInSettingsRequest
	}) (*out[apitypes.SignInSettings], error) {
		return reply(s.core.SetSignInSettings(ctx, actor, in.Body))
	})
}
