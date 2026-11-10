package pg

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

func TestAppendRow(t *testing.T) {
	row := appendRow(nil, [][]byte{[]byte("1"), []byte("x")}, []int{0, -1, 1}, []data.CellKind{data.CellNumber, data.CellMasked, data.CellString}, false)
	if string(row) != `[1,{"masked":true},"x"]` {
		t.Errorf("row: %s", row)
	}
}

func TestCheckReadStatement(t *testing.T) {
	ok := []string{
		"SELECT 1",
		"select 1;",
		"SELECT 1; -- done\n/* really */ ;",
		"  (SELECT 1) UNION (SELECT 2)",
		"WITH x AS (SELECT ';') SELECT * FROM x",
		"SELECT $$a;b$$, $t$ ; $t$, E'\\';', 'it''s;', \"we;ird\" FROM t",
		"/* a /* nested; */ comment */ VALUES (1)",
		"-- comment\nexplain select 1",
		"SELECT a$1 FROM t; ",
	}
	for _, sql := range ok {
		if err := checkReadStatement(sql); err != nil {
			t.Errorf("%q: %v", sql, err)
		}
	}
	bad := []string{
		"", "  ;", "-- only a comment",
		"SELECT 1; SELECT 2",
		"SELECT 'a'; DELETE FROM t",
		"SELECT $$;$$; DROP TABLE t",
		"DELETE FROM t",
		"SET statement_timeout = 0",
		"PREPARE p AS SELECT 1",
		"SELECT 'unterminated",
		"SELECT /* open",
		"SELECT pg_catalog.pg_advisory_lock(1)",
		`SELECT "pg_try_advisory_lock_shared"(1)`,
		"SELECT PG_LOGICAL_EMIT_MESSAGE(false, 'p', 'x')",
		"SELECT ts_stat('SELECT 1')",
		"SELECT dblink('dbname=app', 'SELECT 1')",
		"SELECT public.dblink_exec('dbname=app', 'DROP TABLE t')",
		`SELECT "dblink_connect"('c', 'dbname=app')`,
		"SELECT DBLINK_SEND_QUERY('c', 'DELETE FROM t')",
		"SELECT pg_terminate_backend(42)",
		"SELECT pg_background_launch('DELETE FROM t')",
		"SELECT pg_logical_slot_get_changes('s', NULL, NULL)",
		"SELECT pg_create_logical_replication_slot('s', 'pgoutput')",
		"SELECT pg_file_write('x', 'y', false)",
		`SELECT u&"x"`,
	}
	for _, sql := range bad {
		if err := checkReadStatement(sql); !errors.Is(err, data.ErrInvalid) {
			t.Errorf("%q: %v", sql, err)
		}
	}
}

// TestRedact: every literal form becomes ?, comments go, and
// identifiers, keywords and parameters stay.
func TestRedact(t *testing.T) {
	cases := map[string]string{
		"SELECT * FROM t WHERE email = 'a@b.c' AND n > 42.5e-3":                       "SELECT * FROM t WHERE email = ? AND n > ?",
		`UPDATE "t1" SET "x" = E'it\'s' WHERE id = $1 AND b = B'101'`:                 `UPDATE "t1" SET "x" = ? WHERE id = $1 AND b = ?`,
		"SELECT $$secret$$, $q$also$q$, U&'d\\0061ta', x'ff', 1_000 -- pw: hunter2\n": "SELECT ?, ?, ?, ?, ?  ",
		"SELECT a /* token 123 */ FROM t2.c3":                                         "SELECT a   FROM t2.c3",
		"SELECT 'unterminated":                                                        "SELECT ?",
	}
	for in, want := range cases {
		if got := redact(in); got != want {
			t.Errorf("redact(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestLiterals(t *testing.T) {
	if got := arrayLiteral([]string{"a", `b"c`, `d\e`, "NULL", ""}); got != `{"a","b\"c","d\\e","NULL",""}` {
		t.Errorf("array: %s", got)
	}
	if got := quoteLiteral("it's"); got != `'it''s'` {
		t.Errorf("literal: %s", got)
	}
	if got := quoteLiteral(`a\'b`); got != `E'a\\''b'` {
		t.Errorf("escaped literal: %s", got)
	}
	if got := appName("dock", "bo'b\\ é"); got != "dock:bo_b_ __" {
		t.Errorf("app name: %q", got)
	}
	if got := len(appName("dock", strings.Repeat("x", 100))); got != 63 {
		t.Errorf("app name length %d", got)
	}
}

// testCatalog is a small catalog in catalogSQL's shape.
func testCatalog(t *testing.T) *catalog {
	t.Helper()
	doc := `[
 {"schema":"public","name":"orders","kind":"table","estimated_rows":10,
  "columns":[
   {"name":"id","type":"integer","type_name":"int4","nullable":false},
   {"name":"status","type":"text","type_name":"text","nullable":true},
   {"name":"meta","type":"json","type_name":"json","nullable":true},
   {"name":"total","type":"numeric","type_name":"numeric","nullable":true,"generated":true},
   {"name":"customer_id","type":"integer","type_name":"int4","nullable":false}],
  "primary_key":["id"],
  "foreign_keys":[{"name":"orders_customer_fk","columns":["customer_id"],"ref_schema":"public","ref_table":"customers","ref_columns":["id"]}],
  "idx":[{"name":"orders_pkey","unique":true,"primary":true,"definition":"...","cols":["id"]}]},
 {"schema":"public","name":"customers","kind":"table","estimated_rows":-1,
  "columns":[{"name":"id","type":"integer","type_name":"int4","nullable":false}],"primary_key":["id"]},
 {"schema":"public","name":"codes","kind":"table","estimated_rows":-1,
  "columns":[{"name":"code","type":"text","type_name":"text","nullable":false},{"name":"x","type":"text","type_name":"text","nullable":true}],
  "primary_key":[],
  "idx":[{"name":"codes_x","unique":true,"cols":["x"]},{"name":"codes_code","unique":true,"cols":["code"]}]},
 {"schema":"a.b","name":"c.d","kind":"view","estimated_rows":-1,"columns":[{"name":"v","type":"text","type_name":"text","nullable":true}]}
]`
	cat, err := parseCatalog([]byte(doc), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func TestCatalog(t *testing.T) {
	cat := testCatalog(t)
	orders, err := cat.table("public.orders")
	if err != nil || !orders.Editable || orders.Indexes == nil || orders.Constraints == nil {
		t.Fatalf("orders: %+v %v", orders, err)
	}
	codes, _ := cat.table("public.codes")
	if codes.Editable || len(codes.key) != 1 || codes.key[0] != "code" {
		t.Fatalf("codes key: %v (a unique index on a nullable column doesn't order pages)", codes.key)
	}
	view, err := cat.table("a.b.c.d")
	if err != nil || view.Name != "c.d" || view.key != nil || view.sysKey != nil {
		t.Fatalf("dotted view: %+v %v", view, err)
	}
	if in := cat.incoming[tableKey("public", "customers")]; len(in) != 1 || in[0].from != orders {
		t.Fatalf("incoming: %+v", in)
	}
	if _, err := cat.table("public.nope"); !errors.Is(err, data.ErrNoTable) {
		t.Fatal(err)
	}
}

func TestPlanPage(t *testing.T) {
	cat := testCatalog(t)
	orders, _ := cat.table("public.orders")

	plan, err := planPage(orders, apitypes.DataPageRequest{
		Table: "public.orders",
		Order: "-status",
		Filters: []apitypes.DataFilter{
			{Column: "status", Op: apitypes.FilterIn, Values: []string{"paid", "new"}},
			{Column: "meta", Op: apitypes.FilterEq, Value: "{}"},
			{Column: "customer_id", Op: apitypes.FilterILike, Value: "1%"},
		},
	}, map[string]bool{"total": true})
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT "id", "status", "meta", "customer_id" FROM "public"."orders" WHERE "status" = ANY($1) AND "meta"::text = $2 AND "customer_id"::text ILIKE $3 ORDER BY "status" DESC NULLS FIRST, "id" DESC LIMIT $4`
	if plan.sql != want {
		t.Fatalf("sql\n got %s\nwant %s", plan.sql, want)
	}
	if plan.args[0] != `{"paid","new"}` || plan.args[3] != int64(101) || plan.src[3] != -1 || plan.kinds[3] != data.CellMasked {
		t.Fatalf("plan: %+v", plan)
	}
	if plan.countSQL != `SELECT count(*) FROM "public"."orders" WHERE "status" = ANY($1) AND "meta"::text = $2 AND "customer_id"::text ILIKE $3` || len(plan.countArgs) != 3 {
		t.Fatalf("count: %s %v", plan.countSQL, plan.countArgs)
	}

	// The next page continues from a cursor.
	paid, id := "paid", "7"
	after := encodeCursor(cursor{C: plan.check, K: []*string{&paid, &id}})
	plan2, err := planPage(orders, apitypes.DataPageRequest{Table: "public.orders", Order: "-status", After: after,
		Filters: []apitypes.DataFilter{
			{Column: "status", Op: apitypes.FilterIn, Values: []string{"paid", "new"}},
			{Column: "meta", Op: apitypes.FilterEq, Value: "{}"},
			{Column: "customer_id", Op: apitypes.FilterILike, Value: "1%"},
		}}, map[string]bool{"total": true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan2.sql, `AND (("status", "id") < ($4, $5)) ORDER BY`) {
		t.Fatalf("keyset: %s", plan2.sql)
	}

	codes, _ := cat.table("public.codes")
	plan3, _ := planPage(codes, apitypes.DataPageRequest{Table: "public.codes", Limit: 5}, nil)
	if plan3.sql != `SELECT "code", "x" FROM "public"."codes" ORDER BY "code" LIMIT $1` {
		t.Fatalf("unique key order: %s", plan3.sql)
	}

	for _, req := range []apitypes.DataPageRequest{
		{Order: "nope"},
		{Filters: []apitypes.DataFilter{{Column: "status", Op: "regex", Value: "x"}}},
		{Filters: []apitypes.DataFilter{{Column: "status", Op: apitypes.FilterIn}}},
		{Filters: []apitypes.DataFilter{{Column: "status", Op: apitypes.FilterEq, Values: []string{"a"}}}},
		{Filters: []apitypes.DataFilter{{Column: "status", Op: apitypes.FilterNotNull, Value: "a"}}},
		{After: after}, // cursor of another order and filter
		{Limit: apitypes.DataPageMaxLimit + 1},
	} {
		req.Table = "public.orders"
		if _, err := planPage(orders, req, nil); !errors.Is(err, data.ErrInvalid) {
			t.Errorf("%+v: %v", req, err)
		}
	}
}

func TestKeysetPredicate(t *testing.T) {
	s := func(v string) *string { return &v }
	cols := []orderCol{{expr: `"name"`, nullable: true}, {expr: `"id"`}}
	cases := []struct {
		vals []*string
		desc bool
		want string
	}{
		{[]*string{s("b"), s("3")}, false, `(("name", "id") > ($1, $2) OR "name" IS NULL)`},
		{[]*string{nil, s("3")}, false, `("name" IS NULL AND ("id") > ($1))`},
		{[]*string{s("b"), s("3")}, true, `(("name", "id") < ($1, $2))`},
		{[]*string{nil, s("3")}, true, `(("name" IS NULL AND ("id") < ($1)) OR "name" IS NOT NULL)`},
	}
	for _, c := range cases {
		var p params
		got, err := keysetPredicate(cols, c.vals, c.desc, &p)
		if err != nil || got != c.want {
			t.Errorf("%v desc=%v: got %s (%v), want %s", c.vals, c.desc, got, err, c.want)
		}
	}
	var p params
	if _, err := keysetPredicate(cols, []*string{s("b"), nil}, false, &p); !errors.Is(err, data.ErrInvalid) {
		t.Errorf("null key value: %v", err)
	}
}

func TestPlanChanges(t *testing.T) {
	cat := testCatalog(t)
	s := func(v string) *string { return &v }
	plans, err := planChanges(cat, []apitypes.DataChange{
		{Op: apitypes.DataChangeInsert, Table: "public.orders", New: map[string]*string{"status": s("new"), "id": s("9"), "meta": nil}},
		{Op: apitypes.DataChangeUpdate, Table: "public.orders", Key: map[string]string{"id": "1"},
			Old: map[string]*string{"status": nil, "meta": s(`{"a": 1}`)}, New: map[string]*string{"status": s("paid")}},
		{Op: apitypes.DataChangeDelete, Table: "public.orders", Key: map[string]string{"id": "2"}},
		{Op: apitypes.DataChangeInsert, Table: "public.customers"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `BEGIN;
INSERT INTO "public"."orders" ("id", "status", "meta") VALUES ('9', 'new', NULL);
UPDATE "public"."orders" SET "status" = 'paid' WHERE "id" = '1' AND "status" IS NULL AND "meta"::text = '{"a": 1}';
DELETE FROM "public"."orders" WHERE "id" = '2';
INSERT INTO "public"."customers" DEFAULT VALUES;
COMMIT;
`
	if got := renderTx(plans); got != want {
		t.Fatalf("render\n got %s\nwant %s", got, want)
	}
	if plans[1].sql != `UPDATE "public"."orders" SET "status" = $1 WHERE "id" = $2 AND "status" IS NULL AND "meta"::text = $3` || len(plans[1].args) != 3 {
		t.Fatalf("update: %s %v", plans[1].sql, plans[1].args)
	}
	if plans[1].checkSQL != `SELECT "status"::text, "meta"::text FROM "public"."orders" WHERE "id" = $1` || plans[2].checkSQL != `SELECT 1 FROM "public"."orders" WHERE "id" = $1` {
		t.Fatalf("checks: %s / %s", plans[1].checkSQL, plans[2].checkSQL)
	}

	for _, ch := range []apitypes.DataChange{
		{Op: apitypes.DataChangeUpdate, Table: "public.orders", Key: map[string]string{"id": "1"}, New: map[string]*string{"status": s("x")}},
		{Op: apitypes.DataChangeUpdate, Table: "public.orders", Key: map[string]string{"id": "1"}, Old: map[string]*string{"total": nil}, New: map[string]*string{"total": s("1")}},
		{Op: apitypes.DataChangeUpdate, Table: "public.orders", Key: map[string]string{"status": "1"}, Old: map[string]*string{"status": nil}, New: map[string]*string{"status": s("x")}},
		{Op: apitypes.DataChangeDelete, Table: "public.orders", Key: map[string]string{"id": "1"}, New: map[string]*string{"status": nil}},
		{Op: apitypes.DataChangeInsert, Table: "public.orders", Key: map[string]string{"id": "1"}},
		{Op: apitypes.DataChangeDelete, Table: "public.codes", Key: map[string]string{"code": "a"}},
		{Op: "upsert", Table: "public.orders"},
	} {
		if _, err := planChanges(cat, []apitypes.DataChange{ch}); !errors.Is(err, data.ErrInvalid) {
			t.Errorf("%+v: %v", ch, err)
		}
	}
	if _, err := planChanges(cat, nil); !errors.Is(err, data.ErrInvalid) {
		t.Error("empty changeset accepted")
	}
}

// TestSetupSQL: setup strips privileges and every role membership (older
// setups made the roles members of pg_read_all_data) and grants none.
func TestSetupSQL(t *testing.T) {
	specs := []roleSpec{{name: data.RoleReader, verifier: "SCRAM-SHA-256$4096:c2FsdA==$a:b", settings: [][2]string{{"default_transaction_read_only", "on"}}}}
	got := strings.Join(setupSQL(specs, map[string]existingRole{
		"dock_reader": {Privileged: true, MemberOf: []string{"pg_read_all_data", "app_owner"}},
	}, `my "db"`), "\n")
	want := `ALTER ROLE "dock_reader" WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT PASSWORD 'SCRAM-SHA-256$4096:c2FsdA==$a:b'
REVOKE "pg_read_all_data" FROM "dock_reader"
REVOKE "app_owner" FROM "dock_reader"
ALTER ROLE "dock_reader" SET default_transaction_read_only = 'on'
GRANT CONNECT ON DATABASE "my ""db""" TO "dock_reader"`
	if got != want {
		t.Fatalf("setup\n got %s\nwant %s", got, want)
	}
	fresh := setupSQL(specs, nil, "postgres")
	if !strings.HasPrefix(fresh[0], `CREATE ROLE "dock_reader" WITH LOGIN NOSUPERUSER`) || len(fresh) != 3 || strings.Contains(strings.Join(fresh, "\n"), "GRANT \"") {
		t.Fatalf("fresh: %q", fresh)
	}
}

func TestScramVerifier(t *testing.T) {
	// Expected value computed independently with Python's hashlib.
	v, err := scramVerifierSalt("pencil", []byte("0123456789abcdef"), 4096)
	want := "SCRAM-SHA-256$4096:MDEyMzQ1Njc4OWFiY2RlZg==$nQpbZ77WudtqufPwikHXGRt6g2QJ4zns8bZLw273DRM=:jn2amWP1q1h+jgjy0YTO14S6/F02SV7taipOeB7ef20="
	if err != nil || v != want {
		t.Fatalf("verifier: %s %v", v, err)
	}
	if _, err := scramVerifier("pässword"); !errors.Is(err, data.ErrInvalid) {
		t.Fatalf("non-ASCII password: %v", err)
	}
}
