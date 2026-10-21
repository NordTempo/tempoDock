package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/platform/internal/apitypes"
	"github.com/NordTempo/platform/internal/core"
)

// sessionCookie holds the UI session secret (HttpOnly, SameSite=Strict).
const sessionCookie = "dock_session"

type actorKey struct{}

func actorFrom(ctx context.Context) core.Actor {
	a, _ := ctx.Value(actorKey{}).(core.Actor)
	return a
}

// authenticate resolves `Authorization: Bearer <token>` or the session
// cookie to an actor for every non-public operation.
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
		if h := ctx.Header("Authorization"); h != "" {
			token, ok := strings.CutPrefix(h, "Bearer ")
			if !ok {
				_ = huma.WriteErr(a, ctx, http.StatusUnauthorized, "Authorization header must be `Bearer <token>`")
				return
			}
			actor, err = s.core.AuthenticateToken(ctx.Context(), strings.TrimSpace(token))
		} else if c, cerr := huma.ReadCookie(ctx, sessionCookie); cerr == nil {
			actor, err = s.core.AuthenticateSession(ctx.Context(), c.Value)
		} else {
			_ = huma.WriteErr(a, ctx, http.StatusUnauthorized, "authentication required: send `Authorization: Bearer <token>` or log in")
			return
		}
		if err != nil {
			if core.CodeOf(err) == apitypes.CodeUnauthorized {
				_ = huma.WriteErr(a, ctx, http.StatusUnauthorized, err.Error())
			} else {
				s.opts.Logger.Error("authenticate", "err", err)
				_ = huma.WriteErr(a, ctx, http.StatusInternalServerError, "internal error; see the daemon log")
			}
			return
		}
		next(huma.WithValue(ctx, actorKey{}, actor))
	}
}

type loginInput struct {
	Body   apitypes.LoginRequest
	secure bool
}

// Resolve records whether the request came over HTTPS, for the cookie's
// Secure flag.
func (in *loginInput) Resolve(ctx huma.Context) []error {
	in.secure = ctx.TLS() != nil || ctx.Header("X-Forwarded-Proto") == "https"
	return nil
}

type loginOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      apitypes.Me
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
		Summary: "Log in to the UI", Description: "Sets an HttpOnly session cookie.", Tags: []string{"auth"},
		Errors: []int{http.StatusUnauthorized},
	}, func(ctx context.Context, in *loginInput) (*loginOutput, error) {
		secret, expires, actor, err := s.core.Login(ctx, in.Body.Email, in.Body.Password)
		if err != nil {
			return nil, s.problem(err)
		}
		return &loginOutput{
			SetCookie: http.Cookie{
				Name: sessionCookie, Value: secret, Path: "/", Expires: expires,
				HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: in.secure,
			},
			Body: actor.Me(),
		}, nil
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
