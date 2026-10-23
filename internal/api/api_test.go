package api

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/node/fake"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
)

type fixture struct {
	t       *testing.T
	srv     *httptest.Server
	core    *core.Core
	rt      *fake.Runtime
	dataDir string
	tokens  map[string]string // name → secret
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "dock.db"))
	if err != nil {
		t.Fatal(err)
	}
	box, _ := secrets.New(bytes.Repeat([]byte{1}, secrets.KeySize))
	cfg := config.Default()
	cfg.DataDir = dir
	rt := fake.New()
	c, err := core.New(ctx, core.Options{Config: cfg, Store: st, Secrets: box, Runtime: rt, Logger: slog.New(slog.DiscardHandler), CutoverDrain: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(c, Options{MaxUploadBytes: 1 << 20, Logger: slog.New(slog.DiscardHandler)}))
	t.Cleanup(func() {
		srv.Close()
		c.Close()
		_ = st.Close()
	})
	f := &fixture{t: t, srv: srv, core: c, rt: rt, dataDir: dir, tokens: map[string]string{}}

	for _, p := range []string{"shop", "blog"} {
		if _, err := c.CreateProject(ctx, core.System, apitypes.CreateProjectRequest{Name: p}); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []core.ServiceRef{
		{Project: "shop", Environment: "production", Service: "web"},
		{Project: "shop", Environment: "production", Service: "worker"},
		{Project: "blog", Environment: "production", Service: "api"},
	} {
		if _, err := c.CreateService(ctx, core.System, s.Env(), apitypes.CreateServiceRequest{Name: s.Service, Image: "app:1"}); err != nil {
			t.Fatal(err)
		}
	}
	for name, req := range map[string]apitypes.CreateTokenRequest{
		"admin":       {Scope: apitypes.ScopeAdmin},
		"deploy":      {Scope: apitypes.ScopeDeploy},
		"read":        {Scope: apitypes.ScopeRead},
		"shop-deploy": {Scope: apitypes.ScopeDeploy, Project: "shop"},
	} {
		req.Name = name
		tok, err := c.CreateToken(ctx, core.System, req)
		if err != nil {
			t.Fatal(err)
		}
		f.tokens[name] = tok.Secret
	}
	return f
}

// result is a finished HTTP exchange.
type result struct {
	StatusCode int
	Header     http.Header
}

func (r result) Cookies() []*http.Cookie { return (&http.Response{Header: r.Header}).Cookies() }

func (f *fixture) do(token, method, path string, body any) (result, []byte) {
	f.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+f.tokens[token])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return result{StatusCode: resp.StatusCode, Header: resp.Header}, b
}

const shopProd = "/api/v1/projects/shop/environments/production"

func TestPermissionMatrix(t *testing.T) {
	f := newFixture(t)
	defer f.rt.HoldHealthChecks()() // keep deploys in flight
	web, worker := shopProd+"/services/web", shopProd+"/services/worker"
	blogAPI := "/api/v1/projects/blog/environments/production/services/api"
	cases := []struct {
		name, token, method, path string
		body                      any
		status                    int
		code                      apitypes.ErrorCode
		detail                    string
	}{
		{"no credentials", "", "GET", "/api/v1/projects", nil, 401, apitypes.CodeUnauthorized, "authentication required"},
		{"read can list", "read", "GET", shopProd + "/services", nil, 200, "", ""},
		{"read can't deploy", "read", "POST", web + "/releases", apitypes.DeployImageRequest{}, 403, apitypes.CodeForbidden, "requires deploy"},
		{"read can't stage variables", "read", "PATCH", shopProd + "/variables", apitypes.SetVariablesRequest{Set: map[string]string{"A": "b"}}, 403, apitypes.CodeForbidden, "requires deploy"},
		{"deploy can deploy", "deploy", "POST", web + "/releases", apitypes.DeployImageRequest{}, 202, "", ""},
		{"one deploy at a time", "deploy", "POST", web + "/releases", apitypes.DeployImageRequest{}, 409, apitypes.CodeDeployInProgress, "already deploying"},
		{"deploy can't archive", "deploy", "POST", worker + "/archive", nil, 403, apitypes.CodeForbidden, "archiving a service requires admin"},
		{"deploy can list variable names", "deploy", "GET", shopProd + "/variables", nil, 200, "", ""},
		{"deploy can't reveal secrets", "deploy", "GET", shopProd + "/variables?reveal=true", nil, 403, apitypes.CodeForbidden, "revealing secret values requires admin"},
		{"deploy can't manage tokens", "deploy", "GET", "/api/v1/tokens", nil, 403, apitypes.CodeForbidden, "requires admin"},
		{"limited token can't read another project", "shop-deploy", "GET", blogAPI, nil, 403, apitypes.CodeForbidden, "limited to project shop; it cannot access project blog"},
		{"limited token can't deploy another project", "shop-deploy", "POST", blogAPI + "/releases", apitypes.DeployImageRequest{}, 403, apitypes.CodeForbidden, "limited to project shop"},
		{"limited token deploys its project", "shop-deploy", "POST", worker + "/releases", apitypes.DeployImageRequest{}, 202, "", ""},
		{"limited token can't create projects", "shop-deploy", "POST", "/api/v1/projects", apitypes.CreateProjectRequest{Name: "new"}, 403, apitypes.CodeForbidden, "unlimited"},
		{"admin can reveal", "admin", "GET", shopProd + "/variables?reveal=true", nil, 200, "", ""},
		{"not found", "admin", "GET", shopProd + "/services/nope", nil, 404, apitypes.CodeNotFound, `service "nope" not found`},
		{"validation", "admin", "POST", "/api/v1/projects", apitypes.CreateProjectRequest{Name: "Bad Name"}, 422, apitypes.CodeValidation, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := f.do(tc.token, tc.method, tc.path, tc.body)
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d: %s", resp.StatusCode, tc.status, body)
			}
			if tc.code == "" {
				return
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("content type %q", ct)
			}
			var p apitypes.Error
			if err := json.Unmarshal(body, &p); err != nil {
				t.Fatal(err)
			}
			if p.Code != tc.code || p.Status != tc.status || !strings.Contains(p.Detail, tc.detail) {
				t.Fatalf("problem = %+v, want code %s and detail containing %q", p, tc.code, tc.detail)
			}
		})
	}

	// Mutations are audited with the actor.
	resp, body := f.do("admin", "GET", "/api/v1/audit?action=release.deploy", nil)
	var audit []apitypes.AuditEvent
	_ = json.Unmarshal(body, &audit)
	if resp.StatusCode != 200 || len(audit) != 2 || audit[0].Actor != "shop-deploy" || audit[0].ActorKind != "token" {
		t.Fatalf("audit = %d %+v", resp.StatusCode, audit)
	}
}

func TestCaddyAsk(t *testing.T) {
	f := newFixture(t)
	ask := httptest.NewServer(Internal(f.core, slog.Default(), nil))
	defer ask.Close()
	check := func(host string) int {
		resp, err := http.Get(ask.URL + "/internal/caddy/ask?domain=" + host)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if resp, body := f.do("deploy", "POST", shopProd+"/services/web/domains", apitypes.AddDomainRequest{Hostname: "shop.example.com"}); resp.StatusCode != 201 {
		t.Fatalf("add domain: %d %s", resp.StatusCode, body)
	}
	if got := check("shop.example.com"); got != 200 {
		t.Fatalf("known hostname: %d", got)
	}
	if got := check("evil.example.com"); got != 404 {
		t.Fatalf("unknown hostname: %d", got)
	}
	if resp, body := f.do("admin", "POST", shopProd+"/services/web/archive", nil); resp.StatusCode != 200 {
		t.Fatalf("archive: %d %s", resp.StatusCode, body)
	}
	if got := check("shop.example.com"); got != 404 {
		t.Fatalf("archived service's hostname: %d", got)
	}
}

// The upload must reach disk while the client is still sending, i.e. it is
// streamed, never buffered whole in memory.
func TestSourceUploadStreamsToDisk(t *testing.T) {
	f := newFixture(t)
	var tarball bytes.Buffer
	tw := tar.NewWriter(&tarball)
	content := bytes.Repeat([]byte("x"), 256<<10)
	_ = tw.WriteHeader(&tar.Header{Name: "big.bin", Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(content)
	_ = tw.Close()
	data := tarball.Bytes()

	pr, pw := io.Pipe()
	req, _ := http.NewRequest("POST", f.srv.URL+shopProd+"/services/web/releases/source", pr)
	req.Header.Set("Authorization", "Bearer "+f.tokens["deploy"])
	req.Header.Set("Content-Type", "application/x-tar")
	type uploaded struct {
		status int
		rel    apitypes.Release
	}
	done := make(chan uploaded, 1)
	go func() {
		var u uploaded
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			done <- u
			return
		}
		defer func() { _ = resp.Body.Close() }()
		u.status = resp.StatusCode
		_ = json.NewDecoder(resp.Body).Decode(&u.rel)
		done <- u
	}()

	half := len(data) / 2
	if _, err := pw.Write(data[:half]); err != nil {
		t.Fatal(err)
	}
	uploads := filepath.Join(f.dataDir, "uploads")
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, _ := os.ReadDir(uploads)
		if len(entries) == 1 {
			if info, _ := entries[0].Info(); info != nil && info.Size() >= int64(half)/2 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("upload never reached disk while streaming")
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, _ = pw.Write(data[half:])
	_ = pw.Close()

	if u := <-done; u.status != 202 || u.rel.Source != apitypes.SourceUpload {
		t.Fatalf("upload: %d %+v", u.status, u.rel)
	}
	// The build receives the whole stream, and the upload is removed after.
	deadline = time.Now().Add(5 * time.Second)
	for {
		_, logBody := f.do("read", "GET", shopProd+"/services/web/releases/1/build-log", nil)
		entries, _ := os.ReadDir(uploads)
		if strings.Contains(string(logBody), "Received "+strconv.Itoa(len(data))+" bytes") && len(entries) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("build log %q, uploads left %d", logBody, len(entries))
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Over the limit: 413 and nothing left behind.
	big := bytes.NewReader(make([]byte, 2<<20))
	req, _ = http.NewRequest("POST", f.srv.URL+shopProd+"/services/worker/releases/source", big)
	req.Header.Set("Authorization", "Bearer "+f.tokens["deploy"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	entries, _ := os.ReadDir(uploads)
	if resp.StatusCode != http.StatusRequestEntityTooLarge || len(entries) != 0 {
		t.Fatalf("oversized upload: %d, %d files left", resp.StatusCode, len(entries))
	}
}

func TestLoginSessionAndEvents(t *testing.T) {
	f := newFixture(t)
	if _, err := f.core.CreateUser(context.Background(), core.System, apitypes.CreateUserRequest{
		Email: "ada@example.com", Name: "Ada", Password: "correct horse battery", Role: apitypes.ScopeAdmin,
	}); err != nil {
		t.Fatal(err)
	}
	resp, body := f.do("", "POST", "/api/v1/auth/login", apitypes.LoginRequest{Email: "ada@example.com", Password: "wrong password!"})
	if resp.StatusCode != 401 {
		t.Fatalf("wrong password: %d %s", resp.StatusCode, body)
	}
	resp, _ = f.do("", "POST", "/api/v1/auth/login", apitypes.LoginRequest{Email: "ada@example.com", Password: "correct horse battery"})
	cookies := resp.Cookies()
	if resp.StatusCode != 200 || len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("login: %d %+v", resp.StatusCode, cookies)
	}

	// The session cookie authenticates the event stream.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", f.srv.URL+"/api/v1/events?project=shop", nil)
	req.AddCookie(cookies[0])
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Body.Close() }()
	if stream.StatusCode != 200 || stream.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("events: %d %s", stream.StatusCode, stream.Header.Get("Content-Type"))
	}
	f.do("deploy", "PATCH", shopProd+"/variables", apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"A": "1"}})

	sc := bufio.NewScanner(stream.Body)
	var name string
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "event: "); ok {
			name = v
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok && name == "audit" {
			var ev apitypes.Event
			if err := json.Unmarshal([]byte(v), &ev); err != nil {
				t.Fatal(err)
			}
			if ev.Type != apitypes.EventAudit || ev.Audit.Action != "variables.set" || ev.Project != "shop" {
				t.Fatalf("event = %+v", ev)
			}
			return
		}
	}
	t.Fatal("stream ended without the audit event")
}

// The TOML export is the CLI's output as a download, and the same config
// as the JSON export.
func TestExportTOML(t *testing.T) {
	f := newFixture(t)
	resp, body := f.do("read", "GET", "/api/v1/projects/shop/export.toml", nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Disposition") != `attachment; filename="shop.dock.toml"` {
		t.Fatalf("status %d, headers %v: %s", resp.StatusCode, resp.Header, body)
	}
	var fromTOML, fromJSON apitypes.ProjectExport
	if err := toml.Unmarshal(body, &fromTOML); err != nil {
		t.Fatal(err)
	}
	_, jsonBody := f.do("read", "GET", "/api/v1/projects/shop/export", nil)
	if err := json.Unmarshal(jsonBody, &fromJSON); err != nil {
		t.Fatal(err)
	}
	if len(fromTOML.Environments) != 1 || len(fromTOML.Environments[0].Services) != 2 || fromTOML.Project != fromJSON.Project || len(fromJSON.Environments[0].Services) != 2 {
		t.Fatalf("toml %+v, json %+v", fromTOML, fromJSON)
	}
	if resp, _ := f.do("shop-deploy", "GET", "/api/v1/projects/blog/export.toml", nil); resp.StatusCode != 403 {
		t.Fatalf("another project's token: status %d", resp.StatusCode)
	}
}
