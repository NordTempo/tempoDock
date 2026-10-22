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

// Servers and placement (ADR 0015).

func newServerCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "server",
		Short: "Servers environments run on, and what may be placed on them",
	})
	cmd.AddCommand(newServerListCmd(g), newServerShowCmd(g), newServerUpdateCmd(g))
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
				t := g.io.Table("name", "status", "version", "accepts", "memory reserved", "projects", "environments")
				for _, sv := range servers {
					t.Row(sv.Name, sv.Status, dash(sv.Version), accepts(sv), reserved(sv), strconv.Itoa(sv.Projects), strconv.Itoa(sv.Environments))
				}
				return t.Flush()
			})
		},
	}
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
				_, _ = fmt.Fprintf(g.io.Out, "%s  %s  %s\naccepts %s\nmemory reserved %s\n", sv.Name, sv.Status, dash(sv.Version), accepts(sv), reserved(sv))
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
	var prod, noProd, nonProd, noNonProd, disable, enable bool
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Change which environment kinds a server accepts (admin)",
		Long: `Change which environment kinds a server accepts, and whether it takes new
placements. Environments already on the server stay where they are.`,
		Example: `  dock server update homelab --non-production --no-production
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
			if req == (apitypes.UpdateServerRequest{}) {
				return usageError("nothing to change", "Pass --production, --no-production, --non-production, --no-non-production, --disable-placement or --enable-placement.")
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
				g.io.Successf("Server %s accepts %s", sv.Name, accepts(sv))
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
