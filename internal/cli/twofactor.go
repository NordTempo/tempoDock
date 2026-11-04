package cli

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Two-factor sign-in (ADR 0048): your own factors, and the organization's
// and the platform's requirements. Factors are set up in the browser, on
// the account page; the break-glass reset is `dock admin reset-2fa`.

// newMeTwoFactorCmd is `dock me two-factor`: the caller's second factors
// and how many recovery codes are left.
func newMeTwoFactorCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "two-factor",
		Aliases: []string{"2fa"},
		Short:   "Your second factors and unused recovery codes",
		Long: `List your second factors (authenticator apps) with when each was added and
last used, and how many unused recovery codes you have. Set them up, remove
them and create new recovery codes on your account page in the browser.

Needs a token from a browser ` + "`dock login`" + `, which acts for you; API tokens
have no second factors. After adding your first factor, run ` + "`dock login`" + `
again: a token counts as two-factor only when the sign-in that approved it
used one (` + "`dock whoami`" + ` says).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			tf, err := get[apitypes.TwoFactor](cmd.Context(), s, apiPrefix+"/me/two-factor")
			if err != nil {
				return err
			}
			account := strings.TrimRight(s.server.Server, "/") + "/account"
			return g.io.Print(tf, func() error {
				if len(tf.Factors) == 0 {
					g.io.Warnf("Two-factor sign-in is off: you have no second factor")
					g.io.Detail("Add an authenticator app on your account page: " + account)
					return nil
				}
				t := g.io.Table("kind", "name", "added", "last used")
				for _, f := range tf.Factors {
					t.Row(factorKind(f.Kind), f.Name, ago(f.CreatedAt), agoPtr(f.LastUsedAt))
				}
				if err := t.Flush(); err != nil {
					return err
				}
				if _, err := fmt.Fprintf(g.io.Out, "\n%d unused recovery codes\n", tf.RecoveryCodes); err != nil {
					return err
				}
				if tf.RecoveryCodes == 0 {
					g.io.Warnf("No recovery codes left: create new ones on your account page (%s)", account)
				}
				return nil
			})
		},
	}
}

// factorKind names a factor kind as the account page does.
func factorKind(kind string) string {
	switch kind {
	case "totp":
		return "authenticator app"
	case "passkey":
		return "passkey"
	}
	return kind
}

// newOrgTwoFactorCmd is `dock org two-factor [on|off]`: show or change
// whether members must sign in with a second factor.
func newOrgTwoFactorCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "two-factor [on|off]",
		Aliases: []string{"2fa"},
		Short:   "Show or set whether members must sign in with a second factor (admin)",
		Long: `Without an argument, show whether the organization requires two-factor
sign-in. With on or off, change it (an admin with access to every project).

While it is on, a member whose sign-in used no second factor stays signed
in but can't act in the organization until they add one on their account
page. A ` + "`dock login`" + ` token counts when the browser sign-in that approved it
used a second factor. API tokens are not affected.

Turning it on needs your own sign-in to have used a second factor, so you
don't lock yourself out.`,
		Example: `  dock org two-factor
  dock org two-factor on`,
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				o, err := get[apitypes.Org](cmd.Context(), s, apiPrefix+"/org")
				if err != nil {
					return err
				}
				return g.io.Print(o, func() error {
					return g.io.Fields("organization", o.Slug, "two-factor", requiredWord(o.RequireTwoFactor))
				})
			}
			on, err := parseOnOff(args[0], "dock org two-factor on")
			if err != nil {
				return err
			}
			o, err := send[apitypes.Org](cmd.Context(), s, http.MethodPut, apiPrefix+"/org/two-factor", apitypes.RequireTwoFactorRequest{Require: on})
			if err != nil {
				return err
			}
			return g.io.Print(o, func() error {
				if o.RequireTwoFactor {
					g.io.Successf("%s requires two-factor sign-in of its members", o.Name)
				} else {
					g.io.Successf("%s no longer requires two-factor sign-in", o.Name)
				}
				return nil
			})
		},
	}
}

// newSecurityCmd is `dock security`: the platform's sign-in policy, for
// operators.
func newSecurityCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "security",
		Short: "The platform's sign-in policy (operator)",
		Long: `Show the platform's sign-in policy: whether operators must sign in with a
second factor to run the platform.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			p, err := get[apitypes.PlatformSecurity](cmd.Context(), s, apiPrefix+"/platform/security")
			if err != nil {
				return err
			}
			return g.io.Print(p, func() error {
				return g.io.Fields("operators two-factor", requiredWord(p.OperatorsTwoFactor))
			})
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:     "operators-two-factor on|off",
		Aliases: []string{"operators-2fa"},
		Short:   "Require operators to sign in with a second factor (operator)",
		Long: `While it is on, an operator whose sign-in used no second factor keeps their
roles in organizations but can't run the platform (servers, backups, user
accounts, settings) until they add one on their account page. A
` + "`dock login`" + ` token counts when the browser sign-in that approved it used a
second factor. Operator API tokens are not affected.

Turning it on needs your own sign-in to have used a second factor.`,
		Example: "  dock security operators-two-factor on",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			on, err := parseOnOff(args[0], "dock security operators-two-factor on")
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			p, err := send[apitypes.PlatformSecurity](cmd.Context(), s, http.MethodPut, apiPrefix+"/platform/security", apitypes.PlatformSecurity{OperatorsTwoFactor: on})
			if err != nil {
				return err
			}
			return g.io.Print(p, func() error {
				if p.OperatorsTwoFactor {
					g.io.Successf("Operators run the platform only after signing in with a second factor")
				} else {
					g.io.Successf("Operators no longer need a second factor to run the platform")
				}
				return nil
			})
		},
	})
	return cmd
}

// parseOnOff parses an on|off argument; example is the command to show on a
// usage error.
func parseOnOff(arg, example string) (bool, error) {
	switch strings.ToLower(arg) {
	case "on":
		return true, nil
	case "off":
		return false, nil
	}
	return false, usageError(strconv.Quote(arg)+" is not on or off", "For example: `"+example+"`.")
}

// requiredWord shows a two-factor requirement in tables and fields.
func requiredWord(required bool) string {
	if required {
		return "required"
	}
	return "not required"
}
