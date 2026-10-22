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
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcp"
	"github.com/NordTempo/tempoDock/internal/version"
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
	// data replaces core for the database panel endpoints in tests.
	data dataCore
}

// New returns the daemon's root handler: the API and the web UI.
func New(c *core.Core, opts Options) http.Handler {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	mux := http.NewServeMux()
	newAPI(mux, &Server{core: c, opts: opts})
	// The MCP endpoint (ADR 0017) speaks JSON-RPC, not REST, so it sits
	// next to the Huma API rather than in its OpenAPI spec.
	mux.Handle(Prefix+"/mcp", mcp.Handler(c, mcp.Options{Logger: opts.Logger}))
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
	registerCLILogin(s, a)
	registerProjects(s, a)
	registerServices(s, a)
	registerReleases(s, a)
	registerDomains(s, a)
	registerAdmin(s, a)
	registerServers(s, a)
	registerStreams(s, a)
	registerData(s, a)
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
	case apitypes.CodeConflict, apitypes.CodeDeployInProgress, apitypes.CodeHostnameTaken:
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

func registerSystem(s *Server, a huma.API) {
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
		return reply(apitypes.Version{Version: version.Version, APIVersion: apitypes.APIVersion, MaxUploadBytes: s.opts.MaxUploadBytes}, nil)
	})
}
