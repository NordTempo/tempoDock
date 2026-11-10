package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// newStatusPageCmd is `dock status-page`: the organization's public
// status pages (ADR 0073).
func newStatusPageCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "status-page", Short: "Public status pages of the organization's services"})
	cmd.AddCommand(newStatusPageListCmd(g), newStatusPageShowCmd(g), newStatusPageCreateCmd(g), newStatusPageUpdateCmd(g),
		newStatusPagePublishCmd(g, true), newStatusPagePublishCmd(g, false), newStatusPageRemoveCmd(g), newStatusComponentCmd(g))
	return cmd
}

// statusPagePath is a page's API path.
func statusPagePath(slug string) string { return apiPrefix + "/status-pages/" + url.PathEscape(slug) }

func newStatusPageListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the organization's status pages",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			pages, err := get[[]apitypes.StatusPageSummary](cmd.Context(), s, apiPrefix+"/status-pages")
			if err != nil {
				return err
			}
			return g.io.Print(pages, func() error {
				if len(pages) == 0 {
					g.io.Successf("No status pages. Make one with `dock status-page create <name>`.")
					return nil
				}
				t := g.io.Table("name", "title", "published", "components", "address")
				for _, p := range pages {
					t.Row(p.Slug, p.Title, yesNo(p.Published), strconv.Itoa(p.Components), orDash(p.URL))
				}
				return t.Flush()
			})
		},
	}
}

func newStatusPageShowCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "show <page>",
		Short: "Show a status page's components and their status now",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			p, err := get[apitypes.StatusPage](cmd.Context(), s, statusPagePath(args[0]))
			if err != nil {
				return err
			}
			return g.io.Print(p, func() error { return printStatusPage(g, p) })
		},
	}
}

// printStatusPage shows a page: its address and state, then a row per
// component.
func printStatusPage(g *globals, p apitypes.StatusPage) error {
	state := "unpublished"
	if p.Published {
		state = "published at " + orDash(p.URL)
	}
	_, _ = fmt.Fprintf(g.io.Out, "%s (%s): %s, %s\n\n", p.Title, p.Slug, state, strings.ReplaceAll(string(p.Status), "_", " "))
	if len(p.Components) == 0 {
		_, _ = fmt.Fprintf(g.io.Out, "No components. Add one with `dock status-page component add %s <name> --service <project/environment/service>`.\n", p.Slug)
		return nil
	}
	t := g.io.Table("component", "group", "status", "90d", "from")
	for _, c := range p.Components {
		var from []string
		for _, src := range c.Sources {
			from = append(from, sourceText(src))
		}
		if !c.Auto {
			from = append(from, "(set by hand)")
		}
		t.Row(c.Name, orDash(c.Group), strings.ReplaceAll(string(c.Status), "_", " "), pct(c.Uptime), orDash(strings.Join(from, ", ")))
	}
	return t.Flush()
}

// sourceText is a component's source in a table.
func sourceText(src apitypes.StatusSource) string {
	s := src.Service
	switch {
	case src.Hidden:
		s = "another project"
	case src.Check != 0:
		s = "check " + strconv.FormatInt(src.Check, 10)
	}
	if src.Problem != "" {
		s += " (" + src.Problem + ")"
	}
	return s
}

func newStatusPageCreateCmd(g *globals) *cobra.Command {
	var title string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Make a status page, unpublished (admin)",
		Long: `Make a status page. Its name is its address, status-<name>.<base domain>,
and one page's in the whole install. Add components, then publish it.`,
		Example: `  dock status-page create acme --title "Acme"
  dock status-page component add acme API --service shop/production/api --group Core
  dock status-page publish acme`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			p, err := send[apitypes.StatusPage](cmd.Context(), s, http.MethodPost, apiPrefix+"/status-pages",
				apitypes.CreateStatusPageRequest{Slug: args[0], Title: title})
			if err != nil {
				return err
			}
			return g.io.Print(p, func() error {
				g.io.Successf("Made status page %s. Add components with `dock status-page component add %s <name> --service <project/environment/service>`, then `dock status-page publish %s`.", p.Slug, p.Slug, p.Slug)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "the page's heading (default: its name)")
	return cmd
}

func newStatusPageUpdateCmd(g *globals) *cobra.Command {
	var title string
	var showUptime bool
	cmd := &cobra.Command{
		Use:   "update <page>",
		Short: "Change a status page's title or whether it shows percentages (admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var req apitypes.UpdateStatusPageRequest
			if cmd.Flags().Changed("title") {
				req.Title = &title
			}
			if cmd.Flags().Changed("show-uptime") {
				req.ShowUptime = &showUptime
			}
			if req.Title == nil && req.ShowUptime == nil {
				return &output.Error{Exit: output.ExitUsage, Message: "nothing to change", Hint: "Pass --title or --show-uptime."}
			}
			return patchStatusPage(cmd.Context(), g, args[0], req, "Updated status page %s")
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "the page's heading")
	cmd.Flags().BoolVar(&showUptime, "show-uptime", true, "show uptime percentages (the bars and statuses stay either way)")
	return cmd
}

func newStatusPagePublishCmd(g *globals, publish bool) *cobra.Command {
	use, short, done := "publish <page>", "Serve a status page at its address (admin)", "Published status page %s"
	if !publish {
		use, short, done = "unpublish <page>", "Stop serving a status page; its address answers 404 (admin)", "Unpublished status page %s"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return patchStatusPage(cmd.Context(), g, args[0], apitypes.UpdateStatusPageRequest{Published: &publish}, done)
		},
	}
}

// patchStatusPage sends a page's change and says done (with the page's
// name, and its address once published).
func patchStatusPage(ctx context.Context, g *globals, slug string, req apitypes.UpdateStatusPageRequest, done string) error {
	s, err := g.connect()
	if err != nil {
		return err
	}
	p, err := send[apitypes.StatusPage](ctx, s, http.MethodPatch, statusPagePath(slug), req)
	if err != nil {
		return err
	}
	return g.io.Print(p, func() error {
		msg := fmt.Sprintf(done, p.Slug)
		if p.Published && p.URL != "" {
			msg += ": " + p.URL
		}
		g.io.Successf("%s", msg)
		return nil
	})
}

func newStatusPageRemoveCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "remove <page>",
		Aliases: []string{"rm"},
		Short:   "Remove a status page and its components (admin)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Remove status page %s? Its address stops answering and its components go.", args[0]), "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, statusPagePath(args[0])); err != nil {
				return err
			}
			return g.io.Print(map[string]string{"removed": args[0]}, func() error {
				g.io.Successf("Removed status page %s", args[0])
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

// newStatusComponentCmd is `dock status-page component`: changing a
// page's component list, which the API takes whole.
func newStatusComponentCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "component", Short: "Add, remove and order a status page's components (admin)"})
	cmd.AddCommand(newStatusComponentAddCmd(g), newStatusComponentRemoveCmd(g), newStatusComponentMoveCmd(g))
	return cmd
}

// editComponents reads a page's components, changes them with edit and
// sends the whole list back.
func editComponents(ctx context.Context, g *globals, slug string, edit func([]apitypes.StatusComponentInput) ([]apitypes.StatusComponentInput, error), done string) error {
	s, err := g.connect()
	if err != nil {
		return err
	}
	p, err := get[apitypes.StatusPage](ctx, s, statusPagePath(slug))
	if err != nil {
		return err
	}
	list := make([]apitypes.StatusComponentInput, len(p.Components))
	for i, c := range p.Components {
		auto := c.Auto
		in := apitypes.StatusComponentInput{Name: c.Name, Group: c.Group, Description: c.Description, Auto: &auto}
		for _, src := range c.Sources {
			if src.Hidden {
				return &output.Error{Exit: output.ExitAuth, Message: fmt.Sprintf("component %s is built from a project you can't see", c.Name),
					Hint: "Change the page's components from a session or token that sees every project."}
			}
			in.Sources = append(in.Sources, src.StatusSourceRef)
		}
		list[i] = in
	}
	if list, err = edit(list); err != nil {
		return err
	}
	p, err = send[apitypes.StatusPage](ctx, s, http.MethodPut, statusPagePath(slug)+"/components", apitypes.SetStatusComponentsRequest{Components: list})
	if err != nil {
		return err
	}
	return g.io.Print(p, func() error {
		g.io.Successf("%s", done)
		return printStatusPage(g, p)
	})
}

// componentIndex is the position of the component called name, or a
// usage error.
func componentIndex(list []apitypes.StatusComponentInput, page, name string) (int, error) {
	for i, c := range list {
		if strings.EqualFold(c.Name, name) {
			return i, nil
		}
	}
	return 0, &output.Error{Exit: output.ExitNotFound, Message: fmt.Sprintf("status page %s has no component %q", page, name),
		Hint: "`dock status-page show " + page + "` lists them."}
}

func newStatusComponentAddCmd(g *globals) *cobra.Command {
	var services []string
	var checks []int64
	var grp, description string
	var manual bool
	cmd := &cobra.Command{
		Use:   "add <page> <name>",
		Short: "Add a component, or change the one of that name",
		Long: `Add a component to a status page, at the end. Its name is what the public
sees: pick one that doesn't name your services or servers. Its status
comes from its sources: services (all their uptime checks and their
health) and single uptime checks (dock uptime list shows their IDs).
Previews can't be on a status page. --manual leaves it operational until
incidents set it.`,
		Example: `  dock status-page component add acme API --service shop/production/api --group Core
  dock status-page component add acme "Sign-in" --check 12 --check 14`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			auto := !manual
			in := apitypes.StatusComponentInput{Name: args[1], Group: grp, Description: description, Auto: &auto}
			for _, svc := range services {
				in.Sources = append(in.Sources, apitypes.StatusSourceRef{Service: svc})
			}
			for _, id := range checks {
				in.Sources = append(in.Sources, apitypes.StatusSourceRef{Check: id})
			}
			if len(in.Sources) == 0 && !manual {
				return &output.Error{Exit: output.ExitUsage, Message: "a component needs a source",
					Hint: "Pass --service project/environment/service or --check ID, or --manual for one only incidents set."}
			}
			return editComponents(cmd.Context(), g, args[0], func(list []apitypes.StatusComponentInput) ([]apitypes.StatusComponentInput, error) {
				if i, err := componentIndex(list, args[0], args[1]); err == nil {
					list[i] = in
					return list, nil
				}
				return append(list, in), nil
			}, "Set component "+args[1])
		},
	}
	cmd.Flags().StringArrayVar(&services, "service", nil, "a service it shows, as project/environment/service (repeatable)")
	cmd.Flags().Int64SliceVar(&checks, "check", nil, "an uptime check it shows, by ID (repeatable)")
	cmd.Flags().StringVar(&grp, "group", "", "the group it is listed under")
	cmd.Flags().StringVar(&description, "description", "", "a few words shown next to its name")
	cmd.Flags().BoolVar(&manual, "manual", false, "no status from checks or health: only incidents set it")
	return cmd
}

func newStatusComponentRemoveCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "remove <page> <name>",
		Aliases: []string{"rm"},
		Short:   "Remove a component from a status page",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return editComponents(cmd.Context(), g, args[0], func(list []apitypes.StatusComponentInput) ([]apitypes.StatusComponentInput, error) {
				i, err := componentIndex(list, args[0], args[1])
				if err != nil {
					return nil, err
				}
				return append(list[:i], list[i+1:]...), nil
			}, "Removed component "+args[1])
		},
	}
}

func newStatusComponentMoveCmd(g *globals) *cobra.Command {
	var to int
	cmd := &cobra.Command{
		Use:   "move <page> <name> --to <position>",
		Short: "Move a component to a position, 1 being the top",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return editComponents(cmd.Context(), g, args[0], func(list []apitypes.StatusComponentInput) ([]apitypes.StatusComponentInput, error) {
				i, err := componentIndex(list, args[0], args[1])
				if err != nil {
					return nil, err
				}
				if to < 1 || to > len(list) {
					return nil, &output.Error{Exit: output.ExitUsage, Message: fmt.Sprintf("--to is 1 to %d", len(list))}
				}
				c := list[i]
				list = append(list[:i], list[i+1:]...)
				return append(list[:to-1], append([]apitypes.StatusComponentInput{c}, list[to-1:]...)...), nil
			}, fmt.Sprintf("Moved component %s to %d", args[1], to))
		},
	}
	cmd.Flags().IntVar(&to, "to", 0, "the position, 1 being the top")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}
