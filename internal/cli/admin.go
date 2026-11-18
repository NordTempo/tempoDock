//go:build !dockcli

package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/api"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
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
	cmd.AddCommand(newBootstrapCmd(), newCaddyConfigCmd(), newRegistryConfigCmd(), newRegistryStorageCmd(), newFirewallCmd(),
		newAdminBackupCmd(), newAdminRestoreCmd(), newAdminRecoverCmd(), newAdminFetchBackupCmd(), newAdminUpdateCmd(), newResetTwoFactorCmd(), newAdminPasswordSignInCmd())
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
			b, err := caddy.BootstrapConfig(caddyOptions(cfg, ""))
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
		Short: "Create the first user and token",
		Long: `Creates the first user (the platform's operator and the admin of its
first organization, default) and an operator API token if no users exist
yet.

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

// newResetTwoFactorCmd is `dock admin reset-2fa`, the break-glass for
// someone who lost their second factor and recovery codes (ADR 0057). It
// opens the state directly, like bootstrap: shell access to the server is
// the proof, so it needs no session and nobody else.
func newResetTwoFactorCmd() *cobra.Command {
	configPath := config.DefaultPath
	cmd := &cobra.Command{
		Use:     "reset-2fa <email>",
		Aliases: []string{"reset-two-factor"},
		Short:   "Turn off a user's two-factor sign-in, for one who lost their factor",
		Long: `Removes the user's second factors (passkeys and authenticator apps) and
recovery codes, and ends their UI sessions and dock login tokens. Their API
tokens stay. The audit log records it (user.2fa_reset).

Run it on the server, as root or the service user. The person then signs
in with their password alone and sets up a new factor on their account
page. While an organization they belong to requires two-factor sign-in,
they act in it again once they have. Safe to run while the daemon is
running.`,
		Example: "  sudo dock admin reset-2fa ada@example.com",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email := strings.TrimSpace(args[0])
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
			if err := c.ResetTwoFactor(cmd.Context(), core.System, email); err != nil {
				if core.CodeOf(err) == apitypes.CodeNotFound {
					return &output.Error{Exit: output.ExitNotFound, Err: err, Hint: "Check the email: `dock user list` shows the users."}
				}
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Reset the two-factor sign-in of %s: removed their second factors and recovery codes, and ended their sessions and dock login tokens.\nThey sign in with their password and set up a new factor on their account page.\n", email)
			return err
		},
	}
	cmd.Flags().StringVar(&configPath, "config", configPath, "path to the daemon config file")
	return cmd
}

// newAdminPasswordSignInCmd is `dock admin password-sign-in on`, the
// break-glass for a platform whose identity providers all fail while
// password sign-in is off (ADR 0065). Like reset-2fa it opens the state
// directly: shell access to the server is the proof.
func newAdminPasswordSignInCmd() *cobra.Command {
	configPath := config.DefaultPath
	var passwordFor string
	cmd := &cobra.Command{
		Use:   "password-sign-in on",
		Short: "Turn password sign-in back on, when no identity provider works",
		Long: `People sign in with email and password again; the identity providers stay.
An operator turns passwords off again with ` + "`dock sso password-sign-in off`" + `.
The audit log records it (platform.password_sign_in).

With --password-for, that account also gets a new generated password,
printed once: for an operator who only ever signed in through a provider
and has no password they know. Their sessions end.

Run it on the server, as root or the service user. Safe to run while the
daemon is running.`,
		Example: "  sudo dock admin password-sign-in on\n  sudo dock admin password-sign-in on --password-for ops@example.com",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "on" {
				return usageError("only on is done on the server", "Turn passwords off from the console or `dock sso password-sign-in off`, signed in through an identity provider.")
			}
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
			pw, err := c.ResetPasswordSignIn(cmd.Context(), core.System, passwordFor)
			if core.CodeOf(err) == apitypes.CodeNotFound {
				return &output.Error{Exit: output.ExitNotFound, Err: err, Hint: "Check the email: the console's Users page and `dock user list` show it."}
			}
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			_, err = fmt.Fprintln(out, "Password sign-in is on: people sign in with email and password again.")
			if pw != "" && err == nil {
				_, err = fmt.Fprintf(out, "New password of %s (shown once): %s\n", passwordFor, pw)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&configPath, "config", configPath, "path to the daemon config file")
	cmd.Flags().StringVar(&passwordFor, "password-for", "", "also give this account a new generated password")
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
