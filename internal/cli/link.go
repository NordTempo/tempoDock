package cli

import (
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/platform/internal/cli/output"
	"github.com/NordTempo/platform/internal/clientconfig"
)

// linkView is the JSON shape of a directory link.
type linkView struct {
	Path string `json:"path"`
	clientconfig.Link
}

// describe renders a link as "project / environment / service".
func (v linkView) describe() string {
	parts := []string{v.Project.Name, v.Environment.Name}
	if v.Service.Name != "" {
		parts = append(parts, v.Service.Name)
	}
	return strings.Join(parts, " / ")
}

func newUnlinkCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "unlink",
		Short: "Forget what this directory is linked to",
		Long: `Remove the link that applies to the current directory: its own, or the
nearest linked parent directory's. Nothing changes on the server.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			dir, l, ok := cfg.Unlink(wd)
			if !ok {
				return &output.Error{
					Exit:    output.ExitNotFound,
					Message: "this directory is not linked",
					Hint:    "Nothing to do. `platform link` links a directory to a project.",
				}
			}
			if err := cfg.save(); err != nil {
				return err
			}
			v := linkView{dir, l}
			return g.io.Print(v, func() error {
				g.io.Successf("Unlinked %s from %s", dir, v.describe())
				return nil
			})
		},
	}
}
