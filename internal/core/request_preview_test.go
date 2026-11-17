package core

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
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

// Deciding a request isn't revealing values (ADR 0080: the Approvers
// group approves without being admins): an approver who can't reveal
// sees which keys a variables request changes and how, and no value;
// someone who may reveal there sees them.
func TestApproverWithoutRevealSeesARequestsKeysNotItsValues(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.wait(web, h.deploy(web, "app:1").Number)
	prod := EnvRef{"shop", "production"}
	admin := h.user("ada@example.com", apitypes.ScopeAdmin)
	const secret = "sk_live_51Hh-the-real-stripe-key"
	if _, err := h.c.SetVariables(h.ctx, admin, prod, apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"STRIPE_KEY": secret}, Apply: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.LockEnvironment(h.ctx, admin, prod, apitypes.LockEnvironmentRequest{People: true}); err != nil {
		t.Fatal(err)
	}
	// A Developer: variables:edit, no reveal. An Approver: sees and approves, nothing else.
	dev := h.user("dev@example.com", apitypes.ScopeDeploy)
	// The seeded groups, as Settings > Members gives them: Viewers and Approvers.
	ap := h.user("ap@example.com", apitypes.ScopeRead)
	if _, err := h.c.AddGroupMember(h.ctx, admin, "Approvers", ap.Email); err != nil {
		t.Fatal(err)
	}
	approver := h.in(userActor(h.userRow(ap.UserID)), "")

	// Neither reveals values.
	for _, a := range []Actor{dev, approver} {
		if _, err := h.c.ListVariables(h.ctx, a, prod, "", true); CodeOf(err) != apitypes.CodeForbidden {
			t.Fatalf("%s revealing variables: %v", a.Name, err)
		}
	}

	// The developer's change waits as a request, as the API files it
	// (api.go: Via "api", the operation ID, the Huma input as args).
	const asked = "sk_live_the-value-asked-for"
	_, err := h.c.SetVariables(h.ctx, dev, prod, apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"NEW_KEY": asked}, Unset: []string{"STRIPE_KEY"}})
	err = h.c.FileRequest(h.ctx, dev, err, Call{Via: "api", Op: "set-variables", Title: "Set variables",
		Args: []byte(`{"Project":"shop","Environment":"production","Body":{"service":"web","set":{"NEW_KEY":"` + asked + `"},"unset":["STRIPE_KEY"]}}`)})
	le, ok := errors.AsType[*LockedError](err)
	if !ok || le.Request == 0 {
		t.Fatalf("the developer's change in a protected environment: %v", err)
	}

	got, err := h.c.GetRequest(h.ctx, approver, le.Request)
	if err != nil {
		t.Fatal(err)
	}
	// The approver sees what would change, by key, and no value: not the
	// live one, not the one asked for, in the preview or the arguments.
	if len(got.Changes) != 2 || got.Changes[0].Key != "NEW_KEY" || got.Changes[0].Action != apitypes.ChangeAdd ||
		got.Changes[1].Key != "STRIPE_KEY" || got.Changes[1].Action != apitypes.ChangeRemove {
		t.Fatalf("the approver's preview: %+v", got.Changes)
	}
	for _, ch := range got.Changes {
		if ch.Old != nil || ch.New != nil {
			t.Errorf("%s shows a value to an approver who can't reveal: %v, %v", ch.Key, ch.Old, ch.New)
		}
	}
	if args := string(got.Args); strings.Contains(args, asked) || strings.Contains(args, secret) || !strings.Contains(args, `"NEW_KEY"`) || !strings.Contains(args, `"STRIPE_KEY"`) {
		t.Errorf("the approver's arguments should name the keys and no value: %s", args)
	}
	if !got.Decide {
		t.Error("the approver should still be the one who decides")
	}
	// An admin, who may reveal there, sees both values.
	full, err := h.c.GetRequest(h.ctx, admin, le.Request)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Changes) != 2 || full.Changes[0].New == nil || *full.Changes[0].New != asked || full.Changes[1].Old == nil || *full.Changes[1].Old != secret || !strings.Contains(string(full.Args), asked) {
		t.Errorf("the admin's preview: %+v, args %s", full.Changes, full.Args)
	}
	// The developer who asked sees their own request, without the preview.
	mine, err := h.c.GetRequest(h.ctx, dev, le.Request)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine.Changes) != 0 || len(mine.Args) != 0 {
		t.Errorf("the asker's own request shows changes %v, args %s", mine.Changes, mine.Args)
	}
}

// withoutVariableValues hides the values a variables request sets, in
// both forms its args are stored in, and leaves other calls' args alone.
func TestWithoutVariableValues(t *testing.T) {
	for name, tc := range map[string]struct{ via, op, args, want string }{
		"api":      {"api", "set-variables", `{"Project":"shop","Body":{"service":"web","set":{"A":"1","B":"2"},"unset":["C"]}}`, `{"Body":{"service":"web","set":{"A":"` + hiddenValue + `","B":"` + hiddenValue + `"},"unset":["C"]},"Project":"shop"}`},
		"mcp":      {"mcp", "set_variables", `{"project":"shop","set":{"A":"1"}}`, `{"project":"shop","set":{"A":"` + hiddenValue + `"}}`},
		"unset":    {"mcp", "set_variables", `{"project":"shop","unset":["A"]}`, `{"project":"shop","unset":["A"]}`},
		"other":    {"api", "deploy-image", `{"Body":{"image":"app:2","set":{"A":"1"}}}`, `{"Body":{"image":"app:2","set":{"A":"1"}}}`},
		"not json": {"api", "set-variables", `{`, `{`},
	} {
		if got := string(withoutVariableValues(tc.via, tc.op, json.RawMessage(tc.args))); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}
