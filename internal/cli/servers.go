package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Servers and placement (ADR 0014, 0015).

func newServerCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "server",
		Short: "Servers environments run on, and what may be placed on them",
	})
	cmd.AddCommand(newServerListCmd(g), newServerShowCmd(g), newServerDiskCmd(g), newServerPruneCmd(g), newServerAddCmd(g),
		newServerUpdateCmd(g), newServerRevokeCmd(g), newServerRotateKeyCmd(g), newServerRemoveCmd(g), newServerTunnelCmd(g))
	return cmd
}

func newServerListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List servers",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			servers, err := get[[]apitypes.Server](cmd.Context(), s, apiPrefix+"/servers")
			if err != nil {
				return err
			}
			return g.io.Print(servers, func() error {
				t := g.io.Table("name", "status", "version", "ingress", "accepts", "builds", "memory reserved", "projects", "environments")
				for _, sv := range servers {
					builds := ""
					if sv.Builder {
						builds = "yes"
					}
					t.Row(sv.Name, serverStatus(sv), dash(sv.Version), dash(ingressWords(sv.Ingress)), accepts(sv), dash(builds), reserved(sv),
						strconv.Itoa(sv.Projects), strconv.Itoa(sv.Environments))
				}
				return t.Flush()
			})
		},
	}
}

// serverStatus is a server's link status, with when it was last seen if
// it is offline and until when it can join if it is joining.
func serverStatus(s apitypes.Server) string {
	switch {
	case s.Status == apitypes.ServerOffline && s.LastSeenAt != nil:
		return "offline (seen " + ago(*s.LastSeenAt) + ")"
	case s.Status == apitypes.ServerJoining && s.JoinExpiresAt != nil:
		return "joining (token until " + s.JoinExpiresAt.Local().Format("15:04") + ")"
	}
	return string(s.Status)
}

// ingressTarget is where DNS for the server's hostnames points: its public
// IPs, or its tunnel's CNAME target.
func ingressTarget(s apitypes.Server) string {
	switch {
	case s.TunnelHostname != "":
		return " (CNAME " + s.TunnelHostname + ")"
	case len(s.PublicIPs) > 0:
		return " (" + strings.Join(s.PublicIPs, ", ") + ")"
	}
	return ""
}

// ingressWords is how the CLI writes an ingress mode.
func ingressWords(ingress apitypes.Ingress) string {
	if ingress == apitypes.IngressCloudflareTunnel {
		return "tunnel"
	}
	return string(ingress)
}

func newServerAddCmd(g *globals) *cobra.Command {
	var prod, nonProd, builder, tunnel bool
	var cloudflare string
	cmd := &cobra.Command{
		Use:   "add <name> [--production] [--non-production] [--builder] [--tunnel]",
		Short: "Add a server: create its join token and print the install command (admin)",
		Long: `Add a server as a node of this control plane. Prints a join token, valid
for an hour and usable once, and the install.sh command to run on the new
server. The token is shown only here. The server shows as joining until it
connects.

Choose which environment kinds it takes (both is fine), and --builder for a
server that builds images for others. All of it can be changed later with
` + "`dock server update`" + `.

--tunnel puts the server behind a Cloudflare tunnel that Dock creates once it
joins, with a Cloudflare connection (` + "`dock cloudflare add`" + `): it needs no
open ports, and domains added from Cloudflare go through the tunnel.`,
		Example: `  dock server add homelab --non-production --builder --tunnel
  dock server add vps-2 --production`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !prod && !nonProd {
				return usageError("choose what the server takes", "Pass --production, --non-production or both.")
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			a, err := send[apitypes.AddedServer](cmd.Context(), s, http.MethodPost, apiPrefix+"/servers", apitypes.AddServerRequest{
				Name: args[0], AcceptsProduction: prod, AcceptsNonProduction: nonProd, Builder: builder,
				Tunnel: tunnel, Cloudflare: cloudflare,
			})
			if err != nil {
				return err
			}
			return g.io.Print(a, func() error {
				builds := ""
				if a.Server.Builder {
					builds = " and builds images"
				}
				g.io.Successf("Server %s added; it accepts %s%s", a.Server.Name, accepts(a.Server), builds)
				_, _ = fmt.Fprintf(g.io.Out, "\nOn the new server, with Docker installed and the dock binary and deploy/ copied over, run:\n\n  %s\n\n", a.Command)
				_, _ = fmt.Fprintf(g.io.Out, "The token works once, until %s, and is not shown again.\nThen check it is online: dock server list\n", a.ExpiresAt.Local().Format("15:04"))
				return nil
			})
		},
	}
	f := cmd.Flags()
	f.BoolVar(&prod, "production", false, "accept production environments")
	f.BoolVar(&nonProd, "non-production", false, "accept non-production environments")
	f.BoolVar(&builder, "builder", false, "build images on this server")
	f.BoolVar(&tunnel, "tunnel", false, "put it behind a Cloudflare tunnel Dock creates once it joins")
	f.StringVar(&cloudflare, "cloudflare", "", "Cloudflare connection for --tunnel (default: the only one)")
	return cmd
}

// newServerTunnelCmd is `dock server tunnel`: have Dock manage a tunnel
// server's Cloudflare tunnel.
func newServerTunnelCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "tunnel",
		Short: "Have Dock run a server's Cloudflare tunnel (admin)",
		Long: `Have Dock run a Cloudflare tunnel on a server, in tunnel mode or next to a direct server's public IP (ADR 0036), with
a Cloudflare connection (` + "`dock cloudflare add`" + `): cloudflared runs there in
Dock's dock-cloudflared container, and a tunnel Dock created is deleted with
the server. Adding a domain from Cloudflare does this when the server needs it.`,
	})
	run := func(action string) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			fs := cmd.Flags()
			cf, _ := fs.GetString("cloudflare")
			s, err := g.connect()
			if err != nil {
				return err
			}
			sv, err := send[apitypes.Server](cmd.Context(), s, http.MethodPost, apiPrefix+"/servers/"+url.PathEscape(args[0])+"/tunnel",
				apitypes.ServerTunnelRequest{Action: action, Cloudflare: cf})
			if err != nil {
				return err
			}
			return g.io.Print(sv, func() error {
				if action == "create" {
					g.io.Successf("Dock creates server %s's Cloudflare tunnel; `dock server show %s` shows when it is ready", sv.Name, sv.Name)
					return nil
				}
				g.io.Successf("Dock runs server %s's tunnel", sv.Name)
				g.io.Detail("It runs in Dock's dock-cloudflared container now, next to the old cloudflared service. Run install.sh on the server again to remove that one.")
				return nil
			})
		}
	}
	create := &cobra.Command{
		Use:     "create <server> [--cloudflare <connection>]",
		Short:   "Create a tunnel for a server that runs none (admin)",
		Example: `  dock server tunnel create homelab`,
		Args:    cobra.ExactArgs(1),
		RunE:    run("create"),
	}
	create.Flags().String("cloudflare", "", "Cloudflare connection (default: the only one)")
	adopt := &cobra.Command{
		Use:   "adopt <server> [--cloudflare <connection>]",
		Short: "Take over the tunnel a server runs already (admin)",
		Long: `Take over the tunnel the server runs already, made in the Cloudflare dashboard
and installed with its token. Its routes stay; domains added from Cloudflare
get their own route when its catch-all doesn't go to the server's Caddy.
Traffic doesn't drop: Dock's cloudflared joins the old one as a second
connector until install.sh removes the old one. Dock never deletes a tunnel
it adopted. Adding a domain from Cloudflare adopts it too.`,
		Example: `  dock server tunnel adopt homelab`,
		Args:    cobra.ExactArgs(1),
		RunE:    run("adopt"),
	}
	adopt.Flags().String("cloudflare", "", "Cloudflare connection of the tunnel's account (default: the only one)")
	cmd.AddCommand(create, adopt)
	return cmd
}

// tunnelWords describes the tunnel Dock manages for a server.
func tunnelWords(t *apitypes.ServerTunnel) string {
	words := string(t.Status)
	if t.Adopted {
		words += ", adopted"
	}
	words += ", connection " + t.Connection
	if t.Connectors != nil {
		words += fmt.Sprintf(", %d connectors", len(t.Connectors))
	}
	if t.Reason != "" {
		words += ": " + t.Reason
	}
	return words
}

func newServerRevokeCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "revoke <name>",
		Short: "Revoke a server's key and drop its connection (admin)",
		Long: `Revoke a server: the control plane forgets its key and drops the link, so it
can't connect again until it re-joins with a new token (` + "`dock server add`" + `, then
install.sh with NODE_JOIN_TOKEN on it). Apps already on it keep running there,
but nothing can be deployed to it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Revoke server %s? It disconnects until it joins again.", args[0]), "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			sv, err := send[apitypes.Server](cmd.Context(), s, http.MethodPost, apiPrefix+"/servers/"+url.PathEscape(args[0])+"/revoke", nil)
			if err != nil {
				return err
			}
			return g.io.Print(sv, func() error {
				g.io.Successf("Revoked server %s", sv.Name)
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

func newServerRotateKeyCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "rotate-key <name>",
		Short: "Have an online server switch to a new link key (admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			sv, err := send[apitypes.Server](cmd.Context(), s, http.MethodPost, apiPrefix+"/servers/"+url.PathEscape(args[0])+"/rotate-key", nil)
			if err != nil {
				return err
			}
			return g.io.Print(sv, func() error {
				g.io.Successf("Server %s is reconnecting with a new key", sv.Name)
				return nil
			})
		},
	}
}

func newServerRemoveCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a server no live project uses (admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Remove server %s?", args[0]), "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, apiPrefix+"/servers/"+url.PathEscape(args[0])); err != nil {
				return err
			}
			v := struct {
				Name    string `json:"name"`
				Removed bool   `json:"removed"`
			}{args[0], true}
			return g.io.Print(v, func() error {
				g.io.Successf("Removed server %s", args[0])
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

// accepts describes which kinds a server takes, for tables.
func accepts(s apitypes.Server) string {
	var kinds []string
	if s.AcceptsProduction {
		kinds = append(kinds, "production")
	}
	if s.AcceptsNonProduction {
		kinds = append(kinds, "non-production")
	}
	out := strings.Join(kinds, ", ")
	if out == "" {
		out = "nothing"
	}
	if s.DisabledForPlacement {
		out += " (placement disabled)"
	}
	return out
}

func reserved(s apitypes.Server) string {
	if s.MemoryBytes == 0 {
		return bytesize(s.ReservedMemoryBytes) + " of ?"
	}
	return bytesize(s.ReservedMemoryBytes) + " of " + bytesize(s.MemoryBytes)
}

func newServerShowCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show a server: what is placed on it and its current usage (admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			d, err := get[apitypes.ServerDetail](cmd.Context(), s, apiPrefix+"/servers/"+url.PathEscape(args[0]))
			if err != nil {
				return err
			}
			return g.io.Print(d, func() error {
				sv := d.Server
				_, _ = fmt.Fprintf(g.io.Out, "%s  %s  %s\naccepts %s\nmemory reserved %s\n", sv.Name, serverStatus(sv), dash(sv.Version), accepts(sv), reserved(sv))
				_, _ = fmt.Fprintf(g.io.Out, "ingress %s%s\nbase domain %s\n", dash(ingressWords(sv.Ingress)), ingressTarget(sv), dash(sv.BaseDomain))
				if sv.Tunnel != nil {
					_, _ = fmt.Fprintf(g.io.Out, "tunnel managed by Dock: %s\n", tunnelWords(sv.Tunnel))
				}
				if sv.Builder {
					_, _ = fmt.Fprintln(g.io.Out, "builds images")
				}
				if u := sv.Usage; u != nil {
					_, _ = fmt.Fprintf(g.io.Out, "cpu %.1f%% of %d cores, memory %s of %s, load %.2f\n",
						u.CPUPercent, u.CPUs, bytesize(u.MemoryUsedBytes), bytesize(u.MemoryTotalBytes), u.Load1)
					for _, disk := range u.Disks {
						_, _ = fmt.Fprintf(g.io.Out, "disk %s of %s (%s)\n", bytesize(disk.UsedBytes), bytesize(disk.TotalBytes), strings.Join(disk.Paths, ", "))
					}
				}
				if len(d.Placements) == 0 {
					_, _ = fmt.Fprintln(g.io.Out, "\nNothing placed here yet.")
					return nil
				}
				_, _ = fmt.Fprintln(g.io.Out)
				t := g.io.Table("project", "kind", "environments", "pinned")
				for _, p := range d.Placements {
					pinned := ""
					if p.Pinned {
						pinned = "yes"
					}
					t.Row(p.Project, kindWords(p.Kind), dash(strings.Join(p.Environments, ", ")), dash(pinned))
				}
				return t.Flush()
			})
		},
	}
}

func newServerUpdateCmd(g *globals) *cobra.Command {
	var prod, noProd, nonProd, noNonProd, disable, enable, builder, noBuilder bool
	var baseDomain string
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Change what a server accepts, whether it builds, and its base domain (admin)",
		Long: `Change which environment kinds a server accepts, whether it takes new
placements, whether it builds images, and the base domain of the hostnames
generated for services on it (` + "`--base-domain \"\"`" + ` turns them off). Environments
already on the server stay where they are.`,
		Example: `  dock server update homelab --non-production --no-production
  dock server update homelab --builder --base-domain example-apps.dev
  dock server update vps-2 --disable-placement`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var req apitypes.UpdateServerRequest
			var err error
			if req.AcceptsProduction, err = switchFlag(prod, noProd, "production"); err != nil {
				return err
			}
			if req.AcceptsNonProduction, err = switchFlag(nonProd, noNonProd, "non-production"); err != nil {
				return err
			}
			if req.DisabledForPlacement, err = switchFlag(disable, enable, "disable-placement"); err != nil {
				return err
			}
			if req.Builder, err = switchFlag(builder, noBuilder, "builder"); err != nil {
				return err
			}
			if cmd.Flags().Changed("base-domain") {
				req.BaseDomain = &baseDomain
			}
			if req == (apitypes.UpdateServerRequest{}) {
				return usageError("nothing to change", "Pass --production, --no-production, --non-production, --no-non-production, --disable-placement, --enable-placement, --builder, --no-builder or --base-domain.")
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			sv, err := send[apitypes.Server](cmd.Context(), s, http.MethodPatch, apiPrefix+"/servers/"+url.PathEscape(args[0]), req)
			if err != nil {
				return err
			}
			return g.io.Print(sv, func() error {
				builds, base := "doesn't build", "no base domain"
				if sv.Builder {
					builds = "builds images"
				}
				if sv.BaseDomain != "" {
					base = "base domain " + sv.BaseDomain
				}
				g.io.Successf("Server %s accepts %s; %s; %s", sv.Name, accepts(sv), builds, base)
				return nil
			})
		},
	}
	f := cmd.Flags()
	f.BoolVar(&prod, "production", false, "accept production environments")
	f.BoolVar(&noProd, "no-production", false, "stop accepting production environments")
	f.BoolVar(&nonProd, "non-production", false, "accept non-production environments")
	f.BoolVar(&noNonProd, "no-non-production", false, "stop accepting non-production environments")
	f.BoolVar(&disable, "disable-placement", false, "place nothing new here (pins still work)")
	f.BoolVar(&enable, "enable-placement", false, "take new placements again")
	f.BoolVar(&builder, "builder", false, "build images here")
	f.BoolVar(&noBuilder, "no-builder", false, "stop building images here")
	f.StringVar(&baseDomain, "base-domain", "", "base of generated hostnames, <service>-<project>[-<env>].<base>; needs wildcard DNS to this server")
	return cmd
}

// switchFlag turns an on/off flag pair into an optional bool.
func switchFlag(on, off bool, name string) (*bool, error) {
	switch {
	case on && off:
		return nil, usageError("conflicting flags for "+name, "Pass only one of each on/off pair.")
	case on:
		return &on, nil
	case off:
		v := false
		return &v, nil
	}
	return nil, nil
}

// kindWords is how the CLI writes an environment kind.
func kindWords(k apitypes.EnvironmentKind) string {
	return strings.ReplaceAll(string(k), "_", "-")
}

// parseKind accepts production, non-production or non_production.
func parseKind(s string) (apitypes.EnvironmentKind, error) {
	switch strings.ReplaceAll(s, "-", "_") {
	case "":
		return "", nil
	case string(apitypes.KindProduction):
		return apitypes.KindProduction, nil
	case string(apitypes.KindNonProduction):
		return apitypes.KindNonProduction, nil
	}
	return "", usageError(fmt.Sprintf("unknown kind %q", s), "Use production or non-production.")
}

func newProjectPinCmd(g *globals) *cobra.Command {
	var kind, server string
	cmd := &cobra.Command{
		Use:   "pin <project> --kind production|non-production --server <name>",
		Short: "Pin a project's environments of a kind to a server (admin)",
		Long: `Pin where a project's environments of one kind will live, before any of
them exist. Without a pin, the first environment of a kind goes to the
eligible server with the most free memory. The server must accept the kind.`,
		Example: "  dock project pin shop --kind production --server vps-2",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := parseKind(kind)
			if err != nil {
				return err
			}
			if k == "" || server == "" {
				return usageError("--kind and --server are required", "")
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			t := target{Project: args[0]}
			p, err := send[apitypes.Placement](cmd.Context(), s, http.MethodPut, t.projectPath()+"/placements/"+string(k), apitypes.PinPlacementRequest{Server: server})
			if err != nil {
				return err
			}
			return g.io.Print(p, func() error {
				g.io.Successf("Pinned %s environments of %s to %s", kindWords(p.Kind), t.Project, p.Server)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "production or non-production")
	cmd.Flags().StringVar(&server, "server", "", "server name")
	return cmd
}
