package cli

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Where a server's disk goes, and pruning it on demand (ADR 0035).

// diskGroupNames are the console's words for the disk groups.
var diskGroupNames = map[apitypes.DiskGroup]string{
	apitypes.DiskApps: "app data", apitypes.DiskImages: "images kept", apitypes.DiskReclaimable: "reclaimable",
	apitypes.DiskBuild: "build cache", apitypes.DiskDock: "dock", apitypes.DiskOther: "other",
}

// newServerDiskCmd is `dock server disk`.
func newServerDiskCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "disk <name>",
		Short: "Show where a server's disk goes, and what a prune frees (admin)",
		Long: `Show the filesystem of Docker's data root split into parts: services'
volumes, images (running, for rollbacks, shared layers, not Dock's), images
a prune removes, BuildKit's cache, Dock's own data and the rest. Docker sizes
every image and volume for it, which takes a few seconds.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			d, err := get[apitypes.ServerDisk](cmd.Context(), s, apiPrefix+"/servers/"+url.PathEscape(args[0])+"/disk")
			if err != nil {
				return err
			}
			return g.io.Print(d, func() error {
				_, _ = fmt.Fprintf(g.io.Out, "%s used of %s, %s reclaimable\n\n", bytesize(d.UsedBytes), bytesize(d.TotalBytes), bytesize(d.ReclaimableBytes))
				t := g.io.Table("group", "part", "disk", "of used", "reclaimable", "detail")
				for _, p := range d.Parts {
					share := "-"
					if d.UsedBytes > 0 {
						share = fmt.Sprintf("%.0f%%", 100*float64(p.Bytes)/float64(d.UsedBytes))
					}
					free := "-"
					if p.Reclaimable > 0 {
						free = bytesize(p.Reclaimable)
					}
					t.Row(diskGroupNames[p.Group], p.Name, bytesize(p.Bytes), share, free, dash(p.Detail))
				}
				if err := t.Flush(); err != nil {
					return err
				}
				if d.ReclaimableBytes > 0 {
					g.io.Detail("Free it with `dock server prune " + d.Server + "`.")
				}
				return nil
			})
		},
	}
}

// newServerPruneCmd is `dock server prune`.
func newServerPruneCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "prune <name>",
		Short: "Free disk on a server: unused images and excess build cache (admin)",
		Long: `Remove the images Dock made that no release needs and, on a builder,
BuildKit's cache above [retention] build_cache_keep_mb. Dock does this by
itself once the disk passes prune_disk_percent; this does it now. Running
apps, rollback images and volumes are never touched.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Prune server %s? Unused images and build cache above what is kept are removed.", args[0]), "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			r, err := send[apitypes.ServerPruneResult](cmd.Context(), s, http.MethodPost, apiPrefix+"/servers/"+url.PathEscape(args[0])+"/prune", nil)
			if err != nil {
				return err
			}
			return g.io.Print(r, func() error {
				g.io.Successf("Freed %s on %s: %s in %s, %s of build cache", bytesize(r.ImageBytes+r.CacheBytes), r.Server,
					bytesize(r.ImageBytes), plural(r.ImagesRemoved, "image"), bytesize(r.CacheBytes))
				if r.Error != "" {
					g.io.Warnf("Part of the prune failed: %s", r.Error)
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}
