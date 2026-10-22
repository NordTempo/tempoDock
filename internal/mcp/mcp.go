// Package mcp is Tempo Dock's MCP endpoint (ADR 0017): one Model Context
// Protocol server for the whole platform, data tools included, so agents
// (Claude Code and others) operate Dock with their scoped tokens.
//
// It implements the part of MCP Streamable HTTP that Dock needs, statelessly:
// every POST carries one JSON-RPC message and a bearer token, which resolves
// to a core.Actor exactly as the API's does. Every tool calls internal/core
// with that actor, so core's permission checks and audit apply unchanged
// and a tool can never do more than the token behind it.
//
// Nothing runs while no agent is calling: the tool and resource tables are
// built on the first request, and a call's work (including an event bus
// subscription while it waits for a deploy) ends with the call.
package mcp

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/version"
)

// maxRequestBytes caps a request body. The largest legitimate one is a
// data changeset of 500 rows or a long SQL statement.
const maxRequestBytes = 4 << 20

// writeTimeout bounds each write to a client, so one that stops reading
// can't hold a call (and its event subscription) open.
const writeTimeout = 15 * time.Second

// protocolVersions are the MCP versions served, newest first. Every one
// has what Dock uses: tools, resources, progress notifications.
var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

// Options configure the endpoint.
type Options struct {
	Logger *slog.Logger
}

// handler serves the endpoint for one Core.
type handler struct {
	core *core.Core
	log  *slog.Logger

	once      sync.Once
	tools     []*tool
	toolsByID map[string]*tool
	toolsList []byte // the tools/list result, encoded once
	docs      []doc
}

// Handler returns the MCP endpoint, which the API mounts at /api/v1/mcp.
func Handler(c *core.Core, opts Options) http.Handler {
	return &handler{core: c, log: cmp.Or(opts.Logger, slog.Default())}
}

// build fills the tool and resource tables on first use.
func (h *handler) build() {
	h.once.Do(func() {
		h.addPlatformTools()
		h.addDataTools()
		h.toolsByID = make(map[string]*tool, len(h.tools))
		defs := make([]*tool, len(h.tools))
		for i, t := range h.tools {
			h.toolsByID[t.Name] = t
			defs[i] = t
		}
		var err error
		if h.toolsList, err = json.Marshal(map[string]any{"tools": defs}); err != nil {
			panic(err) // schemas are built from Go types; a failure is a bug
		}
		h.docs = loadDocs()
	})
}

// instructions are sent to the client on initialize; clients show them to
// the model, so they state the safety model up front.
const instructions = `Tempo Dock deploys Docker apps to your own servers. You act with a scoped API token.

- Call whoami first: it shows the token's scope (read or deploy) and the project and environment it is limited to. Tools default project and environment to those limits.
- The server enforces every permission. A forbidden error means the token can't do it: stop and report it; don't look for another credential or a workaround.
- Deploys are safe: a release only gets traffic after its health check passes. A failed deploy changed nothing and needs no rollback; read release_details for the reason and the container's last log lines, fix, deploy again. Roll back only a release that went live and misbehaves.
- Variables are staged: set_variables, review with show_changes, then apply_changes (or the next deploy takes them live). Secret values are write-only.
- Data tools follow the data access policy; data_status says what you may do and why. Data changes are proposals a human approves; tokens never write production data.
- Every change is audited with the token's name. Read dock://docs/agents for the full guide.`

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
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		problem(w, http.StatusMethodNotAllowed, apitypes.CodeValidation, "the MCP endpoint takes POST requests only; it keeps no sessions or streams")
		return
	}
	// Browsers can't send the bearer token cross-site without a CORS
	// preflight, which the daemon never allows; refusing foreign origins
	// on top is what the MCP spec asks of servers.
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
	actor, ok := h.authenticate(w, r)
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

	h.build()
	out := &responder{w: w, id: msg.ID, streams: acceptsStream(r)}
	result, err := h.dispatch(r.Context(), out, actor, msg)
	resp := rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: result}
	if err != nil {
		var re *rpcError
		if !errors.As(err, &re) {
			h.log.Error("mcp: "+msg.Method, "err", err)
			re = &rpcError{-32603, "internal error; see the daemon log"}
		}
		resp.Result, resp.Error = nil, re
	}
	out.finish(resp)
}

// dispatch runs one request.
func (h *handler) dispatch(ctx context.Context, out *responder, a core.Actor, msg rpcMessage) (any, error) {
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
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{}, "resources": map[string]any{}},
			"serverInfo":      map[string]string{"name": "tempo-dock", "title": "Tempo Dock", "version": version.Version},
			"instructions":    instructions,
		}, nil
	case "ping":
		return struct{}{}, nil
	case "tools/list":
		return json.RawMessage(h.toolsList), nil
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
		t, ok := h.toolsByID[p.Name]
		if !ok {
			return nil, &rpcError{codeInvalidParams, "unknown tool " + p.Name + "; tools/list shows them"}
		}
		c := &call{actor: a, out: out, progressToken: p.Meta.ProgressToken}
		return h.run(ctx, t, c, p.Arguments), nil
	case "resources/list":
		return map[string]any{"resources": h.docs}, nil
	case "resources/templates/list":
		return map[string]any{"resourceTemplates": []any{}}, nil
	case "resources/read":
		var p struct {
			URI string `json:"uri"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		return readDoc(h.docs, p.URI)
	}
	return nil, &rpcError{codeNoMethod, "method " + msg.Method + " is not supported"}
}

// authenticate resolves the bearer token to an actor, as the API does.
// Only tokens are accepted (API tokens and `dock login` tokens): no
// cookies, so a browser page can't call tools on someone's behalf.
func (h *handler) authenticate(w http.ResponseWriter, r *http.Request) (core.Actor, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="dock"`)
		problem(w, http.StatusUnauthorized, apitypes.CodeUnauthorized, "send `Authorization: Bearer <token>` with a Dock API token")
		return core.Actor{}, false
	}
	a, err := h.core.AuthenticateToken(r.Context(), strings.TrimSpace(token))
	if err != nil {
		if core.CodeOf(err) == apitypes.CodeUnauthorized {
			w.Header().Set("WWW-Authenticate", `Bearer realm="dock", error="invalid_token"`)
			problem(w, http.StatusUnauthorized, apitypes.CodeUnauthorized, err.Error())
		} else {
			h.log.Error("mcp: authenticate", "err", err)
			problem(w, http.StatusInternalServerError, apitypes.CodeInternal, "internal error; see the daemon log")
		}
		return core.Actor{}, false
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
