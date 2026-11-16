package mcp

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
)

// Tool inputs. Project and environment default to what the token is
// limited to, so an agent with a token for shop/staging only names the
// service.

// ProjectInput names a project. The instructions say once what the
// defaults are, rather than every tool's schema.
type ProjectInput struct {
	Project string `json:"project,omitempty"`
}

// EnvInput names an environment.
type EnvInput struct {
	ProjectInput
	Environment string `json:"environment,omitempty"`
}

// ServiceInput names a service.
type ServiceInput struct {
	EnvInput
	Service string `json:"service"`
}

// projectOf, envOf and serviceOf resolve an input to a reference, filling in the
// token's limits. Core still checks the result against them.
func projectOf(c *call, in ProjectInput) (string, error) {
	if p := in.Project; p != "" {
		return p, nil
	}
	if c.Actor.ProjectName == "" {
		return "", mcpkit.Invalid("pass project: your token isn't limited to one (list_projects shows them)")
	}
	return c.Actor.ProjectName, nil
}

func envOf(c *call, in EnvInput) (core.EnvRef, error) {
	p, err := projectOf(c, in.ProjectInput)
	if err != nil {
		return core.EnvRef{}, err
	}
	e := in.Environment
	if e == "" && p == c.Actor.ProjectName {
		e = c.Actor.EnvironmentName
	}
	if e == "" {
		return core.EnvRef{}, mcpkit.Invalid("pass environment: your token isn't limited to one (project_overview lists them)")
	}
	return core.EnvRef{Project: p, Environment: e}, nil
}

func serviceOf(c *call, in ServiceInput) (core.ServiceRef, error) {
	e, err := envOf(c, in.EnvInput)
	if err != nil {
		return core.ServiceRef{}, err
	}
	if in.Service == "" {
		return core.ServiceRef{}, mcpkit.Invalid("pass service (project_overview lists them)")
	}
	return core.ServiceRef{Project: e.Project, Environment: e.Environment, Service: in.Service}, nil
}

// whoLine says who the caller is and what it may do: whoami's summary, and
// the last line of the instructions.
func whoLine(me apitypes.Me) string {
	where := "every project"
	switch {
	case me.Environment != "":
		where = me.Project + "/" + me.Environment
	case me.Project != "":
		where = "project " + me.Project
	}
	can := map[apitypes.Scope]string{
		apitypes.ScopeRead:   "read status, releases, logs, resource use, variable names and the audit log, and wait for releases",
		apitypes.ScopeDeploy: "read, and deploy, redeploy, roll back, promote, stop and start services, create services, stage and apply variables, settings and volumes, add and remove domains",
		apitypes.ScopeAdmin:  "everything, including secret values",
	}[me.Scope]
	return fmt.Sprintf("%s %q, scope %s, limited to %s: may %s.", me.Kind, me.Name, me.Scope, where, can)
}

// filterLimits fills a filter's project and environment from the token's
// limits, as envOf does for the tools that act on one: tools/list leaves
// both out for a token limited to them.
func filterLimits(c *call, project, env string) (string, string) {
	if project == "" {
		project = c.Actor.ProjectName
	}
	if env == "" && project == c.Actor.ProjectName {
		env = c.Actor.EnvironmentName
	}
	return project, env
}

// addPlatformTools registers the tools for projects, services, releases,
// logs, variables, domains, previews, servers, the audit log and alerts.
func (h *handler) addPlatformTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "whoami", "Who am I",
		"The token's name, scope and the project and environment it is limited to.",
		mcpkit.ReadOnly, func(_ context.Context, c *call, _ struct{}) (mcpkit.Reply, error) {
			me := c.Actor.Me()
			return mcpkit.Reply{Summary: whoLine(me), Data: me}, nil
		})

	mcpkit.AddTool(s, "list_projects", "List projects",
		"Projects you can see.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, _ struct{}) (mcpkit.Reply, error) {
			ps, err := h.core.ListProjects(ctx, c.Actor, false)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			names := make([]string, len(ps))
			for i, p := range ps {
				names[i] = p.Name
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%d projects: %s", len(names), strings.Join(names, ", ")), Data: map[string][]string{"projects": names}}, nil
		})

	mcpkit.AddTool(s, "project_overview", "Project overview",
		"Best first look: a project's environments (kind, locked, server online) and services: state, active, in-flight and last failed release, domains, staged changes.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in ProjectInput) (mcpkit.Reply, error) {
			p, err := projectOf(c, in)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			o, err := h.core.Overview(ctx, c.Actor, p)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			v := viewOverview(o)
			parts := make([]string, len(v.Environments))
			for i, e := range v.Environments {
				parts[i] = fmt.Sprintf("%s (%d services)", e.Name, len(e.Services))
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("Project %s: %s.", v.Project, strings.Join(parts, ", ")), Data: v}, nil
		})

	mcpkit.AddTool(s, "export_project", "Export project config",
		"A project's live config as a project file: environments, services, settings, volumes, domains, variable names.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in ProjectInput) (mcpkit.Reply, error) {
			p, err := projectOf(c, in)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			ex, err := h.core.ExportProject(ctx, c.Actor, p)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("Project %s: %d environments.", ex.Project, len(ex.Environments)), Data: ex}, nil
		})

	mcpkit.AddTool(s, "plan_project_file", "Plan a project file",
		"Diff a project file (export_project's format, TOML or JSON) against live state, like terraform plan. Changes nothing.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in projectFileInput) (mcpkit.Reply, error) {
			p, err := projectOf(c, in.ProjectInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			plan, err := h.core.PlanProject(ctx, c.Actor, p, in.request(c))
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: planSummary(plan), Data: plan}, nil
		})

	mcpkit.AddTool(s, "apply_project_file", "Stage a project file",
		"Stage a project file's changes; deploy also applies them. Creates missing projects and environments. Secret values go in values.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ProjectInput
			File        string                   `json:"file"`
			Environment string                   `json:"environment,omitempty"`
			Prune       bool                     `json:"prune,omitempty" doc:"Also remove what the file doesn't mention (never volumes or environments)."`
			Values      []apitypes.VariableValue `json:"values,omitempty" doc:"Values of the secrets the file names."`
			Deploy      bool                     `json:"deploy,omitempty"`
			WaitInput
		}) (mcpkit.Reply, error) {
			p, err := projectOf(c, in.ProjectInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			wait, err := in.duration()
			if err != nil {
				return mcpkit.Reply{}, err
			}
			req := apitypes.ApplyRequest{File: in.File, Environment: cmp.Or(in.Environment, c.Actor.EnvironmentName), Prune: in.Prune, Values: in.Values, Deploy: in.Deploy}
			res, err := h.core.ApplyProject(ctx, c.Actor, p, req)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			staged := 0
			for _, pc := range res.Staged {
				staged += pc.Count
			}
			summary := planSummary(res.Plan) + fmt.Sprintf(" %d changes now staged across %d environments.", staged, len(res.Staged))
			if !in.Deploy {
				return mcpkit.Reply{Summary: summary + " Review with show_changes, then apply_changes per environment.", Data: res}, nil
			}
			views := map[string][]releaseView{}
			for _, a := range res.Applied {
				vs, err := h.waitReleases(ctx, c, a.Releases, wait)
				if err != nil {
					return mcpkit.Reply{}, err
				}
				views[a.Environment] = vs
				summary += " " + a.Environment + ": " + waitSummary("Applied", vs)
				if len(a.Failed) > 0 {
					summary += " Still staged: " + strings.Join(a.Failed, "; ") + "."
				}
			}
			return mcpkit.Reply{Summary: summary, Data: map[string]any{"plan": res.Plan, "staged": res.Staged, "applied": res.Applied, "releases": views}}, nil
		})

	mcpkit.AddTool(s, "import_compose", "Convert a Compose file",
		"Convert a docker-compose.yml into a project file and plan it. Changes nothing; stage the file with apply_project_file.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			ProjectInput
			Compose  string            `json:"compose"`
			EnvFiles map[string]string `json:"env_files,omitempty" doc:"env_file paths it reads, and .env, by name."`
		}) (mcpkit.Reply, error) {
			p, err := projectOf(c, in.ProjectInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			res, err := h.core.ImportCompose(ctx, c.Actor, p, apitypes.ComposeImportRequest{Compose: in.Compose, EnvFiles: in.EnvFiles})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			summary := fmt.Sprintf("Converted into project %s with %d warnings and %d secrets. ", p, len(res.Warnings), len(res.Secrets)) + planSummary(res.Plan)
			for _, b := range res.Builds {
				summary += fmt.Sprintf(" %s builds from %s: deploy it from there after applying.", b.Service, b.Context)
			}
			return mcpkit.Reply{Summary: summary, Data: res}, nil
		})

	mcpkit.AddTool(s, "service_status", "Service status",
		"A service's live state: running, crashed (exit code, restarts, OOM) or stopped, its active release and a failed or in-flight one.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in ServiceInput) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			svc, err := h.core.GetService(ctx, c.Actor, ref)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			rels, err := h.core.ListReleases(ctx, c.Actor, ref, 1)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			v := viewService(svc)
			summary := fmt.Sprintf("%s is %s", ref, v.State)
			if v.State == apitypes.ServiceRunning && v.Health == apitypes.HealthUnhealthy {
				summary += " but failing its health check"
			}
			short := func(r apitypes.Release) *releaseSummary {
				return &releaseSummary{Number: r.Number, Status: r.Status, Image: r.ImageRef, Error: r.Error, ErrorCode: r.ErrorCode, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt}
			}
			if v.StoppedAt != nil {
				summary += fmt.Sprintf(" (by %s; start_service runs it again)", v.StoppedBy)
			}
			if n := svc.ActiveRelease; n != nil {
				if v.StoppedAt != nil {
					summary += fmt.Sprintf(", keeping release %d", *n)
				} else {
					summary += fmt.Sprintf(", serving release %d", *n)
				}
				if len(rels) > 0 && rels[0].Number == *n {
					v.Active = short(rels[0])
				} else if active, err := h.core.GetRelease(ctx, c.Actor, ref, *n); err == nil {
					active.Events = nil
					v.Active = short(active)
				} else {
					return mcpkit.Reply{}, err
				}
			}
			if len(rels) > 0 && (svc.ActiveRelease == nil || rels[0].Number != *svc.ActiveRelease) {
				v.Latest = short(rels[0])
				summary += "; latest " + describeRelease(rels[0])
			}
			return mcpkit.Reply{Summary: summary + ".", Data: v}, nil
		})

	mcpkit.AddTool(s, "list_releases", "List releases",
		"A service's releases, newest first.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Limit int `json:"limit,omitempty" doc:"Default 10."`
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			n, err := mcpkit.Limit("limit", in.Limit, 10, 100)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			rels, err := h.core.ListReleases(ctx, c.Actor, ref, n)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%d releases of %s.", len(rels), ref), Data: viewReleases(rels)}, nil
		})

	mcpkit.AddTool(s, "release_details", "Release details",
		"One release (default the latest) with its deploy steps, and for a failed one the error and last log lines.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Number   int64 `json:"number,omitempty"`
			LogLines int   `json:"log_lines,omitempty" doc:"Default 40."`
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			lines, err := mcpkit.Limit("log_lines", in.LogLines, failureLogLines, 500)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			number := in.Number
			if number == 0 {
				rels, err := h.core.ListReleases(ctx, c.Actor, ref, 1)
				if err != nil {
					return mcpkit.Reply{}, err
				}
				if len(rels) == 0 {
					return mcpkit.Reply{}, errf(apitypes.CodeNotFound, "%s has no releases yet; deploy it first", ref)
				}
				number = rels[0].Number
			}
			v, err := h.releaseDetails(ctx, c.Actor, ref, number, lines)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			summary := fmt.Sprintf("Release %d of %s: %s", v.Number, ref, v.Status)
			if v.Error != "" {
				summary += ": " + v.Error
			}
			return mcpkit.Reply{Summary: summary + ".", Data: v}, nil
		})

	mcpkit.AddTool(s, "diagnose", "Diagnose a failure",
		"Why a release failed or the live one crash loops or is unhealthy: the likely cause and fix (cause.rule empty: no rule matched, read the evidence), what was ruled out, logs and what changed. Call first after a failed deploy.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Release int64 `json:"release,omitempty" doc:"Default: the newest diagnosis."`
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			list, err := h.core.ListDiagnoses(ctx, c.Actor, ref, in.Release, 1)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			if len(list) == 0 {
				what := fmt.Sprintf("%s has no diagnoses: Dock keeps one for each failed release, for 30 days", ref)
				if in.Release > 0 {
					what = fmt.Sprintf("release %d of %s has no diagnosis: only failed releases have one", in.Release, ref)
				}
				return mcpkit.Reply{}, errf(apitypes.CodeNotFound, "%s", what)
			}
			d, err := h.core.GetDiagnosis(ctx, c.Actor, ref, list[0].ID)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			summary := d.Headline() + ". "
			if d.Cause.Rule != "" {
				summary += "Likely cause: " + d.Cause.Summary + "."
				if f := d.Cause.Fix; f != nil && f.CLI != "" {
					summary += " Fix: " + f.CLI + "."
				}
			} else {
				summary += "No rule matched; the evidence is below. Error: " + d.Failure.Error
			}
			return mcpkit.Reply{Summary: summary, Data: d}, nil
		})

	h.addReleaseTools(s)

	mcpkit.AddTool(s, "logs", "Service logs",
		"A service's recent output, from the active release or an earlier one. Lines on stderr are marked [stderr], which doesn't make them errors.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Release int64  `json:"release,omitempty"`
			Tail    int    `json:"tail,omitempty" doc:"Default 100, or 1000 with grep or stderr; at most 1000."`
			Since   string `json:"since,omitempty" doc:"e.g. 10m or an RFC 3339 time."`
			Grep    string `json:"grep,omitempty" doc:"Plain text, after tail; lower case matches any case."`
			Stderr  bool   `json:"stderr,omitempty"`
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			// A filter over the last 100 lines rarely finds what it's for.
			def := 100
			if in.Grep != "" || in.Stderr {
				def = 1000
			}
			n, err := mcpkit.Limit("tail", in.Tail, def, 1000)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			q := core.LogQuery{Release: in.Release, Tail: n, Grep: in.Grep, Stderr: in.Stderr}
			if in.Since != "" {
				if d, err := time.ParseDuration(in.Since); err == nil {
					q.Since = time.Now().Add(-d)
				} else if t, err := time.Parse(time.RFC3339, in.Since); err == nil {
					q.Since = t
				} else {
					return mcpkit.Reply{}, mcpkit.Invalid("since %q must be a duration like 10m or an RFC 3339 time", in.Since)
				}
			}
			lines, err := h.logLines(ctx, c.Actor, ref, q, n)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%d log lines of %s.", len(lines), ref), Data: map[string][]string{"lines": lines}}, nil
		})

	h.addConfigTools(s)
	h.addSnapshotTools(s)
	h.addAnalyticsTools(s)
	h.addErrorTools(s)
	h.addTraceTools(s)
	h.addMetricTools(s)
	h.addVulnTools(s)
	h.addRunTools(s)
	h.addJobTools(s)
	h.addPreviewTools(s)
	h.addSleepTools(s)
	h.addStatusTools(s)
	h.addTemplateTools(s)

	mcpkit.AddTool(s, "list_servers", "List servers",
		"Servers and their status. Operators only.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, _ struct{}) (mcpkit.Reply, error) {
			ss, err := h.core.ListServers(ctx, c.Actor)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			out := make([]serverView, len(ss))
			parts := make([]string, len(ss))
			for i, sv := range ss {
				out[i] = viewServer(sv)
				parts[i] = sv.Name + " " + string(sv.Status)
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%d servers: %s.", len(out), strings.Join(parts, ", ")), Data: out}, nil
		})

	// Read-only on purpose: a tool that takes or returns registry
	// passwords would put them in an agent's context (ADR 0051).
	mcpkit.AddTool(s, "list_registry_credentials", "List registry credentials",
		"Registries the organization signs in to (host and user). Admins only.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, _ struct{}) (mcpkit.Reply, error) {
			list, err := h.core.ListRegistryCredentials(ctx, c.Actor)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			parts := make([]string, len(list))
			for i, rc := range list {
				parts[i] = rc.Host + " as " + rc.Username
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%d registry credentials: %s.", len(list), strings.Join(parts, ", ")), Data: list}, nil
		})

	mcpkit.AddTool(s, "audit_log", "Audit log",
		"Audit events, newest first: who did what, where, when.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			Project     string `json:"project,omitempty"`
			Environment string `json:"environment,omitempty"`
			Service     string `json:"service,omitempty"`
			Action      string `json:"action,omitempty" doc:"Prefix, e.g. release. or domain.add"`
			Before      int64  `json:"before,omitempty"`
			Limit       int    `json:"limit,omitempty"`
		}) (mcpkit.Reply, error) {
			n, err := mcpkit.Limit("limit", in.Limit, 50, 500)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			p, e := filterLimits(c, in.Project, in.Environment)
			evs, err := h.core.ListAudit(ctx, c.Actor, core.AuditFilter{
				Project: p, Environment: e, Service: in.Service, Action: in.Action, Before: in.Before, Limit: n,
			})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%d audit events.", len(evs)), Data: evs}, nil
		})

	h.addServiceTools(s)
}

// addReleaseTools registers the tools that start releases, and
// wait_for_release. Each waits for its releases to finish (waitReleases).
func (h *handler) addReleaseTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "wait_for_release", "Wait for a release",
		"Wait for a deploying release (default the in-flight one) to end, and return it like deploy_image.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Number int64 `json:"number,omitempty"`
			WaitInput
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			wait, err := in.duration()
			if err != nil {
				return mcpkit.Reply{}, err
			}
			var rel apitypes.Release
			if in.Number > 0 {
				if rel, err = h.core.GetRelease(ctx, c.Actor, ref, in.Number); err != nil {
					return mcpkit.Reply{}, err
				}
			} else {
				// Only one release of a service deploys at a time, and it
				// is the newest.
				rels, err := h.core.ListReleases(ctx, c.Actor, ref, 1)
				if err != nil {
					return mcpkit.Reply{}, err
				}
				if len(rels) == 0 {
					return mcpkit.Reply{}, errf(apitypes.CodeNotFound, "%s has no releases yet; deploy it first", ref)
				}
				rel = rels[0]
			}
			action := "Waited"
			if rel.Status.Done() {
				action = "Nothing in flight"
				if in.Number > 0 {
					action = "Already done"
				}
			}
			return h.waitOne(ctx, c, action, rel, wait)
		})

	mcpkit.AddTool(s, "deploy_image", "Deploy an image",
		"Deploy an image to a service with its staged changes, and wait. Source code deploys need `dock deploy`.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Image string `json:"image,omitempty" doc:"Default: the service's image."`
			WaitInput
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			wait, err := in.duration()
			if err != nil {
				return mcpkit.Reply{}, err
			}
			rel, err := h.core.DeployImage(ctx, c.Actor, ref, apitypes.DeployImageRequest{Image: in.Image})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return h.waitOne(ctx, c, "Deploy", rel, wait)
		})

	mcpkit.AddTool(s, "redeploy", "Redeploy (restart)",
		"Restart a service: a new release copying the active one, health checked. Staged changes are not included.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			WaitInput
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			wait, err := in.duration()
			if err != nil {
				return mcpkit.Reply{}, err
			}
			rel, err := h.core.Restart(ctx, c.Actor, ref)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return h.waitOne(ctx, c, "Redeploy", rel, wait)
		})

	mcpkit.AddTool(s, "stop_service", "Stop a service",
		"Stop a service until started or deployed: no container, its domains answer 503. Only when asked.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in ServiceInput) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			svc, err := h.core.StopService(ctx, c.Actor, ref)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("Stopped %s; its domains answer 503 until start_service or a deploy.", ref), Data: viewService(svc)}, nil
		})

	mcpkit.AddTool(s, "start_service", "Start a stopped service",
		"Start a stopped service and wait.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			WaitInput
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			wait, err := in.duration()
			if err != nil {
				return mcpkit.Reply{}, err
			}
			rel, err := h.core.StartService(ctx, c.Actor, ref)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			vs, err := h.waitReleases(ctx, c, []apitypes.Release{rel}, wait)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			summary := waitSummary("Start", vs)
			if v := vs[0]; v.Status == apitypes.ReleaseFailed {
				// Not waitSummary's "the previous one still serves": nothing does.
				summary = fmt.Sprintf("Start: %s #%d failed (%s). The service stays stopped. Read error and logs, fix, then start_service again or deploy.", v.Service, v.Number, v.ErrorCode)
			}
			return mcpkit.Reply{Summary: summary, Data: vs[0]}, nil
		})

	mcpkit.AddTool(s, "rollback", "Roll back",
		"Roll back to an earlier release that served traffic, and wait. Not for a failed deploy: it never got traffic.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Number int64 `json:"number"`
			WaitInput
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			if in.Number <= 0 {
				return mcpkit.Reply{}, mcpkit.Invalid("pass number: the release to roll back to (list_releases shows them)")
			}
			wait, err := in.duration()
			if err != nil {
				return mcpkit.Reply{}, err
			}
			rel, err := h.core.Rollback(ctx, c.Actor, ref, in.Number)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return h.waitOne(ctx, c, "Rollback", rel, wait)
		})

	mcpkit.AddTool(s, "promote", "Promote",
		"Deploy the images an environment runs to another of the project (e.g. staging to production), with the target's own config, and wait.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ProjectInput
			From     string   `json:"from"`
			To       string   `json:"to"`
			Services []string `json:"services,omitempty" doc:"Default: all."`
			WaitInput
		}) (mcpkit.Reply, error) {
			p, err := projectOf(c, in.ProjectInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			wait, err := in.duration()
			if err != nil {
				return mcpkit.Reply{}, err
			}
			res, err := h.core.Promote(ctx, c.Actor, p, apitypes.PromoteRequest{From: in.From, To: in.To, Services: in.Services})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			rels := make([]apitypes.Release, len(res.Promoted))
			for i, pr := range res.Promoted {
				rels[i] = pr.Release
			}
			vs, err := h.waitReleases(ctx, c, rels, wait)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			summary := waitSummary(fmt.Sprintf("Promote %s to %s", res.From, res.To), vs)
			if len(res.Skipped) > 0 {
				skipped := make([]string, len(res.Skipped))
				for i, sk := range res.Skipped {
					skipped[i] = sk.Service + " (" + string(sk.Reason) + ")"
				}
				summary += " Skipped: " + strings.Join(skipped, ", ") + "."
			}
			return mcpkit.Reply{Summary: summary, Data: map[string]any{"releases": vs, "skipped": res.Skipped}}, nil
		})

	mcpkit.AddTool(s, "clone_environment", "Clone an environment",
		"Create an environment as a copy of another: services, settings, variables and empty volumes, not domains or data. Secret values that can't be copied are listed. deploy also deploys from's images.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ProjectInput
			From       string `json:"from"`
			To         string `json:"to"`
			Kind       string `json:"kind,omitempty" doc:"production or non_production."`
			Deploy     bool   `json:"deploy,omitempty"`
			CopyValues bool   `json:"copy_values,omitempty" doc:"Admin only."`
			WaitInput
		}) (mcpkit.Reply, error) {
			p, err := projectOf(c, in.ProjectInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			wait, err := in.duration()
			if err != nil {
				return mcpkit.Reply{}, err
			}
			res, err := h.core.CloneEnvironment(ctx, c.Actor, core.EnvRef{Project: p, Environment: in.From}, apitypes.CloneEnvironmentRequest{
				Name: in.To, Kind: apitypes.EnvironmentKind(in.Kind), Deploy: in.Deploy, CopyValues: in.CopyValues,
			})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			e := res.Environment
			summary := fmt.Sprintf("Cloned %s into %s (%s): %d services, %d variables copied.", res.From, e.Name, e.Kind, len(res.Services), res.Variables)
			if len(res.Skipped) > 0 {
				names := make([]string, len(res.Skipped))
				for i, v := range res.Skipped {
					names[i] = v.String()
				}
				summary += " Values not copied, set them before deploying: " + strings.Join(names, ", ") + "."
			}
			if len(res.CopiedAcrossKinds) > 0 {
				names := make([]string, len(res.CopiedAcrossKinds))
				for i, v := range res.CopiedAcrossKinds {
					names[i] = v.String()
				}
				summary += fmt.Sprintf(" Values copied from %s environment %s: %s.", res.SourceKind, res.From, strings.Join(names, ", "))
			}
			summary += " Not copied: " + strings.Join(res.NotCopied, ", ") + "."
			var vs []releaseView
			switch {
			case res.DeployError != "":
				summary += " Nothing deployed: " + res.DeployError
			case res.Deploy != nil:
				rels := make([]apitypes.Release, len(res.Deploy.Promoted))
				for i, pr := range res.Deploy.Promoted {
					rels[i] = pr.Release
				}
				if vs, err = h.waitReleases(ctx, c, rels, wait); err != nil {
					return mcpkit.Reply{}, err
				}
				summary += " " + waitSummary("Deploy", vs)
			}
			return mcpkit.Reply{Summary: summary, Data: map[string]any{"clone": res, "releases": vs}}, nil
		})
}

// waitOne waits for one release and replies with it.
func (h *handler) waitOne(ctx context.Context, c *call, action string, rel apitypes.Release, wait time.Duration) (mcpkit.Reply, error) {
	vs, err := h.waitReleases(ctx, c, []apitypes.Release{rel}, wait)
	if err != nil {
		return mcpkit.Reply{}, err
	}
	return mcpkit.Reply{Summary: waitSummary(action, vs), Data: vs[0]}, nil
}

// addConfigTools registers the tools for variables, staged changes and
// domains.
func (h *handler) addConfigTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "list_variables", "List variables",
		"An environment's variables, shared and per service, with staged changes. Names only unless reveal (admin).",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			EnvInput
			Service string `json:"service,omitempty"`
			Reveal  bool   `json:"reveal,omitempty"`
		}) (mcpkit.Reply, error) {
			ref, err := envOf(c, in.EnvInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			vs, err := h.core.ListVariables(ctx, c.Actor, ref, in.Service, in.Reveal)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%d variables in %s/%s.", len(vs), ref.Project, ref.Environment), Data: vs}, nil
		})

	mcpkit.AddTool(s, "set_variables", "Stage variables",
		"Stage variables on a service, or without service shared ones, which every service of the environment gets (new ones too). Values may reference ${{shared.KEY}} or ${{service.KEY}} and are never shown back.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			EnvInput
			Service string            `json:"service,omitempty"`
			Set     map[string]string `json:"set,omitempty"`
			Unset   []string          `json:"unset,omitempty"`
			ApplyInput
		}) (mcpkit.Reply, error) {
			ref, err := envOf(c, in.EnvInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			if len(in.Set)+len(in.Unset) == 0 {
				return mcpkit.Reply{}, mcpkit.Invalid("pass set or unset")
			}
			res, err := h.core.SetVariables(ctx, c.Actor, ref, apitypes.SetVariablesRequest{Service: in.Service, Set: in.Set, Unset: in.Unset, Apply: in.Apply})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			keys := make([]string, 0, len(in.Set))
			for k := range in.Set {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			var did []string
			if len(keys) > 0 {
				did = append(did, "set "+strings.Join(keys, ", "))
			}
			if len(in.Unset) > 0 {
				did = append(did, "unset "+strings.Join(in.Unset, ", "))
			}
			return h.stagedReply(ctx, c, strings.Join(did, "; "), ref, res)
		})

	mcpkit.AddTool(s, "set_pre_deploy_command", "Stage the pre-deploy command",
		"Stage a command run in each new release's image before it gets traffic, e.g. migrations. A failure fails the release. Empty removes it.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Command        string `json:"command"`
			TimeoutSeconds *int   `json:"timeout_seconds,omitempty" doc:"Default 600."`
			Snapshot       *bool  `json:"snapshot,omitempty" doc:"Snapshot stateful services first."`
			ApplyInput
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			res, err := h.core.UpdateService(ctx, c.Actor, ref, apitypes.UpdateServiceRequest{
				PreDeployCommand: &in.Command, PreDeployTimeoutSeconds: in.TimeoutSeconds, PreDeploySnapshot: in.Snapshot, Apply: in.Apply,
			})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			did := fmt.Sprintf("pre-deploy command of %s set to %q", ref.Service, in.Command)
			if in.Command == "" {
				did = "pre-deploy command of " + ref.Service + " removed"
			}
			return h.stagedReply(ctx, c, did, ref.Env(), res)
		})

	mcpkit.AddTool(s, "show_changes", "Show staged changes",
		"An environment's staged changes as a diff, and the services applying them redeploys.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in EnvInput) (mcpkit.Reply, error) {
			ref, err := envOf(c, in)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			p, err := h.core.GetChanges(ctx, c.Actor, ref, false)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%d staged changes in %s/%s, affecting: %s.", p.Count, ref.Project, ref.Environment, list(p.Affected)), Data: p}, nil
		})

	mcpkit.AddTool(s, "apply_changes", "Apply staged changes",
		"Apply an environment's staged changes: config releases for the affected services, health checked, and wait.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			EnvInput
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
			res, err := h.core.ApplyChanges(ctx, c.Actor, ref)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return h.appliedReply(ctx, c, "Applied", res, wait)
		})

	mcpkit.AddTool(s, "discard_changes", "Discard staged changes",
		"Drop staged changes: the ones named, or all.",
		mcpkit.Destructive, func(ctx context.Context, c *call, in discardInput) (mcpkit.Reply, error) {
			ref, err := envOf(c, in.EnvInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			p, err := h.core.DiscardChanges(ctx, c.Actor, ref, in.request())
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("Discarded. %d changes still staged in %s/%s.", p.Count, ref.Project, ref.Environment), Data: p}, nil
		})

	mcpkit.AddTool(s, "list_domains", "List domains",
		"A service's domains, their status and the DNS records to create, with the TXT record proving control while unverified.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in ServiceInput) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			ds, err := h.core.ListDomains(ctx, c.Actor, ref)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			lines := make([]string, len(ds))
			for i, d := range ds {
				lines[i] = domainLine(d)
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%d domains of %s: %s.", len(ds), ref, list(lines)), Data: ds}, nil
		})

	mcpkit.AddTool(s, "add_domain", "Add a domain",
		"Add a hostname to a service. Without hostname, a generated one under its server's base domain, which needs no DNS. A custom one's result has the DNS records the user must create, including a TXT record unless verified.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Hostname   string `json:"hostname,omitempty"`
			RedirectTo string `json:"redirect_to,omitempty" doc:"Redirect to this hostname instead of serving."`
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			var dc apitypes.DomainChange
			if in.Hostname == "" {
				if in.RedirectTo != "" {
					return mcpkit.Reply{}, mcpkit.Invalid("pass hostname: only a custom hostname redirects")
				}
				dc, err = h.core.GenerateDomain(ctx, c.Actor, ref)
			} else {
				dc, err = h.core.AddDomain(ctx, c.Actor, ref, apitypes.AddDomainRequest{Hostname: in.Hostname, RedirectTo: in.RedirectTo})
			}
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return h.domainReply(ctx, c, ref, dc)
		})

	mcpkit.AddTool(s, "check_domain", "Check a domain now",
		"Check a domain's verification, DNS and certificate now.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Hostname string `json:"hostname"`
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			d, err := h.core.CheckDomainNow(ctx, c.Actor, ref, in.Hostname)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: domainLine(d) + ".", Data: d}, nil
		})
}

// domainReply waits for the release a domain change started, if any, and
// replies with the domain.
func (h *handler) domainReply(ctx context.Context, c *call, ref core.ServiceRef, dc apitypes.DomainChange) (mcpkit.Reply, error) {
	out := map[string]any{"domain": dc.Domain}
	summary := fmt.Sprintf("Added %s to %s", domainLine(dc.Domain), ref)
	if r := dc.Domain.Verification; r != nil {
		summary += fmt.Sprintf(". It routes once this TXT record proves control: %s %s %s", r.Type, r.Name, r.Value)
	}
	if len(dc.Domain.DNSRecords) > 0 && dc.Domain.Status != apitypes.DomainActive {
		recs := make([]string, len(dc.Domain.DNSRecords))
		for i, r := range dc.Domain.DNSRecords {
			recs[i] = fmt.Sprintf("%s %s %s", r.Type, r.Name, r.Value)
		}
		summary += ". Create one of these DNS records: " + strings.Join(recs, "; ")
	}
	summary += "."
	if dc.Release != nil {
		vs, err := h.waitReleases(ctx, c, []apitypes.Release{*dc.Release}, defaultWait)
		if err != nil {
			return mcpkit.Reply{}, err
		}
		out["release"] = vs[0]
		summary += " " + waitSummary("Redeploy to publish the port", vs)
	}
	return mcpkit.Reply{Summary: summary, Data: out}, nil
}

// list joins names for a summary, or says none.
func list(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// projectFileInput is a project file to plan or apply (ADR 0049).
type projectFileInput struct {
	ProjectInput
	File        string `json:"file"`
	Environment string `json:"environment,omitempty"`
	Prune       bool   `json:"prune,omitempty" doc:"Also remove what the file doesn't mention (never volumes or environments)."`
}

// request is the API request, its environment defaulting to the one
// the caller is limited to (tools/list leaves the argument out for it).
func (in projectFileInput) request(c *call) apitypes.ApplyRequest {
	return apitypes.ApplyRequest{File: in.File, Environment: cmp.Or(in.Environment, c.Actor.EnvironmentName), Prune: in.Prune}
}

// planSummary counts a plan's lines for a tool's summary.
func planSummary(p apitypes.ApplyPlan) string {
	s := fmt.Sprintf("Plan for %s: %d changes, %d kept (not in the file), %d need a person.", p.Project, len(p.Changes), len(p.Kept), len(p.Manual))
	for _, m := range p.Manual {
		s += fmt.Sprintf(" %s/%s %s %s: %s.", m.Environment, m.Service, m.Kind, m.Name, m.Note)
	}
	return s
}

// discardInput names staged changes as short strings rather than the
// API's objects, which cost an agent's context in every tools/list:
// variables KEY (shared) or service.KEY, settings service.field, volumes
// service.name, domains service or service.hostname. Variable keys and
// service names have no dots, so the first one splits.
type discardInput struct {
	EnvInput
	Variables []string `json:"variables,omitempty" doc:"KEY (shared) or service.KEY."`
	Settings  []string `json:"settings,omitempty" doc:"service.field, e.g. web.port."`
	Volumes   []string `json:"volumes,omitempty" doc:"service.name."`
	Domains   []string `json:"domains,omitempty" doc:"service or service.hostname."`
	Services  []string `json:"services,omitempty" doc:"Staged services to drop, with all staged on them."`
}

// request is the API's discard request.
func (in discardInput) request() apitypes.DiscardChangesRequest {
	r := apitypes.DiscardChangesRequest{Services: in.Services}
	for _, v := range in.Variables {
		svc, key, ok := strings.Cut(v, ".")
		if !ok {
			svc, key = "", v
		}
		r.Variables = append(r.Variables, apitypes.VariableRef{Service: svc, Key: key})
	}
	for _, v := range in.Settings {
		svc, field, _ := strings.Cut(v, ".")
		r.Settings = append(r.Settings, apitypes.SettingRef{Service: svc, Field: field})
	}
	for _, v := range in.Volumes {
		svc, name, _ := strings.Cut(v, ".")
		r.Volumes = append(r.Volumes, apitypes.VolumeRef{Service: svc, Name: name})
	}
	for _, v := range in.Domains {
		svc, host, _ := strings.Cut(v, ".")
		r.Domains = append(r.Domains, apitypes.DomainRef{Service: svc, Hostname: host})
	}
	return r
}
