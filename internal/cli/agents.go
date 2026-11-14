package cli

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Your agents (ADR 0080): `dock agent settings` and `dock agent set`, what
// your `dock login` tokens may use of your access in an organization.

var agentKinds = []string{"production", "other", "previews"}

func newAgentCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "agent", Short: "What your agents (`dock login` tokens) may do with your access"})
	var agent int64
	settings := &cobra.Command{
		Use: "settings", Short: "Each area role's level for your agents, per environment kind, and where they ask you first", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			cur, err := get[apitypes.AgentSettings](cmd.Context(), s, fmt.Sprintf("%s/me/agent-settings?agent=%d", apiPrefix, agent))
			if err != nil {
				return err
			}
			return printAgentSettings(g, cur)
		},
	}
	settings.Flags().Int64Var(&agent, "agent", 0, "one agent's narrowing (its token ID, from dock token list --mine) instead of all your agents'")

	var kinds []string
	var ask, noAsk bool
	set := &cobra.Command{
		Use:   "set AREA=LEVEL...",
		Short: "Set areas' levels for your agents: a level of the area (`dock role list`), all or off",
		Long: `Sets what your agents may use of your access, for every environment kind or
only --kinds. --ask makes their changes there wait for your approval (the
Inbox); --no-ask stops that. An agent may tighten this; loosening it needs
you, signed in on the Account page. --agent narrows one agent only.`,
		Example: `  dock agent set deploy=off --kinds production
  dock agent set deploy=edit data=read --kinds other,previews --ask`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, k := range kinds {
				if !slices.Contains(agentKinds, k) {
					return usageError(fmt.Sprintf("--kinds takes production, other and previews, not %q", k), "")
				}
			}
			if len(kinds) == 0 {
				kinds = agentKinds
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			path := fmt.Sprintf("%s/me/agent-settings?agent=%d", apiPrefix, agent)
			cur, err := get[apitypes.AgentSettings](cmd.Context(), s, path)
			if err != nil {
				return err
			}
			req := apitypes.AgentSettings{Agent: agent}
			for _, arg := range args {
				area, level, ok := strings.Cut(arg, "=")
				if !ok {
					return usageError(fmt.Sprintf("write each as AREA=LEVEL, like deploy=view, not %q", arg), "")
				}
				for _, c := range cur.Settings {
					if c.Area != area || !slices.Contains(kinds, c.Kind) {
						continue
					}
					c.Level = level
					if ask || noAsk {
						c.Ask = ask
					}
					req.Settings = append(req.Settings, c)
				}
				if !slices.ContainsFunc(cur.Settings, func(c apitypes.AgentSetting) bool { return c.Area == area }) {
					return usageError(fmt.Sprintf("%q isn't an area role", area), "`dock role list` lists them, like deploy")
				}
			}
			next, err := send[apitypes.AgentSettings](cmd.Context(), s, http.MethodPut, apiPrefix+"/me/agent-settings", req)
			if err != nil {
				return err
			}
			return printAgentSettings(g, next)
		},
	}
	set.Flags().StringSliceVar(&kinds, "kinds", nil, "only these environment kinds: production, other, previews (default all)")
	set.Flags().BoolVar(&ask, "ask", false, "their changes there wait for your approval")
	set.Flags().BoolVar(&noAsk, "no-ask", false, "stop asking you first there")
	set.Flags().Int64Var(&agent, "agent", 0, "narrow one agent only (its token ID)")
	set.MarkFlagsMutuallyExclusive("ask", "no-ask")
	cmd.AddCommand(settings, set)
	return cmd
}

// printAgentSettings shows a setting as a table: an area per row, a kind
// per column, "ask" after a level its changes ask first in.
func printAgentSettings(g *globals, a apitypes.AgentSettings) error {
	return g.io.Print(a, func() error {
		t := g.io.Table("area", "production", "other", "previews")
		var row []string
		for _, s := range a.Settings {
			if len(row) == 0 {
				row = []string{s.Area}
			}
			cell := s.Level
			if s.Ask {
				cell += ", ask"
			}
			row = append(row, cell)
			if len(row) == 4 {
				t.Row(row...)
				row = nil
			}
		}
		return t.Flush()
	})
}
