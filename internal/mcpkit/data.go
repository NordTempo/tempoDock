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
// data_rows, data_query, data_insights, propose_data_changes) over a host's panel
// operations. L is the host's locator input (Dock's service, tempoBase's
// connection), whose properties every data tool takes; locate resolves it
// to the host's database reference for the caller, and name says how
// messages name that database.
func DataTools[A, R, L any](s *Server[A], ops datapanel.Ops[A, R], locate func(*Call[A], L) (R, error), name func(R) string) {
	addLocatedTool(s, "data_status", "Database status",
		"A database service's state (ready, setup_needed, not_running, ...) and your data access: level (none, schema, read, propose, write), whether sensitive columns are masked for you, and the policy steps that decided it. "+
			"Call it before other data tools. Access comes from the data access policy an admin sets; if it is too low, report that instead of working around it.",
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
		"A database's tables with columns (name, type, not null, sensitive), primary keys and foreign keys. Pass table for one table with its indexes and constraints. "+
			"Needs data access level schema.",
		ReadOnly, func(ctx context.Context, c *Call[A], loc L, in struct {
			Table string `json:"table,omitempty" doc:"One table as schema.name (or name for public), with its indexes and constraints."`
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
		"One page of a table, in primary key order (or order), filtered by column conditions. Sensitive columns are masked. Pass the result's next as after for the following page. "+
			"Needs data access level read; audited.",
		ReadOnly, func(ctx context.Context, c *Call[A], loc L, in struct {
			Table   string                `json:"table" doc:"Table as schema.name, e.g. public.orders."`
			Filters []apitypes.DataFilter `json:"filters,omitempty" doc:"Conditions, ANDed: {column, op, value} with op eq, ne, lt, lte, gt, gte, like, ilike, in (values), is_null, not_null."`
			Order   string                `json:"order,omitempty" doc:"Column to sort by, -column for descending. Default: the primary key."`
			After   string                `json:"after,omitempty" doc:"The previous page's next cursor."`
			Limit   int                   `json:"limit,omitempty" doc:"Rows per page. Default 50, at most 200."`
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
		"Run one read-only SQL statement (SELECT, EXPLAIN, ...) in a read-only transaction as a least-privilege role; writes are impossible here, use propose_data_changes. "+
			"Rows come as JSON arrays in column order. Cells over 8 KB are cut. Needs data access level read; every statement is audited with its SQL.",
		ReadOnly, func(ctx context.Context, c *Call[A], loc L, in struct {
			SQL            string `json:"sql" doc:"One SQL statement."`
			Limit          int    `json:"limit,omitempty" doc:"Row cap. Default 100, at most 1000. Rows past it are dropped and the result is marked truncated."`
			TimeoutSeconds int    `json:"timeout_seconds,omitempty" doc:"Statement timeout. Default 10, at most 60."`
		}) (Reply, error) {
			ref, err := locate(c, loc)
			if err != nil {
				return Reply{}, err
			}
			n, err := Limit("limit", in.Limit, defaultQueryRows, maxQueryRows)
			if err != nil {
				return Reply{}, err
			}
			stream, err := ops.DataQuery(ctx, c.Actor, ref, apitypes.DataQueryRequest{SQL: in.SQL, Limit: n, TimeoutSeconds: in.TimeoutSeconds})
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

	addLocatedTool(s, "propose_data_changes", "Propose data changes",
		"Change rows: inserts, updates and deletes by primary key, as one changeset applied in one transaction. dry_run returns the exact SQL without storing anything. "+
			"Otherwise the changeset is stored as pending for an admin to review and approve (needs level propose); with commit it is also committed right away, only if your level is write and the environment is not production. "+
			"Tokens never write production: there it always waits for approval. Updates must carry the old values you saw; a row changed since fails the whole changeset. At most 500 rows.",
		Destructive, func(ctx context.Context, c *Call[A], loc L, in struct {
			Changes []apitypes.DataChange `json:"changes" doc:"Row changes: {op: insert|update|delete, table: schema.name, key: primary key values (update, delete), old: the values you read (update), new: values to set or insert}. Values are strings in Postgres text format, or null."`
			Message string                `json:"message" doc:"Why, for the reviewer."`
			DryRun  bool                  `json:"dry_run,omitempty" doc:"Only render the SQL; store nothing."`
			Commit  bool                  `json:"commit,omitempty" doc:"Commit at once if your access is write on a non-production environment; otherwise it stays pending."`
		}) (Reply, error) {
			ref, err := locate(c, loc)
			if err != nil {
				return Reply{}, err
			}
			if strings.TrimSpace(in.Message) == "" {
				return Reply{}, Invalid("pass message: why these changes are needed, for the reviewer")
			}
			cs, err := ops.CreateDataChangeset(ctx, c.Actor, ref, apitypes.DataChangesetRequest{Changes: in.Changes, Message: in.Message, DryRun: in.DryRun})
			if err != nil {
				return Reply{}, err
			}
			switch {
			case in.DryRun:
				return Reply{Summary: fmt.Sprintf("Preview of %d changes; nothing stored.", len(cs.Changes)), Data: cs}, nil
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
