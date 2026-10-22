package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// Path parameters. Resources are addressed by name; names are unique in
// their parent and immutable. (Exported because Huma only finds parameters
// in exported embedded structs.)

// ProjectPath addresses a project.
type ProjectPath struct {
	Project string `path:"project" doc:"Project name"`
}

// EnvPath addresses an environment.
type EnvPath struct {
	ProjectPath
	Environment string `path:"environment" doc:"Environment name"`
}

func (p EnvPath) ref() core.EnvRef {
	return core.EnvRef{Project: p.Project, Environment: p.Environment}
}

// ServicePath addresses a service.
type ServicePath struct {
	EnvPath
	Service string `path:"service" doc:"Service name"`
}

func (p ServicePath) ref() core.ServiceRef {
	return core.ServiceRef{Project: p.Project, Environment: p.Environment, Service: p.Service}
}

const (
	projectsPath = Prefix + "/projects"
	projectPathT = projectsPath + "/{project}"
	envsPath     = projectPathT + "/environments"
	envPathT     = envsPath + "/{environment}"
)

// RevealQuery asks for secret values (admin only).
type RevealQuery struct {
	Reveal bool `query:"reveal" doc:"Include secret values (admin only)"`
}

func registerProjects(s *Server, a huma.API) {
	tags := []string{"projects"}
	authed(s, a, huma.Operation{
		OperationID: "list-projects", Method: http.MethodGet, Path: projectsPath,
		Summary: "List projects", Description: "Archived projects only with `archived=true`.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Archived bool `query:"archived" doc:"Include archived projects"`
	}) (*out[[]apitypes.Project], error) {
		return reply(s.core.ListProjects(ctx, actor, in.Archived))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-project", Method: http.MethodPost, Path: projectsPath,
		Summary: "Create a project", Description: "Creates the project with a `production` environment. Scope: deploy, unlimited.",
		Tags: tags, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.CreateProjectRequest }) (*out[apitypes.Project], error) {
		return reply(s.core.CreateProject(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-project", Method: http.MethodGet, Path: projectPathT,
		Summary: "Get a project", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *ProjectPath) (*out[apitypes.Project], error) {
		return reply(s.core.GetProject(ctx, actor, in.Project))
	})
	authed(s, a, huma.Operation{
		OperationID: "archive-project", Method: http.MethodPost, Path: projectPathT + "/archive",
		Summary: "Archive a project", Tags: tags,
		Description: "Archives every service in the project (routes removed, containers stopped in the background; volumes and history kept) and hides the project from lists. Refused while a service is deploying. Scope: admin.",
	}, func(ctx context.Context, actor core.Actor, in *ProjectPath) (*out[apitypes.Project], error) {
		return reply(s.core.ArchiveProject(ctx, actor, in.Project))
	})
	authed(s, a, huma.Operation{
		OperationID: "project-overview", Method: http.MethodGet, Path: projectPathT + "/overview",
		Summary: "Project overview", Description: "Environments → services with status, releases, domains and staged-change counts, in one call.",
		Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *ProjectPath) (*out[apitypes.ProjectOverview], error) {
		return reply(s.core.Overview(ctx, actor, in.Project))
	})

	tags = []string{"environments"}
	authed(s, a, huma.Operation{
		OperationID: "list-environments", Method: http.MethodGet, Path: envsPath,
		Summary: "List environments", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *ProjectPath) (*out[[]apitypes.Environment], error) {
		return reply(s.core.ListEnvironments(ctx, actor, in.Project))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-environment", Method: http.MethodPost, Path: envsPath,
		Summary: "Create an environment", Description: "Placed where the project's environments of the same kind are; the first of a kind goes to the eligible server with the most free memory. Scope: deploy.",
		Tags: tags, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ProjectPath
		Body apitypes.CreateEnvironmentRequest
	}) (*out[apitypes.Environment], error) {
		return reply(s.core.CreateEnvironment(ctx, actor, in.Project, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-environment", Method: http.MethodGet, Path: envPathT,
		Summary: "Get an environment", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *EnvPath) (*out[apitypes.Environment], error) {
		return reply(s.core.GetEnvironment(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "clone-environment", Method: http.MethodPost, Path: envPathT + "/clone",
		Summary: "Clone an environment", Tags: tags, DefaultStatus: http.StatusCreated, Errors: withOffline,
		Description: "Creates a new environment with this one's services, settings, variables and (new, empty) volumes, placed like any new environment of its kind. " +
			"Values are copied only for callers that may read them (admin); otherwise reference-only values are copied, template credentials are made anew and the rest are listed in `skipped`. " +
			"Domains, git connections, releases, backups and staged changes are never copied. Nothing is deployed unless `deploy: true`, which promotes the source's images. Scope: read on this environment, deploy on the project.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		EnvPath
		Body apitypes.CloneEnvironmentRequest
	}) (*out[apitypes.CloneResult], error) {
		return reply(s.core.CloneEnvironment(ctx, actor, in.ref(), in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "export-project", Method: http.MethodGet, Path: projectPathT + "/export",
		Summary: "Export a project's config", Tags: []string{"projects"},
		Description: "The project's environments, services and non-secret settings as they are live, sorted by name. Variables are names only; values are never included. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *ProjectPath) (*out[apitypes.ProjectExport], error) {
		return reply(s.core.ExportProject(ctx, actor, in.Project))
	})

	authed(s, a, huma.Operation{
		OperationID: "get-changes", Method: http.MethodGet, Path: envPathT + "/changes",
		Summary: "Staged changes", Description: "The environment's staged (not yet live) changes as a diff. Values are masked unless `reveal=true` (admin).",
		Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		EnvPath
		RevealQuery
	}) (*out[apitypes.PendingChanges], error) {
		return reply(s.core.GetChanges(ctx, actor, in.ref(), in.Reveal))
	})
	authed(s, a, huma.Operation{
		OperationID: "apply-changes", Method: http.MethodPost, Path: envPathT + "/changes/apply",
		Summary: "Apply staged changes", Description: "Applies all staged changes atomically and starts a config release for every affected running service. Scope: deploy.",
		Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *EnvPath) (*out[apitypes.AppliedChanges], error) {
		return reply(s.core.ApplyChanges(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "discard-changes", Method: http.MethodPost, Path: envPathT + "/changes/discard",
		Summary: "Discard staged changes", Tags: tags,
		Description: "Discards the selected staged changes, or all of them without a body (or an empty selection). Selecting a change that isn't staged is a 404 and discards nothing. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		EnvPath
		Body *apitypes.DiscardChangesRequest
	}) (*out[apitypes.PendingChanges], error) {
		var sel apitypes.DiscardChangesRequest
		if in.Body != nil {
			sel = *in.Body
		}
		return reply(s.core.DiscardChanges(ctx, actor, in.ref(), sel))
	})

	tags = []string{"variables"}
	authed(s, a, huma.Operation{
		OperationID: "list-variables", Method: http.MethodGet, Path: envPathT + "/variables",
		Summary: "List variables", Description: "Shared (environment-level) and service variables. Names only unless `reveal=true` (admin).",
		Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		EnvPath
		RevealQuery
		Service string `query:"service" doc:"Only shared variables and this service's"`
	}) (*out[[]apitypes.Variable], error) {
		return reply(s.core.ListVariables(ctx, actor, in.ref(), in.Service, in.Reveal))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-variables", Method: http.MethodPatch, Path: envPathT + "/variables",
		Summary: "Stage variable changes", Description: "Sets and unsets variables as staged changes; `apply: true` also applies the environment's staged changes. Values may reference `${{service.KEY}}` or `${{shared.KEY}}`. Scope: deploy.",
		Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		EnvPath
		Body apitypes.SetVariablesRequest
	}) (*out[apitypes.ChangeResult], error) {
		return reply(s.core.SetVariables(ctx, actor, in.ref(), in.Body))
	})
}
