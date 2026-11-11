package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Every public update of a published page goes to its hooks, in each
// kind's format, signed for webhooks; drafts and unpublished pages post
// nothing, and nothing private is in a post.
func TestStatusHooks(t *testing.T) {
	h, _, _ := incidentPage(t)
	k := newReceiver(h)
	admin := h.user("admin@example.com", apitypes.ScopeAdmin)
	if _, err := h.c.AddStatusHook(h.ctx, admin, "acme", apitypes.AddStatusHookRequest{Name: "lan", Kind: apitypes.HookSlack, URL: "https://169.254.169.254/hook"}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("a hook to a link-local address: %v", err)
	}
	if _, err := h.c.AddStatusHook(h.ctx, h.user("dev@example.com", apitypes.ScopeDeploy), "acme", apitypes.AddStatusHookRequest{Name: "x", Kind: apitypes.HookSlack, URL: k.srv.URL}); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("a deployer added a hook: %v", err)
	}
	wh, err := h.c.AddStatusHook(h.ctx, admin, "acme", apitypes.AddStatusHookRequest{Name: "partners", Kind: apitypes.HookWebhook, URL: k.srv.URL + "/token-123"})
	if err != nil {
		t.Fatal(err)
	}
	if wh.Secret == "" || wh.Hook.Target != k.srv.URL || strings.Contains(wh.Hook.Target, "token-123") {
		t.Fatalf("webhook hook = %+v", wh)
	}
	if _, err := h.c.AddStatusHook(h.ctx, admin, "acme", apitypes.AddStatusHookRequest{Name: "slack", Kind: apitypes.HookSlack, URL: k.srv.URL}); err != nil {
		t.Fatal(err)
	}
	// At most statusMaxHooks, each update a post to every one.
	for i := range statusMaxHooks - 1 {
		name := fmt.Sprintf("extra%d", i)
		_, err := h.c.AddStatusHook(h.ctx, admin, "acme", apitypes.AddStatusHookRequest{Name: name, Kind: apitypes.HookSlack, URL: "https://hooks.example.com/" + name})
		if want := i < statusMaxHooks-2; (err == nil) != want {
			t.Fatalf("hook %d: %v", i+3, err)
		}
	}
	for i := range statusMaxHooks - 2 {
		if err := h.c.RemoveStatusHook(h.ctx, admin, "acme", fmt.Sprintf("extra%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := h.c.OpenIncident(h.ctx, testSystem, "acme", apitypes.OpenIncidentRequest{Title: "Draft", State: apitypes.IncidentDraft,
		Components: []apitypes.StatusImpact{{Component: "API", Impact: apitypes.StatusDegraded}}}); err != nil {
		t.Fatal(err)
	}
	inc, err := h.c.OpenIncident(h.ctx, testSystem, "acme", apitypes.OpenIncidentRequest{Title: "API <errors>", Message: "Looking into it.",
		Components: []apitypes.StatusImpact{{Component: "API", Impact: apitypes.StatusPartialOutage}}})
	if err != nil {
		t.Fatal(err)
	}
	k.events(t, 2)
	h.outboxSent()
	k.mu.Lock()
	sigs := append([]string(nil), k.sigs...)
	k.mu.Unlock()
	if len(sigs) != 2 {
		t.Fatalf("posts = %v", sigs)
	}
	var hook, slack string
	for _, s := range sigs {
		if strings.HasPrefix(s, "|") {
			slack = s
		} else {
			hook = s
		}
	}
	if !strings.HasPrefix(hook, "t=") || !strings.Contains(hook, `"event":"update"`) || !strings.Contains(hook, `"id":"`+inc.ID+`"`) ||
		!strings.Contains(hook, `"message":"Looking into it."`) {
		t.Errorf("webhook post = %s", hook)
	}
	var msg struct{ Text string }
	if err := json.Unmarshal([]byte(strings.TrimPrefix(slack, "|")), &msg); err != nil ||
		!strings.HasPrefix(msg.Text, "Incident on Acme: API &lt;errors&gt; (Investigating)\nAffects API\nLooking into it.\nhttps://status-acme.dock.example.net/incidents/") {
		t.Errorf("slack post = %s (%v)", slack, err)
	}
	for _, private := range []string{"shop", "production", "system", "example.com"} {
		if strings.Contains(hook+slack, private) {
			t.Errorf("a post says %q", private)
		}
	}

	res, err := h.c.TestStatusHook(h.ctx, admin, "acme", "slack")
	if err != nil || !res.OK || res.Hook.LastSentAt == nil {
		t.Errorf("test = %+v, %v", res, err)
	}
	if err := h.c.RemoveStatusHook(h.ctx, admin, "acme", "slack"); err != nil {
		t.Fatal(err)
	}
	if list, err := h.c.ListStatusHooks(h.ctx, admin, "acme"); err != nil || len(list) != 1 || list[0].Name != "partners" {
		t.Errorf("hooks = %+v, %v", list, err)
	}

	if _, err := h.c.UpdateStatusPage(h.ctx, testSystem, "acme", apitypes.UpdateStatusPageRequest{Published: new(false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.PostIncidentUpdate(h.ctx, testSystem, inc.ID, apitypes.PostIncidentUpdateRequest{State: apitypes.IncidentResolved, Message: "Fixed."}); err != nil {
		t.Fatal(err)
	}
	h.outboxSent()
	k.mu.Lock()
	n := len(k.sigs)
	k.mu.Unlock()
	if n != 3 {
		t.Errorf("an unpublished page posted: %d posts", n)
	}
}
