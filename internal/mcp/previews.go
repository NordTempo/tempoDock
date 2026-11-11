package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
)

// Preview environments per pull request (ADR 0056), read only: an agent
// working on a pull request finds its preview's address and status.

// PreviewInput names a preview: by its environment, or by its pull
// request's number or head branch among the project's open previews.
type PreviewInput struct {
	ProjectInput
	Environment string `json:"environment,omitempty" doc:"e.g. pr-12."`
	PullRequest int64  `json:"pull_request,omitempty"`
	Branch      string `json:"branch,omitempty"`
}

// addPreviewTools registers get_previews and get_preview.
func (h *handler) addPreviewTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "get_previews", "Preview settings and open previews",
		"A project's pull request preview settings and open previews.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in ProjectInput) (mcpkit.Reply, error) {
			p, err := projectOf(c, in)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			set, err := h.core.GetPreviews(ctx, c.Actor, p)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			summary := fmt.Sprintf("Previews of %s are off", p)
			if set.Enabled {
				summary = fmt.Sprintf("Previews of %s are on, cloned from %s, at most %d open", p, set.Base, set.MaxPreviews)
			}
			lines := make([]string, len(set.Previews))
			for i, pv := range set.Previews {
				lines[i] = previewLine(pv)
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%s. %d open: %s.", summary, len(lines), list(lines)), Data: set}, nil
		})

	mcpkit.AddTool(s, "get_preview", "Get a preview",
		"One pull request's preview: status, addresses, commit. Find it by environment, pull_request or branch.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in PreviewInput) (mcpkit.Reply, error) {
			pv, err := h.findPreview(ctx, c, in)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			summary := previewLine(pv)
			if pv.Event != "" {
				summary += "; last: " + pv.Event
			}
			return mcpkit.Reply{Summary: summary + ".", Data: pv}, nil
		})
}

// addSleepTools registers sleep_environment and wake_environment
// (ADR 0070).
func (h *handler) addSleepTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "sleep_environment", "Put an environment to sleep",
		"Put a non-production environment to sleep, freeing memory; a request, deploy or run wakes it.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in EnvInput) (mcpkit.Reply, error) {
			ref, err := envOf(c, in)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			e, err := h.core.SleepEnvironment(ctx, c.Actor, ref)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%s/%s is asleep; the next request to its domains wakes it.", e.Project, e.Name), Data: e}, nil
		})
	mcpkit.AddTool(s, "wake_environment", "Wake an environment",
		"Wake an asleep environment and wait until its services are healthy.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in EnvInput) (mcpkit.Reply, error) {
			ref, err := envOf(c, in)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			e, err := h.core.WakeEnvironment(ctx, c.Actor, ref)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%s/%s is awake.", e.Project, e.Name), Data: e}, nil
		})
}

// findPreview resolves a PreviewInput: the environment's preview when it
// is named (or none of the others is), otherwise the one open preview
// whose pull request number and branch match.
func (h *handler) findPreview(ctx context.Context, c *call, in PreviewInput) (apitypes.Preview, error) {
	if in.Environment != "" || in.PullRequest == 0 && in.Branch == "" {
		ref, err := envOf(c, EnvInput{ProjectInput: in.ProjectInput, Environment: in.Environment})
		if err != nil {
			return apitypes.Preview{}, mcpkit.Invalid("pass environment, pull_request or branch (get_previews lists the open previews)")
		}
		return h.core.GetPreview(ctx, c.Actor, ref)
	}
	p, err := projectOf(c, in.ProjectInput)
	if err != nil {
		return apitypes.Preview{}, err
	}
	set, err := h.core.GetPreviews(ctx, c.Actor, p)
	if err != nil {
		return apitypes.Preview{}, err
	}
	var found []apitypes.Preview
	for _, pv := range set.Previews {
		if (in.PullRequest == 0 || pv.Number == in.PullRequest) && (in.Branch == "" || pv.Branch == in.Branch) {
			found = append(found, pv)
		}
	}
	var what []string
	if in.PullRequest != 0 {
		what = append(what, fmt.Sprintf("pull request #%d", in.PullRequest))
	}
	if in.Branch != "" {
		what = append(what, "branch "+in.Branch)
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return apitypes.Preview{}, errf(apitypes.CodeNotFound, "%s has no open preview of %s; get_previews lists them, and a closed one is read by its environment", p, strings.Join(what, " on "))
	}
	envs := make([]string, len(found))
	for i, pv := range found {
		envs[i] = pv.Environment
	}
	return apitypes.Preview{}, mcpkit.Invalid("%s has %d open previews of %s (%s); pass environment", p, len(found), strings.Join(what, " on "), strings.Join(envs, ", "))
}

// previewLine sums up a preview: environment, pull request, status, first
// address and commit.
func previewLine(pv apitypes.Preview) string {
	s := fmt.Sprintf("%s (%s#%d %q, %s) is %s", pv.Environment, pv.Repo, pv.Number, pv.Title, pv.Branch, pv.Status)
	if len(pv.URLs) > 0 {
		s += " at " + pv.URLs[0].URL
	}
	if pv.Commit != "" {
		s += ", commit " + pv.Commit[:min(7, len(pv.Commit))]
	}
	return s
}
