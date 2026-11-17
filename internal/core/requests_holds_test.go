package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// An approval lifts what its decider may lift (ADR 0044, 0080): a person
// answering their own agent's "ask me first" lifts that alone, so a lock
// set or an agent rule made while the request waited stops its run like
// a new call; an approver's decision lifts the lock, the org's agent
// rules and the person's asking.
func TestApprovalLiftsOnlyWhatItsDeciderMay(t *testing.T) {
	const (
		none = iota
		lock
		lockPeople
		rule
	)
	for _, tc := range []struct {
		name string
		// atFiling holds the call when it is made, besides the person's
		// asking first; since is added while the request waits.
		atFiling, since int
		runs            bool
	}{
		{"the person approves their agent's ask", none, none, true},
		{"the person approves, a lock was set since", none, lock, false},
		{"the person approves, a lock for people was set since", none, lockPeople, false},
		{"the person approves, an agent rule asks since", none, rule, false},
		{"an approver approves a locked call", lock, none, true},
		{"an approver approves a call locked for people", lockPeople, none, true},
		{"an approver approves a call the agent rules ask for", rule, none, true},
		{"an approver approves a locked call, an agent rule asks since", lock, rule, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.enforce()
			web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
			h.deploy(web, "app:1")
			h.c.SetReplayer("test", func(ctx context.Context, a Actor, _ string, _ json.RawMessage) (json.RawMessage, error) {
				rel, err := h.c.Restart(ctx, a, web)
				if err != nil {
					return nil, err
				}
				return json.Marshal(rel)
			})
			approver := h.user("bo@example.com", apitypes.ScopeAdmin)
			dev := h.cliUser("dev@example.com", apitypes.ScopeDeploy)
			if _, err := h.c.SetAgentSettings(h.ctx, dev, apitypes.AgentSettings{Settings: []apitypes.AgentSetting{
				{Area: "deploy", Kind: "production", Level: "edit", Ask: true},
			}}); err != nil {
				t.Fatal(err)
			}
			secret := h.cliToken(dev)
			hold := func(what int) {
				t.Helper()
				var err error
				switch what {
				case lock, lockPeople:
					_, err = h.c.LockEnvironment(h.ctx, approver, web.Env(), apitypes.LockEnvironmentRequest{People: what == lockPeople})
				case rule:
					_, err = h.c.SetAgentRules(h.ctx, approver, apitypes.AgentSettings{Settings: []apitypes.AgentSetting{
						{Area: "deploy", Kind: "production", Level: "all", Ask: true},
					}})
				}
				if err != nil {
					t.Fatal(err)
				}
			}

			hold(tc.atFiling)
			bot, err := h.c.AuthenticateToken(h.ctx, secret, "")
			if err != nil {
				t.Fatal(err)
			}
			_, err = h.c.Restart(h.ctx, bot, web)
			err = h.c.FileRequest(h.ctx, bot, err, Call{Via: "test", Op: "restart", Title: "Restart", Args: []byte(`{}`)})
			le, ok := errors.AsType[*LockedError](err)
			if !ok || le.Request == 0 {
				t.Fatalf("want a filed request, got %v", err)
			}
			decider := approver
			if tc.atFiling == none {
				if le.askOwner == nil {
					t.Fatalf("the agent's call doesn't ask its person first: %v", err)
				}
				decider = dev
			}
			hold(tc.since)

			before, err := h.c.ListReleases(h.ctx, testSystem, web, 50)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.c.DecideRequest(h.ctx, decider, le.Request, true, apitypes.DecideAccessRequest{}); err != nil {
				t.Fatalf("deciding: %v", err)
			}
			var r apitypes.AccessRequest
			eventually(t, "the request ended", func() bool {
				r, err = h.c.GetRequest(h.ctx, decider, le.Request)
				return err == nil && (r.Status == apitypes.RequestDone || r.Status == apitypes.RequestFailed)
			})
			after, err := h.c.ListReleases(h.ctx, testSystem, web, 50)
			if err != nil {
				t.Fatal(err)
			}
			if ran := r.Status == apitypes.RequestDone; ran != tc.runs || ran != (len(after) == len(before)+1) {
				t.Fatalf("request %s (%q), releases %d -> %d; want it to run: %v", r.Status, r.Error, len(before), len(after), tc.runs)
			}
			if !tc.runs && r.Error == "" {
				t.Fatal("a run that was stopped doesn't say why")
			}
		})
	}
}
