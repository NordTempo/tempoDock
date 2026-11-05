package core

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// TestRequestPreview: an admin viewing an API- or MCP-shaped variables
// request sees each changed, added and removed key's current value
// against the requested one; the token that asked sees no values.
func TestRequestPreview(t *testing.T) {
	h := newRequestHarness(t)
	prod := EnvRef{"shop", "production"}
	if _, err := h.c.SetVariables(h.ctx, h.admin, prod, apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"A": "1", "B": "2", "KEEP": "k"}, Apply: true}); err != nil {
		t.Fatal(err)
	}
	// The refusal a token's change gets, to file the calls under.
	_, locked := h.c.SetVariables(h.ctx, h.agent, prod, apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"A": "x"}})
	str := func(s string) *string { return &s }
	want := []apitypes.VariableChange{
		{Key: "A", Scope: apitypes.ScopeService, Service: "web", Action: apitypes.ChangeUpdate, Old: str("1"), New: str("10")},
		{Key: "B", Scope: apitypes.ScopeService, Service: "web", Action: apitypes.ChangeRemove, Old: str("2")},
		{Key: "C", Scope: apitypes.ScopeService, Service: "web", Action: apitypes.ChangeAdd, New: str("3")},
	}
	for _, call := range []Call{
		{Via: "api", Op: "set-variables", Args: []byte(`{"Project":"shop","Environment":"production","Body":{"service":"web","set":{"A":"10","C":"3","KEEP":"k"},"unset":["B","NONE"]}}`)},
		{Via: "mcp", Op: "set_variables", Args: []byte(`{"project":"shop","environment":"production","service":"web","set":{"A":"10","C":"3","KEEP":"k"},"unset":["B","NONE"]}`)},
	} {
		le, ok := errors.AsType[*LockedError](h.c.FileRequest(h.ctx, h.agent, locked, call))
		if !ok || le.Request == 0 {
			t.Fatalf("%s: no request filed: %v", call.Via, locked)
		}
		got, err := h.c.GetRequest(h.ctx, h.admin, le.Request)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Changes, want) {
			b, _ := json.Marshal(got.Changes)
			t.Errorf("%s: changes %s", call.Via, b)
		}
		if mine, err := h.c.GetRequest(h.ctx, h.agent, le.Request); err != nil || mine.Changes != nil {
			t.Fatalf("%s: the agent's view: %+v %v", call.Via, mine.Changes, err)
		}
	}
}
