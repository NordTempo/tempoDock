package mcp_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/NordTempo/tempoDock/internal/api"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/fake"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
)

// fixture is a daemon on the fake runtime with two projects:
// shop (production, staging) and blog (production), and tokens.
type fixture struct {
	t   testing.TB
	srv *httptest.Server
	// handler is the daemon's root handler, which srv serves.
	handler http.Handler
	core    *core.Core
	rt      *fake.Runtime
	tokens  map[string]string
}

// sys is the system actor in the test org, the first one created.
var sys = core.System.InOrg(1)

func newFixture(t testing.TB) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "dock.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOrg(ctx, store.CreateOrgParams{Slug: "test", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	box, _ := secrets.New(bytes.Repeat([]byte{1}, secrets.KeySize))
	cfg := config.Default()
	cfg.DataDir = dir
	// The analytics store runs from a binary the fake only names.
	cfg.Analytics.StoreBinary = filepath.Join(dir, "dock-analytics")
	if err := os.WriteFile(cfg.Analytics.StoreBinary, []byte("store"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := fake.New()
	quiet := slog.New(slog.DiscardHandler)
	c, err := core.New(ctx, core.Options{Config: cfg, Store: st, Secrets: box, Runtime: rt, Logger: quiet, CutoverDrain: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	h := api.New(c, api.Options{MaxUploadBytes: 1 << 20, Logger: quiet})
	srv := httptest.NewServer(h)
	t.Cleanup(func() {
		srv.Close()
		c.Close()
		_ = st.Close()
	})
	f := &fixture{t: t, srv: srv, handler: h, core: c, rt: rt, tokens: map[string]string{}}
	for _, p := range []string{"shop", "blog"} {
		if _, err := c.CreateProject(ctx, sys, apitypes.CreateProjectRequest{Name: p}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.CreateEnvironment(ctx, sys, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []struct {
		ref   core.ServiceRef
		image string
	}{
		{core.ServiceRef{Project: "shop", Environment: "staging", Service: "web"}, "app:1"},
		{core.ServiceRef{Project: "shop", Environment: "staging", Service: "db"}, "postgres:17"},
		{core.ServiceRef{Project: "blog", Environment: "production", Service: "api"}, "app:1"},
	} {
		if _, err := c.CreateService(ctx, sys, s.ref.Env(), apitypes.CreateServiceRequest{Name: s.ref.Service, Image: s.image}); err != nil {
			t.Fatal(err)
		}
	}
	for name, req := range map[string]apitypes.CreateTokenRequest{
		"shop-agent": {Scope: apitypes.ScopeDeploy, Project: "shop", Environment: "staging"},
		"admin":      {Scope: apitypes.ScopeAdmin},
	} {
		req.Name = name
		tok, err := c.CreateToken(ctx, sys, req)
		if err != nil {
			t.Fatal(err)
		}
		f.tokens[name] = tok.Secret
	}
	return f
}

// bearer adds a token to every request.
type bearer struct {
	token string
	base  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

// connect opens an MCP client session as the named token, the way Claude
// Code does: Streamable HTTP with an Authorization header.
func (f *fixture) connect(token string, opts *sdk.ClientOptions) *sdk.ClientSession {
	f.t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, opts)
	tr := &sdk.StreamableClientTransport{
		Endpoint:             f.srv.URL + "/api/v1/mcp",
		HTTPClient:           &http.Client{Transport: bearer{f.tokens[token], http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}
	s, err := client.Connect(context.Background(), tr, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = s.Close() })
	return s
}

// called is a tool result split into its summary and JSON.
type called struct {
	isError bool
	summary string
	json    string
}

func (c called) decode(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(c.json), v); err != nil {
		t.Fatalf("decode %q: %v", c.json, err)
	}
}

func call(t *testing.T, s *sdk.ClientSession, name string, args map[string]any) called {
	t.Helper()
	res, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return split(t, res)
}

func split(t *testing.T, res *sdk.CallToolResult) called {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("want one content block, got %d", len(res.Content))
	}
	text := res.Content[0].(*sdk.TextContent).Text
	summary, js, _ := strings.Cut(text, "\n\n")
	return called{isError: res.IsError, summary: summary, json: js}
}

// errorCode is the code of a tool error.
func (c called) errorCode(t *testing.T) apitypes.ErrorCode {
	t.Helper()
	if !c.isError {
		t.Fatalf("want a tool error, got %q", c.summary)
	}
	var e struct {
		Error struct{ Code apitypes.ErrorCode }
	}
	c.decode(t, &e)
	return e.Error.Code
}

type release struct {
	Service, Status, Error string
	Number                 int64
	Events                 []string
}

func TestScopedToken(t *testing.T) {
	f := newFixture(t)
	s := f.connect("shop-agent", nil)

	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	for _, want := range []string{"whoami", "deploy_image", "rollback", "promote", "set_variables", "apply_changes", "data_query", "propose_data_changes"} {
		if !slices.Contains(names, want) {
			t.Errorf("tools/list lacks %s: %v", want, names)
		}
	}
	for _, name := range names {
		if strings.Contains(name, "archive") || strings.Contains(name, "token") || strings.Contains(name, "revoke") {
			t.Errorf("admin tool %s is exposed", name)
		}
	}

	var me apitypes.Me
	who := call(t, s, "whoami", nil)
	who.decode(t, &me)
	if me.Name != "shop-agent" || me.Scope != apitypes.ScopeDeploy || me.Project != "shop" || me.Environment != "staging" {
		t.Errorf("whoami = %+v", me)
	}

	// Project and environment default to the token's limits.
	var rel release
	dep := call(t, s, "deploy_image", map[string]any{"service": "web", "image": "app:2"})
	dep.decode(t, &rel)
	if dep.isError || rel.Status != "active" || rel.Number != 1 {
		t.Fatalf("deploy_image = %q %+v", dep.summary, rel)
	}

	// Anything outside shop/staging is refused by core, as over the API.
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"deploy_image", map[string]any{"project": "blog", "environment": "production", "service": "api"}},
		{"service_status", map[string]any{"project": "blog", "environment": "production", "service": "api"}},
		{"set_variables", map[string]any{"environment": "production", "set": map[string]any{"A": "1"}}},
		{"set_pre_deploy_command", map[string]any{"project": "blog", "environment": "production", "service": "api", "command": "x"}},
		{"list_variables", map[string]any{"reveal": true}},
	} {
		if code := call(t, s, tc.tool, tc.args).errorCode(t); code != apitypes.CodeForbidden {
			t.Errorf("%s %v: code %s, want forbidden", tc.tool, tc.args, code)
		}
	}

	// The pre-deploy command is staged like a variable.
	pre := call(t, s, "set_pre_deploy_command", map[string]any{"service": "web", "command": "migrate up", "timeout_seconds": 900})
	if pre.isError || !strings.Contains(pre.summary, `"migrate up"`) {
		t.Fatalf("set_pre_deploy_command = %q", pre.summary)
	}
	if bad := call(t, s, "set_pre_deploy_command", map[string]any{"service": "web", "command": "x", "timeout_seconds": 99999}); !bad.isError {
		t.Fatalf("a timeout over an hour was accepted: %q", bad.summary)
	}

	// Variables: names only, never values.
	set := call(t, s, "set_variables", map[string]any{"service": "web", "set": map[string]any{"STRIPE_KEY": "sk_live_secret"}})
	if set.isError || strings.Contains(set.summary+set.json, "sk_live_secret") {
		t.Fatalf("set_variables leaked the value or failed: %q %s", set.summary, set.json)
	}
	vars := call(t, s, "list_variables", nil)
	if vars.isError || !strings.Contains(vars.json, "STRIPE_KEY") || strings.Contains(vars.json, "sk_live_secret") {
		t.Errorf("list_variables = %s", vars.json)
	}

	// Data tools follow the policy: tokens get none by default.
	var info apitypes.DataInfo
	call(t, s, "data_status", map[string]any{"service": "db"}).decode(t, &info)
	if info.Access.Level != apitypes.LevelNone || len(info.Access.Why) == 0 {
		t.Errorf("data_status access = %+v", info.Access)
	}
	for _, tool := range []string{"data_schema", "data_query"} {
		args := map[string]any{"service": "db", "sql": "select 1"}
		if tool == "data_schema" {
			delete(args, "sql")
		}
		if code := call(t, s, tool, args).errorCode(t); code != apitypes.CodeForbidden {
			t.Errorf("%s: code %s, want forbidden", tool, code)
		}
	}

	// Invalid input is rejected before core sees it.
	if res := call(t, s, "logs", map[string]any{"service": "web", "tail": 5000}); !res.isError {
		t.Errorf("logs with tail 5000 = %q", res.summary)
	}

	// Mutating calls are audited like the API, with the token as actor.
	evs, err := f.core.ListAudit(context.Background(), sys, core.AuditFilter{Project: "shop", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]string{}
	for _, e := range evs {
		actions[e.Action] = e.Actor
	}
	for _, a := range []string{"release.deploy", "variables.set"} {
		if actions[a] != "shop-agent" {
			t.Errorf("audit %s by %q, want shop-agent (events %v)", a, actions[a], actions)
		}
	}
}

func TestDeployWaitsWithProgress(t *testing.T) {
	f := newFixture(t)
	var mu sync.Mutex
	var progress []string
	got := make(chan struct{}, 64)
	s := f.connect("shop-agent", &sdk.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *sdk.ProgressNotificationClientRequest) {
			mu.Lock()
			progress = append(progress, r.Params.Message)
			mu.Unlock()
			got <- struct{}{}
		},
	})
	hold := f.rt.HoldHealthChecks()
	defer hold()

	params := &sdk.CallToolParams{Name: "deploy_image", Arguments: map[string]any{"service": "web"}}
	params.SetProgressToken("deploy-1")
	done := make(chan *sdk.CallToolResult, 1)
	go func() {
		res, err := s.CallTool(context.Background(), params)
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()

	// The release waits on its health check; the tool keeps waiting and
	// reports the steps so far as progress.
	select {
	case <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("no progress notification while the release health checks")
	}
	select {
	case res := <-done:
		t.Fatalf("returned before the release finished: %v", split(t, res).summary)
	case <-time.After(50 * time.Millisecond):
	}
	hold()

	var res *sdk.CallToolResult
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deploy_image didn't return after the health check passed")
	}
	var rel release
	out := split(t, res)
	out.decode(t, &rel)
	if rel.Status != "active" || len(rel.Events) == 0 {
		t.Errorf("deploy_image = %q %+v", out.summary, rel)
	}
	// The client handles notifications on its own goroutine, so the last
	// one can land just after the result.
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		ok := slices.ContainsFunc(progress, func(m string) bool { return strings.HasPrefix(m, "web #1: ") }) &&
			slices.Contains(progress, "release 1 of web: active")
		seen := slices.Clone(progress)
		mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("progress = %q", seen)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDeployTimeoutAndFailure(t *testing.T) {
	f := newFixture(t)
	s := f.connect("shop-agent", nil)

	hold := f.rt.HoldHealthChecks()
	var rel release
	res := call(t, s, "deploy_image", map[string]any{"service": "web", "wait_seconds": 1})
	res.decode(t, &rel)
	if rel.Status != string(apitypes.ReleaseHealthChecking) || !strings.Contains(res.summary, "Still deploying") {
		t.Errorf("deploy_image after the wait = %q %+v", res.summary, rel)
	}
	hold()
	waitFor(t, func() bool {
		r, err := f.core.GetRelease(context.Background(), sys, core.ServiceRef{Project: "shop", Environment: "staging", Service: "web"}, 1)
		return err == nil && r.Status.Done()
	})

	// A failed health check is a result, not a tool error: the agent gets
	// the reason and what the container wrote, and must not roll back.
	hold = f.rt.HoldHealthChecks()
	done := make(chan *sdk.CallToolResult, 1)
	go func() {
		r, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: "deploy_image", Arguments: map[string]any{"service": "web"}})
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	waitFor(t, func() bool { return slices.Contains(f.rt.Running(), "dock_p1_e3_s1_r2") })
	f.rt.AddLogs("dock_p1_e3_s1_r2", "panic: DATABASE_URL is not set")
	f.rt.FailHealthCheck(func(node.ContainerSpec) error { return errors.New("connection refused") })
	hold()
	res = split(t, <-done)
	var failed struct {
		release
		Logs struct{ Container []string }
	}
	res.decode(t, &failed)
	rel = failed.release
	if res.isError || rel.Status != "failed" || rel.Error == "" || !strings.Contains(res.summary, "don't roll back") ||
		len(failed.Logs.Container) != 1 || !strings.HasSuffix(failed.Logs.Container[0], " panic: DATABASE_URL is not set") {
		t.Errorf("failed deploy = %q %s", res.summary, res.json)
	}
	det := call(t, s, "release_details", map[string]any{"service": "web"})
	var got release
	det.decode(t, &got)
	if got.Number != rel.Number || got.Status != "failed" {
		t.Errorf("release_details = %q %+v", det.summary, got)
	}
}

func TestDocsResources(t *testing.T) {
	f := newFixture(t)
	s := f.connect("shop-agent", nil)
	list, err := s.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var agents *sdk.Resource
	for _, r := range list.Resources {
		if r.URI == "dock://docs/agents" {
			agents = r
		}
	}
	if agents == nil || agents.Description == "" || agents.Title != "Agents" {
		t.Fatalf("dock://docs/agents = %+v", agents)
	}
	res, err := s.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: agents.URI})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Contents) != 1 || !strings.HasPrefix(res.Contents[0].Text, "# Agents") {
		t.Errorf("read dock://docs/agents = %+v", res.Contents)
	}
}

// post sends one JSON-RPC message as a bare HTTP client would and returns
// the status and the first JSON-RPC message of the response.
func post(t *testing.T, f *fixture, token, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/api/v1/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+f.tokens[token])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok && data != "" {
			return resp.StatusCode, data
		}
	}
	return resp.StatusCode, ""
}

func TestStatelessHTTP(t *testing.T) {
	f := newFixture(t)
	if code, _ := post(t, f, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); code != http.StatusUnauthorized {
		t.Errorf("no token: status %d", code)
	}
	f.tokens["bogus"] = "dock_bogus"
	if code, _ := post(t, f, "bogus", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); code != http.StatusUnauthorized {
		t.Errorf("bad token: status %d", code)
	}

	code, body := post(t, f, "shop-agent", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}`)
	var init struct {
		Result struct {
			ProtocolVersion string
			ServerInfo      struct{ Name string }
			Instructions    string
		}
	}
	if err := json.Unmarshal([]byte(body), &init); err != nil || code != http.StatusOK {
		t.Fatalf("initialize: %d %s", code, body)
	}
	if init.Result.ProtocolVersion != "2025-06-18" || init.Result.ServerInfo.Name != "tempo-dock" || !strings.Contains(init.Result.Instructions, "whoami") {
		t.Errorf("initialize = %+v", init.Result)
	}

	// No session: a call works without a prior initialize on this connection.
	code, body = post(t, f, "shop-agent", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"whoami","arguments":{}}}`)
	if code != http.StatusOK || !strings.Contains(body, `scope deploy`) {
		t.Errorf("tools/call whoami: %d %s", code, body)
	}
}

// TestNoGoroutinesLeft checks that calls, including one that waits on the
// event bus, leave nothing running once the client has closed its
// connections.
func TestNoGoroutinesLeft(t *testing.T) {
	f := newFixture(t)
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"service_status","arguments":{"service":"web"}}}`
	post(t, f, "shop-agent", call) // warm up: build the server, open pools
	http.DefaultClient.CloseIdleConnections()
	before := settledGoroutines()

	for range 5 {
		post(t, f, "shop-agent", call)
	}
	post(t, f, "shop-agent", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"deploy_image","arguments":{"service":"web"}}}`)
	http.DefaultClient.CloseIdleConnections()
	waitFor(t, func() bool {
		r, err := f.core.GetRelease(context.Background(), sys, core.ServiceRef{Project: "shop", Environment: "staging", Service: "web"}, 1)
		return err == nil && r.Status.Done()
	})
	if after := settledGoroutines(); after > before {
		buf := make([]byte, 1<<16)
		t.Errorf("goroutines: %d before, %d after\n%s", before, after, buf[:runtime.Stack(buf, true)])
	}
}

// settledGoroutines counts goroutines once their number stops falling.
func settledGoroutines() int {
	n := runtime.NumGoroutine()
	for range 50 {
		time.Sleep(20 * time.Millisecond)
		m := runtime.NumGoroutine()
		if m >= n {
			return m
		}
		n = m
	}
	return n
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The analytics tools read a site as the Analytics tab does, within the
// token's limits.
func TestAnalyticsTools(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	web := core.ServiceRef{Project: "shop", Environment: "staging", Service: "web"}
	if _, err := f.core.DeployImage(ctx, sys, web, apitypes.DeployImageRequest{Image: "app:1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.core.AddDomain(ctx, sys, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.core.EnableHostnameAnalytics(ctx, sys, "shop.example.com", apitypes.EnableAnalyticsRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.core.AddSiteGoal(ctx, sys, "shop", "shop.example.com", apitypes.CreateGoalRequest{Name: "Signup",
		GoalStep: apitypes.GoalStep{Kind: "event", Match: "signup"}}); err != nil {
		t.Fatal(err)
	}
	// The store starts in the background.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if st, err := f.core.SiteStats(ctx, sys, "shop", "shop.example.com", core.StatsQuery{}); err == nil && st.StoreOnline {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the analytics store didn't start")
		}
	}
	s := f.connect("shop-agent", nil)
	if c := call(t, s, "analytics_sites", nil); c.isError || !strings.Contains(c.summary, "shop.example.com (staging)") {
		t.Errorf("analytics_sites = %q", c.summary)
	}
	for tool, args := range map[string]map[string]any{
		"analytics_stats":     {"site": "shop.example.com", "range": "7d"},
		"analytics_breakdown": {"site": "shop.example.com", "dimension": "referrer"},
		"analytics_errors":    {"site": "shop.example.com", "filters": []string{"device:is:mobile"}},
		"analytics_vitals":    {"site": "shop.example.com", "by": []string{"page"}, "metric": "lcp"},
		"analytics_replays":   {"site": "shop.example.com", "errors_only": true},
		"analytics_goals":     {"site": "shop.example.com"},
		"analytics_funnel":    {"site": "shop.example.com"},
	} {
		if c := call(t, s, tool, args); c.isError {
			t.Errorf("%s = %q", tool, c.summary)
		}
	}
	if c := call(t, s, "analytics_goals", map[string]any{"site": "shop.example.com"}); !strings.Contains(c.summary, "1 goals") {
		t.Errorf("analytics_goals = %q", c.summary)
	}
	if code := call(t, s, "analytics_stats", map[string]any{"site": "shop.example.com", "filters": []string{"nope"}}).errorCode(t); code != apitypes.CodeValidation {
		t.Errorf("a malformed filter: %s", code)
	}
	if code := call(t, s, "analytics_stats", map[string]any{"project": "blog", "site": "x"}).errorCode(t); code != apitypes.CodeForbidden && code != apitypes.CodeNotFound {
		t.Errorf("another project's site: %s", code)
	}
}
