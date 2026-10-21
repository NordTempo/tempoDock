package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/platform/internal/apitypes"
	"github.com/NordTempo/platform/internal/cli/output"
	"github.com/NordTempo/platform/internal/clientconfig"
)

func newLinkCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Link this directory to a project, environment and service",
		Long: `Remember which project, environment and (optionally) service this directory
deploys to, so other commands need no flags. The link is stored in the CLI
config, not in the repo, and applies to subdirectories too.

On a terminal, missing choices are picked from a list. Otherwise pass
--project (and --environment when the project has several); --service is
optional and links the whole environment when left out.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			// Re-linking keeps what's already linked: `dock link -s api` in a
			// linked directory only adds the service.
			if s.link != nil && s.link.Context == s.server.Name && f.project == "" {
				f.project = s.link.Project.Name
				if f.environment == "" {
					f.environment = s.link.Environment.Name
				}
			}
			l, err := g.pickLink(cmd.Context(), s, f)
			if err != nil {
				return err
			}
			if err := s.cfg.SetLink(wd, l); err != nil {
				return err
			}
			if err := s.cfg.save(); err != nil {
				return err
			}
			v := linkView{wd, l}
			return g.io.Print(v, func() error {
				g.io.Successf("Linked %s to %s", wd, v.describe())
				return nil
			})
		},
	}
	f.add(cmd, needService)
	return cmd
}

// pickLink checks the names given as flags against the server and asks for
// the missing ones on a terminal.
func (g *globals) pickLink(ctx context.Context, s *session, f targetFlags) (clientconfig.Link, error) {
	l := clientconfig.Link{Context: s.server.Name}
	t := target{Project: f.project}
	if t.Project == "" {
		projects, err := get[[]apitypes.Project](ctx, s, apiPrefix+"/projects")
		if err != nil {
			return l, err
		}
		if len(projects) == 0 {
			return l, &output.Error{Exit: output.ExitNotFound, Message: "there are no projects yet", Hint: "Create one with `dock project create <name>`."}
		}
		if t.Project, err = g.io.Choose("Project", "project", names(projects, func(p apitypes.Project) string { return p.Name })); err != nil {
			return l, err
		}
	}
	p, err := get[apitypes.Project](ctx, s, t.projectPath())
	if err != nil {
		return l, err
	}
	l.Project = clientconfig.Ref{ID: itoa(p.ID), Name: p.Name}

	envs, err := get[[]apitypes.Environment](ctx, s, t.projectPath()+"/environments")
	if err != nil {
		return l, err
	}
	t.Environment = f.environment
	switch {
	case t.Environment != "":
	case len(envs) == 1:
		t.Environment = envs[0].Name
	default:
		if t.Environment, err = g.io.Choose("Environment", "environment", names(envs, func(e apitypes.Environment) string { return e.Name })); err != nil {
			return l, err
		}
	}
	env, err := get[apitypes.Environment](ctx, s, t.envPath())
	if err != nil {
		return l, err
	}
	l.Environment = clientconfig.Ref{ID: itoa(env.ID), Name: env.Name}

	t.Service = f.service
	if t.Service == "" && g.io.Interactive {
		svcs, err := get[[]apitypes.Service](ctx, s, t.envPath()+"/services")
		if err != nil {
			return l, err
		}
		const none = "(none: link the whole environment)"
		options := names(live(svcs), func(s apitypes.Service) string { return s.Name })
		if len(options) > 0 {
			if t.Service, err = g.io.Choose("Service", "service", append(options, none)); err != nil {
				return l, err
			}
		}
		if t.Service == none {
			t.Service = ""
		}
	}
	if t.Service != "" {
		svc, err := get[apitypes.Service](ctx, s, t.servicePath())
		if output.ExitCodeOf(err) == output.ExitNotFound {
			return l, &output.Error{
				Exit: output.ExitNotFound, Err: err,
				Hint: fmt.Sprintf("Create it with `dock service create %s -p %s -e %s`, or deploy with `dock deploy -p %s -e %s -s %s`.", t.Service, t.Project, t.Environment, t.Project, t.Environment, t.Service),
			}
		}
		if err != nil {
			return l, err
		}
		l.Service = clientconfig.Ref{ID: itoa(svc.ID), Name: svc.Name}
	}
	return l, nil
}

// names maps items to their names.
func names[T any](items []T, name func(T) string) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = name(it)
	}
	return out
}

// live drops archived services.
func live(svcs []apitypes.Service) []apitypes.Service {
	out := make([]apitypes.Service, 0, len(svcs))
	for _, s := range svcs {
		if s.ArchivedAt == nil {
			out = append(out, s)
		}
	}
	return out
}

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
					Hint:    "Nothing to do. `dock link` links a directory to a project.",
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
