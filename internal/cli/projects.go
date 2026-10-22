package cli

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

func newProjectCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "project", Short: "List, create and archive projects"})
	var all bool
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List projects",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			path := apiPrefix + "/projects"
			if all {
				path += "?archived=true"
			}
			projects, err := get[[]apitypes.Project](cmd.Context(), s, path)
			if err != nil {
				return err
			}
			return g.io.Print(projects, func() error {
				t := g.io.Table("name", "created")
				for _, p := range projects {
					created := ago(p.CreatedAt)
					if p.ArchivedAt != nil {
						created += ", archived " + ago(*p.ArchivedAt)
					}
					t.Row(p.Name, created)
				}
				return t.Flush()
			})
		},
	}
	list.Flags().BoolVar(&all, "all", false, "include archived projects")
	cmd.AddCommand(
		list,
		newProjectArchiveCmd(g),
		newProjectPinCmd(g),
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

// newProjectArchiveCmd archives a project. The name is required, never
// taken from the directory's link.
func newProjectArchiveCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "archive <name>",
		Short: "Archive a project and all its services, keeping volumes and history",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			t := target{Project: args[0]}
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Archive project %s? Every service in it stops and its domains stop routing.", t.Project), "yes"); err != nil {
					return err
				}
			}
			p, err := send[apitypes.Project](cmd.Context(), s, http.MethodPost, t.projectPath()+"/archive", nil)
			if err != nil {
				return err
			}
			return g.io.Print(p, func() error {
				g.io.Successf("Archived project %s. Its containers are stopping; volumes and release history are kept.", p.Name)
				g.io.Progressf("It is hidden from `dock project list`; see it with --all.")
				for _, n := range p.ArchiveNotes {
					g.io.Warnf("%s", n)
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
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
				tb := g.io.Table("name", "kind", "server", "created")
				for _, e := range envs {
					tb.Row(e.Name, kindWords(e.Kind), e.Server, ago(e.CreatedAt))
				}
				return tb.Flush()
			})
		},
	}
	listFlags.add(list, needProject)
	var kind string
	create := &cobra.Command{
		Use:   "create <name> [--kind production|non-production]",
		Short: "Create an environment; it is placed on a server automatically",
		Long: `Create an environment. Its kind (production or non-production) never
changes: an environment named production is production unless --kind says
otherwise, every other one non-production. It goes to the server the
project's environments of that kind are on; the first of a kind goes to the
eligible server with the most free memory.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := parseKind(kind)
			if err != nil {
				return err
			}
			s, t, err := g.open(createFlags, needProject)
			if err != nil {
				return err
			}
			e, err := send[apitypes.Environment](cmd.Context(), s, http.MethodPost, t.projectPath()+"/environments", apitypes.CreateEnvironmentRequest{Name: args[0], Kind: k})
			if err != nil {
				return err
			}
			return g.io.Print(e, func() error {
				g.io.Successf("Created %s environment %s/%s on %s", kindWords(e.Kind), e.Project, e.Name, e.Server)
				return nil
			})
		},
	}
	createFlags.add(create, needProject)
	create.Flags().StringVar(&kind, "kind", "", "production or non-production (default: from the name)")
	cmd.AddCommand(list, create)
	return cmd
}
