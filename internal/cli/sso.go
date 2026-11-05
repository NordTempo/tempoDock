package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Single sign-on with OpenID Connect (ADR 0058): the organization's
// identity providers and its sso-only switch. People sign in through them
// in the browser; the break-glass is `dock admin sso-only`.

// newOrgSSOCmd is `dock org sso`: the organization's identity providers.
func newOrgSSOCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "sso",
		Short: "Single sign-on: the organization's OpenID Connect providers (admin)",
		Long: `People whose email domain a provider allows sign in to Dock through it, from
the sign-in page. Any OpenID Connect provider works: Google, Microsoft Entra
ID, Okta, Keycloak, Authentik, or GitHub through an OIDC proxy like Dex.

Register Dock at the provider as a web application with the redirect URL
` + "`dock org sso list`" + ` shows, and the scopes openid, email and profile. A person
must be a member already or have an invite, unless the provider has a
just-in-time role (--jit-role). Managing providers needs an admin with access
to every project.`,
	})
	cmd.AddCommand(newOrgSSOListCmd(g), newOrgSSOAddCmd(g), newOrgSSOSetCmd(g), newOrgSSORemoveCmd(g))
	return cmd
}

func newOrgSSOListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the organization's single sign-on providers",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			ps, err := get[[]apitypes.SSOProvider](cmd.Context(), s, apiPrefix+"/org/sso-providers")
			if err != nil {
				return err
			}
			return g.io.Print(ps, func() error {
				if len(ps) == 0 {
					g.io.Detail("No single sign-on providers. Add one with `dock org sso add`.")
					return nil
				}
				t := g.io.Table("name", "issuer", "domains", "new members", "added")
				for _, p := range ps {
					t.Row(p.Name, p.Issuer, strings.Join(p.AllowedDomains, ", "), jitWord(p.JITRole), ago(p.CreatedAt)+" by "+p.CreatedBy)
				}
				if err := t.Flush(); err != nil {
					return err
				}
				_, err := fmt.Fprintf(g.io.Out, "\nRedirect URL to register at the provider: %s\n", ps[0].RedirectURL)
				return err
			})
		},
	}
}

// ssoFlags are the provider settings add and set take.
type ssoFlags struct {
	issuer, clientID, jitRole string
	domains                   []string
	secretStdin               bool
}

func (f *ssoFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.issuer, "issuer", "", "the provider's issuer URL, like https://accounts.google.com")
	cmd.Flags().StringVar(&f.clientID, "client-id", "", "client ID of the app registered for Dock at the provider")
	cmd.Flags().BoolVar(&f.secretStdin, "client-secret-stdin", false, "read its client secret from stdin")
	cmd.Flags().StringArrayVar(&f.domains, "domain", nil, "email domain it signs people in for; repeat for more")
	cmd.Flags().StringVar(&f.jitRole, "jit-role", "", `role for people with an allowed domain and no invite: read or deploy ("" for invite only)`)
}

// readClientSecret reads a client secret piped to stdin.
func readClientSecret(g *globals, cmdline string) (string, error) {
	b, err := io.ReadAll(io.LimitReader(g.io.In, maxSecretInput))
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(b))
	if secret == "" {
		return "", usageError("--client-secret-stdin read nothing", fmt.Sprintf("Pipe the secret in: `printf %%s \"$CLIENT_SECRET\" | %s`.", cmdline))
	}
	return secret, nil
}

func newOrgSSOAddCmd(g *globals) *cobra.Command {
	var f ssoFlags
	cmd := &cobra.Command{
		Use:   "add <name> --issuer <url> --client-id <id> --client-secret-stdin --domain <domain>",
		Short: "Add a single sign-on provider (admin)",
		Long: `Add an OpenID Connect provider for people with the given email domains. Dock
reads the issuer's discovery document now, so a wrong URL fails here. The
client secret is read from stdin and never shown again.

Without --jit-role, new members need an invite (` + "`dock org invite`" + `), which
signing in accepts. With --jit-role read or deploy, anyone the provider
vouches for with an allowed, verified email joins with that role.`,
		Example: `  printf %s "$CLIENT_SECRET" | dock org sso add google \
    --issuer https://accounts.google.com --client-id 123.apps.googleusercontent.com \
    --client-secret-stdin --domain example.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if f.issuer == "" || f.clientID == "" || len(f.domains) == 0 || !f.secretStdin {
				return usageError("--issuer, --client-id, --client-secret-stdin and --domain are required", "See `dock org sso add --help`.")
			}
			secret, err := readClientSecret(g, "dock org sso add "+args[0]+" ... --client-secret-stdin")
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			p, err := send[apitypes.SSOProvider](cmd.Context(), s, http.MethodPost, apiPrefix+"/org/sso-providers", apitypes.CreateSSOProviderRequest{
				Name: args[0], Issuer: f.issuer, ClientID: f.clientID, ClientSecret: secret, AllowedDomains: f.domains, JITRole: apitypes.Scope(f.jitRole),
			})
			if err != nil {
				return err
			}
			return printSSOProvider(g, p, "Added")
		},
	}
	f.register(cmd)
	return cmd
}

func newOrgSSOSetCmd(g *globals) *cobra.Command {
	var f ssoFlags
	cmd := &cobra.Command{
		Use:   "set <name> [flags]",
		Short: "Change a single sign-on provider (admin)",
		Long: `Change a provider's issuer, client ID, client secret, email domains or
just-in-time role; what you leave out stays. --domain replaces the list.
--jit-role "" makes new members need an invite again.`,
		Example: `  dock org sso set google --domain example.com --domain example.org
  printf %s "$NEW_SECRET" | dock org sso set google --client-secret-stdin
  dock org sso set google --jit-role ""`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var req apitypes.UpdateSSOProviderRequest
			fl := cmd.Flags()
			if fl.Changed("issuer") {
				req.Issuer = &f.issuer
			}
			if fl.Changed("client-id") {
				req.ClientID = &f.clientID
			}
			if fl.Changed("domain") {
				req.AllowedDomains = f.domains
			}
			if fl.Changed("jit-role") {
				role := apitypes.Scope(f.jitRole)
				req.JITRole = &role
			}
			if f.secretStdin {
				secret, err := readClientSecret(g, "dock org sso set "+args[0]+" --client-secret-stdin")
				if err != nil {
					return err
				}
				req.ClientSecret = &secret
			}
			if req.Issuer == nil && req.ClientID == nil && req.ClientSecret == nil && req.AllowedDomains == nil && req.JITRole == nil {
				return usageError("nothing to change", "Pass --issuer, --client-id, --client-secret-stdin, --domain or --jit-role.")
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			p, err := send[apitypes.SSOProvider](cmd.Context(), s, http.MethodPatch, apiPrefix+"/org/sso-providers/"+url.PathEscape(args[0]), req)
			if err != nil {
				return err
			}
			return printSSOProvider(g, p, "Saved")
		},
	}
	f.register(cmd)
	return cmd
}

func newOrgSSORemoveCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a single sign-on provider (admin)",
		Long: `Remove a provider and the identities linked through it. People keep their
accounts and memberships, and sign in with a password or another provider.
The last provider of an sso-only organization stays until that is off.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Remove single sign-on provider %s? People who sign in through it need a password or another provider.", args[0]), "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, apiPrefix+"/org/sso-providers/"+url.PathEscape(args[0])); err != nil {
				return err
			}
			return g.io.Print(map[string]string{"removed": args[0]}, func() error {
				g.io.Successf("Removed single sign-on provider %s", args[0])
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

// printSSOProvider reports a saved provider and the redirect URL to
// register at it.
func printSSOProvider(g *globals, p apitypes.SSOProvider, verb string) error {
	return g.io.Print(p, func() error {
		g.io.Successf("%s single sign-on provider %s for %s", verb, p.Name, strings.Join(p.AllowedDomains, ", "))
		g.io.Detail("Redirect URL to register at the provider: " + p.RedirectURL)
		g.io.Detail("New members: " + jitWord(p.JITRole))
		return nil
	})
}

// jitWord says who may join through a provider.
func jitWord(role apitypes.Scope) string {
	if role == "" {
		return "invite only"
	}
	return "join as " + string(role)
}

// newOrgSSOOnlyCmd is `dock org sso-only [on|off]`.
func newOrgSSOOnlyCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "sso-only [on|off]",
		Short: "Show or set whether members must sign in with single sign-on (admin)",
		Long: `Without an argument, show whether the organization requires single sign-on.
With on or off, change it (an admin with access to every project).

While it is on, a member acts in the organization only after signing in
through one of its providers; a password sign-in still works for their
other organizations. A ` + "`dock login`" + ` token counts when the browser sign-in
that approved it went through the organization's single sign-on. API
tokens are not affected.

Turning it on needs your own sign-in to have gone through one of its
providers, so you know it works. If the provider goes down, an operator
turns it off on the server with ` + "`dock admin sso-only <org> off`" + `.`,
		Example: `  dock org sso-only
  dock org sso-only on`,
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
					return g.io.Fields("organization", o.Slug, "single sign-on", requiredWord(o.SSOOnly))
				})
			}
			on, err := parseOnOff(args[0], "dock org sso-only on")
			if err != nil {
				return err
			}
			o, err := send[apitypes.Org](cmd.Context(), s, http.MethodPut, apiPrefix+"/org/sso-only", apitypes.SetSSOOnlyRequest{SSOOnly: on})
			if err != nil {
				return err
			}
			return g.io.Print(o, func() error {
				if o.SSOOnly {
					g.io.Successf("Members act in %s only after signing in with its single sign-on", o.Name)
				} else {
					g.io.Successf("%s no longer requires single sign-on", o.Name)
				}
				return nil
			})
		},
	}
}
