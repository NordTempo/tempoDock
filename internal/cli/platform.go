package cli

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// The platform's own credentials and roles (ADR 0080): platform tokens
// and the platform roles of user accounts, for operators.

func newPlatformCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "platform",
		Short: "The platform's own tokens (operators)",
		Long: `Running Dock itself (servers, accounts, updates, backups, the registry,
its settings) takes a platform role, never an organization's, and a
platform credential: ` + "`dock login --platform`" + ` for a person, a
platform token for automation. A platform credential never acts in an
organization. Legacy tokens from before this are both an org's and the
platform's until an operator splits them.`,
	})
	token := group(&cobra.Command{Use: "token", Short: "Platform tokens: list, create, revoke, split legacy ones"})
	token.AddCommand(newPlatformTokenListCmd(g), newPlatformTokenCreateCmd(g), newPlatformTokenRevokeCmd(g), newPlatformTokenSplitCmd(g))
	cmd.AddCommand(token, newAccessDiffCmd(g))
	return cmd
}

// newAccessDiffCmd is `dock platform access-diff` (ADR 0080, phase 3).
func newAccessDiffCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "access-diff",
		Short: "Where the new permission model disagreed with today's checks (platform.view)",
		Long: `The permission model of ADR 0080 watches before it decides: beside every
check it computes its own answer and counts where it differs, since the
daemon started. No output means it agreed with every check. A data policy
rule that gives less than a wider one shows as a lowering rule: the
migration to the model stops on those until the rule is removed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			list, err := get[[]apitypes.AccessDisagreement](cmd.Context(), s, apiPrefix+"/console/access-diff")
			if err != nil {
				return err
			}
			return g.io.Print(list, func() error {
				if len(list) == 0 {
					g.io.Successf("The permission model agreed with every check since the daemon started")
					return nil
				}
				t := g.io.Table("count", "check")
				for _, d := range list {
					t.Row(itoa(d.Count), d.Check)
				}
				return t.Flush()
			})
		},
	}
}

// platformTokenKind says what a token in the platform's list is.
func platformTokenKind(t apitypes.Token) string {
	switch {
	case t.Platform == "login":
		return "platform login of " + t.User
	case t.Platform != "":
		return "platform token, " + t.Platform
	case t.Origin == "cli_login":
		return "legacy: " + t.User + "'s dock login, also the platform's"
	}
	return "legacy: org " + t.Org + "'s token, also the platform's (split it)"
}

func newPlatformTokenListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List platform tokens, platform logins and legacy tokens",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			list, err := get[[]apitypes.Token](cmd.Context(), s, apiPrefix+"/console/tokens")
			if err != nil {
				return err
			}
			return g.io.Print(list, func() error {
				if len(list) == 0 {
					g.io.Successf("No platform tokens")
					return nil
				}
				t := g.io.Table("id", "name", "what", "status", "last used")
				for _, tok := range list {
					t.Row(itoa(tok.ID), tok.Name, platformTokenKind(tok), tokenStatus(tok), agoPtr(tok.LastUsedAt))
				}
				return t.Flush()
			})
		},
	}
}

func newPlatformTokenCreateCmd(g *globals) *cobra.Command {
	var role, expires string
	cmd := &cobra.Command{
		Use:   "create <name> --role operator|viewer",
		Short: "Create a platform token (accounts.manage, a person)",
		Long: `Create a token that runs the platform with a platform role and never acts
in an organization. Its secret starts with dock_p_ and is shown once. Keep
it beside an org token with DOCK_PLATFORM_TOKEN, or in a context with
` + "`dock login <url> --token-stdin`" + ` (it is kept as the platform login).`,
		Example: "  dock platform token create backup-check --role viewer --expires 90d",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			days, err := parseExpiry(expires)
			if err != nil {
				return err
			}
			if role == "" {
				return usageError("pick a platform role", "Pass --role operator or --role viewer.")
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			req := apitypes.CreatePlatformTokenRequest{Name: args[0], Role: apitypes.PlatformRole(role), ExpiresInDays: days}
			ct, err := send[apitypes.CreatedToken](cmd.Context(), s, http.MethodPost, apiPrefix+"/console/tokens", req)
			if err != nil {
				return err
			}
			return g.io.Print(ct, func() error {
				g.io.Successf("Created platform token %s (id %d, %s, %s)", ct.Token.Name, ct.Token.ID, ct.Token.Platform, tokenStatus(ct.Token))
				_, _ = fmt.Fprintln(g.io.Out, ct.Secret)
				g.io.Warnf("Store the token now; it is never shown again.")
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "operator or viewer")
	cmd.Flags().StringVar(&expires, "expires", "", "lifetime: days like 90d, or a duration like 720h (default: never)")
	return cmd
}

func newPlatformTokenRevokeCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke a platform token, platform login or legacy token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := tokenIDArg(args[0])
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, apiPrefix+"/console/tokens/"+itoa(id)); err != nil {
				return err
			}
			return g.io.Print(map[string]any{"id": id, "revoked": true}, func() error {
				g.io.Successf("Revoked platform token %d", id)
				return nil
			})
		},
	}
}

func newPlatformTokenSplitCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "split <id>",
		Short: "Split a legacy token into the org's token and a new platform token",
		Long: `A legacy organization token that also runs the platform stays the
organization's, with its scope and limits, and stops running the platform;
a new platform token with the operator role takes that over. Its secret is
shown once. A legacy ` + "`dock login`" + ` just stops running the platform:
its person runs ` + "`dock login --platform`" + `.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := tokenIDArg(args[0])
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			res, err := send[apitypes.SplitTokenResult](cmd.Context(), s, http.MethodPost, apiPrefix+"/console/tokens/"+itoa(id)+"/split", nil)
			if err != nil {
				return err
			}
			return g.io.Print(res, func() error {
				g.io.Successf("Token %s is the org's only now", res.Token.Name)
				if res.Platform.Secret == "" {
					g.io.Progressf("Its person runs `dock login --platform` for the platform.")
					return nil
				}
				g.io.Successf("Created platform token %s (id %d, operator)", res.Platform.Token.Name, res.Platform.Token.ID)
				_, _ = fmt.Fprintln(g.io.Out, res.Platform.Secret)
				g.io.Warnf("Store the token now; it is never shown again. Put it where the old token ran platform commands.")
				return nil
			})
		},
	}
}

// tokenIDArg reads a token's ID argument.
func tokenIDArg(arg string) (int64, error) {
	var id int64
	if _, err := fmt.Sscan(arg, &id); err != nil || id <= 0 {
		return 0, usageError(fmt.Sprintf("%q is not a token ID", arg), "`dock platform token list` shows them.")
	}
	return id, nil
}

// newUserPlatformRoleCmd is `dock user platform-role`.
func newUserPlatformRoleCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "platform-role <email|id> operator|viewer|none",
		Short: "Give a user a platform role, or take it away (accounts.manage, a person)",
		Long: `operator runs the platform: servers, accounts, updates, backups, the
registry and its settings. viewer reads the console and changes nothing.
none is neither. The platform keeps at least one operator who can sign in.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			role := strings.ToLower(args[1])
			switch role {
			case "operator", "viewer", "none":
			default:
				return usageError(fmt.Sprintf("%q is not a platform role", args[1]), "Use operator, viewer or none.")
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			accounts, err := get[[]apitypes.Account](cmd.Context(), s, apiPrefix+"/console/users")
			if err != nil {
				return err
			}
			var id int64
			for _, a := range accounts {
				if strings.EqualFold(a.Email, args[0]) || itoa(a.ID) == args[0] {
					id = a.ID
				}
			}
			if id == 0 {
				return &output.Error{Exit: output.ExitNotFound, Message: fmt.Sprintf("no user account %q", args[0]), Hint: "The console's Users page lists them."}
			}
			acc, err := send[apitypes.Account](cmd.Context(), s, http.MethodPut, apiPrefix+"/console/users/"+itoa(id)+"/platform-role", apitypes.SetPlatformRoleRequest{Role: role})
			if err != nil {
				return err
			}
			return g.io.Print(acc, func() error {
				if acc.PlatformRole == "" {
					g.io.Successf("%s has no platform role", acc.Email)
				} else {
					g.io.Successf("%s is a platform %s", acc.Email, acc.PlatformRole)
				}
				return nil
			})
		},
	}
}
