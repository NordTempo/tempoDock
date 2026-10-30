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

// Cloudflare connections (ADR 0035): API tokens with which Dock runs
// Cloudflare tunnels for tunnel servers and routes domains added from the
// tokens' zones through them.

// cloudflareScopes is what a token needs, for help texts.
const cloudflareScopes = `Create an API token in the Cloudflare dashboard (My Profile › API Tokens, or
the account's API Tokens for one owned by the account) with:
  Account › Cloudflare Tunnel › Edit
  Zone › DNS › Edit    on the zones domains may be added from
  Zone › Zone › Read   on the same zones
Dock only edits records it made (comment dock:<install ID>), never
overwrites one it didn't, and never points DNS at an IP: domains go
through tunnels.`

// newCloudflareCmd is `dock cloudflare`: the list, and add, set, refresh,
// remove, records and retry.
func newCloudflareCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cloudflare",
		Short: "Cloudflare connections: tunnels and domains through them (admin)",
		Long: `List the Cloudflare connections: API tokens with which Dock runs Cloudflare
tunnels for servers behind one, and routes domains added from the tokens'
zones (` + "`dock domain add <host> --cloudflare`" + `) through them with a proxied
CNAME. Tokens are never shown.

` + cloudflareScopes,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			list, err := get[[]apitypes.Cloudflare](cmd.Context(), s, apiPrefix+"/cloudflare")
			if err != nil {
				return err
			}
			return g.io.Print(list, func() error {
				if len(list) == 0 {
					g.io.Detail("No Cloudflare connections. Add one with `dock cloudflare add <name> --token-stdin`.")
					return nil
				}
				t := g.io.Table("name", "account", "zones", "status", "tunnels for", "changed")
				for _, cf := range list {
					status := string(cf.Status)
					if cf.StatusReason != "" {
						status += ": " + cf.StatusReason
					}
					t.Row(cf.Name, cf.AccountName, orDash(strings.Join(cf.Zones, ", ")), status, orDash(strings.Join(cf.UsedBy, ", ")),
						ago(cf.UpdatedAt)+" by "+cf.UpdatedBy)
				}
				return t.Flush()
			})
		},
	}
	cmd.AddCommand(newCloudflareAddCmd(g), newCloudflareSetCmd(g), newCloudflareRefreshCmd(g), newCloudflareRemoveCmd(g),
		newCloudflareZonesCmd(g), newCloudflareRecordsCmd(g), newCloudflareRetryCmd(g))
	return cmd
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
func printCloudflare(g *globals, cf apitypes.Cloudflare, verb string) error {
	return g.io.Print(cf, func() error {
		g.io.Successf("%s Cloudflare connection %s: account %s", verb, cf.Name, cf.AccountName)
		if len(cf.Zones) == 0 {
			g.io.Detail("No domains can be added from it yet: give the token Zone › DNS › Edit and Zone › Read on some zones, then `dock cloudflare refresh " + cf.Name + "`.")
		} else {
			g.io.Detail("Domains can be added from " + strings.Join(cf.Zones, ", ") + " (`dock domain add <host> --cloudflare`).")
		}
		return nil
	})
}

// newCloudflareAddCmd is `dock cloudflare add`.
func newCloudflareAddCmd(g *globals) *cobra.Command {
	var req apitypes.CreateCloudflareRequest
	var tokenStdin bool
	cmd := &cobra.Command{
		Use:   "add <name> --token-stdin [--account-id <id>]",
		Short: "Add a Cloudflare connection (admin)",
		Long: `Add a Cloudflare connection. Dock checks the token before saving it: it is
active, reaches the account and its tunnels, and lists zones; zones whose DNS
records it can't read are left out. --account-id picks the account when the
token reaches several, and is needed for a token owned by an account.

` + cloudflareScopes,
		Example: `  printf %s "$CLOUDFLARE_API_TOKEN" | dock cloudflare add home --token-stdin`,
		Args:    cobra.ExactArgs(1),
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
			cf, err := send[apitypes.Cloudflare](cmd.Context(), s, http.MethodPost, apiPrefix+"/cloudflare", req)
			if err != nil {
				return err
			}
			return printCloudflare(g, cf, "Added")
		},
	}
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the API token from stdin")
	cmd.Flags().StringVar(&req.AccountID, "account-id", "", "Cloudflare account ID")
	return cmd
}

// newCloudflareSetCmd is `dock cloudflare set`.
func newCloudflareSetCmd(g *globals) *cobra.Command {
	var accountID string
	var tokenStdin bool
	cmd := &cobra.Command{
		Use:   "set <name> --token-stdin [--account-id <id>]",
		Short: "Give a Cloudflare connection a new token (admin)",
		Long: `Give a Cloudflare connection a new token, checked like a new one; its zones
are listed again. A connection whose token Cloudflare refused (revoked or
expired) works again, and the tunnels and domains waiting for it resume.`,
		Example: `  printf %s "$NEW_TOKEN" | dock cloudflare set home --token-stdin`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !tokenStdin {
				return usageError("the new API token is required", "Pipe it in with --token-stdin; to list new zones with the same token, use `dock cloudflare refresh`.")
			}
			token, err := readToken(g, "dock cloudflare set "+args[0]+" --token-stdin")
			if err != nil {
				return err
			}
			req := apitypes.UpdateCloudflareRequest{Token: token}
			if cmd.Flags().Changed("account-id") {
				req.AccountID = &accountID
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			cf, err := send[apitypes.Cloudflare](cmd.Context(), s, http.MethodPatch, apiPrefix+"/cloudflare/"+url.PathEscape(args[0]), req)
			if err != nil {
				return err
			}
			return printCloudflare(g, cf, "Saved")
		},
	}
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the API token from stdin")
	cmd.Flags().StringVar(&accountID, "account-id", "", "Cloudflare account ID")
	return cmd
}

// newCloudflareRefreshCmd is `dock cloudflare refresh`.
func newCloudflareRefreshCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "refresh <name>",
		Short: "List a Cloudflare connection's zones again (admin)",
		Long:  `Check the connection's token again and list its zones, after zones were added to (or taken from) the token in Cloudflare.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			cf, err := send[apitypes.Cloudflare](cmd.Context(), s, http.MethodPost, apiPrefix+"/cloudflare/"+url.PathEscape(args[0])+"/refresh", nil)
			if err != nil {
				return err
			}
			return printCloudflare(g, cf, "Refreshed")
		},
	}
}

// newCloudflareRemoveCmd is `dock cloudflare remove`.
func newCloudflareRemoveCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a Cloudflare connection no tunnel uses (admin)",
		Long:  `Remove a Cloudflare connection. Dock forgets the domains it routed with it; their records stay in Cloudflare. Refused while a server's tunnel uses it.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Remove Cloudflare connection %s? The records of its domains stay in Cloudflare, no longer kept by Dock.", args[0]), "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, apiPrefix+"/cloudflare/"+url.PathEscape(args[0])); err != nil {
				return err
			}
			return g.io.Print(map[string]string{"removed": args[0]}, func() error {
				g.io.Successf("Removed Cloudflare connection %s", args[0])
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

// newCloudflareZonesCmd is `dock cloudflare zones`.
func newCloudflareZonesCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "zones",
		Short: "List the zones domains can be added from",
		Long:  `List the zones of the Cloudflare connections: a hostname in one can be added with ` + "`dock domain add <host> --cloudflare`" + `, and Dock routes it through the server's tunnel.`,
		Args:  cobra.NoArgs,
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
					g.io.Detail("No Cloudflare zones are connected; an operator adds a connection with `dock cloudflare add`.")
					return nil
				}
				t := g.io.Table("zone", "connection")
				for _, z := range list {
					t.Row(z.Name, z.Connection)
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
