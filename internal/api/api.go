// Package api is the daemon's HTTP API (Huma, OpenAPI 3.1) under /api/v1,
// plus the embedded web UI and the loopback-only internal endpoints.
//
// Handlers are thin: the auth middleware resolves the caller to a
// core.Actor, handlers call internal/core (which checks permissions) and
// return internal/apitypes values. Errors are RFC 9457 problem documents
// with a machine-readable `code` (apitypes.Error).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/NordTempo/platform/internal/apitypes"
	"github.com/NordTempo/platform/internal/core"
	"github.com/NordTempo/platform/internal/version"
	"github.com/NordTempo/platform/web"
)

// Prefix is the base path of every API operation.
const Prefix = "/api/" + apitypes.APIVersion

// Options configure the API server.
type Options struct {
	// MaxUploadBytes caps source uploads.
	MaxUploadBytes int64
	Logger         *slog.Logger
}

// Server holds the API's dependencies.
type Server struct {
	core *core.Core
	opts Options
}

// New returns the daemon's root handler: the API and the web UI.
func New(c *core.Core, opts Options) http.Handler {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	mux := http.NewServeMux()
	newAPI(mux, &Server{core: c, opts: opts})
	mux.Handle("/", uiHandler())
	return guard(mux)
}

// Spec returns the OpenAPI document (JSON) without a running daemon.
func Spec() ([]byte, error) {
	a := newAPI(http.NewServeMux(), &Server{})
	return json.MarshalIndent(a.OpenAPI(), "", "  ")
}

func newAPI(mux *http.ServeMux, s *Server) huma.API {
	cfg := huma.DefaultConfig("Tempo Dock", version.Version)
	cfg.Info.Description = "The Tempo Dock API. The CLI and web UI are thin clients of it."
	cfg.OpenAPIPath = Prefix + "/openapi"
	cfg.DocsPath = Prefix + "/docs"
	cfg.SchemasPath = Prefix + "/schemas"
	// No `$schema` field in response bodies: the JSON contract stays exactly
	// the apitypes fields.
	cfg.CreateHooks = nil
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearer":  {Type: "http", Scheme: "bearer", Description: "API token (dock_...)"},
		"session": {Type: "apiKey", In: "cookie", Name: sessionCookie, Description: "UI session from /auth/login"},
	}
	a := humago.New(mux, cfg)
	a.UseMiddleware(s.authenticate(a))

	registerSystem(s, a)
	registerAuth(s, a)
	registerProjects(s, a)
	registerServices(s, a)
	registerReleases(s, a)
	registerDomains(s, a)
	registerAdmin(s, a)
	registerStreams(s, a)
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
	case apitypes.CodeConflict, apitypes.CodeDeployInProgress, apitypes.CodeHostnameTaken:
		return http.StatusConflict
	case apitypes.CodeTooLarge:
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusInternalServerError
}

// problem converts an error from core into an API error. Unexpected errors
// are logged and hidden behind a generic message.
func (s *Server) problem(err error) error {
	if ae, ok := errors.AsType[*apitypes.Error](err); ok {
		return ae
	}
	var ce *core.Error
	if errors.As(err, &ce) {
		status := statusForCode(ce.Code)
		return &apitypes.Error{Status: status, Title: http.StatusText(status), Detail: ce.Msg, Code: ce.Code}
	}
	s.opts.Logger.Error("api: internal error", "err", err)
	return &apitypes.Error{
		Status: http.StatusInternalServerError, Title: http.StatusText(http.StatusInternalServerError),
		Detail: "internal error; see the daemon log", Code: apitypes.CodeInternal,
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

// security is attached to every authenticated operation.
var security = []map[string][]string{{"bearer": {}}, {"session": {}}}

// authed registers an operation that requires a caller. The handler gets
// the authenticated actor; core errors are mapped to problem responses.
func authed[I, O any](s *Server, a huma.API, op huma.Operation, h func(context.Context, core.Actor, *I) (*O, error)) {
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
func public[I, O any](a huma.API, op huma.Operation, h func(context.Context, *I) (*O, error)) {
	op.Metadata = map[string]any{"public": true}
	huma.Register(a, op, h)
}

func registerSystem(_ *Server, a huma.API) {
	public(a, huma.Operation{
		OperationID: "health", Method: http.MethodGet, Path: Prefix + "/health",
		Summary: "Daemon health and version", Tags: []string{"system"},
	}, func(context.Context, *struct{}) (*out[apitypes.Health], error) {
		return reply(apitypes.Health{Status: "ok", Version: version.Version}, nil)
	})
	public(a, huma.Operation{
		OperationID: "version", Method: http.MethodGet, Path: Prefix + "/version",
		Summary: "Server and API version, for client compatibility checks", Tags: []string{"system"},
	}, func(context.Context, *struct{}) (*out[apitypes.Version], error) {
		return reply(apitypes.Version{Version: version.Version, APIVersion: apitypes.APIVersion}, nil)
	})
}

// uiHandler serves the embedded SPA: real files when they exist, index.html
// for client-side routes, and a placeholder when the UI hasn't been built.
func uiHandler() http.Handler {
	dist, _ := fs.Sub(web.Dist, "dist")
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(placeholderPage))
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" {
			if _, err := fs.Stat(dist, path); err == nil {
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}

const placeholderPage = `<!doctype html><meta charset="utf-8"><title>Tempo Dock</title>
<body style="background:#000;color:#fff;font:14px ui-monospace,monospace;padding:2rem">
Tempo Dock is running. The web UI has not been built into this binary.`
