package mcp

import (
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

// ProjectInput names a project.
type ProjectInput struct {
	Project string `json:"project,omitempty" doc:"Project name. Default: the project your token is limited to."`
}

// EnvInput names an environment.
type EnvInput struct {
	ProjectInput
	Environment string `json:"environment,omitempty" doc:"Environment name, e.g. staging. Default: the environment your token is limited to."`
}

// ServiceInput names a service.
type ServiceInput struct {
	EnvInput
	Service string `json:"service" doc:"Service name, e.g. web."`
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

// addPlatformTools registers the tools for projects, services, releases,
// logs, variables, domains, previews, servers, the audit log and alerts.
func (h *handler) addPlatformTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "whoami", "Who am I",
		"Who you are acting as: the token's name, scope (read, deploy or admin) and the project and environment it is limited to. Call it first; other tools default project and environment to these limits.",
		mcpkit.ReadOnly, func(_ context.Context, c *call, _ struct{}) (mcpkit.Reply, error) {
			me := c.Actor.Me()
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
			return mcpkit.Reply{Summary: fmt.Sprintf("%s %q, scope %s, limited to %s: may %s.", me.Kind, me.Name, me.Scope, where, can), Data: me}, nil
		})

	mcpkit.AddTool(s, "list_projects", "List projects",
		"List the projects you can see.",
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
		"A project's environments (with kind, and whether their server is online) and their services: state, active release, an in-flight deploy, the latest release when it isn't the active one (e.g. a failed deploy), domains and staged-change counts. "+
			"The best first look at a project.",
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
		"A project's configuration as it is live: environments, services and their settings, volumes, domains, git connections and variable names (never values), sorted by name.",
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
		"Compare a project file (the format export_project returns, as TOML or JSON) with the live state, like terraform plan: changes (what apply_project_file would stage), "+
			"kept (live things the file doesn't mention; prune lists the removable ones as removals) and manual (what needs a person: secrets without a value, git connections). "+
			"The project may not exist yet. Secrets are never in a file: list their names in variables and send values separately.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in projectFileInput) (mcpkit.Reply, error) {
			p, err := projectOf(c, in.ProjectInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			plan, err := h.core.PlanProject(ctx, c.Actor, p, in.request())
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: planSummary(plan), Data: plan}, nil
		})

	mcpkit.AddTool(s, "apply_project_file", "Stage a project file",
		"Stage a project file's plan as staged changes in each of its environments (review them with show_changes); nothing goes live unless deploy, which applies each environment's staged changes and waits for the releases. "+
			"A missing project or environment is created empty. Removals only with prune, which never removes volumes or environments (archiving services needs an admin token). "+
			"values sets secrets the file names in variables; they are never shown back.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ProjectInput
			File        string                   `json:"file" doc:"The project file: TOML or JSON, as export_project returns it."`
			Environment string                   `json:"environment,omitempty" doc:"Only this environment of the file (needed with a token limited to one environment)."`
			Prune       bool                     `json:"prune,omitempty" doc:"Also remove variables, domains and services the file doesn't mention. Off by default: a partial file never deletes anything."`
			Values      []apitypes.VariableValue `json:"values,omitempty" doc:"Values for variables the file names without one (secrets); staged, never shown back."`
			Deploy      bool                     `json:"deploy,omitempty" doc:"Apply each environment's staged changes afterwards and wait for the releases."`
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
			req := apitypes.ApplyRequest{File: in.File, Environment: in.Environment, Prune: in.Prune, Values: in.Values, Deploy: in.Deploy}
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
		"Convert a docker-compose.yml into a Dock project file and plan it: the file, warnings for everything that doesn't map exactly (nothing is dropped silently), the secrets it found (names only) and the plan. "+
			"postgres and redis images become Dock's database templates. Nothing changes: stage the returned file with apply_project_file, sending the secrets' values in values.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			ProjectInput
			Compose  string            `json:"compose" doc:"The docker-compose.yml."`
			EnvFiles map[string]string `json:"env_files,omitempty" doc:"Files it reads: env_file paths as written in it, and .env for ${VAR} interpolation."`
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
		"One service's live state: running, crashed (with exit code and restarts, OOM kills), stopped (by whom, when), its active release and its latest release if that one failed or is still deploying.",
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
		"A service's releases, newest first: number, status, source (image, rollback, restart, config, promote, upload), image, who made it and why it failed.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Limit int `json:"limit,omitempty" doc:"How many releases. Default 10, at most 100."`
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
		"One release (default: the latest) with its deploy steps. For a failed release also the reason (error, error_code) and the last lines of its container's output and its build or image pull. "+
			"Read this after a failed deploy instead of rolling back: a failed release never got traffic.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Number   int64 `json:"number,omitempty" doc:"Release number. Default: the latest release."`
			LogLines int   `json:"log_lines,omitempty" doc:"Log lines of a failed release, per log. Default 40, at most 500."`
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
		"Why a release failed, or why the live one crash looped or turned unhealthy (kind), in one bundle Dock kept when it happened: the likely cause from deterministic rules (cause.rule, cause.summary, a fix with its setting and CLI command), "+
			"every rule checked with its verdict (checked: what was ruled out and why), the failure with its exit code and OOM kill, the health check's answer, "+
			"what changed since the last good release (image, variable names, settings), the pre-deploy command, the last lines of the container (matched lines marked) and the build, "+
			"error issues and memory around it. Default: the service's newest diagnosis; release picks one. Call this first after a failed deploy instead of reading logs. "+
			"A failed release never got traffic: fix the cause and deploy again, don't roll back. No rule matching (empty cause.rule) means read the evidence.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Release int64 `json:"release,omitempty" doc:"Release number: a failed one, or a live one that crash looped. Default: the service's newest diagnosis."`
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
		"The last lines a service's container wrote (stdout and stderr), from the active release or a given one. Earlier releases work too: a replaced or failed release's output was kept on its server when its container was removed (for 30 days by default), and is read from there. "+
			"Lines start with their time; stderr lines are marked [stderr]. Use grep to get only the lines that matter. stderr is not the same as errors: many servers (Caddy, for one) write every line there, so judge a line by the level it states (\"level\":\"error\", level=warn) or its words.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Release int64  `json:"release,omitempty" doc:"Release number, also of an earlier (replaced or failed) release. Default: the active release."`
			Tail    int    `json:"tail,omitempty" doc:"How many of the last lines. Default 100, at most 1000."`
			Since   string `json:"since,omitempty" doc:"Only lines since then: a duration like 10m or an RFC 3339 time."`
			Grep    string `json:"grep,omitempty" doc:"Only lines containing this text: a plain, case-sensitive substring, not a regex. Applied after tail, so it finds the matches among the last tail lines; raise tail to search further back."`
			Stderr  bool   `json:"stderr,omitempty" doc:"Only lines written to stderr, which may be all of them. Applied after tail, like grep."`
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			n, err := mcpkit.Limit("tail", in.Tail, 100, 1000)
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
	h.addRunTools(s)
	h.addJobTools(s)
	h.addPreviewTools(s)
	h.addSleepTools(s)

	mcpkit.AddTool(s, "list_servers", "List servers",
		"The servers apps run on: status (online, offline, ...), which environment kinds each accepts, its base domain for generated hostnames, and memory reserved by the services placed there. Platform operators only.",
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
		"The private image registries the organization signs in to when a deploy pulls an image from them: host and username, never the password. An image from any other host is pulled anonymously; a person adds credentials with `dock registry login`. Admin tokens only.",
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
		"Audit events, newest first: who (user or token) did what, where and when. Every change is recorded, yours included. Tokens limited to a project or environment only see events there.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			Project     string `json:"project,omitempty" doc:"Only events in this project."`
			Environment string `json:"environment,omitempty" doc:"Only events in this environment; needs project."`
			Service     string `json:"service,omitempty" doc:"Only events targeting this service; needs project and environment."`
			Action      string `json:"action,omitempty" doc:"Action prefix, e.g. release. or domain.add"`
			Before      int64  `json:"before,omitempty" doc:"Only events with a smaller id, to page back."`
			Limit       int    `json:"limit,omitempty" doc:"How many events. Default 50, at most 500."`
		}) (mcpkit.Reply, error) {
			n, err := mcpkit.Limit("limit", in.Limit, 50, 500)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			evs, err := h.core.ListAudit(ctx, c.Actor, core.AuditFilter{
				Project: in.Project, Environment: in.Environment, Service: in.Service, Action: in.Action, Before: in.Before, Limit: n,
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
		"Wait for a release that is already deploying (yours after a tool's wait ran out, or anyone's) until it is active or failed, and return its final state like deploy_image does, with the logs of a failure. "+
			"Default: the service's in-flight release; if nothing is in flight, the latest release's state is returned at once. Changes nothing.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Number int64 `json:"number,omitempty" doc:"Release number. Default: the release in flight, if any."`
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
		"Deploy a prebuilt image (e.g. ghcr.io/acme/web:3f2a1c) to a service, taking the service's staged changes with it, and wait for the result. "+
			"The new release gets traffic only after its health check passes; if it fails, nothing changed and the previous release keeps serving (don't roll back; read the error and logs in the result). "+
			"Deploying source code isn't possible here: run `dock deploy` in the repository instead.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Image string `json:"image,omitempty" doc:"Image reference. Default: the service's image setting (the image it was last deployed from)."`
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
		"Restart a service: a new release identical to the active one (same image and config), through the same health-checked path, and wait for the result. "+
			"For a stuck or crashed service. Staged changes are not included; apply_changes takes those live.",
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
		"Stop a service: its domains answer 503 and its container is removed, freeing its memory. Its active release, port, volumes, domains and variables are kept. "+
			"It stays stopped (no restarts, no alerts, no backups) until start_service or a deploy runs it again. Takes the service offline: only when asked.",
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
		"Start a stopped service: a new release identical to the active one, through the same health-checked path, and wait for the result. "+
			"If it fails, the service stays stopped; read the error and logs in the result.",
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
		"Roll a service back to an earlier release that served traffic: a new release with that release's image and config, health checked like any deploy, and wait for the result. "+
			"Only for a release that went live and misbehaves; a failed deploy never got traffic and needs no rollback.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Number int64 `json:"number" doc:"The release to roll back to (one that was active, see list_releases)."`
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
		"Promote what runs in one environment to another of the same project (e.g. staging to production): for each service, the same image by digest with the target's own variables and settings. "+
			"Releases deploy one at a time and stop at the first failure; services missing in the target are skipped and listed. Waits for the result. Needs deploy on the target environment.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ProjectInput
			From     string   `json:"from" doc:"Source environment, e.g. staging."`
			To       string   `json:"to" doc:"Target environment, e.g. production."`
			Services []string `json:"services,omitempty" doc:"Only these services. Default: every service with an active release in from."`
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
		"Create a new environment as a copy of another in the same project: services, settings, variables and new, empty volumes, placed like any new environment of its kind. "+
			"Reference-only values are always copied. Literal values are copied only for admins, and between production and non-production only with copy_values (admin); otherwise database templates get new credentials and the rest are listed as skipped for you to set. "+
			"Domains, git connections, releases and backups are never copied. With deploy, the images serving in from are deployed like promote, and the tool waits for them. Needs deploy on the project.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ProjectInput
			From       string `json:"from" doc:"Environment to copy, e.g. staging."`
			To         string `json:"to" doc:"Name of the new environment."`
			Kind       string `json:"kind,omitempty" doc:"production or non_production. Default: production for an environment named production, otherwise non_production."`
			Deploy     bool   `json:"deploy,omitempty" doc:"Also deploy the images serving in from, by digest."`
			CopyValues bool   `json:"copy_values,omitempty" doc:"Admin only: also copy literal values between production and non-production."`
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
		"An environment's variables: shared ones (every service gets them) and each service's, with staged changes marked as pending. Names only: values are secret. "+
			"reveal includes values, which only admin tokens may do.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			EnvInput
			Service string `json:"service,omitempty" doc:"Only shared variables and this service's."`
			Reveal  bool   `json:"reveal,omitempty" doc:"Include values (admin tokens only; refused otherwise)."`
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
		"Stage variable changes: set and unset keys on a service, or shared (environment-level) ones without service. Nothing goes live until apply_changes or the service's next deploy. "+
			"Values may reference other variables: ${{shared.KEY}} or ${{service.KEY}}. Values are secret: they are never shown back.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			EnvInput
			Service string            `json:"service,omitempty" doc:"The service whose variables change. Default: shared variables."`
			Set     map[string]string `json:"set,omitempty" doc:"Keys and values to set."`
			Unset   []string          `json:"unset,omitempty" doc:"Keys to remove."`
		}) (mcpkit.Reply, error) {
			ref, err := envOf(c, in.EnvInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			if len(in.Set)+len(in.Unset) == 0 {
				return mcpkit.Reply{}, mcpkit.Invalid("pass set or unset")
			}
			res, err := h.core.SetVariables(ctx, c.Actor, ref, apitypes.SetVariablesRequest{Service: in.Service, Set: in.Set, Unset: in.Unset})
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
			return mcpkit.Reply{Summary: fmt.Sprintf("Staged: %s. %d changes staged in %s/%s; apply_changes takes them live.",
					strings.Join(did, "; "), res.Pending.Count, ref.Project, ref.Environment), Data: res.Pending},
				nil
		})

	mcpkit.AddTool(s, "set_pre_deploy_command", "Stage the pre-deploy command",
		"Stage a service's pre-deploy command: it runs with each new release's image, variables and private network before the release gets traffic, typically database migrations "+
			"(e.g. \"npx prisma migrate deploy\"). A non-zero exit fails the release and the previous one keeps serving. An empty command removes it. "+
			"With snapshot, the environment's stateful services are snapshotted before it runs (a failed snapshot fails the release). "+
			"Nothing goes live until apply_changes or the service's next deploy; dock.toml's deploy.pre_deploy_command wins over it.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Command        string `json:"command" doc:"Run with sh -c. Empty removes the pre-deploy command."`
			TimeoutSeconds *int   `json:"timeout_seconds,omitempty" doc:"How long it may run, up to 3600. 0 means the default, 600."`
			Snapshot       *bool  `json:"snapshot,omitempty" doc:"Snapshot the environment's stateful services before it runs (pre_deploy_snapshot). Left out: unchanged."`
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			res, err := h.core.UpdateService(ctx, c.Actor, ref, apitypes.UpdateServiceRequest{
				PreDeployCommand: &in.Command, PreDeployTimeoutSeconds: in.TimeoutSeconds, PreDeploySnapshot: in.Snapshot,
			})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			did := fmt.Sprintf("pre-deploy command of %s set to %q", ref.Service, in.Command)
			if in.Command == "" {
				did = "pre-deploy command of " + ref.Service + " removed"
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("Staged: %s. %d changes staged in %s/%s; apply_changes takes them live.",
				did, res.Pending.Count, ref.Project, ref.Environment), Data: res.Pending}, nil
		})

	mcpkit.AddTool(s, "show_changes", "Show staged changes",
		"An environment's staged changes (variables, settings, volumes) as a diff, and the services applying them would redeploy. Secret values are masked.",
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
		"Apply an environment's staged changes atomically: every affected running service gets a config release (health checked like any deploy). Waits for the releases to finish. "+
			"Review them first with show_changes.",
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
			vs, err := h.waitReleases(ctx, c, res.Releases, wait)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			summary := waitSummary("Applied", vs)
			if len(res.Skipped) > 0 {
				summary += " Not redeployed (a deploy was in progress; their next release takes the changes): " + strings.Join(res.Skipped, ", ") + "."
			}
			return mcpkit.Reply{Summary: summary, Data: map[string]any{"releases": vs, "skipped": res.Skipped}}, nil
		})

	mcpkit.AddTool(s, "discard_changes", "Discard staged changes",
		"Discard staged changes in an environment: the ones selected, or all of them when nothing is selected. Live config is not touched.",
		mcpkit.Destructive, func(ctx context.Context, c *call, in struct {
			EnvInput
			apitypes.DiscardChangesRequest
		}) (mcpkit.Reply, error) {
			ref, err := envOf(c, in.EnvInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			p, err := h.core.DiscardChanges(ctx, c.Actor, ref, in.DiscardChangesRequest)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("Discarded. %d changes still staged in %s/%s.", p.Count, ref.Project, ref.Environment), Data: p}, nil
		})

	mcpkit.AddTool(s, "list_domains", "List domains",
		"A service's domains with their status (waiting_for_dns, issuing_certificate, active, error) and the exact DNS records to create for custom ones; one not yet verified also has the TXT record that proves control (verification), and isn't routed until it is found.",
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
		"Add a custom hostname to a service. A hostname belongs to one service only. The result lists the DNS records the user must create; the certificate is issued once DNS points at the server. "+
			"Unless verified is true, it routes only once the user also creates the TXT record in verification, which proves their organization controls the hostname (check_domain looks for it at once). A hostname another organization has proved is refused. "+
			"If the service's port wasn't published yet, this redeploys it and waits.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Hostname   string `json:"hostname" doc:"The hostname, e.g. shop.example.com."`
			RedirectTo string `json:"redirect_to,omitempty" doc:"Permanently redirect this hostname to another one instead of serving the app."`
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			dc, err := h.core.AddDomain(ctx, c.Actor, ref, apitypes.AddDomainRequest{Hostname: in.Hostname, RedirectTo: in.RedirectTo})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return h.domainReply(ctx, c, ref, dc)
		})

	mcpkit.AddTool(s, "generate_domain", "Generate a domain",
		"Give a service a generated hostname under its server's base domain (<service>-<project>[-<env>].<base>), which works without any DNS work. Redeploys the service if its port wasn't published yet, and waits.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in ServiceInput) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			dc, err := h.core.GenerateDomain(ctx, c.Actor, ref)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return h.domainReply(ctx, c, ref, dc)
		})

	mcpkit.AddTool(s, "check_domain", "Check a domain now",
		"Check a domain at once instead of at the server's next retry: its verification TXT record while it isn't verified, then its DNS and certificate. Returns the domain with its status.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Hostname string `json:"hostname" doc:"The domain's hostname, e.g. shop.example.com."`
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
	File        string `json:"file" doc:"The project file: TOML or JSON, as export_project returns it."`
	Environment string `json:"environment,omitempty" doc:"Only this environment of the file (needed with a token limited to one environment)."`
	Prune       bool   `json:"prune,omitempty" doc:"Also remove variables, domains and services the file doesn't mention. Off by default: a partial file never deletes anything."`
}

func (in projectFileInput) request() apitypes.ApplyRequest {
	return apitypes.ApplyRequest{File: in.File, Environment: in.Environment, Prune: in.Prune}
}

// planSummary counts a plan's lines for a tool's summary.
func planSummary(p apitypes.ApplyPlan) string {
	s := fmt.Sprintf("Plan for %s: %d changes, %d kept (not in the file), %d need a person.", p.Project, len(p.Changes), len(p.Kept), len(p.Manual))
	for _, m := range p.Manual {
		s += fmt.Sprintf(" %s/%s %s %s: %s.", m.Environment, m.Service, m.Kind, m.Name, m.Note)
	}
	return s
}
