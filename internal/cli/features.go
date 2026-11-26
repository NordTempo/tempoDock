package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// featureNames are the CLI's and console's words for the features, and
// the commands that switch them.
var featureNames = map[apitypes.FeatureKey]struct{ name, cmd string }{
	apitypes.FeatureScanning:  {"vulnerability scanning", "dock scanning on|off [--remove-database]"},
	apitypes.FeatureAnalytics: {"analytics", "dock analytics on|off [--remove-data]"},
	apitypes.FeatureTracing:   {"tracing", "dock tracing on|off"},
}

// newFeaturesCmd is `dock features`: the optional features and what they
// take (ADR 0101), the console's Features page.
func newFeaturesCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "features",
		Short: "The optional features, on or off, and the memory and disk each takes (operators)",
		Long: `List vulnerability scanning, Dock Analytics and tracing: whether each is on,
the servers it runs on, its memory now and at most, and its data on disk
(ADR 0101). Memory is the last minute's sample; disk is each server's last
disk reading, read in the background when there is none yet. Each line
names the command that switches it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			fs, err := get[apitypes.Features](cmd.Context(), s, apiPrefix+"/features")
			if err != nil {
				return err
			}
			return g.io.Print(fs, func() error {
				t := g.io.Table("feature", "state", "runs on", "memory", "disk", "switch")
				for _, f := range fs.Items {
					state := "off"
					if f.Enabled {
						state = "on"
					}
					mem := "up to " + bytesize(f.MemoryLimitBytes)
					if f.MemoryBytes != nil {
						mem = fmt.Sprintf("%s of %s", bytesize(*f.MemoryBytes), bytesize(f.MemoryLimitBytes))
					}
					disk := "-"
					if f.DiskBytes != nil {
						disk = bytesize(*f.DiskBytes)
					}
					n := featureNames[f.Key]
					t.Row(n.name, state, dash(strings.Join(f.Servers, ", ")), mem, disk, n.cmd)
				}
				return t.Flush()
			})
		},
	}
}
