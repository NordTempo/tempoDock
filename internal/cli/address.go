package cli

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Dock's own address (ADR 0029): the public hostname the control plane's
// Caddy serves the API and UI on, beside the private address.

func newAddressCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "address",
		Short: "Dock's public address: where the UI, API and CLI login are served (operator)",
		Long: `Show where Dock is reached: its public address, if one is set, with the DNS
record it needs and its status (checked now: DNS, then the certificate),
and its private address, which always works.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			a, err := get[apitypes.Address](cmd.Context(), s, apiPrefix+"/address")
			if err != nil {
				return err
			}
			return g.io.Print(a, func() error { return printAddress(g, a) })
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "set <hostname>",
		Short: "Serve Dock at a public hostname, with HTTPS (operator)",
		Long: `Serve the API and UI at https://<hostname> through the control plane's
Caddy. Point the hostname's DNS at the control plane (the record is
printed); Caddy gets a certificate once it resolves. The private address
keeps working. Setting another hostname moves the address.`,
		Example: "  dock address set dock.example.com",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			a, err := send[apitypes.Address](cmd.Context(), s, http.MethodPut, apiPrefix+"/address", apitypes.SetAddressRequest{Hostname: args[0]})
			if err != nil {
				return err
			}
			return g.io.Print(a, func() error {
				g.io.Successf("Dock's public address is %s", a.URL)
				if err := printAddress(g, a); err != nil {
					return err
				}
				if a.Status == apitypes.DomainActive {
					g.io.Detail(fmt.Sprintf("Log in there with `dock login %s`.", a.URL))
				}
				return nil
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "remove",
		Short: "Stop serving Dock at its public address (operator)",
		Long: `Stop serving Dock at its public address. Sessions and CLI contexts that
use it stop working; the private address keeps working.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			a, err := send[apitypes.Address](cmd.Context(), s, http.MethodDelete, apiPrefix+"/address", nil)
			if err != nil {
				return err
			}
			return g.io.Print(a, func() error {
				g.io.Successf("Dock has no public address; it is at %s", a.PrivateURL)
				return nil
			})
		},
	})
	return cmd
}

// printAddress prints the addresses, and the DNS record while the public
// one isn't active.
func printAddress(g *globals, a apitypes.Address) error {
	public := "none (`dock address set <hostname>`)"
	if a.URL != "" {
		public = a.URL
	}
	status := strings.ReplaceAll(string(a.Status), "_", " ")
	if a.StatusReason != "" {
		status += ": " + a.StatusReason
	}
	if err := g.io.Fields("public", public, "status", status, "private", a.PrivateURL); err != nil {
		return err
	}
	if a.Status != apitypes.DomainActive {
		for i, r := range a.DNSRecords {
			label := "Point DNS:"
			if i > 0 {
				label = "       or:"
			}
			_, _ = fmt.Fprintf(g.io.Out, "%s  %s  %s  →  %s\n", label, r.Type, r.Name, r.Value)
		}
	}
	return nil
}
