package mcpkit

import (
	"context"
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
)

// Data tools (ADR 0016, 0024): the database panel for agents, the same in
// Dock and tempoBase. The host's operations check the data access policy
// on every call and audit queries, browsing and changesets; these tools
// add tighter caps, since results go into a model's context.

const (
	// defaultQueryRows and maxQueryRows cap data_query results.
	defaultQueryRows = 100
	maxQueryRows     = 1000
	// maxResultBytes caps the rows of one data_query or data_rows result;
	// rows past it are dropped and the result is marked truncated.
	maxResultBytes = 512 << 10
	// defaultPageRows and maxPageRows cap data_rows pages.
	defaultPageRows = 50
	maxPageRows     = 200
)

// queryResult is a data_query result.
type queryResult struct {
	Columns   []apitypes.DataResultColumn `json:"columns"`
	Rows      []apitypes.DataRow          `json:"rows"`
	Truncated bool                        `json:"truncated,omitempty"`
	Command   string                      `json:"command,omitempty"`
	// Error is what the database reported, possibly after some rows.
	Error *apitypes.DataQueryError `json:"error,omitempty"`
}

// tableView is a table as data_schema lists it: columns as "name type",
// with markers for the primary key, nullability and sensitive columns.
type tableView struct {
	Table         string   `json:"table"`
	Kind          string   `json:"kind,omitempty"`
	EstimatedRows int64    `json:"estimated_rows"`
	Columns       []string `json:"columns"`
	PrimaryKey    []string `json:"primary_key,omitempty"`
	// References are foreign keys as "col -> schema.table(col)".
	References []string `json:"references,omitempty"`
	Editable   bool     `json:"editable"`
}

func viewTable(t apitypes.DataTable) tableView {
	v := tableView{Table: t.Schema + "." + t.Name, EstimatedRows: t.EstimatedRows, PrimaryKey: t.PrimaryKey, Editable: t.Editable}
	if t.Kind != apitypes.TableKindTable {
		v.Kind = string(t.Kind)
	}
	for _, c := range t.Columns {
		s := c.Name + " " + c.Type
		if !c.Nullable {
			s += " not null"
		}
		if c.Sensitive {
			s += " sensitive"
		}
		v.Columns = append(v.Columns, s)
	}
	for _, fk := range t.ForeignKeys {
		v.References = append(v.References, fmt.Sprintf("%s -> %s.%s(%s)",
			strings.Join(fk.Columns, ","), fk.RefSchema, fk.RefTable, strings.Join(fk.RefColumns, ",")))
	}
	return v
}

// capRows drops rows past maxResultBytes and reports whether it did.
func capRows(rows []apitypes.DataRow) ([]apitypes.DataRow, bool) {
	size := 0
	for i, r := range rows {
		size += len(r) + 1
		if size > maxResultBytes {
			return rows[:i], true
		}
	}
	return rows, false
}

// DataTools registers the data tools (data_status, data_schema,
// data_rows, data_query, data_insights, data_cancel, propose_data_changes) over a host's panel
// operations. L is the host's locator input (Dock's service, tempoBase's
// connection), whose properties every data tool takes; locate resolves it
// to the host's database reference for the caller, and name says how
// messages name that database.
func DataTools[A, R, L any](s *Server[A], ops datapanel.Ops[A, R], locate func(*Call[A], L) (R, error), name func(R) string) {
	addLocatedTool(s, "data_status", "Database status",
		"A database service's state and your data access level (none, schema, read, propose, write) and why; the other data tools say the same when they refuse.",
		ReadOnly, func(ctx context.Context, c *Call[A], loc L, _ struct{}) (Reply, error) {
			ref, err := locate(c, loc)
			if err != nil {
				return Reply{}, err
			}
			info, err := ops.DataInfo(ctx, c.Actor, ref)
			if err != nil {
				return Reply{}, err
			}
			why := make([]string, len(info.Access.Why))
			for i, w := range info.Access.Why {
				why[i] = w.Detail
			}
			summary := fmt.Sprintf("%s: %s %s, your access %s (sensitive columns %s)", name(ref), info.Engine, info.Status, info.Access.Level, info.Access.Sensitive)
			if info.Reason != "" {
				summary += "; " + info.Reason
			}
			if len(why) > 0 {
				summary += ". Why: " + strings.Join(why, "; ")
			}
			return Reply{Summary: summary + ".", Data: info}, nil
		})

	addLocatedTool(s, "data_schema", "Database schema",
		"A database's tables, columns and keys; with table, one table's indexes and constraints.",
		ReadOnly, func(ctx context.Context, c *Call[A], loc L, in struct {
			Table string `json:"table,omitempty"`
		}) (Reply, error) {
			ref, err := locate(c, loc)
			if err != nil {
				return Reply{}, err
			}
			sch, err := ops.DataSchema(ctx, c.Actor, ref)
			if err != nil {
				return Reply{}, err
			}
			if in.Table != "" {
				table := in.Table
				if !strings.Contains(table, ".") {
					table = "public." + table
				}
				for _, t := range sch.Tables {
					if t.Schema+"."+t.Name == table {
						return Reply{Summary: fmt.Sprintf("Table %s: %d columns, about %d rows.", table, len(t.Columns), t.EstimatedRows), Data: t}, nil
					}
				}
				return Reply{}, datapanel.Errorf(apitypes.CodeNotFound, "table %s not found in %s; data_schema without table lists them", table, name(ref))
			}
			out := make([]tableView, len(sch.Tables))
			for i, t := range sch.Tables {
				out[i] = viewTable(t)
			}
			return Reply{Summary: fmt.Sprintf("%d tables in %s.", len(out), name(ref)), Data: out}, nil
		})

	addLocatedTool(s, "data_rows", "Table rows",
		"One page of a table's rows, filtered and ordered. Sensitive columns masked.",
		ReadOnly, func(ctx context.Context, c *Call[A], loc L, in struct {
			Table   string                `json:"table" doc:"e.g. public.orders."`
			Filters []apitypes.DataFilter `json:"filters,omitempty" doc:"ANDed."`
			Order   string                `json:"order,omitempty" doc:"column or -column. Default: the primary key."`
			After   string                `json:"after,omitempty" doc:"The previous page's next."`
			Limit   int                   `json:"limit,omitempty" doc:"Default 50, at most 200."`
		}) (Reply, error) {
			ref, err := locate(c, loc)
			if err != nil {
				return Reply{}, err
			}
			n, err := Limit("limit", in.Limit, defaultPageRows, maxPageRows)
			if err != nil {
				return Reply{}, err
			}
			page, err := ops.DataRows(ctx, c.Actor, ref, apitypes.DataPageRequest{Table: in.Table, Filters: in.Filters, Order: in.Order, After: in.After, Limit: n})
			if err != nil {
				return Reply{}, err
			}
			var cut bool
			if page.Rows, cut = capRows(page.Rows); cut {
				page.Next = "" // the cursor would skip the dropped rows
			}
			summary := fmt.Sprintf("%d rows of %s (about %d in the table)", len(page.Rows), in.Table, page.EstimatedRows)
			switch {
			case cut:
				summary += "; cut at 512 KB, ask for fewer rows"
			case page.Next != "":
				summary += "; more with after"
			}
			return Reply{Summary: summary + ".", Data: page}, nil
		})

	addLocatedTool(s, "data_query", "Run read-only SQL",
		"Run one read-only SQL statement, or on Redis one read command. Writes go through propose_data_changes.",
		ReadOnly, func(ctx context.Context, c *Call[A], loc L, in struct {
			SQL            string `json:"sql"`
			Limit          int    `json:"limit,omitempty" doc:"Default 100, at most 1000."`
			TimeoutSeconds int    `json:"timeout_seconds,omitempty" doc:"Default 10, at most 60."`
			DB             int    `json:"db,omitempty" doc:"Redis database. Default 0."`
		}) (Reply, error) {
			ref, err := locate(c, loc)
			if err != nil {
				return Reply{}, err
			}
			n, err := Limit("limit", in.Limit, defaultQueryRows, maxQueryRows)
			if err != nil {
				return Reply{}, err
			}
			stream, err := ops.DataQuery(ctx, c.Actor, ref, apitypes.DataQueryRequest{SQL: in.SQL, Limit: n, TimeoutSeconds: in.TimeoutSeconds, DB: in.DB})
			if err != nil {
				return Reply{}, err
			}
			res := queryResult{Rows: []apitypes.DataRow{}}
			size := 0
			err = stream(ctx, func() {}, func(fs ...apitypes.DataQueryFrame) error {
				for _, f := range fs {
					switch f.Type {
					case apitypes.FrameHeader:
						res.Columns = f.Columns
					case apitypes.FrameRows:
						for _, r := range f.Rows {
							if size += len(r) + 1; size > maxResultBytes {
								res.Truncated = true
								break
							}
							res.Rows = append(res.Rows, r)
						}
					case apitypes.FrameEnd:
						res.Truncated = res.Truncated || f.Truncated
						res.Command = f.Command
					case apitypes.FrameError:
						res.Error = f.Error
					}
				}
				return nil
			})
			if err != nil {
				return Reply{}, err
			}
			if res.Error != nil {
				return Reply{}, Invalid("the database reported: %s", queryError(res.Error))
			}
			summary := fmt.Sprintf("%d rows", len(res.Rows))
			if res.Truncated {
				summary += " (truncated: more rows than the limit or 512 KB)"
			}
			return Reply{Summary: summary + ".", Data: res}, nil
		})

	addInsightsTool(s, ops, locate, name)
	addCancelTool(s, ops, locate, name)

	addLocatedTool(s, "propose_data_changes", "Propose data changes",
		"Change rows (or Redis keys) as one changeset: pending for an admin's approval; commit applies it at once with write access outside production. Updates carry the old values read.",
		Destructive, func(ctx context.Context, c *Call[A], loc L, in struct {
			Changes    []apitypes.DataChange    `json:"changes,omitempty" doc:"key: primary key values (update, delete); old: the values you read (update); new: values to set. Values are text, or null."`
			KeyChanges []apitypes.DataKeyChange `json:"key_changes,omitempty" doc:"Redis instead of changes; old is {type, value} as read (all but pushes)."`
			Message    string                   `json:"message"`
			DryRun     bool                     `json:"dry_run,omitempty" doc:"Only return the SQL."`
			Commit     bool                     `json:"commit,omitempty"`
		}) (Reply, error) {
			ref, err := locate(c, loc)
			if err != nil {
				return Reply{}, err
			}
			if strings.TrimSpace(in.Message) == "" {
				return Reply{}, Invalid("pass message: why these changes are needed, for the reviewer")
			}
			cs, err := ops.CreateDataChangeset(ctx, c.Actor, ref, apitypes.DataChangesetRequest{Changes: in.Changes, KeyChanges: in.KeyChanges, Message: in.Message, DryRun: in.DryRun})
			if err != nil {
				return Reply{}, err
			}
			switch {
			case in.DryRun:
				return Reply{Summary: fmt.Sprintf("Preview of %d changes; nothing stored.", len(cs.Changes)+len(cs.KeyChanges)), Data: cs}, nil
			case !in.Commit:
				return Reply{Summary: fmt.Sprintf("Changeset %d proposed: pending until an admin approves it.", cs.ID), Data: cs}, nil
			case cs.ConfirmCommit:
				return Reply{Summary: fmt.Sprintf("Changeset %d proposed, not committed: %s is production, so an admin must approve it.", cs.ID, name(ref)), Data: cs}, nil
			}
			done, err := ops.CommitDataChangeset(ctx, c.Actor, ref, cs.ID, apitypes.DataCommitRequest{})
			if err == nil {
				return Reply{Summary: fmt.Sprintf("Changeset %d committed: %v rows affected.", done.ID, done.RowsAffected), Data: done}, nil
			}
			// The changeset exists either way; say what happened to it.
			if cur, gerr := ops.GetDataChangeset(ctx, c.Actor, ref, cs.ID); gerr == nil {
				cs = cur
			}
			code := datapanel.CodeOf(err)
			if code == apitypes.CodeInternal {
				return Reply{}, err
			}
			return Reply{Summary: fmt.Sprintf("Changeset %d is %s; the commit failed: %s: %v", cs.ID, cs.Status, code, err), Data: map[string]any{
				"changeset": cs, "commit_error": map[string]any{"code": code, "message": err.Error()},
			}}, nil
		})
}

// queryError renders a database error with its detail and hint.
func queryError(e *apitypes.DataQueryError) string {
	s := e.Message
	if e.Detail != "" {
		s += " (" + e.Detail + ")"
	}
	if e.Hint != "" {
		s += " Hint: " + e.Hint
	}
	if e.SQLState != "" {
		s += " [" + e.SQLState + "]"
	}
	return s
}

// DataKeyTools registers the key-value tools (data_keys, data_key) for a
// host that has a key-value engine (Dock's Redis, ADR 0072); arguments
// as DataTools'.
func DataKeyTools[A, R, L any](s *Server[A], ops datapanel.Ops[A, R], locate func(*Call[A], L) (R, error)) {
	addLocatedTool(s, "data_keys", "Redis keys",
		"One page of a Redis database's keys (SCAN, match glob).",
		ReadOnly, func(ctx context.Context, c *Call[A], loc L, in struct {
			DB    int    `json:"db,omitempty" doc:"Default 0."`
			Match string `json:"match,omitempty" doc:"Glob, e.g. user:*."`
			Type  string `json:"type,omitempty" enum:",string,hash,list,set,zset,stream"`
			After string `json:"after,omitempty" doc:"The previous page's next."`
			Limit int    `json:"limit,omitempty" doc:"Default 50, at most 200."`
		}) (Reply, error) {
			ref, err := locate(c, loc)
			if err != nil {
				return Reply{}, err
			}
			n, err := Limit("limit", in.Limit, defaultPageRows, maxPageRows)
			if err != nil {
				return Reply{}, err
			}
			page, err := ops.DataKeys(ctx, c.Actor, ref, apitypes.DataKeysRequest{DB: in.DB, Match: in.Match, Type: in.Type, After: in.After, Limit: n})
			if err != nil {
				return Reply{}, err
			}
			summary := fmt.Sprintf("%d keys (%d in db %d)", len(page.Keys), page.Total, in.DB)
			if page.Next != "" {
				summary += "; more with after"
			}
			return Reply{Summary: summary + ".", Data: page}, nil
		})

	addLocatedTool(s, "data_key", "Redis key",
		"One Redis key's type, TTL and a page of its value.",
		ReadOnly, func(ctx context.Context, c *Call[A], loc L, in struct {
			DB    int    `json:"db,omitempty" doc:"Default 0."`
			Key   string `json:"key" doc:"Hex when hex is set."`
			Hex   bool   `json:"hex,omitempty"`
			After string `json:"after,omitempty" doc:"The previous page's next."`
			Limit int    `json:"limit,omitempty" doc:"Default 50, at most 200."`
		}) (Reply, error) {
			ref, err := locate(c, loc)
			if err != nil {
				return Reply{}, err
			}
			n, err := Limit("limit", in.Limit, defaultPageRows, maxPageRows)
			if err != nil {
				return Reply{}, err
			}
			v, err := ops.DataKey(ctx, c.Actor, ref, apitypes.DataKeyRequest{DB: in.DB, Key: in.Key, Hex: in.Hex, After: in.After, Limit: n})
			if err != nil {
				return Reply{}, err
			}
			var cut bool
			if v.Rows, cut = capRows(v.Rows); cut {
				v.Next = ""
			}
			summary := fmt.Sprintf("%s: a %s of length %d, %d rows", in.Key, v.Type, v.Length, len(v.Rows))
			switch {
			case cut:
				summary += "; cut at 512 KB, ask for fewer"
			case v.Next != "":
				summary += "; more with after"
			}
			return Reply{Summary: summary + ".", Data: v}, nil
		})
}
