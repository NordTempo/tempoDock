package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/api"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/node/caddy"
)

// newAdminCmd groups server-side commands that run on the server itself
// with direct access to the data directory.
func newAdminCmd() *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "admin",
		Short: "Server-side administration (run on the server)",
	})
	cmd.AddCommand(newBootstrapCmd(), newCaddyConfigCmd(), newFirewallCmd())
	return cmd
}

// newCaddyConfigCmd prints Caddy's initial config: admin on the Unix socket
// and the 404 page, before the daemon has loaded any routes. The install
// script writes it to the file Caddy starts from.
func newCaddyConfigCmd() *cobra.Command {
	configPath := config.DefaultPath
	cmd := &cobra.Command{
		Use:   "caddy-config",
		Short: "Print the initial Caddy config for this server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			b, err := caddy.BootstrapConfig(caddyOptions(cfg))
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			return err
		},
	}
	cmd.Flags().StringVar(&configPath, "config", configPath, "path to the daemon config file")
	return cmd
}

func newBootstrapCmd() *cobra.Command {
	configPath := config.DefaultPath
	email := "admin@localhost"
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Create the first admin user and token",
		Long: `Creates the first admin user and an admin API token if no users exist yet.

The token is printed to stdout (and only the token, for scripts); the
user's generated UI password goes to stderr. Both are shown once. When an
admin already exists nothing is created and stdout stays empty. Safe to run
while the daemon is running.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			st, box, err := openState(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			c, err := core.New(cmd.Context(), core.Options{Config: cfg, Store: st, Secrets: box})
			if err != nil {
				return err
			}
			b, ok, err := c.Bootstrap(cmd.Context(), email)
			if err != nil {
				return err
			}
			if !ok {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "an admin already exists; nothing to do")
				return nil
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Created admin user %s with password %s (shown once)\n", b.Email, b.Password)
			_, err = fmt.Fprintln(cmd.OutOrStdout(), b.Token)
			return err
		},
	}
	cmd.Flags().StringVar(&configPath, "config", configPath, "path to the daemon config file")
	cmd.Flags().StringVar(&email, "email", email, "email of the admin user (the UI login)")
	return cmd
}

// newOpenAPICmd writes the API's OpenAPI document, for the UI client
// generator (`go generate ./web`).
func newOpenAPICmd() *cobra.Command {
	var outPath string
	cmd := &cobra.Command{
		Use:    "openapi",
		Short:  "Print the API's OpenAPI 3.1 document",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			spec, err := api.Spec()
			if err != nil {
				return err
			}
			spec = append(spec, '\n')
			if outPath == "" {
				_, err = cmd.OutOrStdout().Write(spec)
				return err
			}
			return os.WriteFile(outPath, spec, 0o644)
		},
	}
	cmd.Flags().StringVarP(&outPath, "output", "o", "", "write to this file instead of stdout")
	return cmd
}
