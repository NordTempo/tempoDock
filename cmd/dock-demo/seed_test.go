package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/NordTempo/tempoDock/internal/api"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/node/fake"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store/storetest"
)

// The seed runs through the real API on the fake runtime, as in the demo,
// so a change to the API it calls fails here. Once seeded the demo signs
// visitors in, shows them everything and lets them change nothing.
func TestSeed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st := storetest.Open(t, filepath.Join(dir, "dock.db"))
	box, err := secrets.New(bytes.Repeat([]byte{1}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.BaseDomain = "apps.example.com"
	cfg.PublicIP = "203.0.113.10"
	cfg.Demo.User = "demo@acme.example"
	cfg.Analytics.StoreBinary = filepath.Join(dir, "no-store")
	if err := os.WriteFile(cfg.Analytics.StoreBinary, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rt := fake.New()
	rt.PassHealthAfter(time.Millisecond)
	c, err := core.New(ctx, core.Options{
		Config: cfg, Store: st, Secrets: box, Runtime: rt, Logger: slog.New(slog.DiscardHandler),
		CutoverDrain: time.Nanosecond, PasswordCost: bcrypt.MinCost,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(c, api.Options{MaxUploadBytes: 1 << 20, Logger: slog.New(slog.DiscardHandler)}))
	t.Cleanup(func() {
		srv.Close()
		c.Close()
		_ = st.Close()
	})
	b, _, err := c.Bootstrap(ctx, seedUser)
	if err != nil {
		t.Fatal(err)
	}

	visitor := func() *client {
		jar, _ := cookiejar.New(nil)
		return &client{base: srv.URL, http: &http.Client{Jar: jar}}
	}
	status := func(err error) int {
		if ae, ok := errors.AsType[*apiError](err); ok {
			return ae.Status
		}
		if err != nil {
			t.Fatal(err)
		}
		return http.StatusOK
	}
	if got := status(visitor().do(ctx, http.MethodPost, "/auth/demo", nil, nil)); got != http.StatusServiceUnavailable {
		t.Fatalf("sign-in before the seed: %d, want 503", got)
	}

	if err := seed(ctx, slog.New(slog.DiscardHandler), seedConfig{
		URL: srv.URL, Password: b.Password, DemoUser: cfg.Demo.User, Spool: filepath.Join(dir, "spool.jsonl"),
	}); err != nil {
		t.Fatal(err)
	}

	v := visitor()
	if err := v.do(ctx, http.MethodGet, "/auth/demo", nil, nil); err != nil {
		t.Fatalf("not ready after the seed: %v", err)
	}
	if err := v.do(ctx, http.MethodPost, "/auth/demo", nil, nil); err != nil {
		t.Fatal(err)
	}
	var me apitypes.Me
	if err := v.do(ctx, http.MethodGet, "/me", nil, &me); err != nil {
		t.Fatal(err)
	}
	if me.Email != cfg.Demo.User || me.Org != "acme" || !me.Operator || !me.Demo {
		t.Fatalf("signed in as %+v", me)
	}
	var projects []apitypes.Project
	if err := v.do(ctx, http.MethodGet, "/projects", nil, &projects); err != nil || len(projects) != 3 {
		t.Fatalf("projects: %v %v", projects, err)
	}
	// View-only: nothing changes, and signing out still works.
	if got := status(v.do(ctx, http.MethodPost, "/projects", apitypes.CreateProjectRequest{Name: "mine"}, nil)); got != http.StatusForbidden {
		t.Fatalf("create a project: %d, want 403", got)
	}
	if err := v.do(ctx, http.MethodPost, "/auth/logout", nil, nil); err != nil {
		t.Fatal(err)
	}
}
