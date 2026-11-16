//go:build integration

package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/mysql/mysqltest"
)

// The integration tests run against each of mysqltest.Images, started
// once by TestMain. Each server's app database gets seedSQL; every test
// runs per server.

var servers []*mysqltest.Server

var owner = data.Credentials{User: "root", Password: mysqltest.RootPassword}

func TestMain(m *testing.M) {
	var err error
	if servers, err = mysqltest.StartAll(mysqltest.Images...); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := func() int {
		defer func() {
			for _, s := range servers {
				s.Stop()
			}
		}()
		for i, s := range servers {
			if err := seed(target(s, int64(i+1))); err != nil {
				fmt.Fprintf(os.Stderr, "seed %s: %v\n", s.Image, err)
				return 1
			}
		}
		return m.Run()
	}()
	os.Exit(code)
}

var seedSQL = []string{
	"CREATE TABLE customers (id INT PRIMARY KEY, name VARCHAR(100) NOT NULL, email VARCHAR(200), password_hash VARCHAR(200))",
	`CREATE TABLE items (
  id BIGINT PRIMARY KEY,
  name VARCHAR(100),
  qty SMALLINT NOT NULL DEFAULT 0,
  price DECIMAL(10,2),
  ratio DOUBLE,
  raw VARBINARY(16),
  at DATETIME,
  total DECIMAL(12,2) GENERATED ALWAYS AS (price * qty) STORED,
  customer_id INT,
  CONSTRAINT items_customer FOREIGN KEY (customer_id) REFERENCES customers (id),
  CONSTRAINT qty_positive CHECK (qty >= 0))`,
	"CREATE TABLE logs (msg VARCHAR(50))",
	"CREATE VIEW cheap AS SELECT id, name FROM items WHERE price < 10",
	"INSERT INTO customers VALUES (1, 'Ada', 'ada@x.io', 'h1'), (2, 'Bo', 'bo@x.io', 'h2'), (3, 'Cy', NULL, NULL)",
	`INSERT INTO items (id, name, qty, price, ratio, raw, at, customer_id) VALUES
  (1, 'pen', 3, 1.50, 0.5, X'00ff', '2026-10-20 14:02:00', 1),
  (2, 'ink', 1, NULL, NULL, NULL, NULL, 1),
  (3, 'cap', 5, 12.00, 1e100, X'0a', '2026-10-21 08:00:00', 2),
  (9007199254740993, 'big', 0, 4.25, -2.5, NULL, NULL, NULL),
  (5, 'it''s', 2, NULL, NULL, NULL, NULL, NULL)`,
	"INSERT INTO logs VALUES ('a'), ('b'), ('c')",
	// Outside the app database: Dock's users never see it.
	"CREATE DATABASE other",
	"CREATE TABLE other.secret (v VARCHAR(10))",
	"INSERT INTO other.secret VALUES ('no')",
}

// seed creates the test data and Dock's users on t's server.
func seed(t data.Target) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	e := New(Options{})
	defer e.Close()
	c, _, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return err
	}
	defer c.Close()
	for _, sql := range seedSQL {
		if err := c.exec(ctx, sql); err != nil {
			return fmt.Errorf("%s: %w", sql, err)
		}
	}
	if _, err := e.Setup(ctx, t, owner, t.Reader, t.Writer); err != nil {
		return err
	}
	_, err = e.SyncGrants(ctx, t, owner, nil)
	return err
}

const (
	readerPassword = "reader-pw"
	writerPassword = "writer-pw"
	maskedPassword = "masked-pw"
)

// target is s as database id, with Dock's logins.
func target(s *mysqltest.Server, id int64) data.Target {
	return data.Target{
		ServiceID:    id,
		Dial:         s.Dial,
		Database:     mysqltest.Database,
		Reader:       data.Credentials{User: string(data.RoleReader), Password: readerPassword},
		Writer:       data.Credentials{User: string(data.RoleWriter), Password: writerPassword},
		ReaderMasked: data.Credentials{User: string(data.RoleReaderMasked), Password: maskedPassword},
	}
}

// each runs fn per server with a fresh engine and the server's target.
func each(t *testing.T, fn func(t *testing.T, e *Engine, tg data.Target)) {
	for i, s := range servers {
		t.Run(s.Image, func(t *testing.T) {
			e := New(Options{})
			t.Cleanup(e.Close)
			fn(t, e, target(s, int64(i+1)))
		})
	}
}

// ownerExec runs sql as root on tg's server.
func ownerExec(t *testing.T, e *Engine, tg data.Target, sql string) {
	t.Helper()
	c, _, err := e.ownerConn(t.Context(), tg, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.exec(t.Context(), sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func TestSetupAndStatus(t *testing.T) {
	each(t, func(t *testing.T, e *Engine, tg data.Target) {
		ctx := t.Context()
		info, err := e.Check(ctx, tg, owner)
		if err != nil || info.VersionNum < minMySQL {
			t.Fatalf("check: %+v %v", info, err)
		}
		if _, err := e.Check(ctx, tg, data.Credentials{User: "root", Password: "wrong"}); !errors.Is(err, data.ErrLoginFailed) {
			t.Fatalf("wrong owner password: %v", err)
		}
		if info, err := e.Status(ctx, tg); err != nil || info.Version == "" {
			t.Fatalf("status: %+v %v", info, err)
		}
		bad := tg
		bad.ServiceID += 100
		bad.Reader.Password = "wrong"
		if _, err := e.Status(ctx, bad); !errors.Is(err, data.ErrNotSetUp) {
			t.Fatalf("wrong reader password: %v", err)
		}
		// Setup again rotates passwords and keeps working.
		if _, err := e.Setup(ctx, tg, owner, tg.Reader, tg.Writer); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Status(ctx, tg); err != nil {
			t.Fatalf("status after setup again: %v", err)
		}
	})
}

// TestUsersCantReadSecrets: Dock's users see the app database only: not
// mysql.* (password hashes), not other databases, and the reader can't
// write.
func TestUsersCantReadSecrets(t *testing.T) {
	each(t, func(t *testing.T, e *Engine, tg data.Target) {
		for _, sql := range []string{
			"SELECT authentication_string FROM mysql.user",
			"SELECT v FROM other.secret",
		} {
			frames := query(t, e, tg, data.Call{}, sql)
			if last := frames[len(frames)-1]; last.Type != apitypes.FrameError {
				t.Errorf("%s as the reader: %+v", sql, last)
			}
		}
		cn, err := e.acquire(t.Context(), tg, data.RoleReader)
		if err != nil {
			t.Fatal(err)
		}
		defer e.release(cn)
		if err := cn.Conn.exec(t.Context(), "INSERT INTO logs VALUES ('x')"); err == nil {
			t.Error("the reader inserted a row")
		}
	})
}

func TestSchema(t *testing.T) {
	each(t, func(t *testing.T, e *Engine, tg data.Target) {
		s, err := e.Schema(t.Context(), tg, data.Call{}, []apitypes.DataColumnRef{{Schema: "app", Table: "customers", Column: "email"}})
		if err != nil {
			t.Fatal(err)
		}
		byName := map[string]apitypes.DataTable{}
		for _, tb := range s.Tables {
			byName[tb.Name] = tb
			if tb.Schema != "app" {
				t.Errorf("table outside the app database: %s.%s", tb.Schema, tb.Name)
			}
		}
		items, cust, logs, cheap := byName["items"], byName["customers"], byName["logs"], byName["cheap"]
		if !items.Editable || !slices.Equal(items.PrimaryKey, []string{"id"}) || len(items.ForeignKeys) != 1 ||
			items.ForeignKeys[0].RefTable != "customers" || !slices.Equal(items.ForeignKeys[0].RefColumns, []string{"id"}) {
			t.Errorf("items: %+v", items)
		}
		var total, price apitypes.DataColumn
		for _, c := range items.Columns {
			switch c.Name {
			case "total":
				total = c
			case "price":
				price = c
			}
		}
		if !total.Generated || price.TypeName != "decimal" || price.Type != "decimal(10,2)" || !price.Nullable {
			t.Errorf("columns: total %+v price %+v", total, price)
		}
		hasCheck := slices.ContainsFunc(items.Constraints, func(c apitypes.DataConstraint) bool { return c.Kind == apitypes.ConstraintCheck })
		if !hasCheck {
			t.Errorf("no check constraint: %+v", items.Constraints)
		}
		if !cust.Columns[2].Sensitive || cust.Columns[1].Sensitive {
			t.Errorf("sensitive marks: %+v", cust.Columns)
		}
		if logs.Editable || len(logs.PrimaryKey) != 0 || cheap.Kind != apitypes.TableKindView || cheap.Editable {
			t.Errorf("logs %+v cheap %+v", logs, cheap)
		}
	})
}

// rows decodes a page's rows into cells.
func rows(t *testing.T, p apitypes.DataPage) [][]any {
	t.Helper()
	out := make([][]any, len(p.Rows))
	for i, r := range p.Rows {
		if err := json.Unmarshal(r, &out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// walk reads every page of req and returns the first column of each row.
func walk(t *testing.T, e *Engine, tg data.Target, req apitypes.DataPageRequest) []any {
	t.Helper()
	var out []any
	for range 20 {
		p, err := e.Page(t.Context(), tg, data.Call{}, req)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows(t, p) {
			out = append(out, r[0])
		}
		if p.Next == "" {
			return out
		}
		req.After = p.Next
	}
	t.Fatal("too many pages")
	return nil
}

func TestPages(t *testing.T) {
	each(t, func(t *testing.T, e *Engine, tg data.Target) {
		ids := func(vals ...any) []any { return vals }
		// Keyset by the BIGINT key, past 2^53.
		got := walk(t, e, tg, apitypes.DataPageRequest{Table: "app.items", Limit: 2})
		if want := ids(1.0, 2.0, 3.0, 5.0, "9007199254740993"); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("by key: %v", got)
		}
		// By a nullable column: nulls first ascending, last descending.
		asc := walk(t, e, tg, apitypes.DataPageRequest{Table: "app.items", Order: "price", Limit: 2})
		desc := walk(t, e, tg, apitypes.DataPageRequest{Table: "app.items", Order: "-price", Limit: 2})
		if fmt.Sprint(asc) != fmt.Sprint(ids(2.0, 5.0, 1.0, "9007199254740993", 3.0)) ||
			fmt.Sprint(desc) != fmt.Sprint(ids(3.0, "9007199254740993", 1.0, 5.0, 2.0)) {
			t.Errorf("by price: asc %v desc %v", asc, desc)
		}
		// Filters, values rendered for their types.
		for _, c := range []struct {
			f    apitypes.DataFilter
			want []any
		}{
			{apitypes.DataFilter{Column: "id", Op: apitypes.FilterEq, Value: "9007199254740993"}, ids("9007199254740993")},
			{apitypes.DataFilter{Column: "name", Op: apitypes.FilterEq, Value: "it's"}, ids(5.0)},
			{apitypes.DataFilter{Column: "name", Op: apitypes.FilterILike, Value: "P%"}, ids(1.0)},
			{apitypes.DataFilter{Column: "price", Op: apitypes.FilterIsNull}, ids(2.0, 5.0)},
			{apitypes.DataFilter{Column: "raw", Op: apitypes.FilterEq, Value: `\x00ff`}, ids(1.0)},
			{apitypes.DataFilter{Column: "id", Op: apitypes.FilterIn, Values: []string{"1", "3"}}, ids(1.0, 3.0)},
			{apitypes.DataFilter{Column: "at", Op: apitypes.FilterGte, Value: "2026-10-21"}, ids(3.0)},
		} {
			if got := walk(t, e, tg, apitypes.DataPageRequest{Table: "app.items", Filters: []apitypes.DataFilter{c.f}}); fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Errorf("%+v: %v", c.f, got)
			}
		}
		if _, err := e.Page(t.Context(), tg, data.Call{}, apitypes.DataPageRequest{Table: "app.items",
			Filters: []apitypes.DataFilter{{Column: "id", Op: apitypes.FilterEq, Value: "1 OR 1=1"}}}); !errors.Is(err, data.ErrInvalid) {
			t.Errorf("non-number for a number column: %v", err)
		}

		// Cells: numbers, exact decimals and bigints, \x hex, UTC datetimes.
		p, err := e.Page(t.Context(), tg, data.Call{}, apitypes.DataPageRequest{Table: "app.items", Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		// BIGINT goes out as an exact string, like Postgres' int8.
		if r := string(p.Rows[0]); r != `["1","pen",3,"1.50",0.5,"\\x00ff","2026-10-20 14:02:00","4.50",1]` {
			t.Errorf("cells: %s", r)
		}

		// A table without a key pages by offset; a cursor from elsewhere
		// is refused.
		if got := walk(t, e, tg, apitypes.DataPageRequest{Table: "app.logs", Limit: 2}); len(got) != 3 {
			t.Errorf("logs: %v", got)
		}
		first, _ := e.Page(t.Context(), tg, data.Call{}, apitypes.DataPageRequest{Table: "app.items", Limit: 1})
		if _, err := e.Page(t.Context(), tg, data.Call{}, apitypes.DataPageRequest{Table: "app.items", Order: "name", After: first.Next}); !errors.Is(err, data.ErrInvalid) {
			t.Errorf("foreign cursor: %v", err)
		}

		refs, err := e.References(t.Context(), tg, data.Call{}, apitypes.DataReferencesRequest{Table: "app.customers", Key: map[string]string{"id": "1"}})
		if err != nil || len(refs) != 1 || refs[0].Table != "items" || len(refs[0].Page.Rows) != 2 {
			t.Fatalf("references: %+v %v", refs, err)
		}
	})
}

// query runs sql and returns its frames.
func query(t *testing.T, e *Engine, tg data.Target, c data.Call, sql string) []apitypes.DataQueryFrame {
	t.Helper()
	var frames []apitypes.DataQueryFrame
	_, err := e.Query(t.Context(), tg, c, apitypes.DataQueryRequest{SQL: sql}, func(f apitypes.DataQueryFrame) error {
		// Frames reuse their buffers: keep a copy.
		b, _ := json.Marshal(f)
		var cp apitypes.DataQueryFrame
		_ = json.Unmarshal(b, &cp)
		frames = append(frames, cp)
		return nil
	})
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return frames
}

func TestQuery(t *testing.T) {
	each(t, func(t *testing.T, e *Engine, tg data.Target) {
		f := query(t, e, tg, data.Call{Actor: "ada"}, "SELECT id, price, raw FROM items ORDER BY id")
		if len(f) != 3 || f[0].Type != apitypes.FrameHeader || f[0].Columns[0].TypeName != "bigint" || f[0].Columns[2].TypeName != "varbinary" ||
			len(f[1].Rows) != 5 || string(f[1].Rows[0]) != `["1","1.50","\\x00ff"]` || f[2].Type != apitypes.FrameEnd || f[2].Command != "SELECT 5" {
			t.Fatalf("frames: %+v", f)
		}
		var capped []apitypes.DataQueryFrame
		stats, err := e.Query(t.Context(), tg, data.Call{}, apitypes.DataQueryRequest{SQL: "SELECT id FROM items", Limit: 2}, func(f apitypes.DataQueryFrame) error {
			capped = append(capped, f)
			return nil
		})
		if err != nil || stats.Rows != 2 || !capped[len(capped)-1].Truncated {
			t.Fatalf("capped: %+v %v", capped, err)
		}
		if f := query(t, e, tg, data.Call{}, "SELECT nope FROM items"); f[len(f)-1].Type != apitypes.FrameError || f[len(f)-1].Error.SQLState != "42S22" {
			t.Fatalf("error frame: %+v", f)
		}
		for _, sql := range []string{
			"DELETE FROM items",
			"SELECT * FROM items FOR UPDATE",
			"SELECT /*!50000 SLEEP(1) */ 1",
			"SET @a = 1",
		} {
			if _, err := e.Query(t.Context(), tg, data.Call{}, apitypes.DataQueryRequest{SQL: sql}, func(apitypes.DataQueryFrame) error { return nil }); !errors.Is(err, data.ErrInvalid) {
				t.Errorf("%s: %v", sql, err)
			}
		}

		// A named lock taken by a query is released after it.
		query(t, e, tg, data.Call{}, "SELECT GET_LOCK('dock_test', 0)")
		f = query(t, e, tg, data.Call{}, "SELECT IS_FREE_LOCK('dock_test')")
		// IS_FREE_LOCK is a BIGINT on MySQL, an INT on MariaDB.
		if r := string(f[1].Rows[0]); r != "[1]" && r != `["1"]` {
			t.Errorf("lock still held: %s", r)
		}

		// The statement timeout ends a long query; a context that ends
		// kills it on the server.
		start := time.Now()
		stats, _ = e.Query(t.Context(), tg, data.Call{}, apitypes.DataQueryRequest{SQL: "SELECT COUNT(*) FROM information_schema.COLUMNS a, information_schema.COLUMNS b, information_schema.COLUMNS c", TimeoutSeconds: 1},
			func(apitypes.DataQueryFrame) error { return nil })
		if stats.Outcome != apitypes.OutcomeTimeout || time.Since(start) > 4*time.Second {
			t.Errorf("timeout: %+v after %s", stats, time.Since(start))
		}
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		start = time.Now()
		stats, _ = e.Query(ctx, tg, data.Call{}, apitypes.DataQueryRequest{SQL: "SELECT SLEEP(20)"}, func(apitypes.DataQueryFrame) error { return nil })
		if time.Since(start) > 4*time.Second || stats.Outcome == apitypes.OutcomeOK && stats.Rows == 0 {
			t.Errorf("cancel: %+v after %s", stats, time.Since(start))
		}
		// The connections still work afterwards.
		if f := query(t, e, tg, data.Call{}, "SELECT 1"); f[len(f)-1].Type != apitypes.FrameEnd {
			t.Errorf("after cancel: %+v", f)
		}
	})
}

func TestMasked(t *testing.T) {
	each(t, func(t *testing.T, e *Engine, tg data.Target) {
		sens := []apitypes.DataColumnRef{{Schema: "app", Table: "customers", Column: "email"}}
		if _, err := e.SyncGrants(t.Context(), tg, owner, sens); err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = e.SyncGrants(context.Background(), tg, owner, nil) }()
		c := data.Call{Masked: true, Sensitive: sens}
		p, err := e.Page(t.Context(), tg, c, apitypes.DataPageRequest{Table: "app.customers", Limit: 1})
		if err != nil || string(p.Rows[0]) != `[1,"Ada",{"masked":true},"h1"]` {
			t.Fatalf("masked page: %s %v", p.Rows, err)
		}
		if _, err := e.Page(t.Context(), tg, c, apitypes.DataPageRequest{Table: "app.customers", Order: "email"}); !errors.Is(err, data.ErrInvalid) {
			t.Errorf("sort by a masked column: %v", err)
		}
		if f := query(t, e, tg, c, "SELECT email FROM customers"); f[len(f)-1].Type != apitypes.FrameError {
			t.Errorf("masked SQL read email: %+v", f)
		}
		if f := query(t, e, tg, c, "SELECT id, name FROM customers"); f[len(f)-1].Type != apitypes.FrameEnd {
			t.Errorf("masked SQL: %+v", f)
		}
		// A view runs as its definer: while a column is sensitive, masked
		// callers read no view.
		if f := query(t, e, tg, c, "SELECT * FROM cheap"); f[len(f)-1].Type != apitypes.FrameError {
			t.Errorf("masked SQL read a view: %+v", f)
		}
		if _, err := e.Page(t.Context(), tg, c, apitypes.DataPageRequest{Table: "app.cheap", Limit: 1}); !errors.Is(err, data.ErrInvalid) {
			t.Errorf("masked page of a view: %v", err)
		}
	})
}

func TestCommit(t *testing.T) {
	each(t, func(t *testing.T, e *Engine, tg data.Target) {
		ctx := t.Context()
		s := func(v string) *string { return &v }
		ownerExec(t, e, tg, "INSERT INTO customers VALUES (40, 'Tmp', 't@x.io', NULL)")
		defer ownerExec(t, e, tg, "DELETE FROM customers WHERE id >= 40")

		update := apitypes.DataChange{Op: apitypes.DataChangeUpdate, Table: "app.customers", Key: map[string]string{"id": "40"},
			Old: map[string]*string{"name": s("Tmp"), "password_hash": nil}, New: map[string]*string{"name": s("Tím 'q'"), "password_hash": s(`a\b`)}}
		sql, err := e.Render(ctx, tg, []apitypes.DataChange{update})
		if err != nil || !strings.Contains(sql, "`password_hash` <=> NULL") || !strings.Contains(sql, "'Tím ''q'''") || !strings.Contains(sql, "_utf8mb4 X'615c62'") {
			t.Fatalf("render: %s %v", sql, err)
		}
		res, err := e.Commit(ctx, tg, data.Call{Actor: "ada"}, []apitypes.DataChange{update,
			{Op: apitypes.DataChangeInsert, Table: "app.customers", New: map[string]*string{"id": s("41"), "name": s("New")}}})
		if err != nil || len(res.Conflicts) != 0 || !slices.Equal(res.RowsAffected, []int64{1, 1}) {
			t.Fatalf("commit: %+v %v", res, err)
		}
		// Setting the value a row already has still matches it.
		same := apitypes.DataChange{Op: apitypes.DataChangeUpdate, Table: "app.customers", Key: map[string]string{"id": "41"},
			Old: map[string]*string{"name": s("New")}, New: map[string]*string{"name": s("New")}}
		if res, err := e.Commit(ctx, tg, data.Call{}, []apitypes.DataChange{same}); err != nil || len(res.Conflicts) != 0 {
			t.Fatalf("same value: %+v %v", res, err)
		}

		// A stale old value, a missing row and a failing statement: each a
		// conflict, and nothing of the changeset is written.
		stale := apitypes.DataChange{Op: apitypes.DataChangeUpdate, Table: "app.customers", Key: map[string]string{"id": "40"},
			Old: map[string]*string{"name": s("Tmp")}, New: map[string]*string{"name": s("X")}}
		del := apitypes.DataChange{Op: apitypes.DataChangeDelete, Table: "app.customers", Key: map[string]string{"id": "41"}}
		res, err = e.Commit(ctx, tg, data.Call{}, []apitypes.DataChange{del, stale})
		if err != nil || len(res.Conflicts) != 1 || res.Conflicts[0].Reason != apitypes.ConflictChanged || *res.Conflicts[0].Current["name"] != "Tím 'q'" {
			t.Fatalf("stale: %+v %v", res, err)
		}
		gone := apitypes.DataChange{Op: apitypes.DataChangeDelete, Table: "app.customers", Key: map[string]string{"id": "99"}}
		if res, _ := e.Commit(ctx, tg, data.Call{}, []apitypes.DataChange{gone}); len(res.Conflicts) != 1 || res.Conflicts[0].Reason != apitypes.ConflictMissing {
			t.Fatalf("missing: %+v", res)
		}
		neg := apitypes.DataChange{Op: apitypes.DataChangeUpdate, Table: "app.items", Key: map[string]string{"id": "1"},
			Old: map[string]*string{"qty": s("3")}, New: map[string]*string{"qty": s("-1")}}
		if res, _ := e.Commit(ctx, tg, data.Call{}, []apitypes.DataChange{del, neg}); len(res.Conflicts) != 1 || res.Conflicts[0].Reason != apitypes.ConflictFailed {
			t.Fatalf("failed: %+v", res)
		}
		if got := walk(t, e, tg, apitypes.DataPageRequest{Table: "app.customers", Filters: []apitypes.DataFilter{{Column: "id", Op: apitypes.FilterEq, Value: "41"}}}); len(got) != 1 {
			t.Errorf("the delete of a rolled back changeset happened: %v", got)
		}
		for _, bad := range []apitypes.DataChange{
			{Op: apitypes.DataChangeUpdate, Table: "app.items", Key: map[string]string{"id": "1"}, Old: map[string]*string{"total": s("4.50")}, New: map[string]*string{"total": s("1")}},
			{Op: apitypes.DataChangeDelete, Table: "app.logs", Key: map[string]string{"msg": "a"}},
		} {
			if _, err := e.Render(ctx, tg, []apitypes.DataChange{bad}); !errors.Is(err, data.ErrInvalid) {
				t.Errorf("%+v: %v", bad, err)
			}
		}
	})
}
