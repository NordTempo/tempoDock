package core

import (
	"errors"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// TestLockCoverage: the operations of stop/start, log drains, project
// files (applying one without naming an environment, and planning, are
// TestApplyLockedEnvironment's), Compose import and registry credentials,
// called by an admin token with shop/production locked. Changes to
// production are refused with environment_locked naming it, before
// anything changes; reads and org-level settings that change no
// environment by themselves stay open.
func TestLockCoverage(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	drain := h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainHTTP, URL: newDrainReceiver(h, false).srv.URL + "/ingest"})
	h.lock("production")
	agent := Actor{Kind: "token", OrgID: 1, Name: "agent", TokenID: 9, Scope: apitypes.ScopeAdmin}
	prod := web.Env()
	file := "version = 1\n[[environments]]\nname = \"production\"\n[[environments.services]]\nname = \"web\"\nport = 9999\n"
	compose := "services:\n  web:\n    image: nginx\n    expose: [\"9999\"]\n"
	cred := apitypes.SetRegistryCredentialRequest{Username: "bot", Password: "s3cret"}

	cases := []struct {
		name   string
		locked bool
		call   func() error
	}{
		{"stop service", true, func() error { _, err := h.c.StopService(h.ctx, agent, web); return err }},
		{"start service", true, func() error { _, err := h.c.StartService(h.ctx, agent, web); return err }},
		{"add log drain", true, func() error {
			_, err := h.c.AddLogDrain(h.ctx, agent, prod, apitypes.AddLogDrainRequest{Kind: apitypes.DrainHTTP, Name: "other", URL: "http://127.0.0.1:1/ingest"})
			return err
		}},
		{"test log drain", true, func() error { _, err := h.c.TestLogDrain(h.ctx, agent, prod, drain.ID); return err }},
		{"remove log drain", true, func() error { return h.c.RemoveLogDrain(h.ctx, agent, prod, drain.ID) }},
		{"list log drains", false, func() error { _, err := h.c.ListLogDrains(h.ctx, agent, prod); return err }},
		{"apply project file to production", true, func() error {
			_, err := h.c.ApplyProject(h.ctx, agent, "shop", apitypes.ApplyRequest{File: file, Environment: "production", Deploy: true})
			return err
		}},
		{"import compose", true, func() error {
			_, err := h.c.ImportCompose(h.ctx, agent, "shop", apitypes.ComposeImportRequest{Compose: compose, Apply: true})
			return err
		}},
		{"plan compose import", false, func() error {
			_, err := h.c.ImportCompose(h.ctx, agent, "shop", apitypes.ComposeImportRequest{Compose: compose})
			return err
		}},
		// The org's, changing no environment until a deploy, which the
		// lock stops on its own.
		{"set registry credential", false, func() error {
			_, err := h.c.SetRegistryCredential(h.ctx, agent, "ghcr.io", cred)
			return err
		}},
		{"remove registry credential", false, func() error { return h.c.DeleteRegistryCredential(h.ctx, agent, "ghcr.io") }},
	}
	for _, tc := range cases {
		err := tc.call()
		le, ok := errors.AsType[*LockedError](err)
		switch {
		case tc.locked && (!ok || CodeOf(err) != apitypes.CodeEnvironmentLocked || le.Environment != "production"):
			t.Errorf("%s: want environment_locked for production, got %v", tc.name, err)
		case !tc.locked && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		}
	}

	// Nothing changed in production.
	if drains, err := h.c.ListLogDrains(h.ctx, testSystem, prod); err != nil || len(drains) != 1 {
		t.Fatalf("drains = %+v, %v", drains, err)
	}
	if s, err := h.c.GetService(h.ctx, testSystem, web); err != nil || s.StoppedAt != nil || s.PendingChanges != 0 {
		t.Fatalf("web = %+v, %v", s, err)
	}
}
