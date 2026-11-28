package mongo

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/mongo/wire"
)

// A masked path is reached through array positions too, and the console
// refuses code in a chained projection or sort as in its arguments.
func TestMaskedPathsAndConsoleGuards(t *testing.T) {
	masked := []string{"items.email"}
	for path, want := range map[string]bool{"items.0.email": true, "items.$.email": true, "items": true, "items.0.sku": false, "email": false} {
		if got := touches(masked, path); got != want {
			t.Errorf("touches(%q) = %v", path, got)
		}
	}
	for _, line := range []string{
		`db.orders.find().project({x: {$function: {body: "1", args: [], lang: "js"}}})`,
		`db.orders.find({}, {x: {$accumulator: {}}})`,
	} {
		if _, err := parseStatement(line, "app"); err == nil {
			t.Errorf("%s was allowed", line)
		}
	}
	if _, err := parseStatement(`db.orders.find().project({total: 1}).sort({total: -1})`, "app"); err != nil {
		t.Errorf("a plain projection: %v", err)
	}
}

// A mark with an array position masks the field in every element.
func TestIndexedMarkMasksEveryElement(t *testing.T) {
	c := data.Call{Masked: true, Sensitive: []apitypes.DataColumnRef{{Schema: "app", Table: "orders", Column: "items.0.email"}}}
	doc, err := wire.ParseDoc(`{_id: 1, items: [{email: "a@x", sku: "A"}, {email: "b@x", sku: "B"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	row := string(appendRow(nil, doc, []string{"_id", "items"}, maskedPaths(c, "app", "orders"), true, new([]byte)))
	if strings.Contains(row, "@x") || strings.Count(row, `$masked`) != 2 || !strings.Contains(row, `\"sku\":\"B\"`) {
		t.Fatalf("row: %s", row)
	}
}

// In a document a numeric part is a key: a mark on scores.2024 masks
// that key, not every value of scores.
func TestNumericKeyMark(t *testing.T) {
	c := data.Call{Masked: true, Sensitive: []apitypes.DataColumnRef{{Schema: "app", Table: "orders", Column: "scores.2024"}}}
	doc, err := wire.ParseDoc(`{_id: 1, scores: {"2023": 1, "2024": 99}}`)
	if err != nil {
		t.Fatal(err)
	}
	row := string(appendRow(nil, doc, []string{"_id", "scores"}, maskedPaths(c, "app", "orders"), true, new([]byte)))
	if strings.Contains(row, "99") || !strings.Contains(row, `\"2023\":{\"$numberInt\":\"1\"}`) {
		t.Fatalf("row: %s", row)
	}
}

// Changes can't reach into arrays, and a masked filter can't use $text.
func TestPositionalChangesAndText(t *testing.T) {
	s := func(v string) *string { return &v }
	for _, k := range []string{"tags.$[]", "tags.0", "tags.$"} {
		_, err := plan([]apitypes.DataChange{{Op: apitypes.DataChangeUpdate, Table: "app.orders", Key: map[string]string{"_id": "1"},
			Old: map[string]*string{k: nil}, New: map[string]*string{k: s(`"x"`)}}}, func(string, string) []string { return nil })
		if err == nil {
			t.Errorf("%s was allowed", k)
		}
	}
	if _, err := withWhere(wire.D{}, `{$or: [{$text: {$search: "ada"}}]}`, []string{"customer.email"}); err == nil {
		t.Error("$text while masked was allowed")
	}
	if _, err := withWhere(wire.D{}, `{$text: {$search: "ada"}}`, nil); err != nil {
		t.Errorf("$text unmasked: %v", err)
	}
}
