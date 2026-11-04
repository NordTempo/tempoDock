package mcpkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
)

// tool is one MCP tool: its definition as tools/list shows it, and how to
// run it.
type tool[A any] struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations Annotations     `json:"annotations"`

	run func(context.Context, *Call[A], json.RawMessage) (Reply, error)
}

// Annotations are hints for clients, e.g. to auto-approve read-only tools.
// Every tool is closed-world: it only acts on its own host.
type Annotations struct {
	ReadOnly    bool  `json:"readOnlyHint"`
	Destructive *bool `json:"destructiveHint,omitempty"`
	OpenWorld   bool  `json:"openWorldHint"`
}

// Kinds of tools: read-only ones change nothing; mutating ones change the
// host but destroy nothing; destructive ones throw something away (staged
// changes, rows).
var (
	ReadOnly    = Annotations{ReadOnly: true}
	Mutating    = Annotations{Destructive: new(false)}
	Destructive = Annotations{Destructive: new(true)}
)

// Call is one tool call: who makes it and where its progress goes.
type Call[A any] struct {
	Actor         A
	out           *responder
	progressToken json.RawMessage // nil: the client wants no progress
	progress      int
}

// Notify sends a progress notification, if the client asked for them by
// sending a progress token with the call.
func (c *Call[A]) Notify(msg string) {
	if len(c.progressToken) == 0 {
		return
	}
	c.progress++
	c.out.notify("notifications/progress", map[string]any{"progressToken": c.progressToken, "progress": c.progress, "message": msg})
}

// Reply is a tool's result: a one-line summary for people and the model,
// and the data, sent as compact JSON after it.
type Reply struct {
	Summary string
	Data    any
}

// AddTool registers a tool; call it from Options.Tools. Its input schema
// comes from In through Huma, like the API's request schemas: json names,
// `doc` descriptions, enum, minimum and maximum, required unless
// omitempty, no unknown properties. Embedded input types must be exported
// for Huma to see their fields. Arguments are validated against it before
// fn runs as the caller.
func AddTool[A, In any](s *Server[A], name, title, description string, ann Annotations, fn func(context.Context, *Call[A], In) (Reply, error)) {
	reg := huma.NewMapRegistry("#/$defs/", huma.DefaultSchemaNamer)
	schema := reg.Schema(reflect.TypeFor[In](), false, name)
	addTool(s, name, title, description, ann, reg, schema, func(ctx context.Context, c *Call[A], args json.RawMessage) (Reply, error) {
		var in In
		if err := json.Unmarshal(args, &in); err != nil {
			return Reply{}, Invalid("invalid arguments: %v", err)
		}
		return fn(ctx, c, in)
	})
}

// addLocatedTool registers a tool whose input is the host's locator L (a
// service, a connection) and the tool's own In, as one object: L's
// properties are merged into In's schema. Go can't embed a type
// parameter, so the two are decoded from the same arguments.
func addLocatedTool[A, L, In any](s *Server[A], name, title, description string, ann Annotations, fn func(context.Context, *Call[A], L, In) (Reply, error)) {
	reg := huma.NewMapRegistry("#/$defs/", huma.DefaultSchemaNamer)
	loc := reg.Schema(reflect.TypeFor[L](), false, name+"Locator")
	own := reg.Schema(reflect.TypeFor[In](), false, name)
	schema := &huma.Schema{Type: huma.TypeObject, Properties: map[string]*huma.Schema{}, AdditionalProperties: false}
	// The tool's own required properties first, as Huma lists them for a
	// struct that embeds the locator.
	for _, part := range []*huma.Schema{own, loc} {
		for k, p := range part.Properties {
			schema.Properties[k] = p
		}
		schema.Required = append(schema.Required, part.Required...)
	}
	schema.PrecomputeMessages()
	addTool(s, name, title, description, ann, reg, schema, func(ctx context.Context, c *Call[A], args json.RawMessage) (Reply, error) {
		var l L
		var in In
		if err := json.Unmarshal(args, &l); err != nil {
			return Reply{}, Invalid("invalid arguments: %v", err)
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return Reply{}, Invalid("invalid arguments: %v", err)
		}
		return fn(ctx, c, l, in)
	}, loc, own)
}

// addTool registers a tool with its schema; skip are schemas in reg that
// are parts of it rather than definitions it refers to.
func addTool[A any](s *Server[A], name, title, description string, ann Annotations, reg huma.Registry, schema *huma.Schema,
	decode func(context.Context, *Call[A], json.RawMessage) (Reply, error), skip ...*huma.Schema) {
	defs := map[string]*huma.Schema{}
	for k, d := range reg.Map() {
		if d != schema && !slices.Contains(skip, d) {
			defs[k] = d
		}
	}
	js, err := json.Marshal(schema)
	if err == nil && len(defs) > 0 {
		// Nested types are $refs into $defs, so the schema stands alone.
		var top map[string]json.RawMessage
		if err = json.Unmarshal(js, &top); err == nil {
			if top["$defs"], err = json.Marshal(defs); err == nil {
				js, err = json.Marshal(top)
			}
		}
	}
	if err != nil {
		panic(fmt.Sprintf("tool %s: input schema: %v", name, err))
	}
	s.tools = append(s.tools, &tool[A]{
		Name: name, Title: title, Description: description, InputSchema: js, Annotations: ann,
		run: func(ctx context.Context, c *Call[A], args json.RawMessage) (Reply, error) {
			if len(args) == 0 || string(args) == "null" {
				args = json.RawMessage("{}")
			}
			var v any
			if err := json.Unmarshal(args, &v); err != nil {
				return Reply{}, Invalid("arguments must be a JSON object: %v", err)
			}
			res := &huma.ValidateResult{}
			huma.Validate(reg, schema, huma.NewPathBuffer([]byte{}, 0), huma.ModeWriteToServer, v, res)
			if len(res.Errors) > 0 {
				// Location and message only: the offending value may be a
				// secret (Dock's set_variables).
				msgs := make([]string, len(res.Errors))
				for i, e := range res.Errors {
					msgs[i] = e.Error()
					if d, ok := errors.AsType[*huma.ErrorDetail](e); ok {
						msgs[i] = strings.TrimPrefix(d.Location+": "+d.Message, ": ")
					}
				}
				return Reply{}, Invalid("invalid arguments: %s", strings.Join(msgs, "; "))
			}
			return decode(ctx, c, args)
		},
	})
}

// toolResult is a tools/call result.
type toolResult struct {
	Content []textContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ToolInfo is what a host needs to know of a tool outside its call.
type ToolInfo struct {
	Name, Title string
	Annotations Annotations
}

// Tool returns a tool's info, or false for no such tool.
func (s *Server[A]) Tool(name string) (*ToolInfo, bool) {
	s.build()
	t, ok := s.toolsByID[name]
	if !ok {
		return nil, false
	}
	return &ToolInfo{Name: t.Name, Title: t.Title, Annotations: t.Annotations}, true
}

// Call runs a tool as a, outside any request (no progress), and returns
// its reply: how a host runs a stored call again. Errors are returned as
// they are, without Options.Refused.
func (s *Server[A]) Call(ctx context.Context, a A, name string, args json.RawMessage) (Reply, error) {
	s.build()
	t, ok := s.toolsByID[name]
	if !ok {
		return Reply{}, Invalid("unknown tool %s", name)
	}
	return t.run(ctx, &Call[A]{Actor: a}, args)
}

// run calls a tool and renders its reply as one text block: the summary,
// a blank line and the data as compact JSON. Errors are tool errors the
// model can read, not protocol errors.
func (s *Server[A]) run(ctx context.Context, t *tool[A], c *Call[A], args json.RawMessage) toolResult {
	r, err := t.run(ctx, c, args)
	if err != nil {
		if s.o.Refused != nil {
			err = s.o.Refused(ctx, c.Actor, &ToolInfo{Name: t.Name, Title: t.Title, Annotations: t.Annotations}, args, err)
		}
		return s.fail(err)
	}
	text := r.Summary
	if r.Data != nil {
		b, err := json.Marshal(r.Data)
		if err != nil {
			return s.fail(err)
		}
		text += "\n\n" + string(b)
	}
	return toolResult{Content: []textContent{{"text", text}}}
}

// toolError is the JSON of a failed call, in the shape of the CLI's
// errors: {"error": {"code", "message", "hint"}}, plus the access request
// a refused change filed.
type toolError struct {
	Error struct {
		Code    apitypes.ErrorCode `json:"code"`
		Message string             `json:"message"`
		Hint    string             `json:"hint,omitempty"`
		Request int64              `json:"request,omitempty"`
	} `json:"error"`
}

// accessRequester is an error that names an access request (Dock's
// core.LockedError).
type accessRequester interface {
	error
	AccessRequest() int64
}

// fail renders an error as a tool error. Domain errors (datapanel.Error,
// which Dock's core.Error is) keep their code and message; anything
// unexpected is logged and hidden.
func (s *Server[A]) fail(err error) toolResult {
	var te toolError
	te.Error.Code, te.Error.Message = apitypes.CodeInternal, "internal error; see the daemon log"
	switch de, ok := errors.AsType[*datapanel.Error](err); {
	case ok:
		te.Error.Code, te.Error.Message = de.Code, de.Msg
	case errors.Is(err, context.Canceled):
		te.Error.Message = "the call was cancelled" // the client went away
	default:
		s.log.Error("mcp: tool", "err", err)
	}
	te.Error.Hint = s.o.Hints[te.Error.Code]
	if r, ok := errors.AsType[accessRequester](err); ok {
		te.Error.Request = r.AccessRequest()
	}
	b, _ := json.Marshal(te)
	return toolResult{IsError: true, Content: []textContent{{"text", fmt.Sprintf("%s: %s\n\n%s", te.Error.Code, te.Error.Message, b)}}}
}

// Invalid is a validation error raised by a tool itself: input the host
// can't see (defaults it couldn't fill, out-of-range limits).
func Invalid(format string, args ...any) error {
	return datapanel.Errorf(apitypes.CodeValidation, format, args...)
}

// Limit returns n, or def for 0; out of [1, max] is invalid.
func Limit(name string, n, def, max int) (int, error) {
	switch {
	case n == 0:
		return def, nil
	case n < 0 || n > max:
		return 0, Invalid("%s must be between 1 and %d", name, max)
	}
	return n, nil
}
