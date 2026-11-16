package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
)

// App templates for agents (ADR 0083): find a template and use it.

// addTemplateTools registers the template tools.
func (h *handler) addTemplateTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "list_templates", "List app templates",
		"Apps (Umami, n8n, Forgejo, ...) and databases to create in one step. With name: its services and inputs.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			Name string `json:"name,omitempty"`
		}) (mcpkit.Reply, error) {
			if in.Name != "" {
				t, err := h.core.GetAppTemplate(ctx, c.Actor, in.Name)
				if err != nil {
					return mcpkit.Reply{}, err
				}
				var inputs []string
				for _, i := range t.Inputs {
					note := string(i.Kind)
					if i.Default != "" {
						note += ", default " + i.Default
					} else if !i.Optional {
						note += ", required"
					}
					inputs = append(inputs, fmt.Sprintf("%s (%s)", i.Key, note))
				}
				if len(inputs) == 0 {
					inputs = []string{"none"}
				}
				return mcpkit.Reply{Summary: fmt.Sprintf("%s: %d services, %d MB. Inputs: %s.", t.Title, len(t.Services), t.MemoryMB, strings.Join(inputs, ", ")), Data: t}, nil
			}
			list, err := h.core.ListAppTemplates(ctx, c.Actor)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			type item struct {
				Name     string `json:"name"`
				Summary  string `json:"summary"`
				Category string `json:"category"`
				MemoryMB int    `json:"memory_mb"`
			}
			items := make([]item, 0, len(list))
			for _, t := range list {
				items = append(items, item{t.Name, t.Summary, t.Category, t.MemoryMB})
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%d templates. Pass name for one's inputs.", len(items)), Data: items}, nil
		})

	mcpkit.AddTool(s, "use_template", "Use an app template",
		"Create a template's services in an environment and deploy them. Main service is name (default the template's), others NAME-<part>. plan: only say what it creates.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			EnvInput
			Template string            `json:"template"`
			Name     string            `json:"name,omitempty"`
			Domain   string            `json:"domain,omitempty" doc:"Main service's hostname; default a generated one."`
			Inputs   map[string]string `json:"inputs,omitempty" doc:"By key, from list_templates with name."`
			Plan     bool              `json:"plan,omitempty"`
			WaitInput
		}) (mcpkit.Reply, error) {
			ref, err := envOf(c, in.EnvInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			wait, err := in.duration()
			if err != nil {
				return mcpkit.Reply{}, err
			}
			res, err := h.core.UseAppTemplate(ctx, c.Actor, ref, in.Template, apitypes.UseTemplateRequest{Name: in.Name, Domain: in.Domain, Inputs: in.Inputs, Plan: in.Plan})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			names := strings.Join(res.Services, ", ")
			if in.Plan {
				return mcpkit.Reply{Summary: fmt.Sprintf("Would create %s. %s Run again without plan to create them.", names, planSummary(res.Plan)), Data: res.Plan}, nil
			}
			views, err := h.waitReleases(ctx, c, res.Releases, wait)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			summary := fmt.Sprintf("Created %s in %s/%s. %s", names, ref.Project, ref.Environment, waitSummary("Deployed", views))
			if len(res.Failed) > 0 {
				summary += " Not done: " + strings.Join(res.Failed, "; ") + "."
			}
			return mcpkit.Reply{Summary: summary, Data: map[string]any{"services": res.Services, "releases": views, "failed": res.Failed}}, nil
		})
}
