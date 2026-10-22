// Package tbhttp is tempoBase's HTTP API (Huma, OpenAPI 3.1) under
// /api/v1 (ADR 0024 section 3). Handlers are thin: the auth middleware
// resolves the caller to a tempobase.Actor, handlers call the App (which
// checks permissions) and return tbtypes or apitypes values. Errors are
// apitypes.Error problem documents with Dock's codes.
package tbhttp

//go:generate go run ../../../cmd/tempobase openapi -o ../../../web/tempobase/openapi.json

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/httpkit"
	"github.com/NordTempo/tempoDock/internal/tempobase"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
	"github.com/NordTempo/tempoDock/internal/version"
	tbweb "github.com/NordTempo/tempoDock/web/tempobase"
)

// Prefix is the base path of every API operation.
const Prefix = "/api/" + apitypes.APIVersion

// Options configure the server.
type Options struct {
	Logger *slog.Logger
}

// server holds the handlers' dependencies.
type server struct {
	app  *tempobase.App
	cfg  tempobase.Config
	opts Options
}

// New returns tempoBase's root handler: the API and the web UI behind
// the security headers and the CSRF guard.
func New(app *tempobase.App, opts Options) http.Handler {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	mux := http.NewServeMux()
	newAPI(mux, &server{app: app, cfg: app.Config(), opts: opts})
	dist, _ := fs.Sub(tbweb.Dist, "dist")
	mux.Handle("/", httpkit.UIHandler(dist, placeholderPage))
	return httpkit.Guard(mux, nil)
}

// placeholderPage is served instead of the UI when the binary was built
// without web/tempobase/dist.
const placeholderPage = `<!doctype html><meta charset="utf-8"><title>tempoBase</title>
<body style="background:#000;color:#fff;font:14px ui-monospace,monospace;padding:2rem">
tempoBase is running. The web UI has not been built into this binary.`

// Spec returns the OpenAPI document (JSON) without a running server.
func Spec() ([]byte, error) {
	a := newAPI(http.NewServeMux(), &server{})
	return json.MarshalIndent(a.OpenAPI(), "", "  ")
}

func newAPI(mux *http.ServeMux, s *server) huma.API {
	cfg := huma.DefaultConfig("tempoBase", version.Version)
	cfg.Info.Description = "The tempoBase API: a self-hosted Postgres viewer with safe editing. The web UI is a thin client of it."
	cfg.OpenAPIPath = Prefix + "/openapi"
	cfg.DocsPath = Prefix + "/docs"
	cfg.SchemasPath = Prefix + "/schemas"
	// No `$schema` field in response bodies: the contract stays exactly the
	// types' fields.
	cfg.CreateHooks = nil
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearer":  {Type: "http", Scheme: "bearer", Description: "API token (tbt_...)"},
		"session": {Type: "apiKey", In: "cookie", Name: sessionCookie, Description: "UI session from /auth/login"},
	}
	a := humago.New(mux, cfg)
	a.UseMiddleware(s.authenticate(a))

	registerSystem(s, a)
	registerAuth(s, a)
	registerPeople(s, a)
	registerTokens(s, a)
	registerConnections(s, a)
	registerPolicy(s, a)
	registerData(s, a)
	registerAudit(s, a)
	return a
}

func init() {
	// Every error Huma writes (validation, auth, ours) is an apitypes.Error.
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		e := &apitypes.Error{Status: status, Title: http.StatusText(status), Detail: msg, Code: codeForStatus(status)}
		for _, err := range errs {
			if err == nil {
				continue
			}
			var d huma.ErrorDetailer
			if errors.As(err, &d) {
				dd := d.ErrorDetail()
				e.Errors = append(e.Errors, apitypes.ErrorDetail{Message: dd.Message, Location: dd.Location})
			} else {
				e.Errors = append(e.Errors, apitypes.ErrorDetail{Message: err.Error()})
			}
		}
		return e
	}
}

func codeForStatus(status int) apitypes.ErrorCode {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return apitypes.CodeValidation
	case http.StatusUnauthorized:
		return apitypes.CodeUnauthorized
	case http.StatusForbidden:
		return apitypes.CodeForbidden
	case http.StatusNotFound:
		return apitypes.CodeNotFound
	case http.StatusConflict:
		return apitypes.CodeConflict
	case http.StatusRequestEntityTooLarge:
		return apitypes.CodeTooLarge
	case http.StatusTooManyRequests:
		return apitypes.CodeRateLimited
	case http.StatusServiceUnavailable:
		return apitypes.CodeServerOffline
	case http.StatusGone:
		return apitypes.CodeExpired
	}
	return apitypes.CodeInternal
}

func statusForCode(code apitypes.ErrorCode) int {
	switch code {
	case apitypes.CodeValidation:
		return http.StatusBadRequest
	case apitypes.CodeUnauthorized:
		return http.StatusUnauthorized
	case apitypes.CodeForbidden:
		return http.StatusForbidden
	case apitypes.CodeNotFound:
		return http.StatusNotFound
	case apitypes.CodeConflict:
		return http.StatusConflict
	case apitypes.CodeTooLarge:
		return http.StatusRequestEntityTooLarge
	case apitypes.CodeRateLimited:
		return http.StatusTooManyRequests
	case apitypes.CodeServerOffline:
		return http.StatusServiceUnavailable
	case apitypes.CodeDenied:
		return http.StatusForbidden
	case apitypes.CodeExpired:
		return http.StatusGone
	}
	return http.StatusInternalServerError
}

// problem converts an App error into an API error. Unexpected errors are
// logged and hidden behind a generic message.
func (s *server) problem(err error) error {
	if ae, ok := errors.AsType[*apitypes.Error](err); ok {
		return ae
	}
	if de, ok := errors.AsType[*datapanel.Error](err); ok {
		status := statusForCode(de.Code)
		return &apitypes.Error{Status: status, Title: http.StatusText(status), Detail: de.Msg, Code: de.Code}
	}
	s.opts.Logger.Error("api: internal error", "err", err)
	return &apitypes.Error{
		Status: http.StatusInternalServerError, Title: http.StatusText(http.StatusInternalServerError),
		Detail: "internal error; see the server log", Code: apitypes.CodeInternal,
	}
}

// out wraps a response body.
type out[T any] struct{ Body T }

func reply[T any](v T, err error) (*out[T], error) {
	if err != nil {
		return nil, err
	}
	return &out[T]{Body: v}, nil
}

// empty is a 204 response.
type empty struct{}

// security is attached to every authenticated operation.
var security = []map[string][]string{{"bearer": {}}, {"session": {}}}

// authed registers an operation that requires a caller. The handler gets
// the authenticated actor; App errors become problem responses.
func authed[I, O any](s *server, a huma.API, op huma.Operation, h func(context.Context, tempobase.Actor, *I) (*O, error)) {
	op.Security = security
	if op.Errors == nil {
		op.Errors = []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict}
	}
	huma.Register(a, op, func(ctx context.Context, in *I) (*O, error) {
		o, err := h(ctx, actorFrom(ctx), in)
		if err != nil {
			return nil, s.problem(err)
		}
		return o, nil
	})
}

// public registers an operation that needs no credentials.
func public[I, O any](s *server, a huma.API, op huma.Operation, h func(context.Context, *I) (*O, error)) {
	op.Metadata = map[string]any{"public": true}
	huma.Register(a, op, func(ctx context.Context, in *I) (*O, error) {
		o, err := h(ctx, in)
		if err != nil {
			return nil, s.problem(err)
		}
		return o, nil
	})
}

func registerSystem(s *server, a huma.API) {
	public(s, a, huma.Operation{
		OperationID: "health", Method: http.MethodGet, Path: Prefix + "/health",
		Summary: "Server health", Tags: []string{"system"},
	}, func(context.Context, *struct{}) (*out[tbtypes.Health], error) {
		return reply(tbtypes.Health{Status: "ok"}, nil)
	})
	public(s, a, huma.Operation{
		OperationID: "version", Method: http.MethodGet, Path: Prefix + "/version",
		Summary: "Server version", Tags: []string{"system"},
	}, func(context.Context, *struct{}) (*out[tbtypes.Version], error) {
		return reply(tbtypes.Version{Version: version.Version}, nil)
	})
}
