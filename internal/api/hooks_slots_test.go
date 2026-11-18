package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/node/fake"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/store/storetest"
)

// A webhook's deliveries take turns before they take one of the server's
// read slots, and a delivery that sends nothing for hookReadIdle is cut.
// So deliveries trickled to one webhook, by whoever knows its address,
// hold up that webhook's and nobody else's: another organization's
// delivery is answered at once.
func TestOneWebhooksDeliveriesHoldUpNoOthers(t *testing.T) {
	old := hookReadIdle
	hookReadIdle = 300 * time.Millisecond
	t.Cleanup(func() { hookReadIdle = old })
	ctx := context.Background()
	dir := t.TempDir()
	st := storetest.Open(t, filepath.Join(dir, "dock.db"))
	box, _ := secrets.New(bytes.Repeat([]byte{1}, secrets.KeySize))
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.Hooks.PublicHost = "hooks.example.com"
	c, err := core.New(ctx, core.Options{Config: cfg, Store: st, Secrets: box, Runtime: fake.New(), Logger: slog.New(slog.DiscardHandler), CutoverDrain: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	hooks := httptest.NewServer(Hooks(c, slog.New(slog.DiscardHandler)))
	t.Cleanup(func() {
		hooks.Close()
		c.Close()
		_ = st.Close()
	})
	// Two orgs, each with a service and a webhook connection of its own.
	connect := func(a core.Actor, project, repo string) apitypes.GitConnectResult {
		t.Helper()
		if _, err := c.CreateProject(ctx, a, apitypes.CreateProjectRequest{Name: project}); err != nil {
			t.Fatal(err)
		}
		web := core.ServiceRef{Project: project, Environment: "production", Service: "web"}
		if _, err := c.CreateService(ctx, a, web.Env(), apitypes.CreateServiceRequest{Name: "web"}); err != nil {
			t.Fatal(err)
		}
		conn, err := c.GitConnect(ctx, a, web, apitypes.GitConnectRequest{Repo: repo, Branch: "main"})
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
	mine := connect(sys, "shop", "acme/site")
	org, err := st.CreateOrg(ctx, store.CreateOrgParams{Slug: "other", Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	theirs := connect(core.System.InOrg(org.ID), "theirs", "acme/shop")

	// More trickling deliveries to one webhook than there are read slots.
	var slow []*io.PipeWriter
	ended := make(chan int, maxHookReads+2)
	for range maxHookReads + 2 {
		pr, pw := io.Pipe()
		slow = append(slow, pw)
		req, _ := http.NewRequest(http.MethodPost, hooks.URL+"/hooks/github/"+path.Base(mine.Connection.WebhookURL), pr)
		// Shaped like a signature: one that isn't is refused unread.
		req.Header.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64))
		go func() {
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				ended <- 0
				return
			}
			_ = resp.Body.Close()
			ended <- resp.StatusCode
		}()
		_, _ = pw.Write([]byte("{"))
	}
	t.Cleanup(func() {
		for _, pw := range slow {
			_ = pw.Close()
		}
	})

	body := []byte(`{"zen":"Keep it logically awesome."}`)
	mac := hmac.New(sha256.New, []byte(theirs.Secret))
	mac.Write(body)
	done := make(chan int, 1)
	start := time.Now()
	go func() {
		req, _ := http.NewRequest(http.MethodPost, hooks.URL+"/hooks/github/"+path.Base(theirs.Connection.WebhookURL), bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", "ping")
		req.Header.Set("X-GitHub-Delivery", "d-1")
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- 0
			return
		}
		_ = resp.Body.Close()
		done <- resp.StatusCode
	}()
	select {
	case status := <-done:
		if status != http.StatusOK {
			t.Fatalf("another webhook's delivery was answered %d", status)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("another webhook's delivery still waits after %s behind one webhook's trickling ones", time.Since(start).Round(time.Millisecond))
	}
	// Each trickling delivery is cut after hookReadIdle without a byte,
	// one after the other.
	for range slow {
		select {
		case status := <-ended:
			if status != http.StatusBadRequest {
				t.Fatalf("a trickling delivery was answered %d", status)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a trickling delivery is still held open")
		}
	}
}
