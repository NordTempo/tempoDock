package mcp

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
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

// project, env and service resolve an input to a reference, filling in the
// token's limits. Core still checks the result against them.
func (c *call) project(in ProjectInput) (string, error) {
	if p := in.Project; p != "" {
		return p, nil
	}
	if c.actor.ProjectName == "" {
		return "", invalid("pass project: your token isn't limited to one (list_projects shows them)")
	}
	return c.actor.ProjectName, nil
}

func (c *call) env(in EnvInput) (core.EnvRef, error) {
	p, err := c.project(in.ProjectInput)
	if err != nil {
		return core.EnvRef{}, err
	}
	e := in.Environment
	if e == "" && p == c.actor.ProjectName {
		e = c.actor.EnvironmentName
	}
	if e == "" {
		return core.EnvRef{}, invalid("pass environment: your token isn't limited to one (project_overview lists them)")
	}
	return core.EnvRef{Project: p, Environment: e}, nil
}

func (c *call) service(in ServiceInput) (core.ServiceRef, error) {
	e, err := c.env(in.EnvInput)
	if err != nil {
		return core.ServiceRef{}, err
	}
	if in.Service == "" {
		return core.ServiceRef{}, invalid("pass service (project_overview lists them)")
	}
	return core.ServiceRef{Project: e.Project, Environment: e.Environment, Service: in.Service}, nil
}

// addPlatformTools registers the tools for projects, services, releases,
// logs, variables, domains, servers and the audit log.
func (h *handler) addPlatformTools() {
	addTool(h, "whoami", "Who am I",
		"Who you are acting as: the token's name, scope (read, deploy or admin) and the project and environment it is limited to. Call it first; other tools default project and environment to these limits.",
		readOnly, func(_ context.Context, c *call, _ struct{}) (reply, error) {
			me := c.actor.Me()
			where := "every project"
			switch {
			case me.Environment != "":
				where = me.Project + "/" + me.Environment
			case me.Project != "":
				where = "project " + me.Project
			}
			can := map[apitypes.Scope]string{
				apitypes.ScopeRead:   "read status, releases, logs, variable names and the audit log",
				apitypes.ScopeDeploy: "read, and deploy, redeploy, roll back, promote, stage and apply variables, add domains",
				apitypes.ScopeAdmin:  "everything, including secret values",
			}[me.Scope]
			return reply{fmt.Sprintf("%s %q, scope %s, limited to %s: may %s.", me.Kind, me.Name, me.Scope, where, can), me}, nil
		})

	addTool(h, "list_projects", "List projects",
		"List the projects you can see.",
		readOnly, func(ctx context.Context, c *call, _ struct{}) (reply, error) {
			ps, err := h.core.ListProjects(ctx, c.actor, false)
			if err != nil {
				return reply{}, err
			}
			names := make([]string, len(ps))
			for i, p := range ps {
				names[i] = p.Name
			}
			return reply{fmt.Sprintf("%d projects: %s", len(names), strings.Join(names, ", ")), map[string][]string{"projects": names}}, nil
		})

	addTool(h, "project_overview", "Project overview",
		"A project's environments (with kind and server) and their services: state, active release, an in-flight deploy, the latest release when it isn't the active one (e.g. a failed deploy), domains and staged-change counts. "+
			"The best first look at a project.",
		readOnly, func(ctx context.Context, c *call, in ProjectInput) (reply, error) {
			p, err := c.project(in)
			if err != nil {
				return reply{}, err
			}
			o, err := h.core.Overview(ctx, c.actor, p)
			if err != nil {
				return reply{}, err
			}
			v := viewOverview(o)
			parts := make([]string, len(v.Environments))
			for i, e := range v.Environments {
				parts[i] = fmt.Sprintf("%s (%d services)", e.Name, len(e.Services))
			}
			return reply{fmt.Sprintf("Project %s: %s.", v.Project, strings.Join(parts, ", ")), v}, nil
		})

	addTool(h, "export_project", "Export project config",
		"A project's configuration as it is live: environments, services and their settings, volumes, domains, git connections and variable names (never values), sorted by name.",
		readOnly, func(ctx context.Context, c *call, in ProjectInput) (reply, error) {
			p, err := c.project(in)
			if err != nil {
				return reply{}, err
			}
			ex, err := h.core.ExportProject(ctx, c.actor, p)
			if err != nil {
				return reply{}, err
			}
			return reply{fmt.Sprintf("Project %s: %d environments.", ex.Project, len(ex.Environments)), ex}, nil
		})

	addTool(h, "service_status", "Service status",
		"One service's live state: running, crashed (with exit code and restarts, OOM kills), its active release and its latest release if that one failed or is still deploying.",
		readOnly, func(ctx context.Context, c *call, in ServiceInput) (reply, error) {
			ref, err := c.service(in)
			if err != nil {
				return reply{}, err
			}
			svc, err := h.core.GetService(ctx, c.actor, ref)
			if err != nil {
				return reply{}, err
			}
			rels, err := h.core.ListReleases(ctx, c.actor, ref, 1)
			if err != nil {
				return reply{}, err
			}
			v := viewService(svc)
			summary := fmt.Sprintf("%s is %s", ref, v.State)
			short := func(r apitypes.Release) *releaseSummary {
				return &releaseSummary{Number: r.Number, Status: r.Status, Image: r.ImageRef, Error: r.Error, ErrorCode: r.ErrorCode, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt}
			}
			if n := svc.ActiveRelease; n != nil {
				summary += fmt.Sprintf(", serving release %d", *n)
				if len(rels) > 0 && rels[0].Number == *n {
					v.Active = short(rels[0])
				} else if active, err := h.core.GetRelease(ctx, c.actor, ref, *n); err == nil {
					active.Events = nil
					v.Active = short(active)
				} else {
					return reply{}, err
				}
			}
			if len(rels) > 0 && (svc.ActiveRelease == nil || rels[0].Number != *svc.ActiveRelease) {
				v.Latest = short(rels[0])
				summary += "; latest " + describeRelease(rels[0])
			}
			return reply{summary + ".", v}, nil
		})

	addTool(h, "list_releases", "List releases",
		"A service's releases, newest first: number, status, source (image, rollback, restart, config, promote, upload), image, who made it and why it failed.",
		readOnly, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Limit int `json:"limit,omitempty" doc:"How many releases. Default 10, at most 100."`
		}) (reply, error) {
			ref, err := c.service(in.ServiceInput)
			if err != nil {
				return reply{}, err
			}
			n, err := limit("limit", in.Limit, 10, 100)
			if err != nil {
				return reply{}, err
			}
			rels, err := h.core.ListReleases(ctx, c.actor, ref, n)
			if err != nil {
				return reply{}, err
			}
			return reply{fmt.Sprintf("%d releases of %s.", len(rels), ref), viewReleases(rels)}, nil
		})

	addTool(h, "release_details", "Release details",
		"One release (default: the latest) with its deploy steps. For a failed release also the reason (error, error_code) and the last lines of its container's output and its build or image pull. "+
			"Read this after a failed deploy instead of rolling back: a failed release never got traffic.",
		readOnly, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Number   int64 `json:"number,omitempty" doc:"Release number. Default: the latest release."`
			LogLines int   `json:"log_lines,omitempty" doc:"Log lines of a failed release, per log. Default 40, at most 500."`
		}) (reply, error) {
			ref, err := c.service(in.ServiceInput)
			if err != nil {
				return reply{}, err
			}
			lines, err := limit("log_lines", in.LogLines, failureLogLines, 500)
			if err != nil {
				return reply{}, err
			}
			number := in.Number
			if number == 0 {
				rels, err := h.core.ListReleases(ctx, c.actor, ref, 1)
				if err != nil {
					return reply{}, err
				}
				if len(rels) == 0 {
					return reply{}, errf(apitypes.CodeNotFound, "%s has no releases yet; deploy it first", ref)
				}
				number = rels[0].Number
			}
			v, err := h.releaseDetails(ctx, c.actor, ref, number, lines)
			if err != nil {
				return reply{}, err
			}
			summary := fmt.Sprintf("Release %d of %s: %s", v.Number, ref, v.Status)
			if v.Error != "" {
				summary += ": " + v.Error
			}
			return reply{summary + ".", v}, nil
		})

	h.addReleaseTools()

	addTool(h, "logs", "Service logs",
		"The last lines a service's container wrote (stdout and stderr), from the active release or a given one. A failed release's container is gone; its last lines were kept and are returned instead. "+
			"Lines start with their time; stderr lines are marked [stderr].",
		readOnly, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Release int64  `json:"release,omitempty" doc:"Release number. Default: the active release."`
			Tail    int    `json:"tail,omitempty" doc:"How many of the last lines. Default 100, at most 1000."`
			Since   string `json:"since,omitempty" doc:"Only lines since then: a duration like 10m or an RFC 3339 time."`
		}) (reply, error) {
			ref, err := c.service(in.ServiceInput)
			if err != nil {
				return reply{}, err
			}
			n, err := limit("tail", in.Tail, 100, 1000)
			if err != nil {
				return reply{}, err
			}
			q := core.LogQuery{Release: in.Release, Tail: n}
			if in.Since != "" {
				if d, err := time.ParseDuration(in.Since); err == nil {
					q.Since = time.Now().Add(-d)
				} else if t, err := time.Parse(time.RFC3339, in.Since); err == nil {
					q.Since = t
				} else {
					return reply{}, invalid("since %q must be a duration like 10m or an RFC 3339 time", in.Since)
				}
			}
			lines, err := h.logLines(ctx, c.actor, ref, q, n)
			if err != nil {
				return reply{}, err
			}
			return reply{fmt.Sprintf("%d log lines of %s.", len(lines), ref), map[string][]string{"lines": lines}}, nil
		})

	h.addConfigTools()

	addTool(h, "list_servers", "List servers",
		"The servers apps run on: status (online, offline, ...), which environment kinds each accepts, its base domain for generated hostnames, and memory reserved by the services placed there.",
		readOnly, func(ctx context.Context, c *call, _ struct{}) (reply, error) {
			ss, err := h.core.ListServers(ctx, c.actor)
			if err != nil {
				return reply{}, err
			}
			out := make([]serverView, len(ss))
			parts := make([]string, len(ss))
			for i, sv := range ss {
				out[i] = viewServer(sv)
				parts[i] = sv.Name + " " + string(sv.Status)
			}
			return reply{fmt.Sprintf("%d servers: %s.", len(out), strings.Join(parts, ", ")), out}, nil
		})

	addTool(h, "audit_log", "Audit log",
		"Audit events, newest first: who (user or token) did what, where and when. Every change is recorded, yours included. Tokens limited to a project or environment only see events there.",
		readOnly, func(ctx context.Context, c *call, in struct {
			Project     string `json:"project,omitempty" doc:"Only events in this project."`
			Environment string `json:"environment,omitempty" doc:"Only events in this environment; needs project."`
			Service     string `json:"service,omitempty" doc:"Only events targeting this service; needs project and environment."`
			Action      string `json:"action,omitempty" doc:"Action prefix, e.g. release. or domain.add"`
			Before      int64  `json:"before,omitempty" doc:"Only events with a smaller id, to page back."`
			Limit       int    `json:"limit,omitempty" doc:"How many events. Default 50, at most 500."`
		}) (reply, error) {
			n, err := limit("limit", in.Limit, 50, 500)
			if err != nil {
				return reply{}, err
			}
			evs, err := h.core.ListAudit(ctx, c.actor, core.AuditFilter{
				Project: in.Project, Environment: in.Environment, Service: in.Service, Action: in.Action, Before: in.Before, Limit: n,
			})
			if err != nil {
				return reply{}, err
			}
			return reply{fmt.Sprintf("%d audit events.", len(evs)), evs}, nil
		})
}

// addReleaseTools registers the tools that start releases. Each waits for
// its releases to finish (waitReleases).
func (h *handler) addReleaseTools() {
	addTool(h, "deploy_image", "Deploy an image",
		"Deploy a prebuilt image (e.g. ghcr.io/acme/web:3f2a1c) to a service, taking the service's staged changes with it, and wait for the result. "+
			"The new release gets traffic only after its health check passes; if it fails, nothing changed and the previous release keeps serving (don't roll back; read the error and logs in the result). "+
			"Deploying source code isn't possible here: run `dock deploy` in the repository instead.",
		mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Image string `json:"image,omitempty" doc:"Image reference. Default: the service's image setting (the image it was last deployed from)."`
			WaitInput
		}) (reply, error) {
			ref, err := c.service(in.ServiceInput)
			if err != nil {
				return reply{}, err
			}
			wait, err := in.duration()
			if err != nil {
				return reply{}, err
			}
			rel, err := h.core.DeployImage(ctx, c.actor, ref, apitypes.DeployImageRequest{Image: in.Image})
			if err != nil {
				return reply{}, err
			}
			return h.waitOne(ctx, c, "Deploy", rel, wait)
		})

	addTool(h, "redeploy", "Redeploy (restart)",
		"Restart a service: a new release identical to the active one (same image and config), through the same health-checked path, and wait for the result. "+
			"For a stuck or crashed service. Staged changes are not included; apply_changes takes those live.",
		mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			WaitInput
		}) (reply, error) {
			ref, err := c.service(in.ServiceInput)
			if err != nil {
				return reply{}, err
			}
			wait, err := in.duration()
			if err != nil {
				return reply{}, err
			}
			rel, err := h.core.Restart(ctx, c.actor, ref)
			if err != nil {
				return reply{}, err
			}
			return h.waitOne(ctx, c, "Redeploy", rel, wait)
		})

	addTool(h, "rollback", "Roll back",
		"Roll a service back to an earlier release that served traffic: a new release with that release's image and config, health checked like any deploy, and wait for the result. "+
			"Only for a release that went live and misbehaves; a failed deploy never got traffic and needs no rollback.",
		mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Number int64 `json:"number" doc:"The release to roll back to (one that was active, see list_releases)."`
			WaitInput
		}) (reply, error) {
			ref, err := c.service(in.ServiceInput)
			if err != nil {
				return reply{}, err
			}
			if in.Number <= 0 {
				return reply{}, invalid("pass number: the release to roll back to (list_releases shows them)")
			}
			wait, err := in.duration()
			if err != nil {
				return reply{}, err
			}
			rel, err := h.core.Rollback(ctx, c.actor, ref, in.Number)
			if err != nil {
				return reply{}, err
			}
			return h.waitOne(ctx, c, "Rollback", rel, wait)
		})

	addTool(h, "promote", "Promote",
		"Promote what runs in one environment to another of the same project (e.g. staging to production): for each service, the same image by digest with the target's own variables and settings. "+
			"Releases deploy one at a time and stop at the first failure; services missing in the target are skipped and listed. Waits for the result. Needs deploy on the target environment.",
		mutating, func(ctx context.Context, c *call, in struct {
			ProjectInput
			From     string   `json:"from" doc:"Source environment, e.g. staging."`
			To       string   `json:"to" doc:"Target environment, e.g. production."`
			Services []string `json:"services,omitempty" doc:"Only these services. Default: every service with an active release in from."`
			WaitInput
		}) (reply, error) {
			p, err := c.project(in.ProjectInput)
			if err != nil {
				return reply{}, err
			}
			wait, err := in.duration()
			if err != nil {
				return reply{}, err
			}
			res, err := h.core.Promote(ctx, c.actor, p, apitypes.PromoteRequest{From: in.From, To: in.To, Services: in.Services})
			if err != nil {
				return reply{}, err
			}
			rels := make([]apitypes.Release, len(res.Promoted))
			for i, pr := range res.Promoted {
				rels[i] = pr.Release
			}
			vs, err := h.waitReleases(ctx, c, rels, wait)
			if err != nil {
				return reply{}, err
			}
			summary := waitSummary(fmt.Sprintf("Promote %s to %s", res.From, res.To), vs)
			if len(res.Skipped) > 0 {
				skipped := make([]string, len(res.Skipped))
				for i, sk := range res.Skipped {
					skipped[i] = sk.Service + " (" + string(sk.Reason) + ")"
				}
				summary += " Skipped: " + strings.Join(skipped, ", ") + "."
			}
			return reply{summary, map[string]any{"releases": vs, "skipped": res.Skipped}}, nil
		})

	addTool(h, "clone_environment", "Clone an environment",
		"Create a new environment as a copy of another in the same project: services, settings, variables and new, empty volumes, placed like any new environment of its kind. "+
			"Reference-only values are always copied. Literal values are copied only for admins, and between production and non-production only with copy_values (admin); otherwise database templates get new credentials and the rest are listed as skipped for you to set. "+
			"Domains, git connections, releases and backups are never copied. With deploy, the images serving in from are deployed like promote, and the tool waits for them. Needs deploy on the project.",
		mutating, func(ctx context.Context, c *call, in struct {
			ProjectInput
			From       string `json:"from" doc:"Environment to copy, e.g. staging."`
			To         string `json:"to" doc:"Name of the new environment."`
			Kind       string `json:"kind,omitempty" doc:"production or non_production. Default: production for an environment named production, otherwise non_production."`
			Deploy     bool   `json:"deploy,omitempty" doc:"Also deploy the images serving in from, by digest."`
			CopyValues bool   `json:"copy_values,omitempty" doc:"Admin only: also copy literal values between production and non-production."`
			WaitInput
		}) (reply, error) {
			p, err := c.project(in.ProjectInput)
			if err != nil {
				return reply{}, err
			}
			wait, err := in.duration()
			if err != nil {
				return reply{}, err
			}
			res, err := h.core.CloneEnvironment(ctx, c.actor, core.EnvRef{Project: p, Environment: in.From}, apitypes.CloneEnvironmentRequest{
				Name: in.To, Kind: apitypes.EnvironmentKind(in.Kind), Deploy: in.Deploy, CopyValues: in.CopyValues,
			})
			if err != nil {
				return reply{}, err
			}
			e := res.Environment
			summary := fmt.Sprintf("Cloned %s into %s (%s, server %s): %d services, %d variables copied.", res.From, e.Name, e.Kind, e.Server, len(res.Services), res.Variables)
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
					return reply{}, err
				}
				summary += " " + waitSummary("Deploy", vs)
			}
			return reply{summary, map[string]any{"clone": res, "releases": vs}}, nil
		})
}

// waitOne waits for one release and replies with it.
func (h *handler) waitOne(ctx context.Context, c *call, action string, rel apitypes.Release, wait time.Duration) (reply, error) {
	vs, err := h.waitReleases(ctx, c, []apitypes.Release{rel}, wait)
	if err != nil {
		return reply{}, err
	}
	return reply{waitSummary(action, vs), vs[0]}, nil
}

// addConfigTools registers the tools for variables, staged changes and
// domains.
func (h *handler) addConfigTools() {
	addTool(h, "list_variables", "List variables",
		"An environment's variables: shared ones (every service gets them) and each service's, with staged changes marked as pending. Names only: values are secret. "+
			"reveal includes values, which only admin tokens may do.",
		readOnly, func(ctx context.Context, c *call, in struct {
			EnvInput
			Service string `json:"service,omitempty" doc:"Only shared variables and this service's."`
			Reveal  bool   `json:"reveal,omitempty" doc:"Include values (admin tokens only; refused otherwise)."`
		}) (reply, error) {
			ref, err := c.env(in.EnvInput)
			if err != nil {
				return reply{}, err
			}
			vs, err := h.core.ListVariables(ctx, c.actor, ref, in.Service, in.Reveal)
			if err != nil {
				return reply{}, err
			}
			return reply{fmt.Sprintf("%d variables in %s/%s.", len(vs), ref.Project, ref.Environment), vs}, nil
		})

	addTool(h, "set_variables", "Stage variables",
		"Stage variable changes: set and unset keys on a service, or shared (environment-level) ones without service. Nothing goes live until apply_changes or the service's next deploy. "+
			"Values may reference other variables: ${{shared.KEY}} or ${{service.KEY}}. Values are secret: they are never shown back.",
		mutating, func(ctx context.Context, c *call, in struct {
			EnvInput
			Service string            `json:"service,omitempty" doc:"The service whose variables change. Default: shared variables."`
			Set     map[string]string `json:"set,omitempty" doc:"Keys and values to set."`
			Unset   []string          `json:"unset,omitempty" doc:"Keys to remove."`
		}) (reply, error) {
			ref, err := c.env(in.EnvInput)
			if err != nil {
				return reply{}, err
			}
			if len(in.Set)+len(in.Unset) == 0 {
				return reply{}, invalid("pass set or unset")
			}
			res, err := h.core.SetVariables(ctx, c.actor, ref, apitypes.SetVariablesRequest{Service: in.Service, Set: in.Set, Unset: in.Unset})
			if err != nil {
				return reply{}, err
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
			return reply{fmt.Sprintf("Staged: %s. %d changes staged in %s/%s; apply_changes takes them live.",
				strings.Join(did, "; "), res.Pending.Count, ref.Project, ref.Environment), res.Pending}, nil
		})

	addTool(h, "show_changes", "Show staged changes",
		"An environment's staged changes (variables, settings, volumes) as a diff, and the services applying them would redeploy. Secret values are masked.",
		readOnly, func(ctx context.Context, c *call, in EnvInput) (reply, error) {
			ref, err := c.env(in)
			if err != nil {
				return reply{}, err
			}
			p, err := h.core.GetChanges(ctx, c.actor, ref, false)
			if err != nil {
				return reply{}, err
			}
			return reply{fmt.Sprintf("%d staged changes in %s/%s, affecting: %s.", p.Count, ref.Project, ref.Environment, list(p.Affected)), p}, nil
		})

	addTool(h, "apply_changes", "Apply staged changes",
		"Apply an environment's staged changes atomically: every affected running service gets a config release (health checked like any deploy). Waits for the releases to finish. "+
			"Review them first with show_changes.",
		mutating, func(ctx context.Context, c *call, in struct {
			EnvInput
			WaitInput
		}) (reply, error) {
			ref, err := c.env(in.EnvInput)
			if err != nil {
				return reply{}, err
			}
			wait, err := in.duration()
			if err != nil {
				return reply{}, err
			}
			res, err := h.core.ApplyChanges(ctx, c.actor, ref)
			if err != nil {
				return reply{}, err
			}
			vs, err := h.waitReleases(ctx, c, res.Releases, wait)
			if err != nil {
				return reply{}, err
			}
			summary := waitSummary("Applied", vs)
			if len(res.Skipped) > 0 {
				summary += " Not redeployed (a deploy was in progress; their next release takes the changes): " + strings.Join(res.Skipped, ", ") + "."
			}
			return reply{summary, map[string]any{"releases": vs, "skipped": res.Skipped}}, nil
		})

	addTool(h, "discard_changes", "Discard staged changes",
		"Discard staged changes in an environment: the ones selected, or all of them when nothing is selected. Live config is not touched.",
		destructive, func(ctx context.Context, c *call, in struct {
			EnvInput
			apitypes.DiscardChangesRequest
		}) (reply, error) {
			ref, err := c.env(in.EnvInput)
			if err != nil {
				return reply{}, err
			}
			p, err := h.core.DiscardChanges(ctx, c.actor, ref, in.DiscardChangesRequest)
			if err != nil {
				return reply{}, err
			}
			return reply{fmt.Sprintf("Discarded. %d changes still staged in %s/%s.", p.Count, ref.Project, ref.Environment), p}, nil
		})

	addTool(h, "list_domains", "List domains",
		"A service's domains with their status (waiting_for_dns, issuing_certificate, active, error) and the exact DNS records to create for custom ones.",
		readOnly, func(ctx context.Context, c *call, in ServiceInput) (reply, error) {
			ref, err := c.service(in)
			if err != nil {
				return reply{}, err
			}
			ds, err := h.core.ListDomains(ctx, c.actor, ref)
			if err != nil {
				return reply{}, err
			}
			lines := make([]string, len(ds))
			for i, d := range ds {
				lines[i] = domainLine(d)
			}
			return reply{fmt.Sprintf("%d domains of %s: %s.", len(ds), ref, list(lines)), ds}, nil
		})

	addTool(h, "add_domain", "Add a domain",
		"Add a custom hostname to a service. A hostname belongs to one service only. The result lists the DNS records the user must create; the certificate is issued once DNS points at the server. "+
			"If the service's port wasn't published yet, this redeploys it and waits.",
		mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Hostname   string `json:"hostname" doc:"The hostname, e.g. shop.example.com."`
			RedirectTo string `json:"redirect_to,omitempty" doc:"Permanently redirect this hostname to another one instead of serving the app."`
		}) (reply, error) {
			ref, err := c.service(in.ServiceInput)
			if err != nil {
				return reply{}, err
			}
			dc, err := h.core.AddDomain(ctx, c.actor, ref, apitypes.AddDomainRequest{Hostname: in.Hostname, RedirectTo: in.RedirectTo})
			if err != nil {
				return reply{}, err
			}
			return h.domainReply(ctx, c, ref, dc)
		})

	addTool(h, "generate_domain", "Generate a domain",
		"Give a service a generated hostname under its server's base domain (<service>-<project>[-<env>].<base>), which works without any DNS work. Redeploys the service if its port wasn't published yet, and waits.",
		mutating, func(ctx context.Context, c *call, in ServiceInput) (reply, error) {
			ref, err := c.service(in)
			if err != nil {
				return reply{}, err
			}
			dc, err := h.core.GenerateDomain(ctx, c.actor, ref)
			if err != nil {
				return reply{}, err
			}
			return h.domainReply(ctx, c, ref, dc)
		})
}

// domainReply waits for the release a domain change started, if any, and
// replies with the domain.
func (h *handler) domainReply(ctx context.Context, c *call, ref core.ServiceRef, dc apitypes.DomainChange) (reply, error) {
	out := map[string]any{"domain": dc.Domain}
	summary := fmt.Sprintf("Added %s to %s", domainLine(dc.Domain), ref)
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
			return reply{}, err
		}
		out["release"] = vs[0]
		summary += " " + waitSummary("Redeploy to publish the port", vs)
	}
	return reply{summary, out}, nil
}

// list joins names for a summary, or says none.
func list(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
