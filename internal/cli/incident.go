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

// newIncidentCmd is `dock incident`: incidents on the organization's
// status pages (ADR 0073). Top level, since on-call types it under
// stress.
func newIncidentCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "incident", Short: "Open, update and resolve incidents on status pages"})
	cmd.AddCommand(newIncidentListCmd(g), newIncidentShowCmd(g), newIncidentOpenCmd(g), newIncidentUpdateCmd(g),
		newIncidentPublishCmd(g), newIncidentResolveCmd(g), newIncidentEditCmd(g), newIncidentRemoveCmd(g))
	return cmd
}

// incidentPath is an incident's API path.
func incidentPath(id string) string { return apiPrefix + "/incidents/" + url.PathEscape(id) }

// impactNames are the impacts --component takes, short and full.
var impactNames = map[string]apitypes.ComponentStatus{
	"degraded": apitypes.StatusDegraded, "partial": apitypes.StatusPartialOutage, "partial_outage": apitypes.StatusPartialOutage,
	"major": apitypes.StatusMajorOutage, "major_outage": apitypes.StatusMajorOutage,
}

// parseImpacts reads --component NAME:IMPACT flags.
func parseImpacts(flags []string) ([]apitypes.StatusImpact, error) {
	out := make([]apitypes.StatusImpact, 0, len(flags))
	for _, f := range flags {
		i := strings.LastIndex(f, ":")
		impact, ok := impactNames[strings.ToLower(f[i+1:])]
		if i <= 0 || !ok {
			return nil, &output.Error{Exit: output.ExitUsage, Message: fmt.Sprintf("--component %q isn't NAME:IMPACT", f),
				Hint: "IMPACT is degraded, partial or major, e.g. --component API:partial."}
		}
		out = append(out, apitypes.StatusImpact{Component: f[:i], Impact: impact})
	}
	return out, nil
}

// impactsLine is what an incident does, in a line.
func impactsLine(ims []apitypes.StatusImpact) string {
	parts := make([]string, len(ims))
	for i, im := range ims {
		parts[i] = im.Component
		if im.Impact != apitypes.StatusMaintenance {
			parts[i] += " " + strings.ReplaceAll(string(im.Impact), "_", " ")
		}
	}
	return orDash(strings.Join(parts, ", "))
}

func newIncidentListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "list [page]",
		Aliases: []string{"ls"},
		Short:   "List incidents open or resolved in the last 90 days",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			slugs := args
			if len(slugs) == 0 {
				pages, err := get[[]apitypes.StatusPageSummary](cmd.Context(), s, apiPrefix+"/status-pages")
				if err != nil {
					return err
				}
				for _, p := range pages {
					slugs = append(slugs, p.Slug)
				}
			}
			out := []apitypes.StatusIncident{}
			for _, slug := range slugs {
				p, err := get[apitypes.StatusPage](cmd.Context(), s, statusPagePath(slug))
				if err != nil {
					return err
				}
				out = append(out, p.Incidents...)
			}
			return g.io.Print(out, func() error {
				if len(out) == 0 {
					g.io.Successf("No incidents in the last 90 days.")
					return nil
				}
				t := g.io.Table("id", "page", "kind", "title", "state", "components", "starts")
				for _, inc := range out {
					t.Row(inc.ID, inc.Page, string(inc.Kind), inc.Title, string(inc.State), impactsLine(inc.Components), startsText(inc))
				}
				return t.Flush()
			})
		},
	}
}

func newIncidentShowCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show an incident and its updates",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			inc, err := get[apitypes.StatusIncident](cmd.Context(), s, incidentPath(args[0]))
			if err != nil {
				return err
			}
			return g.io.Print(inc, func() error { return printIncident(g, inc) })
		},
	}
}

// startsText is when an incident started, or a window's time.
func startsText(inc apitypes.StatusIncident) string {
	if inc.Kind == apitypes.KindMaintenance && inc.EndsAt != nil {
		return inc.StartedAt.UTC().Format("2 Jan 15:04") + " to " + inc.EndsAt.UTC().Format("15:04 MST")
	}
	return ago(inc.StartedAt)
}

// printIncident shows an incident: what it is and does, then its
// updates oldest first.
func printIncident(g *globals, inc apitypes.StatusIncident) error {
	w := g.io.Out
	_, _ = fmt.Fprintf(w, "%s %q on %s: %s %s\n", inc.ID, inc.Title, inc.Page, inc.Kind, inc.State)
	if inc.Kind == apitypes.KindMaintenance {
		_, _ = fmt.Fprintf(w, "window: %s\n", startsText(inc))
	}
	_, _ = fmt.Fprintf(w, "components: %s\n", impactsLine(inc.Components))
	if inc.URL != "" {
		_, _ = fmt.Fprintf(w, "public at %s\n", inc.URL)
	}
	if inc.Alert != nil {
		_, _ = fmt.Fprintf(w, "drafted by %s from alert %s on %s: %s\n", inc.CreatedBy, inc.Alert.Kind, orDash(inc.Alert.Service), inc.Alert.Title)
	} else {
		_, _ = fmt.Fprintf(w, "opened by %s %s\n", inc.CreatedBy, ago(inc.StartedAt))
	}
	if inc.State == apitypes.IncidentDraft {
		_, _ = fmt.Fprintf(w, "\nDraft, not public: %s\nPublish it with `dock incident publish %s`.\n", orDash(inc.Draft), inc.ID)
		return nil
	}
	_, _ = fmt.Fprintln(w)
	t := g.io.Table("update", "at", "state", "by", "message")
	for _, u := range inc.Updates {
		msg := strings.ReplaceAll(u.Message, "\n", " ")
		if u.EditedAt != nil {
			msg += " (edited)"
		}
		t.Row(strconv.FormatInt(u.ID, 10), u.At.UTC().Format("2 Jan 15:04"), string(u.State), u.By, msg)
	}
	return t.Flush()
}

// printIncidentDone says what happened to an incident and where it is.
func printIncidentDone(g *globals, inc apitypes.StatusIncident, done string) error {
	return g.io.Print(inc, func() error {
		msg := fmt.Sprintf("%s %s (%s)", done, inc.ID, inc.State)
		if inc.URL != "" {
			msg += ": " + inc.URL
		}
		g.io.Successf("%s", msg)
		return nil
	})
}

func newIncidentOpenCmd(g *globals) *cobra.Command {
	var comps []string
	var message, state string
	var draft bool
	cmd := &cobra.Command{
		Use:   "open <page> <title>",
		Short: "Open an incident on a status page, public at once unless --draft",
		Long: `Open an incident. Its title and message are public as soon as it is open;
--draft keeps it in Dock until someone runs dock incident publish. While
it is open, each --component reads at least the impact given. Deploy
scope; a token limited to a project names only components built from it.`,
		Example: `  dock incident open acme "API errors for some requests" --component API:partial -m "We are investigating."
  dock incident open acme "Slow sign-in" --component Sign-in:degraded --draft`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ims, err := parseImpacts(comps)
			if err != nil {
				return err
			}
			req := apitypes.OpenIncidentRequest{Title: args[1], Components: ims, State: apitypes.IncidentState(state), Message: message}
			if draft {
				req.State = apitypes.IncidentDraft
			} else if message == "" {
				return &output.Error{Exit: output.ExitUsage, Message: "an incident's first update needs a message",
					Hint: "Pass -m \"what is going on\", or --draft to write it later."}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			inc, err := send[apitypes.StatusIncident](cmd.Context(), s, http.MethodPost, statusPagePath(args[0])+"/incidents", req)
			if err != nil {
				return err
			}
			if draft {
				return printIncidentDone(g, inc, "Drafted incident")
			}
			return printIncidentDone(g, inc, "Opened incident")
		},
	}
	cmd.Flags().StringArrayVar(&comps, "component", nil, "a component and its impact, NAME:degraded|partial|major (repeatable)")
	cmd.Flags().StringVarP(&message, "message", "m", "", "the first update, public")
	cmd.Flags().StringVar(&state, "state", "", "investigating (default), identified or monitoring")
	cmd.Flags().BoolVar(&draft, "draft", false, "keep it unpublished; -m is the draft's words")
	return cmd
}

// postUpdate posts an update on an incident.
func postUpdate(ctx context.Context, g *globals, id string, req apitypes.PostIncidentUpdateRequest, done string) error {
	s, err := g.connect()
	if err != nil {
		return err
	}
	inc, err := send[apitypes.StatusIncident](ctx, s, http.MethodPost, incidentPath(id)+"/updates", req)
	if err != nil {
		return err
	}
	return printIncidentDone(g, inc, done)
}

func newIncidentUpdateCmd(g *globals) *cobra.Command {
	var comps []string
	var message, state string
	cmd := &cobra.Command{
		Use:   "update <id> -m <message>",
		Short: "Post an update on an incident, public at once",
		Long: `Post an update. --state defaults to the incident's (investigating for a
draft, which this publishes); resolved resolves it. --component replaces
what it does to components.`,
		Example: `  dock incident update 4f2kqz7mna --state identified -m "A slow index rebuild; moving traffic off it."
  dock incident update 4f2kqz7mna -m "Still on it." --component API:major`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := apitypes.PostIncidentUpdateRequest{State: apitypes.IncidentState(state), Message: message}
			if cmd.Flags().Changed("component") {
				ims, err := parseImpacts(comps)
				if err != nil {
					return err
				}
				req.Components = &ims
			}
			if req.State == "" {
				s, err := g.connect()
				if err != nil {
					return err
				}
				inc, err := get[apitypes.StatusIncident](cmd.Context(), s, incidentPath(args[0]))
				if err != nil {
					return err
				}
				req.State = inc.State
				if inc.State == apitypes.IncidentDraft {
					req.State = apitypes.IncidentInvestigating
				}
			}
			return postUpdate(cmd.Context(), g, args[0], req, "Updated incident")
		},
	}
	cmd.Flags().StringArrayVar(&comps, "component", nil, "a component and its impact, NAME:degraded|partial|major (repeatable; replaces the incident's)")
	cmd.Flags().StringVarP(&message, "message", "m", "", "the update, public")
	cmd.Flags().StringVar(&state, "state", "", "investigating, identified, monitoring or resolved (default: the incident's)")
	_ = cmd.MarkFlagRequired("message")
	return cmd
}

func newIncidentPublishCmd(g *globals) *cobra.Command {
	var message, state string
	cmd := &cobra.Command{
		Use:   "publish <id>",
		Short: "Publish a draft incident with its draft words, or -m",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			inc, err := get[apitypes.StatusIncident](cmd.Context(), s, incidentPath(args[0]))
			if err != nil {
				return err
			}
			if inc.State != apitypes.IncidentDraft {
				return &output.Error{Exit: output.ExitConflict, Message: fmt.Sprintf("incident %s is published already", inc.ID),
					Hint: "Post an update with `dock incident update " + inc.ID + " -m ...`."}
			}
			if message == "" {
				message = inc.Draft
			}
			if message == "" {
				return &output.Error{Exit: output.ExitUsage, Message: "the draft has no words", Hint: "Pass -m \"what is going on\"."}
			}
			return postUpdate(cmd.Context(), g, args[0], apitypes.PostIncidentUpdateRequest{State: apitypes.IncidentState(state), Message: message}, "Published incident")
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "", "the first update (default: the draft's words)")
	cmd.Flags().StringVar(&state, "state", string(apitypes.IncidentInvestigating), "investigating, identified or monitoring")
	return cmd
}

func newIncidentResolveCmd(g *globals) *cobra.Command {
	var message string
	cmd := &cobra.Command{
		Use:   "resolve <id>",
		Short: "Resolve an incident; its components read as their checks say again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return postUpdate(cmd.Context(), g, args[0], apitypes.PostIncidentUpdateRequest{State: apitypes.IncidentResolved, Message: message}, "Resolved incident")
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "This incident has been resolved.", "the last update, public")
	return cmd
}

func newIncidentEditCmd(g *globals) *cobra.Command {
	var comps []string
	var title, draft, message string
	var update int64
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Change an incident without posting an update",
		Long: `Change an incident's title, what it does to components, a draft's words
(--draft-message), or fix a posted update's words (--update ID -m; the
page says it was edited). dock incident show lists the updates' IDs.`,
		Example: `  dock incident edit 4f2kqz7mna --title "API errors in one region"
  dock incident edit 4f2kqz7mna --update 12 -m "We found the cause: a slow index rebuild."`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("update") {
				if message == "" {
					return &output.Error{Exit: output.ExitUsage, Message: "--update needs -m with the update's new words"}
				}
				inc, err := send[apitypes.StatusIncident](cmd.Context(), s, http.MethodPatch, incidentPath(args[0])+"/updates/"+strconv.FormatInt(update, 10),
					apitypes.EditIncidentUpdateRequest{Message: message})
				if err != nil {
					return err
				}
				return printIncidentDone(g, inc, "Edited an update of incident")
			}
			var req apitypes.UpdateIncidentRequest
			if cmd.Flags().Changed("title") {
				req.Title = &title
			}
			if cmd.Flags().Changed("draft-message") {
				req.Draft = &draft
			}
			if cmd.Flags().Changed("component") {
				ims, err := parseImpacts(comps)
				if err != nil {
					return err
				}
				req.Components = &ims
			}
			if req.Title == nil && req.Draft == nil && req.Components == nil {
				return &output.Error{Exit: output.ExitUsage, Message: "nothing to change", Hint: "Pass --title, --component, --draft-message or --update with -m."}
			}
			inc, err := send[apitypes.StatusIncident](cmd.Context(), s, http.MethodPatch, incidentPath(args[0]), req)
			if err != nil {
				return err
			}
			return printIncidentDone(g, inc, "Changed incident")
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "the incident's title")
	cmd.Flags().StringArrayVar(&comps, "component", nil, "a component and its impact, NAME:degraded|partial|major (repeatable; replaces the incident's)")
	cmd.Flags().StringVar(&draft, "draft-message", "", "a draft's words, posted when it is published")
	cmd.Flags().Int64Var(&update, "update", 0, "the posted update to fix, by ID")
	cmd.Flags().StringVarP(&message, "message", "m", "", "the update's new words, with --update")
	return cmd
}

func newIncidentRemoveCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "remove <id>",
		Aliases: []string{"rm"},
		Short:   "Remove an incident from its page and history (admin)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Remove incident %s? It goes from the page, its history and the feed.", args[0]), "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, incidentPath(args[0])); err != nil {
				return err
			}
			return g.io.Print(map[string]string{"removed": args[0]}, func() error {
				g.io.Successf("Removed incident %s", args[0])
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}
