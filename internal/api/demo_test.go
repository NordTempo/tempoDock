package api

import (
	"encoding/json"
	"testing"
)

// A misspelled operation in demoRefused would let the demo do it: every
// one must be an operation of the API.
func TestDemoRefusesRealOperations(t *testing.T) {
	spec, err := Spec()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(spec, &doc); err != nil {
		t.Fatal(err)
	}
	ops := map[string]bool{}
	for _, item := range doc.Paths {
		for _, op := range item {
			ops[op.OperationID] = true
		}
	}
	for id := range demoRefused {
		if !ops[id] {
			t.Errorf("demoRefused names %q, which isn't an API operation", id)
		}
	}
}
