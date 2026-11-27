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
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strconv"

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
	// replays runs the API's changes again for approved access requests
	// (ADR 0044), by operation ID; nil while building the spec.
	replays map[string]func(context.Context, core.Actor, json.RawMessage) (json.RawMessage, error)
}

// replay is the API's core.Replayer.
func (s *Server) replay(ctx context.Context, a core.Actor, op string, args json.RawMessage) (json.RawMessage, error) {
	run, ok := s.replays[op]
	if !ok {
		return nil, fmt.Errorf("API operation %q can't run again", op)
	}
	return run(ctx, a, args)
}

// New returns the daemon's root handler: the API and the web UI.
func New(c *core.Core, opts Options) http.Handler {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	mux := http.NewServeMux()
	s := &Server{core: c, opts: opts, replays: map[string]func(context.Context, core.Actor, json.RawMessage) (json.RawMessage, error){}}
	newAPI(mux, s)
	c.SetReplayer("api", s.replay)
	// The MCP endpoint (ADR 0017) speaks JSON-RPC, not REST, so it sits
	// next to the Huma API rather than in its OpenAPI spec.
	mux.Handle(Prefix+"/mcp", mcpHandler(c, opts))
	mux.Handle("/", uiHandler())
	return guard(mux)
}

// mcpHandler is the MCP endpoint, which the public demo (ADR 0100) leaves
// out: the demo is view-only, and most of its tools change things.
func mcpHandler(c *core.Core, opts Options) http.Handler {
	if c.Demo() {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "MCP isn't in the demo; install Dock on your own server to connect an agent", http.StatusForbidden)
		})
	}
	return mcp.Handler(c, mcp.Options{Logger: opts.Logger})
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
	// No docs page: Huma's loads its script from a CDN, and the daemon
	// serves nothing that makes a browser fetch from another site. The
	// spec itself is served, for any viewer pointed at it.
	cfg.DocsPath = ""
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
	registerGuide(a)
	registerAuth(s, a)
	registerDemo(s, a)
	registerTwoFactor(s, a)
	registerSSO(s, a)
	registerCLILogin(s, a)
	registerProjects(s, a)
	registerLocks(s, a)
	registerSleep(s, a)
	registerRequests(s, a)
	registerPreviews(s, a)
	registerCompose(s, a)
	registerServices(s, a)
	registerReleases(s, a)
	registerDeployQueue(s, a)
	registerDiagnoses(s, a)
	registerScans(s, a)
	registerDomains(s, a)
	registerUptime(s, a)
	registerAdmin(s, a)
	registerOrgSettings(s, a)
	registerGroups(s, a)
	registerServers(s, a)
	registerAlerts(s, a)
	registerHeartbeat(s, a)
	registerErrors(s, a)
	registerLogDrains(s, a)
	registerStreams(s, a)
	registerData(s, a)
	registerGit(s, a)
	registerGitHub(s, a, mux)
	registerBackups(s, a)
	registerAppTemplates(s, a)
	registerSnapshots(s, a)
	registerMoves(s, a)
	registerServerDrains(s, a)
	registerRegistry(s, a)
	registerStorage(s, a)
	registerCloudflare(s, a)
	registerUpdate(s, a)
	registerAddress(s, a)
	registerEmail(s, a)
	registerPasswordReset(s, a)
	registerOperator(s, a)
	registerAnalytics(s, a)
	registerTracing(s, a)
	registerTraces(s, a)
	registerAppMetrics(s, a)
	registerSites(s, a)
	registerSiteLive(s, a)
	registerConnect(s, a, mux)
	registerRuns(s, a, mux)
	registerJobs(s, a)
	registerStatusPages(s, a)
	registerIncidents(s, a)
	shareErrorResponses(a.OpenAPI())
	return a
}

// shareErrorResponses points every operation's error responses at one
// *huma.Response per status code. Huma builds a fresh response, content
// map and schema ref for each code of each operation (~2,000 in all) and
// keeps them for the life of the daemon; sharing frees them. Only a
// response deeply equal to the shared one is replaced, so the OpenAPI
// document stays byte-identical. Runs once, after every operation is
// registered, so nothing mutates a shared response afterwards.
func shareErrorResponses(o *huma.OpenAPI) {
	ct := (&apitypes.Error{}).ContentType("application/json")
	ref := o.Components.Schemas.Schema(reflect.TypeFor[apitypes.Error](), true, "Error")
	shared := map[string]*huma.Response{}
	for _, item := range o.Paths {
		for _, op := range []*huma.Operation{item.Get, item.Put, item.Post, item.Delete, item.Options, item.Head, item.Patch, item.Trace} {
			if op == nil {
				continue
			}
			for code, r := range op.Responses {
				want, ok := shared[code]
				if !ok {
					n, err := strconv.Atoi(code)
					if err != nil || n < 400 {
						continue
					}
					want = &huma.Response{
						Description: http.StatusText(n),
						Content:     map[string]*huma.MediaType{ct: {Schema: ref}},
					}
					shared[code] = want
				}
				if r != want && reflect.DeepEqual(r, want) {
					op.Responses[code] = want
				}
			}
		}
	}
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
	case http.StatusLocked:
		return apitypes.CodeEnvironmentLocked
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
	case apitypes.CodeDenied, apitypes.CodeReauthRequired, apitypes.CodeTwoFactorRequired, apitypes.CodeSSORequired:
		return http.StatusForbidden
	case apitypes.CodeExpired:
		return http.StatusGone
	case apitypes.CodeEnvironmentLocked:
		return http.StatusLocked
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
		e := &apitypes.Error{Status: status, Title: http.StatusText(status), Detail: ce.Msg, Code: ce.Code, Release: ce.Release}
		e.Refusal = ce.Refusal
		if le, ok := errors.AsType[*core.LockedError](err); ok {
			e.Request = le.Request
		}
		return e
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

// orgParam is the `org` query parameter every authenticated operation
// takes; the auth middleware reads it (ADR 0027).
var orgParam = &huma.Param{
	Name: "org", In: "query", Schema: &huma.Schema{Type: "string"},
	Description: "Organization slug to act in (or the Dock-Org header). Optional for a token, which belongs to one, and for a user with one organization.",
}

// authed registers an operation that requires a caller. The handler gets
// the authenticated actor; core errors are mapped to problem responses.
// It is a typed shell around authedOp and serveAuthed: a generic body is
// compiled once per input and output shape, so the logic lives in those
// two once (docs/performance.md, "Binary size").
func authed[I, O any](s *Server, a huma.API, op huma.Operation, h func(context.Context, core.Actor, *I) (*O, error)) {
	call := func(ctx context.Context, actor core.Actor, in any) (any, error) {
		o, err := h(ctx, actor, in.(*I))
		if o == nil {
			return nil, err
		}
		return o, err
	}
	op, replayable := s.authedOp(op, func() any { return new(I) }, call)
	huma.Register(a, op, func(ctx context.Context, in *I) (*O, error) {
		o, err := s.serveAuthed(ctx, &op, replayable, in, call)
		if o == nil {
			return nil, err
		}
		return o.(*O), err
	})
}

// authedCall is an authed handler with its input (*I) and output (*O)
// as any; the output is nil when the handler returned none.
type authedCall func(ctx context.Context, actor core.Actor, in any) (any, error)

// authedOp completes an authed operation's description (security, the org
// parameter, its errors) and registers its replay when a locked
// environment can refuse it, returning the operation and whether it is
// replayable. newIn returns a new, empty input.
func (s *Server) authedOp(op huma.Operation, newIn func() any, call authedCall) (huma.Operation, bool) {
	op.Security = security
	op.Parameters = append(op.Parameters, orgParam)
	if op.Errors == nil {
		op.Errors = []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict}
	}
	if op.Method != http.MethodGet && !slices.Contains(op.Errors, http.StatusLocked) && !slices.Contains(op.Tags, "data") {
		// Any change can meet a locked environment (ADR 0044). The Data
		// panel's operations keep tempoBase's contract: their writes are
		// capped at proposing instead.
		op.Errors = append(slices.Clone(op.Errors), http.StatusLocked)
	}
	// A change a locked environment refuses becomes an access request
	// that runs it again, from its decoded input, once approved. Inputs
	// that don't survive JSON (a streamed upload) opt out.
	replayable := op.Method != http.MethodGet && op.Metadata["replay"] != false
	if replayable && s.replays != nil {
		s.replays[op.OperationID] = func(ctx context.Context, actor core.Actor, args json.RawMessage) (json.RawMessage, error) {
			in := newIn()
			if err := json.Unmarshal(args, in); err != nil {
				return nil, err
			}
			o, err := call(ctx, actor, in)
			if err != nil {
				return nil, err
			}
			return replyBody(o)
		}
	}
	return op, replayable
}

// serveAuthed runs an authed operation's handler for a request: as the
// request's actor, scoped to the project its input names, with its error
// filed as an access request (when replayable) and mapped to a problem.
func (s *Server) serveAuthed(ctx context.Context, op *huma.Operation, replayable bool, in any, call authedCall) (any, error) {
	if err := s.demoRefusal(ctx, op); err != nil {
		return nil, err
	}
	actor := actorFrom(ctx)
	if p, ok := in.(interface{ expectedProjectID() int64 }); ok {
		actor.ExpectedProjectID = p.expectedProjectID()
	}
	o, err := call(ctx, actor, in)
	if err != nil {
		if replayable {
			err = s.fileRequest(ctx, actor, *op, in, err)
		}
		return nil, s.problem(s.core.Explain(ctx, actor, err))
	}
	return o, nil
}

// fileRequest files an access request for a call a locked environment
// refused (core.FileRequest); other errors pass through.
func (s *Server) fileRequest(ctx context.Context, actor core.Actor, op huma.Operation, in any, err error) error {
	if _, ok := errors.AsType[*core.LockedError](err); !ok {
		return err
	}
	args, merr := json.Marshal(in)
	if merr != nil {
		return err
	}
	// The reason is in ctx (core.WithReason, from the Dock-Reason header).
	return s.core.FileRequest(ctx, actor, err, core.Call{Via: "api", Op: op.OperationID, Title: op.Summary, Args: args})
}

// replyBody is a handler's response body as JSON: the Body field of the
// out struct (nil for a response without one).
func replyBody(o any) (json.RawMessage, error) {
	v := reflect.ValueOf(o)
	if v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil, nil
	}
	if b := v.FieldByName("Body"); b.IsValid() {
		return json.Marshal(b.Interface())
	}
	return nil, nil
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
