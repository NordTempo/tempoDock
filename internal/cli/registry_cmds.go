package cli

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The platform registry (ADR 0014 section 4): its size, pruning, where it
// keeps its images (ADR 0034), and the services keeping images in it.

func newRegistryCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "The image registry: storage, size, pruning and the services keeping images (admin)",
		Long: `Show the platform registry: where it keeps its images (this server's disk or
a storage connection's bucket), their size, how the last prune and migration
went, and every service with images in it, with how many its rollback
window keeps. Prune and migration state are since the daemon started.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			r, err := get[apitypes.Registry](cmd.Context(), s, apiPrefix+"/registry")
			if err != nil {
				return err
			}
			return g.io.Print(r, func() error { return printRegistry(g, r) })
		},
	}
	cmd.AddCommand(newRegistryPruneCmd(g), newRegistryMigrateCmd(g))
	return cmd
}

func printRegistry(g *globals, r apitypes.Registry) error {
	if !r.Enabled {
		g.io.Detail("This Dock has no registry; images stay on the server that built them.")
		return nil
	}
	prune := "not since the daemon started"
	if r.Pruning {
		prune = "running"
	} else if r.LastPruneAt != nil {
		prune = ago(*r.LastPruneAt) + ", " + plural(r.LastPruned, "image") + " deleted"
	}
	storage := "this server's disk"
	if r.Storage != "local" && r.Storage != "" {
		storage = "storage " + r.Storage
	}
	if err := g.io.Fields(
		"host", r.Host,
		"storage", storage,
		"stored", bytesize(r.StoredBytes),
		"rollback window", plural(r.KeepReleases, "release"),
		"last prune", prune,
		"error", r.LastPruneError,
	); err != nil {
		return err
	}
	if m := r.Migration; m != nil {
		switch {
		case m.Running:
			g.io.Warnf("Moving the images from %s to %s since %s ago; builds and pulls wait for it.", m.From, m.To, ago(m.StartedAt))
		case m.Error != "":
			g.io.Warnf("Moving the images from %s to %s failed: %s", m.From, m.To, m.Error)
		default:
			g.io.Detail(fmt.Sprintf("Moved the images from %s to %s: %d objects, %s.", m.From, m.To, m.Objects, bytesize(m.Bytes)))
		}
	}
	if r.CollectPending {
		g.io.Warnf("Deleted images' layers are still on disk; the next prune collects them.")
	}
	if len(r.Services) == 0 {
		g.io.Detail("No images pushed yet.")
		return nil
	}
	t := g.io.Table("service", "repository", "kept", "newest", "active")
	for _, s := range r.Services {
		kept := strconv.Itoa(s.Kept)
		if s.Archived {
			kept = "archived"
		}
		active := "-"
		if s.Active != 0 {
			active = "#" + itoa(s.Active)
		}
		t.Row(s.Project+"/"+s.Environment+"/"+s.Service, s.Repository, kept, "#"+itoa(s.Newest), active)
	}
	return t.Flush()
}

func newRegistryPruneCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "Delete the images no release keeps, now (admin)",
		Long: `Delete the registry's images outside every service's rollback window that
no active release uses, and collect their layers. It runs in the
background; the registry stops for the seconds the collection takes.
Dock also prunes by itself, at most hourly, after deploys and archives.
Refused while builds or pulls use the registry.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			r, err := send[apitypes.Registry](cmd.Context(), s, http.MethodPost, apiPrefix+"/registry/prune", nil)
			if err != nil {
				return err
			}
			return g.io.Print(r, func() error {
				g.io.Successf("Pruning the registry")
				g.io.Detail("It finishes in the background; `dock registry` shows the result.")
				return nil
			})
		},
	}
}

// newRegistryMigrateCmd is `dock registry migrate`.
func newRegistryMigrateCmd(g *globals) *cobra.Command {
	var req apitypes.MigrateRegistryRequest
	var yes bool
	cmd := &cobra.Command{
		Use:   "migrate --to s3|local [--storage <name>]",
		Short: "Move the registry's images to a bucket or back to this server's disk (admin)",
		Long: `Move the registry's images to a storage connection's bucket (--to s3) or to
the control plane's disk (--to local). The registry serves them read-only
while they are copied; builds and pulls wait, like during a prune. Once the
copy is checked, the registry switches over and the old copy is deleted. A
failure before the switch leaves everything as it was.

The bucket must not hold registry data yet (dock-registry/ in it). --storage
may be left out when there is only one storage connection. It runs in the
background; ` + "`dock registry`" + ` shows how it went.`,
		Example: `  dock storage add hetzner ...      # once
  dock registry migrate --to s3 --storage hetzner
  dock registry migrate --to local`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if req.To != "s3" && req.To != "local" {
				return usageError("--to must be s3 or local", "Run `dock registry migrate --help`.")
			}
			if req.To == "local" && req.Storage != "" {
				return usageError("--storage goes with --to s3", "Leave out --storage to move the images to this server's disk.")
			}
			if !yes {
				if err := g.io.Confirm("Move the registry's images? Builds and pulls wait until the copy is done.", "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			r, err := send[apitypes.Registry](cmd.Context(), s, http.MethodPost, apiPrefix+"/registry/migrate", req)
			if err != nil {
				return err
			}
			return g.io.Print(r, func() error {
				to := req.To
				if r.Migration != nil {
					to = r.Migration.To
				}
				g.io.Successf("Moving the registry's images to %s", to)
				g.io.Detail("It finishes in the background; `dock registry` shows the result.")
				return nil
			})
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&req.To, "to", "", "s3 (a storage connection) or local (this server's disk)")
	fs.StringVar(&req.Storage, "storage", "", "the storage connection, with --to s3")
	fs.BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}
