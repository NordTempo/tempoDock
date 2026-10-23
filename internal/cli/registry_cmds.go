package cli

import (
	"net/http"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The platform registry (ADR 0014 section 4): its size, pruning, and the
// services keeping images in it.

func newRegistryCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "The image registry: size, pruning and the services keeping images (admin)",
		Long: `Show the platform registry: its size on disk, how the last prune went, and
every service with images in it, with how many its rollback window keeps.
Prune state is since the daemon started.`,
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
	cmd.AddCommand(newRegistryPruneCmd(g))
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
	if err := g.io.Fields(
		"host", r.Host,
		"stored", bytesize(r.StoredBytes),
		"rollback window", plural(r.KeepReleases, "release"),
		"last prune", prune,
		"error", r.LastPruneError,
	); err != nil {
		return err
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
