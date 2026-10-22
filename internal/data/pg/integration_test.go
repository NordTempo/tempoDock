//go:build integration

package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// The integration tests share one postgres:17-alpine container, started by
// TestMain on a random loopback port and removed afterwards. Run with
// `go test -tags integration ./internal/data/pg`.

const (
	ownerPassword  = "owner-pw"
	readerPassword = "reader-pw"
	writerPassword = "writer-pw"
)

var pgAddr string

func TestMain(m *testing.M) {
	name := fmt.Sprintf("dock-pgtest-%d", time.Now().UnixNano())
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name,
		"-e", "POSTGRES_PASSWORD="+ownerPassword, "-p", "127.0.0.1::5432", "postgres:17-alpine").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres: %v: %s\n", err, out)
		os.Exit(1)
	}
	code := func() int {
		defer func() { _ = exec.Command("docker", "rm", "-f", name).Run() }()
		out, err := exec.Command("docker", "port", name, "5432/tcp").Output()
		if err != nil {
			fmt.Fprintf(os.Stderr, "docker port: %v\n", err)
			return 1
		}
		pgAddr = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		if err := seed(); err != nil {
			fmt.Fprintf(os.Stderr, "seed: %v\n", err)
			return 1
		}
		return m.Run()
	}()
	os.Exit(code)
}

func dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", pgAddr)
}

func target(id int64) data.Target {
	return data.Target{
		ServiceID: id,
		Dial:      dial,
		Database:  "postgres",
		Reader:    data.Credentials{User: string(data.RoleReader), Password: readerPassword},
		Writer:    data.Credentials{User: string(data.RoleWriter), Password: writerPassword},
	}
}

var owner = data.Credentials{User: "postgres", Password: ownerPassword}

const seedSQL = `
CREATE TYPE mood AS ENUM ('happy', 'sad');
CREATE TABLE customers (id int PRIMARY KEY, name text NOT NULL, email text, password_hash text);
CREATE TABLE items (
  id int PRIMARY KEY,
  name text,
  qty int2 NOT NULL DEFAULT 0,
  big int8,
  price numeric(10,2),
  ratio float8,
  ok bool,
  uid uuid,
  doc jsonb,
  j json,
  at timestamptz,
  raw bytea,
  tags text[],
  nums int4[],
  m mood,
  total numeric GENERATED ALWAYS AS (price * qty) STORED,
  customer_id int REFERENCES customers (id),
  CHECK (qty >= 0)
);
COMMENT ON TABLE items IS 'things';
CREATE INDEX items_name ON items (name);
INSERT INTO customers SELECT g, 'c' || g, 'c' || g || '@x.io', 'hash' || g FROM generate_series(1, 5) g;
INSERT INTO items (id, name, qty, big, price, ratio, ok, uid, doc, j, at, raw, tags, nums, m, customer_id)
SELECT g,
  CASE WHEN g % 10 = 0 THEN NULL ELSE 'item ' || lpad(g::text, 3, '0') END,
  g % 7, g * 10000000000, g * 1.25, g / 4.0, g % 2 = 0,
  ('00000000-0000-0000-0000-' || lpad(g::text, 12, '0'))::uuid,
  jsonb_build_object('n', g, 'even', g % 2 = 0), json_build_object('n', g),
  timestamptz '2026-01-01 00:00:00+00' + g * interval '1 hour',
  decode(lpad(to_hex(g), 4, '0'), 'hex'),
  ARRAY['t' || g, 'all'], ARRAY[g, g + 1],
  CASE WHEN g % 2 = 0 THEN 'happy' ELSE 'sad' END::mood,
  1 + g % 5
FROM generate_series(1, 250) g;
UPDATE items SET ratio = 'NaN' WHERE id = 3;
CREATE TABLE nokey (a int, b text);
INSERT INTO nokey SELECT g, 'x' FROM generate_series(1, 30) g;
CREATE VIEW cheap AS SELECT id, name FROM items WHERE price < 10;
CREATE TABLE notes (id int PRIMARY KEY, body text);
INSERT INTO notes VALUES (1, repeat('é', 6000));
ANALYZE;
`

// seed waits for the server, loads the test data as the owner and sets up
// Dock's roles.
func seed() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	t := target(0)
	for {
		pg, err := connect(ctx, t, owner)
		if err == nil {
			err = simpleExec(ctx, pg, seedSQL)
			_ = pg.Close(ctx)
			if err != nil {
				return err
			}
			break
		}
		if ctx.Err() != nil {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
	e := New()
	defer e.Close()
	_, err := e.Setup(ctx, t, owner, t.Reader, t.Writer)
	return err
}

func newEngine(t *testing.T) *Engine {
	e := New()
	t.Cleanup(e.Close)
	return e
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSetupStatusAndRoles(t *testing.T) {
	ctx := ctxT(t)
	e := newEngine(t)
	tg := target(1)

	info, err := e.Status(ctx, tg)
	if err != nil {
		t.Fatal(err)
	}
	if info.VersionNum < 170000 || !strings.HasPrefix(info.Version, "17") {
		t.Fatalf("version %+v", info)
	}

	if _, err := e.Setup(ctx, tg, data.Credentials{User: "postgres", Password: "wrong"}, tg.Reader, tg.Writer); !errors.Is(err, data.ErrLoginFailed) {
		t.Fatalf("wrong owner password: %v", err)
	}

	bad := tg
	bad.ServiceID = 2
	bad.Reader.Password = "nope"
	if _, err := e.Status(ctx, bad); !errors.Is(err, data.ErrNotSetUp) {
		t.Fatalf("wrong reader password: %v", err)
	}

	// The roles can't escape their grants.
	reader, err := connect(ctx, tg, tg.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(ctx) }()
	if _, err := reader.Exec(ctx, "INSERT INTO nokey VALUES (1, 'x')"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("reader insert: %v", err)
	}
	writer, err := connect(ctx, tg, tg.Writer)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close(ctx) }()
	for _, ddl := range []string{"DROP TABLE nokey", "CREATE TABLE evil (a int)", "TRUNCATE nokey"} {
		if _, err := writer.Exec(ctx, ddl); err == nil {
			t.Fatalf("writer ran %q", ddl)
		}
	}

	// Rotating changes the passwords; setup is idempotent.
	rot := tg
	rot.Reader.Password, rot.Writer.Password = "rotated-r", "rotated-w"
	if _, err := e.Setup(ctx, rot, owner, rot.Reader, rot.Writer); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Status(ctx, tg); !errors.Is(err, data.ErrNotSetUp) {
		t.Fatalf("old password after rotation: %v", err)
	}
	if _, err := e.Status(ctx, rot); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Setup(ctx, tg, owner, tg.Reader, tg.Writer); err != nil {
		t.Fatal(err)
	}
}

func TestSchema(t *testing.T) {
	ctx := ctxT(t)
	e := newEngine(t)
	s, err := e.Schema(ctx, target(1), data.Call{Actor: "test"}, []apitypes.DataColumnRef{{Schema: "public", Table: "customers", Column: "password_hash"}})
	if err != nil {
		t.Fatal(err)
	}
	tables := map[string]apitypes.DataTable{}
	for _, tb := range s.Tables {
		tables[tb.Schema+"."+tb.Name] = tb
	}
	items := tables["public.items"]
	if !items.Editable || items.Kind != apitypes.TableKindTable || items.Comment != "things" || items.EstimatedRows != 250 {
		t.Fatalf("items: %+v", items)
	}
	cols := map[string]apitypes.DataColumn{}
	for _, c := range items.Columns {
		cols[c.Name] = c
	}
	if c := cols["price"]; c.Type != "numeric(10,2)" || c.TypeName != "numeric" || !c.Nullable {
		t.Fatalf("price: %+v", c)
	}
	if c := cols["qty"]; c.TypeName != "int2" || c.Nullable || c.Default != "0" {
		t.Fatalf("qty: %+v", c)
	}
	if c := cols["tags"]; c.TypeName != "_text" {
		t.Fatalf("tags: %+v", c)
	}
	if c := cols["m"]; c.TypeName != "mood" {
		t.Fatalf("m: %+v", c)
	}
	if !cols["total"].Generated {
		t.Fatal("total should be generated")
	}
	if len(items.ForeignKeys) != 1 || items.ForeignKeys[0].RefTable != "customers" || items.ForeignKeys[0].Columns[0] != "customer_id" {
		t.Fatalf("fks: %+v", items.ForeignKeys)
	}
	if len(items.PrimaryKey) != 1 || items.PrimaryKey[0] != "id" || len(items.Indexes) != 2 || len(items.Constraints) != 1 {
		t.Fatalf("keys: %+v %+v %+v", items.PrimaryKey, items.Indexes, items.Constraints)
	}
	if tables["public.nokey"].Editable || tables["public.cheap"].Editable || tables["public.cheap"].Kind != apitypes.TableKindView {
		t.Fatal("nokey and cheap aren't editable")
	}
	for _, c := range tables["public.customers"].Columns {
		if c.Sensitive != (c.Name == "password_hash") {
			t.Fatalf("sensitive mark on %s", c.Name)
		}
	}
}

// allRows pages through a table and returns every row, decoded.
func allRows(t *testing.T, e *Engine, req apitypes.DataPageRequest) [][]any {
	t.Helper()
	var out [][]any
	for i := 0; ; i++ {
		page, err := e.Page(ctxT(t), target(1), data.Call{}, req)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Rows {
			var row []any
			if err := json.Unmarshal(r, &row); err != nil {
				t.Fatalf("row %s: %v", r, err)
			}
			out = append(out, row)
		}
		if page.Next == "" {
			return out
		}
		if i > 100 {
			t.Fatal("too many pages")
		}
		req.After = page.Next
	}
}

func TestPageKeyset(t *testing.T) {
	e := newEngine(t)
	rows := allRows(t, e, apitypes.DataPageRequest{Table: "public.items", Limit: 100})
	if len(rows) != 250 {
		t.Fatalf("got %d rows", len(rows))
	}
	for i, r := range rows {
		if r[0].(float64) != float64(i+1) {
			t.Fatalf("row %d has id %v", i, r[0])
		}
	}

	for _, order := range []string{"name", "-name", "-qty", "ratio"} {
		rows := allRows(t, e, apitypes.DataPageRequest{Table: "public.items", Order: order, Limit: 40})
		seen := map[float64]bool{}
		for _, r := range rows {
			seen[r[0].(float64)] = true
		}
		if len(rows) != 250 || len(seen) != 250 {
			t.Fatalf("order %s: %d rows, %d distinct", order, len(rows), len(seen))
		}
		if order == "name" && rows[249][1] != nil || order == "-name" && rows[0][1] != nil {
			t.Fatalf("order %s: nulls in the wrong place", order)
		}
	}

	if n := len(allRows(t, e, apitypes.DataPageRequest{Table: "public.nokey", Limit: 7})); n != 30 {
		t.Fatalf("nokey by ctid: %d rows", n)
	}
	if n := len(allRows(t, e, apitypes.DataPageRequest{Table: "public.cheap", Order: "id", Limit: 3})); n != 7 {
		t.Fatalf("view by offset: %d rows", n)
	}
}

func TestPageFilters(t *testing.T) {
	e := newEngine(t)
	ctx := ctxT(t)
	f := func(col string, op apitypes.DataFilterOp, v string, vs ...string) apitypes.DataFilter {
		return apitypes.DataFilter{Column: col, Op: op, Value: v, Values: vs}
	}
	cases := []struct {
		filter apitypes.DataFilter
		want   int64
	}{
		{f("id", apitypes.FilterGt, "200"), 50},
		{f("name", apitypes.FilterILike, "ITEM 00%"), 9},
		{f("uid", apitypes.FilterEq, "00000000-0000-0000-0000-000000000007"), 1},
		{f("doc", apitypes.FilterEq, `{"even": true, "n": 4}`), 1},
		{f("doc", apitypes.FilterLike, `%"even": true%`), 125},
		{f("j", apitypes.FilterEq, `{"n" : 5}`), 1},
		{f("at", apitypes.FilterGte, "2026-01-11 00:00:00+00"), 11},
		{f("price", apitypes.FilterLt, "2.50"), 1},
		{f("raw", apitypes.FilterEq, `\x0007`), 1},
		{f("tags", apitypes.FilterEq, "{t5,all}"), 1},
		{f("name", apitypes.FilterIsNull, ""), 25},
		{f("name", apitypes.FilterNotNull, ""), 225},
		{f("m", apitypes.FilterIn, "", "happy"), 125},
		{f("id", apitypes.FilterIn, "", "1", "2", "300"), 2},
		{f("name", apitypes.FilterIn, "", `item 001`, `it"em`, `a\b`), 1},
		{f("ok", apitypes.FilterEq, "true"), 125},
		{f("ratio", apitypes.FilterEq, "NaN"), 1},
		{f("big", apitypes.FilterGte, "2500000000000"), 1},
		{f("m", apitypes.FilterNe, "happy"), 125},
	}
	for _, c := range cases {
		page, err := e.Page(ctx, target(1), data.Call{}, apitypes.DataPageRequest{Table: "public.items", Filters: []apitypes.DataFilter{c.filter}, Count: true, Limit: 1000})
		if err != nil {
			t.Fatalf("%+v: %v", c.filter, err)
		}
		if page.ExactRows == nil || *page.ExactRows != c.want || int64(len(page.Rows)) != c.want {
			t.Fatalf("%+v: count %v, %d rows, want %d", c.filter, page.ExactRows, len(page.Rows), c.want)
		}
	}

	bad := []apitypes.DataPageRequest{
		{Table: "public.items", Filters: []apitypes.DataFilter{f("nope", apitypes.FilterEq, "1")}},
		{Table: "public.items", Filters: []apitypes.DataFilter{f("id", apitypes.FilterEq, "abc")}},
		{Table: "public.items", Filters: []apitypes.DataFilter{f("id", apitypes.FilterIsNull, "1")}},
		{Table: "public.items", Order: "nope"},
		{Table: "public.items", After: "garbage"},
		{Table: "public.items", Full: true, Limit: 2},
	}
	for _, req := range bad {
		if _, err := e.Page(ctx, target(1), data.Call{}, req); !errors.Is(err, data.ErrInvalid) {
			t.Fatalf("%+v: %v", req, err)
		}
	}
	if _, err := e.Page(ctx, target(1), data.Call{}, apitypes.DataPageRequest{Table: "public.nope"}); !errors.Is(err, data.ErrNoTable) {
		t.Fatalf("unknown table: %v", err)
	}
	// A cursor is bound to its request.
	p1, err := e.Page(ctx, target(1), data.Call{}, apitypes.DataPageRequest{Table: "public.items", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Page(ctx, target(1), data.Call{}, apitypes.DataPageRequest{Table: "public.items", Order: "name", After: p1.Next}); !errors.Is(err, data.ErrInvalid) {
		t.Fatalf("cursor of another order: %v", err)
	}
}

func TestPageCells(t *testing.T) {
	e := newEngine(t)
	ctx := ctxT(t)
	page, err := e.Page(ctx, target(1), data.Call{}, apitypes.DataPageRequest{Table: "public.items", Filters: []apitypes.DataFilter{{Column: "id", Op: apitypes.FilterIn, Values: []string{"3", "4"}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `[4,"item 004",4,"40000000000","5.00",1,true,"00000000-0000-0000-0000-000000000004","{\"n\": 4, \"even\": true}","{\"n\" : 4}","2026-01-01 04:00:00+00","\\x0004","{t4,all}","{4,5}","happy","20.00",5]`
	if string(page.Rows[1]) != want {
		t.Fatalf("row\n got %s\nwant %s", page.Rows[1], want)
	}
	if !strings.Contains(string(page.Rows[0]), `,"NaN",`) {
		t.Fatalf("NaN row: %s", page.Rows[0])
	}
	if page.Columns[2].TypeName != "int2" || page.Columns[14].TypeName != "mood" {
		t.Fatalf("columns: %+v", page.Columns)
	}

	notes, err := e.Page(ctx, target(1), data.Call{}, apitypes.DataPageRequest{Table: "public.notes"})
	if err != nil {
		t.Fatal(err)
	}
	var row []json.RawMessage
	_ = json.Unmarshal(notes.Rows[0], &row)
	var cell struct {
		Truncated string `json:"truncated"`
		Bytes     int    `json:"bytes"`
	}
	if err := json.Unmarshal(row[1], &cell); err != nil || cell.Bytes != 12000 || len(cell.Truncated) != apitypes.DataCellMaxBytes || !utf8.ValidString(cell.Truncated) {
		t.Fatalf("truncated cell: %d bytes, %d, %v", cell.Bytes, len(cell.Truncated), err)
	}
	full, err := e.Page(ctx, target(1), data.Call{}, apitypes.DataPageRequest{Table: "public.notes", Limit: 1, Full: true})
	if err != nil {
		t.Fatal(err)
	}
	var fullRow []any
	_ = json.Unmarshal(full.Rows[0], &fullRow)
	if s, _ := fullRow[1].(string); len(s) != 12000 {
		t.Fatalf("full value: %d bytes", len(s))
	}
}

func TestReferences(t *testing.T) {
	e := newEngine(t)
	refs, err := e.References(ctxT(t), target(1), data.Call{}, apitypes.DataReferencesRequest{Table: "public.customers", Key: map[string]string{"id": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Table != "items" || refs[0].Constraint != "items_customer_id_fkey" {
		t.Fatalf("refs: %+v", refs)
	}
	r := refs[0]
	if len(r.Page.Rows) != apitypes.DataReferencesLimit || r.Page.Next == "" || r.Filters[0].Column != "customer_id" || r.Filters[0].Value != "1" {
		t.Fatalf("ref page: %d rows, next %q, filters %+v", len(r.Page.Rows), r.Page.Next, r.Filters)
	}
	// The filters and cursor open the referencing table where it left off.
	rest := allRows(t, e, apitypes.DataPageRequest{Table: "public.items", Filters: r.Filters, After: r.Page.Next, Limit: apitypes.DataReferencesLimit})
	if len(rest) != 40 {
		t.Fatalf("rest: %d rows", len(rest))
	}
	if _, err := e.References(ctxT(t), target(1), data.Call{}, apitypes.DataReferencesRequest{Table: "public.customers", Key: map[string]string{"name": "x"}}); !errors.Is(err, data.ErrInvalid) {
		t.Fatalf("bad key: %v", err)
	}
}

// collect runs a query and returns its frames (rows copied, since the
// engine reuses them).
func collect(t *testing.T, ctx context.Context, e *Engine, req apitypes.DataQueryRequest) ([]apitypes.DataQueryFrame, data.QueryStats, error) {
	t.Helper()
	var frames []apitypes.DataQueryFrame
	stats, err := e.Query(ctx, target(1), data.Call{Actor: "alice"}, req, func(f apitypes.DataQueryFrame) error {
		b, _ := json.Marshal(f)
		var c apitypes.DataQueryFrame
		_ = json.Unmarshal(b, &c)
		frames = append(frames, c)
		return nil
	})
	return frames, stats, err
}

func TestQuery(t *testing.T) {
	e := newEngine(t)
	ctx := ctxT(t)

	frames, stats, err := collect(t, ctx, e, apitypes.DataQueryRequest{SQL: "SELECT g, 'x' || g AS s FROM generate_series(1, 1200) g;"})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 5 || frames[0].Type != apitypes.FrameHeader || frames[0].Columns[0] != (apitypes.DataResultColumn{Name: "g", TypeName: "int4"}) ||
		len(frames[1].Rows) != 500 || len(frames[2].Rows) != 500 || len(frames[3].Rows) != 200 ||
		frames[4].Type != apitypes.FrameEnd || frames[4].RowCount != 1200 || frames[4].Command != "SELECT 1200" || frames[4].Truncated {
		t.Fatalf("frames: %d %+v", len(frames), frames[len(frames)-1])
	}
	if string(frames[3].Rows[199]) != `[1200,"x1200"]` || stats.Rows != 1200 || stats.Outcome != apitypes.OutcomeOK {
		t.Fatalf("last row %s, stats %+v", frames[3].Rows[199], stats)
	}

	// The row cap truncates and cuts the connection; the next query works.
	frames, stats, err = collect(t, ctx, e, apitypes.DataQueryRequest{SQL: "SELECT g FROM generate_series(1, 10000000) g", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	end := frames[len(frames)-1]
	if end.Type != apitypes.FrameEnd || !end.Truncated || end.RowCount != 1000 || stats.Rows != 1000 {
		t.Fatalf("truncated end: %+v", end)
	}

	// Types the driver doesn't know are named from pg_type.
	frames, _, err = collect(t, ctx, e, apitypes.DataQueryRequest{SQL: "SELECT 'happy'::mood AS m, now() AS t, 1.5::float4 AS f, 'Infinity'::float8 AS inf"})
	if err != nil {
		t.Fatal(err)
	}
	if frames[0].Columns[0].TypeName != "mood" || frames[0].Columns[1].TypeName != "timestamptz" || !strings.HasSuffix(string(frames[1].Rows[0]), `1.5,"Infinity"]`) {
		t.Fatalf("types: %+v %s", frames[0].Columns, frames[1].Rows[0])
	}

	// Session changes are rolled back with the read transaction.
	if _, _, err := collect(t, ctx, e, apitypes.DataQueryRequest{SQL: "SELECT set_config('search_path', 'evil', false)"}); err != nil {
		t.Fatal(err)
	}
	frames, _, _ = collect(t, ctx, e, apitypes.DataQueryRequest{SQL: "SHOW search_path"})
	if strings.Contains(string(frames[1].Rows[0]), "evil") {
		t.Fatal("search_path leaked into the next query")
	}

	// Writes fail in the database even when they look like reads.
	frames, stats, _ = collect(t, ctx, e, apitypes.DataQueryRequest{SQL: "WITH d AS (DELETE FROM items RETURNING 1) SELECT * FROM d"})
	if last := frames[len(frames)-1]; last.Type != apitypes.FrameError || last.Error.SQLState != "25006" || stats.Outcome != apitypes.OutcomeError {
		t.Fatalf("write in CTE: %+v", last)
	}
	frames, _, _ = collect(t, ctx, e, apitypes.DataQueryRequest{SQL: "SELECT nope FROM items"})
	if last := frames[len(frames)-1]; last.Type != apitypes.FrameError || last.Error.SQLState != "42703" || last.Error.Position != 8 {
		t.Fatalf("bad column: %+v", last.Error)
	}

	for _, sql := range []string{"SELECT 1; SELECT 2", "DELETE FROM items", "SET statement_timeout = 0", ""} {
		if _, _, err := collect(t, ctx, e, apitypes.DataQueryRequest{SQL: sql}); !errors.Is(err, data.ErrInvalid) {
			t.Fatalf("%q: %v", sql, err)
		}
	}
	if _, err := e.Query(ctx, target(1), data.Call{Masked: true}, apitypes.DataQueryRequest{SQL: "SELECT 1"}, func(apitypes.DataQueryFrame) error { return nil }); !errors.Is(err, data.ErrInvalid) {
		t.Fatalf("masked SQL: %v", err)
	}
}

func TestQueryTimeoutAndCancel(t *testing.T) {
	e := newEngine(t)
	frames, stats, err := collect(t, ctxT(t), e, apitypes.DataQueryRequest{SQL: "SELECT pg_sleep(5)", TimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	if last := frames[len(frames)-1]; last.Type != apitypes.FrameError || last.Error.SQLState != "57014" || stats.Outcome != apitypes.OutcomeTimeout {
		t.Fatalf("timeout: %+v %+v", last, stats)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	_, stats, err = collect(t, ctx, e, apitypes.DataQueryRequest{SQL: "SELECT pg_sleep(10)"})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Outcome != apitypes.OutcomeCanceled || time.Since(start) > 3*time.Second {
		t.Fatalf("cancel: %+v after %s", stats, time.Since(start))
	}
	// The server canceled the query: no backend is still sleeping.
	frames, _, _ = collect(t, ctxT(t), e, apitypes.DataQueryRequest{SQL: "SELECT count(*) FROM pg_stat_activity WHERE query = 'SELECT pg_sleep(10)' AND state = 'active'"})
	if string(frames[1].Rows[0]) != `["0"]` {
		t.Fatalf("sleeping backends: %s", frames[1].Rows[0])
	}
}

func TestMasked(t *testing.T) {
	ctx := ctxT(t)
	e := newEngine(t)
	sensitive := []apitypes.DataColumnRef{{Schema: "public", Table: "customers", Column: "password_hash"}}
	masked := data.Call{Masked: true, Sensitive: sensitive}
	req := apitypes.DataPageRequest{Table: "public.customers", Limit: 1}
	page, err := e.Page(ctx, target(1), masked, req)
	if err != nil {
		t.Fatal(err)
	}
	if string(page.Rows[0]) != `[1,"c1","c1@x.io",{"masked":true}]` {
		t.Fatalf("masked row: %s", page.Rows[0])
	}
	page, _ = e.Page(ctx, target(1), data.Call{}, req)
	if string(page.Rows[0]) != `[1,"c1","c1@x.io","hash1"]` {
		t.Fatalf("unmasked row: %s", page.Rows[0])
	}
	req.Filters = []apitypes.DataFilter{{Column: "password_hash", Op: apitypes.FilterLike, Value: "hash1%"}}
	if _, err := e.Page(ctx, target(1), masked, req); !errors.Is(err, data.ErrInvalid) {
		t.Fatalf("filter on masked column: %v", err)
	}

	// SQL runs as the masked reader, whose column grants leave out the
	// sensitive columns.
	tg := target(1)
	tg.ReaderMasked = data.Credentials{User: string(data.RoleReaderMasked), Password: "masked-pw"}
	if _, err := e.Status(ctx, tg); !errors.Is(err, data.ErrNotSetUp) {
		t.Fatalf("status before the masked reader exists: %v", err)
	}
	if _, err := e.Setup(ctx, tg, owner, tg.Reader, tg.Writer); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Status(ctx, tg); err != nil {
		t.Fatal(err)
	}
	hash, err := e.SyncMasked(ctx, tg, owner, sensitive)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := e.Schema(ctx, tg, masked, sensitive)
	if err != nil || data.MaskedHash(schema.Tables) != hash {
		t.Fatalf("hash after sync: %s, schema's %s, %v", hash, data.MaskedHash(schema.Tables), err)
	}
	query := func(sql string) []apitypes.DataQueryFrame {
		t.Helper()
		var frames []apitypes.DataQueryFrame
		_, err := e.Query(ctx, tg, masked, apitypes.DataQueryRequest{SQL: sql}, func(f apitypes.DataQueryFrame) error {
			frames = append(frames, f)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return frames
	}
	if f := query("SELECT id, email FROM customers ORDER BY id LIMIT 1"); f[len(f)-1].Type != apitypes.FrameEnd {
		t.Fatalf("masked select of visible columns: %+v", f[len(f)-1])
	}
	for _, sql := range []string{"SELECT password_hash FROM customers", "SELECT * FROM customers"} {
		if f := query(sql); f[len(f)-1].Type != apitypes.FrameError || f[len(f)-1].Error.SQLState != "42501" {
			t.Fatalf("%s: %+v", sql, f[len(f)-1])
		}
	}
	// Unmarking the column and syncing again grants it.
	if _, err := e.SyncMasked(ctx, tg, owner, nil); err != nil {
		t.Fatal(err)
	}
	if f := query("SELECT password_hash FROM customers"); f[len(f)-1].Type != apitypes.FrameEnd {
		t.Fatalf("after unmarking: %+v", f[len(f)-1])
	}
}

func TestCommit(t *testing.T) {
	ctx := ctxT(t)
	e := newEngine(t)
	tg := target(1)
	s := func(v string) *string { return &v }

	update := apitypes.DataChange{
		Op: apitypes.DataChangeUpdate, Table: "public.items",
		Key: map[string]string{"id": "1"},
		Old: map[string]*string{"name": s("item 001"), "price": s("1.25"), "j": s(`{"n" : 1}`)},
		New: map[string]*string{"price": s("2.00"), "name": s("it's")},
	}
	sql, err := e.Render(ctx, tg, []apitypes.DataChange{update})
	if err != nil {
		t.Fatal(err)
	}
	want := "BEGIN;\nUPDATE \"public\".\"items\" SET \"name\" = 'it''s', \"price\" = '2.00' WHERE \"id\" = '1' AND \"name\" = 'item 001' AND \"price\" = '1.25' AND \"j\"::text = '{\"n\" : 1}';\nCOMMIT;\n"
	if sql != want {
		t.Fatalf("render\n got %q\nwant %q", sql, want)
	}
	res, err := e.Commit(ctx, tg, data.Call{Actor: "alice"}, []apitypes.DataChange{update})
	if err != nil || len(res.Conflicts) > 0 || len(res.RowsAffected) != 1 || res.RowsAffected[0] != 1 || res.SQL != want {
		t.Fatalf("commit: %+v %v", res, err)
	}

	// A stale old value fails the whole changeset; nothing is written.
	res, err = e.Commit(ctx, tg, data.Call{}, []apitypes.DataChange{
		{Op: apitypes.DataChangeInsert, Table: "public.items", New: map[string]*string{"id": s("1000"), "name": s("new"), "doc": nil}},
		update,
		{Op: apitypes.DataChangeDelete, Table: "public.items", Key: map[string]string{"id": "9999"}},
		{Op: apitypes.DataChangeDelete, Table: "public.items", Key: map[string]string{"id": "10"}, Old: map[string]*string{"name": nil}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 2 || res.RowsAffected != nil ||
		res.Conflicts[0].Change != 1 || res.Conflicts[0].Reason != apitypes.ConflictChanged || *res.Conflicts[0].Current["name"] != "it's" ||
		res.Conflicts[1].Change != 2 || res.Conflicts[1].Reason != apitypes.ConflictMissing {
		t.Fatalf("conflicts: %+v", res.Conflicts)
	}
	for _, id := range []string{"1000", "10"} {
		page, _ := e.Page(ctx, tg, data.Call{}, apitypes.DataPageRequest{Table: "public.items", Filters: []apitypes.DataFilter{{Column: "id", Op: apitypes.FilterEq, Value: id}}})
		if (id == "1000") != (len(page.Rows) == 0) {
			t.Fatalf("row %s after rolled back commit: %d rows", id, len(page.Rows))
		}
	}

	// A failing statement is a failed conflict.
	res, err = e.Commit(ctx, tg, data.Call{}, []apitypes.DataChange{{Op: apitypes.DataChangeInsert, Table: "public.items", New: map[string]*string{"id": s("2")}}})
	if err != nil || len(res.Conflicts) != 1 || res.Conflicts[0].Reason != apitypes.ConflictFailed || !strings.Contains(res.Conflicts[0].Message, "duplicate key") {
		t.Fatalf("duplicate: %+v %v", res, err)
	}

	for _, bad := range []apitypes.DataChange{
		{Op: apitypes.DataChangeUpdate, Table: "public.items", Key: map[string]string{"id": "1"}, Old: map[string]*string{"total": nil}, New: map[string]*string{"total": s("1")}},
		{Op: apitypes.DataChangeUpdate, Table: "public.items", Key: map[string]string{"id": "1"}, New: map[string]*string{"name": s("x")}},
		{Op: apitypes.DataChangeDelete, Table: "public.nokey", Key: map[string]string{"a": "1"}},
		{Op: apitypes.DataChangeDelete, Table: "public.cheap", Key: map[string]string{"id": "1"}},
		{Op: apitypes.DataChangeInsert, Table: "public.items", New: map[string]*string{"nope": nil}},
	} {
		if _, err := e.Commit(ctx, tg, data.Call{}, []apitypes.DataChange{bad}); !errors.Is(err, data.ErrInvalid) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
}

func TestConnCache(t *testing.T) {
	ctx := ctxT(t)
	e := newEngine(t)
	e.idleTimeout = 200 * time.Millisecond
	count := func() int {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.total
	}

	if _, err := e.Schema(ctx, target(1), data.Call{}, nil); err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatalf("%d conns", count())
	}
	deadline := time.Now().Add(3 * time.Second)
	for count() > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	e.mu.Lock()
	idle, schemas := e.total, len(e.schemas)
	e.mu.Unlock()
	if idle != 0 || schemas != 0 {
		t.Fatalf("after idle: %d conns, %d schemas", idle, schemas)
	}

	// Per key: the third caller waits for a release.
	e.idleTimeout = time.Minute
	a, err := e.acquire(ctx, target(1), data.RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.acquire(ctx, target(1), data.RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if _, err := e.acquire(short, target(1), data.RoleReader); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third conn: %v", err)
	}
	time.AfterFunc(100*time.Millisecond, func() { e.release(a) })
	c, err := e.acquire(ctx, target(1), data.RoleReader)
	if err != nil || c != a {
		t.Fatalf("waiter got %p (%v), want the released %p", c, err, a)
	}
	e.release(b)
	e.release(c)

	// Over MaxConns, an idle connection of another key makes room.
	e.maxConns = 2
	d, err := e.acquire(ctx, target(2), data.RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	left := len(e.conns[connKey{1, data.RoleReader}])
	e.mu.Unlock()
	if count() != 2 || left != 1 {
		t.Fatalf("eviction: %d conns, %d of service 1", count(), left)
	}
	e.release(d)

	e.Forget(1)
	e.Forget(2)
	if count() != 0 {
		t.Fatalf("after forget: %d conns", count())
	}
}

// TestConnMemory reports the Go heap one open, used connection costs.
func TestConnMemory(t *testing.T) {
	ctx := ctxT(t)
	e := newEngine(t)
	const n = 8
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := range n {
		if _, err := e.Page(ctx, target(int64(100+i)), data.Call{}, apitypes.DataPageRequest{Table: "public.items"}); err != nil {
			t.Fatal(err)
		}
	}
	e.mu.Lock()
	clear(e.schemas) // count connections only
	e.mu.Unlock()
	runtime.GC()
	runtime.ReadMemStats(&after)
	t.Logf("heap per open connection: %d KB (%d connections)", int64(after.HeapAlloc-before.HeapAlloc)/n/1024, n)
}
