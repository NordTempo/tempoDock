package mcp

import (
	"context"
	"fmt"

	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
)

// Error issues (ADR 0062, "One place for errors"), for agents: read only,
// with read permission. Resolving one is for people and the CLI.

// addErrorTools registers the errors tool.
func (h *handler) addErrorTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "list_errors", "List error issues",
		"Error issues grouped by fingerprint: from container logs, browsers and 5xx spikes. With id, one issue's latest sample.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			Project     string `json:"project,omitempty"`
			Environment string `json:"environment,omitempty"`
			Service     string `json:"service,omitempty"`
			State       string `json:"state,omitempty" doc:"open (default), resolved or all."`
			ID          int64  `json:"id,omitempty"`
		}) (mcpkit.Reply, error) {
			if in.ID != 0 {
				d, err := h.core.GetError(ctx, c.Actor, in.ID)
				if err != nil {
					return mcpkit.Reply{}, err
				}
				return mcpkit.Reply{Summary: fmt.Sprintf("%s (%s, %s) in %s/%s/%s: %d errors, first seen %s in release %d, last seen %s.",
					d.Title, d.State, d.Source, d.Project, d.Environment, d.Service, d.Count,
					d.FirstSeen.Format("2006-01-02 15:04"), d.FirstRelease, d.LastSeen.Format("2006-01-02 15:04")), Data: d}, nil
			}
			p, e := filterLimits(c, in.Project, in.Environment)
			l, err := h.core.ListErrors(ctx, c.Actor, core.ErrorFilter{
				Project: p, Environment: e, Service: in.Service, State: in.State, Limit: 50,
			})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			top := make([]string, 0, 5)
			for _, i := range l.Issues[:min(len(l.Issues), 5)] {
				top = append(top, fmt.Sprintf("#%d %s %s/%s: %s (%d)", i.ID, i.State, i.Environment, i.Service, i.Title, i.Count))
			}
			sum := fmt.Sprintf("%d error issues: %s.", len(l.Issues), list(top))
			if l.Truncated {
				sum += " More are not listed; narrow with project, environment or service."
			}
			return mcpkit.Reply{Summary: sum, Data: l}, nil
		})
}
