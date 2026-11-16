package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A lock for people too (ADR 0080, protected environments): a developer's
// restart in production waits as their access request and runs as them
// once an approver approves; the admin, who approves there, changes it
// directly; and only an approver loosens it.
func TestProtectionForPeople(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	admin := h.user("ada@example.com", apitypes.ScopeAdmin)
	dev := h.user("dev@example.com", apitypes.ScopeDeploy)
	h.c.SetReplayer("test", func(ctx context.Context, a Actor, op string, _ json.RawMessage) (json.RawMessage, error) {
		rel, err := h.c.Restart(ctx, a, web)
		if err != nil {
			return nil, err
		}
		return json.Marshal(rel)
	})
	if _, err := h.c.LockEnvironment(h.ctx, admin, EnvRef{"shop", "production"}, apitypes.LockEnvironmentRequest{People: true}); err != nil {
		t.Fatal(err)
	}

	_, err := h.c.Restart(h.ctx, dev, web)
	err = h.c.FileRequest(h.ctx, dev, err, Call{Via: "test", Op: "restart", Title: "Restart", Args: []byte(`{}`)})
	le, ok := errors.AsType[*LockedError](err)
	if !ok || le.Request == 0 || !le.person {
		t.Fatalf("a developer's restart: %v", err)
	}
	if mine, err := h.c.ListRequests(h.ctx, dev, RequestFilter{}); err != nil || len(mine) != 1 {
		t.Fatalf("the developer's requests = %+v, %v", mine, err)
	}
	if _, err := h.c.SetVariables(h.ctx, admin, EnvRef{"shop", "production"}, apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatalf("the approver's own change: %v", err)
	}
	r, err := h.c.DecideRequest(h.ctx, admin, le.Request, true, apitypes.DecideAccessRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if r, err = h.c.WaitRequest(h.ctx, admin, r.ID, 5*time.Second); err != nil || r.Status != apitypes.RequestDone || r.RequestedBy != dev.Name {
		t.Fatalf("approved: %+v, %v", r, err)
	}

	if _, err := h.c.UnlockEnvironment(h.ctx, dev, EnvRef{"shop", "production"}, apitypes.UnlockEnvironmentRequest{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a developer unlocking: %v", err)
	}
	if _, err := h.c.UnlockEnvironment(h.ctx, admin, EnvRef{"shop", "production"}, apitypes.UnlockEnvironmentRequest{Minutes: 30}); err != nil {
		t.Fatalf("an approver opening it: %v", err)
	}
	if _, err := h.c.SetVariables(h.ctx, dev, EnvRef{"shop", "production"}, apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"B": "1"}}); err != nil {
		t.Fatalf("a developer while it's open: %v", err)
	}
}
