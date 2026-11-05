package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// sessionCookie holds the UI session secret (HttpOnly, SameSite=Strict).
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
		} else if c, cerr := huma.ReadCookie(ctx, sessionCookie); cerr == nil {
			actor, err = s.core.AuthenticateSession(ctx.Context(), c.Value, org)
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

// client is how a request arrived: over HTTPS (for the cookie's Secure
// flag) and from which IP (for login rate limits). Behind a reverse proxy
// the IP is the proxy's.
type client struct {
	secure bool
	ip     string
}

func clientOf(ctx huma.Context) client {
	ip := ctx.RemoteAddr()
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	return client{secure: ctx.TLS() != nil || ctx.Header("X-Forwarded-Proto") == "https", ip: ip}
}

func (c client) sessionCookie(secret string, expires time.Time) http.Cookie {
	return http.Cookie{
		Name: sessionCookie, Value: secret, Path: "/", Expires: expires,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: c.secure,
	}
}

// pendingCookie holds a password sign-in waiting for its second factor
// (ADR 0057), sent only to the sign-in endpoints.
const pendingCookie = "dock_login"

// pendingCookie sets (secret) or clears ("") the pending sign-in cookie.
func (c client) pendingCookie(secret string, expires time.Time) http.Cookie {
	ck := http.Cookie{
		Name: pendingCookie, Value: secret, Path: Prefix + "/auth", Expires: expires,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: c.secure,
	}
	if secret == "" {
		ck.Expires, ck.MaxAge = time.Time{}, -1
	}
	return ck
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
		SetCookie: []http.Cookie{c.sessionCookie(res.Secret, res.Expires), c.pendingCookie("", time.Time{})},
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
	SetCookie string `header:"Set-Cookie"`
}

type logoutInput struct {
	Session string `cookie:"dock_session"`
}

type logoutOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
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
			cookie := in.client.sessionCookie(secret, expires)
			out.SetCookie = cookie.String()
		}
		return out, nil
	})
	authed(s, a, huma.Operation{
		OperationID: "logout", Method: http.MethodPost, Path: Prefix + "/auth/logout",
		Summary: "End the UI session", Tags: []string{"auth"}, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, _ core.Actor, in *logoutInput) (*logoutOutput, error) {
		if in.Session != "" {
			if err := s.core.Logout(ctx, in.Session); err != nil {
				return nil, err
			}
		}
		return &logoutOutput{SetCookie: http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode}}, nil
	})
	authed(s, a, huma.Operation{
		OperationID: "me", Method: http.MethodGet, Path: Prefix + "/me",
		Summary: "Who the caller is and what it may do", Tags: []string{"auth"},
	}, func(_ context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.Me], error) {
		return reply(actor.Me(), nil)
	})
}
