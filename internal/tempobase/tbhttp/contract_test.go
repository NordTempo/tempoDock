package tbhttp_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/api"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbhttp"
)

// The web UI's data panel (web/src/features/data) is shared by Dock and
// tempoBase and typed from Dock's spec, so every data operation must take
// and return exactly the same shapes in both (ADR 0024 section 3): the
// same query parameters, request bodies, response statuses and schemas,
// with every $ref followed. Only the path differs (a service in Dock, a
// connection in tempoBase).
func TestDataContractMatchesDock(t *testing.T) {
	dock, tb := loadSpec(t, api.Spec), loadSpec(t, tbhttp.Spec)

	type pair struct {
		dock, tb string
		// requestOnly compares the request alone: the responses are each
		// product's own type (a Dock or tempoBase token).
		requestOnly bool
	}
	var pairs []pair
	under := func(prefix string) func(string) bool {
		return func(path string) bool { return strings.HasSuffix(path, prefix) || strings.Contains(path, prefix+"/") }
	}
	dockData := dock.ops(under("/services/{service}/data"))
	tbData := tb.ops(under("/connections/{connection}/data"))
	if !slices.Equal(sortedKeys(dockData), sortedKeys(tbData)) {
		t.Fatalf("data operations differ:\ndock:      %v\ntempoBase: %v", sortedKeys(dockData), sortedKeys(tbData))
	}
	for _, id := range sortedKeys(dockData) {
		pairs = append(pairs, pair{dock: id, tb: id})
	}
	pairs = append(pairs,
		pair{dock: "get-default-data-policy", tb: "get-data-policy"},
		pair{dock: "set-default-data-policy", tb: "set-data-policy"},
		pair{dock: "get-environment-data-policy", tb: "get-connection-data-policy"},
		pair{dock: "set-environment-data-policy", tb: "set-connection-data-policy"},
		pair{dock: "get-audit-detail", tb: "get-audit-detail"},
		pair{dock: "update-token", tb: "update-token", requestOnly: true},
	)
	for _, p := range pairs {
		d, ok := dock.byID[p.dock]
		if !ok {
			t.Fatalf("Dock has no operation %s", p.dock)
		}
		b, ok := tb.byID[p.tb]
		if !ok {
			t.Fatalf("tempoBase has no operation %s", p.tb)
		}
		got, want := tb.shape(b, p.requestOnly), dock.shape(d, p.requestOnly)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s (tempoBase) differs from %s (Dock):\n%s", p.tb, p.dock, firstDiff("", got, want))
		}
	}
}

// spec is a parsed OpenAPI document.
type spec struct {
	schemas map[string]any
	// byID holds each operation (with its method and path) by its ID.
	byID  map[string]map[string]any
	paths map[string]string
}

func loadSpec(t *testing.T, build func() ([]byte, error)) *spec {
	t.Helper()
	raw, err := build()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths      map[string]map[string]map[string]any `json:"paths"`
		Components struct {
			Schemas map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	s := &spec{schemas: doc.Components.Schemas, byID: map[string]map[string]any{}, paths: map[string]string{}}
	for path, ops := range doc.Paths {
		for _, op := range ops {
			id, _ := op["operationId"].(string)
			s.byID[id] = op
			s.paths[id] = path
		}
	}
	return s
}

// ops returns the IDs of operations whose path matches.
func (s *spec) ops(match func(path string) bool) map[string]bool {
	out := map[string]bool{}
	for id, path := range s.paths {
		if match(path) {
			out[id] = true
		}
	}
	return out
}

// locators are the path parameters that name the database: Dock's
// project, environment and service, tempoBase's connection.
var locators = map[string]bool{"project": true, "environment": true, "service": true, "connection": true}

// shape is what the client sees of an operation: its parameters but the
// locators, request body and responses, with every $ref resolved.
func (s *spec) shape(op map[string]any, requestOnly bool) map[string]any {
	var params []any
	list, _ := op["parameters"].([]any)
	for _, p := range list {
		p := p.(map[string]any)
		if p["in"] != "path" || !locators[p["name"].(string)] {
			params = append(params, p)
		}
	}
	out := map[string]any{"parameters": s.resolve(params, nil), "requestBody": s.resolve(op["requestBody"], nil)}
	if !requestOnly {
		out["responses"] = s.resolve(op["responses"], nil)
	}
	return out
}

// resolve replaces each {"$ref": "#/components/schemas/X"} with X,
// resolved; seen stops recursive types.
func (s *spec) resolve(v any, seen []string) any {
	switch v := v.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok {
			name := strings.TrimPrefix(ref, "#/components/schemas/")
			if slices.Contains(seen, name) {
				return map[string]any{"$ref": name}
			}
			return s.resolve(s.schemas[name], append(seen, name))
		}
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = s.resolve(e, seen)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = s.resolve(e, seen)
		}
		return out
	}
	return v
}

// firstDiff describes the first place where got and want differ.
func firstDiff(path string, got, want any) string {
	switch g := got.(type) {
	case map[string]any:
		w, ok := want.(map[string]any)
		if !ok {
			break
		}
		for _, k := range sortedKeys(mergeKeys(g, w)) {
			if !reflect.DeepEqual(g[k], w[k]) {
				return firstDiff(path+"."+k, g[k], w[k])
			}
		}
	case []any:
		w, ok := want.([]any)
		if !ok || len(g) != len(w) {
			break
		}
		for i := range g {
			if !reflect.DeepEqual(g[i], w[i]) {
				return firstDiff(fmt.Sprintf("%s[%d]", path, i), g[i], w[i])
			}
		}
	}
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	return fmt.Sprintf("at %s\ntempoBase: %.400s\nDock:      %.400s", path, gb, wb)
}

func mergeKeys(a, b map[string]any) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Every {param} in a tempoBase path is a declared path parameter: Huma
// silently drops the fields of unexported embedded input types, which
// leaves the handler with an empty value.
func TestPathParametersDeclared(t *testing.T) {
	tb := loadSpec(t, tbhttp.Spec)
	for id, path := range tb.paths {
		var declared []string
		list, _ := tb.byID[id]["parameters"].([]any)
		for _, p := range list {
			if p := p.(map[string]any); p["in"] == "path" {
				declared = append(declared, p["name"].(string))
			}
		}
		var want []string
		for part := range strings.SplitSeq(path, "/") {
			if name, ok := strings.CutPrefix(part, "{"); ok {
				want = append(want, strings.TrimSuffix(name, "}"))
			}
		}
		slices.Sort(declared)
		slices.Sort(want)
		if !slices.Equal(declared, want) {
			t.Errorf("%s %s declares path parameters %v", id, path, declared)
		}
	}
}
