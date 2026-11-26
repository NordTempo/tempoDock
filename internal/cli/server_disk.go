package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

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
		Short: "Show where a server's disk goes, and what each prune frees (admin)",
		Long: `Show the filesystem of Docker's data root split into parts: services'
volumes, images (running, for rollbacks, shared layers, not Dock's), images
a prune removes, BuildKit's cache, Dock's own data and the rest, then what
each prune (` + "`dock server prune --only`" + `) would free. Docker sizes every
image and volume for it, which takes a few seconds.`,
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
				if len(d.Prunes) == 0 {
					return nil
				}
				_, _ = fmt.Fprintln(g.io.Out)
				p := g.io.Table("prune", "frees", "what")
				for _, x := range d.Prunes {
					name := string(x.Target)
					if x.Safe {
						name += " *"
					}
					p.Row(name, bytesize(x.Bytes), pruneTargetNames[x.Target])
				}
				if err := p.Flush(); err != nil {
					return err
				}
				g.io.Detail("* safe: `dock server prune " + d.Server + "` frees these; --only picks any.")
				return nil
			})
		},
	}
}

// pruneTargetNames are the CLI's and console's words for prune targets:
// what each frees, and what it costs later for the unsafe ones.
var pruneTargetNames = map[apitypes.PruneTarget]string{
	apitypes.PruneImages:    "images nothing needs",
	apitypes.PruneScanner:   "scanner leftovers",
	apitypes.PruneCache:     "build cache above what is kept",
	apitypes.PruneRollbacks: "older rollbacks (pulled again from the registry to roll back that far)",
	apitypes.PruneAllCache:  "all build cache (the next builds start cold)",
	apitypes.PruneScanDB:    "the vulnerability database (downloaded again before the next scan)",
}

// newServerPruneCmd is `dock server prune`.
func newServerPruneCmd(g *globals) *cobra.Command {
	var yes bool
	var only []string
	cmd := &cobra.Command{
		Use:   "prune <name>",
		Short: "Free disk on a server: all that nothing needs, or one part (admin)",
		Long: `Free disk on a server (ADR 0035 amendment 2). Without --only it runs the
safe prunes, which Dock also runs by itself once the disk passes
[retention] prune_disk_percent:

  images     images of archived services, out of rollback windows, and
             other versions of Dock's own images
  scanner    what cut-off downloads left in the vulnerability database
  cache      BuildKit's cache above [retention] build_cache_keep_mb

--only picks any of those, and these, which each cost something later:

  rollbacks  rollback images the registry has too, but the active
             release's and the one before: rolling back further pulls them
  all-cache  all of BuildKit's cache: the next builds start cold
  scan-db    the whole vulnerability database: downloaded again before
             the next scan

Running apps and volumes are never touched. ` + "`dock server disk`" + ` shows what each frees.`,
		Example: `  dock server prune prod-1
  dock server prune prod-1 --only scanner
  dock server prune prod-1 --only rollbacks,all-cache`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targets := make([]apitypes.PruneTarget, 0, len(only))
			for _, o := range only {
				t := apitypes.PruneTarget(o)
				if _, ok := pruneTargetNames[t]; !ok {
					return fmt.Errorf("--only %q: pick from images, scanner, cache, rollbacks, all-cache, scan-db", o)
				}
				targets = append(targets, t)
			}
			if !yes {
				what := "everything nothing needs: unused images, scanner leftovers and build cache above what is kept"
				if len(targets) > 0 {
					names := make([]string, len(targets))
					for i, t := range targets {
						names[i] = pruneTargetNames[t]
					}
					what = strings.Join(names, ", ")
				}
				if err := g.io.Confirm(fmt.Sprintf("Prune server %s? This removes %s.", args[0], what), "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			r, err := send[apitypes.ServerPruneResult](cmd.Context(), s, http.MethodPost, apiPrefix+"/servers/"+url.PathEscape(args[0])+"/prune",
				apitypes.ServerPruneRequest{Targets: targets})
			if err != nil {
				return err
			}
			return g.io.Print(r, func() error {
				var total int64
				for _, f := range r.Freed {
					total += f.Bytes
				}
				g.io.Successf("Freed %s on %s", bytesize(total), r.Server)
				t := g.io.Table("prune", "freed")
				for _, f := range r.Freed {
					t.Row(string(f.Target), bytesize(f.Bytes))
				}
				if err := t.Flush(); err != nil {
					return err
				}
				if r.Error != "" {
					g.io.Warnf("Part of the prune failed: %s", r.Error)
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	cmd.Flags().StringSliceVar(&only, "only", nil, "prune only these: images, scanner, cache, rollbacks, all-cache, scan-db (comma-separated)")
	return cmd
}
