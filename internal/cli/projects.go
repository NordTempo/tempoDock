package cli

import (
	"net/http"

	"github.com/spf13/cobra"

	"github.com/NordTempo/platform/internal/apitypes"
)

func newProjectCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "project", Short: "List and create projects"})
	cmd.AddCommand(
		&cobra.Command{
			Use:     "list",
			Aliases: []string{"ls"},
			Short:   "List projects",
			Args:    cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				s, err := g.connect()
				if err != nil {
					return err
				}
				projects, err := get[[]apitypes.Project](cmd.Context(), s, apiPrefix+"/projects")
				if err != nil {
					return err
				}
				return g.io.Print(projects, func() error {
					t := g.io.Table("name", "created")
					for _, p := range projects {
						t.Row(p.Name, ago(p.CreatedAt))
					}
					return t.Flush()
				})
			},
		},
		&cobra.Command{
			Use:   "create <name>",
			Short: "Create a project (with a production environment)",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				s, err := g.connect()
				if err != nil {
					return err
				}
				p, err := send[apitypes.Project](cmd.Context(), s, http.MethodPost, apiPrefix+"/projects", apitypes.CreateProjectRequest{Name: args[0]})
				if err != nil {
					return err
				}
				return g.io.Print(p, func() error {
					g.io.Successf("Created project %s with environment production", p.Name)
					g.io.Progressf("Link a directory to it with `dock link -p %s`.", p.Name)
					return nil
				})
			},
		},
	)
	return cmd
}

func newEnvironmentCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "environment",
		Short: "List and create environments",
		Long:  "Environments of a project. (`dock env` manages variables.)",
	})
	var listFlags, createFlags targetFlags
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List a project's environments and the servers they run on",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(listFlags, needProject)
			if err != nil {
				return err
			}
			envs, err := get[[]apitypes.Environment](cmd.Context(), s, t.projectPath()+"/environments")
			if err != nil {
				return err
			}
			return g.io.Print(envs, func() error {
				tb := g.io.Table("name", "server", "created")
				for _, e := range envs {
					tb.Row(e.Name, e.Server, ago(e.CreatedAt))
				}
				return tb.Flush()
			})
		},
	}
	listFlags.add(list, needProject)
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an environment; it is placed on a server automatically",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(createFlags, needProject)
			if err != nil {
				return err
			}
			e, err := send[apitypes.Environment](cmd.Context(), s, http.MethodPost, t.projectPath()+"/environments", apitypes.CreateEnvironmentRequest{Name: args[0]})
			if err != nil {
				return err
			}
			return g.io.Print(e, func() error {
				g.io.Successf("Created environment %s/%s on %s", e.Project, e.Name, e.Server)
				return nil
			})
		},
	}
	createFlags.add(create, needProject)
	cmd.AddCommand(list, create)
	return cmd
}
