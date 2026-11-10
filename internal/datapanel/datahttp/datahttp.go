// Package datahttp holds what the data panel's HTTP endpoints share
// between Tempo Dock and tempoBase (ADR 0024): request caps, query
// parameter parsing, the error statuses, the NDJSON stream of a query and
// the operations' summaries and descriptions. Huma can't embed a generic
// path type, so each host registers its own handlers (Dock under
// /services/{s}/data, tempoBase under /connections/{c}/data) and calls
// these.
package datahttp

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
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/datapanel"
)

// SQLBodyBytes caps request bodies that carry SQL: the SQL limit with room
// for JSON escaping.
const SQLBodyBytes = 2*apitypes.DataSQLMaxBytes + 64<<10

// ChangesetBodyBytes caps a changeset request: up to 500 rows of values.
const ChangesetBodyBytes = 16 << 20

// Errors are the statuses data operations answer with besides 200: 503
// when the database isn't running or can't be reached.
var Errors = []int{
	http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
	http.StatusConflict, http.StatusServiceUnavailable,
}

// Created answers with Status: 201 for a stored changeset, 200 for a dry
// run.
type Created[T any] struct {
	Status int
	Body   T
}

// BadRequest is a validation problem about a request parameter at
// location ("query.filter").
func BadRequest(location, msg string) error {
	return &apitypes.Error{
		Status: http.StatusBadRequest, Title: http.StatusText(http.StatusBadRequest), Code: apitypes.CodeValidation,
		Detail: msg, Errors: []apitypes.ErrorDetail{{Message: msg, Location: location}},
	}
}

// ParseFilters reads the rows endpoint's filter parameter: a JSON array of
// apitypes.DataFilter; "" is none.
func ParseFilters(raw string) ([]apitypes.DataFilter, error) {
	if raw == "" {
		return nil, nil
	}
	var out []apitypes.DataFilter
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, BadRequest("query.filter", "filter must be a JSON array of {column, op, value}: "+err.Error())
	}
	return out, nil
}

// ParseKey reads the references endpoint's key parameter: a row's primary
// key as a JSON object of column to value.
func ParseKey(raw string) (map[string]string, error) {
	var key map[string]string
	if err := json.Unmarshal([]byte(raw), &key); err != nil || len(key) == 0 {
		return nil, BadRequest("query.key", `key must be a JSON object of primary key columns to values, e.g. {"id":"42"}`)
	}
	return key, nil
}

// QueryResponses is the query operation's 200 response: NDJSON frames.
func QueryResponses(a huma.API) map[string]*huma.Response {
	frame := a.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[apitypes.DataQueryFrame](), true, "")
	return map[string]*huma.Response{"200": {
		Description: "NDJSON stream of DataQueryFrame, one per line",
		Content:     map[string]*huma.MediaType{"application/x-ndjson": {Schema: frame}},
	}}
}

// Describe returns the summary and description of a data operation by
// its OperationID, in the host's words (w.Product, w.Database,
// w.ConfirmWhat) and with its role names. Both hosts use the same
// OperationIDs; unknown ones get empty strings.
func Describe(id string, roles data.Roles, w datapanel.Words) (summary, description string) {
	switch id {
	case "get-data":
		return "Database panel state", "Engine, version, status (ready, setup_needed, unsupported, unreachable, not_running) and the caller's data access with the steps that decided it."
	case "setup-data":
		return "Set up data access", "Creates (or rotates) " + w.Product + "'s least-privilege roles " + string(roles.Reader) + " and " + string(roles.Writer) +
			" in the database, using the owner's credentials. Writes to the user's database. Admin."
	case "get-data-schema":
		return "Database schema", "Tables, columns, keys, foreign keys, indexes and constraints, from one catalog query cached for 60 s. Level schema."
	case "get-data-rows":
		return "One page of a table", "A keyset page: sorted by `order` and the primary key, starting after the `after` cursor. Pass the page's `next` as `after` for the following page. " +
			"Sensitive columns are masked unless `reveal` is set by a caller with sensitive visible. Level read."
	case "get-data-keys":
		return "One page of keys", "Keys of a key-value database (Redis), found with bounded SCAN steps: `match` is a glob, `type` keeps one type. " +
			"A page may hold fewer keys than `limit` and still have a `next`: pass it as `after`. Level read."
	case "get-data-key":
		return "One key", "A key's type, TTL, length and sampled memory, and one page of its value as grid rows. Pass `next` as `after` for the following page. Level read."
	case "get-data-references":
		return "Rows referencing a row", "For each incoming foreign key, the first rows that reference the row with primary key `key`, and filters that open them. Level read."
	case "data-query":
		return "Run a read-only SQL statement", "Runs one statement as " + string(roles.Reader) + " in a READ ONLY transaction; several statements in one string are refused. " +
			"Results are capped by `limit` (default 10 000, at most 100 000) and 64 MB, cells over 8 KB are truncated. Level read; masked callers' SQL runs as " +
			string(roles.ReaderMasked) + ", which can't select sensitive columns." + queryDoc
	case "get-data-history":
		return "Your query history", "The caller's own queries on this database (their data.query audit events), newest first. Page back with `before`, the last entry's audit_id."
	case "list-saved-queries":
		return "Your saved queries", "Per user and " + w.Database + "."
	case "create-saved-query":
		return "Save a query", ""
	case "update-saved-query":
		return "Replace a saved query", ""
	case "delete-saved-query":
		return "Delete a saved query", ""
	case "list-changesets":
		return "List changesets", "Newest first. Level schema."
	case "create-changeset":
		return "Preview or propose a changeset", "With `dry_run`, validates the changes and renders the SQL without storing anything (200, status preview). Otherwise stores the changeset as pending (201); " +
			"nothing is written until it is committed. Level propose."
	case "get-changeset":
		return "Get a changeset", ""
	case "commit-changeset":
		return "Commit a changeset", "Revalidates the pending changeset against current data and commits it in one transaction. Rows that changed meanwhile fail it as a whole (409, status conflict). " +
			"One's own with level write; anyone's (approving) as admin. Production needs `confirm` set to " + w.ConfirmWhat + "."
	case "reject-changeset":
		return "Reject a changeset", "Admins, or the author withdrawing it."
	case "set-sensitive-columns":
		return "Set sensitive columns", "Replaces the database's sensitive columns. Admin."
	case "set-sensitive-keys":
		return "Set sensitive keys", "Replaces a key-value database's sensitive key patterns (Redis globs, e.g. session:*): matching keys are listed by pattern and read only with an audited reveal. Admin."
	case "get-data-insights":
		return "How the database is doing", "Reads the database's own statistics on demand, as " + string(roles.Monitor) + " (a member of pg_monitor that runs only " + w.Product + "'s fixed queries): " +
			"connections against the limit, cache hit ratio and size (overview); the top statements from pg_stat_statements (queries); sessions with what blocks them (sessions); " +
			"table and index sizes and use (tables, indexes); and hints about what needs a look. Nothing is stored. " +
			"Overview, tables and indexes need level schema; queries and sessions read, with statement values redacted for callers who see sensitive columns masked. " +
			"Views the caller may not see, or the server can't give, are listed in `unavailable`."
	case "cancel-data-session":
		return "Cancel a query or end a session", "Cancels the session's running query, or with `terminate` ends the session (its connection and open transaction), as the owner. " +
			"`query_started_at` (the session's `query_at`) makes sure only that query is cancelled, never a later one; a session that ended or moved on is 409. " +
			"Level write, which tokens never have on production; ending a session on production needs `confirm` set to " + w.ConfirmWhat + ". Audited as data.cancel or data.terminate."
	case "reset-data-query-stats":
		return "Reset query stats", "Sets pg_stat_statements back to zero, as the owner. Admin; audited as data.stats_reset."
	case "enable-data-query-stats":
		return "Turn on query stats", "Creates the pg_stat_statements extension as the owner when the server loads it (state on). " + w.EnableQueryStats
	}
	return "", ""
}

const queryDoc = "\n\nThe response is NDJSON (`application/x-ndjson`): one DataQueryFrame per line. A `header` frame (columns), `rows` frames of up to 500 rows, " +
	"then exactly one `end` frame (row count, truncated, duration) or `error` frame (what the database reported, possibly after rows). " +
	"Permission and validation errors are normal problem responses before any frame. Closing the connection cancels the query."

// NDJSONWriteTimeout bounds each write of a query stream, so a client that
// stops reading can't hold a database connection.
const NDJSONWriteTimeout = 15 * time.Second

// StreamFrames writes a query stream as NDJSON. A stream that fails before
// its first frame answers with a problem response (problem maps the error
// to an *apitypes.Error, as the host's handlers do); one that fails later
// ends with an error frame. Client disconnects cancel the stream's context.
func StreamFrames(hctx huma.Context, stream datapanel.Stream[apitypes.DataQueryFrame], problem func(error) error) {
	w := &ndjsonWriter{hctx: hctx}
	ctx, cancel := context.WithCancel(hctx.Context())
	defer cancel()
	err := stream(ctx, func() { _ = w.write() }, w.write)
	switch {
	case err != nil && !w.started():
		w.problem(problem(err))
	case err != nil && ctx.Err() == nil:
		msg := err.Error()
		if ae, ok := errors.AsType[*apitypes.Error](problem(err)); ok {
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
		if err := w.rc.SetWriteDeadline(time.Now().Add(NDJSONWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
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

// InsightsQuery is the insights endpoint's query parameters; both hosts
// embed it.
type InsightsQuery struct {
	View  []apitypes.DataInsightView  `query:"view" enum:"overview,queries,sessions,tables,indexes" doc:"Views to read, comma separated; default every view you may see"`
	Order apitypes.DataStatementOrder `query:"order" enum:"total,mean,calls,rows" doc:"Order of the queries view: total time (default), mean time, calls or rows"`
	Limit int                         `query:"limit" minimum:"0" maximum:"100" doc:"Statements in the queries view; 0 means 50"`
}

// Request is the parameters as an insights request.
func (q InsightsQuery) Request() apitypes.DataInsightsRequest {
	return apitypes.DataInsightsRequest{Views: q.View, Order: q.Order, Limit: q.Limit}
}

// KeysQuery is get-data-keys' query parameters, shared by both hosts.
type KeysQuery struct {
	DB    int    `query:"db" minimum:"0" maximum:"1024" doc:"Logical database; default 0"`
	Match string `query:"match" doc:"SCAN glob, e.g. user:*; empty matches every key"`
	Type  string `query:"type" enum:",string,hash,list,set,zset,stream" doc:"Keep keys of one type"`
	After string `query:"after" doc:"The previous page's next cursor"`
	Limit int    `query:"limit" minimum:"0" maximum:"500" doc:"Keys per page; 0 means 100"`
}

// Request is the query as a DataKeysRequest.
func (q KeysQuery) Request() apitypes.DataKeysRequest {
	return apitypes.DataKeysRequest{DB: q.DB, Match: q.Match, Type: q.Type, After: q.After, Limit: q.Limit}
}

// KeyQuery is get-data-key's query parameters, shared by both hosts.
type KeyQuery struct {
	DB     int    `query:"db" minimum:"0" maximum:"1024" doc:"Logical database; default 0"`
	Key    string `query:"key" required:"true" doc:"The key's name (hex when hex is set)"`
	Hex    bool   `query:"hex" doc:"key is hex: for names that aren't UTF-8"`
	After  string `query:"after" doc:"The previous page's next cursor"`
	Limit  int    `query:"limit" minimum:"0" maximum:"1000" doc:"Items of the value per page; 0 means 100"`
	Full   bool   `query:"full" doc:"Values untruncated, up to 1 MB each"`
	Reveal bool   `query:"reveal" doc:"Read a key matching a sensitive pattern. Needs sensitive visible (403 otherwise); audited as data.reveal"`
}

// Request is the query as a DataKeyRequest.
func (q KeyQuery) Request() apitypes.DataKeyRequest {
	return apitypes.DataKeyRequest{DB: q.DB, Key: q.Key, Hex: q.Hex, After: q.After, Limit: q.Limit, Full: q.Full, Reveal: q.Reveal}
}
