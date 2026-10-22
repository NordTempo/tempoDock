package tbhttp

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/tempobase"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// sessionCookie holds the UI session secret (HttpOnly, SameSite=Strict).
const sessionCookie = "tempobase_session"

type (
	actorKey  struct{}
	clientKey struct{}
)

func actorFrom(ctx context.Context) tempobase.Actor {
	a, _ := ctx.Value(actorKey{}).(tempobase.Actor)
	return a
}

// client is how a request arrived: its address (for rate limits and the
// audit log), whether over HTTPS (for the cookie's Secure flag) and the
// origin links point at. X-Forwarded-For and -Proto count only from
// trusted proxies.
type client struct {
	ip     string
	secure bool
	base   string
}

func clientFrom(ctx context.Context) client {
	c, _ := ctx.Value(clientKey{}).(client)
	return c
}

// clientOf works out how a request arrived.
func (s *server) clientOf(ctx huma.Context) client {
	ip := ctx.RemoteAddr()
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	addr, _ := netip.ParseAddr(ip)
	trusted := addr.IsValid() && s.cfg.Trusted(addr)
	c := client{ip: ip, secure: ctx.TLS() != nil}
	if trusted {
		// The client is the last address a trusted proxy didn't add.
		hops := strings.Split(ctx.Header("X-Forwarded-For"), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				break
			}
			c.ip = hop.String()
			if !s.cfg.Trusted(hop) {
				break
			}
		}
		if ctx.Header("X-Forwarded-Proto") == "https" {
			c.secure = true
		}
	}
	c.base = s.cfg.BaseURL
	if c.base == "" {
		scheme := "http"
		if c.secure {
			scheme = "https"
		}
		c.base = scheme + "://" + ctx.Host()
	} else if strings.HasPrefix(c.base, "https://") {
		c.secure = true
	}
	return c
}

// authenticate records how each request arrived and resolves `Authorization:
// Bearer <token>` or the session cookie to an actor for every non-public
// operation: one indexed query.
func (s *server) authenticate(a huma.API) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		ctx = huma.WithValue(ctx, clientKey{}, s.clientOf(ctx))
		if op := ctx.Operation(); op != nil && op.Metadata["public"] == true {
			next(ctx)
			return
		}
		var (
			actor tempobase.Actor
			err   error
		)
		if h := ctx.Header("Authorization"); h != "" {
			token, ok := strings.CutPrefix(h, "Bearer ")
			if !ok {
				_ = huma.WriteErr(a, ctx, http.StatusUnauthorized, "the Authorization header must be `Bearer <token>`")
				return
			}
			actor, err = s.app.AuthenticateToken(ctx.Context(), strings.TrimSpace(token))
		} else if c, cerr := huma.ReadCookie(ctx, sessionCookie); cerr == nil {
			actor, err = s.app.AuthenticateSession(ctx.Context(), c.Value)
		} else {
			_ = huma.WriteErr(a, ctx, http.StatusUnauthorized, "sign in, or send `Authorization: Bearer <token>`")
			return
		}
		if err != nil {
			if datapanel.CodeOf(err) == apitypes.CodeUnauthorized {
				_ = huma.WriteErr(a, ctx, http.StatusUnauthorized, err.Error())
			} else {
				s.opts.Logger.Error("authenticate", "err", err)
				_ = huma.WriteErr(a, ctx, http.StatusInternalServerError, "internal error; see the server log")
			}
			return
		}
		next(huma.WithValue(ctx, actorKey{}, actor))
	}
}

// sessionCookie is the cookie that carries a session.
func (c client) sessionCookie(secret string, expires time.Time) http.Cookie {
	return http.Cookie{
		Name: sessionCookie, Value: secret, Path: "/", Expires: expires,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: c.secure,
	}
}

// signedIn is the answer of every operation that starts a session.
type signedIn struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      tbtypes.Me
}

// signIn turns a new session into its response.
func (s *server) signIn(ctx context.Context, secret string, expires time.Time, act tempobase.Actor, err error) (*signedIn, error) {
	if err != nil {
		return nil, err
	}
	me, err := s.app.Me(ctx, act)
	if err != nil {
		return nil, err
	}
	return &signedIn{SetCookie: clientFrom(ctx).sessionCookie(secret, expires), Body: me}, nil
}

// CodePath is a link's code in a path.
type CodePath struct {
	Code string `path:"code" doc:"The code from the link's fragment"`
}

type useLinkInput struct {
	CodePath
	Body tbtypes.UseLinkRequest
}

type logoutInput struct {
	Session string `cookie:"tempobase_session"`
}

type clearCookie struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
}

// passwordOutput sets the caller's fresh session cookie.
type passwordOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
}

func registerAuth(s *server, a huma.API) {
	public(s, a, huma.Operation{
		OperationID: "setup-status", Method: http.MethodGet, Path: Prefix + "/setup",
		Summary: "Whether the first admin has yet to be created", Tags: []string{"auth"},
	}, func(ctx context.Context, _ *struct{}) (*out[tbtypes.SetupStatus], error) {
		needed, err := s.app.SetupNeeded(ctx)
		return reply(tbtypes.SetupStatus{Needed: needed}, err)
	})
	public(s, a, huma.Operation{
		OperationID: "setup", Method: http.MethodPost, Path: Prefix + "/setup",
		Summary:     "Create the first admin",
		Description: "With the one-time setup code the server logs at start while nobody exists. Signs the admin in (session cookie).",
		Tags:        []string{"auth"}, Errors: []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusConflict, http.StatusTooManyRequests},
	}, func(ctx context.Context, in *struct{ Body tbtypes.SetupRequest }) (*signedIn, error) {
		secret, expires, act, err := s.app.Setup(ctx, in.Body, clientFrom(ctx).ip)
		return s.signIn(ctx, secret, expires, act, err)
	})
	public(s, a, huma.Operation{
		OperationID: "login", Method: http.MethodPost, Path: Prefix + "/auth/login",
		Summary: "Sign in", Description: "Sets an HttpOnly session cookie for 30 days.", Tags: []string{"auth"},
		Errors: []int{http.StatusUnauthorized, http.StatusTooManyRequests},
	}, func(ctx context.Context, in *struct{ Body apitypes.LoginRequest }) (*signedIn, error) {
		secret, expires, act, err := s.app.Login(ctx, in.Body.Email, in.Body.Password, clientFrom(ctx).ip)
		return s.signIn(ctx, secret, expires, act, err)
	})
	authed(s, a, huma.Operation{
		OperationID: "logout", Method: http.MethodPost, Path: Prefix + "/auth/logout",
		Summary: "End the session", Tags: []string{"auth"}, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, _ tempobase.Actor, in *logoutInput) (*clearCookie, error) {
		if in.Session != "" {
			if err := s.app.Logout(ctx, in.Session); err != nil {
				return nil, err
			}
		}
		return &clearCookie{SetCookie: http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode}}, nil
	})
	public(s, a, huma.Operation{
		OperationID: "get-link", Method: http.MethodGet, Path: Prefix + "/links/{code}",
		Summary: "What an invite or reset link is for", Tags: []string{"auth"},
		Errors: []int{http.StatusNotFound, http.StatusGone},
	}, func(ctx context.Context, in *CodePath) (*out[tbtypes.LinkInfo], error) {
		return reply(s.app.LinkInfo(ctx, in.Code))
	})
	public(s, a, huma.Operation{
		OperationID: "use-link", Method: http.MethodPost, Path: Prefix + "/links/{code}",
		Summary:     "Accept an invite or set a new password",
		Description: "An invite creates the person with a name and password; a reset link sets a new password and ends their other sessions. Either signs them in (session cookie). Links work once.",
		Tags:        []string{"auth"}, Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusGone, http.StatusTooManyRequests},
	}, func(ctx context.Context, in *useLinkInput) (*signedIn, error) {
		secret, expires, act, err := s.app.UseLink(ctx, in.Code, in.Body, clientFrom(ctx).ip)
		return s.signIn(ctx, secret, expires, act, err)
	})
	authed(s, a, huma.Operation{
		OperationID: "me", Method: http.MethodGet, Path: Prefix + "/me",
		Summary: "Who the caller is", Tags: []string{"auth"},
	}, func(ctx context.Context, act tempobase.Actor, _ *struct{}) (*out[tbtypes.Me], error) {
		return reply(s.app.Me(ctx, act))
	})
	authed(s, a, huma.Operation{
		OperationID: "change-password", Method: http.MethodPost, Path: Prefix + "/me/password",
		Summary:     "Change your password",
		Description: "People only (tokens have no password). Ends all your sessions; you get a fresh session cookie.",
		Tags:        []string{"auth"}, DefaultStatus: http.StatusNoContent,
		Errors: []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusTooManyRequests},
	}, func(ctx context.Context, act tempobase.Actor, in *struct {
		Body apitypes.ChangePasswordRequest
	}) (*passwordOutput, error) {
		c := clientFrom(ctx)
		secret, expires, err := s.app.ChangePassword(ctx, act, in.Body, c.ip)
		if err != nil {
			return nil, err
		}
		return &passwordOutput{SetCookie: c.sessionCookie(secret, expires)}, nil
	})
}
