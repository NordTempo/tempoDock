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
// organization's public status pages say now, and status_incident_open
// and status_incident_update for incidents on them. Changing pages and
// components stays with people (UI, CLI, API).
func (h *handler) addStatusTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "status_pages", "Read status pages",
		"The organization's public status pages: whether each is published and where, and what it says now about each component "+
			"(operational, idle, degraded, partial_outage, major_outage) with its 90-day uptime and the services and uptime checks it is built from. "+
			"With page, only that page.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			Page string `json:"page,omitempty" doc:"A page's name (slug). Default: every page."`
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
		"Opens an incident on a public status page. Title and message are public as soon as it is open, unless draft is true: a draft stays in Dock "+
			"until a person publishes it, so prefer a draft unless you were asked to post. Name the components it affects (public names, from status_pages) "+
			"with an impact each: degraded, partial_outage or major_outage. Write for the page's visitors: no service, server or error names.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			Page string `json:"page" doc:"The page's name (slug), from status_pages"`
			apitypes.OpenIncidentRequest
			Draft bool `json:"draft,omitempty" doc:"Keep it unpublished for a person to publish; message becomes the draft's words"`
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
		"Posts an update on an incident, public at once: on a draft it publishes it, state resolved resolves it. "+
			"components, when given, replaces what it does to components. To change a draft's words without publishing, pass draft_message instead of state and message.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ID           string                   `json:"id" doc:"The incident's ID"`
			State        apitypes.IncidentState   `json:"state,omitempty" enum:"investigating,identified,monitoring,resolved"`
			Message      string                   `json:"message,omitempty" maxLength:"5000" doc:"The update, public"`
			Components   *[]apitypes.StatusImpact `json:"components,omitempty" maxItems:"100"`
			DraftMessage *string                  `json:"draft_message,omitempty" maxLength:"5000" doc:"A draft's new words; publishes nothing"`
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
		"Maintenance windows on a status page, public at once. action schedule (page, title, components, starts_at, ends_at, message): "+
			"while it is in progress its components read maintenance, alerts about their services are held until it ends and their uptime isn't counted. "+
			"action start, complete or cancel (id, message) moves a window sooner than its times; extend (id, ends_at) moves its end.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			Action     string     `json:"action" enum:"schedule,start,complete,extend,cancel"`
			Page       string     `json:"page,omitempty" doc:"The page's name (slug), for schedule"`
			ID         string     `json:"id,omitempty" doc:"The window's ID, for start, complete, extend and cancel"`
			Title      string     `json:"title,omitempty" maxLength:"120"`
			Components []string   `json:"components,omitempty" maxItems:"100" doc:"Public component names, for schedule"`
			StartsAt   *time.Time `json:"starts_at,omitempty" doc:"For schedule; default now"`
			EndsAt     *time.Time `json:"ends_at,omitempty" doc:"For schedule and extend"`
			Message    string     `json:"message,omitempty" maxLength:"5000" doc:"What visitors read"`
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
