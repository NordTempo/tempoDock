package cli

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

func newProjectCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "project", Short: "List, create, rename, export and archive projects"})
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
		newProjectRenameCmd(g),
		newProjectPinCmd(g),
		newProjectExportCmd(g),
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
		Use:               "archive <name>",
		Short:             "Archive a project and all its services, keeping volumes and history",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeProjectArg,
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
		Short: "List, create, clone, lock and unlock environments, and preview pull requests",
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
				// Preview environments name their pull request; the column
				// shows only when there is one.
				// The server column is for platform operators, the only ones
				// the API names servers to.
				previews := slices.ContainsFunc(envs, func(e apitypes.Environment) bool { return e.Preview != nil })
				servers := slices.ContainsFunc(envs, func(e apitypes.Environment) bool { return e.Server != "" })
				headers := []string{"name", "kind"}
				if servers {
					headers = append(headers, "server")
				}
				headers = append(headers, "lock", "created")
				if previews {
					headers = append(headers, "pull request")
				}
				tb := g.io.Table(headers...)
				for _, e := range envs {
					row := []string{e.Name, kindWords(e.Kind)}
					if servers {
						row = append(row, e.Server)
					}
					row = append(row, cmp.Or(lockWords(e.Lock), "-"), ago(e.CreatedAt))
					if previews {
						row = append(row, pullRequestText(e.Preview))
					}
					tb.Row(row...)
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
				g.io.Successf("Created %s environment %s/%s%s", kindWords(e.Kind), e.Project, e.Name, onServer(e.Server))
				return nil
			})
		},
	}
	createFlags.add(create, needProject)
	create.Flags().StringVar(&kind, "kind", "", "production or non-production (default: from the name)")
	cmd.AddCommand(list, create, newEnvironmentCloneCmd(g), newEnvironmentLockCmd(g), newEnvironmentUnlockCmd(g), newPreviewsCmd(g), newEnvironmentMoveCmd(g))
	return cmd
}

// newProjectRenameCmd renames an explicit project without changing its identity.
func newProjectRenameCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use: "rename <current> <new>", Short: "Rename a project and its address, keeping services and domains unchanged",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeProjectArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			t := target{Project: args[0]}
			current, err := get[apitypes.Project](cmd.Context(), s, t.projectPath())
			if err != nil {
				return err
			}
			p, err := send[apitypes.Project](cmd.Context(), s, http.MethodPatch, t.projectPath(), apitypes.RenameProjectRequest{Name: args[1], ID: current.ID})
			if err != nil {
				return err
			}
			return g.io.Print(p, func() error {
				g.io.Successf("Renamed project %s to %s", args[0], p.Name)
				g.io.Progressf("Update old project links and explicit --project flags. Linked directories follow the same project; services and domains are unchanged.")
				return nil
			})
		},
	}
}
