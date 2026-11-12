package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
)

// Access requests over MCP (ADR 0044): a change a locked environment
// refuses becomes a request (refused), approving it runs the tool again
// (replay), and three tools ask first, wait and list.

// refused files an access request for a tool call a locked environment
// refused; other errors pass through.
func (h *handler) refused(ctx context.Context, a core.Actor, t *mcpkit.ToolInfo, args json.RawMessage, err error) error {
	if t.Annotations.ReadOnly {
		return err
	}
	return h.core.FileRequest(ctx, a, err, core.Call{Via: "mcp", Op: t.Name, Title: t.Title, Args: args})
}

// replay is MCP's core.Replayer: it runs the tool again and returns its
// reply as {"summary", "data"}.
func (h *handler) replay(ctx context.Context, a core.Actor, tool string, args json.RawMessage) (json.RawMessage, error) {
	r, err := h.srv.Call(ctx, a, tool, args)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"summary": r.Summary, "data": r.Data})
}

// notAskable are tools request_access refuses: its own, and data tools,
// whose changes are changesets an admin approves in the Data panel.
func notAskable(name string) bool {
	return strings.HasSuffix(name, "_request") || strings.HasSuffix(name, "_requests") || name == "request_access" ||
		strings.HasPrefix(name, "data_") || name == "propose_data_changes"
}

// requestView is a request for the model.
type requestView struct {
	ID        int64                        `json:"id"`
	Status    apitypes.AccessRequestStatus `json:"status"`
	Summary   string                       `json:"summary"`
	Tool      string                       `json:"tool,omitempty"`
	Reason    string                       `json:"reason,omitempty"`
	ExpiresAt time.Time                    `json:"expires_at"`
	DecidedBy string                       `json:"decided_by,omitempty"`
	Comment   string                       `json:"comment,omitempty"`
	Error     string                       `json:"error,omitempty"`
	Result    json.RawMessage              `json:"result,omitempty"`
}

func viewRequest(r apitypes.AccessRequest) requestView {
	v := requestView{ID: r.ID, Status: r.Status, Summary: r.Summary, Reason: r.Reason, ExpiresAt: r.ExpiresAt,
		DecidedBy: r.DecidedBy, Comment: r.Comment, Error: r.Error, Result: r.Result}
	if r.Via == "mcp" {
		v.Tool = r.Operation
	}
	return v
}

// requestLine says where a request is, in a sentence.
func requestLine(r apitypes.AccessRequest) string {
	switch r.Status {
	case apitypes.RequestPending:
		return fmt.Sprintf("Access request #%d waits for an admin's decision in the UI (expires %s).", r.ID, r.ExpiresAt.Format(time.RFC3339))
	case apitypes.RequestApproved:
		return fmt.Sprintf("Access request #%d was approved by %s and is running.", r.ID, r.DecidedBy)
	case apitypes.RequestDone:
		return fmt.Sprintf("Access request #%d was approved by %s and ran.", r.ID, r.DecidedBy)
	case apitypes.RequestFailed:
		return fmt.Sprintf("Access request #%d was approved by %s but failed: %s", r.ID, r.DecidedBy, r.Error)
	case apitypes.RequestRejected:
		return fmt.Sprintf("Access request #%d was rejected by %s%s. Don't retry it; ask the user.", r.ID, r.DecidedBy, commentSuffix(r.Comment))
	}
	if r.Comment != "" { // ended early, e.g. its environment was unlocked
		return fmt.Sprintf("Access request #%d ended without a decision: %s.", r.ID, r.Comment)
	}
	return fmt.Sprintf("Access request #%d expired without a decision.", r.ID)
}

func commentSuffix(c string) string {
	if c == "" {
		return ""
	}
	return ": " + c
}

// addRequestTools registers request_access, wait_for_request and
// list_requests.
func (h *handler) addRequestTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "request_access", "Ask before a change",
		"Ask a person to approve a change first: once approved, the tool runs as you with these arguments. Also adds a reason to a request a locked environment filed for the same call.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			Tool      string          `json:"tool"`
			Arguments json.RawMessage `json:"arguments"`
			Reason    string          `json:"reason" maxLength:"500" doc:"Why, for the approver."`
		}) (mcpkit.Reply, error) {
			t, ok := h.srv.Tool(in.Tool)
			switch {
			case !ok:
				return mcpkit.Reply{}, mcpkit.Invalid("unknown tool %s; tools/list shows them", in.Tool)
			case t.Annotations.ReadOnly:
				return mcpkit.Reply{}, mcpkit.Invalid("%s only reads; call it directly", in.Tool)
			case notAskable(in.Tool):
				return mcpkit.Reply{}, mcpkit.Invalid("%s can't be asked for; data changes are proposals (propose_data_changes)", in.Tool)
			}
			// A reason among the arguments is the request's, as on a direct
			// call; the stored arguments go without it.
			reason, args := h.srv.SplitReason(in.Tool, in.Arguments)
			if in.Reason == "" {
				in.Reason = reason
			}
			in.Arguments = args
			_, err := h.srv.Call(ctx, c.Actor.AskFirst(), in.Tool, in.Arguments)
			if err == nil {
				return mcpkit.Reply{}, mcpkit.Invalid("%s finished without asking: it changed nothing that needs approval", in.Tool)
			}
			err = h.core.FileRequest(ctx, c.Actor, err, core.Call{Via: "mcp", Op: t.Name, Title: t.Title, Args: in.Arguments, Reason: in.Reason})
			le, ok := errors.AsType[*core.LockedError](err)
			if !ok || le.Request == 0 {
				return mcpkit.Reply{}, err
			}
			r, err := h.core.GetRequest(ctx, c.Actor, le.Request)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: requestLine(r) + " Follow it with wait_for_request.", Data: viewRequest(r)}, nil
		})

	// Read-only though reason annotates the request: only the caller's
	// own, which it filed; clients shouldn't ask a person to allow a wait.
	mcpkit.AddTool(s, "wait_for_request", "Wait for an access request",
		"Wait for an access request's decision and, if approved, its run. A rejection is final: report it.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			ID     int64  `json:"id" minimum:"1"`
			Reason string `json:"reason,omitempty" maxLength:"500" doc:"Why, for the approver, if the request has no reason yet."`
			WaitInput
		}) (mcpkit.Reply, error) {
			wait, err := in.duration()
			if err != nil {
				return mcpkit.Reply{}, err
			}
			if in.Reason != "" {
				if err := h.core.GiveRequestReason(ctx, c.Actor, in.ID, in.Reason); err != nil {
					return mcpkit.Reply{}, err
				}
			}
			c.Notify(fmt.Sprintf("waiting for access request #%d", in.ID))
			r, err := h.core.WaitRequest(ctx, c.Actor, in.ID, wait)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: requestLine(r), Data: viewRequest(r)}, nil
		})

	mcpkit.AddTool(s, "list_requests", "List access requests",
		"Your access requests, newest first.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			Status apitypes.AccessRequestStatus `json:"status,omitempty" enum:"pending,approved,rejected,expired,done,failed"`
		}) (mcpkit.Reply, error) {
			rs, err := h.core.ListRequests(ctx, c.Actor, core.RequestFilter{Status: in.Status, Limit: 50})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			out := make([]requestView, len(rs))
			for i, r := range rs {
				out[i] = viewRequest(r)
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%d access requests.", len(out)), Data: map[string]any{"requests": out}}, nil
		})
}
