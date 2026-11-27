//go:build integration

package mongo_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/mongo"
	"github.com/NordTempo/tempoDock/internal/data/mongo/mongotest"
	"github.com/NordTempo/tempoDock/internal/data/mongo/wire"
)

var server *mongotest.Server

func TestMain(m *testing.M) {
	var err error
	if server, err = mongotest.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	server.Stop()
	os.Exit(code)
}

var owner = data.Credentials{User: mongotest.Root, Password: mongotest.Password}

// setUp makes a target with Dock's users set up, and a collection of
// orders in its own database.
func setUp(t *testing.T, e *mongo.Engine, id int64) data.Target {
	t.Helper()
	ctx := t.Context()
	tg := data.Target{
		ServiceID: id, Dial: server.Dial, Database: "app",
		Reader: data.Credentials{User: "dock_reader", Password: "reader-pw"},
		Writer: data.Credentials{User: "dock_writer", Password: "writer-pw"},
	}
	if _, err := e.Setup(ctx, tg, owner, tg.Reader, tg.Writer); err != nil {
		t.Fatal(err)
	}
	c, err := server.Connect(ctx, mongotest.Root, mongotest.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	docs, err := wire.ParseJSON(`[
		{_id: 1, status: "open", total: NumberDecimal("249.00"), customer: {email: "ada@example.com", name: "Ada"}},
		{_id: 2, status: "paid", total: NumberDecimal("10.50"), customer: {email: "bob@example.com", name: "Bob"}, coupon: "X"},
		{_id: 3, status: "open", total: NumberDecimal("99.00"), customer: {email: "cy@example.com", name: "Cy"}}]`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run("app", wire.D{{Key: "drop", Value: "orders"}}); err != nil && !strings.Contains(err.Error(), "NamespaceNotFound") {
		t.Fatal(err)
	}
	if _, err := c.Run("app", wire.D{{Key: "insert", Value: "orders"}, {Key: "documents", Value: docs}}); err != nil {
		t.Fatal(err)
	}
	return tg
}

func cells(t *testing.T, row apitypes.DataRow) []any {
	t.Helper()
	var out []any
	if err := json.Unmarshal(row, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Setup creates the users; the reader browses collections and pages of
// documents, filtered; a second Setup rotates the passwords.
func TestBrowse(t *testing.T) {
	e := mongo.New(mongo.Options{})
	defer e.Close()
	tg := setUp(t, e, 1)
	ctx := t.Context()
	info, err := e.Status(ctx, tg)
	if err != nil || !strings.HasPrefix(info.Version, "MongoDB 8.") {
		t.Fatalf("status %+v, %v", info, err)
	}
	s, err := e.Schema(ctx, tg, data.Call{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var orders *apitypes.DataTable
	for i := range s.Tables {
		if s.Tables[i].Schema == "app" && s.Tables[i].Name == "orders" {
			orders = &s.Tables[i]
		}
	}
	if orders == nil || orders.EstimatedRows != 3 || !orders.Editable || len(orders.Columns) != 5 || orders.Columns[4].Name != "coupon" {
		t.Fatalf("orders in the schema: %+v", orders)
	}
	page, err := e.Page(ctx, tg, data.Call{}, apitypes.DataPageRequest{
		Table: "app.orders", Limit: 1, Order: "-total", Count: true,
		Filters: []apitypes.DataFilter{{Column: "status", Op: apitypes.FilterEq, Value: "open"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 || page.Next == "" || page.ExactRows == nil || *page.ExactRows != 2 {
		t.Fatalf("page: %+v", page)
	}
	row := cells(t, page.Rows[0])
	if row[0] != `{"$numberInt":"1"}` || row[2] != `{"$numberDecimal":"249.00"}` {
		t.Fatalf("first open order by total, descending: %v", row)
	}
	next, err := e.Page(ctx, tg, data.Call{}, apitypes.DataPageRequest{
		Table: "app.orders", Limit: 1, Order: "-total", After: page.Next,
		Filters: []apitypes.DataFilter{{Column: "status", Op: apitypes.FilterEq, Value: "open"}},
	})
	if err != nil || len(next.Rows) != 1 || cells(t, next.Rows[0])[0] != `{"$numberInt":"3"}` || next.Next != "" {
		t.Fatalf("second page: %+v, %v", next, err)
	}

	// Masked: the field holding a sensitive path shows it masked inside,
	// and can't be filtered on.
	masked := data.Call{Masked: true, Sensitive: []apitypes.DataColumnRef{{Schema: "app", Table: "orders", Column: "customer.email"}}}
	mp, err := e.Page(ctx, tg, masked, apitypes.DataPageRequest{Table: "app.orders", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(mp.Rows) != 1 {
		t.Fatalf("masked page: %+v", mp)
	}
	var customer any
	if r := cells(t, mp.Rows[0]); len(r) > 3 {
		customer = r[3]
	}
	if s, isStr := customer.(string); !isStr || strings.Contains(s, "ada@") || !strings.Contains(s, `"email":{"$masked":true}`) {
		t.Fatalf("masked customer: %v", customer)
	}
	_, err = e.Page(ctx, tg, masked, apitypes.DataPageRequest{Table: "app.orders", Filters: []apitypes.DataFilter{{Column: "customer.email", Op: apitypes.FilterLike, Value: "ada%"}}})
	if !errors.Is(err, data.ErrInvalid) {
		t.Fatalf("filtering on a masked field: %v", err)
	}

	// The writer's old password stops working after a new setup.
	rotated := tg
	rotated.Reader.Password, rotated.Writer.Password = "reader-2", "writer-2"
	if _, err := e.Setup(ctx, rotated, owner, rotated.Reader, rotated.Writer); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Status(ctx, tg); !errors.Is(err, data.ErrNotSetUp) {
		t.Fatalf("the old reader password: %v", err)
	}
	if _, err := e.Status(ctx, rotated); err != nil {
		t.Fatal(err)
	}
	// The writer's too: its old password logs in no more, the new one does.
	if _, err := server.Connect(ctx, tg.Writer.User, tg.Writer.Password); err == nil {
		t.Fatal("the old writer password still logs in")
	}
	if c, err := server.Connect(ctx, rotated.Writer.User, rotated.Writer.Password); err != nil {
		t.Fatalf("the new writer password: %v", err)
	} else {
		_ = c.Close()
	}
}

// The console runs reads in the shell's words, refuses what writes, and
// the reader couldn't write anyway.
func TestConsole(t *testing.T) {
	e := mongo.New(mongo.Options{})
	defer e.Close()
	tg := setUp(t, e, 2)
	query := func(text string) (cols []string, rows [][]any, errMsg string) {
		t.Helper()
		_, err := e.Query(t.Context(), tg, data.Call{}, apitypes.DataQueryRequest{SQL: text}, func(f apitypes.DataQueryFrame) error {
			for _, c := range f.Columns {
				cols = append(cols, c.Name)
			}
			for _, r := range f.Rows {
				rows = append(rows, cells(t, r))
			}
			if f.Error != nil {
				errMsg = f.Error.Message
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return cols, rows, errMsg
	}
	if _, rows, msg := query(`db.orders.find({status: 'open'}, {status: 1}).sort({_id: -1}).limit(5)`); msg != "" || len(rows) != 2 || len(rows[0]) == 0 || rows[0][0] != `{"$numberInt":"3"}` {
		t.Fatalf("find: %v %s", rows, msg)
	}
	if _, rows, msg := query(`db.orders.aggregate([{$group: {_id: "$status", n: {$sum: 1}}}, {$sort: {_id: 1}}])`); msg != "" || len(rows) != 2 || len(rows[0]) == 0 || rows[0][0] != `"open"` {
		t.Fatalf("aggregate: %v %s", rows, msg)
	}
	if _, rows, msg := query(`db.orders.countDocuments({status: "open"})`); msg != "" || len(rows) != 1 || len(rows[0]) == 0 || rows[0][0] != float64(2) {
		t.Fatalf("count: %v %s", rows, msg)
	}
	if _, _, msg := query(`db.orders.aggregate([{$out: "copy"}])`); !strings.Contains(msg, "$out isn't allowed") {
		t.Fatalf("$out: %s", msg)
	}
	if _, _, msg := query(`db.orders.find({$where: "sleep(1000)"})`); !strings.Contains(msg, "$where isn't allowed") {
		t.Fatalf("$where: %s", msg)
	}
	if _, _, msg := query(`db.orders.drop()`); !strings.Contains(msg, "isn't a read") {
		t.Fatalf("drop: %s", msg)
	}
	if got := e.Redact(`db.orders.find({"email": "ada@example.com", n: 42})`); got != `db.orders.find({"email": ?, n: ?})` {
		t.Fatalf("redacted: %s", got)
	}
}

// A change is one document's: it applies when the document still holds
// the old values, and is a conflict with the current values when not.
func TestChanges(t *testing.T) {
	e := mongo.New(mongo.Options{})
	defer e.Close()
	tg := setUp(t, e, 3)
	ctx := t.Context()
	str := func(s string) *string { return &s }
	upd := apitypes.DataChange{
		Op: apitypes.DataChangeUpdate, Table: "app.orders", Key: map[string]string{"_id": "1"},
		Old: map[string]*string{"status": str(`"open"`), "note": nil}, New: map[string]*string{"status": str(`"paid"`), "note": str(`"ok"`)},
	}
	text, err := e.Render(ctx, tg, []apitypes.DataChange{upd})
	if err != nil || !strings.Contains(text, `.updateOne({"_id":{"$eq":{"$numberInt":"1"}},"note":{"$exists":false},"$expr":{"$and":[{"$eq":["$status",{"$literal":"open"}]},{"$eq":[{"$type":"$status"},"string"]}]}}, {"$set":{"note":"ok","status":"paid"}}, {"collation":{"locale":"simple"}})`) {
		t.Fatalf("render: %s, %v", text, err)
	}
	res, err := e.Commit(ctx, tg, data.Call{}, []apitypes.DataChange{upd})
	if err != nil || len(res.Conflicts) != 0 || len(res.RowsAffected) != 1 {
		t.Fatalf("commit: %+v, %v", res, err)
	}
	// The same change again: status is paid now.
	res, err = e.Commit(ctx, tg, data.Call{}, []apitypes.DataChange{upd})
	if err != nil || len(res.Conflicts) != 1 || res.Conflicts[0].Reason != apitypes.ConflictChanged ||
		res.Conflicts[0].Current["status"] == nil || *res.Conflicts[0].Current["status"] != `"paid"` {
		t.Fatalf("a stale change: %+v, %v", res, err)
	}
	del := apitypes.DataChange{Op: apitypes.DataChangeDelete, Table: "app.orders", Key: map[string]string{"_id": "9"}}
	if res, err := e.Commit(ctx, tg, data.Call{}, []apitypes.DataChange{del}); err != nil || len(res.Conflicts) != 1 || res.Conflicts[0].Reason != apitypes.ConflictMissing {
		t.Fatalf("delete of a missing document: %+v, %v", res, err)
	}
	ins := apitypes.DataChange{Op: apitypes.DataChangeInsert, Table: "app.orders", New: map[string]*string{"_id": str("10"), "status": str(`"new"`)}}
	if _, err := e.Commit(ctx, tg, data.Call{}, []apitypes.DataChange{ins, del}); !errors.Is(err, data.ErrInvalid) {
		t.Fatalf("two documents at once: %v", err)
	}
	if res, err := e.Commit(ctx, tg, data.Call{}, []apitypes.DataChange{ins}); err != nil || len(res.Conflicts) != 0 || len(res.RowsAffected) != 1 || res.RowsAffected[0] != 1 {
		t.Fatalf("insert: %+v, %v", res, err)
	}
	masked := data.Call{Masked: true, Sensitive: []apitypes.DataColumnRef{{Schema: "app", Table: "orders", Column: "customer.email"}}}
	edit := apitypes.DataChange{Op: apitypes.DataChangeUpdate, Table: "app.orders", Key: map[string]string{"_id": "2"},
		Old: map[string]*string{"customer": str(`{}`)}, New: map[string]*string{"customer": str(`{}`)}}
	if _, err := e.Commit(context.Background(), tg, masked, []apitypes.DataChange{edit}); !errors.Is(err, data.ErrInvalid) {
		t.Fatalf("changing a field that holds a masked one: %v", err)
	}
}

// A page's find filter ANDs with its filters, and can't reach a masked
// field, not even through $expr.
func TestWhere(t *testing.T) {
	e := mongo.New(mongo.Options{})
	defer e.Close()
	tg := setUp(t, e, 4)
	ctx := t.Context()
	page, err := e.Page(ctx, tg, data.Call{}, apitypes.DataPageRequest{
		Table: "app.orders", Where: `{$or: [{status: "paid"}, {total: {$gt: NumberDecimal("100")}}]}`,
		Filters: []apitypes.DataFilter{{Column: "customer.name", Op: apitypes.FilterNe, Value: "Bob"}},
	})
	if err != nil || len(page.Rows) != 1 || cells(t, page.Rows[0])[0] != `{"$numberInt":"1"}` {
		t.Fatalf("where and filters: %+v, %v", page, err)
	}
	masked := data.Call{Masked: true, Sensitive: []apitypes.DataColumnRef{{Schema: "app", Table: "orders", Column: "customer.email"}}}
	for _, w := range []string{`{"customer.email": {$regex: "a"}}`, `{customer: {$elemMatch: {email: "x"}}}`, `{$expr: {$eq: ["$customer.email", "x"]}}`, `{$where: "true"}`} {
		if _, err := e.Page(ctx, tg, masked, apitypes.DataPageRequest{Table: "app.orders", Where: w}); !errors.Is(err, data.ErrInvalid) {
			t.Errorf("%s while masked: %v", w, err)
		}
	}
}

// A console find longer than its first batch comes whole (getMore), and
// an edit guarded by a value the field still holds inside an array is a
// conflict, not a write.
func TestBatchesAndExactGuards(t *testing.T) {
	e := mongo.New(mongo.Options{})
	defer e.Close()
	tg := setUp(t, e, 5)
	ctx := t.Context()
	c, err := server.Connect(ctx, mongotest.Root, mongotest.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	many := wire.A{}
	for i := range 250 {
		many = append(many, wire.D{{Key: "_id", Value: int32(1000 + i)}})
	}
	if _, err := c.Run("app", wire.D{{Key: "insert", Value: "orders"}, {Key: "documents", Value: many}}); err != nil {
		t.Fatal(err)
	}
	var rows int64
	var end *apitypes.DataQueryFrame
	_, err = e.Query(ctx, tg, data.Call{}, apitypes.DataQueryRequest{SQL: `db.orders.find({_id: {$gte: 1000}})`}, func(f apitypes.DataQueryFrame) error {
		rows += int64(len(f.Rows))
		if f.Type == apitypes.FrameEnd || f.Type == apitypes.FrameError {
			end = &f
		}
		return nil
	})
	if err != nil || end == nil || end.Type != apitypes.FrameEnd || rows != 250 {
		t.Fatalf("a find of 250: %d rows, end %+v, %v", rows, end, err)
	}

	if _, err := c.Run("app", wire.D{{Key: "update", Value: "orders"}, {Key: "updates", Value: wire.A{wire.D{
		{Key: "q", Value: wire.D{{Key: "_id", Value: int32(2)}}}, {Key: "u", Value: wire.D{{Key: "$set", Value: wire.D{{Key: "status", Value: wire.A{"paid", "refunded"}}}}}},
	}}}}); err != nil {
		t.Fatal(err)
	}
	old, next := `"paid"`, `"done"`
	res, err := e.Commit(ctx, tg, data.Call{}, []apitypes.DataChange{{
		Op: apitypes.DataChangeUpdate, Table: "app.orders", Key: map[string]string{"_id": "2"},
		Old: map[string]*string{"status": &old}, New: map[string]*string{"status": &next},
	}})
	if err != nil || len(res.Conflicts) != 1 || res.Conflicts[0].Reason != apitypes.ConflictChanged {
		t.Fatalf("an edit of a field that became an array: %+v, %v", res, err)
	}
	// int32 7 became int64 7: $eq would take it, the type guard doesn't.
	if _, err := c.Run("app", wire.D{{Key: "update", Value: "orders"}, {Key: "updates", Value: wire.A{wire.D{
		{Key: "q", Value: wire.D{{Key: "_id", Value: int32(3)}}}, {Key: "u", Value: wire.D{{Key: "$set", Value: wire.D{{Key: "qty", Value: int64(7)}}}}},
	}}}}); err != nil {
		t.Fatal(err)
	}
	seen, want := `{"$numberInt":"7"}`, `{"$numberInt":"8"}`
	res, err = e.Commit(ctx, tg, data.Call{}, []apitypes.DataChange{{
		Op: apitypes.DataChangeUpdate, Table: "app.orders", Key: map[string]string{"_id": "3"},
		Old: map[string]*string{"qty": &seen}, New: map[string]*string{"qty": &want},
	}})
	if err != nil || len(res.Conflicts) != 1 || res.Conflicts[0].Reason != apitypes.ConflictChanged {
		t.Fatalf("an int32 guard on an int64 value: %+v, %v", res, err)
	}
	long := `{"$numberLong":"7"}`
	res, err = e.Commit(ctx, tg, data.Call{}, []apitypes.DataChange{{
		Op: apitypes.DataChangeUpdate, Table: "app.orders", Key: map[string]string{"_id": "3"},
		Old: map[string]*string{"qty": &long}, New: map[string]*string{"qty": &want},
	}})
	if err != nil || len(res.Conflicts) != 0 || len(res.RowsAffected) != 1 {
		t.Fatalf("an unchanged int64: %+v, %v", res, err)
	}
	// An _id shaped like an operator is a literal key, so it matches no
	// document.
	del := apitypes.DataChange{Op: apitypes.DataChangeDelete, Table: "app.orders", Key: map[string]string{"_id": `{$ne: null}`}}
	if res, err := e.Commit(ctx, tg, data.Call{}, []apitypes.DataChange{del}); err != nil || len(res.Conflicts) != 1 || res.Conflicts[0].Reason != apitypes.ConflictMissing {
		t.Fatalf("an operator-shaped _id: %+v, %v", res, err)
	}
}
