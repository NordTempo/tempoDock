package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// sessionCookie holds the UI session secret (HttpOnly, SameSite=Strict),
// as __Host-dock_session over HTTPS (cookies.go).
const sessionCookie = "dock_session"

type actorKey struct{}

func actorFrom(ctx context.Context) core.Actor {
	a, _ := ctx.Value(actorKey{}).(core.Actor)
	return a
}

// orgHeader names the org like the `org` parameter does. The web UI sends
// it: adding a parameter means rebuilding the browser's request, which
// loses its body.
const orgHeader = "Dock-Org"

// authenticate resolves `Authorization: Bearer <token>` or the session
// cookie to an actor for every non-public operation, acting in the org the
// `org` parameter (or the Dock-Org header) names (ADR 0027).
func (s *Server) authenticate(a huma.API) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if op := ctx.Operation(); op != nil && op.Metadata["public"] == true {
			next(ctx)
			return
		}
		var (
			actor core.Actor
			err   error
		)
		org := ctx.Query("org")
		if org == "" {
			org = ctx.Header(orgHeader)
		}
		if h := ctx.Header("Authorization"); h != "" {
			token, ok := strings.CutPrefix(h, "Bearer ")
			if !ok {
				_ = huma.WriteErr(a, ctx, http.StatusUnauthorized, "Authorization header must be `Bearer <token>`")
				return
			}
			actor, err = s.core.AuthenticateToken(ctx.Context(), strings.TrimSpace(token), org)
		} else if secret, legacy := clientOf(ctx).session(humaCookies(ctx)); secret != "" {
			actor, err = s.core.AuthenticateSession(ctx.Context(), secret, org, legacy)
		} else {
			_ = huma.WriteErr(a, ctx, http.StatusUnauthorized, "authentication required: send `Authorization: Bearer <token>` or log in")
			return
		}
		if err != nil {
			// Unknown orgs and tokens of another org are 404 and 403.
			ae, _ := errors.AsType[*apitypes.Error](s.problem(err))
			_ = huma.WriteErr(a, ctx, ae.Status, ae.Detail)
			return
		}
		ctx = huma.WithValue(ctx, actorKey{}, actor)
		if r := strings.TrimSpace(ctx.Header(reasonHeader)); r != "" {
			ctx = huma.WithContext(ctx, core.WithReason(ctx.Context(), r[:min(len(r), maxReason)]))
		}
		next(ctx)
	}
}

// reasonHeader carries why a caller makes a change (`dock --reason`); an
// access request the change files shows it (ADR 0044).
const reasonHeader = "Dock-Reason"

// maxReason caps a reason's length in bytes.
const maxReason = 500

// pendingCookie holds a password sign-in waiting for its second factor
// (ADR 0057), sent only to the sign-in endpoints over plain HTTP.
const pendingCookie = "dock_login"

// pendingCookie sets (secret) or clears ("") the pending sign-in cookie.
func (c client) pendingCookie(secret string, expires time.Time) http.Cookie {
	return c.cookie(pendingCookie, secret, Prefix+"/auth", expires, http.SameSiteStrictMode)
}

// signedIn answers a sign-in step: the session cookie and who the caller
// is, or the pending sign-in's cookie and the factors to finish it with.
func (c client) signedIn(res core.LoginResult) *loginOutput {
	if res.Pending {
		return &loginOutput{
			SetCookie: []http.Cookie{c.pendingCookie(res.Secret, res.Expires)},
			Body:      apitypes.LoginResult{SecondFactor: res.Factors},
		}
	}
	me := res.Actor.Me()
	return &loginOutput{
		SetCookie: append(c.sessionCookies(res.Secret, res.Expires), c.pendingCookie("", time.Time{})),
		Body:      apitypes.LoginResult{Me: &me},
	}
}

type loginInput struct {
	Body   apitypes.LoginRequest
	client client
}

// Resolve records how the request arrived.
func (in *loginInput) Resolve(ctx huma.Context) []error {
	in.client = clientOf(ctx)
	return nil
}

type passwordInput struct {
	Body   apitypes.ChangePasswordRequest
	client client
}

// Resolve records how the request arrived.
func (in *passwordInput) Resolve(ctx huma.Context) []error {
	in.client = clientOf(ctx)
	return nil
}

// loginOutput sets the session cookie, or the pending sign-in's while a
// second factor is needed, and clears the other.
type loginOutput struct {
	SetCookie []http.Cookie `header:"Set-Cookie"`
	Body      apitypes.LoginResult
}

// passwordOutput sets the caller's fresh session cookie, if it has one.
type passwordOutput struct {
	SetCookie []http.Cookie `header:"Set-Cookie"`
}

// logoutInput is the session cookie to end, if the caller signed in with
// one.
type logoutInput struct {
	session string
	client  client
}

// Resolve reads the session cookie.
func (in *logoutInput) Resolve(ctx huma.Context) []error {
	in.client = clientOf(ctx)
	in.session, _ = in.client.session(humaCookies(ctx))
	return nil
}

type logoutOutput struct {
	SetCookie []http.Cookie `header:"Set-Cookie"`
	Body      apitypes.LoggedOut
}

func registerAuth(s *Server, a huma.API) {
	public(a, huma.Operation{
		OperationID: "login", Method: http.MethodPost, Path: Prefix + "/auth/login",
		Summary: "Log in to the UI", Description: "Sets an HttpOnly session cookie, or, for a user with a second factor, a short-lived cookie for the pending sign-in and lists the factors to finish it with at POST /auth/login/verify.", Tags: []string{"auth"},
		Errors: []int{http.StatusUnauthorized, http.StatusTooManyRequests},
	}, func(ctx context.Context, in *loginInput) (*loginOutput, error) {
		res, err := s.core.Login(ctx, in.Body.Email, in.Body.Password, in.client.ip)
		if err != nil {
			return nil, s.problem(err)
		}
		return in.client.signedIn(res), nil
	})
	authed(s, a, huma.Operation{
		OperationID: "change-password", Method: http.MethodPost, Path: Prefix + "/me/password",
		Summary: "Change your password", Description: "Users, from a UI session or a `dock login` token (API tokens have no password). Ends all your sessions and revokes your other `dock login` tokens; a session caller gets a fresh session cookie, a token caller keeps its token.",
		Tags: []string{"auth"}, DefaultStatus: http.StatusNoContent,
		Errors: []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusTooManyRequests},
	}, func(ctx context.Context, actor core.Actor, in *passwordInput) (*passwordOutput, error) {
		secret, expires, err := s.core.ChangePassword(ctx, actor, in.Body, in.client.ip)
		if err != nil {
			return nil, err
		}
		out := &passwordOutput{}
		if secret != "" { // a session caller; tokens get no session
			out.SetCookie = in.client.sessionCookies(secret, expires)
		}
		return out, nil
	})
	authed(s, a, huma.Operation{
		OperationID: "logout", Method: http.MethodPost, Path: Prefix + "/auth/logout",
		Summary: "End the UI session",
		Description: "A session that signed in through an OpenID Connect provider with an end-session endpoint answers with end_session_url: " +
			"send the browser there to sign out of the provider too; it comes back to the sign-in page.",
		Tags: []string{"auth"},
	}, func(ctx context.Context, _ core.Actor, in *logoutInput) (*logoutOutput, error) {
		out := &logoutOutput{SetCookie: in.client.sessionCookies("", time.Time{})}
		if in.session != "" {
			var err error
			if out.Body.EndSessionURL, err = s.core.Logout(ctx, in.session); err != nil {
				return nil, err
			}
		}
		return out, nil
	})
	authed(s, a, huma.Operation{
		OperationID: "update-me", Method: http.MethodPatch, Path: Prefix + "/me",
		Summary: "Change your name", Tags: []string{"auth"},
		Description: "Your display name, 1 to 64 characters (trimmed). Users, from a UI session or a `dock login` token; API tokens have no account. Answers with who you are now.",
		Errors:      []int{http.StatusBadRequest, http.StatusUnauthorized},
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.UpdateMeRequest }) (*out[apitypes.Me], error) {
		return reply(s.core.UpdateMe(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-my-email", Method: http.MethodGet, Path: Prefix + "/me/email",
		Summary: "Which optional mail you get", Tags: []string{"auth"},
		Description: "Mail you may turn off (ADR 0079): access requests waiting for you to decide. Users only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.MyEmail], error) {
		return reply(s.core.MyEmail(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-my-email", Method: http.MethodPut, Path: Prefix + "/me/email",
		Summary: "Turn optional mail on or off", Tags: []string{"auth"},
		Description: "Users only. Invites, password reset links and security notices can't be turned off.",
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.MyEmail }) (*out[apitypes.MyEmail], error) {
		return reply(s.core.SetMyEmail(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "me", Method: http.MethodGet, Path: Prefix + "/me",
		Summary: "Who the caller is and what it may do", Tags: []string{"auth"},
	}, func(_ context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.Me], error) {
		me := actor.Me()
		me.Demo = s.core.Demo()
		return reply(me, nil)
	})
}
