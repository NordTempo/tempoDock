package tbhttp

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/datapanel/datahttp"
	"github.com/NordTempo/tempoDock/internal/tempobase"
)

// The data panel's endpoints under /connections/{connection}/data (ADR
// 0024 section 3): Dock's operations, query parameters, bodies, response
// types, statuses and NDJSON framing, with a connection instead of a
// project, environment and service. The App checks the caller's data
// permission; a connection the caller can't see is 404.

const dataPathT = Prefix + "/connections/{connection}/data"

// TablePath addresses one table of a connection, as "schema.name".
type TablePath struct {
	ConnPath
	Table string `path:"table" example:"public.orders" doc:"Table as schema.name"`
}

// ChangesetPath addresses one changeset.
type ChangesetPath struct {
	ConnPath
	ID int64 `path:"id" doc:"Changeset ID"`
}

// SavedQueryPath addresses one saved query.
type SavedQueryPath struct {
	ConnPath
	ID int64 `path:"id" doc:"Saved query ID"`
}

// dataOp adds the shared summary, description and error statuses to a
// data operation, in tempoBase's words.
func dataOp(op huma.Operation) huma.Operation {
	op.Tags = []string{"data"}
	op.Errors = datahttp.Errors
	op.Summary, op.Description = datahttp.Describe(op.OperationID, data.TempoBaseRoles, tempobase.Words)
	return op
}

func registerData(s *server, a huma.API) {
	authed(s, a, dataOp(huma.Operation{
		OperationID: "get-data", Method: http.MethodGet, Path: dataPathT,
	}), func(ctx context.Context, act tempobase.Actor, in *ConnPath) (*out[apitypes.DataInfo], error) {
		return reply(s.app.DataInfo(ctx, act, in.Connection))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "setup-data", Method: http.MethodPost, Path: dataPathT + "/setup",
	}), func(ctx context.Context, act tempobase.Actor, in *ConnPath) (*out[apitypes.DataInfo], error) {
		return reply(s.app.SetupData(ctx, act, in.Connection))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "get-data-schema", Method: http.MethodGet, Path: dataPathT + "/schema",
	}), func(ctx context.Context, act tempobase.Actor, in *ConnPath) (*out[apitypes.DataSchema], error) {
		return reply(s.app.DataSchema(ctx, act, in.Connection))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "get-data-rows", Method: http.MethodGet, Path: dataPathT + "/tables/{table}/rows",
	}), func(ctx context.Context, act tempobase.Actor, in *struct {
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
		return reply(s.app.DataRows(ctx, act, in.Connection, req))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "get-data-references", Method: http.MethodGet, Path: dataPathT + "/tables/{table}/references",
	}), func(ctx context.Context, act tempobase.Actor, in *struct {
		TablePath
		Key   string `query:"key" required:"true" doc:"The row's primary key as a JSON object, column to value, e.g. {\"id\":\"42\"}"`
		Limit int    `query:"limit" minimum:"0" maximum:"100" doc:"Rows per foreign key; 0 means 10"`
	}) (*out[[]apitypes.DataReferences], error) {
		req := apitypes.DataReferencesRequest{Table: in.Table, Limit: in.Limit}
		var err error
		if req.Key, err = datahttp.ParseKey(in.Key); err != nil {
			return nil, err
		}
		return reply(s.app.DataReferences(ctx, act, in.Connection, req))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "get-data-keys", Method: http.MethodGet, Path: dataPathT + "/keys",
	}), func(ctx context.Context, act tempobase.Actor, in *struct {
		ConnPath
		datahttp.KeysQuery
	}) (*out[apitypes.DataKeyPage], error) {
		return reply(s.app.DataKeys(ctx, act, in.Connection, in.Request()))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "get-data-key", Method: http.MethodGet, Path: dataPathT + "/keys/value",
	}), func(ctx context.Context, act tempobase.Actor, in *struct {
		ConnPath
		datahttp.KeyQuery
	}) (*out[apitypes.DataKeyValue], error) {
		return reply(s.app.DataKey(ctx, act, in.Connection, in.Request()))
	})
	registerDataQuery(s, a)
	authed(s, a, dataOp(huma.Operation{
		OperationID: "get-data-history", Method: http.MethodGet, Path: dataPathT + "/history",
	}), func(ctx context.Context, act tempobase.Actor, in *struct {
		ConnPath
		Before int64 `query:"before" minimum:"0" doc:"Only entries with a smaller audit_id"`
		Limit  int   `query:"limit" default:"50" minimum:"1" maximum:"200"`
	}) (*out[[]apitypes.DataHistoryEntry], error) {
		return reply(s.app.DataHistory(ctx, act, in.Connection, in.Before, in.Limit))
	})

	authed(s, a, dataOp(huma.Operation{
		OperationID: "get-data-insights", Method: http.MethodGet, Path: dataPathT + "/insights",
	}), func(ctx context.Context, act tempobase.Actor, in *struct {
		ConnPath
		datahttp.InsightsQuery
	}) (*out[apitypes.DataInsights], error) {
		return reply(s.app.DataInsights(ctx, act, in.Connection, in.Request()))
	})

	authed(s, a, dataOp(huma.Operation{
		OperationID: "cancel-data-session", Method: http.MethodPost, Path: dataPathT + "/insights/sessions/{session}/cancel",
	}), func(ctx context.Context, act tempobase.Actor, in *struct {
		ConnPath
		Session string `path:"session" example:"4121.1794328800123456" doc:"Session ID from the sessions view"`
		Body    *apitypes.DataCancelRequest
	}) (*out[apitypes.DataCancelResult], error) {
		var req apitypes.DataCancelRequest
		if in.Body != nil {
			req = *in.Body
		}
		return reply(s.app.CancelDataSession(ctx, act, in.Connection, in.Session, req))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "reset-data-query-stats", Method: http.MethodPost, Path: dataPathT + "/insights/queries/reset",
		DefaultStatus: http.StatusNoContent,
	}), func(ctx context.Context, act tempobase.Actor, in *ConnPath) (*struct{}, error) {
		return nil, s.app.ResetDataQueryStats(ctx, act, in.Connection)
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "enable-data-query-stats", Method: http.MethodPost, Path: dataPathT + "/insights/queries/enable",
	}), func(ctx context.Context, act tempobase.Actor, in *ConnPath) (*out[apitypes.DataQueryStatsResult], error) {
		return reply(s.app.EnableDataQueryStats(ctx, act, in.Connection))
	})

	authed(s, a, dataOp(huma.Operation{
		OperationID: "list-saved-queries", Method: http.MethodGet, Path: dataPathT + "/saved-queries",
	}), func(ctx context.Context, act tempobase.Actor, in *ConnPath) (*out[[]apitypes.DataSavedQuery], error) {
		return reply(s.app.ListSavedQueries(ctx, act, in.Connection))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "create-saved-query", Method: http.MethodPost, Path: dataPathT + "/saved-queries",
		DefaultStatus: http.StatusCreated, MaxBodyBytes: datahttp.SQLBodyBytes,
	}), func(ctx context.Context, act tempobase.Actor, in *struct {
		ConnPath
		Body apitypes.SaveDataQueryRequest
	}) (*out[apitypes.DataSavedQuery], error) {
		return reply(s.app.SaveDataQuery(ctx, act, in.Connection, 0, in.Body))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "update-saved-query", Method: http.MethodPut, Path: dataPathT + "/saved-queries/{id}",
		MaxBodyBytes: datahttp.SQLBodyBytes,
	}), func(ctx context.Context, act tempobase.Actor, in *struct {
		SavedQueryPath
		Body apitypes.SaveDataQueryRequest
	}) (*out[apitypes.DataSavedQuery], error) {
		return reply(s.app.SaveDataQuery(ctx, act, in.Connection, in.ID, in.Body))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "delete-saved-query", Method: http.MethodDelete, Path: dataPathT + "/saved-queries/{id}",
		DefaultStatus: http.StatusNoContent,
	}), func(ctx context.Context, act tempobase.Actor, in *SavedQueryPath) (*empty, error) {
		return nil, s.app.DeleteSavedQuery(ctx, act, in.Connection, in.ID)
	})

	authed(s, a, dataOp(huma.Operation{
		OperationID: "list-changesets", Method: http.MethodGet, Path: dataPathT + "/changesets",
	}), func(ctx context.Context, act tempobase.Actor, in *struct {
		ConnPath
		Status apitypes.DataChangesetStatus `query:"status" enum:"pending,committed,rejected,expired,conflict" doc:"Only changesets with this status"`
	}) (*out[[]apitypes.DataChangeset], error) {
		return reply(s.app.ListDataChangesets(ctx, act, in.Connection, in.Status))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "create-changeset", Method: http.MethodPost, Path: dataPathT + "/changesets",
		MaxBodyBytes: datahttp.ChangesetBodyBytes,
	}), func(ctx context.Context, act tempobase.Actor, in *struct {
		ConnPath
		Body apitypes.DataChangesetRequest
	}) (*datahttp.Created[apitypes.DataChangeset], error) {
		cs, err := s.app.CreateDataChangeset(ctx, act, in.Connection, in.Body)
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
	}), func(ctx context.Context, act tempobase.Actor, in *ChangesetPath) (*out[apitypes.DataChangeset], error) {
		return reply(s.app.GetDataChangeset(ctx, act, in.Connection, in.ID))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "commit-changeset", Method: http.MethodPost, Path: dataPathT + "/changesets/{id}/commit",
	}), func(ctx context.Context, act tempobase.Actor, in *struct {
		ChangesetPath
		Body *apitypes.DataCommitRequest
	}) (*out[apitypes.DataChangeset], error) {
		var req apitypes.DataCommitRequest
		if in.Body != nil {
			req = *in.Body
		}
		return reply(s.app.CommitDataChangeset(ctx, act, in.Connection, in.ID, req))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "reject-changeset", Method: http.MethodPost, Path: dataPathT + "/changesets/{id}/reject",
	}), func(ctx context.Context, act tempobase.Actor, in *struct {
		ChangesetPath
		Body *apitypes.DataRejectRequest
	}) (*out[apitypes.DataChangeset], error) {
		var req apitypes.DataRejectRequest
		if in.Body != nil {
			req = *in.Body
		}
		return reply(s.app.RejectDataChangeset(ctx, act, in.Connection, in.ID, req))
	})

	authed(s, a, dataOp(huma.Operation{
		OperationID: "set-sensitive-columns", Method: http.MethodPut, Path: dataPathT + "/sensitive-columns",
	}), func(ctx context.Context, act tempobase.Actor, in *struct {
		ConnPath
		Body apitypes.SetSensitiveColumnsRequest
	}) (*out[[]apitypes.DataColumnRef], error) {
		return reply(s.app.SetSensitiveColumns(ctx, act, in.Connection, in.Body))
	})
	authed(s, a, dataOp(huma.Operation{
		OperationID: "set-sensitive-keys", Method: http.MethodPut, Path: dataPathT + "/sensitive-keys",
	}), func(ctx context.Context, act tempobase.Actor, in *struct {
		ConnPath
		Body apitypes.SetSensitiveKeysRequest
	}) (*out[[]string], error) {
		return reply(s.app.SetSensitiveKeys(ctx, act, in.Connection, in.Body))
	})
}

// registerDataQuery registers POST /data/query, which streams NDJSON
// frames.
func registerDataQuery(s *server, a huma.API) {
	op := dataOp(huma.Operation{
		OperationID: "data-query", Method: http.MethodPost, Path: dataPathT + "/query",
		MaxBodyBytes: datahttp.SQLBodyBytes, Responses: datahttp.QueryResponses(a),
	})
	authed(s, a, op, func(ctx context.Context, act tempobase.Actor, in *struct {
		ConnPath
		Body apitypes.DataQueryRequest
	}) (*huma.StreamResponse, error) {
		stream, err := s.app.DataQuery(ctx, act, in.Connection, in.Body)
		if err != nil {
			return nil, err
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			datahttp.StreamFrames(hctx, stream, s.problem)
		}}, nil
	})
}
