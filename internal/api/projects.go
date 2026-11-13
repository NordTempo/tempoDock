package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// Path parameters. Resources are addressed by name; names are unique in
// their parent. A project can be renamed; its ID stays stable. (Exported because Huma only finds parameters
// in exported embedded structs.)

// ProjectPath addresses a project.
type ProjectPath struct {
	Project    string `path:"project" doc:"Project name"`
	ExpectedID int64  `json:"-" header:"Dock-Project-ID" minimum:"1" doc:"Expected project identity for linked clients"`
}

// expectedProjectID lets authed constrain name-based requests to their identity.
func (p ProjectPath) expectedProjectID() int64 { return p.ExpectedID }

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
		OperationID: "rename-project", Method: http.MethodPatch, Path: projectPathT,
		Summary: "Rename a project", Description: "Changes its name and address, not its ID. Running services and domains are unchanged. Old-name URLs and scripts need updating. Scope: admin, whole project.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ProjectPath
		Body apitypes.RenameProjectRequest
	}) (*out[apitypes.Project], error) {
		return reply(s.core.RenameProject(ctx, actor, in.Project, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-project-by-id", Method: http.MethodGet, Path: Prefix + "/project-ids/{id}",
		Summary: "Get a project by stable ID", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID int64 `path:"id" minimum:"1"`
	}) (*out[apitypes.Project], error) {
		return reply(s.core.GetProjectByID(ctx, actor, in.ID))
	})
	authed(s, a, huma.Operation{
		OperationID: "archive-project", Method: http.MethodPost, Path: projectPathT + "/archive",
		Summary: "Archive a project", Tags: tags,
		Description: "Archives every service in the project (routes removed, containers stopped in the background; volumes and history kept) and hides the project from lists. Refused while a service is deploying. Scope: admin.",
	}, func(ctx context.Context, actor core.Actor, in *ProjectPath) (*out[apitypes.Project], error) {
		return reply(s.core.ArchiveProject(ctx, actor, in.Project))
	})
	authed(s, a, huma.Operation{
		OperationID: "delete-project", Method: http.MethodDelete, Path: projectPathT,
		Summary: "Delete an archived project", Tags: tags,
		Description: "Deletes an archived project for good (ADR 0078): its containers, networks, volumes and volume snapshots on its servers, its sites' analytics events, " +
			"and everything Dock knows of it, which frees its name and its domains. Its activity stays in the audit log; its backups stay in their restic repositories (`notes` says where). " +
			"`confirm` is the project's name. Every server it has something on must be online. Scope: admin, unlimited; a session needs a recent sign-in.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ProjectPath
		Body apitypes.DeleteProjectRequest
	}) (*out[apitypes.ProjectDeleted], error) {
		return reply(s.core.DeleteProject(ctx, actor, in.Project, in.Body))
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
			"Reference-only values are always copied. Literal values are copied only for admins, and between production and non-production only with `copy_values` (admin; listed in `copied_across_kinds`); otherwise template credentials are made anew and the rest are listed in `skipped`. " +
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
		OperationID: "plan-project-file", Method: http.MethodPost, Path: projectPathT + "/plan",
		Summary: "Plan a project file", Tags: []string{"projects"},
		Description: "Compares a project file (the export's format, TOML or JSON) with the live state: what applying it stages (`changes`), " +
			"live things the file doesn't mention (`kept`; `prune` lists the removable ones as removals instead) and what needs a person (`manual`). " +
			"The project may not exist yet. Variable values are never in a plan. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ProjectPath
		Body apitypes.ApplyRequest
	}) (*out[apitypes.ApplyPlan], error) {
		return reply(s.core.PlanProject(ctx, actor, in.Project, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "apply-project-file", Method: http.MethodPost, Path: projectPathT + "/apply",
		Summary: "Stage a project file", Tags: []string{"projects"},
		Description: "Turns a project file's plan into staged changes in each of its environments; nothing goes live. A missing project and missing environments are created empty. " +
			"Removals only with `prune` (archiving services needs admin). With `deploy`, each touched environment's staged changes are applied afterwards, as `changes/apply` does. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ProjectPath
		Body apitypes.ApplyRequest
	}) (*out[apitypes.ApplyResult], error) {
		return reply(s.core.ApplyProject(ctx, actor, in.Project, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "import-compose", Method: http.MethodPost, Path: projectPathT + "/import/compose",
		Summary: "Import a Docker Compose file", Tags: []string{"projects"},
		Description: "Converts a docker-compose.yml into a project file and plans it: the file, warnings for everything that doesn't map exactly, the detected secrets (names only) and the plan. " +
			"With `apply`, stages the file with the secrets' values (the Compose file's, or `values`) and applies it, as `apply` with `deploy` does. The project may not exist yet. Scope: read, or deploy with `apply`.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ProjectPath
		Body apitypes.ComposeImportRequest
	}) (*out[apitypes.ComposeImport], error) {
		return reply(s.core.ImportCompose(ctx, actor, in.Project, in.Body))
	})
	// The same export as TOML, as `dock project export` prints it, so the
	// UI can download it without a TOML encoder of its own.
	authed(s, a, huma.Operation{
		OperationID: "export-project-toml", Method: http.MethodGet, Path: projectPathT + "/export.toml",
		Summary: "Export a project's config as TOML", Tags: []string{"projects"},
		Description: "The export as a `<project>.dock.toml` attachment, exactly as `dock project export` prints it. Scope: read.",
		Responses: map[string]*huma.Response{"200": {
			Description: "Export",
			Content:     map[string]*huma.MediaType{"application/toml": {Schema: &huma.Schema{Type: huma.TypeString}}},
		}},
	}, func(ctx context.Context, actor core.Actor, in *ProjectPath) (*huma.StreamResponse, error) {
		ex, err := s.core.ExportProject(ctx, actor, in.Project)
		if err != nil {
			return nil, err
		}
		b, err := ex.TOML()
		if err != nil {
			return nil, err
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			hctx.SetHeader("Content-Type", "application/toml; charset=utf-8")
			hctx.SetHeader("Content-Disposition", fmt.Sprintf("attachment; filename=%q", ex.Project+".dock.toml"))
			hctx.SetStatus(http.StatusOK)
			_, _ = hctx.BodyWriter().Write(b)
		}}, nil
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
	authed(s, a, huma.Operation{
		OperationID: "switch-references", Method: http.MethodPost, Path: servicePathT + "/switch-references",
		Summary:     "Switch references to another service",
		Description: "Rewrites every `${{service.KEY}}` reference to this service in the environment's variables to `${{to.KEY}}` and applies it: the services using it get config releases (ADR 0061, \"use the restored database\"). Switching back is the same call the other way. `to` must be a live service of the environment with every referenced key. Refused while the environment has other staged changes. `dry_run` only lists the services that would change. Scope: deploy.",
		Tags:        tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.SwitchReferencesRequest
	}) (*out[apitypes.SwitchReferencesResult], error) {
		return reply(s.core.SwitchReferences(ctx, actor, in.ref(), in.Body))
	})
}
