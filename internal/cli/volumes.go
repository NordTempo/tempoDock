package cli

import (
	"net/http"

	"github.com/spf13/cobra"

	"github.com/NordTempo/platform/internal/apitypes"
)

func newVolumeCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "volume",
		Short: "Manage a service's persistent volumes",
		Long: `Volumes keep data across releases. A service with a volume is stateful: its
deploys stop the old container before starting the new one. Volumes are
never deleted as a side effect of anything else.`,
	})

	var listFlags targetFlags
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List volumes",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(listFlags, needService)
			if err != nil {
				return err
			}
			vols, err := get[[]apitypes.Volume](cmd.Context(), s, t.servicePath()+"/volumes")
			if err != nil {
				return err
			}
			return g.io.Print(vols, func() error {
				if len(vols) == 0 {
					g.io.Successf("%s has no volumes", t)
					return nil
				}
				tb := g.io.Table("name", "path", "status", "docker volume")
				for _, v := range vols {
					status := "mounted"
					if v.Pending {
						status = "staged"
					}
					tb.Row(v.Name, v.MountPath, status, v.DockerName)
				}
				return tb.Flush()
			})
		},
	}
	listFlags.add(list, needService)

	var addFlags targetFlags
	var deploy, detach bool
	add := &cobra.Command{
		Use:   "add <name> <path>",
		Short: "Stage a volume mounted at an absolute path in the container",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(addFlags, needService)
			if err != nil {
				return err
			}
			req := apitypes.AddVolumeRequest{Name: args[0], MountPath: args[1], Apply: deploy}
			o := stageOpts{what: "Added volume " + args[0] + " at " + args[1] + " to " + t.Service, deploy: deploy, detach: detach}
			return g.stage(cmd.Context(), s, t, o, func() (apitypes.ChangeResult, error) {
				return send[apitypes.ChangeResult](cmd.Context(), s, http.MethodPost, t.servicePath()+"/volumes", req)
			})
		},
	}
	addFlags.add(add, needService)
	addDeployFlags(add, &deploy, &detach)

	cmd.AddCommand(list, add)
	return cmd
}
