package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// newEnvironmentCloneCmd is `dock environment clone <from> <to>` (ADR
// 0022), also registered as `dock env clone`.
func newEnvironmentCloneCmd(g *globals) *cobra.Command {
	var f targetFlags
	var kind string
	var deploy, detach bool
	cmd := &cobra.Command{
		Use:   "clone <from> <to> [--kind production|non-production] [--deploy]",
		Short: "Create an environment as a copy of another one",
		Long: `Create environment <to> with <from>'s services, settings, variables and
volumes (new and empty), as they are live in <from>. It is placed like
` + "`dock environment create`" + `: its kind comes from --kind or the name, and it goes to
the server the project's environments of that kind are on.

Variable values are copied only if you may read them (admin). Otherwise
values that are only references (${{postgres.DATABASE_URL}}) are copied,
database templates get new credentials, and the rest are listed so you can
set them. Domains, git connections, releases, backups and staged changes
are never copied.

Nothing is deployed unless --deploy, which deploys the images serving in
<from> like ` + "`dock promote`" + ` and follows them unless --detach. Needs read on
<from> and deploy on the project.`,
		Example: `  dock environment clone staging staging-2
  dock environment clone production staging --deploy`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := parseKind(kind)
			if err != nil {
				return err
			}
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			from := target{Project: t.Project, Environment: args[0]}
			res, err := send[apitypes.CloneResult](ctx, s, http.MethodPost, from.envPath()+"/clone", apitypes.CloneEnvironmentRequest{
				Name: args[1], Kind: k, Deploy: deploy,
			})
			if err != nil {
				return err
			}
			if res.Deploy != nil && len(res.Deploy.Promoted) > 0 && !detach {
				// The environment exists only now; the watch misses the
				// first lines, and follow fetches the releases anyway.
				w, err := g.watch(ctx, s, target{Project: t.Project, Environment: res.Environment.Name})
				if err != nil {
					return err
				}
				defer w.close()
				rels := make([]apitypes.Release, len(res.Deploy.Promoted))
				for i, p := range res.Deploy.Promoted {
					rels[i] = p.Release
				}
				rels, err = w.follow(ctx, rels)
				if err != nil {
					return interrupted(err, rels)
				}
				for i := range res.Deploy.Promoted {
					res.Deploy.Promoted[i].Release = rels[i]
				}
			}
			if err := g.io.Print(res, func() error { g.printClone(res); return nil }); err != nil {
				return err
			}
			if res.Deploy != nil {
				for _, p := range res.Deploy.Promoted {
					if err := releaseFailure(ctx, s, p.Release); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
	f.add(cmd, needProject)
	cmd.Flags().StringVar(&kind, "kind", "", "production or non-production (default: from the name)")
	cmd.Flags().BoolVar(&deploy, "deploy", false, "also deploy the images serving in <from>, by digest, like promote")
	addDetachFlag(cmd, &detach)
	return cmd
}

// printClone is a clone's human output: what was created, what needs
// doing, and what was left out.
func (g *globals) printClone(res apitypes.CloneResult) {
	e := res.Environment
	g.io.Successf("Cloned %s into %s environment %s/%s on %s: %s, %s, %s",
		res.From, kindWords(e.Kind), e.Project, e.Name, e.Server,
		plural(len(res.Services), "service"), plural(res.Variables, "variable"), plural(res.Volumes, "new volume"))
	if len(res.Generated) > 0 {
		g.io.Progressf("Set anew (copying needs admin): %s", variableNames(res.Generated))
	}
	if len(res.Skipped) > 0 {
		g.io.Warnf("Not copied, copying their values needs admin: %s", variableNames(res.Skipped))
		g.io.Progressf("Set them with `dock env set KEY=value -e %s -s <service>` (--shared for shared ones).", e.Name)
	}
	g.io.Progressf("Not copied: %s.", strings.Join(res.NotCopied, ", "))
	switch {
	case res.DeployError != "":
		g.io.Warnf("Nothing deployed: %s", res.DeployError)
	case res.Deploy == nil:
		g.io.Progressf("Nothing is deployed. Deploy the same images with `dock promote %s %s`.", res.From, e.Name)
	default:
		for _, p := range res.Deploy.Promoted {
			r := p.Release
			switch r.Status {
			case apitypes.ReleaseActive:
				g.io.Successf("%s: release %d (from %s release %d) is live", p.Service, r.Number, res.From, p.FromRelease)
			case apitypes.ReleaseFailed:
				g.io.Failf("%s: release %d failed: %s", p.Service, r.Number, r.Error)
			default:
				g.io.Successf("%s: release %d (from %s release %d) queued", p.Service, r.Number, res.From, p.FromRelease)
			}
		}
		for _, sk := range res.Deploy.Skipped {
			g.io.Warnf("%s not deployed: %s", sk.Service, sk.Message)
		}
	}
}

// variableNames lists variables as references name them.
func variableNames(names []apitypes.VariableRef) string {
	s := make([]string, len(names))
	for i, n := range names {
		s[i] = n.String()
	}
	return strings.Join(s, ", ")
}

// newProjectExportCmd is `dock project export` (ADR 0022): the project's
// config as TOML (or JSON), for review, backup and diffs in git.
func newProjectExportCmd(g *globals) *cobra.Command {
	var f targetFlags
	var format string
	cmd := &cobra.Command{
		Use:   "export [<name>] [--format toml|json]",
		Short: "Print a project's config: environments, services, settings, variable names",
		Long: `Print a project's configuration as it is live: its environments, their
services and settings, volumes, domains, git connections and the names of
its variables. Variable values are never included, nor are staged changes,
releases or archived services. The output is sorted by name, so it diffs
well in git.

TOML by default; --format json (or --json) prints the API's JSON.`,
		Example: `  dock project export shop > shop.dock.toml
  dock project export --format json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "toml" && format != "json" {
				return usageError(fmt.Sprintf("unknown format %q", format), "Use toml or json.")
			}
			if len(args) == 1 {
				f.project = args[0]
			}
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			ex, err := get[apitypes.ProjectExport](cmd.Context(), s, t.projectPath()+"/export")
			if err != nil {
				return err
			}
			if format == "json" || g.io.JSON {
				jsonIO := *g.io
				jsonIO.JSON = true
				return jsonIO.Print(ex, nil)
			}
			b, err := exportTOML(ex)
			if err != nil {
				return err
			}
			_, err = g.io.Out.Write(b)
			return err
		},
	}
	f.add(cmd, needProject)
	cmd.Flags().StringVar(&format, "format", "toml", "toml or json")
	return cmd
}

// exportTOML renders an export as TOML with a header saying what it is.
func exportTOML(ex apitypes.ProjectExport) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Tempo Dock project %s, exported with `dock project export`.\n", ex.Project)
	b.WriteString("# Settings as they are live. Variables are names only: values are never exported.\n\n")
	enc := toml.NewEncoder(&b)
	enc.SetIndentTables(true)
	if err := enc.Encode(ex); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
