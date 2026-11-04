package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// requestHarness is a locked shop/production with a web service, an
// agent token and a replayer that runs SetVariables like the API would.
type requestHarness struct {
	*harness
	agent, admin Actor
}

func newRequestHarness(t *testing.T) *requestHarness {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.lock("production")
	rh := &requestHarness{harness: h,
		agent: h.token(testSystem, apitypes.CreateTokenRequest{Name: "claude", Scope: apitypes.ScopeDeploy}),
		admin: Actor{Kind: "user", OrgID: 1, Name: "saile@example.com", UserID: 1, Scope: apitypes.ScopeAdmin},
	}
	h.c.SetReplayer("test", func(ctx context.Context, a Actor, op string, args json.RawMessage) (json.RawMessage, error) {
		if op == "restart" {
			rel, err := h.c.Restart(ctx, a, ServiceRef{"shop", "production", "web"})
			if err != nil {
				return nil, err
			}
			return json.Marshal(rel)
		}
		var req apitypes.SetVariablesRequest
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, err
		}
		res, err := h.c.SetVariables(ctx, a, EnvRef{"shop", "production"}, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(res)
	})
	return rh
}

// ask tries to set A=value as the agent and files the refusal.
func (h *requestHarness) ask(value, reason string) *LockedError {
	h.t.Helper()
	req := apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"A": value}}
	_, err := h.c.SetVariables(h.ctx, h.agent, EnvRef{"shop", "production"}, req)
	args, _ := json.Marshal(req)
	err = h.c.FileRequest(h.ctx, h.agent, err, Call{Via: "test", Op: "set-variables", Title: "Set variables", Args: args, Reason: reason})
	le, ok := errors.AsType[*LockedError](err)
	if !ok || le.Request == 0 || CodeOf(err) != apitypes.CodeEnvironmentLocked {
		h.t.Fatalf("want a filed request, got %v", err)
	}
	return le
}

// staged reports whether A is staged on web.
func (h *requestHarness) staged() bool {
	h.t.Helper()
	ch, err := h.c.GetChanges(h.ctx, testSystem, EnvRef{"shop", "production"}, true)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, v := range ch.Variables {
		if v.Key == "A" {
			return true
		}
	}
	return false
}

// TestRequestApproveRuns: a refused change becomes one request (an
// identical retry joins it and adds its reason); approving it runs it as
// the agent, audited with both names.
func TestRequestApproveRuns(t *testing.T) {
	h := newRequestHarness(t)
	first := h.ask("1", "")
	again := h.ask("1", "launch prices")
	if again.Request != first.Request || !strings.Contains(again.Error(), "dock request wait") {
		t.Fatalf("retry filed #%d, first #%d: %v", again.Request, first.Request, again)
	}
	got, err := h.c.GetRequest(h.ctx, h.admin, first.Request)
	if err != nil || got.Status != apitypes.RequestPending || got.Reason != "launch prices" || !strings.Contains(string(got.Args), `"A":"1"`) ||
		got.Summary != "token claude: setting variables in shop/production" {
		t.Fatalf("request: %+v %v", got, err)
	}
	// The agent sees its own, without the arguments.
	if mine, err := h.c.GetRequest(h.ctx, h.agent, first.Request); err != nil || mine.Args != nil {
		t.Fatalf("agent's view: %+v %v", mine, err)
	}
	// Tokens never decide, not even an admin's.
	if _, err := h.c.DecideRequest(h.ctx, h.agent, first.Request, true, apitypes.DecideAccessRequest{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("token approves: %v", err)
	}
	done, err := h.c.DecideRequest(h.ctx, h.admin, first.Request, true, apitypes.DecideAccessRequest{Comment: "go"})
	if err != nil || done.Status != apitypes.RequestDone || done.DecidedBy != "saile@example.com" || len(done.Result) == 0 {
		t.Fatalf("approve: %+v %v", done, err)
	}
	if !h.staged() {
		t.Fatal("A isn't staged after approval")
	}
	evs, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: "variables.set"})
	if err != nil || len(evs) == 0 || evs[0].Actor != "claude" || !strings.Contains(evs[0].Summary, "approved by saile@example.com") {
		t.Fatalf("audit: %+v %v", evs, err)
	}
	if _, err := h.c.DecideRequest(h.ctx, h.admin, first.Request, false, apitypes.DecideAccessRequest{}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("deciding twice: %v", err)
	}
	// The grant was for that call alone: the next change is a new request.
	if next := h.ask("2", ""); next.Request == first.Request {
		t.Fatal("the approval outlived its call")
	}
}

// TestRequestRejectAndExpire: a rejected request runs nothing; one past
// its time is expired, can't be approved, and a retry files a new one.
func TestRequestRejectAndExpire(t *testing.T) {
	h := newRequestHarness(t)
	r := h.ask("1", "")
	out, err := h.c.DecideRequest(h.ctx, h.admin, r.Request, false, apitypes.DecideAccessRequest{Comment: "not now"})
	if err != nil || out.Status != apitypes.RequestRejected || out.Comment != "not now" || h.staged() {
		t.Fatalf("reject: %+v %v", out, err)
	}

	r = h.ask("2", "")
	if _, err := h.store.DB.ExecContext(h.ctx, "UPDATE access_requests SET expires_at = ? WHERE id = ?", now().Add(-time.Minute), r.Request); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.c.GetRequest(h.ctx, h.admin, r.Request); got.Status != apitypes.RequestExpired {
		t.Fatalf("past its time: %s", got.Status)
	}
	if _, err := h.c.DecideRequest(h.ctx, h.admin, r.Request, true, apitypes.DecideAccessRequest{}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("approve expired: %v", err)
	}
	if again := h.ask("2", ""); again.Request == r.Request {
		t.Fatal("a retry joined the expired request")
	}
	if err := h.c.expireRequests(h.ctx, now()); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.store.GetAccessRequest(h.ctx, r.Request); got.Status != string(apitypes.RequestExpired) {
		t.Fatalf("stored: %s", got.Status)
	}
}

// TestRequestRevalidates: approval runs the call as things are now. A
// revoked token runs nothing; a call that no longer applies fails with
// its own error, and the request says so.
func TestRequestRevalidates(t *testing.T) {
	h := newRequestHarness(t)
	r := h.ask("1", "")
	if err := h.c.RevokeToken(h.ctx, testSystem, h.agent.TokenID); err != nil {
		t.Fatal(err)
	}
	out, err := h.c.DecideRequest(h.ctx, h.admin, r.Request, true, apitypes.DecideAccessRequest{})
	if err != nil || out.Status != apitypes.RequestFailed || !strings.Contains(out.Error, "revoked") || h.staged() {
		t.Fatalf("revoked: %+v %v", out, err)
	}

	// A deploy started meanwhile: restarting no longer applies.
	h2 := newRequestHarness(t)
	web := ServiceRef{"shop", "production", "web"}
	h2.deploy(web, "app:1")
	_, err = h2.c.Restart(h2.ctx, h2.agent, web)
	err = h2.c.FileRequest(h2.ctx, h2.agent, err, Call{Via: "test", Op: "restart", Title: "Restart", Args: json.RawMessage(`{}`)})
	le, _ := errors.AsType[*LockedError](err)
	if le == nil || le.Request == 0 {
		t.Fatalf("restart: %v", err)
	}
	r = le
	defer h2.rt.HoldHealthChecks()()
	if _, err := h2.c.DeployImage(h2.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:2"}); err != nil {
		t.Fatal(err)
	}
	out, err = h2.c.DecideRequest(h2.ctx, h2.admin, r.Request, true, apitypes.DecideAccessRequest{})
	if err != nil || out.Status != apitypes.RequestFailed || !strings.Contains(out.Error, "already deploying") {
		t.Fatalf("no longer applies: %+v %v", out, err)
	}
	if got, _ := h2.c.GetRequest(h2.ctx, h2.admin, r.Request); got.ChangesSince == nil || *got.ChangesSince == 0 {
		t.Fatalf("changes since: %+v", got.ChangesSince)
	}
}

// TestRequestInterruptedByRestart: a run a restart cut off fails.
func TestRequestInterruptedByRestart(t *testing.T) {
	h := newRequestHarness(t)
	r := h.ask("1", "")
	if _, err := h.store.DB.ExecContext(h.ctx, "UPDATE access_requests SET status = 'approved' WHERE id = ?", r.Request); err != nil {
		t.Fatal(err)
	}
	h.restart()
	if got, _ := h.c.GetRequest(h.ctx, h.admin, r.Request); got.Status != apitypes.RequestFailed || !strings.Contains(got.Error, "restarted") {
		t.Fatalf("after restart: %+v", got)
	}
}

// TestAskFirst: an actor that asks first files a request for a change in
// an unlocked environment, and can't change anything outside one.
func TestAskFirst(t *testing.T) {
	h := newRequestHarness(t)
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CreateService(h.ctx, testSystem, EnvRef{"shop", "staging"}, apitypes.CreateServiceRequest{Name: "web", Image: "app:1"}); err != nil {
		t.Fatal(err)
	}
	_, err := h.c.SetVariables(h.ctx, h.agent.AskFirst(), EnvRef{"shop", "staging"}, apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"A": "1"}})
	if CodeOf(err) != apitypes.CodeEnvironmentLocked {
		t.Fatalf("ask first in staging: %v", err)
	}
	if _, err := h.c.CreateEnvironment(h.ctx, h.agent.AskFirst(), "shop", apitypes.CreateEnvironmentRequest{Name: "qa"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("ask first outside an environment: %v", err)
	}
}
