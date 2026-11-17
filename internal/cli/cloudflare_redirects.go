package cli

import (
	"cmp"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Redirects on Cloudflare zones (ADR 0068): `dock cloudflare redirects`.
// The organization's by default; --platform (cloudflare's persistent
// flag) for the platform's.

// newCloudflareRedirectsCmd is `dock cloudflare redirects`: the list, and
// add, set, remove and retry.
func newCloudflareRedirectsCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "redirects",
		Short: "Redirects: hostnames in your zones that go to a URL (admin; --platform: operators)",
		Long: `List the organization's redirects (with --platform, the platform's): hostnames
in its own Cloudflare zones that Cloudflare redirects to a URL at its edge,
with no service behind them. Dock makes a proxied placeholder record
(AAAA 100::) and a Single Redirect rule for each, and edits only its own.
A redirect is on the level its zone is added: a platform zone shared with
organizations keeps its redirects on the platform.

The connection's token needs Zone › Dynamic URL Redirects › Edit on the zone, as
well as Zone › DNS › Edit.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			list, err := get[[]apitypes.CloudflareRedirect](cmd.Context(), s, withPlatform(cmd, apiPrefix+"/cloudflare/redirects"))
			if err != nil {
				return err
			}
			platform, _ := cmd.Flags().GetBool("platform")
			return g.io.Print(list, func() error {
				if len(list) == 0 {
					g.io.Detail("No redirects. Add one with `dock cloudflare redirects add <hostname> <url>" + platformFlag(platform) + "`.")
					return nil
				}
				t := g.io.Table("hostname", "target", "code", "path", "status", "detail")
				for _, r := range list {
					path := "-"
					if r.KeepPath {
						path = "kept"
					}
					t.Row(r.Hostname, r.Target, strconv.Itoa(r.StatusCode), path, string(r.Status), orDash(cmp.Or(r.Reason, r.Note)))
				}
				return t.Flush()
			})
		},
	}
	cmd.AddCommand(newRedirectAddCmd(g), newRedirectSetCmd(g), newRedirectRemoveCmd(g), newRedirectRetryCmd(g))
	return cmd
}

// redirectPath is the API path of one redirect.
func redirectPath(cmd *cobra.Command, host, suffix string) string {
	return withPlatform(cmd, apiPrefix+"/cloudflare/redirects/"+url.PathEscape(host)+suffix)
}

// printRedirect reports a saved redirect.
func printRedirect(g *globals, r apitypes.CloudflareRedirect, verb string) error {
	return g.io.Print(r, func() error {
		g.io.Successf("%s redirect %s → %s (%d)", verb, r.Hostname, r.Target, r.StatusCode)
		g.io.Detail("Dock makes it in Cloudflare in the background; `dock cloudflare redirects` shows when it is live.")
		if r.Note != "" {
			g.io.Warnf("%s", r.Note)
		}
		return nil
	})
}

// newRedirectAddCmd is `dock cloudflare redirects add`.
func newRedirectAddCmd(g *globals) *cobra.Command {
	var req apitypes.CreateCloudflareRedirectRequest
	cmd := &cobra.Command{
		Use:   "add <hostname> <url> [--code 302] [--keep-path] [--connection NAME]",
		Short: "Redirect a hostname in one of your zones to a URL",
		Long: `Redirect a hostname in one of the level's own Cloudflare zones to a URL.
--code is 302 (temporary) unless set: 301 and 308 are permanent, and browsers
keep them. --keep-path appends the request's path and query to the URL.
--connection picks the connection when the zone is on several.

Refused when a domain holds the hostname, or Cloudflare has a record for it
that Dock didn't make.`,
		Example: `  dock cloudflare redirects add instagram.tempo.run https://instagram.com/tempo
  dock cloudflare redirects add docs.tempo.run https://tempo.dev/docs --code 301 --keep-path --platform`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Hostname, req.Target = args[0], args[1]
			s, err := g.connect()
			if err != nil {
				return err
			}
			r, err := send[apitypes.CloudflareRedirect](cmd.Context(), s, http.MethodPost, withPlatform(cmd, apiPrefix+"/cloudflare/redirects"), req)
			if err != nil {
				return err
			}
			return printRedirect(g, r, "Added")
		},
	}
	cmd.Flags().IntVar(&req.StatusCode, "code", 302, "status code: 301, 302, 307 or 308")
	cmd.Flags().BoolVar(&req.KeepPath, "keep-path", false, "append the request's path and query to the URL")
	cmd.Flags().StringVar(&req.Connection, "connection", "", "Cloudflare connection, when the zone is on several")
	return cmd
}

// newRedirectSetCmd is `dock cloudflare redirects set`.
func newRedirectSetCmd(g *globals) *cobra.Command {
	var target string
	var code int
	var keep bool
	cmd := &cobra.Command{
		Use:     "set <hostname> [--to URL] [--code N] [--keep-path[=false]]",
		Short:   "Change a redirect's URL, status code or whether it keeps the path",
		Example: `  dock cloudflare redirects set instagram.tempo.run --to https://instagram.com/tempo.run --code 301`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var req apitypes.UpdateCloudflareRedirectRequest
			if cmd.Flags().Changed("to") {
				req.Target = &target
			}
			if cmd.Flags().Changed("code") {
				req.StatusCode = &code
			}
			if cmd.Flags().Changed("keep-path") {
				req.KeepPath = &keep
			}
			if req.Target == nil && req.StatusCode == nil && req.KeepPath == nil {
				return usageError("nothing to change", "Give --to, --code or --keep-path.")
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			r, err := send[apitypes.CloudflareRedirect](cmd.Context(), s, http.MethodPatch, redirectPath(cmd, args[0], ""), req)
			if err != nil {
				return err
			}
			return printRedirect(g, r, "Changed")
		},
	}
	cmd.Flags().StringVar(&target, "to", "", "the URL to redirect to")
	cmd.Flags().IntVar(&code, "code", 302, "status code: 301, 302, 307 or 308")
	cmd.Flags().BoolVar(&keep, "keep-path", false, "append the request's path and query to the URL")
	return cmd
}

// newRedirectRemoveCmd is `dock cloudflare redirects remove`.
func newRedirectRemoveCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <hostname>",
		Short: "Remove a redirect; Dock deletes its rule and record in Cloudflare",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Remove redirect %s? It stops redirecting once Dock deletes its rule in Cloudflare.", args[0]), "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, redirectPath(cmd, args[0], "")); err != nil {
				return err
			}
			return g.io.Print(map[string]string{"removed": args[0]}, func() error {
				g.io.Successf("Removing redirect %s", args[0])
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

// newRedirectRetryCmd is `dock cloudflare redirects retry`.
func newRedirectRetryCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "retry <hostname>",
		Short: "Try a failed redirect again, after fixing what failed it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			r, err := send[apitypes.CloudflareRedirect](cmd.Context(), s, http.MethodPost, redirectPath(cmd, args[0], "/retry"), nil)
			if err != nil {
				return err
			}
			return printRedirect(g, r, "Retrying")
		},
	}
}
