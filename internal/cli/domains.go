package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

func newDomainCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "domain", Short: "Manage a service's public hostnames"})
	cmd.AddCommand(newDomainListCmd(g), newDomainAddCmd(g), newDomainGenerateCmd(g), newDomainCheckCmd(g), newDomainRemoveCmd(g))
	return cmd
}

func newDomainListCmd(g *globals) *cobra.Command {
	var f targetFlags
	var wide bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List domains with their status and the DNS records to create",
		Long: `List the service's domains with their status and the DNS record to create:
the TXT record that proves you control a custom hostname first, while it
isn't verified. --wide adds each domain's URL, when the server last checked its DNS, and
what the hostname resolved to then.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			domains, err := get[[]apitypes.Domain](cmd.Context(), s, t.servicePath()+"/domains")
			if err != nil {
				return err
			}
			return g.io.Print(domains, func() error {
				if len(domains) == 0 {
					g.io.Successf("%s has no domains. Add one with `dock domain add <host>` or `dock domain generate`.", t)
					return nil
				}
				headers := []string{"hostname", "status", "redirect", "dns"}
				if wide {
					headers = append(headers, "url", "checked", "resolves to")
				}
				tb := g.io.Table(headers...)
				for _, d := range domains {
					dns := "-"
					switch {
					case d.Verification != nil:
						r := d.Verification
						dns = r.Type + " " + r.Name + " → " + r.Value
					case d.DNS != nil:
						dns = dnsWords(d.DNS)
					case d.Status != apitypes.DomainActive && len(d.DNSRecords) > 0:
						r := d.DNSRecords[0]
						dns = r.Type + " " + r.Name + " → " + r.Value
					}
					status := string(d.Status)
					if d.StatusReason != "" {
						status += ": " + d.StatusReason
					}
					row := []string{d.Hostname, status, dash(d.RedirectTo), dns}
					if wide {
						row = append(row, d.URL, agoPtr(d.CheckedAt), dash(strings.Join(d.ResolvedIPs, ", ")))
					}
					tb.Row(row...)
				}
				return tb.Flush()
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().BoolVarP(&wide, "wide", "w", false, "also show the URL, the last DNS check and what the hostname resolved to")
	return cmd
}

// domainFlags are the options shared by `domain add` and `domain generate`.
type domainFlags struct {
	targetFlags
	detach  bool
	timeout time.Duration
}

func (f *domainFlags) add(cmd *cobra.Command) {
	f.targetFlags.add(cmd, needService)
	cmd.Flags().BoolVarP(&f.detach, "detach", "d", false, "don't wait for DNS and the certificate")
	cmd.Flags().DurationVar(&f.timeout, "timeout", 5*time.Minute, "how long to wait for DNS and the certificate")
}

func newDomainAddCmd(g *globals) *cobra.Command {
	var f domainFlags
	var redirect, analyticsMode string
	var fromCloudflare bool
	cmd := &cobra.Command{
		Use:   "add <hostname> [--cloudflare]",
		Short: "Add a hostname and wait for DNS and its certificate",
		Long: `Add any hostname to the service. The exact DNS record to create is printed;
DNS can be set up before or after. Unless --detach, waits (up to --timeout)
while the server checks DNS and issues the certificate. Still waiting after
that is not an error: the server keeps checking.

A custom hostname is routed only once a TXT record proves your organization
controls it; the record to create is printed first, and ` + "`dock domain check`" + `
looks for it at once. Hostnames under the server's base domain, from
Cloudflare, or added by an operator need none. A hostname another
organization has proved is refused.

--cloudflare adds a hostname in a zone connected to Dock (` + "`dock cloudflare zones`" + `)
and has Dock set up all it needs: it routes the hostname through the
server's Cloudflare tunnel (creating or adopting one) and creates its
proxied CNAME. On a direct server, the tunnel runs next to the public IP and
serves only such domains. A record Dock didn't make at that name is refused.`,
		Example: `  dock domain add shop.example.com -s web
  dock domain add shop.tempo.run -s web --cloudflare`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.addDomain(cmd.Context(), f, "/domains", apitypes.AddDomainRequest{Hostname: args[0], RedirectTo: redirect, Cloudflare: fromCloudflare, Analytics: analyticsMode})
		},
	}
	f.add(cmd)
	cmd.Flags().StringVar(&redirect, "redirect-to", "", "permanently redirect to this hostname instead of serving the service")
	cmd.Flags().BoolVar(&fromCloudflare, "cloudflare", false, "add it from a connected Cloudflare zone: Dock routes it through the server's tunnel")
	cmd.Flags().StringVar(&analyticsMode, "analytics", "", "for a hostname that collected analytics on another service: keep its site's history (default) or start a new site (keep|new)")
	return cmd
}

func newDomainGenerateCmd(g *globals) *cobra.Command {
	var f domainFlags
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Give the service a hostname under the server's base domain",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return g.addDomain(cmd.Context(), f, "/domains/generate", nil)
		},
	}
	f.add(cmd)
	return cmd
}

// addDomain adds (or generates) a domain, prints the DNS record to create
// and waits for it to go live, like this:
//
//	Added shop.example.com → web (production)
//	Prove control:  TXT  _dock-challenge.shop.example.com  →  dock-verify=…
//	Point DNS:  A  shop.example.com  →  203.0.113.10
//	Waiting for the TXT record... ok. Waiting for DNS... ok. Issuing certificate... ok.
//	https://shop.example.com is live.
func (g *globals) addDomain(ctx context.Context, f domainFlags, path string, body any) error {
	s, t, err := g.open(f.targetFlags, needService)
	if err != nil {
		return err
	}
	var w *watcher
	if !f.detach {
		if w, err = g.watch(ctx, s, t); err != nil {
			return err
		}
		defer w.close()
	}
	dc, err := send[apitypes.DomainChange](ctx, s, http.MethodPost, t.servicePath()+path, body)
	if err != nil {
		return err
	}
	d := dc.Domain
	if !g.io.JSON {
		target := d.Service
		if d.RedirectTo != "" {
			target = "redirect to " + d.RedirectTo
		}
		_, _ = fmt.Fprintf(g.io.Out, "Added %s → %s (%s)\n", d.Hostname, target, d.Environment)
		if r := d.Verification; r != nil {
			_, _ = fmt.Fprintf(g.io.Out, "Prove control:  %s  %s  →  %s\n", r.Type, r.Name, r.Value)
		}
		if d.DNS != nil {
			_, _ = fmt.Fprintf(g.io.Out, "DNS: Dock routes it through the server's Cloudflare tunnel (zone %s)\n", d.DNS.Zone)
		}
		for i, r := range d.DNSRecords {
			if d.DNS != nil {
				break
			}
			label := "Point DNS:"
			if i > 0 {
				label = "       or:"
			}
			_, _ = fmt.Fprintf(g.io.Out, "%s  %s  %s  →  %s\n", label, r.Type, r.Name, r.Value)
		}
		if dc.Release != nil {
			g.io.Progressf("Redeploying %s to publish its port (release %d)", d.Service, dc.Release.Number)
		}
	}
	if w != nil {
		if dc.Domain, err = g.waitForDomain(ctx, w, t, d, f.timeout); err != nil {
			return err
		}
	}
	return g.io.Print(dc, func() error {
		switch dc.Domain.Status {
		case apitypes.DomainActive:
			_, err := fmt.Fprintf(g.io.Out, "%s is live.\n", dc.Domain.URL)
			return err
		case apitypes.DomainError:
			return nil // reported as an error below
		default:
			if !f.detach {
				g.io.Warnf("Still %s after %s: %s", strings.ReplaceAll(string(dc.Domain.Status), "_", " "), f.timeout, dash(dc.Domain.StatusReason))
			}
			g.io.Progressf("The server keeps checking. See progress with `dock domain list`.")
			return nil
		}
	})
}

// waitForDomain prints DNS and certificate progress from domain events
// until the domain is active, fails, or timeout passes. Timing out returns
// the domain as it was, without an error. The domain is also fetched again
// now and then, in case an event was missed.
func (g *globals) waitForDomain(ctx context.Context, w *watcher, t target, d apitypes.Domain, timeout time.Duration) (apitypes.Domain, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	refetch := func() {
		domains, err := get[[]apitypes.Domain](ctx, w.s, t.servicePath()+"/domains")
		if err != nil {
			return
		}
		for _, x := range domains {
			if x.Hostname == d.Hostname {
				d = x
			}
		}
	}
	open := "" // the step printed without its "ok." yet
	step := func(name string) {
		if open == name || g.io.JSON {
			return
		}
		if open != "" {
			_, _ = fmt.Fprint(g.io.Err, " ok. ")
		}
		_, _ = fmt.Fprint(g.io.Err, name+"...")
		open = name
	}
	finish := func(word string) {
		if open != "" && !g.io.JSON {
			_, _ = fmt.Fprintln(g.io.Err, " "+word)
		}
		open = ""
	}
	for {
		switch d.Status {
		case apitypes.DomainActive:
			finish("ok.")
			return d, nil
		case apitypes.DomainError:
			finish("failed.")
			return d, &output.Error{
				Exit:    output.ExitError,
				Message: fmt.Sprintf("%s: %s", d.Hostname, d.StatusReason),
				Hint:    "Fix the cause; the server keeps checking. See `dock domain list`.",
			}
		case apitypes.DomainIssuingCertificate:
			step("Issuing certificate")
		default:
			if d.Verified {
				step("Waiting for DNS")
			} else {
				step("Waiting for the TXT record")
			}
		}
		select {
		case <-ctx.Done():
			finish("")
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return d, nil
			}
			return d, ctx.Err()
		case <-tick.C:
			refetch()
		case ev, ok := <-w.events:
			if !ok {
				finish("")
				g.io.Warnf("Lost the live event stream.")
				return d, nil
			}
			if ev.Type == apitypes.EventDomain && ev.Domain.Hostname == d.Hostname && ev.Message != "removed" {
				d = *ev.Domain
			}
		}
	}
}

func newDomainCheckCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "check <hostname>",
		Short: "Check a domain's TXT record, DNS and certificate now",
		Long: `Check a domain now instead of at the server's next retry: its TXT record
while it isn't verified, then its DNS and certificate. Prints its status and
the record still to create.`,
		Example: `  dock domain check shop.example.com -s web`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			d, err := send[apitypes.Domain](cmd.Context(), s, http.MethodPost, t.servicePath()+"/domains/"+url.PathEscape(args[0])+"/check", nil)
			if err != nil {
				return err
			}
			return g.io.Print(d, func() error {
				status := strings.ReplaceAll(string(d.Status), "_", " ")
				if d.StatusReason != "" {
					status += ": " + d.StatusReason
				}
				_, err := fmt.Fprintf(g.io.Out, "%s: %s\n", d.Hostname, status)
				if r := d.Verification; err == nil && r != nil {
					_, err = fmt.Fprintf(g.io.Out, "Prove control:  %s  %s  →  %s\n", r.Type, r.Name, r.Value)
				}
				return err
			})
		},
	}
	f.add(cmd, needService)
	return cmd
}

func newDomainRemoveCmd(g *globals) *cobra.Command {
	var f targetFlags
	var yes bool
	cmd := &cobra.Command{
		Use:     "remove <hostname>",
		Aliases: []string{"rm"},
		Short:   "Remove a hostname; it stops routing right away",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			host := args[0]
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Remove %s from %s? It stops serving traffic immediately.", host, t), "yes"); err != nil {
					return err
				}
			}
			if err := del(cmd.Context(), s, t.servicePath()+"/domains/"+url.PathEscape(host)); err != nil {
				return err
			}
			v := struct {
				Hostname string `json:"hostname"`
				Removed  bool   `json:"removed"`
			}{host, true}
			return g.io.Print(v, func() error {
				g.io.Successf("Removed %s from %s", host, t)
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

// dnsWords describes how Dock routes a domain through its tunnel, for
// `domain list`.
func dnsWords(d *apitypes.DomainDNS) string {
	if d.Reason != "" && d.Status == apitypes.DNSError {
		return "Cloudflare error: " + d.Reason
	}
	return "Cloudflare " + string(d.Status)
}
