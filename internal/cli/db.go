package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/apiclient"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// `dock db`: the database panel (ADR 0016) from the terminal. Every
// command acts on the linked service (or -p/-e/-s) and calls the same API
// as the web UI; the server checks the caller's data access.

func newDBCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "db",
		Short: "Browse, query and change a database service's data",
		Long: `Work with the data in a database service (Postgres, MySQL, MariaDB, Redis or
Valkey): tables or keys, schema, rows,
read-only SQL, query history, saved queries and changesets, and how the
database is doing (health, queries, sessions, storage).

What you may do is the data access policy's decision (dock db status says
what you have and why). Queries run as Dock's read-only role; changes go
through changesets that are committed in one transaction, and production
commits need --confirm <environment>.`,
	})
	cmd.AddCommand(
		newDBStatusCmd(g), newDBSetupCmd(g), newDBSettingsCmd(g),
		newDBTablesCmd(g), newDBSchemaCmd(g), newDBRowsCmd(g), newDBKeysCmd(g), newDBKeyCmd(g), newDBQueryCmd(g), newDBHistoryCmd(g),
		newDBSavedCmd(g), newDBChangesCmd(g), newDBApplyCmd(g),
		newDBHealthCmd(g), newDBQueriesCmd(g), newDBSessionsCmd(g), newDBStorageCmd(g), newDBCancelCmd(g),
		newDBPolicyCmd(g), newDBSensitiveCmd(g),
	)
	return cmd
}

// dbCmd is a `dock db` subcommand acting on one service: it adds the
// target flags and runs fn with a session, the target and its data path.
// Errors from the data endpoints get hints that point at what to do.
func dbCmd(g *globals, cmd *cobra.Command, fn func(ctx context.Context, s *session, t target, data string, args []string) error) *cobra.Command {
	var f targetFlags
	f.add(cmd, needService)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		s, t, err := g.open(f, needService)
		if err != nil {
			return err
		}
		return dataHint(fn(cmd.Context(), s, t, t.servicePath()+"/data", args))
	}
	return cmd
}

// dataHint replaces the generic hints of permission and availability
// errors with data-specific ones.
func dataHint(err error) error {
	e, ok := errors.AsType[*output.Error](err)
	p, isProblem := errors.AsType[*apiclient.Problem](err)
	if !ok || !isProblem || p.Code == "" {
		return err
	}
	switch p.Status {
	case http.StatusForbidden:
		e.Hint = "`dock db status` shows your data access and why; an admin changes it with `dock db policy set` or on your token."
	case http.StatusServiceUnavailable:
		e.Hint = "The database must be running and reachable: check `dock status` and `dock db status`."
	}
	return err
}

func newDBStatusCmd(g *globals) *cobra.Command {
	return dbCmd(g, &cobra.Command{
		Use:   "status",
		Short: "Engine, version, setup state, and your access and why",
		Args:  cobra.NoArgs,
	}, func(ctx context.Context, s *session, t target, data string, _ []string) error {
		info, err := get[apitypes.DataInfo](ctx, s, data)
		if err != nil {
			return err
		}
		return g.io.Print(info, func() error {
			setUp := ""
			if info.SetUpAt != nil {
				setUp = ago(*info.SetUpAt) + " by " + info.SetUpBy
			}
			if err := g.io.Fields(
				"service", t.String(),
				"engine", engineLabel(info),
				"status", string(info.Status),
				"reason", info.Reason,
				"set up", setUp,
				"used by", strings.Join(info.UsedBy, ", "),
				"access", accessSummary(info.Access, t),
			); err != nil {
				return err
			}
			for _, step := range info.Access.Why {
				_, _ = fmt.Fprintln(g.io.Out, "  "+output.GlyphPending+" "+step.Detail)
			}
			return nil
		})
	})
}

// accessSummary is one line about a DataPermission, e.g. "read, sensitive
// masked".
func accessSummary(p apitypes.DataPermission, t target) string {
	parts := []string{string(p.Level), "sensitive " + string(p.Sensitive)}
	if p.Approve {
		parts = append(parts, "approves changesets")
	}
	if p.Manage {
		parts = append(parts, "manages access")
	}
	if p.ConfirmCommit {
		parts = append(parts, "commits need --confirm "+t.Environment)
	}
	return strings.Join(parts, ", ")
}

func newDBSetupCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := dbCmd(g, &cobra.Command{
		Use:   "setup",
		Short: "Create or rotate Dock's database roles (admin)",
		Long: `Create Dock's least-privilege roles (dock_reader, dock_writer) in the
database with the owner's credentials, or rotate their passwords. Writes to
the database, so it asks first; --yes skips the question.`,
		Args: cobra.NoArgs,
	}, func(ctx context.Context, s *session, t target, data string, _ []string) error {
		if !yes {
			if err := g.io.Confirm(fmt.Sprintf("Create or rotate roles dock_reader and dock_writer in %s?", t), "yes"); err != nil {
				return err
			}
		}
		info, err := send[apitypes.DataInfo](ctx, s, http.MethodPost, data+"/setup", nil)
		if err != nil {
			return err
		}
		return g.io.Print(info, func() error {
			g.io.Successf("Data access is set up for %s (%s)", t, info.Status)
			return nil
		})
	})
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

func newDBSettingsCmd(g *globals) *cobra.Command {
	var database, urlVariable string
	var cmd *cobra.Command
	cmd = dbCmd(g, &cobra.Command{
		Use:   "settings [--database postgres|mysql|redis|none|detect] [--url-variable NAME]",
		Short: "Mark a service as a database, or name its connection URL variable",
		Long: `Change Dock-side database settings. They apply immediately and never create
a release. --database overrides detection from the image (detect goes back to
it); --url-variable names the variable with the connection URL, such as
DATABASE_URL or api.DATABASE_URL (empty: the service's DATABASE_URL, then
POSTGRES_*).`,
		Args: cobra.NoArgs,
	}, func(ctx context.Context, s *session, t target, _ string, _ []string) error {
		var req apitypes.UpdateDatabaseSettingsRequest
		if cmd.Flags().Changed("database") {
			d := apitypes.DatabaseSetting(database)
			switch database {
			case "detect":
				d = apitypes.DatabaseDetect
			case "postgres", "mysql", "redis", "none":
			default:
				return usageError(fmt.Sprintf("invalid --database %q", database), "Use postgres, mysql, redis, none or detect.")
			}
			req.Database = &d
		}
		if cmd.Flags().Changed("url-variable") {
			req.DatabaseURLVariable = &urlVariable
		}
		if req.Database == nil && req.DatabaseURLVariable == nil {
			return usageError("nothing to change", "Pass --database or --url-variable.")
		}
		svc, err := send[apitypes.Service](ctx, s, http.MethodPut, t.servicePath()+"/database-settings", req)
		if err != nil {
			return err
		}
		return g.io.Print(svc, func() error {
			g.io.Successf("%s: database %s, URL variable %s, detected engine %s", t,
				dash(string(svc.Database)), dash(svc.DatabaseURLVariable), dash(string(svc.DataEngine)))
			return nil
		})
	})
	cmd.Flags().StringVar(&database, "database", "", "postgres, mysql (MariaDB too), redis (Valkey too), none, or detect (from the image)")
	cmd.Flags().StringVar(&urlVariable, "url-variable", "", "variable with the connection URL, e.g. DATABASE_URL or api.DATABASE_URL")
	return cmd
}

// schemaOf reads the database's schema.
func schemaOf(ctx context.Context, s *session, data string) (apitypes.DataSchema, error) {
	return get[apitypes.DataSchema](ctx, s, data+"/schema")
}

// engineLabel is "mysql 8.4.13", or the server's version alone when it
// names the server itself ("Redis 8.10.2", "MariaDB 11.4.5").
func engineLabel(info apitypes.DataInfo) string {
	v := info.ServerVersion
	if v != "" && (v[0] < '0' || v[0] > '9') {
		return v
	}
	return strings.TrimSpace(string(info.Engine) + " " + v)
}

func newDBTablesCmd(g *globals) *cobra.Command {
	return dbCmd(g, &cobra.Command{
		Use:   "tables",
		Short: "List tables and views",
		Args:  cobra.NoArgs,
	}, func(ctx context.Context, s *session, _ target, data string, _ []string) error {
		sc, err := schemaOf(ctx, s, data)
		if err != nil {
			return err
		}
		return g.io.Print(sc.Tables, func() error {
			t := g.io.Table("table", "kind", "rows", "size", "key")
			for _, tb := range sc.Tables {
				t.Row(tb.Schema+"."+tb.Name, string(tb.Kind), tableRows(tb), bytesize(tb.SizeBytes), dash(strings.Join(tb.PrimaryKey, ", ")))
			}
			return t.Flush()
		})
	})
}

// estimate renders Postgres' row estimate: "~12431", or "?" when the
// table was never analysed.
func estimate(n int64) string {
	if n < 0 {
		return "?"
	}
	return "~" + itoa(n)
}

// tableRows renders a table's row count: exact when the server counted a
// never-analysed table, else the estimate.
func tableRows(tb apitypes.DataTable) string {
	if tb.ExactRows != nil {
		return itoa(*tb.ExactRows)
	}
	return estimate(tb.EstimatedRows)
}

func newDBSchemaCmd(g *globals) *cobra.Command {
	return dbCmd(g, &cobra.Command{
		Use:     "schema [table]",
		Aliases: []string{"describe"},
		Short:   "Columns, keys, indexes and constraints of one table, or of all",
		Long:    "Describe a table (schema.name, or a name in any schema), or every table without one.",
		Args:    cobra.MaximumNArgs(1),
	}, func(ctx context.Context, s *session, _ target, data string, args []string) error {
		sc, err := schemaOf(ctx, s, data)
		if err != nil {
			return err
		}
		if len(args) == 0 {
			return g.io.Print(sc, func() error {
				for i, tb := range sc.Tables {
					if i > 0 {
						_, _ = fmt.Fprintln(g.io.Out)
					}
					if err := describeTable(g.io, tb); err != nil {
						return err
					}
				}
				return nil
			})
		}
		tb, err := findTable(sc, args[0])
		if err != nil {
			return err
		}
		return g.io.Print(tb, func() error { return describeTable(g.io, tb) })
	})
}

// findTable finds "schema.name", or a bare name (public first, then any
// schema).
func findTable(sc apitypes.DataSchema, name string) (apitypes.DataTable, error) {
	schema, table, qualified := strings.Cut(name, ".")
	var found []apitypes.DataTable
	for _, tb := range sc.Tables {
		switch {
		case qualified && tb.Schema == schema && tb.Name == table:
			return tb, nil
		case !qualified && tb.Name == name:
			if tb.Schema == "public" {
				return tb, nil
			}
			found = append(found, tb)
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	if len(found) > 1 {
		return apitypes.DataTable{}, usageError(fmt.Sprintf("table %q is in several schemas", name), "Name it as schema.table.")
	}
	return apitypes.DataTable{}, &output.Error{Exit: output.ExitNotFound, Message: fmt.Sprintf("no table %q", name), Hint: "Tables are listed by `dock db tables`."}
}

// describeTable prints one table's structure.
func describeTable(o *output.IO, tb apitypes.DataTable) error {
	_, _ = fmt.Fprintf(o.Out, "%s.%s  %s  %s rows  %s\n", tb.Schema, tb.Name, tb.Kind, tableRows(tb), bytesize(tb.SizeBytes))
	if tb.Comment != "" {
		_, _ = fmt.Fprintln(o.Out, tb.Comment)
	}
	refs := map[string]string{}
	for _, fk := range tb.ForeignKeys {
		if len(fk.Columns) == 1 {
			refs[fk.Columns[0]] = "→ " + fk.RefSchema + "." + fk.RefTable + "." + fk.RefColumns[0]
		}
	}
	t := o.Table("column", "type", "null", "default", "notes")
	for _, c := range tb.Columns {
		var notes []string
		if slices.Contains(tb.PrimaryKey, c.Name) {
			notes = append(notes, "key")
		}
		if c.Generated {
			notes = append(notes, "generated")
		}
		if c.Sensitive {
			notes = append(notes, "sensitive")
		}
		if r := refs[c.Name]; r != "" {
			notes = append(notes, r)
		}
		null := "no"
		if c.Nullable {
			null = "yes"
		}
		t.Row(c.Name, c.Type, null, dash(c.Default), strings.Join(notes, " "))
	}
	if err := t.Flush(); err != nil {
		return err
	}
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		_, _ = fmt.Fprintln(o.Out, title)
		for _, l := range lines {
			_, _ = fmt.Fprintln(o.Out, "  "+l)
		}
	}
	var fks, idx, cons []string
	for _, fk := range tb.ForeignKeys {
		fks = append(fks, fmt.Sprintf("%s (%s) → %s.%s (%s)", fk.Name, strings.Join(fk.Columns, ", "), fk.RefSchema, fk.RefTable, strings.Join(fk.RefColumns, ", ")))
	}
	for _, ix := range tb.Indexes {
		idx = append(idx, ix.Name+"  "+ix.Definition)
	}
	for _, c := range tb.Constraints {
		cons = append(cons, c.Name+"  "+c.Definition)
	}
	section("foreign keys", fks)
	section("indexes", idx)
	section("constraints", cons)
	if !tb.Editable {
		_, _ = fmt.Fprintln(o.Out, "read-only: changes need a table with a primary key")
	}
	return nil
}

// filterOps are the operators a --filter can name, by their API name.
var filterOps = []apitypes.DataFilterOp{
	apitypes.FilterEq, apitypes.FilterNe, apitypes.FilterLt, apitypes.FilterLte, apitypes.FilterGt, apitypes.FilterGte,
	apitypes.FilterLike, apitypes.FilterILike, apitypes.FilterIn, apitypes.FilterIsNull, apitypes.FilterNotNull,
}

// parseFilter reads a --filter: "col=value" (equals), "col=op:value",
// "col=in:a,b,c", or "col=is_null" / "col=not_null".
func parseFilter(s string) (apitypes.DataFilter, error) {
	col, rhs, ok := strings.Cut(s, "=")
	if !ok || col == "" {
		return apitypes.DataFilter{}, usageError(fmt.Sprintf("invalid --filter %q", s), "Use column=value, column=op:value (eq, ne, lt, lte, gt, gte, like, ilike, in), or column=is_null / not_null.")
	}
	f := apitypes.DataFilter{Column: col, Op: apitypes.FilterEq, Value: rhs}
	if op := apitypes.DataFilterOp(rhs); op == apitypes.FilterIsNull || op == apitypes.FilterNotNull {
		return apitypes.DataFilter{Column: col, Op: op}, nil
	}
	if name, value, ok := strings.Cut(rhs, ":"); ok && slices.Contains(filterOps, apitypes.DataFilterOp(name)) {
		f.Op, f.Value = apitypes.DataFilterOp(name), value
		if f.Op == apitypes.FilterIn {
			f.Value, f.Values = "", strings.Split(value, ",")
		}
	}
	return f, nil
}

func newDBRowsCmd(g *globals) *cobra.Command {
	var filters []string
	var order, after string
	var limit int
	var count, reveal bool
	cmd := dbCmd(g, &cobra.Command{
		Use:   "rows <table> [--filter col=op:value ...] [--order col|-col] [--limit N] [--after CURSOR] [--reveal]",
		Short: "One page of a table's rows; prints the next cursor",
		Long: `Print one keyset page of a table, sorted by --order (a column, -column for
descending) and the primary key. Pass the printed cursor as --after for the
next page. Filters are ANDed: column=value, column=op:value with op one of
eq, ne, lt, lte, gt, gte, like, ilike, in (comma-separated values), or
column=is_null / column=not_null.

Sensitive columns show as (masked). If your access lets you see them,
--reveal shows them; each reveal is audited (data.reveal).

With --json, the page as the API returns it, including next.`,
		Example: `  dock db rows orders --filter status=paid --filter total=gt:100 --order -created_at
  dock db rows public.orders --after eyJ... --limit 500`,
		Args: cobra.ExactArgs(1),
	}, func(ctx context.Context, s *session, _ target, data string, args []string) error {
		q := url.Values{}
		if len(filters) > 0 {
			fs := make([]apitypes.DataFilter, 0, len(filters))
			for _, raw := range filters {
				f, err := parseFilter(raw)
				if err != nil {
					return err
				}
				fs = append(fs, f)
			}
			b, _ := json.Marshal(fs)
			q.Set("filter", string(b))
		}
		for k, v := range map[string]string{"order": order, "after": after} {
			if v != "" {
				q.Set(k, v)
			}
		}
		if limit > 0 {
			q.Set("limit", strconv.Itoa(limit))
		}
		if count {
			q.Set("count", "true")
		}
		if reveal {
			q.Set("reveal", "true")
		}
		table, err := qualify(ctx, s, data, args[0])
		if err != nil {
			return err
		}
		page, err := get[apitypes.DataPage](ctx, s, data+"/tables/"+url.PathEscape(table)+"/rows?"+q.Encode())
		if err != nil {
			return err
		}
		return g.io.Print(page, func() error {
			names := make([]string, len(page.Columns))
			for i, c := range page.Columns {
				names[i] = c.Name
			}
			t := g.io.Table(names...)
			for _, r := range page.Rows {
				cells, err := rowCells(r, false)
				if err != nil {
					return err
				}
				for i := range cells {
					cells[i] = cut(cells[i], maxCellWidth)
				}
				t.Row(cells...)
			}
			if err := t.Flush(); err != nil {
				return err
			}
			total := estimate(page.EstimatedRows)
			if page.ExactRows != nil {
				total = itoa(*page.ExactRows)
			}
			g.io.Progressf("%s of %s", plural(len(page.Rows), "row"), total)
			if page.Next != "" {
				g.io.Progressf("next page: --after %s", page.Next)
			}
			return nil
		})
	})
	cmd.Flags().StringArrayVarP(&filters, "filter", "w", nil, "column=value or column=op:value; repeatable")
	cmd.Flags().StringVar(&order, "order", "", "sort column, -column for descending (default: the primary key)")
	cmd.Flags().StringVar(&after, "after", "", "cursor of the previous page")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, fmt.Sprintf("rows per page (default %d, at most %d)", apitypes.DataPageLimit, apitypes.DataPageMaxLimit))
	cmd.Flags().BoolVar(&count, "count", false, "count matching rows exactly")
	cmd.Flags().BoolVar(&reveal, "reveal", false, "show sensitive columns (needs sensitive visible; audited)")
	return cmd
}

// qualify names a table as schema.table: as given when qualified, else the
// table of that name in the database's schema (findTable: Postgres's public
// first), so a bare name works on MySQL, whose schemas are databases.
func qualify(ctx context.Context, s *session, data, name string) (string, error) {
	if strings.Contains(name, ".") {
		return name, nil
	}
	sc, err := schemaOf(ctx, s, data)
	if err != nil {
		return "", err
	}
	tb, err := findTable(sc, name)
	if err != nil {
		return "", err
	}
	return tb.Schema + "." + tb.Name, nil
}

// queryFlags are the options of commands that run SQL.
type queryFlags struct {
	file    string
	limit   int
	timeout time.Duration
	csv     bool
	// db is the logical database Redis commands run in.
	db int
}

func (f *queryFlags) add(cmd *cobra.Command, withFile bool) {
	if withFile {
		cmd.Flags().StringVarP(&f.file, "file", "f", "", "read the SQL from a file (- for stdin)")
	}
	cmd.Flags().IntVarP(&f.limit, "limit", "n", 1000, fmt.Sprintf("most rows to return (at most %d)", apitypes.DataQueryMaxLimit))
	cmd.Flags().DurationVar(&f.timeout, "timeout", 0, fmt.Sprintf("statement timeout (default %s, at most %s)", apitypes.DataQueryTimeout, apitypes.DataQueryMaxTimeout))
	cmd.Flags().BoolVar(&f.csv, "csv", false, "print CSV instead of a table")
	cmd.Flags().IntVar(&f.db, "db", 0, "logical database (Redis)")
}

// request builds the query request for sql.
func (f queryFlags) request(sql string) (apitypes.DataQueryRequest, error) {
	if f.limit < 1 || f.limit > apitypes.DataQueryMaxLimit {
		return apitypes.DataQueryRequest{}, usageError(fmt.Sprintf("invalid --limit %d", f.limit), fmt.Sprintf("Use 1 to %d.", apitypes.DataQueryMaxLimit))
	}
	if f.timeout < 0 || f.timeout > apitypes.DataQueryMaxTimeout {
		return apitypes.DataQueryRequest{}, usageError(fmt.Sprintf("invalid --timeout %s", f.timeout), fmt.Sprintf("Use at most %s.", apitypes.DataQueryMaxTimeout))
	}
	return apitypes.DataQueryRequest{SQL: sql, Limit: f.limit, TimeoutSeconds: int(math.Ceil(f.timeout.Seconds())), DB: f.db}, nil
}

// readSQL takes SQL from the argument, --file, or stdin ("-", or no
// argument with stdin not a terminal).
func (g *globals) readSQL(args []string, file string) (string, error) {
	var b []byte
	var err error
	switch {
	case file != "" && len(args) > 0:
		return "", usageError("pass the SQL as an argument or with --file, not both", "")
	case file == "-" || (file == "" && len(args) > 0 && args[0] == "-") || (file == "" && len(args) == 0 && !g.io.Interactive):
		b, err = io.ReadAll(io.LimitReader(g.io.In, apitypes.DataSQLMaxBytes+1))
	case file != "":
		b, err = os.ReadFile(file)
	case len(args) > 0:
		b = []byte(args[0])
	}
	if err != nil {
		return "", &output.Error{Exit: output.ExitUsage, Err: err, Hint: "Pass the SQL as an argument, with --file, or on stdin."}
	}
	sql := strings.TrimSpace(string(b))
	switch {
	case sql == "":
		return "", usageError("no SQL given", "Pass the SQL as an argument, with --file, or on stdin.")
	case len(sql) > apitypes.DataSQLMaxBytes:
		return "", usageError("the SQL is too long", fmt.Sprintf("At most %s.", bytesize(apitypes.DataSQLMaxBytes)))
	}
	return sql, nil
}

func newDBQueryCmd(g *globals) *cobra.Command {
	var f queryFlags
	cmd := dbCmd(g, &cobra.Command{
		Use:   `query "<sql>" | -f FILE | -`,
		Short: "Run one read-only SQL statement, streamed",
		Long: `Run one SQL statement as Dock's read-only role and print the result as it
arrives: an aligned table (wide cells are cut; use --csv or --json for full
values), CSV with --csv, or with --json one JSON frame per line (header,
rows, then end or error), as the API streams them.

Results stop at --limit rows (default 1 000) or 64 MB; the summary on stderr
says when. Exit 2 for SQL the database refuses as invalid, 3 for writes
(queries are read-only; change data with ` + "`dock db apply`" + `).`,
		Example: `  dock db query "select id, total from orders order by total desc limit 10"
  dock db query -f report.sql --csv > report.csv`,
		Args: cobra.MaximumNArgs(1),
	}, func(ctx context.Context, s *session, _ target, data string, args []string) error {
		sql, err := g.readSQL(args, f.file)
		if err != nil {
			return err
		}
		req, err := f.request(sql)
		if err != nil {
			return err
		}
		return g.runQuery(ctx, s, data, req, f.csv)
	})
	f.add(cmd, true)
	return cmd
}

// runQuery streams a query's NDJSON frames to stdout.
func (g *globals) runQuery(ctx context.Context, s *session, data string, req apitypes.DataQueryRequest, asCSV bool) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	hr, err := s.api.NewRequest(ctx, http.MethodPost, data+"/query", bytes.NewReader(body))
	if err != nil {
		return err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/x-ndjson, application/problem+json")
	resp, err := s.api.Do(hr)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	p := &queryPrinter{io: g.io, out: bufio.NewWriterSize(g.io.Out, 64<<10)}
	if asCSV && !g.io.JSON {
		p.csv = csv.NewWriter(p.out)
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			_ = p.out.Flush()
			if ctx.Err() != nil {
				return &output.Error{Exit: output.ExitError, Message: "query canceled", Err: ctx.Err()}
			}
			return &output.Error{Exit: output.ExitUnreachable, Message: "the query's result stream ended early", Err: err,
				Hint: "The connection to the server was lost; run the query again."}
		}
		var f apitypes.DataQueryFrame
		if err := json.Unmarshal(raw, &f); err != nil {
			return err
		}
		if done, err := p.frame(raw, f); done || err != nil {
			return err
		}
	}
}

// maxCellWidth is the widest a cell is printed in human tables.
const maxCellWidth = 40

// queryPrinter writes query frames as they arrive: raw frames with --json,
// CSV with --csv, else a table whose column widths come from the first
// rows frame (later, wider cells are cut to fit).
type queryPrinter struct {
	io     *output.IO
	out    *bufio.Writer
	csv    *csv.Writer
	cols   []string
	widths []int
	rows   int64
}

// frame handles one frame; done is set after the last one.
func (p *queryPrinter) frame(raw json.RawMessage, f apitypes.DataQueryFrame) (done bool, err error) {
	if p.io.JSON {
		_, _ = p.out.Write(raw)
		_ = p.out.WriteByte('\n')
	}
	switch f.Type {
	case apitypes.FrameHeader:
		p.cols = make([]string, len(f.Columns))
		for i, c := range f.Columns {
			p.cols[i] = c.Name
		}
		if p.csv != nil {
			_ = p.csv.Write(p.cols)
		}
	case apitypes.FrameRows:
		if err := p.writeRows(f.Rows); err != nil {
			return true, err
		}
	case apitypes.FrameEnd:
		if p.widths == nil && p.csv == nil && !p.io.JSON {
			p.writeTable(nil) // the header of an empty result
		}
		if err := p.flush(); err != nil {
			return true, err
		}
		g := p.io
		if f.Command != "" && f.Command != "SELECT "+itoa(f.RowCount) {
			g.Progressf("%s, %s in %d ms", f.Command, plural(int(f.RowCount), "row"), f.DurationMS)
		} else {
			g.Progressf("%s in %d ms", plural(int(f.RowCount), "row"), f.DurationMS)
		}
		if f.Truncated {
			g.Warnf("The result was cut at %s (--limit, or the 64 MB cap); raise --limit or narrow the query.", plural(int(f.RowCount), "row"))
		}
		return true, nil
	case apitypes.FrameError:
		if err := p.flush(); err != nil {
			return true, err
		}
		return true, queryError(f.Error)
	}
	return false, p.flush()
}

// flush writes out what is buffered, once per frame.
func (p *queryPrinter) flush() error {
	if p.csv != nil {
		p.csv.Flush()
		if err := p.csv.Error(); err != nil {
			return err
		}
	}
	return p.out.Flush()
}

func (p *queryPrinter) writeRows(rows []apitypes.DataRow) error {
	if p.io.JSON {
		return nil
	}
	cells := make([][]string, 0, len(rows))
	for _, r := range rows {
		c, err := rowCells(r, p.csv != nil)
		if err != nil {
			return err
		}
		cells = append(cells, c)
	}
	p.rows += int64(len(rows))
	if p.csv != nil {
		return p.csv.WriteAll(cells)
	}
	p.writeTable(cells)
	return nil
}

// writeTable prints rows aligned, fixing the column widths on first use.
func (p *queryPrinter) writeTable(rows [][]string) {
	if p.widths == nil {
		p.widths = make([]int, len(p.cols))
		for i, c := range p.cols {
			p.widths[i] = min(utf8.RuneCountInString(c), maxCellWidth)
		}
		for _, r := range rows {
			for i, c := range r {
				if i < len(p.widths) {
					p.widths[i] = max(p.widths[i], min(utf8.RuneCountInString(oneLine(c)), maxCellWidth))
				}
			}
		}
		upper := make([]string, len(p.cols))
		for i, c := range p.cols {
			upper[i] = strings.ToUpper(c)
		}
		p.writeLine(upper)
	}
	for _, r := range rows {
		p.writeLine(r)
	}
}

func (p *queryPrinter) writeLine(cells []string) {
	for i, c := range cells {
		if i >= len(p.widths) {
			break
		}
		c = cut(oneLine(c), p.widths[i])
		_, _ = p.out.WriteString(c)
		if i < len(cells)-1 {
			_, _ = p.out.WriteString(strings.Repeat(" ", p.widths[i]-utf8.RuneCountInString(c)+2))
		}
	}
	_ = p.out.WriteByte('\n')
}

var oneLineReplacer = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ")

func oneLine(s string) string { return oneLineReplacer.Replace(s) }

// cut shortens s to at most n runes, ending in "…" when it was longer.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// rowCells renders one result row. For CSV, NULL is an empty field;
// otherwise it reads NULL.
func rowCells(row apitypes.DataRow, forCSV bool) ([]string, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(row, &raw); err != nil {
		return nil, fmt.Errorf("unexpected row %s: %w", row, err)
	}
	cells := make([]string, len(raw))
	for i, c := range raw {
		cells[i] = cellText(c, forCSV)
	}
	return cells, nil
}

// cellText renders one cell (see apitypes.DataRow): strings unquoted,
// numbers and booleans as sent, truncated values with "…", masked values
// as "(masked)".
func cellText(c json.RawMessage, forCSV bool) string {
	switch {
	case len(c) == 0 || string(c) == "null":
		if forCSV {
			return ""
		}
		return "NULL"
	case c[0] == '"':
		var s string
		_ = json.Unmarshal(c, &s)
		return s
	case c[0] == '{':
		var v struct {
			Truncated *string `json:"truncated"`
			Masked    bool    `json:"masked"`
		}
		_ = json.Unmarshal(c, &v)
		switch {
		case v.Masked:
			return "(masked)"
		case v.Truncated != nil:
			return *v.Truncated + "…"
		}
	}
	return string(c)
}

// queryError turns an error frame into an exit-coded error: SQL the
// database refuses (class 42) is a usage error, a write in the read-only
// transaction or a missing privilege is a permission error.
func queryError(e *apitypes.DataQueryError) error {
	if e == nil {
		e = &apitypes.DataQueryError{Message: "the query failed"}
	}
	msg := e.Message
	if e.SQLState != "" {
		msg += " (SQLSTATE " + e.SQLState + ")"
	}
	if e.Position > 0 {
		msg += fmt.Sprintf(" at character %d", e.Position)
	}
	hint := strings.TrimSpace(strings.Join([]string{e.Detail, e.Hint}, "\n"))
	exit := output.ExitError
	switch {
	case e.SQLState == "25006" || e.SQLState == "42501":
		exit = output.ExitAuth
		if hint == "" {
			hint = "Queries are read-only; change data with `dock db apply`."
		}
	case strings.HasPrefix(e.SQLState, "42"):
		exit = output.ExitUsage
	case e.SQLState == "57014":
		if hint == "" {
			hint = fmt.Sprintf("The statement timed out or was canceled; raise --timeout (at most %s).", apitypes.DataQueryMaxTimeout)
		}
	}
	return &output.Error{Exit: exit, Code: "query_failed", Message: msg, Hint: hint}
}

func newDBHistoryCmd(g *globals) *cobra.Command {
	var before int64
	var limit int
	cmd := dbCmd(g, &cobra.Command{
		Use:   "history",
		Short: "Your past queries on this database, newest first",
		Args:  cobra.NoArgs,
	}, func(ctx context.Context, s *session, _ target, data string, _ []string) error {
		q := url.Values{"limit": {strconv.Itoa(limit)}}
		if before > 0 {
			q.Set("before", itoa(before))
		}
		entries, err := get[[]apitypes.DataHistoryEntry](ctx, s, data+"/history?"+q.Encode())
		if err != nil {
			return err
		}
		return g.io.Print(entries, func() error {
			t := g.io.Table("id", "time", "outcome", "rows", "ms", "sql")
			for _, e := range entries {
				t.Row(itoa(e.AuditID), e.CreatedAt.Local().Format("2006-01-02 15:04:05"), string(e.Outcome), itoa(e.Rows), itoa(e.DurationMS), cut(oneLine(e.SQL), 80))
			}
			return t.Flush()
		})
	})
	cmd.Flags().Int64Var(&before, "before", 0, "only queries older than this id (paging)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "how many (at most 200)")
	return cmd
}

func newDBSavedCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "saved", Short: "Your saved queries on this database"})
	list := dbCmd(g, &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List saved queries",
		Args:    cobra.NoArgs,
	}, func(ctx context.Context, s *session, _ target, data string, _ []string) error {
		saved, err := get[[]apitypes.DataSavedQuery](ctx, s, data+"/saved-queries")
		if err != nil {
			return err
		}
		return g.io.Print(saved, func() error {
			t := g.io.Table("id", "name", "updated", "sql")
			for _, q := range saved {
				t.Row(itoa(q.ID), q.Name, ago(q.UpdatedAt), cut(oneLine(q.SQL), 60))
			}
			return t.Flush()
		})
	})

	var saveFile string
	save := dbCmd(g, &cobra.Command{
		Use:   `save <name> "<sql>" | -f FILE | -`,
		Short: "Save a query under a name, replacing one with the same name",
		Args:  cobra.RangeArgs(1, 2),
	}, func(ctx context.Context, s *session, _ target, data string, args []string) error {
		sql, err := g.readSQL(args[1:], saveFile)
		if err != nil {
			return err
		}
		saved, err := get[[]apitypes.DataSavedQuery](ctx, s, data+"/saved-queries")
		if err != nil {
			return err
		}
		req := apitypes.SaveDataQueryRequest{Name: args[0], SQL: sql}
		var q apitypes.DataSavedQuery
		if i := slices.IndexFunc(saved, func(q apitypes.DataSavedQuery) bool { return q.Name == args[0] }); i >= 0 {
			q, err = send[apitypes.DataSavedQuery](ctx, s, http.MethodPut, data+"/saved-queries/"+itoa(saved[i].ID), req)
		} else {
			q, err = send[apitypes.DataSavedQuery](ctx, s, http.MethodPost, data+"/saved-queries", req)
		}
		if err != nil {
			return err
		}
		return g.io.Print(q, func() error {
			g.io.Successf("Saved query %s (id %d)", q.Name, q.ID)
			return nil
		})
	})
	save.Flags().StringVarP(&saveFile, "file", "f", "", "read the SQL from a file (- for stdin)")

	var runFlags queryFlags
	run := dbCmd(g, &cobra.Command{
		Use:   "run <name|id>",
		Short: "Run a saved query (like `dock db query`)",
		Args:  cobra.ExactArgs(1),
	}, func(ctx context.Context, s *session, _ target, data string, args []string) error {
		q, err := findSaved(ctx, s, data, args[0])
		if err != nil {
			return err
		}
		req, err := runFlags.request(q.SQL)
		if err != nil {
			return err
		}
		return g.runQuery(ctx, s, data, req, runFlags.csv)
	})
	runFlags.add(run, false)

	remove := dbCmd(g, &cobra.Command{
		Use:     "delete <name|id>",
		Aliases: []string{"rm"},
		Short:   "Delete a saved query",
		Args:    cobra.ExactArgs(1),
	}, func(ctx context.Context, s *session, _ target, data string, args []string) error {
		q, err := findSaved(ctx, s, data, args[0])
		if err != nil {
			return err
		}
		if err := del(ctx, s, data+"/saved-queries/"+itoa(q.ID)); err != nil {
			return err
		}
		v := struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Deleted bool   `json:"deleted"`
		}{q.ID, q.Name, true}
		return g.io.Print(v, func() error {
			g.io.Successf("Deleted saved query %s", q.Name)
			return nil
		})
	})
	cmd.AddCommand(list, save, run, remove)
	return cmd
}

// findSaved finds one of the caller's saved queries by name or id.
func findSaved(ctx context.Context, s *session, data, which string) (apitypes.DataSavedQuery, error) {
	saved, err := get[[]apitypes.DataSavedQuery](ctx, s, data+"/saved-queries")
	if err != nil {
		return apitypes.DataSavedQuery{}, err
	}
	for _, q := range saved {
		if q.Name == which || itoa(q.ID) == which {
			return q, nil
		}
	}
	return apitypes.DataSavedQuery{}, &output.Error{Exit: output.ExitNotFound, Message: fmt.Sprintf("no saved query %q", which), Hint: "Saved queries are listed by `dock db saved list`."}
}

func newDBChangesCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "changes",
		Short: "Changesets: list, show, commit (approve) or reject",
		Long: `Changesets are row changes committed in one transaction. Proposals (from
callers with level propose) wait as pending until an admin commits or
rejects them; pending changesets expire after 7 days.`,
	})
	var status string
	list := dbCmd(g, &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List changesets, newest first",
		Args:    cobra.NoArgs,
	}, func(ctx context.Context, s *session, _ target, data string, _ []string) error {
		path := data + "/changesets"
		if status != "" {
			path += "?status=" + url.QueryEscape(status)
		}
		list, err := get[[]apitypes.DataChangeset](ctx, s, path)
		if err != nil {
			return err
		}
		return g.io.Print(list, func() error {
			t := g.io.Table("id", "status", "author", "changes", "created", "message")
			for _, cs := range list {
				t.Row(itoa(cs.ID), string(cs.Status), cs.Author, strconv.Itoa(len(cs.Changes)+len(cs.KeyChanges)), ago(cs.CreatedAt), cut(oneLine(cs.Message), 60))
			}
			return t.Flush()
		})
	})
	list.Flags().StringVar(&status, "status", "", "only pending, committed, rejected, expired or conflict")

	show := dbCmd(g, &cobra.Command{
		Use:   "show <id>",
		Short: "A changeset's changes and the SQL it runs",
		Args:  cobra.ExactArgs(1),
	}, func(ctx context.Context, s *session, _ target, data string, args []string) error {
		id, err := changesetID(args[0])
		if err != nil {
			return err
		}
		cs, err := get[apitypes.DataChangeset](ctx, s, data+"/changesets/"+itoa(id))
		if err != nil {
			return err
		}
		return g.io.Print(cs, func() error { return printChangeset(g.io, cs) })
	})

	var confirm string
	commit := dbCmd(g, &cobra.Command{
		Use:   "commit <id> [--confirm ENV]",
		Short: "Commit a pending changeset (approving it if it isn't yours)",
		Long: `Revalidate a pending changeset against the current data and commit it in
one transaction. If a row changed since it was proposed, nothing is written
and the conflicts are listed (exit 5). On production, --confirm must name
the environment (asked for on a terminal).`,
		Args: cobra.ExactArgs(1),
	}, func(ctx context.Context, s *session, t target, data string, args []string) error {
		id, err := changesetID(args[0])
		if err != nil {
			return err
		}
		cs, err := get[apitypes.DataChangeset](ctx, s, data+"/changesets/"+itoa(id))
		if err != nil {
			return err
		}
		return g.commitChangeset(ctx, s, t, data, cs, confirm)
	})
	commit.Flags().StringVar(&confirm, "confirm", "", "the environment's name, required on production")

	var reason string
	reject := dbCmd(g, &cobra.Command{
		Use:   "reject <id> [--reason TEXT]",
		Short: "Reject a pending changeset (admins, or its author withdrawing it)",
		Args:  cobra.ExactArgs(1),
	}, func(ctx context.Context, s *session, _ target, data string, args []string) error {
		id, err := changesetID(args[0])
		if err != nil {
			return err
		}
		cs, err := send[apitypes.DataChangeset](ctx, s, http.MethodPost, data+"/changesets/"+itoa(id)+"/reject", apitypes.DataRejectRequest{Reason: reason})
		if err != nil {
			return err
		}
		return g.io.Print(cs, func() error {
			g.io.Successf("Rejected changeset %d", cs.ID)
			return nil
		})
	})
	reject.Flags().StringVar(&reason, "reason", "", "a note for the author")

	cmd.AddCommand(list, show, commit, reject)
	return cmd
}

func changesetID(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, usageError(fmt.Sprintf("%q is not a changeset id", s), "Changeset ids are shown by `dock db changes list`.")
	}
	return id, nil
}

// printChangeset prints a changeset's state, changes and SQL.
func printChangeset(o *output.IO, cs apitypes.DataChangeset) error {
	decided := ""
	if cs.DecidedAt != nil {
		decided = ago(*cs.DecidedAt) + " by " + cs.DecidedBy
	}
	id := itoa(cs.ID)
	if cs.ID == 0 {
		id = "preview"
	}
	if err := o.Fields(
		"changeset", id,
		"status", string(cs.Status),
		"author", cs.Author,
		"message", cs.Message,
		"changes", strconv.Itoa(len(cs.Changes)+len(cs.KeyChanges)),
		"decided", decided,
		"reason", cs.Reason,
	); err != nil {
		return err
	}
	for _, c := range cs.Conflicts {
		_, _ = fmt.Fprintf(o.Out, "  %s change %d %s: %s\n", output.GlyphFail, c.Change+1, c.Reason, c.Message)
	}
	if cs.SQL != "" {
		_, _ = fmt.Fprintln(o.Out)
		_, _ = fmt.Fprintln(o.Out, strings.TrimRight(cs.SQL, "\n"))
	}
	return nil
}

// commitChangeset commits cs, asking for the environment's name on
// production when --confirm wasn't given.
func (g *globals) commitChangeset(ctx context.Context, s *session, t target, data string, cs apitypes.DataChangeset, confirm string) error {
	if cs.ConfirmCommit && confirm == "" {
		var err error
		if confirm, err = g.io.Prompt(fmt.Sprintf("This commits to %s. Type the environment's name to confirm", t.Environment), "confirm"); err != nil {
			return err
		}
	}
	done, err := send[apitypes.DataChangeset](ctx, s, http.MethodPost, data+"/changesets/"+itoa(cs.ID)+"/commit", apitypes.DataCommitRequest{Confirm: confirm})
	if err != nil {
		return err
	}
	if err := g.io.Print(done, func() error {
		if done.Status == apitypes.ChangesetCommitted {
			var rows int64
			for _, n := range done.RowsAffected {
				rows += n
			}
			g.io.Successf("Committed changeset %d: %s affected", done.ID, plural(int(rows), "row"))
			return nil
		}
		return printChangeset(g.io, done)
	}); err != nil {
		return err
	}
	if done.Status == apitypes.ChangesetConflict {
		return &output.Error{Exit: output.ExitConflict, Code: "conflict",
			Message: fmt.Sprintf("changeset %d conflicts with the current data; nothing was written", done.ID),
			Hint:    "Look at the rows again and propose new changes."}
	}
	return nil
}

func newDBApplyCmd(g *globals) *cobra.Command {
	var file, message, confirm string
	var dryRun, propose bool
	cmd := dbCmd(g, &cobra.Command{
		Use:     "apply -f changes.json [--dry-run | --propose] [--confirm ENV]",
		Aliases: []string{"propose"},
		Short:   "Change rows from a JSON changeset: preview, propose or commit",
		Long: `Read changes from a JSON file (- for stdin): either {"changes": [...],
"message": "..."} or a bare array of changes. Each change is
{"op": "insert"|"update"|"delete", "table": "schema.name", "key": {...},
"old": {...}, "new": {...}} with values as strings in the database's text
format (MySQL binary values as \x hex), or null. An update's "old" holds the values you saw; if the row changed
since, nothing is written (exit 5).

On Redis the file holds {"key_changes": [...]}: each {"op": "set"|"delete"|
"expire"|"hset"|"hdel"|"sadd"|"srem"|"zadd"|"zrem"|"lset"|"rpush"|"lpush"|
"lrem", "db": N, "key": "...", "field": "...", "value": "...", "ttl_ms": N,
"old": {"type": "...", "value": "..."}}; "old" is the key's type and the
value you saw (every op but the pushes).

--dry-run shows the SQL without storing anything. Otherwise the changeset is
stored and committed in one transaction when your access allows writing, or
left pending for an admin when it allows proposing (or with --propose).
Production commits need --confirm <environment>.`,
		Example: `  dock db apply -f changes.json --dry-run
  dock db apply -f changes.json -m "fix order 1041 total" --confirm production`,
		Args: cobra.NoArgs,
	}, func(ctx context.Context, s *session, t target, data string, _ []string) error {
		req, err := g.readChanges(file)
		if err != nil {
			return err
		}
		if message != "" {
			req.Message = message
		}
		if dryRun {
			req.DryRun = true
			cs, err := send[apitypes.DataChangeset](ctx, s, http.MethodPost, data+"/changesets", req)
			if err != nil {
				return err
			}
			return g.io.Print(cs, func() error { return printChangeset(g.io, cs) })
		}
		info, err := get[apitypes.DataInfo](ctx, s, data)
		if err != nil {
			return err
		}
		if !info.Access.Level.Includes(apitypes.LevelPropose) {
			return &output.Error{Exit: output.ExitAuth, Code: string(apitypes.CodeForbidden),
				Message: fmt.Sprintf("changing data needs level propose; you have %s on %s", info.Access.Level, t),
				Hint:    "`dock db status` shows why; an admin changes it with `dock db policy set` or on your token."}
		}
		commit := !propose && info.Access.Level.Includes(apitypes.LevelWrite)
		if commit && info.Access.ConfirmCommit && confirm == "" {
			// Ask before anything is stored, so a refusal leaves nothing pending.
			if confirm, err = g.io.Prompt(fmt.Sprintf("This commits to %s. Type the environment's name to confirm", t.Environment), "confirm"); err != nil {
				return err
			}
		}
		cs, err := send[apitypes.DataChangeset](ctx, s, http.MethodPost, data+"/changesets", req)
		if err != nil {
			return err
		}
		if !commit {
			return g.io.Print(cs, func() error {
				g.io.Successf("Proposed changeset %d (%s)", cs.ID, plural(len(cs.Changes)+len(cs.KeyChanges), "change"))
				g.io.Progressf("An admin commits it with `dock db changes commit %d`.", cs.ID)
				return nil
			})
		}
		if err := g.commitChangeset(ctx, s, t, data, cs, confirm); err != nil {
			if e, ok := errors.AsType[*output.Error](err); ok && e.Exit != output.ExitConflict {
				e.Hint = fmt.Sprintf("Changeset %d stays pending: commit it later with `dock db changes commit %d`, or withdraw it with `dock db changes reject %d`.", cs.ID, cs.ID, cs.ID)
			}
			return err
		}
		return nil
	})
	cmd.Flags().StringVarP(&file, "file", "f", "", "JSON file with the changes (- for stdin)")
	cmd.Flags().StringVarP(&message, "message", "m", "", "why, for reviewers")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the SQL without storing anything")
	cmd.Flags().BoolVar(&propose, "propose", false, "only propose: leave the changeset pending for an admin")
	cmd.Flags().StringVar(&confirm, "confirm", "", "the environment's name, required to commit on production")
	return cmd
}

// readChanges reads a changeset request (or a bare array of changes) from
// file.
func (g *globals) readChanges(file string) (apitypes.DataChangesetRequest, error) {
	var req apitypes.DataChangesetRequest
	if file == "" {
		return req, output.MissingFlag("file")
	}
	var b []byte
	var err error
	if file == "-" {
		b, err = io.ReadAll(g.io.In)
	} else {
		b, err = os.ReadFile(file)
	}
	if err != nil {
		return req, &output.Error{Exit: output.ExitUsage, Err: err, Hint: "Pass a JSON file with --file, or - for stdin."}
	}
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '[' {
		err = json.Unmarshal(b, &req.Changes)
	} else {
		err = json.Unmarshal(b, &req)
	}
	if err != nil {
		return req, usageError("invalid changes JSON: "+err.Error(), "See `dock db apply --help` for the format.")
	}
	if len(req.Changes) == 0 && len(req.KeyChanges) == 0 {
		return req, usageError("no changes in "+file, "See `dock db apply --help` for the format.")
	}
	return req, nil
}

// policyPath is the data policy's path at the scope the flags name: the
// default without --project.
func policyPath(project, env string) (string, string, error) {
	switch {
	case project == "" && env != "":
		return "", "", usageError("--environment needs --project", "")
	case env != "":
		t := target{Project: project, Environment: env}
		return t.envPath() + "/data-policy", "environment " + t.String(), nil
	case project != "":
		t := target{Project: project}
		return t.projectPath() + "/data-policy", "project " + project, nil
	}
	return apiPrefix + "/data-policy", "default policy", nil
}

func newDBPolicyCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "policy",
		Short: "Show or change the data access policy",
		Long: `The data access policy decides, per subject (users with role read, users
with role deploy, tokens) and environment kind, a level (none < schema <
read < propose < write) and whether sensitive columns are masked or visible.
It is set as a default with project and environment overrides; the most
specific rule wins. Admins can always do everything.

--project and --environment pick the scope (the default without them); the
directory's link is not used.`,
	})
	var project, env string
	scopeFlags := func(c *cobra.Command) {
		c.Flags().StringVarP(&project, "project", "p", "", "a project's policy")
		c.Flags().StringVarP(&env, "environment", "e", "", "an environment's policy (with --project)")
	}
	show := &cobra.Command{
		Use:   "show [-p PROJECT [-e ENV]]",
		Short: "The rules at one scope and the effective rule for each subject and kind",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, title, err := policyPath(project, env)
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			p, err := get[apitypes.DataPolicy](cmd.Context(), s, path)
			if err != nil {
				return err
			}
			return g.io.Print(p, func() error { return printPolicy(g.io, title, p) })
		},
	}
	scopeFlags(show)

	var replace bool
	set := &cobra.Command{
		Use:   "set SUBJECT.KIND=LEVEL[,SENSITIVE]... [-p PROJECT [-e ENV]] [--replace]",
		Short: "Change rules at one scope (admin)",
		Long: `Set rules at one scope. SUBJECT is read, deploy or tokens (users with that
role, or tokens without their own access); KIND is production or
non-production; LEVEL is none, schema, read, propose or write, or inherit to
remove the rule; SENSITIVE is masked (default) or visible.

Rules not named stay as they are; --replace drops them, so the scope inherits
everything not named.`,
		Example: `  dock db policy set deploy.production=read
  dock db policy set tokens.non-production=propose -p shop
  dock db policy set deploy.production=inherit -p shop -e production`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && !replace {
				return usageError("no rules given", "Pass rules like deploy.production=read, or --replace to clear the scope.")
			}
			changes := make([]policyChange, 0, len(args))
			for _, a := range args {
				c, err := parsePolicyRule(a)
				if err != nil {
					return err
				}
				changes = append(changes, c)
			}
			path, title, err := policyPath(project, env)
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			var rules []apitypes.DataPolicyRule
			if !replace {
				cur, err := get[apitypes.DataPolicy](cmd.Context(), s, path)
				if err != nil {
					return err
				}
				rules = cur.Rules
			}
			for _, c := range changes {
				rules = slices.DeleteFunc(rules, func(r apitypes.DataPolicyRule) bool { return r.Subject == c.rule.Subject && r.Kind == c.rule.Kind })
				if !c.inherit {
					rules = append(rules, c.rule)
				}
			}
			if rules == nil {
				rules = []apitypes.DataPolicyRule{}
			}
			p, err := send[apitypes.DataPolicy](cmd.Context(), s, http.MethodPut, path, apitypes.UpdateDataPolicyRequest{Rules: rules})
			if err != nil {
				return err
			}
			return g.io.Print(p, func() error {
				g.io.Successf("Updated the %s", title)
				return printPolicy(g.io, title, p)
			})
		},
	}
	scopeFlags(set)
	set.Flags().BoolVar(&replace, "replace", false, "drop the scope's rules that aren't named")
	cmd.AddCommand(show, set)
	return cmd
}

// policyChange is one parsed `policy set` argument.
type policyChange struct {
	rule    apitypes.DataPolicyRule
	inherit bool
}

// parsePolicyRule reads SUBJECT.KIND=LEVEL[,SENSITIVE].
func parsePolicyRule(s string) (policyChange, error) {
	bad := func(what string) (policyChange, error) {
		return policyChange{}, usageError(fmt.Sprintf("invalid rule %q: %s", s, what),
			"Use SUBJECT.KIND=LEVEL[,SENSITIVE], e.g. deploy.production=read or tokens.non-production=propose,masked.")
	}
	lhs, rhs, ok := strings.Cut(s, "=")
	subject, kind, ok2 := strings.Cut(lhs, ".")
	if !ok || !ok2 {
		return bad("missing . or =")
	}
	var c policyChange
	switch strings.TrimPrefix(subject, "role:") {
	case "read":
		c.rule.Subject = apitypes.SubjectReadUsers
	case "deploy":
		c.rule.Subject = apitypes.SubjectDeployUsers
	case "tokens":
		c.rule.Subject = apitypes.SubjectTokens
	default:
		return bad("the subject is read, deploy or tokens")
	}
	switch kind {
	case "production":
		c.rule.Kind = apitypes.KindProduction
	case "non-production", "non_production":
		c.rule.Kind = apitypes.KindNonProduction
	default:
		return bad("the kind is production or non-production")
	}
	level, sensitive, _ := strings.Cut(rhs, ",")
	if level == "inherit" {
		c.inherit = true
		return c, nil
	}
	c.rule.Level = apitypes.DataLevel(level)
	if !slices.Contains(dataLevels, c.rule.Level) {
		return bad("the level is none, schema, read, propose, write or inherit")
	}
	c.rule.Sensitive = apitypes.SensitiveMasked
	if sensitive != "" {
		c.rule.Sensitive = apitypes.DataSensitivity(sensitive)
		if c.rule.Sensitive != apitypes.SensitiveMasked && c.rule.Sensitive != apitypes.SensitiveVisible {
			return bad("sensitive is masked or visible")
		}
	}
	return c, nil
}

var dataLevels = []apitypes.DataLevel{apitypes.LevelNone, apitypes.LevelSchema, apitypes.LevelRead, apitypes.LevelPropose, apitypes.LevelWrite}

// printPolicy prints the effective rules at a scope, marking where each
// comes from.
func printPolicy(o *output.IO, title string, p apitypes.DataPolicy) error {
	_, _ = fmt.Fprintln(o.Out, title)
	t := o.Table("subject", "kind", "level", "sensitive", "from")
	for _, r := range p.Effective {
		from := string(r.From)
		if r.From == p.Scope {
			from += " (set here)"
		}
		t.Row(string(r.Subject), string(r.Kind), string(r.Level), string(r.Sensitive), strings.ReplaceAll(from, "_", "-"))
	}
	return t.Flush()
}

func newDBSensitiveCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "sensitive",
		Short: "Columns whose values are masked (admin)",
		Long: `Sensitive columns are masked in table pages. Callers whose data access
says masked (everyone but admins, by default) never see them; callers with
visible reveal them with ` + "`dock db rows TABLE --reveal`" + `, which is audited.`,
	})
	list := dbCmd(g, &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List sensitive columns",
		Args:    cobra.NoArgs,
	}, func(ctx context.Context, s *session, _ target, data string, _ []string) error {
		sc, err := schemaOf(ctx, s, data)
		if err != nil {
			return err
		}
		cols := []apitypes.DataColumnRef{}
		for _, tb := range sc.Tables {
			for _, c := range tb.Columns {
				if c.Sensitive {
					cols = append(cols, apitypes.DataColumnRef{Schema: tb.Schema, Table: tb.Name, Column: c.Name})
				}
			}
		}
		return g.io.Print(cols, func() error { return printColumns(g.io, cols) })
	})

	var clear, keys bool
	set := dbCmd(g, &cobra.Command{
		Use:   "set [schema.]table.column... | --keys PATTERN... | --clear",
		Short: "Replace the list of sensitive columns (or, on Redis, key patterns)",
		Long: `Replace the whole list with the columns named (a bare table name is looked
up, Postgres's public schema first), or empty it with --clear. On Redis, --keys takes key patterns (globs such as
session:*): matching keys are masked for callers who see sensitive values
masked, and read only with an audited --reveal by the others.`,
		Example: `  dock db sensitive set users.password_hash users.email
  dock db sensitive set --keys 'session:*' 'auth:*'
  dock db sensitive set --clear`,
	}, func(ctx context.Context, s *session, _ target, data string, args []string) error {
		if keys {
			patterns, err := send[[]string](ctx, s, http.MethodPut, data+"/sensitive-keys", apitypes.SetSensitiveKeysRequest{Patterns: append([]string{}, args...)})
			if err != nil {
				return err
			}
			return g.io.Print(patterns, func() error {
				g.io.Successf("%s marked sensitive", plural(len(patterns), "key pattern"))
				for _, p := range patterns {
					g.io.Detail(p)
				}
				return nil
			})
		}
		if len(args) == 0 && !clear {
			return usageError("no columns given", "Name columns as table.column or schema.table.column, or pass --clear.")
		}
		req := apitypes.SetSensitiveColumnsRequest{Columns: []apitypes.DataColumnRef{}}
		for _, a := range args {
			parts := strings.Split(a, ".")
			switch len(parts) {
			case 2:
				table, err := qualify(ctx, s, data, parts[0])
				if err != nil {
					return err
				}
				schema, name, _ := strings.Cut(table, ".")
				req.Columns = append(req.Columns, apitypes.DataColumnRef{Schema: schema, Table: name, Column: parts[1]})
			case 3:
				req.Columns = append(req.Columns, apitypes.DataColumnRef{Schema: parts[0], Table: parts[1], Column: parts[2]})
			default:
				return usageError(fmt.Sprintf("invalid column %q", a), "Name columns as table.column or schema.table.column.")
			}
		}
		cols, err := send[[]apitypes.DataColumnRef](ctx, s, http.MethodPut, data+"/sensitive-columns", req)
		if err != nil {
			return err
		}
		return g.io.Print(cols, func() error {
			g.io.Successf("%s marked sensitive", plural(len(cols), "column"))
			return printColumns(g.io, cols)
		})
	})
	set.Flags().BoolVar(&clear, "clear", false, "mark no columns sensitive")
	set.Flags().BoolVar(&keys, "keys", false, "the arguments are Redis key patterns (none clears them)")
	cmd.AddCommand(list, set)
	return cmd
}

func printColumns(o *output.IO, cols []apitypes.DataColumnRef) error {
	for _, c := range cols {
		_, _ = fmt.Fprintf(o.Out, "%s.%s.%s\n", c.Schema, c.Table, c.Column)
	}
	return nil
}
