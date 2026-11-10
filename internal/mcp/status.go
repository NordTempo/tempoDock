package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
)

// addStatusTools registers status_pages (ADR 0073): what the
// organization's public status pages say now. Changing pages stays with
// people (UI, CLI, API).
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
}
