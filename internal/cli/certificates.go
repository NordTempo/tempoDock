package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Uploaded TLS certificates (ADR 0112): an org's own certificates for
// names ACME can't cover, served for every domain of the org they cover.

// certificatesPath is where the org's uploaded certificates are.
const certificatesPath = apiPrefix + "/org/certificates"

// maxCertFile bounds what --cert and --key read, a little over what the
// API takes so its own message explains a file that is too big.
const maxCertFile = 128 << 10

func newCertificateCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:     "certificate",
		Aliases: []string{"cert"},
		Short:   "Upload TLS certificates for domains Let's Encrypt can't cover",
		Long: `Upload the organization's own TLS certificates: a Cloudflare origin
certificate (SSL mode Full strict), one from an internal or corporate CA for
LAN or Tailscale names, or one a CA you must use issued. A certificate serves
every verified domain of the organization one of its names covers, wildcards
included, instead of a Let's Encrypt certificate.

Uploaded certificates don't renew. Dock alerts 30 days and 7 days before one
expires; upload the renewal and it takes over at once (of several that cover
a domain, the one that expires last serves it). Once one has expired Dock
stops serving it, and its domains get certificates from Let's Encrypt where
it can issue them.`,
	})
	cmd.AddCommand(newCertificateListCmd(g), newCertificateAddCmd(g), newCertificateRemoveCmd(g))
	return cmd
}

func newCertificateListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List uploaded certificates, their expiry and the domains they serve",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			list, err := get[[]apitypes.Certificate](cmd.Context(), s, certificatesPath)
			if err != nil {
				return err
			}
			return g.io.Print(list, func() error {
				if len(list) == 0 {
					g.io.Detail("No uploaded certificates; domains get theirs from Let's Encrypt. Upload one with `dock certificate add`.")
					return nil
				}
				t := g.io.Table("id", "names", "issuer", "expires", "status", "serves")
				for _, c := range list {
					issuer := c.Issuer
					if !c.Public {
						issuer += " (not public)"
					}
					t.Row(strconv.FormatInt(c.ID, 10), strings.Join(c.Names, ", "), issuer, certExpiry(c.NotAfter), string(c.Status), certServes(c.Domains))
				}
				return t.Flush()
			})
		},
	}
}

// certExpiry is a certificate's end date and how far off it is.
func certExpiry(t time.Time) string {
	days := int(time.Until(t).Hours() / 24)
	day := t.UTC().Format(time.DateOnly)
	switch {
	case time.Until(t) <= 0:
		return day + " (expired)"
	case days < 1:
		return day + " (today)"
	}
	return fmt.Sprintf("%s (%d days)", day, days)
}

// certServes lists the hostnames a certificate serves: the first five,
// then how many more (--json has them all).
func certServes(ds []apitypes.CertificateDomain) string {
	const shown = 5
	if len(ds) == 0 {
		return "-"
	}
	var hosts []string
	for _, d := range ds[:min(len(ds), shown)] {
		hosts = append(hosts, d.Hostname)
	}
	if len(ds) > shown {
		return fmt.Sprintf("%s and %d more", strings.Join(hosts, ", "), len(ds)-shown)
	}
	return strings.Join(hosts, ", ")
}

func newCertificateAddCmd(g *globals) *cobra.Command {
	var certFile, keyFile string
	var req apitypes.AddCertificateRequest
	cmd := &cobra.Command{
		Use:   "add --cert FILE --key FILE [--domain HOST]",
		Short: "Upload a certificate chain and its private key",
		Long: `Upload a certificate chain and its private key, both PEM files. The chain is
the certificate and its intermediates (fullchain.pem); Dock puts them in
order. It checks that the key matches, the certificate names DNS hostnames
and is valid now, and with --domain that it covers that hostname. The key is
stored encrypted and never shown again. Pass - to read one of them from stdin.

It serves the organization's domains it covers at once, on every server.`,
		Example: `  dock certificate add --cert origin.pem --key origin.key --domain app.example.com
  dock certificate add --cert fullchain.pem --key privkey.pem`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if certFile == "" || keyFile == "" {
				return usageError("--cert and --key are required", "Pass the PEM files: `dock certificate add --cert fullchain.pem --key privkey.pem`.")
			}
			if certFile == "-" && keyFile == "-" {
				return usageError("only one of --cert and --key can be read from stdin", "Pass a file for the other.")
			}
			var err error
			if req.Chain, err = g.readCertFile(certFile, "--cert"); err != nil {
				return err
			}
			if req.Key, err = g.readCertFile(keyFile, "--key"); err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			c, err := send[apitypes.Certificate](cmd.Context(), s, http.MethodPost, certificatesPath, req)
			if err != nil {
				return err
			}
			return g.io.Print(c, func() error {
				g.io.Successf("Uploaded certificate %d for %s, valid until %s", c.ID, strings.Join(c.Names, ", "), c.NotAfter.UTC().Format(time.DateOnly))
				if len(c.Domains) == 0 {
					g.io.Detail("It serves no domain yet: none of the organization's verified domains matches, or one that expires later covers them.")
				} else {
					g.io.Detail("Serving " + certServes(c.Domains) + ".")
				}
				if !c.Public {
					g.io.Detail(c.Issuer + " isn't a publicly trusted CA: fine behind Cloudflare (Full strict) or for clients that trust it.")
				}
				return nil
			})
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&certFile, "cert", "", "PEM file with the certificate and its intermediates (- for stdin)")
	fs.StringVar(&keyFile, "key", "", "PEM file with the private key (- for stdin)")
	fs.StringVar(&req.Domain, "domain", "", "check that the certificate covers this hostname")
	return cmd
}

// readCertFile reads --cert or --key: a file, or stdin for "-".
func (g *globals) readCertFile(name, flag string) (string, error) {
	r := g.io.In
	if name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return "", &output.Error{Exit: output.ExitUsage, Err: err, Hint: "Pass the path of a PEM file to " + flag + "."}
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, maxCertFile+1))
	if err != nil {
		return "", &output.Error{Exit: output.ExitUsage, Err: err, Hint: "Pass the path of a PEM file to " + flag + "."}
	}
	if len(b) > maxCertFile {
		return "", usageError(flag+" is larger than 128 KB", "Pass a PEM file with the certificate chain or the key only.")
	}
	return string(b), nil
}

func newCertificateRemoveCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "remove <id> [--yes]",
		Aliases: []string{"rm"},
		Short:   "Remove an uploaded certificate",
		Long:    `Remove an uploaded certificate. Its domains get another uploaded certificate that covers them, or certificates from Let's Encrypt again.`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return usageError("the certificate's ID is a number", "See the IDs with `dock certificate list`.")
			}
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Remove certificate %d? Its domains get another upload that covers them, or Let's Encrypt certificates.", id), "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, certificatesPath+"/"+args[0]); err != nil {
				return err
			}
			return g.io.Print(map[string]int64{"removed": id}, func() error {
				g.io.Successf("Removed certificate %d", id)
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}
