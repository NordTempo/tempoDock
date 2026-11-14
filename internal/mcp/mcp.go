// Package mcp is Tempo Dock's MCP endpoint (ADR 0017): one Model Context
// Protocol server for the whole platform, data tools included, so agents
// (Claude Code and others) operate Dock with their scoped tokens.
//
// The transport, tool registry, docs resources and data tools are
// internal/mcpkit's, shared with tempoBase (ADR 0024); this package adds
// Dock's authentication, platform tools and guide. Every POST's bearer
// token resolves to a core.Actor exactly as the API's does, and every tool
// calls internal/core with that actor, so core's permission checks and
// audit apply unchanged and a tool can never do more than the token behind
// it.
//
// Nothing runs while no agent is calling: the tool and resource tables are
// built on the first request, and a call's work (including an event bus
// subscription while it waits for a deploy) ends with the call.
package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/NordTempo/tempoDock/docs/guide"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
	"github.com/NordTempo/tempoDock/internal/version"
)

// Options configure the endpoint.
type Options struct {
	Logger *slog.Logger
}

// handler holds what Dock's tools call.
type handler struct {
	core *core.Core
	// srv is the endpoint itself, for request_access to run a tool.
	srv *mcpkit.Server[core.Actor]
}

// call is one tool call by a Dock actor.
type call = mcpkit.Call[core.Actor]

// orgKey carries the request's `org` parameter to Authenticate.
type orgKey struct{}

// Handler returns the MCP endpoint, which the API mounts at /api/v1/mcp.
// Only tokens are accepted (API tokens and `dock login` tokens). A `dock
// login` token of a user in several orgs names one in the URL:
// /api/v1/mcp?org=<slug>.
func Handler(c *core.Core, opts Options) http.Handler {
	h := &handler{core: c}
	srv := mcpkit.New(mcpkit.Options[core.Actor]{
		Name: "tempo-dock", Title: "Tempo Dock", Version: version.Version,
		Instructions: instructions,
		Authenticate: func(ctx context.Context, token string) (core.Actor, error) {
			org, _ := ctx.Value(orgKey{}).(string)
			return c.AuthenticateToken(ctx, token, org)
		},
		Caller: h.caller,
		View:   h.view,
		WithReason: func(ctx context.Context, r string) context.Context {
			return core.WithReason(ctx, r[:min(len(r), maxReason)])
		},
		Realm:   "dock",
		NoToken: "send `Authorization: Bearer <token>` with a Dock API token",
		Tools: func(s *mcpkit.Server[core.Actor]) {
			h.addPlatformTools(s)
			h.addRequestTools(s)
			mcpkit.DataTools(s, c, serviceOf, core.ServiceRef.String)
			mcpkit.DataKeyTools(s, c, serviceOf)
		},
		Refused: h.refused,
		Docs: mcpkit.Docs{
			FS: guide.Pages, URI: "dock://docs/",
			IndexDescription: "Index of the Tempo Dock guide and the words it uses (project, environment, service, release, ...).",
		},
		Hints:  hints,
		Logger: opts.Logger,
	})
	h.srv = srv
	c.SetReplayer("mcp", h.replay)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), orgKey{}, r.URL.Query().Get("org"))))
	})
}

// caller is the instructions' last line: who the token is and what it may
// do, and for a token limited to a project, its environments, which tools
// then need named.
func (h *handler) caller(ctx context.Context, a core.Actor) string {
	line := "You act as " + whoLine(a.Me())
	if a.ProjectName == "" || a.EnvironmentName != "" {
		return line
	}
	envs, err := h.core.ListEnvironments(ctx, a, a.ProjectName)
	if err != nil || len(envs) == 0 {
		return line
	}
	names := make([]string, len(envs))
	for i, e := range envs {
		names[i] = e.Name + " (" + string(e.Kind) + ")"
	}
	return line + " Its environments: " + strings.Join(names, ", ") + "."
}

// maxReason caps a reason's length in bytes, as the API's Dock-Reason.
const maxReason = 500

// dataTools are the data tools a caller without data access can't use;
// data_status stays, to say why.
var dataTools = []string{"data_schema", "data_rows", "data_keys", "data_query", "data_insights", "data_cancel", "propose_data_changes"}

// platformTools are the tools a platform credential lists: who it is,
// the servers and the platform's alerts (list_alerts with platform).
var platformTools = []string{"whoami", "list_servers", "list_alerts"}

// view is the tools an actor's tools/list shows (mcpkit.View): the
// arguments its limits fill and the tools it can never use are left out.
// A token limited to one environment can't promote, clone or see other
// projects, and without data access there (the default for tokens) has
// data_status only; only the platform's operators and viewers list servers, only admins registry
// credentials (connections.manage), and nobody a tool whose permission
// they hold nowhere (toolPerms); read tokens change nothing. A client lists tools once per
// session: access granted later shows in a new one (the tools answer
// calls either way).
func (h *handler) view(ctx context.Context, a core.Actor) mcpkit.View {
	var v mcpkit.View
	if a.Me().PlatformLogin {
		// A platform credential acts in no org (ADR 0080): its tools are
		// the platform's few.
		v.Only = platformTools
		return v
	}
	if a.ProjectName != "" {
		v.Implied = append(v.Implied, "project")
		v.Hide = append(v.Hide, "list_projects")
	}
	if a.EnvironmentName != "" {
		v.Implied = append(v.Implied, "environment")
		v.Hide = append(v.Hide, "promote", "clone_environment")
		if l, err := h.core.DataLevel(ctx, a, core.EnvRef{Project: a.ProjectName, Environment: a.EnvironmentName}); err == nil && l == apitypes.LevelNone {
			v.Hide = append(v.Hide, dataTools...)
		}
	}
	if a.Platform == "" {
		v.Hide = append(v.Hide, "list_servers")
	}
	// What the caller holds nowhere can't work for it (toolPerms).
	for name, p := range toolPerms {
		if !a.Holds(p) {
			v.Hide = append(v.Hide, name)
		}
	}
	v.ReadOnly = a.Scope == apitypes.ScopeRead
	return v
}

// instructions are sent to the client on initialize; clients show them to
// the model, so they state the safety model up front.
const instructions = `Tempo Dock deploys Docker apps to your own servers. You act with a scoped token; who it is and its limits are at the end, and tools default project and environment to them.

- A forbidden error means the token can't: report it; never look for another credential or a workaround.
- A failed deploy never got traffic and needs no rollback: diagnose, fix, deploy again. Roll back only a release that went live and misbehaves.
- Tools that start releases wait for them; for one still deploying, wait_for_release.
- Variables, settings and volumes are staged until apply_changes, or apply on the staging tool. Secret values are write-only.
- A locked environment turns a change into an access request a person approves or rejects: tell the user, then wait_for_request; don't retry. Any tool takes reason, the approver reads it; request_access asks first.
- Data tools follow the data access policy: data_status says what you may do and why (without access it is the only data tool listed). Tokens never write production data.
- Only a person archives services or projects, deletes snapshots, locks environments or creates tokens, and nothing deletes volumes: say so; don't work around it (e.g. with run_command).
- Every change is audited. dock://docs/agents is the full guide.`

// hints tell the model what to do next where the message doesn't.
var hints = map[apitypes.ErrorCode]string{
	apitypes.CodeForbidden:        "The token can't do this. Stop and tell the user what refusal says (why, and who or what can allow it); don't look for another credential or a workaround.",
	apitypes.CodeDeployInProgress: "Another release of this service is deploying (its number is in release). Call wait_for_release, then retry.",
	apitypes.CodeNotFound:         "Check names with list_projects and project_overview. Don't create things to make it pass unless asked.",
	apitypes.CodeServerOffline:    "The server the environment runs on is offline. Retry later, then report it.",
	apitypes.CodeEnvironmentLocked: "A human locked this environment. This call filed an access request (its number is in request): tell the user what you asked for and why, then call wait_for_request with it and your reason; " +
		"approved, the change runs as you. Don't retry, work around it or look for another token. Staging tools take apply, so one request covers staging and applying.",
	apitypes.CodeInternal:      "Something failed inside Dock (the daemon log has it). Retry once; if it fails again, stop and report it.",
	apitypes.CodeValidation:    "Fix the arguments the message names and call again. Don't guess values the user didn't give you; ask.",
	apitypes.CodeConflict:      "It clashes with what exists. Look first (project_overview, show_changes, list_domains); don't remove or rename things to make room unless asked.",
	apitypes.CodeHostnameTaken: "Another service has this hostname, and a hostname belongs to one service. Report it; don't remove it from the other service unless asked.",
	apitypes.CodeRateLimited:   "Too many attempts. Wait a minute before trying again; don't retry in a loop.",
	apitypes.CodeTooLarge:      "Over the server's size limit. Ask for less (a smaller tail, limit or log_lines) or report it.",
}

// errf is a core error raised by the endpoint itself, mostly invalid
// input core can't see (defaults it couldn't fill).
func errf(code apitypes.ErrorCode, format string, args ...any) error {
	return &core.Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}
