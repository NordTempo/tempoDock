package cli

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// newMaintenanceCmd is `dock maintenance`: maintenance windows on status
// pages (ADR 0073). `dock incident list` and `show` list and show them
// with the incidents.
func newMaintenanceCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "maintenance", Short: "Schedule maintenance windows on status pages"})
	cmd.AddCommand(newMaintenanceScheduleCmd(g),
		newMaintenanceMoveCmd(g, "start", "Start a scheduled window now", apitypes.MaintenanceInProgress, "Maintenance started."),
		newMaintenanceMoveCmd(g, "complete", "Complete a window now; held alerts still open are sent", apitypes.MaintenanceCompleted, "Maintenance completed."),
		newMaintenanceMoveCmd(g, "cancel", "Cancel a window, scheduled or in progress", apitypes.MaintenanceCancelled, "This maintenance is cancelled."),
		newMaintenanceExtendCmd(g))
	return cmd
}

// timeLayouts are the times --from and --to take, UTC unless they say.
var timeLayouts = []string{time.RFC3339, "2006-01-02T15:04Z07:00", "2006-01-02T15:04Z", "2006-01-02T15:04", "2006-01-02 15:04"}

// parseWhen reads a time: "now", or a date and time (UTC unless it has an
// offset).
func parseWhen(flag, s string) (time.Time, error) {
	if s == "" || s == "now" {
		return time.Now().UTC(), nil
	}
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, &output.Error{Exit: output.ExitUsage, Message: fmt.Sprintf("--%s %q isn't a time", flag, s),
		Hint: "Use 2026-11-13T02:00Z (UTC), 2026-11-13T03:00+01:00, or now."}
}

func newMaintenanceScheduleCmd(g *globals) *cobra.Command {
	var comps []string
	var from, to, message string
	var dur time.Duration
	cmd := &cobra.Command{
		Use:   "schedule <page> <title> --component NAME --to TIME",
		Short: "Schedule a maintenance window, public at once",
		Long: `Schedule a maintenance window over some of a page's components. It shows
on the page at once; Dock starts it at --from and completes it at --to.
While it is in progress its components read maintenance, alerts about
their services are held (sent when it ends if still open) and their
uptime isn't counted. Times are UTC unless they carry an offset.`,
		Example: `  dock maintenance schedule acme "Database upgrade" --component Database --from 2026-11-13T02:00Z --for 30m -m "Writes pause for up to 5 minutes."
  dock maintenance schedule acme "Network work" --component API --component "Web app" --to 2026-11-13T04:00Z`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			start, err := parseWhen("from", from)
			if err != nil {
				return err
			}
			var end time.Time
			switch {
			case to != "" && dur != 0:
				return &output.Error{Exit: output.ExitUsage, Message: "pass --to or --for, not both"}
			case to != "":
				if end, err = parseWhen("to", to); err != nil {
					return err
				}
			case dur > 0:
				end = start.Add(dur)
			default:
				return &output.Error{Exit: output.ExitUsage, Message: "a window needs an end", Hint: "Pass --to TIME or --for 30m."}
			}
			if len(comps) == 0 {
				return &output.Error{Exit: output.ExitUsage, Message: "a window names the components it takes down", Hint: "Pass --component NAME, once per component."}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			inc, err := send[apitypes.StatusIncident](cmd.Context(), s, http.MethodPost, statusPagePath(args[0])+"/maintenance",
				apitypes.ScheduleMaintenanceRequest{Title: args[1], Components: comps, StartsAt: start, EndsAt: end, Message: message})
			if err != nil {
				return err
			}
			return printIncidentDone(g, inc, "Scheduled maintenance")
		},
	}
	cmd.Flags().StringArrayVar(&comps, "component", nil, "a component it takes down, by public name (repeatable)")
	cmd.Flags().StringVar(&from, "from", "now", "when it starts")
	cmd.Flags().StringVar(&to, "to", "", "when it ends")
	cmd.Flags().DurationVar(&dur, "for", 0, "how long it lasts, instead of --to")
	cmd.Flags().StringVarP(&message, "message", "m", "", "what visitors read")
	return cmd
}

// newMaintenanceMoveCmd posts an update moving a window to state to.
func newMaintenanceMoveCmd(g *globals, use, short string, to apitypes.IncidentState, def string) *cobra.Command {
	var message string
	cmd := &cobra.Command{
		Use:   use + " <id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return postUpdate(cmd.Context(), g, args[0], apitypes.PostIncidentUpdateRequest{State: to, Message: message},
				strings.ToUpper(use[:1])+use[1:]+" maintenance")
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", def, "the update, public")
	return cmd
}

func newMaintenanceExtendCmd(g *globals) *cobra.Command {
	var to string
	var by time.Duration
	cmd := &cobra.Command{
		Use:     "extend <id> (--to TIME | --by DURATION)",
		Short:   "Move a window's end, later or sooner",
		Example: `  dock maintenance extend 4f2kqz7mna --by 30m`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			var end time.Time
			switch {
			case to != "" && by != 0:
				return &output.Error{Exit: output.ExitUsage, Message: "pass --to or --by, not both"}
			case to != "":
				if end, err = parseWhen("to", to); err != nil {
					return err
				}
			case by != 0:
				inc, err := get[apitypes.StatusIncident](cmd.Context(), s, incidentPath(args[0]))
				if err != nil {
					return err
				}
				if inc.EndsAt == nil {
					return &output.Error{Exit: output.ExitConflict, Message: fmt.Sprintf("%s isn't a maintenance window", inc.ID)}
				}
				end = inc.EndsAt.Add(by)
			default:
				return &output.Error{Exit: output.ExitUsage, Message: "pass --to TIME or --by 30m"}
			}
			inc, err := send[apitypes.StatusIncident](cmd.Context(), s, http.MethodPatch, incidentPath(args[0]), apitypes.UpdateIncidentRequest{EndsAt: &end})
			if err != nil {
				return err
			}
			return printIncidentDone(g, inc, "Maintenance now ends "+end.Format("2 Jan 15:04 MST")+":")
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "the new end")
	cmd.Flags().DurationVar(&by, "by", 0, "move the end by this much (negative for sooner)")
	return cmd
}
