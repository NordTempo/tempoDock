// Package tbmcp is tempoBase's MCP endpoint (ADR 0024 section 2): the
// shared mcpkit transport and data tools over the App, so agents browse,
// query and propose changes to saved connections with their API tokens.
// Every call runs as the token's actor through the App, so the data
// access policy, the token's connection limit and the audit apply
// unchanged. Nothing runs while no agent calls: the tool table is built
// on the first request.
package tbmcp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	tbdocs "github.com/NordTempo/tempoDock/docs/tempobase"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
	"github.com/NordTempo/tempoDock/internal/tempobase"
	"github.com/NordTempo/tempoDock/internal/version"
)

// call is one tool call by a tempoBase actor.
type call = mcpkit.Call[tempobase.Actor]

// ConnectionInput names a connection: every data tool takes it.
type ConnectionInput struct {
	Connection string `json:"connection,omitempty" doc:"Connection name, e.g. orders. Default: the connection your token is limited to, if only one (list_connections shows them)."`
}

// Handler returns the MCP endpoint, which the API mounts at /api/v1/mcp.
// Only API tokens are accepted, never session cookies.
func Handler(app *tempobase.App, log *slog.Logger) http.Handler {
	return mcpkit.New(mcpkit.Options[tempobase.Actor]{
		Name: "tempobase", Title: "tempoBase", Version: version.Version,
		Instructions: instructions,
		Authenticate: app.AuthenticateToken,
		Realm:        "tempobase",
		NoToken:      "send `Authorization: Bearer <token>` with a tempoBase API token (Settings, Tokens)",
		Tools: func(s *mcpkit.Server[tempobase.Actor]) {
			addTools(s, app)
			mcpkit.DataTools(s, app, connectionOf, func(name string) string { return name })
		},
		Docs: mcpkit.Docs{
			FS: tbdocs.Pages, URI: "tempobase://docs/",
			IndexDescription: "Index of the tempoBase guide: install, connections, people and the policy, agents.",
		},
		Hints:  hints,
		Logger: log,
	})
}

// instructions are sent on initialize; clients show them to the model,
// so they state the safety model up front.
const instructions = `tempoBase is a Postgres viewer with safe editing. You act with an API token.

- Call whoami first, then list_connections: it shows the connections you may use and your access on each.
- Access comes from the data access policy an admin sets, capped by your token owner's role. data_status says what you may do and why. A forbidden error means the token can't do it: stop and report it; don't look for another credential or a workaround.
- Reads run read-only as a least-privilege role. Writes are proposals (propose_data_changes) that a person reviews and approves; tokens never write to production connections directly.
- Every query and proposal is audited with the token's name. Read tempobase://docs/agents for the full guide.`

// hints tell the model what to do next where the message doesn't.
var hints = map[apitypes.ErrorCode]string{
	apitypes.CodeForbidden: "The token can't do this. Stop and report it; don't look for another credential.",
	apitypes.CodeNotFound:  "Check the connection name with list_connections. A connection your access doesn't reach is not found.",
}

// connectionOf resolves a data tool's connection, defaulting to the
// token's only connection. The App still checks the caller may use it.
func connectionOf(c *call, in ConnectionInput) (string, error) {
	if in.Connection != "" {
		return in.Connection, nil
	}
	if names := c.Actor.TokenConnectionNames; len(names) == 1 {
		return names[0], nil
	}
	if c.Actor.TokenLimited && len(c.Actor.TokenConnectionNames) == 0 {
		return "", mcpkit.Invalid("your token's connections were all removed, so it may use none; ask its owner for a new token")
	}
	return "", mcpkit.Invalid("pass connection: your token isn't limited to one (list_connections shows them)")
}

// connectionView is a connection as list_connections shows it.
type connectionView struct {
	Name       string                   `json:"name"`
	Target     string                   `json:"target"`
	Production bool                     `json:"production"`
	Level      apitypes.DataLevel       `json:"level"`
	Sensitive  apitypes.DataSensitivity `json:"sensitive"`
}

// addTools registers whoami and list_connections.
func addTools(s *mcpkit.Server[tempobase.Actor], app *tempobase.App) {
	mcpkit.AddTool(s, "whoami", "Who am I",
		"Who you are acting as: the token's name, its owner and their role (which caps the token), and the connections it is limited to. Call it first.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, _ struct{}) (mcpkit.Reply, error) {
			me, err := app.Me(ctx, c.Actor)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			limit := "every connection the policy allows"
			switch {
			case len(me.TokenConnections) > 0:
				limit = strings.Join(me.TokenConnections, ", ")
			case me.TokenLimited:
				limit = "no connections (its connections were removed; ask its owner for a new token)"
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("token %q of %s (role %s), limited to %s.", me.Name, me.Email, me.Role, limit), Data: me}, nil
		})

	mcpkit.AddTool(s, "list_connections", "List connections",
		"The connections you may use, with whether each is production and your data access there (level and sensitive-column visibility). Doesn't contact the databases; data_status does.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, _ struct{}) (mcpkit.Reply, error) {
			conns, err := app.ListConnections(ctx, c.Actor)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			out := make([]connectionView, len(conns))
			names := make([]string, len(conns))
			for i, cn := range conns {
				out[i] = connectionView{
					Name: cn.Name, Target: cn.User + "@" + cn.Host + "/" + cn.Database, Production: cn.Production,
					Level: cn.Access.Level, Sensitive: cn.Access.Sensitive,
				}
				names[i] = cn.Name
			}
			summary := "No connections you may use; ask an admin for access."
			if len(conns) > 0 {
				summary = fmt.Sprintf("%d connections: %s.", len(conns), strings.Join(names, ", "))
			}
			return mcpkit.Reply{Summary: summary, Data: out}, nil
		})
}
