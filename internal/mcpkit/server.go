// Package mcpkit is the MCP server both Tempo Dock and tempoBase serve
// (ADR 0017, 0024): the part of MCP Streamable HTTP they need, stateless,
// a tool registry with Huma schemas, docs resources and the database
// panel's data tools. A host supplies how a bearer token becomes its
// actor A and registers its tools; every tool calls the host's own
// operations with that actor, so the host's permission checks and audit
// apply unchanged.
//
// Every POST carries one JSON-RPC message and a bearer token. Nothing runs
// while no agent is calling: the tool and resource tables are built on the
// first request, and a call's work ends with the call.
package mcpkit

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
)

// maxRequestBytes caps a request body. The largest legitimate one is a
// data changeset of 500 rows or a long SQL statement.
const maxRequestBytes = 4 << 20

// writeTimeout bounds each write to a client, so one that stops reading
// can't hold a call (and what it subscribed to) open.
const writeTimeout = 15 * time.Second

// protocolVersions are the MCP versions served, newest first. Every one
// has what the hosts use: tools, resources, progress notifications.
var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

// Options configure a Server for one host; A is the host's actor.
type Options[A any] struct {
	// Name, Title and Version are the serverInfo clients see:
	// "tempo-dock", "Tempo Dock", version.Version.
	Name, Title, Version string
	// Instructions are sent on initialize; clients show them to the model,
	// so they state the host's safety model up front.
	Instructions string
	// Caller, when set, adds a line about the caller to the instructions,
	// e.g. its scope and limits, so the model needs no call to learn them.
	Caller func(context.Context, A) string
	// View, when set, tailors tools/list to the caller (see View).
	View func(context.Context, A) View
	// WithReason, when set, lets every tool take a reason argument (why the
	// caller makes the change), passed to it in the context instead.
	WithReason func(context.Context, string) context.Context
	// Authenticate resolves a bearer token to the caller. An error with
	// code unauthorized answers 401 with its message; others are logged
	// and answer 500.
	Authenticate func(ctx context.Context, token string) (A, error)
	// Realm is the WWW-Authenticate realm ("dock"); NoToken is the 401
	// message for a request without a token.
	Realm   string
	NoToken string
	// Tools registers the host's tools (AddTool, DataTools). It runs once,
	// on the first request; tools/list shows them in registration order.
	Tools func(s *Server[A])
	// Docs are the host's guide pages as resources; zero serves none.
	Docs Docs
	// Refused, when set, sees every tool error before it is rendered, with
	// the call and its arguments, and returns the error to render (Dock
	// files an access request for a change a locked environment refused).
	Refused func(ctx context.Context, a A, tool *ToolInfo, args json.RawMessage, err error) error
	// Hints tell the model what to do next after an error with that code,
	// where the message doesn't.
	Hints  map[apitypes.ErrorCode]string
	Logger *slog.Logger
}

// Server is the MCP endpoint of one host. It is an http.Handler for POST
// requests; create it with New.
type Server[A any] struct {
	o   Options[A]
	log *slog.Logger

	once      sync.Once
	tools     []*tool[A]
	toolsByID map[string]*tool[A]
	toolsList []byte // the tools/list result, encoded once
	docs      []doc

	// views are the tools/list results of Options.View's views, encoded
	// on first use: a handful (scope and limits), never per caller.
	viewsMu sync.Mutex
	views   map[string][]byte
}

// View is what one caller's tools/list shows. Every tool in the list costs
// a model's context on every turn, so a caller sees neither the tools it
// can never use (Hide) nor the arguments its defaults always fill
// (Implied: a token limited to one environment never names a project or
// environment), and with ReadOnly no tool that changes anything. Only,
// when set, lists those tools and no others (a platform credential's few).
// Calls still take hidden tools and implied arguments.
type View struct {
	Hide, Implied, Only []string
	ReadOnly            bool
}

// New returns the endpoint for the options.
func New[A any](o Options[A]) *Server[A] {
	return &Server[A]{o: o, log: cmp.Or(o.Logger, slog.Default())}
}

// build fills the tool and resource tables on first use.
func (s *Server[A]) build() {
	s.once.Do(func() {
		if s.o.Tools != nil {
			s.o.Tools(s)
		}
		s.toolsByID = make(map[string]*tool[A], len(s.tools))
		for _, t := range s.tools {
			s.toolsByID[t.Name] = t
		}
		var err error
		if s.toolsList, err = json.Marshal(map[string]any{"tools": s.tools}); err != nil {
			panic(err) // schemas are built from Go types; a failure is a bug
		}
		s.docs = s.o.Docs.load()
	})
}

// viewList is tools/list as v shows it, encoded once per view.
func (s *Server[A]) viewList(v View) []byte {
	if len(v.Hide)+len(v.Implied)+len(v.Only) == 0 && !v.ReadOnly {
		return s.toolsList
	}
	slices.Sort(v.Hide)
	slices.Sort(v.Implied)
	slices.Sort(v.Only)
	key := fmt.Sprint(v.Hide, v.Implied, v.Only, v.ReadOnly)
	s.viewsMu.Lock()
	defer s.viewsMu.Unlock()
	if b, ok := s.views[key]; ok {
		return b
	}
	tools := make([]tool[A], 0, len(s.tools))
	for _, t := range s.tools {
		if slices.Contains(v.Hide, t.Name) || v.ReadOnly && !t.Annotations.ReadOnly || len(v.Only) > 0 && !slices.Contains(v.Only, t.Name) {
			continue
		}
		c := *t
		c.InputSchema = withoutProperties(t.InputSchema, v.Implied)
		tools = append(tools, c)
	}
	b, err := json.Marshal(map[string]any{"tools": tools})
	if err != nil {
		panic(err) // as in build
	}
	if s.views == nil {
		s.views = map[string][]byte{}
	}
	s.views[key] = b
	return b
}

// withoutProperties is an input schema without the named top-level
// properties.
func withoutProperties(schema json.RawMessage, names []string) json.RawMessage {
	var v map[string]any
	if len(names) == 0 || json.Unmarshal(schema, &v) != nil {
		return schema
	}
	props, _ := v["properties"].(map[string]any)
	for _, n := range names {
		delete(props, n)
	}
	if req, ok := v["required"].([]any); ok {
		v["required"] = slices.DeleteFunc(req, func(r any) bool { n, _ := r.(string); return slices.Contains(names, n) })
	}
	b, err := json.Marshal(v)
	if err != nil {
		return schema
	}
	return b
}

// JSON-RPC 2.0 messages.

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC and MCP error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeNoMethod       = -32601
	codeInvalidParams  = -32602
	codeNoResource     = -32002
)

func (e *rpcError) Error() string { return e.Message }

// ServeHTTP handles one POST: authenticate, decode one JSON-RPC message,
// answer it. Requests get a JSON response, or an event stream when the
// call sends progress notifications first. Notifications and responses
// from the client are acknowledged with 202. There are no sessions, so GET
// (a server-to-client stream) and DELETE (ending a session) are 405.
func (s *Server[A]) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		problem(w, http.StatusMethodNotAllowed, apitypes.CodeValidation, "the MCP endpoint takes POST requests only; it keeps no sessions or streams")
		return
	}
	// Browsers can't send the bearer token cross-site without a CORS
	// preflight, which the hosts never allow; refusing foreign origins on
	// top is what the MCP spec asks of servers.
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err != nil || u.Host != r.Host {
			problem(w, http.StatusForbidden, apitypes.CodeForbidden, "cross-origin request refused (origin "+o+")")
			return
		}
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !slices.Contains(protocolVersions, v) {
		problem(w, http.StatusBadRequest, apitypes.CodeValidation, "unsupported MCP protocol version "+v+"; this server speaks "+strings.Join(protocolVersions, ", "))
		return
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		problem(w, http.StatusUnsupportedMediaType, apitypes.CodeValidation, "send the JSON-RPC message as application/json")
		return
	}
	actor, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		problem(w, http.StatusRequestEntityTooLarge, apitypes.CodeTooLarge, "the request is over 4 MB")
		return
	}
	var msg rpcMessage
	switch {
	case bytes.HasPrefix(bytes.TrimSpace(body), []byte("[")):
		writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{codeInvalidRequest, "batches are not supported; send one message per request"}})
		return
	case json.Unmarshal(body, &msg) != nil || msg.JSONRPC != "2.0":
		writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{codeParse, "not a JSON-RPC 2.0 message"}})
		return
	case len(msg.ID) == 0 || msg.Method == "":
		// A notification, or a response to a request we never send.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	s.build()
	out := &responder{w: w, id: msg.ID, streams: acceptsStream(r)}
	result, err := s.dispatch(r.Context(), out, actor, msg)
	resp := rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: result}
	if err != nil {
		var re *rpcError
		if !errors.As(err, &re) {
			s.log.Error("mcp: "+msg.Method, "err", err)
			re = &rpcError{-32603, "internal error; see the daemon log"}
		}
		resp.Result, resp.Error = nil, re
	}
	out.finish(resp)
}

// dispatch runs one request.
func (s *Server[A]) dispatch(ctx context.Context, out *responder, a A, msg rpcMessage) (any, error) {
	switch msg.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		v := protocolVersions[0]
		if slices.Contains(protocolVersions, p.ProtocolVersion) {
			v = p.ProtocolVersion
		}
		instructions := s.o.Instructions
		if s.o.Caller != nil {
			instructions += "\n\n" + s.o.Caller(ctx, a)
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{}, "resources": map[string]any{}},
			"serverInfo":      map[string]string{"name": s.o.Name, "title": s.o.Title, "version": s.o.Version},
			"instructions":    instructions,
		}, nil
	case "ping":
		return struct{}{}, nil
	case "tools/list":
		if s.o.View != nil {
			return json.RawMessage(s.viewList(s.o.View(ctx, a))), nil
		}
		return json.RawMessage(s.toolsList), nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      struct {
				ProgressToken json.RawMessage `json:"progressToken"`
			} `json:"_meta"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return nil, &rpcError{codeInvalidParams, "invalid tools/call params: " + err.Error()}
		}
		t, ok := s.toolsByID[p.Name]
		if !ok {
			return nil, &rpcError{codeInvalidParams, "unknown tool " + p.Name + "; tools/list shows them"}
		}
		c := &Call[A]{Actor: a, out: out, progressToken: p.Meta.ProgressToken}
		return s.run(ctx, t, c, p.Arguments), nil
	case "resources/list":
		return map[string]any{"resources": s.docs}, nil
	case "resources/templates/list":
		return map[string]any{"resourceTemplates": []any{}}, nil
	case "resources/read":
		var p struct {
			URI string `json:"uri"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		return s.o.Docs.read(s.docs, p.URI)
	}
	return nil, &rpcError{codeNoMethod, "method " + msg.Method + " is not supported"}
}

// authenticate resolves the bearer token to an actor. Only tokens are
// accepted, no cookies, so a browser page can't call tools on someone's
// behalf.
func (s *Server[A]) authenticate(w http.ResponseWriter, r *http.Request) (A, bool) {
	var zero A
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+s.o.Realm+`"`)
		problem(w, http.StatusUnauthorized, apitypes.CodeUnauthorized, s.o.NoToken)
		return zero, false
	}
	a, err := s.o.Authenticate(r.Context(), strings.TrimSpace(token))
	if err != nil {
		switch code := datapanel.CodeOf(err); code {
		case apitypes.CodeUnauthorized:
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+s.o.Realm+`", error="invalid_token"`)
			problem(w, http.StatusUnauthorized, code, err.Error())
		case apitypes.CodeForbidden:
			problem(w, http.StatusForbidden, code, err.Error())
		case apitypes.CodeNotFound:
			problem(w, http.StatusNotFound, code, err.Error())
		default:
			s.log.Error("mcp: authenticate", "err", err)
			problem(w, http.StatusInternalServerError, apitypes.CodeInternal, "internal error; see the daemon log")
		}
		return zero, false
	}
	return a, true
}

// acceptsStream reports whether the client takes an event stream as the
// response (MCP clients send both application/json and text/event-stream).
func acceptsStream(r *http.Request) bool {
	for _, v := range r.Header.Values("Accept") {
		for part := range strings.SplitSeq(v, ",") {
			if mt, _, _ := mime.ParseMediaType(strings.TrimSpace(part)); mt == "text/event-stream" {
				return true
			}
		}
	}
	return false
}

// responder writes the answer to one request: a single JSON response, or,
// once the call sends a notification, an event stream that ends with the
// response.
type responder struct {
	w       http.ResponseWriter
	id      json.RawMessage
	streams bool // the client accepts an event stream
	rc      *http.ResponseController
	failed  bool // a stream write failed; the client is gone
}

// notify sends a notification ahead of the response, switching the
// response to an event stream. It does nothing for clients that only
// accept JSON, and never blocks longer than writeTimeout.
func (o *responder) notify(method string, params any) {
	if !o.streams || o.failed {
		return
	}
	if o.rc == nil {
		o.rc = http.NewResponseController(o.w)
		h := o.w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		o.w.WriteHeader(http.StatusOK)
	}
	o.event(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// event writes one SSE message event and flushes it.
func (o *responder) event(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		o.failed = true
		return
	}
	_ = o.rc.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := o.w.Write(append(append([]byte("event: message\ndata: "), b...), '\n', '\n')); err != nil {
		o.failed = true
		return
	}
	if err := o.rc.Flush(); err != nil {
		o.failed = true
	}
}

// finish writes the response: as the stream's last event if a stream was
// started, else as JSON.
func (o *responder) finish(resp rpcResponse) {
	if o.rc == nil {
		writeJSON(o.w, http.StatusOK, resp)
		return
	}
	if !o.failed {
		o.event(resp)
	}
	_ = o.rc.SetWriteDeadline(time.Time{})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// problem writes an HTTP-level error (before any JSON-RPC message was
// read) as the API's problem document.
func problem(w http.ResponseWriter, status int, code apitypes.ErrorCode, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apitypes.Error{Status: status, Title: http.StatusText(status), Detail: detail, Code: code})
}
