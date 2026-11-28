package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
)

// addStatusTools registers status_pages (ADR 0073): what the
// organization's public status pages say now, status_incident_open and
// status_incident_update for incidents on them, and outside_uptime_checks
// for the checks of sites outside Dock components can show (ADR 0055).
// Changing pages and components stays with people (UI, CLI, API).
func (h *handler) addStatusTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "status_pages", "Read status pages",
		"The organization's status pages and their components' status.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			Page string `json:"page,omitempty"`
		}) (mcpkit.Reply, error) {
			slugs := []string{in.Page}
			if in.Page == "" {
				list, err := h.core.ListStatusPages(ctx, c.Actor)
				if err != nil {
					return mcpkit.Reply{}, err
				}
				slugs = slugs[:0]
				for _, p := range list {
					slugs = append(slugs, p.Slug)
				}
			}
			pages := make([]apitypes.StatusPage, 0, len(slugs))
			lines := make([]string, 0, len(slugs))
			for _, slug := range slugs {
				p, err := h.core.GetStatusPage(ctx, c.Actor, slug)
				if err != nil {
					return mcpkit.Reply{}, err
				}
				// The 90-day uptime is enough for an agent: each day's
				// history is the page's to draw and would fill a context.
				for i := range p.Components {
					p.Components[i].Days = nil
				}
				pages = append(pages, p)
				var bad []string
				for _, comp := range p.Components {
					if comp.Status != apitypes.StatusOperational && comp.Status != apitypes.StatusIdle {
						bad = append(bad, fmt.Sprintf("%s %s", comp.Name, strings.ReplaceAll(string(comp.Status), "_", " ")))
					}
				}
				state := "unpublished"
				if p.Published {
					state = "published at " + p.URL
				}
				what := "all operational"
				if len(bad) > 0 {
					what = strings.Join(bad, ", ")
				}
				lines = append(lines, fmt.Sprintf("%s (%s, %d components): %s", p.Slug, state, len(p.Components), what))
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%d status pages: %s.", len(pages), list(lines)), Data: pages}, nil
		})

	mcpkit.AddTool(s, "status_incident_open", "Open a status page incident",
		"Open an incident on a status page. It is public at once unless draft is true: pass draft unless the user asked to post. Public text: no service, server or error names.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			Page string `json:"page"`
			apitypes.OpenIncidentRequest
			Draft bool `json:"draft,omitempty" doc:"Keep it unpublished for the user to review."`
		}) (mcpkit.Reply, error) {
			req := in.OpenIncidentRequest
			if in.Draft {
				req.State = apitypes.IncidentDraft
			}
			inc, err := h.core.OpenIncident(ctx, c.Actor, in.Page, req)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: incidentLine(inc), Data: inc}, nil
		})

	mcpkit.AddTool(s, "status_incident_update", "Post a status page incident update",
		"Post an update on an incident (public; publishes a draft); state resolved resolves it. draft_message edits a draft only.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ID           string                   `json:"id"`
			State        apitypes.IncidentState   `json:"state,omitempty" enum:"investigating,identified,monitoring,resolved"`
			Message      string                   `json:"message,omitempty" maxLength:"5000"`
			Components   *[]apitypes.StatusImpact `json:"components,omitempty" maxItems:"100"`
			DraftMessage *string                  `json:"draft_message,omitempty" maxLength:"5000"`
		}) (mcpkit.Reply, error) {
			var inc apitypes.StatusIncident
			var err error
			if in.DraftMessage != nil {
				if in.State != "" || in.Message != "" {
					return mcpkit.Reply{}, mcpkit.Invalid("pass draft_message alone, or state and message to post")
				}
				inc, err = h.core.UpdateIncident(ctx, c.Actor, in.ID, apitypes.UpdateIncidentRequest{Draft: in.DraftMessage, Components: in.Components})
			} else {
				inc, err = h.core.PostIncidentUpdate(ctx, c.Actor, in.ID, apitypes.PostIncidentUpdateRequest{State: in.State, Message: in.Message, Components: in.Components})
			}
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: incidentLine(inc), Data: inc}, nil
		})

	mcpkit.AddTool(s, "status_maintenance", "Schedule or run a maintenance window",
		"Schedule, start, complete, extend or cancel a maintenance window on a status page.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			Action     string     `json:"action" enum:"schedule,start,complete,extend,cancel"`
			Page       string     `json:"page,omitempty"`
			ID         string     `json:"id,omitempty"`
			Title      string     `json:"title,omitempty" maxLength:"120"`
			Components []string   `json:"components,omitempty" maxItems:"100" doc:"Public names, for schedule."`
			StartsAt   *time.Time `json:"starts_at,omitempty" doc:"Default now."`
			EndsAt     *time.Time `json:"ends_at,omitempty"`
			Message    string     `json:"message,omitempty" maxLength:"5000"`
		}) (mcpkit.Reply, error) {
			var inc apitypes.StatusIncident
			var err error
			moves := map[string]apitypes.IncidentState{"start": apitypes.MaintenanceInProgress, "complete": apitypes.MaintenanceCompleted, "cancel": apitypes.MaintenanceCancelled}
			switch {
			case in.Action == "schedule":
				if in.EndsAt == nil {
					return mcpkit.Reply{}, mcpkit.Invalid("schedule needs ends_at")
				}
				start := time.Now()
				if in.StartsAt != nil {
					start = *in.StartsAt
				}
				inc, err = h.core.ScheduleMaintenance(ctx, c.Actor, in.Page, apitypes.ScheduleMaintenanceRequest{Title: in.Title, Components: in.Components,
					StartsAt: start, EndsAt: *in.EndsAt, Message: in.Message})
			case in.Action == "extend":
				if in.EndsAt == nil {
					return mcpkit.Reply{}, mcpkit.Invalid("extend needs ends_at")
				}
				inc, err = h.core.UpdateIncident(ctx, c.Actor, in.ID, apitypes.UpdateIncidentRequest{EndsAt: in.EndsAt})
			case moves[in.Action] != "":
				msg := in.Message
				if msg == "" {
					msg = map[string]string{"start": "Maintenance started.", "complete": "Maintenance completed.", "cancel": "This maintenance is cancelled."}[in.Action]
				}
				inc, err = h.core.PostIncidentUpdate(ctx, c.Actor, in.ID, apitypes.PostIncidentUpdateRequest{State: moves[in.Action], Message: msg})
			default:
				return mcpkit.Reply{}, mcpkit.Invalid("action is schedule, start, complete, extend or cancel")
			}
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: incidentLine(inc), Data: inc}, nil
		})
}

// addOutsideCheckTool registers outside_uptime_checks: the organization's
// uptime checks of sites outside Dock (ADR 0055), one tool for the four
// actions to keep the tool list short.
func (h *handler) addOutsideCheckTool(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "outside_uptime_checks", "Check sites outside Dock",
		"List, add, update or remove the organization's uptime checks of public sites outside Dock. Down alerts go to the organization's alert channels; status page components can show them. A service's own URLs are checked by Dock already.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			Action          string `json:"action" enum:"list,add,update,remove"`
			ID              int64  `json:"id,omitempty" doc:"For update and remove."`
			URL             string `json:"url,omitempty" maxLength:"2048" doc:"For add: http(s), ports 80 and 443."`
			ExpectStatus    string `json:"expect_status,omitempty" pattern:"^([1-5][0-9][0-9]|2xx|3xx)$" doc:"Default 200."`
			IntervalSeconds int    `json:"interval_seconds,omitempty" minimum:"0" maximum:"3600" doc:"60 to 3600, default 60."`
		}) (mcpkit.Reply, error) {
			switch in.Action {
			case "list":
				checks, err := h.core.ListOutsideUptimeChecks(ctx, c.Actor)
				if err != nil {
					return mcpkit.Reply{}, err
				}
				lines := make([]string, len(checks))
				for i, ch := range checks {
					lines[i] = checkLine(ch)
				}
				return mcpkit.Reply{Summary: fmt.Sprintf("%d outside uptime checks: %s.", len(checks), list(lines)), Data: checks}, nil
			case "add":
				ch, err := h.core.AddOutsideUptimeCheck(ctx, c.Actor, apitypes.AddUptimeCheckRequest{URL: in.URL, ExpectStatus: in.ExpectStatus, IntervalSeconds: in.IntervalSeconds})
				if err != nil {
					return mcpkit.Reply{}, err
				}
				return mcpkit.Reply{Summary: "Added " + checkLine(ch) + ".", Data: ch}, nil
			case "update":
				req := apitypes.UpdateUptimeCheckRequest{}
				if in.ExpectStatus != "" {
					req.ExpectStatus = &in.ExpectStatus
				}
				if in.IntervalSeconds != 0 {
					req.IntervalSeconds = &in.IntervalSeconds
				}
				ch, err := h.core.UpdateOutsideUptimeCheck(ctx, c.Actor, in.ID, req)
				if err != nil {
					return mcpkit.Reply{}, err
				}
				return mcpkit.Reply{Summary: "Updated " + checkLine(ch) + ".", Data: ch}, nil
			case "remove":
				if err := h.core.RemoveOutsideUptimeCheck(ctx, c.Actor, in.ID); err != nil {
					return mcpkit.Reply{}, err
				}
				return mcpkit.Reply{Summary: fmt.Sprintf("Removed outside uptime check %d with its history.", in.ID)}, nil
			}
			return mcpkit.Reply{}, mcpkit.Invalid("action is list, add, update or remove")
		})
}

// checkLine is an uptime check in a phrase for the model.
func checkLine(ch apitypes.UptimeCheck) string {
	s := fmt.Sprintf("check %d of %s (%s, every %ds, expects %s", ch.ID, ch.URL, ch.State, ch.IntervalSeconds, ch.ExpectStatus)
	if len(ch.UsedBy) > 0 {
		uses := make([]string, len(ch.UsedBy))
		for i, u := range ch.UsedBy {
			uses[i] = u.Page + "/" + u.Component
		}
		s += ", shown by " + strings.Join(uses, ", ")
	}
	return s + ")"
}

// incidentLine is an incident in a sentence for the model.
func incidentLine(inc apitypes.StatusIncident) string {
	if inc.State == apitypes.IncidentDraft {
		return fmt.Sprintf("Incident %s %q is a draft on %s: not public until a person publishes it in Dock.", inc.ID, inc.Title, inc.Page)
	}
	where := ""
	if inc.URL != "" {
		where = " at " + inc.URL
	}
	what := "Incident"
	if inc.Kind == apitypes.KindMaintenance {
		what = "Maintenance"
	}
	return fmt.Sprintf("%s %s %q on %s is %s, public%s.", what, inc.ID, inc.Title, inc.Page, strings.ReplaceAll(string(inc.State), "_", " "), where)
}
