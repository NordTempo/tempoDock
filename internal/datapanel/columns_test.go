package datapanel

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A MongoDB mark may name a path inside a column; other engines' marks
// name a column exactly.
func TestColumnsExistPaths(t *testing.T) {
	s := apitypes.DataSchema{Tables: []apitypes.DataTable{{Schema: "app", Name: "orders", Columns: []apitypes.DataColumn{{Name: "customer"}}}}}
	nested := []apitypes.DataColumnRef{{Schema: "app", Table: "orders", Column: "customer.email"}}
	if err := columnsExist(s, nested, true); err != nil {
		t.Errorf("a path inside a column: %v", err)
	}
	if err := columnsExist(s, nested, false); err == nil {
		t.Error("a path where columns are exact")
	}
	if err := columnsExist(s, []apitypes.DataColumnRef{{Schema: "app", Table: "orders", Column: "billing.email"}}, true); err == nil {
		t.Error("a path inside a missing column")
	}
}

// With paths, a changeset's field that holds a marked path (or sits
// under one) is masked with its conflict values; without, only the
// exact column is.
func TestMaskChangesPaths(t *testing.T) {
	email := `"a@x"`
	ch := []apitypes.DataChange{{Table: "app.orders", New: map[string]*string{"customer": &email, "total": &email}}}
	cf := []apitypes.DataConflict{{Change: 0, Current: map[string]*string{"customer": &email}}}
	marks := []apitypes.DataColumnRef{{Schema: "app", Table: "orders", Column: "customer.email"}}
	out, conflicts, touched := maskChanges(ch, cf, marks, true)
	if !touched || out[0].New["customer"] != nil || out[0].New["total"] == nil || conflicts[0].Current["customer"] != nil {
		t.Fatalf("masked: %+v %+v", out, conflicts)
	}
	if ch[0].New["customer"] == nil {
		t.Fatal("the input was changed")
	}
	if _, _, touched := maskChanges(ch, cf, marks, false); touched {
		t.Fatal("an exact-column engine masked a parent")
	}
}
