package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
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
	GetDataPolicy(ctx context.Context, a core.Actor, ref core.EnvRef) (apitypes.DataPolicy, error)
	SetDataPolicy(ctx context.Context, a core.Actor, ref core.EnvRef, req apitypes.UpdateDataPolicyRequest) (apitypes.DataPolicy, error)
	UpdateToken(ctx context.Context, a core.Actor, id int64, req apitypes.UpdateTokenRequest) (apitypes.Token, error)
	GetAuditDetail(ctx context.Context, a core.Actor, id int64) (apitypes.AuditDetail, error)
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

// sqlBodyBytes caps request bodies that carry SQL: the SQL limit with room
// for JSON escaping.
const sqlBodyBytes = 2*apitypes.DataSQLMaxBytes + 64<<10

// changesetBodyBytes caps a changeset request: up to 500 rows of values.
const changesetBodyBytes = 16 << 20

// dataErrors are the statuses data operations answer with besides 200:
// 503 when the database isn't running or can't be reached.
var dataErrors = []int{
	http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
	http.StatusConflict, http.StatusServiceUnavailable,
}

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

// created answers with 201, or 200 for a dry run.
type created[T any] struct {
	Status int
	Body   T
}

// badRequest is a validation problem about a request parameter.
func badRequest(location, msg string) error {
	return &apitypes.Error{
		Status: http.StatusBadRequest, Title: http.StatusText(http.StatusBadRequest), Code: apitypes.CodeValidation,
		Detail: msg, Errors: []apitypes.ErrorDetail{{Message: msg, Location: location}},
	}
}

func registerData(s *Server, a huma.API) {
	tags := []string{"data"}
	data := func(op huma.Operation) huma.Operation {
		op.Tags = tags
		op.Errors = dataErrors
		return op
	}

	authed(s, a, data(huma.Operation{
		OperationID: "get-data", Method: http.MethodGet, Path: dataPathT,
		Summary: "Database panel state",
		Description: "Engine, version, status (ready, setup_needed, unsupported, unreachable, not_running) and the caller's data access with the steps that decided it. " +
			"Services that aren't databases are 404.",
	}), func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.DataInfo], error) {
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
	authed(s, a, data(huma.Operation{
		OperationID: "setup-data", Method: http.MethodPost, Path: dataPathT + "/setup",
		Summary:     "Set up data access",
		Description: "Creates (or rotates) Dock's least-privilege roles dock_reader and dock_writer in the database, using the owner's credentials. Writes to the user's database. Admin.",
	}), func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.DataInfo], error) {
		return reply(s.dataAPI().SetupData(ctx, actor, in.ref()))
	})
	authed(s, a, data(huma.Operation{
		OperationID: "get-data-schema", Method: http.MethodGet, Path: dataPathT + "/schema",
		Summary:     "Database schema",
		Description: "Tables, columns, keys, foreign keys, indexes and constraints, from one catalog query cached for 60 s. Level schema.",
	}), func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.DataSchema], error) {
		return reply(s.dataAPI().DataSchema(ctx, actor, in.ref()))
	})
	authed(s, a, data(huma.Operation{
		OperationID: "get-data-rows", Method: http.MethodGet, Path: dataPathT + "/tables/{table}/rows",
		Summary:     "One page of a table",
		Description: "A keyset page: sorted by `order` and the primary key, starting after the `after` cursor. Pass the page's `next` as `after` for the following page. Level read.",
	}), func(ctx context.Context, actor core.Actor, in *struct {
		TablePath
		Order  string `query:"order" doc:"Column to sort by, -column for descending; default the primary key"`
		After  string `query:"after" doc:"The previous page's next cursor"`
		Limit  int    `query:"limit" minimum:"0" maximum:"1000" doc:"Rows per page; 0 means 100"`
		Filter string `query:"filter" doc:"JSON array of DataFilter, ANDed, e.g. [{\"column\":\"status\",\"op\":\"eq\",\"value\":\"paid\"}]"`
		Count  bool   `query:"count" doc:"Also count the matching rows exactly (under the statement timeout)"`
		Full   bool   `query:"full" doc:"Untruncated cells; only with limit 1"`
	}) (*out[apitypes.DataPage], error) {
		req := apitypes.DataPageRequest{Table: in.Table, Order: in.Order, After: in.After, Limit: in.Limit, Count: in.Count, Full: in.Full}
		if in.Filter != "" {
			if err := json.Unmarshal([]byte(in.Filter), &req.Filters); err != nil {
				return nil, badRequest("query.filter", "filter must be a JSON array of {column, op, value}: "+err.Error())
			}
		}
		return reply(s.dataAPI().DataRows(ctx, actor, in.ref(), req))
	})
	authed(s, a, data(huma.Operation{
		OperationID: "get-data-references", Method: http.MethodGet, Path: dataPathT + "/tables/{table}/references",
		Summary:     "Rows referencing a row",
		Description: "For each incoming foreign key, the first rows that reference the row with primary key `key`, and filters that open them. Level read.",
	}), func(ctx context.Context, actor core.Actor, in *struct {
		TablePath
		Key   string `query:"key" required:"true" doc:"The row's primary key as a JSON object, column to value, e.g. {\"id\":\"42\"}"`
		Limit int    `query:"limit" minimum:"0" maximum:"100" doc:"Rows per foreign key; 0 means 10"`
	}) (*out[[]apitypes.DataReferences], error) {
		req := apitypes.DataReferencesRequest{Table: in.Table, Limit: in.Limit}
		if err := json.Unmarshal([]byte(in.Key), &req.Key); err != nil || len(req.Key) == 0 {
			return nil, badRequest("query.key", `key must be a JSON object of primary key columns to values, e.g. {"id":"42"}`)
		}
		return reply(s.dataAPI().DataReferences(ctx, actor, in.ref(), req))
	})
	registerDataQuery(s, a)
	authed(s, a, data(huma.Operation{
		OperationID: "get-data-history", Method: http.MethodGet, Path: dataPathT + "/history",
		Summary:     "Your query history",
		Description: "The caller's own queries on this database (their data.query audit events), newest first. Page back with `before`, the last entry's audit_id.",
	}), func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Before int64 `query:"before" minimum:"0" doc:"Only entries with a smaller audit_id"`
		Limit  int   `query:"limit" default:"50" minimum:"1" maximum:"200"`
	}) (*out[[]apitypes.DataHistoryEntry], error) {
		return reply(s.dataAPI().DataHistory(ctx, actor, in.ref(), in.Before, in.Limit))
	})

	authed(s, a, data(huma.Operation{
		OperationID: "list-saved-queries", Method: http.MethodGet, Path: dataPathT + "/saved-queries",
		Summary: "Your saved queries", Description: "Per user and service.",
	}), func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[[]apitypes.DataSavedQuery], error) {
		return reply(s.dataAPI().ListSavedQueries(ctx, actor, in.ref()))
	})
	authed(s, a, data(huma.Operation{
		OperationID: "create-saved-query", Method: http.MethodPost, Path: dataPathT + "/saved-queries",
		Summary: "Save a query", DefaultStatus: http.StatusCreated, MaxBodyBytes: sqlBodyBytes,
	}), func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.SaveDataQueryRequest
	}) (*out[apitypes.DataSavedQuery], error) {
		return reply(s.dataAPI().SaveDataQuery(ctx, actor, in.ref(), 0, in.Body))
	})
	authed(s, a, data(huma.Operation{
		OperationID: "update-saved-query", Method: http.MethodPut, Path: dataPathT + "/saved-queries/{id}",
		Summary: "Replace a saved query", MaxBodyBytes: sqlBodyBytes,
	}), func(ctx context.Context, actor core.Actor, in *struct {
		SavedQueryPath
		Body apitypes.SaveDataQueryRequest
	}) (*out[apitypes.DataSavedQuery], error) {
		return reply(s.dataAPI().SaveDataQuery(ctx, actor, in.ref(), in.ID, in.Body))
	})
	authed(s, a, data(huma.Operation{
		OperationID: "delete-saved-query", Method: http.MethodDelete, Path: dataPathT + "/saved-queries/{id}",
		Summary: "Delete a saved query", DefaultStatus: http.StatusNoContent,
	}), func(ctx context.Context, actor core.Actor, in *SavedQueryPath) (*struct{}, error) {
		return nil, s.dataAPI().DeleteSavedQuery(ctx, actor, in.ref(), in.ID)
	})

	authed(s, a, data(huma.Operation{
		OperationID: "list-changesets", Method: http.MethodGet, Path: dataPathT + "/changesets",
		Summary: "List changesets", Description: "Newest first. Level schema.",
	}), func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Status apitypes.DataChangesetStatus `query:"status" enum:"pending,committed,rejected,expired,conflict" doc:"Only changesets with this status"`
	}) (*out[[]apitypes.DataChangeset], error) {
		return reply(s.dataAPI().ListDataChangesets(ctx, actor, in.ref(), in.Status))
	})
	authed(s, a, data(huma.Operation{
		OperationID: "create-changeset", Method: http.MethodPost, Path: dataPathT + "/changesets",
		Summary: "Preview or propose a changeset", MaxBodyBytes: changesetBodyBytes,
		Description: "With `dry_run`, validates the changes and renders the SQL without storing anything (200, status preview). Otherwise stores the changeset as pending (201); " +
			"nothing is written until it is committed. Level propose.",
	}), func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.DataChangesetRequest
	}) (*created[apitypes.DataChangeset], error) {
		cs, err := s.dataAPI().CreateDataChangeset(ctx, actor, in.ref(), in.Body)
		if err != nil {
			return nil, err
		}
		status := http.StatusCreated
		if in.Body.DryRun {
			status = http.StatusOK
		}
		return &created[apitypes.DataChangeset]{Status: status, Body: cs}, nil
	})
	authed(s, a, data(huma.Operation{
		OperationID: "get-changeset", Method: http.MethodGet, Path: dataPathT + "/changesets/{id}",
		Summary: "Get a changeset",
	}), func(ctx context.Context, actor core.Actor, in *ChangesetPath) (*out[apitypes.DataChangeset], error) {
		return reply(s.dataAPI().GetDataChangeset(ctx, actor, in.ref(), in.ID))
	})
	authed(s, a, data(huma.Operation{
		OperationID: "commit-changeset", Method: http.MethodPost, Path: dataPathT + "/changesets/{id}/commit",
		Summary: "Commit a changeset",
		Description: "Revalidates the pending changeset against current data and commits it in one transaction. Rows that changed meanwhile fail it as a whole (409, status conflict). " +
			"One's own with level write; anyone's (approving) as admin. Production needs `confirm` set to the environment's name.",
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
	authed(s, a, data(huma.Operation{
		OperationID: "reject-changeset", Method: http.MethodPost, Path: dataPathT + "/changesets/{id}/reject",
		Summary: "Reject a changeset", Description: "Admins, or the author withdrawing it.",
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

	authed(s, a, data(huma.Operation{
		OperationID: "set-sensitive-columns", Method: http.MethodPut, Path: dataPathT + "/sensitive-columns",
		Summary: "Set sensitive columns", Description: "Replaces the database's sensitive columns. Admin.",
	}), func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.SetSensitiveColumnsRequest
	}) (*out[[]apitypes.DataColumnRef], error) {
		return reply(s.dataAPI().SetSensitiveColumns(ctx, actor, in.ref(), in.Body))
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

// ndjsonWriteTimeout bounds each write of a query stream, so a client that
// stops reading can't hold a database connection.
const ndjsonWriteTimeout = 15 * time.Second

const queryDoc = "\n\nThe response is NDJSON (`application/x-ndjson`): one DataQueryFrame per line. A `header` frame (columns), `rows` frames of up to 500 rows, " +
	"then exactly one `end` frame (row count, truncated, duration) or `error` frame (what the database reported, possibly after rows). " +
	"Permission and validation errors are normal problem responses before any frame. Closing the connection cancels the query."

// registerDataQuery registers POST /data/query, which streams NDJSON frames.
func registerDataQuery(s *Server, a huma.API) {
	frame := a.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[apitypes.DataQueryFrame](), true, "")
	op := huma.Operation{
		OperationID: "data-query", Method: http.MethodPost, Path: dataPathT + "/query",
		Summary: "Run a read-only SQL statement", Tags: []string{"data"}, Errors: dataErrors, MaxBodyBytes: sqlBodyBytes,
		Description: "Runs one statement as dock_reader in a READ ONLY transaction; several statements in one string are refused. " +
			"Results are capped by `limit` (default 10 000, at most 100 000) and 64 MB, cells over 8 KB are truncated. Level read; masked callers can't run SQL until masking is enforced in the database." + queryDoc,
		Responses: map[string]*huma.Response{"200": {
			Description: "NDJSON stream of DataQueryFrame, one per line",
			Content:     map[string]*huma.MediaType{"application/x-ndjson": {Schema: frame}},
		}},
	}
	authed(s, a, op, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.DataQueryRequest
	}) (*huma.StreamResponse, error) {
		stream, err := s.dataAPI().DataQuery(ctx, actor, in.ref(), in.Body)
		if err != nil {
			return nil, err
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			s.streamFrames(hctx, stream)
		}}, nil
	})
}

// streamFrames writes a query stream as NDJSON. A stream that fails before
// its first frame answers with a problem response; one that fails later
// ends with an error frame. Client disconnects cancel the stream's context.
func (s *Server) streamFrames(hctx huma.Context, stream core.Stream[apitypes.DataQueryFrame]) {
	w := &ndjsonWriter{hctx: hctx}
	ctx, cancel := context.WithCancel(hctx.Context())
	defer cancel()
	err := stream(ctx, func() { _ = w.write() }, w.write)
	switch {
	case err != nil && !w.started():
		w.problem(s.problem(err))
	case err != nil && ctx.Err() == nil:
		msg := err.Error()
		if ae, ok := errors.AsType[*apitypes.Error](s.problem(err)); ok {
			msg = ae.Detail
		}
		_ = w.write(apitypes.DataQueryFrame{Type: apitypes.FrameError, Error: &apitypes.DataQueryError{Message: msg}})
	default:
		if w.write() == nil {
			w.clearDeadline()
		}
	}
}

// ndjsonWriter writes NDJSON frames. Headers go out with the first write,
// so a stream that fails before it starts can still answer with a problem.
// Each write is one batch of lines and one flush, under a write deadline.
type ndjsonWriter struct {
	mu   sync.Mutex
	hctx huma.Context
	rc   *http.ResponseController
	out  io.Writer // nil until started
	buf  bytes.Buffer
}

func (w *ndjsonWriter) started() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out != nil
}

// write starts the response if needed, then writes frames as lines with
// one flush (just the headers when there are none).
func (w *ndjsonWriter) write(frames ...apitypes.DataQueryFrame) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.out == nil {
		w.hctx.SetHeader("Content-Type", "application/x-ndjson")
		w.hctx.SetHeader("X-Accel-Buffering", "no")
		w.hctx.SetStatus(http.StatusOK)
		w.out = w.hctx.BodyWriter()
		if rw, ok := w.out.(http.ResponseWriter); ok {
			w.rc = http.NewResponseController(rw)
		}
	}
	w.buf.Reset()
	enc := json.NewEncoder(&w.buf)
	for _, f := range frames {
		if err := enc.Encode(f); err != nil { // Encode ends each frame with \n
			return err
		}
	}
	if w.rc != nil {
		if err := w.rc.SetWriteDeadline(time.Now().Add(ndjsonWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
	}
	if w.buf.Len() > 0 {
		if _, err := w.out.Write(w.buf.Bytes()); err != nil {
			return err
		}
	}
	if w.rc != nil {
		if err := w.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
	}
	return nil
}

// clearDeadline lifts the write deadline after a clean end, so it doesn't
// cut off a later response on the same connection.
func (w *ndjsonWriter) clearDeadline() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.rc != nil {
		_ = w.rc.SetWriteDeadline(time.Time{})
	}
}

// problem answers a stream that failed before it started.
func (w *ndjsonWriter) problem(err error) {
	ae, ok := errors.AsType[*apitypes.Error](err)
	if !ok {
		ae = &apitypes.Error{Status: http.StatusInternalServerError, Title: "Internal Server Error", Code: apitypes.CodeInternal, Detail: err.Error()}
	}
	w.hctx.SetHeader("Content-Type", "application/problem+json")
	w.hctx.SetStatus(ae.Status)
	_ = json.NewEncoder(w.hctx.BodyWriter()).Encode(ae)
}
