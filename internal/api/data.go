package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/datapanel/datahttp"
)

// Database panel endpoints (ADR 0016): everything under
// /services/{s}/data, the data access policy at its three scopes, and a
// token's data access override. Core checks the caller's data permission.

// dataCore is the part of core the data endpoints call. *core.Core is the
// real one; handler tests swap in a fake through Server.data.
type dataCore interface {
	DataInfo(ctx context.Context, a core.Actor, ref core.ServiceRef) (apitypes.DataInfo, error)
	UpdateDatabaseSettings(ctx context.Context, a core.Actor, ref core.ServiceRef, req apitypes.UpdateDatabaseSettingsRequest) (apitypes.Service, error)
	SetupData(ctx context.Context, a core.Actor, ref core.ServiceRef) (apitypes.DataInfo, error)
	DataSchema(ctx context.Context, a core.Actor, ref core.ServiceRef) (apitypes.DataSchema, error)
	DataRows(ctx context.Context, a core.Actor, ref core.ServiceRef, req apitypes.DataPageRequest) (apitypes.DataPage, error)
	DataKeys(ctx context.Context, a core.Actor, ref core.ServiceRef, req apitypes.DataKeysRequest) (apitypes.DataKeyPage, error)
	DataKey(ctx context.Context, a core.Actor, ref core.ServiceRef, req apitypes.DataKeyRequest) (apitypes.DataKeyValue, error)
	DataReferences(ctx context.Context, a core.Actor, ref core.ServiceRef, req apitypes.DataReferencesRequest) ([]apitypes.DataReferences, error)
	DataQuery(ctx context.Context, a core.Actor, ref core.ServiceRef, req apitypes.DataQueryRequest) (core.Stream[apitypes.DataQueryFrame], error)
	DataHistory(ctx context.Context, a core.Actor, ref core.ServiceRef, before int64, limit int) ([]apitypes.DataHistoryEntry, error)
	ListDataChangesets(ctx context.Context, a core.Actor, ref core.ServiceRef, status apitypes.DataChangesetStatus) ([]apitypes.DataChangeset, error)
	GetDataChangeset(ctx context.Context, a core.Actor, ref core.ServiceRef, id int64) (apitypes.DataChangeset, error)
	CreateDataChangeset(ctx context.Context, a core.Actor, ref core.ServiceRef, req apitypes.DataChangesetRequest) (apitypes.DataChangeset, error)
	CommitDataChangeset(ctx context.Context, a core.Actor, ref core.ServiceRef, id int64, req apitypes.DataCommitRequest) (apitypes.DataChangeset, error)
	RejectDataChangeset(ctx context.Context, a core.Actor, ref core.ServiceRef, id int64, req apitypes.DataRejectRequest) (apitypes.DataChangeset, error)
	ListSavedQueries(ctx context.Context, a core.Actor, ref core.ServiceRef) ([]apitypes.DataSavedQuery, error)
	SaveDataQuery(ctx context.Context, a core.Actor, ref core.ServiceRef, id int64, req apitypes.SaveDataQueryRequest) (apitypes.DataSavedQuery, error)
	DeleteSavedQuery(ctx context.Context, a core.Actor, ref core.ServiceRef, id int64) error
	SetSensitiveColumns(ctx context.Context, a core.Actor, ref core.ServiceRef, req apitypes.SetSensitiveColumnsRequest) ([]apitypes.DataColumnRef, error)
	SetSensitiveKeys(ctx context.Context, a core.Actor, ref core.ServiceRef, req apitypes.SetSensitiveKeysRequest) ([]string, error)
	GetDataPolicy(ctx context.Context, a core.Actor, ref core.EnvRef) (apitypes.DataPolicy, error)
	SetDataPolicy(ctx context.Context, a core.Actor, ref core.EnvRef, req apitypes.UpdateDataPolicyRequest) (apitypes.DataPolicy, error)
	UpdateToken(ctx context.Context, a core.Actor, id int64, req apitypes.UpdateTokenRequest) (apitypes.Token, error)
	GetAuditDetail(ctx context.Context, a core.Actor, id int64) (apitypes.AuditDetail, error)
	DataInsights(ctx context.Context, a core.Actor, ref core.ServiceRef, req apitypes.DataInsightsRequest) (apitypes.DataInsights, error)
	CancelDataSession(ctx context.Context, a core.Actor, ref core.ServiceRef, session string, req apitypes.DataCancelRequest) (apitypes.DataCancelResult, error)
	ResetDataQueryStats(ctx context.Context, a core.Actor, ref core.ServiceRef) error
	EnableDataQueryStats(ctx context.Context, a core.Actor, ref core.ServiceRef) (apitypes.DataQueryStatsResult, error)
}

var _ dataCore = (*core.Core)(nil)

// dataAPI returns the core the data endpoints call: the fake a test set,
// else the real core.
func (s *Server) dataAPI() dataCore {
	if s.data != nil {
		return s.data
	}
	return s.core
}

const dataPathT = servicePathT + "/data"

// dataErrors are the data operations' error statuses (datahttp.Errors).
var dataErrors = datahttp.Errors

// TablePath addresses one table of a database service, as "schema.name".
type TablePath struct {
	ServicePath
	Table string `path:"table" example:"public.orders" doc:"Table as schema.name"`
}

// ChangesetPath addresses one changeset.
type ChangesetPath struct {
	ServicePath
	ID int64 `path:"id" doc:"Changeset ID"`
}

// SavedQueryPath addresses one saved query.
type SavedQueryPath struct {
	ServicePath
	ID int64 `path:"id" doc:"Saved query ID"`
}

func registerData(s *Server, a huma.API) {
	tags := []string{"data"}
	// dataOp adds the shared summary and description (with Dock's own
	// sentences after it) to a data operation.
	dataOp := func(op huma.Operation, more ...string) huma.Operation {
		op.Tags = tags
		op.Errors = dataErrors
		op.Summary, op.Description = datahttp.Describe(op.OperationID, data.DockRoles, core.DataWords)
		op.Description += strings.Join(more, "")
		return op
	}

	authed(s, a, dataOp(huma.Operation{
		OperationID: "get-data", Method: http.MethodGet, Path: dataPathT,
	}, " Services that aren't databases are 404."), func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.DataInfo], error) {
		return reply(s.dataAPI().DataInfo(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-database-settings", Method: http.MethodPut, Path: servicePathT + "/database-settings",
		Summary: "Change database settings", Tags: tags,
		Description: "Override database detection and name the variable with the connection URL. Applies immediately: never staged, never a release. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.UpdateDatabaseSettingsRequest
	}) (*out[apitypes.Service], error) {
		return reply(s.dataAPI().UpdateDatabaseSettings(ctx, actor, in.ref(), in.Body))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "setup-data", Method: http.MethodPost, Path: dataPathT + "/setup",
	}), func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.DataInfo], error) {
		return reply(s.dataAPI().SetupData(ctx, actor, in.ref()))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "get-data-schema", Method: http.MethodGet, Path: dataPathT + "/schema",
	}), func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.DataSchema], error) {
		return reply(s.dataAPI().DataSchema(ctx, actor, in.ref()))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "get-data-rows", Method: http.MethodGet, Path: dataPathT + "/tables/{table}/rows",
	}), func(ctx context.Context, actor core.Actor, in *struct {
		TablePath
		Order  string `query:"order" doc:"Column to sort by, -column for descending; default the primary key"`
		After  string `query:"after" doc:"The previous page's next cursor"`
		Limit  int    `query:"limit" minimum:"0" maximum:"1000" doc:"Rows per page; 0 means 100"`
		Filter string `query:"filter" doc:"JSON array of DataFilter, ANDed, e.g. [{\"column\":\"status\",\"op\":\"eq\",\"value\":\"paid\"}]"`
		Count  bool   `query:"count" doc:"Also count the matching rows exactly (under the statement timeout)"`
		Full   bool   `query:"full" doc:"Untruncated cells; only with limit 1"`
		Reveal bool   `query:"reveal" doc:"Show the table's sensitive columns instead of masking them. Needs sensitive visible (403 otherwise); audited as data.reveal"`
	}) (*out[apitypes.DataPage], error) {
		req := apitypes.DataPageRequest{Table: in.Table, Order: in.Order, After: in.After, Limit: in.Limit, Count: in.Count, Full: in.Full, Reveal: in.Reveal}
		var err error
		if req.Filters, err = datahttp.ParseFilters(in.Filter); err != nil {
			return nil, err
		}
		return reply(s.dataAPI().DataRows(ctx, actor, in.ref(), req))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "get-data-references", Method: http.MethodGet, Path: dataPathT + "/tables/{table}/references",
	}), func(ctx context.Context, actor core.Actor, in *struct {
		TablePath
		Key   string `query:"key" required:"true" doc:"The row's primary key as a JSON object, column to value, e.g. {\"id\":\"42\"}"`
		Limit int    `query:"limit" minimum:"0" maximum:"100" doc:"Rows per foreign key; 0 means 10"`
	}) (*out[[]apitypes.DataReferences], error) {
		req := apitypes.DataReferencesRequest{Table: in.Table, Limit: in.Limit}
		var err error
		if req.Key, err = datahttp.ParseKey(in.Key); err != nil {
			return nil, err
		}
		return reply(s.dataAPI().DataReferences(ctx, actor, in.ref(), req))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "get-data-keys", Method: http.MethodGet, Path: dataPathT + "/keys",
	}), func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		datahttp.KeysQuery
	}) (*out[apitypes.DataKeyPage], error) {
		return reply(s.dataAPI().DataKeys(ctx, actor, in.ref(), in.Request()))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "get-data-key", Method: http.MethodGet, Path: dataPathT + "/keys/value",
	}), func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		datahttp.KeyQuery
	}) (*out[apitypes.DataKeyValue], error) {
		return reply(s.dataAPI().DataKey(ctx, actor, in.ref(), in.Request()))
	})
	registerDataQuery(s, a)
	authed(s, a, dataOp(huma.Operation{
		OperationID: "get-data-history", Method: http.MethodGet, Path: dataPathT + "/history",
	}), func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Before int64 `query:"before" minimum:"0" doc:"Only entries with a smaller audit_id"`
		Limit  int   `query:"limit" default:"50" minimum:"1" maximum:"200"`
	}) (*out[[]apitypes.DataHistoryEntry], error) {
		return reply(s.dataAPI().DataHistory(ctx, actor, in.ref(), in.Before, in.Limit))
	})

	authed(s, a, dataOp(huma.Operation{
		OperationID: "get-data-insights", Method: http.MethodGet, Path: dataPathT + "/insights",
	}), func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		datahttp.InsightsQuery
	}) (*out[apitypes.DataInsights], error) {
		return reply(s.dataAPI().DataInsights(ctx, actor, in.ref(), in.Request()))
	})

	authed(s, a, dataOp(huma.Operation{
		OperationID: "cancel-data-session", Method: http.MethodPost, Path: dataPathT + "/insights/sessions/{session}/cancel",
	}), func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Session string `path:"session" example:"4121.1794328800123456" doc:"Session ID from the sessions view"`
		Body    *apitypes.DataCancelRequest
	}) (*out[apitypes.DataCancelResult], error) {
		var req apitypes.DataCancelRequest
		if in.Body != nil {
			req = *in.Body
		}
		return reply(s.dataAPI().CancelDataSession(ctx, actor, in.ref(), in.Session, req))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "reset-data-query-stats", Method: http.MethodPost, Path: dataPathT + "/insights/queries/reset",
		DefaultStatus: http.StatusNoContent,
	}), func(ctx context.Context, actor core.Actor, in *ServicePath) (*struct{}, error) {
		return nil, s.dataAPI().ResetDataQueryStats(ctx, actor, in.ref())
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "enable-data-query-stats", Method: http.MethodPost, Path: dataPathT + "/insights/queries/enable",
	}), func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.DataQueryStatsResult], error) {
		return reply(s.dataAPI().EnableDataQueryStats(ctx, actor, in.ref()))
	})

	authed(s, a, dataOp(huma.Operation{
		OperationID: "list-saved-queries", Method: http.MethodGet, Path: dataPathT + "/saved-queries",
	}), func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[[]apitypes.DataSavedQuery], error) {
		return reply(s.dataAPI().ListSavedQueries(ctx, actor, in.ref()))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "create-saved-query", Method: http.MethodPost, Path: dataPathT + "/saved-queries",
		DefaultStatus: http.StatusCreated, MaxBodyBytes: datahttp.SQLBodyBytes,
	}), func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.SaveDataQueryRequest
	}) (*out[apitypes.DataSavedQuery], error) {
		return reply(s.dataAPI().SaveDataQuery(ctx, actor, in.ref(), 0, in.Body))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "update-saved-query", Method: http.MethodPut, Path: dataPathT + "/saved-queries/{id}",
		MaxBodyBytes: datahttp.SQLBodyBytes,
	}), func(ctx context.Context, actor core.Actor, in *struct {
		SavedQueryPath
		Body apitypes.SaveDataQueryRequest
	}) (*out[apitypes.DataSavedQuery], error) {
		return reply(s.dataAPI().SaveDataQuery(ctx, actor, in.ref(), in.ID, in.Body))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "delete-saved-query", Method: http.MethodDelete, Path: dataPathT + "/saved-queries/{id}",
		DefaultStatus: http.StatusNoContent,
	}), func(ctx context.Context, actor core.Actor, in *SavedQueryPath) (*struct{}, error) {
		return nil, s.dataAPI().DeleteSavedQuery(ctx, actor, in.ref(), in.ID)
	})

	authed(s, a, dataOp(huma.Operation{
		OperationID: "list-changesets", Method: http.MethodGet, Path: dataPathT + "/changesets",
	}), func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Status apitypes.DataChangesetStatus `query:"status" enum:"pending,committed,rejected,expired,conflict" doc:"Only changesets with this status"`
	}) (*out[[]apitypes.DataChangeset], error) {
		return reply(s.dataAPI().ListDataChangesets(ctx, actor, in.ref(), in.Status))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "create-changeset", Method: http.MethodPost, Path: dataPathT + "/changesets",
		MaxBodyBytes: datahttp.ChangesetBodyBytes,
	}), func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.DataChangesetRequest
	}) (*datahttp.Created[apitypes.DataChangeset], error) {
		cs, err := s.dataAPI().CreateDataChangeset(ctx, actor, in.ref(), in.Body)
		if err != nil {
			return nil, err
		}
		status := http.StatusCreated
		if in.Body.DryRun {
			status = http.StatusOK
		}
		return &datahttp.Created[apitypes.DataChangeset]{Status: status, Body: cs}, nil
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "get-changeset", Method: http.MethodGet, Path: dataPathT + "/changesets/{id}",
	}), func(ctx context.Context, actor core.Actor, in *ChangesetPath) (*out[apitypes.DataChangeset], error) {
		return reply(s.dataAPI().GetDataChangeset(ctx, actor, in.ref(), in.ID))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "commit-changeset", Method: http.MethodPost, Path: dataPathT + "/changesets/{id}/commit",
	}), func(ctx context.Context, actor core.Actor, in *struct {
		ChangesetPath
		Body *apitypes.DataCommitRequest
	}) (*out[apitypes.DataChangeset], error) {
		var req apitypes.DataCommitRequest
		if in.Body != nil {
			req = *in.Body
		}
		return reply(s.dataAPI().CommitDataChangeset(ctx, actor, in.ref(), in.ID, req))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "reject-changeset", Method: http.MethodPost, Path: dataPathT + "/changesets/{id}/reject",
	}), func(ctx context.Context, actor core.Actor, in *struct {
		ChangesetPath
		Body *apitypes.DataRejectRequest
	}) (*out[apitypes.DataChangeset], error) {
		var req apitypes.DataRejectRequest
		if in.Body != nil {
			req = *in.Body
		}
		return reply(s.dataAPI().RejectDataChangeset(ctx, actor, in.ref(), in.ID, req))
	})

	authed(s, a, dataOp(huma.Operation{
		OperationID: "set-sensitive-columns", Method: http.MethodPut, Path: dataPathT + "/sensitive-columns",
	}), func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.SetSensitiveColumnsRequest
	}) (*out[[]apitypes.DataColumnRef], error) {
		return reply(s.dataAPI().SetSensitiveColumns(ctx, actor, in.ref(), in.Body))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "set-sensitive-keys", Method: http.MethodPut, Path: dataPathT + "/sensitive-keys",
	}), func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.SetSensitiveKeysRequest
	}) (*out[[]string], error) {
		return reply(s.dataAPI().SetSensitiveKeys(ctx, actor, in.ref(), in.Body))
	})

	registerDataPolicy(s, a)

	authed(s, a, huma.Operation{
		OperationID: "update-token", Method: http.MethodPatch, Path: Prefix + "/tokens/{id}",
		Summary: "Change a token", Tags: []string{"tokens"},
		Description: "Sets the token's own data access (`data`), or removes it with `data: null` so the token follows the data policy's tokens rule. Scope: admin, unlimited.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID   int64 `path:"id"`
		Body apitypes.UpdateTokenRequest
	}) (*out[apitypes.Token], error) {
		return reply(s.dataAPI().UpdateToken(ctx, actor, in.ID, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-audit-detail", Method: http.MethodGet, Path: Prefix + "/audit/{id}/detail",
		Summary: "Audit event detail", Tags: []string{"audit"},
		Description: "The SQL, rows, duration and outcome of a data.* audit event (its detail_id), to anyone who may see the event.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID int64 `path:"id" doc:"The event's detail_id"`
	}) (*out[apitypes.AuditDetail], error) {
		return reply(s.dataAPI().GetAuditDetail(ctx, actor, in.ID))
	})
}

// registerDataPolicy registers GET and PUT of the data access policy at its
// three scopes: default, project and environment.
func registerDataPolicy(s *Server, a huma.API) {
	tags := []string{"data"}
	authed(s, a, huma.Operation{
		OperationID: "get-default-data-policy", Method: http.MethodGet, Path: Prefix + "/data-policy",
		Summary: "Default data policy", Tags: tags,
		Description: "The rules every project inherits, and the effective rule per subject and environment kind with where it comes from.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.DataPolicy], error) {
		return reply(s.dataAPI().GetDataPolicy(ctx, actor, core.EnvRef{}))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-default-data-policy", Method: http.MethodPut, Path: Prefix + "/data-policy",
		Summary: "Set the default data policy", Tags: tags,
		Description: "Replaces the default rules; subjects and kinds left out use the built-in defaults. Admin.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.UpdateDataPolicyRequest
	}) (*out[apitypes.DataPolicy], error) {
		return reply(s.dataAPI().SetDataPolicy(ctx, actor, core.EnvRef{}, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-project-data-policy", Method: http.MethodGet, Path: projectPathT + "/data-policy",
		Summary: "Project data policy", Tags: tags,
		Description: "The project's own rules and the effective rules for it.",
	}, func(ctx context.Context, actor core.Actor, in *ProjectPath) (*out[apitypes.DataPolicy], error) {
		return reply(s.dataAPI().GetDataPolicy(ctx, actor, core.EnvRef{Project: in.Project}))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-project-data-policy", Method: http.MethodPut, Path: projectPathT + "/data-policy",
		Summary: "Set a project's data policy", Tags: tags,
		Description: "Replaces the project's rules; anything left out is inherited from the default. Admin.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ProjectPath
		Body apitypes.UpdateDataPolicyRequest
	}) (*out[apitypes.DataPolicy], error) {
		return reply(s.dataAPI().SetDataPolicy(ctx, actor, core.EnvRef{Project: in.Project}, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-environment-data-policy", Method: http.MethodGet, Path: envPathT + "/data-policy",
		Summary: "Environment data policy", Tags: tags,
		Description: "The environment's own rules and the effective rules for its kind.",
	}, func(ctx context.Context, actor core.Actor, in *EnvPath) (*out[apitypes.DataPolicy], error) {
		return reply(s.dataAPI().GetDataPolicy(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-environment-data-policy", Method: http.MethodPut, Path: envPathT + "/data-policy",
		Summary: "Set an environment's data policy", Tags: tags,
		Description: "Replaces the environment's rules, which may only name its own kind; anything left out is inherited from the project. Admin.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		EnvPath
		Body apitypes.UpdateDataPolicyRequest
	}) (*out[apitypes.DataPolicy], error) {
		return reply(s.dataAPI().SetDataPolicy(ctx, actor, in.ref(), in.Body))
	})
}

// registerDataQuery registers POST /data/query, which streams NDJSON frames.
func registerDataQuery(s *Server, a huma.API) {
	op := huma.Operation{
		OperationID: "data-query", Method: http.MethodPost, Path: dataPathT + "/query",
		Tags: []string{"data"}, Errors: dataErrors, MaxBodyBytes: datahttp.SQLBodyBytes,
		Responses: datahttp.QueryResponses(a),
	}
	op.Summary, op.Description = datahttp.Describe(op.OperationID, data.DockRoles, core.DataWords)
	authed(s, a, op, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.DataQueryRequest
	}) (*huma.StreamResponse, error) {
		stream, err := s.dataAPI().DataQuery(ctx, actor, in.ref(), in.Body)
		if err != nil {
			return nil, err
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			datahttp.StreamFrames(hctx, stream, s.problem)
		}}, nil
	})
}
