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

// Single sign-on (ADR 0058, 0065): the platform's identity providers and
// sign-in switches, managed by operators. People sign in through the
// providers in the browser; the break-glass is `dock admin
// password-sign-in on`.

// newSSOCmd is `dock sso`: the platform's identity providers.
func newSSOCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "sso",
		Short: "Single sign-on: the platform's identity providers (operator)",
		Long: `The sign-in page shows a "Continue with" button for each provider: Google,
GitHub, Microsoft Entra ID, GitLab, Okta, Auth0, Keycloak, Authentik, or any
other OpenID Connect provider by its issuer.

Register Dock at the provider as a web application with the redirect URL
` + "`dock sso list`" + ` shows. A provider signs in people with a Dock account (by their
verified email the first time) and creates accounts for people with an open
invite. Unless sign-up is invite only (` + "`dock sso invite-only`" + `), it creates an
account for anyone else it signs in, in no organization. Organizations
still decide who is a member.`,
	})
	cmd.AddCommand(newSSOListCmd(g), newSSOAddCmd(g), newSSOSetCmd(g), newSSORemoveCmd(g),
		newSignInSwitchCmd(g, "password-sign-in"), newSignInSwitchCmd(g, "invite-only"))
	return cmd
}

func newSSOListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the identity providers and how people sign in",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			ps, err := get[apitypes.SSOProviders](cmd.Context(), s, apiPrefix+"/platform/sso-providers")
			if err != nil {
				return err
			}
			set, err := get[apitypes.SignInSettings](cmd.Context(), s, apiPrefix+"/platform/sign-in")
			if err != nil {
				return err
			}
			v := struct {
				apitypes.SSOProviders
				apitypes.SignInSettings
			}{ps, set}
			return g.io.Print(v, func() error {
				if len(ps.Providers) == 0 {
					g.io.Detail("No identity providers. Add one with `dock sso add`.")
				} else {
					t := g.io.Table("name", "kind", "button", "client id", "people", "added")
					for _, p := range ps.Providers {
						t.Row(p.Name, kindWord(p), "Continue with "+p.Label, p.ClientID, fmt.Sprint(p.Linked), ago(p.CreatedAt)+" by "+p.CreatedBy)
					}
					if err := t.Flush(); err != nil {
						return err
					}
					_, _ = fmt.Fprintln(g.io.Out)
				}
				return g.io.Fields("redirect URL", ps.RedirectURL, "password sign-in", onWord(set.PasswordSignIn), "invite only", onWord(set.InviteOnly))
			})
		},
	}
}

// kindWord names a provider's kind, with its issuer for a custom one.
func kindWord(p apitypes.SSOProvider) string {
	if p.Kind == apitypes.SSOOIDC {
		return p.Issuer
	}
	return apitypes.SSOKindNames[p.Kind]
}

// onWord is on or off.
func onWord(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// ssoFlags are the provider settings add and set take.
type ssoFlags struct {
	kind, issuer, label, clientID string
	tenant, domain, realm         string
	secretStdin                   bool
}

func (f *ssoFlags) register(cmd *cobra.Command, add bool) {
	if add {
		cmd.Flags().StringVar(&f.kind, "kind", "", "google, github, microsoft, gitlab, okta, auth0, keycloak, authentik or oidc (default: the name, when it is one of them)")
	}
	cmd.Flags().StringVar(&f.issuer, "issuer", "", "oidc only: the provider's issuer URL")
	cmd.Flags().StringVar(&f.tenant, "tenant", "", "microsoft only: the tenant ID or a domain of it")
	cmd.Flags().StringVar(&f.domain, "domain", "", "okta, auth0: the domain; keycloak, authentik, gitlab: the base URL (gitlab.com by default)")
	cmd.Flags().StringVar(&f.realm, "realm", "", "keycloak: the realm; authentik: the application's slug")
	cmd.Flags().StringVar(&f.label, "label", "", `the button's "Continue with <label>" (default: the kind's name)`)
	cmd.Flags().StringVar(&f.clientID, "client-id", "", "client ID of the app registered for Dock at the provider")
	cmd.Flags().BoolVar(&f.secretStdin, "client-secret-stdin", false, "read its client secret from stdin")
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

func newSSOAddCmd(g *globals) *cobra.Command {
	var f ssoFlags
	cmd := &cobra.Command{
		Use:   "add <name> --client-id <id> --client-secret-stdin [--kind <kind> --tenant|--domain|--realm|--issuer ...]",
		Short: "Add an identity provider (operator)",
		Long: `Add an identity provider. Named after a kind (google, microsoft, okta, ...),
it is that kind; anything else needs --kind. Each kind takes only what it
needs to find the provider:

  google, github   nothing
  microsoft        --tenant: the tenant ID or a domain of it (not common or
                   organizations: Dock signs in one tenant's people)
  gitlab           --domain for a self-hosted GitLab (gitlab.com by default)
  okta, auth0      --domain: acme.okta.com, acme.eu.auth0.com
  keycloak         --domain (its base URL) and --realm
  authentik        --domain (its base URL) and --realm (the application's slug)
  oidc             --issuer and --label: any other OpenID Connect provider

Dock reads an OpenID Connect provider's discovery document now, so a wrong
URL fails here. The client secret is read from stdin and never shown again.
Register Dock at the provider with the redirect URL ` + "`dock sso list`" + ` shows.`,
		Example: `  printf %s "$CLIENT_SECRET" | dock sso add google --client-id 123.apps.googleusercontent.com --client-secret-stdin
  printf %s "$CLIENT_SECRET" | dock sso add github --client-id Ov23li... --client-secret-stdin
  printf %s "$CLIENT_SECRET" | dock sso add microsoft --tenant contoso.onmicrosoft.com --client-id 0f1e... --client-secret-stdin
  printf %s "$CLIENT_SECRET" | dock sso add staff --kind keycloak --domain id.example.com --realm staff \
    --label "Example ID" --client-id dock --client-secret-stdin
  printf %s "$CLIENT_SECRET" | dock sso add corp --kind oidc --issuer https://id.example.com --label "Example ID" \
    --client-id dock --client-secret-stdin`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := apitypes.SSOKind(f.kind)
			if _, named := apitypes.SSOKindNames[apitypes.SSOKind(args[0])]; kind == "" && named && args[0] != string(apitypes.SSOOIDC) {
				kind = apitypes.SSOKind(args[0])
			}
			if kind == "" {
				return usageError("--kind is required for a provider not named after one", "Pass --kind google|github|microsoft|gitlab|okta|auth0|keycloak|authentik, or --kind oidc --issuer <url> --label <label>.")
			}
			if f.clientID == "" || !f.secretStdin {
				return usageError("--client-id and --client-secret-stdin are required", "See `dock sso add --help`.")
			}
			secret, err := readClientSecret(g, "dock sso add "+args[0]+" ... --client-secret-stdin")
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			p, err := send[apitypes.SSOProvider](cmd.Context(), s, http.MethodPost, apiPrefix+"/platform/sso-providers", apitypes.CreateSSOProviderRequest{
				Name: args[0], Kind: kind, Label: f.label, Issuer: f.issuer, ClientID: f.clientID, ClientSecret: secret,
				SSOWhere: apitypes.SSOWhere{Tenant: f.tenant, Domain: f.domain, Realm: f.realm},
			})
			if err != nil {
				return err
			}
			return printSSOProvider(cmd, g, s, p, "Added")
		},
	}
	f.register(cmd, true)
	return cmd
}

func newSSOSetCmd(g *globals) *cobra.Command {
	var f ssoFlags
	cmd := &cobra.Command{
		Use:   "set <name> [flags]",
		Short: "Change an identity provider (operator)",
		Long:  `Change a provider's label, where it is (--issuer, --tenant, --domain, --realm, as its kind takes), client ID or client secret; what you leave out stays.`,
		Example: `  dock sso set corp --label "Example ID"
  dock sso set staff --realm employees
  printf %s "$NEW_SECRET" | dock sso set google --client-secret-stdin`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var req apitypes.UpdateSSOProviderRequest
			fl := cmd.Flags()
			if fl.Changed("label") {
				req.Label = &f.label
			}
			if fl.Changed("issuer") {
				req.Issuer = &f.issuer
			}
			if fl.Changed("tenant") {
				req.Tenant = &f.tenant
			}
			if fl.Changed("domain") {
				req.Domain = &f.domain
			}
			if fl.Changed("realm") {
				req.Realm = &f.realm
			}
			if fl.Changed("client-id") {
				req.ClientID = &f.clientID
			}
			if f.secretStdin {
				secret, err := readClientSecret(g, "dock sso set "+args[0]+" --client-secret-stdin")
				if err != nil {
					return err
				}
				req.ClientSecret = &secret
			}
			if req.Label == nil && req.Issuer == nil && req.Tenant == nil && req.Domain == nil && req.Realm == nil && req.ClientID == nil && req.ClientSecret == nil {
				return usageError("nothing to change", "Pass --label, --issuer, --tenant, --domain, --realm, --client-id or --client-secret-stdin.")
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			p, err := send[apitypes.SSOProvider](cmd.Context(), s, http.MethodPatch, apiPrefix+"/platform/sso-providers/"+url.PathEscape(args[0]), req)
			if err != nil {
				return err
			}
			return printSSOProvider(cmd, g, s, p, "Saved")
		},
	}
	f.register(cmd, false)
	return cmd
}

func newSSORemoveCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an identity provider (operator)",
		Long: `Remove a provider, the identities linked through it and the sessions signed in
through it. People keep their accounts and memberships, and sign in with a
password, a passkey or another provider. The last provider stays while
password sign-in is off.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Remove identity provider %s? People signed in through it are signed out, and sign in another way.", args[0]), "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, apiPrefix+"/platform/sso-providers/"+url.PathEscape(args[0])); err != nil {
				return err
			}
			return g.io.Print(map[string]string{"removed": args[0]}, func() error {
				g.io.Successf("Removed identity provider %s", args[0])
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

// printSSOProvider reports a saved provider and the redirect URL to
// register at it.
func printSSOProvider(cmd *cobra.Command, g *globals, s *session, p apitypes.SSOProvider, verb string) error {
	return g.io.Print(p, func() error {
		g.io.Successf("%s identity provider %s: the sign-in page shows Continue with %s", verb, p.Name, p.Label)
		if ps, err := get[apitypes.SSOProviders](cmd.Context(), s, apiPrefix+"/platform/sso-providers"); err == nil {
			g.io.Detail("Redirect URL to register at the provider: " + ps.RedirectURL)
		}
		return nil
	})
}

// newSignInSwitchCmd is `dock sso password-sign-in|invite-only [on|off]`.
func newSignInSwitchCmd(g *globals, name string) *cobra.Command {
	short, long := "Show or set whether the sign-in page takes passwords (operator)", `Without an argument, show whether people sign in with email and password.
With on or off, change it.

Turning it off needs an identity provider and your own sign-in to have gone
through one, so you know it works. People then sign in through a provider or
a passkey; sessions already signed in stay until they end. If every provider
breaks, turn passwords back on from the server: `+"`sudo dock admin password-sign-in on`"+`.`
	if name == "invite-only" {
		short, long = "Show or set whether only invited people get an account (operator)", `Without an argument, show whether sign-up is invite only. With on or off,
change it.

On (the default): an identity provider creates an account only for an email
with an open invite, and accepts the invites. Anyone else it signs in is told
to ask for an invite. Off: it creates an account for anyone it signs in, in
no organization until an admin invites or adds them.`
	}
	return &cobra.Command{
		Use:     name + " [on|off]",
		Short:   short,
		Long:    long,
		Example: "  dock sso " + name + "\n  dock sso " + name + " off",
		Args:    cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			set, err := get[apitypes.SignInSettings](cmd.Context(), s, apiPrefix+"/platform/sign-in")
			if len(args) == 1 && err == nil {
				on, perr := parseOnOff(args[0], "dock sso "+name+" on")
				if perr != nil {
					return perr
				}
				req := apitypes.SignInSettingsRequest{PasswordSignIn: &on}
				if name == "invite-only" {
					req = apitypes.SignInSettingsRequest{InviteOnly: &on}
				}
				set, err = send[apitypes.SignInSettings](cmd.Context(), s, http.MethodPatch, apiPrefix+"/platform/sign-in", req)
			}
			if err != nil {
				return err
			}
			return g.io.Print(set, func() error {
				return g.io.Fields("password sign-in", onWord(set.PasswordSignIn), "invite only", onWord(set.InviteOnly))
			})
		},
	}
}

// newOrgSSOMovedCmd is `dock org sso` and `dock org sso-only`, which say
// where single sign-on went (ADR 0065).
func newOrgSSOMovedCmd(name string) *cobra.Command {
	return &cobra.Command{
		Use:                name,
		Short:              "Moved: single sign-on is the platform's, see `dock sso`",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(*cobra.Command, []string) error {
			return usageError("single sign-on is the platform's now, not an organization's",
				"An operator manages identity providers with `dock sso` (list, add, set, remove) and turns passwords off with `dock sso password-sign-in off`.")
		},
	}
}
