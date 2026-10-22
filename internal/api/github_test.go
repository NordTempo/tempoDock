package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
)

// The API connects with deploy scope and never returns the token; the
// hooks listener checks signatures and caps bodies.
func TestGitEndpoints(t *testing.T) {
	f := newFixture(t)
	git := shopProd + "/services/web/git"
	token := "github_pat_xyz"
	req := apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main", Token: &token}
	if res, _ := f.do("read", http.MethodPut, git, req); res.StatusCode != http.StatusForbidden {
		t.Fatalf("read token connects: %d", res.StatusCode)
	}
	res, body := f.do("deploy", http.MethodPut, git, req)
	var conn apitypes.GitConnectResult
	_ = json.Unmarshal(body, &conn)
	if res.StatusCode != http.StatusOK || conn.Secret == "" || !conn.Connection.TokenSet || conn.HooksHint == "" {
		t.Fatalf("connect: %d %s", res.StatusCode, body)
	}
	res, body = f.do("read", http.MethodGet, git, nil)
	if res.StatusCode != http.StatusOK || bytes.Contains(body, []byte(token)) || bytes.Contains(body, []byte(conn.Secret)) || !bytes.Contains(body, []byte(`"token_set":true`)) {
		t.Fatalf("status: %d %s", res.StatusCode, body)
	}
	if res, _ := f.do("deploy", http.MethodDelete, git, nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("disconnect: %d", res.StatusCode)
	}
}

func TestHooksHandler(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "dock.db"))
	if err != nil {
		t.Fatal(err)
	}
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
	srv := httptest.NewServer(Hooks(c, slog.New(slog.DiscardHandler)))
	t.Cleanup(func() {
		srv.Close()
		c.Close()
		_ = st.Close()
	})
	if _, err := c.CreateProject(ctx, core.System, apitypes.CreateProjectRequest{Name: "shop"}); err != nil {
		t.Fatal(err)
	}
	web := core.ServiceRef{Project: "shop", Environment: "production", Service: "web"}
	if _, err := c.CreateService(ctx, core.System, web.Env(), apitypes.CreateServiceRequest{Name: "web"}); err != nil {
		t.Fatal(err)
	}
	conn, err := c.GitConnect(ctx, core.System, web, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	hookPath := "/hooks/github/" + path.Base(conn.Connection.WebhookURL)

	post := func(p, event, sig string, body []byte) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+p, bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", event)
		req.Header.Set("X-GitHub-Delivery", "d-1")
		if sig != "" {
			req.Header.Set("X-Hub-Signature-256", sig)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(b))
	}
	body := []byte(`{"zen":"Keep it logically awesome."}`)
	mac := hmac.New(sha256.New, []byte(conn.Secret))
	mac.Write(body)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	for name, tc := range map[string]struct {
		path, sig string
		body      []byte
		status    int
		says      string
	}{
		"good":          {hookPath, good, body, http.StatusOK, "pong"},
		"missing":       {hookPath, "", body, http.StatusUnauthorized, "signature missing or wrong"},
		"bad":           {hookPath, "sha256=" + strings.Repeat("0", 64), body, http.StatusUnauthorized, "signature"},
		"tampered body": {hookPath, good, append(body, ' '), http.StatusUnauthorized, "signature"},
		"unknown hook":  {"/hooks/github/nope", good, body, http.StatusNotFound, "no such webhook"},
		"too large":     {hookPath, good, bytes.Repeat([]byte("x"), maxHookBody+1), http.StatusRequestEntityTooLarge, "4 MB"},
	} {
		if status, says := post(tc.path, "ping", tc.sig, tc.body); status != tc.status || !strings.Contains(says, tc.says) {
			t.Errorf("%s: %d %q, want %d %q", name, status, says, tc.status, tc.says)
		}
	}
	// Nothing but webhook deliveries is served.
	for _, p := range []string{"/api/v1/health", "/debug/pprof/", "/internal/caddy/ask?domain=x"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: %d", p, resp.StatusCode)
		}
	}
}
