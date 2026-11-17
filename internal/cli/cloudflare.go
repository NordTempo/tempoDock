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

// Cloudflare connections (ADR 0036): API tokens with which Dock runs
// Cloudflare tunnels for tunnel servers and routes domains added from the
// tokens' zones through them. The org's own by default; --platform for
// the platform's (ADR 0062).

// cloudflareScopes is what a token needs, for help texts.
const cloudflareScopes = `Create an API token in the Cloudflare dashboard (My Profile › API Tokens, or
the account's API Tokens for one owned by the account) with:
  Account › Cloudflare Tunnel › Edit
  Zone › DNS › Edit    on the zones domains may be added from
  Zone › Zone › Read   on the same zones
  Zone › Dynamic URL Redirects › Edit   on zones with redirects (optional)
Dock only edits records it made (comment dock:<install ID>), never
overwrites one it didn't, and never points DNS at an IP: domains go
through tunnels.`

// newCloudflareCmd is `dock cloudflare`: the list, and add, set, refresh,
// remove, records, retry and redirects.
func newCloudflareCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cloudflare",
		Short: "Cloudflare connections: domains from Cloudflare zones (admin; --platform: operators)",
		Long: `List the organization's Cloudflare connections: API tokens with which Dock
routes domains added from the tokens' zones (` + "`dock domain add <host> --cloudflare`" + `)
through the servers' Cloudflare tunnels with a proxied CNAME. Only this
organization's domains use its zones. Tokens are never shown.

--platform lists the platform's connections instead, for operators: they
run the servers' tunnels, and a shared one's zones are open to every
organization's domains. It works with every subcommand: ` + "`dock cloudflare add --platform`" + `
adds one for the platform.

` + cloudflareScopes,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			list, err := get[[]apitypes.Cloudflare](cmd.Context(), s, withPlatform(cmd, apiPrefix+"/cloudflare"))
			if err != nil {
				return err
			}
			platform, _ := cmd.Flags().GetBool("platform")
			return g.io.Print(list, func() error {
				if len(list) == 0 {
					g.io.Detail("No Cloudflare connections. Add one with `dock cloudflare add <name> --token-stdin`" + platformFlag(platform) + ".")
					return nil
				}
				cols := []string{"name", "account", "zones", "status", "changed"}
				if platform {
					cols = append(cols, "shared", "tunnels for")
				}
				t := g.io.Table(cols...)
				for _, cf := range list {
					status := string(cf.Status)
					if cf.StatusReason != "" {
						status += ": " + cf.StatusReason
					}
					row := []string{cf.Name, cf.AccountName, orDash(strings.Join(cf.Zones, ", ")), status, ago(cf.UpdatedAt) + " by " + cf.UpdatedBy}
					if platform {
						row = append(row, yesNo(cf.Shared), orDash(strings.Join(cf.UsedBy, ", ")))
					}
					t.Row(row...)
				}
				return t.Flush()
			})
		},
	}
	cmd.PersistentFlags().Bool("platform", false, "the platform's connections instead of the organization's (operators)")
	cmd.AddCommand(newCloudflareAddCmd(g), newCloudflareSetCmd(g), newCloudflareRefreshCmd(g), newCloudflareRemoveCmd(g),
		newCloudflareZonesCmd(g), newCloudflareRecordsCmd(g), newCloudflareRetryCmd(g), newCloudflareRedirectsCmd(g))
	return cmd
}

// platformFlag is " --platform" for hints when the command ran with it.
func platformFlag(on bool) string {
	if on {
		return " --platform"
	}
	return ""
}

// readToken reads an API token piped to stdin.
func readToken(g *globals, cmdline string) (string, error) {
	b, err := io.ReadAll(io.LimitReader(g.io.In, maxSecretInput))
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", usageError("--token-stdin read nothing", fmt.Sprintf("Pipe the token in: `printf %%s \"$CLOUDFLARE_API_TOKEN\" | %s`.", cmdline))
	}
	return token, nil
}

// printCloudflare reports a saved connection.
func printCloudflare(g *globals, cmd *cobra.Command, cf apitypes.Cloudflare, verb string) error {
	platform, _ := cmd.Flags().GetBool("platform")
	return g.io.Print(cf, func() error {
		g.io.Successf("%s Cloudflare connection %s: account %s", verb, cf.Name, cf.AccountName)
		if len(cf.ZonesAdded) > 0 {
			g.io.Detail("Zones added: " + strings.Join(cf.ZonesAdded, ", ") + ".")
		}
		if len(cf.ZonesDropped) > 0 {
			g.io.Detail("Zones dropped: " + strings.Join(cf.ZonesDropped, ", ") + ". Their domains move to another connection that has the zone, where there is one.")
		}
		if len(cf.DomainsFailed) > 0 {
			g.io.Detail("No other connection may use the zone of " + strings.Join(cf.DomainsFailed, ", ") + ", so Dock no longer routes these domains: give a connection's token the zone, then `dock cloudflare refresh <name>`.")
		}
		switch {
		case len(cf.Zones) == 0:
			g.io.Detail("No domains can be added from it yet: give the token Zone › DNS › Edit and Zone › Read on some zones, then `dock cloudflare refresh " + cf.Name + platformFlag(platform) + "`.")
		case platform && !cf.Shared:
			g.io.Detail("Its zones are the platform's own: " + strings.Join(cf.Zones, ", ") + ". Share them with organizations with `dock cloudflare set " + cf.Name + " --platform --shared`.")
		case platform:
			g.io.Detail("Every organization's domains can be added from " + strings.Join(cf.Zones, ", ") + " (`dock domain add <host> --cloudflare`).")
		default:
			g.io.Detail("This organization's domains can be added from " + strings.Join(cf.Zones, ", ") + " (`dock domain add <host> --cloudflare`).")
		}
		return nil
	})
}

// newCloudflareAddCmd is `dock cloudflare add`.
func newCloudflareAddCmd(g *globals) *cobra.Command {
	var req apitypes.CreateCloudflareRequest
	var tokenStdin bool
	cmd := &cobra.Command{
		Use:   "add <name> --token-stdin [--account-id <id>] [--platform [--shared]]",
		Short: "Add a Cloudflare connection (admin; --platform: operators)",
		Long: `Add a Cloudflare connection for the organization (with --platform, for the
platform). Dock checks the token before saving it: it is active, reaches the
account and its tunnels, and lists zones; zones whose DNS records it can't
read are left out, and for the organization's own those Cloudflare doesn't
serve yet. A zone may be on several connections: the platform's and other
organizations' have no say in this one's. --account-id picks
the account when the token reaches several, and is needed for a token owned
by an account. --shared (with --platform) lets every organization's domains
use the connection's zones.

` + cloudflareScopes,
		Example: `  printf %s "$CLOUDFLARE_API_TOKEN" | dock cloudflare add home --token-stdin
  printf %s "$CLOUDFLARE_API_TOKEN" | dock cloudflare add edge --token-stdin --platform --shared`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !tokenStdin {
				return usageError("the API token is required", "Pipe it in with --token-stdin; it never goes in an argument.")
			}
			token, err := readToken(g, "dock cloudflare add "+args[0]+" --token-stdin")
			if err != nil {
				return err
			}
			req.Name, req.Token = args[0], token
			s, err := g.connect()
			if err != nil {
				return err
			}
			cf, err := send[apitypes.Cloudflare](cmd.Context(), s, http.MethodPost, withPlatform(cmd, apiPrefix+"/cloudflare"), req)
			if err != nil {
				return err
			}
			return printCloudflare(g, cmd, cf, "Added")
		},
	}
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the API token from stdin")
	cmd.Flags().StringVar(&req.AccountID, "account-id", "", "Cloudflare account ID")
	cmd.Flags().BoolVar(&req.Shared, "shared", false, "with --platform: every organization's domains may use its zones")
	return cmd
}

// newCloudflareSetCmd is `dock cloudflare set`.
func newCloudflareSetCmd(g *globals) *cobra.Command {
	var accountID string
	var tokenStdin, shared bool
	cmd := &cobra.Command{
		Use:   "set <name> [--token-stdin [--account-id <id>]] [--platform --shared[=false]]",
		Short: "Give a Cloudflare connection a new token, or share it (admin; --platform: operators)",
		Long: `Give a Cloudflare connection a new token, checked like a new one; its zones
are listed again, and the zones added and dropped are shown. A connection
whose token Cloudflare refused (revoked or expired) works again, and the
tunnels and domains waiting for it resume. Domains in a dropped zone move to
another connection the organization may use that has it, or fail saying so.
A token reaching fewer zones is how to take zones off a connection that runs
server tunnels, which can't be removed.

With --platform, --shared lets every organization's domains use the
connection's zones, and --shared=false stops that; domains already in them
keep working.`,
		Example: `  printf %s "$NEW_TOKEN" | dock cloudflare set home --token-stdin
  dock cloudflare set edge --platform --shared`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var req apitypes.UpdateCloudflareRequest
			if cmd.Flags().Changed("shared") {
				req.Shared = &shared
			}
			if !tokenStdin && req.Shared == nil {
				return usageError("the new API token is required", "Pipe it in with --token-stdin; to list new zones with the same token, use `dock cloudflare refresh`.")
			}
			if tokenStdin {
				token, err := readToken(g, "dock cloudflare set "+args[0]+" --token-stdin")
				if err != nil {
					return err
				}
				req.Token = token
			}
			if cmd.Flags().Changed("account-id") {
				req.AccountID = &accountID
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			cf, err := send[apitypes.Cloudflare](cmd.Context(), s, http.MethodPatch, withPlatform(cmd, apiPrefix+"/cloudflare/"+url.PathEscape(args[0])), req)
			if err != nil {
				return err
			}
			return printCloudflare(g, cmd, cf, "Saved")
		},
	}
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the API token from stdin")
	cmd.Flags().StringVar(&accountID, "account-id", "", "Cloudflare account ID")
	cmd.Flags().BoolVar(&shared, "shared", false, "with --platform: every organization's domains may use its zones")
	return cmd
}

// newCloudflareRefreshCmd is `dock cloudflare refresh`.
func newCloudflareRefreshCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "refresh <name>",
		Short: "List a Cloudflare connection's zones again (admin; --platform: operators)",
		Long: `Check the connection's token again and list its zones, after zones were added
to (or taken from) the token in Cloudflare, or a permission was fixed. Its
failed domains are tried again, and domains in zones it no longer reaches move
as with ` + "`dock cloudflare set`" + `.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			cf, err := send[apitypes.Cloudflare](cmd.Context(), s, http.MethodPost, withPlatform(cmd, apiPrefix+"/cloudflare/"+url.PathEscape(args[0])+"/refresh"), nil)
			if err != nil {
				return err
			}
			return printCloudflare(g, cmd, cf, "Refreshed")
		},
	}
}

// newCloudflareRemoveCmd is `dock cloudflare remove`.
func newCloudflareRemoveCmd(g *globals) *cobra.Command {
	var yes, force bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a Cloudflare connection no tunnel uses (admin; --platform: operators)",
		Long: `Remove a Cloudflare connection. Its domains move to another connection that
has their zone; Dock forgets the rest, whose records stay in Cloudflare.
Refused while a server's tunnel uses it: to take zones off such a connection,
give it a token that reaches fewer (` + "`dock cloudflare set <name> --platform --token-stdin`" + `).
Refused while it has redirects too, unless --force: Dock then deletes them in
Cloudflare first.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				q := fmt.Sprintf("Remove Cloudflare connection %s? Its domains move to another connection with their zone; the records of the rest stay in Cloudflare, no longer kept by Dock.", args[0])
				if force {
					q += " Its redirects are deleted and stop redirecting."
				}
				if err := g.io.Confirm(q, "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			path := apiPrefix + "/cloudflare/" + url.PathEscape(args[0])
			if force {
				path += "?force=true"
			}
			if err := del(cmd.Context(), s, withPlatform(cmd, path)); err != nil {
				return err
			}
			return g.io.Print(map[string]string{"removed": args[0]}, func() error {
				g.io.Successf("Removed Cloudflare connection %s", args[0])
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	cmd.Flags().BoolVar(&force, "force", false, "remove the connection's redirects with it")
	return cmd
}

// newCloudflareZonesCmd is `dock cloudflare zones`.
func newCloudflareZonesCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "zones",
		Short: "List the zones domains can be added from",
		Long: `List the zones the organization's domains may use: its own connections' and
those the platform shares. A hostname in one can be added with ` + "`dock domain add <host> --cloudflare`" + `,
and Dock routes it through the server's tunnel.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			list, err := get[[]apitypes.CloudflareZone](cmd.Context(), s, apiPrefix+"/cloudflare/zones")
			if err != nil {
				return err
			}
			return g.io.Print(list, func() error {
				if len(list) == 0 {
					g.io.Detail("No Cloudflare zones are connected for this organization; its admins add a connection with `dock cloudflare add`.")
					return nil
				}
				t := g.io.Table("zone", "connection", "from")
				for _, z := range list {
					from := "organization"
					if z.Platform {
						from = "platform"
					}
					t.Row(z.Name, z.Connection, from)
				}
				return t.Flush()
			})
		},
	}
}

// newCloudflareRecordsCmd is `dock cloudflare records`.
func newCloudflareRecordsCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "records",
		Short: "List the domains Dock routes through tunnels (admin)",
		Long:  `List the domains added from Cloudflare that Dock routes through server tunnels, failed ones first (` + "`dock cloudflare retry`" + ` tries them again).`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			list, err := get[[]apitypes.CloudflareRecord](cmd.Context(), s, apiPrefix+"/cloudflare/records")
			if err != nil {
				return err
			}
			return g.io.Print(list, func() error {
				if len(list) == 0 {
					g.io.Detail("Dock routes no domains through tunnels yet.")
					return nil
				}
				t := g.io.Table("hostname", "server", "tunnel", "status", "detail")
				for _, r := range list {
					t.Row(r.Hostname, orDash(r.Server), orDash(r.Tunnel), string(r.Status), orDash(r.Reason))
				}
				return t.Flush()
			})
		},
	}
}

// newCloudflareRetryCmd is `dock cloudflare retry`.
func newCloudflareRetryCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "retry",
		Short: "Try failed domains and tunnels in Cloudflare again (admin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			r, err := send[apitypes.CloudflareRetried](cmd.Context(), s, http.MethodPost, apiPrefix+"/cloudflare/retry", nil)
			if err != nil {
				return err
			}
			return g.io.Print(r, func() error {
				g.io.Successf("Retrying %d failed domains and tunnels; `dock cloudflare records` shows how it goes", r.Retried)
				return nil
			})
		},
	}
}
