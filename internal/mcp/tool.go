package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// tool is one MCP tool: its definition as tools/list shows it, and how to
// run it.
type tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations annotations     `json:"annotations"`

	run func(context.Context, *call, json.RawMessage) (reply, error)
}

// annotations are hints for clients, e.g. to auto-approve read-only tools.
// Every tool is closed-world: it only acts on this Dock.
type annotations struct {
	ReadOnly    bool  `json:"readOnlyHint"`
	Destructive *bool `json:"destructiveHint,omitempty"`
	OpenWorld   bool  `json:"openWorldHint"`
}

// Kinds of tools: read-only ones change nothing; mutating ones change the
// platform but destroy nothing (archive, delete and token tools aren't
// exposed); destructive ones throw something away (staged changes, rows).
var (
	readOnly    = annotations{ReadOnly: true}
	mutating    = annotations{Destructive: new(false)}
	destructive = annotations{Destructive: new(true)}
)

// call is one tool call: who makes it and where its progress goes.
type call struct {
	actor         core.Actor
	out           *responder
	progressToken json.RawMessage // nil: the client wants no progress
	progress      int
}

// notify sends a progress notification, if the client asked for them by
// sending a progress token with the call.
func (c *call) notify(msg string) {
	if len(c.progressToken) == 0 {
		return
	}
	c.progress++
	c.out.notify("notifications/progress", map[string]any{"progressToken": c.progressToken, "progress": c.progress, "message": msg})
}

// reply is a tool's result: a one-line summary for people and the model,
// and the data as compact JSON.
type reply struct {
	summary string
	data    any
}

// addTool registers a tool. Its input schema comes from In through Huma,
// like the API's request schemas: json names, `doc` descriptions, enum,
// minimum and maximum, required unless omitempty, no unknown properties.
// Embedded input types must be exported for Huma to see their fields.
// Arguments are validated against it before fn runs as the caller.
func addTool[In any](h *handler, name, title, description string, ann annotations, fn func(context.Context, *call, In) (reply, error)) {
	reg := huma.NewMapRegistry("#/$defs/", huma.DefaultSchemaNamer)
	schema := reg.Schema(reflect.TypeFor[In](), false, name)
	defs := map[string]*huma.Schema{}
	for k, s := range reg.Map() {
		if s != schema {
			defs[k] = s
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
	h.tools = append(h.tools, &tool{
		Name: name, Title: title, Description: description, InputSchema: js, Annotations: ann,
		run: func(ctx context.Context, c *call, args json.RawMessage) (reply, error) {
			if len(args) == 0 || string(args) == "null" {
				args = json.RawMessage("{}")
			}
			var v any
			if err := json.Unmarshal(args, &v); err != nil {
				return reply{}, invalid("arguments must be a JSON object: %v", err)
			}
			res := &huma.ValidateResult{}
			huma.Validate(reg, schema, huma.NewPathBuffer([]byte{}, 0), huma.ModeWriteToServer, v, res)
			if len(res.Errors) > 0 {
				// Location and message only: the offending value may be a
				// secret (set_variables).
				msgs := make([]string, len(res.Errors))
				for i, e := range res.Errors {
					msgs[i] = e.Error()
					if d, ok := errors.AsType[*huma.ErrorDetail](e); ok {
						msgs[i] = strings.TrimPrefix(d.Location+": "+d.Message, ": ")
					}
				}
				return reply{}, invalid("invalid arguments: %s", strings.Join(msgs, "; "))
			}
			var in In
			if err := json.Unmarshal(args, &in); err != nil {
				return reply{}, invalid("invalid arguments: %v", err)
			}
			return fn(ctx, c, in)
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

// run calls a tool and renders its reply as one text block: the summary,
// a blank line and the data as compact JSON. Errors are tool errors the
// model can read, not protocol errors.
func (h *handler) run(ctx context.Context, t *tool, c *call, args json.RawMessage) toolResult {
	r, err := t.run(ctx, c, args)
	if err != nil {
		return h.fail(err)
	}
	text := r.summary
	if r.data != nil {
		b, err := json.Marshal(r.data)
		if err != nil {
			return h.fail(err)
		}
		text += "\n\n" + string(b)
	}
	return toolResult{Content: []textContent{{"text", text}}}
}

// toolError is the JSON of a failed call, in the shape of the CLI's
// errors: {"error": {"code", "message", "hint"}}.
type toolError struct {
	Error struct {
		Code    apitypes.ErrorCode `json:"code"`
		Message string             `json:"message"`
		Hint    string             `json:"hint,omitempty"`
	} `json:"error"`
}

// hints tell the model what to do next where the message doesn't.
var hints = map[apitypes.ErrorCode]string{
	apitypes.CodeForbidden:        "The token can't do this. Stop and report it; don't look for another credential.",
	apitypes.CodeDeployInProgress: "Another release of this service is deploying. Wait for it (release_details), then retry.",
	apitypes.CodeNotFound:         "Check names with list_projects and project_overview. Don't create things to make it pass unless asked.",
	apitypes.CodeServerOffline:    "The server the environment runs on is offline. Retry later, then report it.",
}

// fail renders an error as a tool error. Core errors keep their code and
// message; anything unexpected is logged and hidden.
func (h *handler) fail(err error) toolResult {
	var te toolError
	te.Error.Code, te.Error.Message = apitypes.CodeInternal, "internal error; see the daemon log"
	switch ce, ok := errors.AsType[*core.Error](err); {
	case ok:
		te.Error.Code, te.Error.Message = ce.Code, ce.Msg
	case errors.Is(err, context.Canceled):
		te.Error.Message = "the call was cancelled" // the client went away
	default:
		h.log.Error("mcp: tool", "err", err)
	}
	te.Error.Hint = hints[te.Error.Code]
	b, _ := json.Marshal(te)
	return toolResult{IsError: true, Content: []textContent{{"text", fmt.Sprintf("%s: %s\n\n%s", te.Error.Code, te.Error.Message, b)}}}
}

// errf is a core error raised by the endpoint itself, mostly invalid
// input core can't see (defaults it couldn't fill, out-of-range limits).
func errf(code apitypes.ErrorCode, format string, args ...any) error {
	return &core.Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

func invalid(format string, args ...any) error {
	return errf(apitypes.CodeValidation, format, args...)
}

// limit returns n, or def for 0; out of [1, max] is invalid.
func limit(name string, n, def, max int) (int, error) {
	switch {
	case n == 0:
		return def, nil
	case n < 0 || n > max:
		return 0, invalid("%s must be between 1 and %d", name, max)
	}
	return n, nil
}
