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

// instructions are sent to the client on initialize; clients show them to
// the model, so they state the safety model up front.
const instructions = `Tempo Dock deploys Docker apps to your own servers. You act with a scoped API token.

- Call whoami first: it shows the token's scope (read or deploy) and the project and environment it is limited to. Tools default project and environment to those limits.
- The server enforces every permission. A forbidden error means the token can't do it: stop and report it; don't look for another credential or a workaround.
- Deploys are safe: a release only gets traffic after its health check passes. A failed deploy changed nothing and needs no rollback; read release_details for the reason and the container's last log lines, fix, deploy again. Roll back only a release that went live and misbehaves.
- Tools that start releases wait for them. If one is still deploying (yours or someone else's), call wait_for_release; don't poll release_details.
- Variables and settings are staged: set_variables, update_service (port, health check path, image, ...) or set_pre_deploy_command, review with show_changes, then apply_changes (or the next deploy takes them live). Secret values are write-only.
- A locked environment (locked in project_overview) refuses every change with environment_locked and files an access request: a human approves or rejects it in the UI, and once approved it runs as you. Tell the user, then wait_for_request; don't retry or work around it. request_access asks before any change, with your reason, even where nothing is locked.
- Data tools follow the data access policy; data_status says what you may do and why. Data changes are proposals a human approves; tokens never write production data.
- Every change is audited with the token's name. Read dock://docs/agents for the full guide.`

// hints tell the model what to do next where the message doesn't.
var hints = map[apitypes.ErrorCode]string{
	apitypes.CodeForbidden:        "The token can't do this. Stop and report it; don't look for another credential.",
	apitypes.CodeDeployInProgress: "Another release of this service is deploying (its number is in release). Call wait_for_release, then retry.",
	apitypes.CodeNotFound:         "Check names with list_projects and project_overview. Don't create things to make it pass unless asked.",
	apitypes.CodeServerOffline:    "The server the environment runs on is offline. Retry later, then report it.",
	apitypes.CodeEnvironmentLocked: "A human locked this environment. This call filed an access request (its number is in request): tell the user what you asked for and why, then call wait_for_request; " +
		"approved, the change runs as you. Or ask a human to unlock the environment. Don't retry, work around it or look for another token. Next time ask first with request_access and a reason.",
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
