package api

import (
	"encoding/json"
	"testing"
)

// demoAllowed names the writes the view-only demo lets through: each must
// be an API operation, and not a GET (GETs need no entry).
func TestDemoAllowsRealOperations(t *testing.T) {
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
	method := map[string]string{}
	for _, item := range doc.Paths {
		for m, op := range item {
			method[op.OperationID] = m
		}
	}
	for id := range demoAllowed {
		switch method[id] {
		case "":
			t.Errorf("demoAllowed names %q, which isn't an API operation", id)
		case "get":
			t.Errorf("demoAllowed names %q, a GET: the demo allows GETs anyway", id)
		}
	}
}
