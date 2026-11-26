package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// App templates (ADR 0083): `dock template list|show|use`.

func newTemplateCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Apps and databases to create from a template",
		Long: `Templates create an app's services in one step: Umami with its Postgres,
n8n, Forgejo and more, with their settings, volumes, generated secrets and a
domain. The services are ordinary afterwards: change them like any other.`,
	}
	cmd.AddCommand(newTemplateListCmd(g), newTemplateShowCmd(g), newTemplateUseCmd(g), newTemplateSaveCmd(g), newTemplateRemoveCmd(g))
	return cmd
}

func newTemplateListCmd(g *globals) *cobra.Command {
	var category string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the templates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			all, err := get[[]apitypes.AppTemplate](cmd.Context(), s, apiPrefix+"/app-templates")
			if err != nil {
				return err
			}
			list := all[:0:0]
			for _, t := range all {
				if category == "" || strings.EqualFold(t.Category, category) {
					list = append(list, t)
				}
			}
			return g.io.Print(list, func() error {
				rows := g.io.Rows()
				rows.Row("NAME", "WHAT", "CATEGORY", "SERVICES", "MEMORY", "LICENSE")
				for _, t := range list {
					category := t.Category
					if t.Org {
						category = "organization's"
					}
					rows.Row(t.Name, t.Summary, category, strconv.Itoa(len(t.Services)), memText(t.MemoryMB), t.License)
				}
				_ = rows.Flush()
				g.io.Progressf("Use one with `dock template use <name> -e <environment>`; `dock template show <name>` lists its inputs.")
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&category, "category", "", "only this category (analytics, dev, databases, ...)")
	return cmd
}

// memText is a memory size in MB as the CLI shows it.
func memText(mb int) string {
	if mb >= 1024 && mb%512 == 0 {
		return strconv.FormatFloat(float64(mb)/1024, 'f', -1, 64) + " GB"
	}
	return strconv.Itoa(mb) + " MB"
}

func newTemplateShowCmd(g *globals) *cobra.Command {
	var raw bool
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show a template's services and inputs",
		Long: `Show a template's services and inputs. --toml prints its file, to edit and
save as the organization's own with ` + "`dock template save --file`" + `.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			if raw {
				f, err := get[apitypes.TemplateFile](cmd.Context(), s, apiPrefix+"/app-templates/"+url.PathEscape(args[0])+"/file")
				if err != nil {
					return err
				}
				_, err = fmt.Fprint(g.io.Out, f.File)
				return err
			}
			t, err := get[apitypes.AppTemplate](cmd.Context(), s, apiPrefix+"/app-templates/"+url.PathEscape(args[0]))
			if err != nil {
				return err
			}
			return g.io.Print(t, func() error {
				out := g.io.Out
				_, _ = fmt.Fprintf(out, "%s: %s\n%s, %s, revision %d\n", t.Title, t.Summary, t.License, t.Website, t.Revision)
				_, _ = fmt.Fprintln(out, "Services:")
				rows := g.io.Rows()
				for _, sv := range t.Services {
					name := sv.Name
					if sv.Main {
						name = "<name>"
					} else {
						name = "<name>-" + name
					}
					what := sv.Image
					if sv.Template != "" {
						what = string(sv.Template) + " template"
					}
					rows.Row("  "+name, what, memText(sv.MemoryMB))
				}
				_ = rows.Flush()
				if len(t.Inputs) > 0 {
					_, _ = fmt.Fprintln(out, "Inputs (--set KEY=VALUE):")
					rows := g.io.Rows()
					for _, in := range t.Inputs {
						note := string(in.Kind)
						switch {
						case in.Default != "":
							note += ", default " + in.Default
						case in.Optional:
							note += ", optional"
						default:
							note += ", required"
						}
						rows.Row("  "+in.Key, in.Label, note, in.Help)
					}
					_ = rows.Flush()
				}
				if t.Notes != "" {
					_, _ = fmt.Fprintln(out, "After: "+t.Notes)
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&raw, "toml", false, "print the template's file")
	return cmd
}

func newTemplateSaveCmd(g *globals) *cobra.Command {
	var f targetFlags
	var file string
	var req apitypes.SaveTemplateFromRequest
	cmd := &cobra.Command{
		Use:   "save [name]",
		Short: "Save an environment's services, or a template file, as the organization's template",
		Long: `Save an organization template (admin). With a name, the environment's
services become one: their settings, volumes, jobs and references between
them; variables that aren't references become secret inputs, so no value is
copied. --services picks some, --main the one users name. With --file, a
template file in the catalog's format is saved as it is (its name is in the
file); ` + "`dock template show NAME --toml`" + ` prints one to start from.

The organization's members see it in ` + "`dock template list`" + ` and use it like
Dock's own; saving again replaces it.`,
		Example: `  dock template save shop-stack -p shop -e production --services api,worker,db
  dock template show umami --toml > stats.toml && dock template save --file stats.toml`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Empty would save the environment's services instead.
			if err := blankFlag(cmd, "file", "Pass the template file to save, or leave out --file to save the environment's services."); err != nil {
				return err
			}
			if file != "" {
				if len(args) > 0 {
					return usageError("--file names the template itself", "Drop the name: `dock template save --file "+file+"`.")
				}
				b, err := os.ReadFile(file)
				if err != nil {
					return usageError(err.Error(), "")
				}
				s, err := g.connect()
				if err != nil {
					return err
				}
				t, err := send[apitypes.AppTemplate](cmd.Context(), s, http.MethodPost, apiPrefix+"/app-templates", apitypes.SaveTemplateRequest{File: string(b)})
				if err != nil {
					return err
				}
				return g.io.Print(t, func() error { g.io.Successf("Saved the organization's template %s", t.Name); return nil })
			}
			if len(args) == 0 {
				return usageError("name the template, or pass --file", "Save an environment's services: `dock template save NAME -e production`.")
			}
			req.Name = args[0]
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			saved, err := send[apitypes.AppTemplate](cmd.Context(), s, http.MethodPost, t.envPath()+"/app-templates", req)
			if err != nil {
				return err
			}
			return g.io.Print(saved, func() error {
				names := make([]string, 0, len(saved.Services))
				for _, sv := range saved.Services {
					names = append(names, sv.Name)
				}
				g.io.Successf("Saved %s from %s as the organization's template %s", strings.Join(names, ", "), t.Environment, saved.Name)
				if len(saved.Inputs) > 0 {
					g.io.Detail(fmt.Sprintf("Using it asks for %s.", plural(len(saved.Inputs), "input")))
				}
				return nil
			})
		},
	}
	f.add(cmd, needEnvironment)
	cmd.Flags().StringVar(&file, "file", "", "save this template file instead")
	cmd.Flags().StringSliceVar(&req.Services, "services", nil, "the services to save (default: every one that runs an image or a database template)")
	cmd.Flags().StringVar(&req.Main, "main", "", "the service users name (default: the first with a domain)")
	cmd.Flags().StringVar(&req.Title, "title", "", "the template's title")
	cmd.Flags().StringVar(&req.Summary, "summary", "", "one line on what it is")
	return cmd
}

func newTemplateRemoveCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an organization template (services made from it stay)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, apiPrefix+"/app-templates/"+url.PathEscape(args[0])); err != nil {
				return err
			}
			return g.io.Print(map[string]string{"removed": args[0]}, func() error {
				g.io.Successf("Removed the organization's template %s", args[0])
				return nil
			})
		},
	}
}

func newTemplateUseCmd(g *globals) *cobra.Command {
	var f targetFlags
	var req apitypes.UseTemplateRequest
	var sets []string
	var file, detach bool
	cmd := &cobra.Command{
		Use:   "use <template>",
		Short: "Create a template's services in an environment and deploy them",
		Long: `Create a template's services and deploy them. The main service is named
--name (default: the template's name) and the others NAME-<part>, such as
umami and umami-db. A template with a domain gets --domain, or a generated
one. Other staged changes in the environment stay staged.

--plan says what it would create; --file prints it as a project file to
edit and apply with ` + "`dock apply`" + `.`,
		Example: `  dock template use umami -e production
  dock template use n8n --name automations --domain n8n.example.com
  dock template use grafana --set GF_SECURITY_ADMIN_PASSWORD="$PASSWORD"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			for _, kv := range sets {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return usageError(fmt.Sprintf("--set %q isn't KEY=VALUE", kv), "See the inputs with `dock template show "+args[0]+"`.")
				}
				if req.Inputs == nil {
					req.Inputs = map[string]string{}
				}
				req.Inputs[k] = v
			}
			req.Plan = req.Plan || file
			path := t.envPath() + "/app-templates/" + url.PathEscape(args[0])
			if req.Plan {
				res, err := send[apitypes.UseTemplateResult](cmd.Context(), s, http.MethodPost, path, req)
				if err != nil {
					return err
				}
				if file {
					_, err := fmt.Fprint(g.io.Out, res.File)
					return err
				}
				return g.io.Print(res, func() error {
					printPlan(g, res.Plan)
					g.io.Progressf("Create them by running this again without --plan.")
					return nil
				})
			}
			req.Detach = detach
			var w *watcher
			if !detach {
				if w, err = g.watch(cmd.Context(), s, t); err != nil {
					return err
				}
				defer w.close()
			}
			res, err := send[apitypes.UseTemplateResult](cmd.Context(), s, http.MethodPost, path, req)
			if err != nil {
				return err
			}
			if !g.io.JSON {
				printPlan(g, res.Plan)
			}
			if w != nil && len(res.Releases) > 0 {
				rels, err := w.follow(cmd.Context(), res.Releases)
				if err != nil {
					return interrupted(err, rels)
				}
				res.Releases = rels
			}
			err = g.io.Print(res, func() error {
				g.io.Successf("Created %s in %s/%s", strings.Join(res.Services, ", "), t.Project, t.Environment)
				if len(res.Services) > 0 && !detach {
					t.Service = res.Services[0]
					g.printURLs(cmd.Context(), s, t)
				}
				for _, r := range res.Releases {
					g.printNotLive(r.Service, r)
				}
				for _, msg := range res.Failed {
					g.io.Warnf("%s", msg)
				}
				if detach {
					g.io.Progressf("Follow the deploys with `dock status -e %s`.", t.Environment)
				}
				return nil
			})
			if err != nil {
				return err
			}
			return firstFailure(cmd.Context(), s, res.Releases)
		},
	}
	f.add(cmd, needEnvironment)
	cmd.Flags().StringVar(&req.Name, "name", "", "the main service's name; the others are NAME-<part> (default: the template's name)")
	cmd.Flags().StringVar(&req.Domain, "domain", "", "the main service's hostname (default: a generated one)")
	cmd.Flags().StringArrayVar(&sets, "set", nil, "an input, KEY=VALUE (repeat for each; dock template show lists them)")
	cmd.Flags().BoolVar(&req.Plan, "plan", false, "only say what it would create")
	cmd.Flags().BoolVar(&file, "file", false, "print the project file it would create, to edit and apply with dock apply")
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "don't wait for the first deploys")
	return cmd
}
