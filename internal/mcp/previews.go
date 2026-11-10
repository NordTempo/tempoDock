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
	Environment string `json:"environment,omitempty" doc:"The preview's environment, e.g. pr-12. Default: found by pull_request or branch, else the environment your token is limited to."`
	PullRequest int64  `json:"pull_request,omitempty" doc:"The pull request's number, e.g. 12."`
	Branch      string `json:"branch,omitempty" doc:"The pull request's head branch, e.g. fix-checkout."`
}

// addPreviewTools registers get_previews and get_preview.
func (h *handler) addPreviewTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "get_previews", "Preview settings and open previews",
		"Whether the project's pull requests get preview environments, cloned from which environment, the limits, the variables whose values previews copy, "+
			"and the open previews with their pull request, head branch and commit, status (waiting, deploying, live, failed) and addresses.",
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
		"One pull request's preview environment: its status (waiting, deploying, live, failed, closed), addresses, the commit it runs and what happened last. "+
			"Name it by environment (pr-12), or by pull_request number or head branch to find it among the open previews. "+
			"A new commit on the pull request redeploys it: call again until the status is live or failed, then read release_details of a failed service.",
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
		"Stop every service of a non-production environment, keeping its containers, to free their memory. The next request to its domains, a deploy, "+
			"run_command or the Data panel wakes it. Previews sleep on their own after a while without requests; this is for doing it now, only when asked.",
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
		"Start an asleep environment's services again, databases first, and wait until each passed its health check. Opening one of its addresses does the same; "+
			"call this before testing a preview whose sleep state (in project_overview or get_preview) is asleep.",
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
