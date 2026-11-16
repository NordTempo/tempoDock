package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/projectfile"
)

// Project files (ADR 0049): `dock apply FILE` plans a file against the
// live state or stages it; `dock apply` without a file applies staged
// changes (changes.go).

// applyFileOpts are `dock apply FILE`'s flags.
type applyFileOpts struct {
	plan, prune, deploy, detach bool
	// environment limits the file to one environment (-e given explicitly).
	environment string
}

// readProjectFile reads a project file from path, or stdin for "-", and
// parses it to fail early and find its project.
func readProjectFile(path string) (string, apitypes.ProjectExport, error) {
	r := io.Reader(os.Stdin)
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return "", apitypes.ProjectExport{}, &output.Error{Exit: output.ExitUsage, Message: err.Error(), Hint: "Write one with `dock project export > dock.project.toml`."}
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, projectfile.MaxSize+1))
	if err != nil {
		return "", apitypes.ProjectExport{}, err
	}
	pf, err := projectfile.Parse(b)
	if err != nil {
		return "", pf, &output.Error{Exit: output.ExitUsage, Message: err.Error(), Hint: "See docs/project-file.md for the format."}
	}
	return string(b), pf, nil
}

// applyFile runs `dock apply FILE`: the plan with --plan, otherwise staging
// (and with --deploy applying, following the releases unless --detach).
func (g *globals) applyFile(ctx context.Context, f targetFlags, path string, o applyFileOpts) error {
	content, pf, err := readProjectFile(path)
	if err != nil {
		return err
	}
	if f.project == "" {
		f.project = pf.Project
	}
	s, t, err := g.open(f, needProject)
	if err != nil {
		return err
	}
	req := apitypes.ApplyRequest{File: content, Environment: o.environment, Prune: o.prune}
	base := t.projectPath()
	if o.plan {
		plan, err := send[apitypes.ApplyPlan](ctx, s, http.MethodPost, base+"/plan", req)
		if err != nil {
			return err
		}
		return g.io.Print(plan, func() error {
			printPlan(g, plan)
			if n := len(plan.Changes); n > 0 {
				prune := ""
				if o.prune {
					prune = " --prune"
				}
				g.io.Progressf("Stage %s with `dock apply %s%s`; nothing goes live until `dock apply -e <environment>`.", plural(n, "change"), path, prune)
			}
			return nil
		})
	}
	res, err := send[apitypes.ApplyResult](ctx, s, http.MethodPost, base+"/apply", req)
	if err != nil {
		return err
	}
	if !o.deploy {
		return g.io.Print(res, func() error {
			printPlan(g, res.Plan)
			for _, p := range res.Staged {
				g.io.Successf("%s staged in %s/%s", plural(p.Count, "change"), p.Project, p.Environment)
			}
			g.io.Progressf("Review with `dock changes -e <environment>`, apply with `dock apply -e <environment>`, or drop with `dock discard`.")
			return nil
		})
	}
	// Staged and the environments exist: watch each, then ask again with
	// deploy (staging the same file twice changes nothing) so the server
	// starts new services in the file's depends_on order.
	watchers := map[string]*watcher{}
	if !o.detach {
		for _, p := range res.Staged {
			if p.Count == 0 {
				continue
			}
			w, err := g.watch(ctx, s, target{Project: t.Project, Environment: p.Environment})
			if err != nil {
				return err
			}
			defer w.close()
			watchers[p.Environment] = w
		}
	}
	req.Deploy = true
	res, err = send[apitypes.ApplyResult](ctx, s, http.MethodPost, base+"/apply", req)
	if err != nil {
		return err
	}
	if !g.io.JSON {
		printPlan(g, res.Plan)
	}
	for i, a := range res.Applied {
		if w := watchers[a.Environment]; w != nil && len(a.Releases) > 0 {
			rels, err := w.follow(ctx, a.Releases)
			if err != nil {
				return interrupted(err, rels)
			}
			res.Applied[i].Releases = rels
		}
	}
	err = g.io.Print(res, func() error {
		for _, a := range res.Applied {
			g.io.Successf("Applied %s/%s: %s started", t.Project, a.Environment, plural(len(a.Releases), "release"))
			for _, r := range a.Releases {
				g.printNotLive(r.Service, r)
			}
			for _, f := range a.Failed {
				g.io.Warnf("Still staged: %s", f)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, a := range res.Applied {
		if err := firstFailure(ctx, s, a.Releases); err != nil {
			return err
		}
	}
	return nil
}

// planGlyph marks a plan line like a diff.
var planGlyph = map[apitypes.ChangeAction]string{apitypes.ChangeAdd: "+", apitypes.ChangeUpdate: "~", apitypes.ChangeRemove: "-"}

// printPlan prints a plan like a Terraform plan: changes, then what is
// kept, then what needs a person.
func printPlan(g *globals, p apitypes.ApplyPlan) {
	out := g.io.Out
	if len(p.Changes) == 0 {
		g.io.Successf("No changes: project %s matches the file", p.Project)
	} else {
		_, _ = fmt.Fprintf(out, "Plan for project %s:\n", p.Project)
		t := g.io.Rows()
		for _, c := range p.Changes {
			t.Row("  "+planGlyph[c.Action], planWhere(c), planWhat(c), planDetail(c))
		}
		_ = t.Flush()
	}
	if len(p.Kept) > 0 {
		_, _ = fmt.Fprintln(out, "Not in the file, kept:")
		t := g.io.Rows()
		for _, c := range p.Kept {
			t.Row("   ", planWhere(c), planWhat(c), c.Note)
		}
		_ = t.Flush()
	}
	if len(p.Manual) > 0 {
		_, _ = fmt.Fprintln(out, "Needs you:")
		t := g.io.Rows()
		for _, c := range p.Manual {
			t.Row("  !", planWhere(c), planWhat(c), planDetail(c))
		}
		_ = t.Flush()
	}
}

// planWhere is a plan line's environment and service.
func planWhere(c apitypes.PlanChange) string {
	return strings.TrimSuffix(c.Environment+"/"+c.Service, "/")
}

// planWhat names what a plan line is about.
func planWhat(c apitypes.PlanChange) string {
	switch c.Kind {
	case apitypes.PlanSetting:
		return c.Name
	case apitypes.PlanDomain:
		if c.Name == "" {
			return "domain (generated)"
		}
	case apitypes.PlanProject, apitypes.PlanEnvironment, apitypes.PlanService:
		return string(c.Kind)
	}
	return string(c.Kind) + " " + c.Name
}

// planDetail is a plan line's values and note.
func planDetail(c apitypes.PlanChange) string {
	var parts []string
	switch {
	case c.Old != "" && c.New != "":
		parts = append(parts, c.Old+" → "+c.New)
	case c.Action == apitypes.ChangeUpdate && c.Kind == apitypes.PlanSetting:
		parts = append(parts, dash(c.Old)+" → "+dash(c.New))
	case c.New != "":
		parts = append(parts, c.New)
	case c.Kind == apitypes.PlanProject:
		parts = append(parts, c.Name)
	case c.Old != "":
		parts = append(parts, c.Old)
	}
	if c.Note != "" && (c.Kind != apitypes.PlanDomain || c.Note != "generated") {
		parts = append(parts, "("+c.Note+")")
	}
	return strings.Join(parts, " ")
}

// newApplyCmd is `dock apply`: apply the environment's staged changes, or
// with a FILE, plan or stage a project file.
func newApplyCmd(g *globals) *cobra.Command {
	var f targetFlags
	var detach, plan, prune, deploy bool
	cmd := &cobra.Command{
		Use:   "apply [FILE] [--plan] [--prune] [--deploy]",
		Short: "Apply the environment's staged changes, or stage a project file",
		Long: `Without FILE: make every staged change in the environment live in one
step, then redeploy each affected running service with a config release,
following progress unless --detach.

With FILE (a project file as ` + "`dock project export`" + ` writes it, or - for
stdin): compare it with the live state and stage the difference in each of
its environments, like edits from the CLI or UI. Nothing goes live until
the environment's changes are applied. A project or environment the file
names that doesn't exist yet is created, empty.

  --plan     show the difference only (+ add, ~ change, - remove); --json for
             agents. --dry-run is the same
  --prune    also remove variables, domains and services the file doesn't
             mention (archiving services needs admin). Never volumes or
             environments. Without it, removals are never staged.
  --deploy   apply each environment's staged changes afterwards
  -e ENV     only this environment of the file

Secrets are never in the file: a name in ` + "`variables`" + ` must exist, and the
plan lists the missing ones with the command that sets them.`,
		Example: `  dock apply                              # apply the linked environment's staged changes
  dock project export > dock.project.toml
  dock apply dock.project.toml --plan
  dock apply dock.project.toml            # stage it
  dock apply dock.project.toml -e staging --deploy`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if len(args) == 1 {
				o := applyFileOpts{plan: plan, prune: prune, deploy: deploy, detach: detach}
				if cmd.Flags().Changed("environment") {
					o.environment = f.environment
				}
				return g.applyFile(ctx, f, args[0], o)
			}
			if plan || prune || deploy {
				return usageError("--plan, --dry-run, --prune and --deploy are for a project file", "Pass the file: `dock apply dock.project.toml --plan`.")
			}
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			var w *watcher
			if !detach {
				if w, err = g.watch(ctx, s, target{Project: t.Project, Environment: t.Environment}); err != nil {
					return err
				}
				defer w.close()
			}
			a, err := send[apitypes.AppliedChanges](ctx, s, http.MethodPost, t.envPath()+"/changes/apply", nil)
			if err != nil {
				return err
			}
			return g.finishApplied(ctx, s, w, a, func(a apitypes.AppliedChanges) any { return a })
		},
	}
	f.add(cmd, needEnvironment)
	addDetachFlag(cmd, &detach)
	cmd.Flags().BoolVar(&plan, "plan", false, "with FILE: show what would change, stage nothing")
	cmd.Flags().BoolVar(&plan, "dry-run", false, "same as --plan")
	cmd.Flags().BoolVar(&prune, "prune", false, "with FILE: also remove what the file doesn't mention (never volumes or environments)")
	cmd.Flags().BoolVar(&deploy, "deploy", false, "with FILE: apply the staged changes afterwards")
	return cmd
}
