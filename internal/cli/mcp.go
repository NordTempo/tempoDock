package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// tokenPlaceholder stands for the token in printed MCP configs. Claude
// Code expands ${VAR} in .mcp.json when it starts the server.
const tokenPlaceholder = "${" + clientconfig.EnvToken + "}"

// mcpConfig is `dock mcp`'s output: how an MCP client reaches the server.
type mcpConfig struct {
	URL    string `json:"url"`
	Header string `json:"header"`
	// MCPJSON is the server's entry for an mcp.json / .mcp.json file.
	MCPJSON map[string]mcpServer `json:"mcp_json"`
	// ClaudeCode is the command that adds it to Claude Code's project
	// config (.mcp.json).
	ClaudeCode string `json:"claude_code"`
}

// mcpServer is one server in an mcp.json file.
type mcpServer struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

func newMCPCmd(g *globals) *cobra.Command {
	var showToken bool
	var name string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Print the MCP config that connects an agent to this server",
		Long: `Print how an MCP client (Claude Code, Cursor, ...) connects to Tempo Dock's
MCP endpoint on the current context's server: its URL and the
Authorization header. Agents then deploy, read logs, stage variables and
query data through tools, with the same permissions as the token.

The token is printed as ` + tokenPlaceholder + `, which Claude Code expands from the
environment, so the config can be committed. Give the agent a scoped
token (see ` + "`dock token create --help`" + `), never your own login.
--show-token prints the context's token instead; don't commit that.`,
		Example: `  dock mcp
  dock mcp --json | jq .mcp_json
  claude mcp add --transport http --scope project dock https://dock.example.com/api/v1/mcp \
    --header 'Authorization: Bearer ${DOCK_TOKEN}'`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			r, err := cfg.resolve()
			if err != nil {
				return err
			}
			token := tokenPlaceholder
			if showToken {
				if r.Token == "" {
					return &output.Error{Exit: output.ExitAuth, Message: "no API token for " + r.Server, Hint: "Run `dock login " + r.Server + "`, or leave out --show-token."}
				}
				token = r.Token
			}
			if g.org != "" {
				r.Org = g.org
			}
			c := newMCPConfig(name, r.Server, token, r.Org)
			return g.io.Print(c, func() error {
				entry, _ := json.MarshalIndent(map[string]any{"mcpServers": c.MCPJSON}, "", "  ")
				_, err := fmt.Fprintf(g.io.Out, "MCP endpoint: %s\n\nClaude Code (writes .mcp.json):\n  %s\n\n.mcp.json or another client's mcp.json:\n%s\n",
					c.URL, c.ClaudeCode, entry)
				if err != nil {
					return err
				}
				if showToken {
					g.io.Warnf("This config holds a token. Don't commit it.")
					return nil
				}
				_, err = fmt.Fprintf(g.io.Out, "\nSet %s to a scoped token for the agent, not your own login:\n  dock token create <name> --scope deploy --project P --environment E\n", clientconfig.EnvToken)
				return err
			})
		},
	}
	cmd.Flags().BoolVar(&showToken, "show-token", false, "print the context's token instead of "+tokenPlaceholder)
	cmd.Flags().StringVar(&name, "name", "dock", "the server's name in the MCP config")
	return cmd
}

// newMCPConfig builds the config for the server at serverURL, sending
// token (or the placeholder) as the bearer token, acting in org when set.
func newMCPConfig(name, serverURL, token, org string) mcpConfig {
	u := strings.TrimRight(serverURL, "/") + apiPrefix + "/mcp"
	if org != "" {
		u += "?org=" + url.QueryEscape(org)
	}
	header := "Authorization: Bearer " + token
	return mcpConfig{
		URL:    u,
		Header: header,
		MCPJSON: map[string]mcpServer{name: {
			Type: "http", URL: u, Headers: map[string]string{"Authorization": "Bearer " + token},
		}},
		ClaudeCode: fmt.Sprintf("claude mcp add --transport http --scope project %s %s --header '%s'", name, u, header),
	}
}
