package cli

import (
	"fmt"
	"maps"
	"regexp"
	"slices"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/cli/apiclient"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// contextView is the JSON shape of a context. Tokens are never printed.
type contextView struct {
	Name     string `json:"name"`
	Server   string `json:"server"`
	Current  bool   `json:"current"`
	HasToken bool   `json:"hasToken"`
}

func newContextCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "context",
		Short: "Manage the servers the CLI talks to",
		Long: `A context is a named server URL plus API token, like a kubectl context.
Commands use the current context unless DOCK_CONTEXT names another, the
directory is linked to another, or DOCK_URL and DOCK_TOKEN are set.`,
	})
	cmd.AddCommand(
		newContextListCmd(g),
		newContextAddCmd(g),
		newContextUseCmd(g),
		newContextRemoveCmd(g),
		newContextCurrentCmd(g),
	)
	return cmd
}

func newContextListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List contexts",
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			views := []contextView{}
			for _, name := range slices.Sorted(maps.Keys(cfg.Contexts)) {
				ctx := cfg.Contexts[name]
				views = append(views, contextView{name, ctx.Server, name == cfg.CurrentContext, ctx.Token != ""})
			}
			return g.io.Print(views, func() error {
				if len(views) == 0 {
					_, err := fmt.Fprintln(g.io.Out, "No contexts. Log in with `dock login <server-url>`.")
					return err
				}
				t := g.io.Table("", "name", "server", "token")
				for _, v := range views {
					current, token := "", "none"
					if v.Current {
						current = "*"
					}
					if v.HasToken {
						token = "set"
					}
					t.Row(current, v.Name, v.Server, token)
				}
				return t.Flush()
			})
		},
	}
}

var contextName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func newContextAddCmd(g *globals) *cobra.Command {
	var server, token string
	var use bool
	cmd := &cobra.Command{
		Use:   "add <name> --server <url> --token <token>",
		Short: "Add a context, or replace one with the same name",
		Long: `Add a context. The first context becomes the current one; pass --use to
switch to a later one. Missing --server or --token are prompted for when
stdin is a terminal.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			if !contextName.MatchString(name) {
				return &output.Error{
					Exit:    output.ExitUsage,
					Message: fmt.Sprintf("invalid context name %q", name),
					Hint:    "Use letters, digits, '.', '_' and '-', e.g. prod or homelab.",
				}
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if server == "" {
				if server, err = g.io.Prompt("Server URL", "server"); err != nil {
					return err
				}
			}
			u, err := apiclient.ParseServerURL(server)
			if err != nil {
				return err
			}
			if token == "" {
				if token, err = g.io.PromptSecret("API token", "token"); err != nil {
					return err
				}
			}

			cfg.Contexts[name] = clientconfig.Context{Server: u.String(), Token: token}
			if use || cfg.CurrentContext == "" {
				cfg.CurrentContext = name
			}
			if err := cfg.save(); err != nil {
				return err
			}
			v := contextView{name, u.String(), cfg.CurrentContext == name, true}
			return g.io.Print(v, func() error {
				g.io.Successf("Added context %s (%s)", name, v.Server)
				if v.Current {
					g.io.Successf("Now using %s", name)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "server URL, e.g. https://dock.example.com")
	cmd.Flags().StringVar(&token, "token", "", "API token")
	cmd.Flags().BoolVar(&use, "use", false, "make it the current context")
	return cmd
}

func newContextUseCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Switch the current context",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			ctx, ok := cfg.Contexts[name]
			if !ok {
				return contextNotFound(name)
			}
			cfg.CurrentContext = name
			if err := cfg.save(); err != nil {
				return err
			}
			return g.io.Print(contextView{name, ctx.Server, true, ctx.Token != ""}, func() error {
				g.io.Successf("Now using %s (%s)", name, ctx.Server)
				return nil
			})
		},
	}
}

func newContextRemoveCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Remove a context and its token from this machine",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			ctx := cfg.Contexts[name]
			wasCurrent := cfg.CurrentContext == name
			if !cfg.RemoveContext(name) {
				return contextNotFound(name)
			}
			if err := cfg.save(); err != nil {
				return err
			}
			return g.io.Print(contextView{name, ctx.Server, false, false}, func() error {
				g.io.Successf("Removed context %s", name)
				if wasCurrent {
					g.io.Warnf("No current context. Pick one with `dock context use <name>`.")
				}
				return nil
			})
		},
	}
}

func newContextCurrentCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Show which server commands in this directory talk to",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			r, err := cfg.resolve()
			if err != nil {
				return err
			}
			v := struct {
				Name     string              `json:"name"`
				Server   string              `json:"server"`
				HasToken bool                `json:"hasToken"`
				Source   clientconfig.Source `json:"source"`
			}{r.Name, r.Server, r.Token != "", r.Source}
			return g.io.Print(v, func() error {
				name := r.Name
				if name == "" {
					name = "(" + clientconfig.EnvURL + ")"
				}
				t := g.io.Table("name", "server", "token", "from")
				token := "none"
				if v.HasToken {
					token = "set"
				}
				t.Row(name, r.Server, token, string(r.Source))
				return t.Flush()
			})
		},
	}
}

func contextNotFound(name string) error {
	return &output.Error{
		Exit:    output.ExitNotFound,
		Message: fmt.Sprintf("context %q not found", name),
		Hint:    "Run `dock context list` to see contexts.",
	}
}
