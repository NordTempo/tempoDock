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
	"golang.org/x/crypto/bcrypt"

	"github.com/NordTempo/tempoDock/internal/api"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/fake"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store/storetest"
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
	st := storetest.Open(t, filepath.Join(dir, "dock.db"))
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
	c, err := core.New(ctx, core.Options{Config: cfg, Store: st, Secrets: box, Runtime: rt, Logger: quiet, CutoverDrain: time.Nanosecond, PasswordCost: bcrypt.MinCost, ScanQuiet: 20 * time.Millisecond})
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
		"shop-all":   {Scope: apitypes.ScopeDeploy, Project: "shop"},
		"admin":      {Scope: apitypes.ScopeAdmin},
	} {
		req.Name = name
		tok, err := c.CreateToken(ctx, sys, req)
		if err != nil {
			t.Fatal(err)
		}
		f.tokens[name] = tok.Secret
	}
	pt, err := c.CreatePlatformToken(ctx, sys, apitypes.CreatePlatformTokenRequest{Name: "platform", Role: apitypes.PlatformViewer})
	if err != nil {
		t.Fatal(err)
	}
	f.tokens["platform"] = pt.Secret
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
		// The token's limits fill project and environment, so its
		// tools/list leaves them out (calls still take them).
		var schema struct{ Properties map[string]any }
		b, _ := json.Marshal(tl.InputSchema)
		_ = json.Unmarshal(b, &schema)
		if _, ok := schema.Properties["project"]; ok {
			t.Errorf("%s takes project: %s", tl.Name, b)
		}
		if _, ok := schema.Properties["environment"]; ok {
			t.Errorf("%s takes environment: %s", tl.Name, b)
		}
	}
	for _, want := range []string{"whoami", "deploy_image", "rollback", "set_variables", "apply_changes", "data_status"} {
		if !slices.Contains(names, want) {
			t.Errorf("tools/list lacks %s: %v", want, names)
		}
	}
	// Nor tools it can never use: promoting needs a second environment,
	// listing servers an operator, the data tools data access (tokens have
	// none by default; data_status says so).
	for _, hidden := range []string{"promote", "clone_environment", "list_projects", "list_servers", "host_checks", "list_registry_credentials", "data_query", "propose_data_changes"} {
		if slices.Contains(names, hidden) {
			t.Errorf("tools/list shows %s to a token limited to shop/staging", hidden)
		}
	}
	for _, name := range names {
		if strings.Contains(name, "archive") || strings.Contains(name, "token") || strings.Contains(name, "revoke") {
			t.Errorf("admin tool %s is exposed", name)
		}
	}

	// Filters default to the token's limits too: tools/list doesn't offer
	// project or environment, so a service alone must do.
	for _, tool := range []string{"list_errors", "audit_log"} {
		if c := call(t, s, tool, map[string]any{"service": "web"}); c.isError {
			t.Errorf("%s with a service only: %q", tool, c.summary)
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
		{"set_variables", map[string]any{"environment": "production", "set": map[string]any{"A": "1"}}},
	} {
		c := call(t, s, tc.tool, tc.args)
		if code := c.errorCode(t); code != apitypes.CodeForbidden {
			t.Errorf("%s %v: code %s, want forbidden", tc.tool, tc.args, code)
		}
		// The refusal says why and what would allow it (ADR 0080).
		var e struct {
			Error struct{ Refusal *apitypes.Refusal }
		}
		c.decode(t, &e)
		if r := e.Error.Refusal; r == nil || r.DecidedBy != apitypes.RefusedLimit || r.Limit != "shop/staging" || r.Fix == "" {
			t.Errorf("%s: refusal %+v", tc.tool, r)
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
	if !strings.Contains(res.summary, "likely cause: DATABASE_URL isn't set") || !strings.Contains(res.summary, "Call diagnose") {
		t.Errorf("failed deploy's summary = %q", res.summary)
	}

	// diagnose has the cause and its evidence (ADR 0071).
	diag := call(t, s, "diagnose", map[string]any{"service": "web"})
	var d apitypes.Diagnosis
	diag.decode(t, &d)
	if d.Release != rel.Number || d.Cause.Rule != "missing_variable" || len(d.Checked) == 0 ||
		!strings.Contains(diag.summary, "Likely cause: DATABASE_URL isn't set. Fix: dock env set DATABASE_URL=") {
		t.Errorf("diagnose = %q %s", diag.summary, diag.json)
	}
}

// stop_service takes a service offline until start_service, which waits
// for its release.
func TestStopAndStartService(t *testing.T) {
	f := newFixture(t)
	s := f.connect("shop-agent", nil)
	call(t, s, "deploy_image", map[string]any{"service": "web"})

	var stopped struct {
		State     apitypes.ServiceState
		StoppedBy string `json:"stopped_by"`
	}
	stop := call(t, s, "stop_service", map[string]any{"service": "web"})
	stop.decode(t, &stopped)
	if stop.isError || stopped.State != apitypes.ServiceStopped || stopped.StoppedBy != "shop-agent" {
		t.Fatalf("stop_service = %q %s", stop.summary, stop.json)
	}
	if st := call(t, s, "service_status", map[string]any{"service": "web"}); !strings.Contains(st.summary, "is stopped (by shop-agent") || !strings.Contains(st.summary, "keeping release 1") {
		t.Errorf("service_status of a stopped service = %q", st.summary)
	}

	var rel release
	start := call(t, s, "start_service", map[string]any{"service": "web"})
	start.decode(t, &rel)
	if start.isError || rel.Status != "active" || rel.Number != 2 {
		t.Fatalf("start_service = %q %+v", start.summary, rel)
	}
	if code := call(t, s, "start_service", map[string]any{"service": "web"}).errorCode(t); code != apitypes.CodeConflict {
		t.Errorf("start_service on a running service: code %s, want conflict", code)
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
	if init.Result.ProtocolVersion != "2025-06-18" || init.Result.ServerInfo.Name != "tempo-dock" || !strings.Contains(init.Result.Instructions, `You act as token "shop-agent", scope deploy, limited to shop/staging`) {
		t.Errorf("initialize = %+v", init.Result)
	}

	// No session: a call works without a prior initialize on this connection.
	code, body = post(t, f, "shop-agent", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"whoami","arguments":{}}}`)
	if code != http.StatusOK || !strings.Contains(body, `scope deploy`) {
		t.Errorf("tools/call whoami: %d %s", code, body)
	}
}

// TestNoGoroutinesLeft checks that calls, including one that waits on the
// event bus, leave nothing of the endpoint running once they are answered.
// It looks for the endpoint's own goroutines by their stacks, so the
// daemon's background work (deploys, checks, idle connections) can't make
// it pass or fail.
func TestNoGoroutinesLeft(t *testing.T) {
	f := newFixture(t)
	for range 5 {
		post(t, f, "shop-agent", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"service_status","arguments":{"service":"web"}}}`)
	}
	post(t, f, "shop-agent", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"deploy_image","arguments":{"service":"web"}}}`)
	// A handler may still be returning after its answer was read, so
	// this waits for the endpoint's goroutines to end; a leaked one never
	// does and fails it.
	var left string
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if left = endpointGoroutines(); left == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines of the MCP endpoint still running:\n%s", left)
		}
	}
}

// endpointGoroutines returns the stacks of goroutines running, or started
// by, the MCP endpoint's packages (internal/mcp and internal/mcpkit, not
// this test's).
func endpointGoroutines() string {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	var out []string
	for g := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.Contains(g, "tempoDock/internal/mcp.") || strings.Contains(g, "tempoDock/internal/mcpkit.") {
			out = append(out, g)
		}
	}
	return strings.Join(out, "\n\n")
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
	if _, err := f.core.EnableServiceAnalytics(ctx, sys, web); err != nil {
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
	if c := call(t, s, "analytics", map[string]any{"report": "sites"}); c.isError || !strings.Contains(c.summary, "shop.example.com (staging)") {
		t.Errorf("analytics sites = %q", c.summary)
	}
	for report, args := range map[string]map[string]any{
		"stats":     {"range": "7d"},
		"breakdown": {"dimension": "referrer"},
		"errors":    {"filters": []string{"device:is:mobile"}},
		"vitals":    {"by": []string{"page"}, "metric": "lcp"},
		"replays":   {"errors_only": true},
		"funnel":    {},
	} {
		args["site"], args["report"] = "shop.example.com", report
		if c := call(t, s, "analytics", args); c.isError {
			t.Errorf("analytics %s = %q", report, c.summary)
		}
	}
	if c := call(t, s, "analytics", map[string]any{"report": "goals", "site": "shop.example.com"}); !strings.Contains(c.summary, "1 goals") {
		t.Errorf("analytics goals = %q", c.summary)
	}
	if code := call(t, s, "analytics", map[string]any{"site": "shop.example.com", "filters": []string{"nope"}}).errorCode(t); code != apitypes.CodeValidation {
		t.Errorf("a malformed filter: %s", code)
	}
	if code := call(t, s, "analytics", map[string]any{"project": "blog", "site": "x"}).errorCode(t); code != apitypes.CodeForbidden && code != apitypes.CodeNotFound {
		t.Errorf("another project's site: %s", code)
	}
}

// An agent takes a snapshot and restores it into a new service, waiting
// for each; restoring in place isn't offered.
func TestSnapshotTools(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pg := core.ServiceRef{Project: "shop", Environment: "staging", Service: "pg"}
	if _, err := f.core.CreateService(ctx, sys, pg.Env(), apitypes.CreateServiceRequest{Name: "pg", Template: apitypes.TemplatePostgres}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		rels, err := f.core.ListReleases(ctx, sys, pg, 1)
		return err == nil && len(rels) == 1 && rels[0].Status == apitypes.ReleaseActive
	})
	f.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if strings.Contains(spec.Script, "dock-snapshot: size") {
			return node.JobResult{Output: "dock-snapshot: size 64\n"}, nil
		}
		return node.JobResult{Output: "dock-snapshot: restored\n"}, nil
	})
	s := f.connect("shop-agent", nil)

	tools, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if tl.Name != "restore_snapshot" {
			continue
		}
		schema, err := json.Marshal(tl.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(schema), "in_place") {
			t.Errorf("restore_snapshot offers an in-place restore: %s", schema)
		}
	}

	var snap apitypes.Snapshot
	c := call(t, s, "create_snapshot", map[string]any{"service": "pg", "note": "before 2.0"})
	c.decode(t, &snap)
	if c.isError || snap.Status != apitypes.SnapshotReady || snap.Note != "before 2.0" {
		t.Fatalf("create_snapshot = %q %+v", c.summary, snap)
	}
	if c := call(t, s, "list_snapshots", map[string]any{"service": "pg"}); c.isError || !strings.Contains(c.summary, "1 snapshots") {
		t.Errorf("list_snapshots = %q", c.summary)
	}
	var res apitypes.SnapshotRestoreResult
	// The backup scheduler's first backup of pg may hold it for a moment.
	waitFor(t, func() bool {
		c = call(t, s, "restore_snapshot", map[string]any{"service": "pg", "snapshot": snap.ID, "as": "pg-old"})
		return !strings.Contains(c.summary, "is running; wait for it to finish")
	})
	c.decode(t, &res)
	if c.isError || res.Restore.InPlace || res.Restore.To != "pg-old" || res.Restore.Status != apitypes.BackupSucceeded {
		t.Fatalf("restore_snapshot = %q %+v", c.summary, res.Restore)
	}

	// Another service's snapshots restore only in place: the agent is
	// told to ask a person, not to pass an argument the tool doesn't have.
	files := core.ServiceRef{Project: "shop", Environment: "staging", Service: "files"}
	if _, err := f.core.CreateService(ctx, sys, files.Env(), apitypes.CreateServiceRequest{Name: "files", Image: "app:1", Port: 8080}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.core.AddVolume(ctx, sys, files, apitypes.AddVolumeRequest{Name: "files", MountPath: "/files"}); err != nil {
		t.Fatal(err)
	}
	if c := call(t, s, "deploy_image", map[string]any{"service": "files"}); c.isError {
		t.Fatalf("deploy_image = %q", c.summary)
	}
	call(t, s, "create_snapshot", map[string]any{"service": "files"}).decode(t, &snap)
	c = call(t, s, "restore_snapshot", map[string]any{"service": "files", "snapshot": snap.ID})
	if !c.isError || !strings.Contains(c.summary, "need a person") || !strings.Contains(c.summary, "--in-place") || strings.Contains(c.summary, "in_place") {
		t.Fatalf("restore_snapshot of a volume = %q", c.summary)
	}
}

// The logs tool's grep and stderr filter on the server.
func TestLogsFilter(t *testing.T) {
	f := newFixture(t)
	s := f.connect("shop-agent", nil)
	if res := call(t, s, "deploy_image", map[string]any{"service": "web"}); res.isError {
		t.Fatalf("deploy_image = %q", res.summary)
	}
	f.rt.AddLogs("dock_p1_e3_s1_r1", "GET /api error 500")
	f.rt.AddStderr("dock_p1_e3_s1_r1", "error: db timeout", "warn: slow query")
	var out struct{ Lines []string }
	call(t, s, "logs", map[string]any{"service": "web", "grep": "error", "stderr": true}).decode(t, &out)
	if len(out.Lines) != 1 || !strings.HasSuffix(out.Lines[0], " [stderr] error: db timeout") {
		t.Errorf("logs grep error, stderr = %q", out.Lines)
	}
}

// wait_for_release follows a release that is already deploying to its end,
// and a deploy refused meanwhile names that release, over MCP and the API.
func TestWaitForRelease(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	progress := make(chan string, 64)
	s := f.connect("shop-agent", &sdk.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *sdk.ProgressNotificationClientRequest) {
			select {
			case progress <- r.Params.Message:
			default:
			}
		},
	})
	hold := f.rt.HoldHealthChecks()
	defer hold()
	web := core.ServiceRef{Project: "shop", Environment: "staging", Service: "web"}
	if _, err := f.core.DeployImage(ctx, sys, web, apitypes.DeployImageRequest{Image: "app:2"}); err != nil {
		t.Fatal(err)
	}

	busy := call(t, s, "deploy_image", map[string]any{"service": "web"})
	var e struct {
		Error struct {
			Code    apitypes.ErrorCode
			Hint    string
			Release int64
		}
	}
	busy.decode(t, &e)
	if e.Error.Code != apitypes.CodeDeployInProgress || e.Error.Release != 1 || !strings.Contains(e.Error.Hint, "wait_for_release") {
		t.Errorf("deploy_image during a deploy = %s", busy.json)
	}
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/api/v1/projects/shop/environments/staging/services/web/releases", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+f.tokens["shop-agent"])
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var problem apitypes.Error
	err = json.NewDecoder(resp.Body).Decode(&problem)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusConflict || problem.Code != apitypes.CodeDeployInProgress || problem.Release != 1 {
		t.Errorf("API deploy during a deploy: %d %+v %v", resp.StatusCode, problem, err)
	}

	params := &sdk.CallToolParams{Name: "wait_for_release", Arguments: map[string]any{"service": "web"}}
	params.SetProgressToken("wait-1")
	done := make(chan *sdk.CallToolResult, 1)
	go func() {
		res, err := s.CallTool(ctx, params)
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	// The first progress line comes once it follows the release.
	select {
	case <-progress:
	case <-time.After(10 * time.Second):
		t.Fatal("wait_for_release sent no progress while the release health checks")
	}
	hold()
	var res *sdk.CallToolResult
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("wait_for_release didn't return after the health check passed")
	}
	var rel release
	out := split(t, res)
	out.decode(t, &rel)
	if out.isError || rel.Number != 1 || rel.Status != "active" || !strings.HasPrefix(out.summary, "Waited: web #1 active") {
		t.Errorf("wait_for_release = %q %+v", out.summary, rel)
	}

	// Nothing in flight: the latest release's state at once.
	idle := call(t, s, "wait_for_release", map[string]any{"service": "web"})
	idle.decode(t, &rel)
	if idle.isError || rel.Number != 1 || rel.Status != "active" || !strings.HasPrefix(idle.summary, "Nothing in flight: web #1 active") {
		t.Errorf("wait_for_release with nothing in flight = %q %+v", idle.summary, rel)
	}
}

// apply on a staging tool takes the change live in the same call: one
// call, and in a locked environment one access request.
func TestStageAndApply(t *testing.T) {
	f := newFixture(t)
	s := f.connect("shop-agent", nil)
	if dep := call(t, s, "deploy_image", map[string]any{"service": "web", "image": "app:2"}); dep.isError {
		t.Fatalf("deploy_image = %q", dep.summary)
	}
	set := call(t, s, "set_variables", map[string]any{"service": "web", "set": map[string]any{"A": "1"}, "apply": true})
	if set.isError || !strings.Contains(set.summary, "Applied: web #2 active") {
		t.Fatalf("set_variables with apply = %q", set.summary)
	}
	var p apitypes.PendingChanges
	call(t, s, "show_changes", nil).decode(t, &p)
	if p.Count != 0 {
		t.Errorf("still staged after apply: %+v", p)
	}
	// create_service with deploy deploys its image in the same call.
	if c := call(t, s, "create_service", map[string]any{"name": "worker", "image": "app:3", "deploy": true}); c.isError || !strings.Contains(c.summary, "worker #1 active") {
		t.Errorf("create_service with deploy = %q", c.summary)
	}
	// A deploy that fails still says the service was created, so it isn't
	// created again.
	f.rt.FailPull(errors.New("manifest unknown"))
	if c := call(t, s, "create_service", map[string]any{"name": "cron", "image": "app:404", "deploy": true}); c.isError || !strings.Contains(c.summary, "Created shop/staging/cron") {
		t.Errorf("create_service with a failing deploy = %q", c.summary)
	}
}

// update_service stages settings like set_variables stages variables.
func TestUpdateService(t *testing.T) {
	f := newFixture(t)
	s := f.connect("shop-agent", nil)
	up := call(t, s, "update_service", map[string]any{"service": "web", "port": 3000, "health_check_path": "/healthz"})
	if up.isError || !strings.Contains(up.summary, "show_changes") || !strings.Contains(up.summary, "apply_changes") {
		t.Fatalf("update_service = %q", up.summary)
	}
	var p apitypes.PendingChanges
	call(t, s, "show_changes", nil).decode(t, &p)
	staged := map[string]string{}
	for _, sc := range p.Settings {
		if sc.Service == "web" {
			staged[sc.Field] = sc.New
		}
	}
	if staged["port"] != "3000" || staged["health_check_path"] != "/healthz" || !slices.Contains(p.Affected, "web") {
		t.Errorf("show_changes after update_service = %+v", p)
	}
	svc, err := f.core.GetService(context.Background(), sys, core.ServiceRef{Project: "shop", Environment: "staging", Service: "web"})
	if err != nil || svc.Port != 0 {
		t.Errorf("update_service changed the live port: %d %v", svc.Port, err)
	}
}

// A token limited to a project is told its environments on initialize:
// tools need one named, and listing them would cost a call.
func TestInstructionsNameEnvironments(t *testing.T) {
	f := newFixture(t)
	s := f.connect("shop-all", nil)
	if got := s.InitializeResult().Instructions; !strings.Contains(got, "Its environments: ") || !strings.Contains(got, "staging (non_production)") {
		t.Errorf("instructions end %q", got[max(0, len(got)-200):])
	}
}

// A platform credential acts in no org (ADR 0080): its tools/list is the
// platform's few, and an org tool says it needs an org's credential.
func TestPlatformTokenTools(t *testing.T) {
	f := newFixture(t)
	s := f.connect("platform", nil)
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"host_checks", "list_alerts", "list_servers", "whoami"}) {
		t.Fatalf("tools/list = %v", names)
	}
	c := call(t, s, "list_servers", nil)
	if c.isError {
		t.Fatalf("list_servers: %s", c.summary)
	}
	// "1 servers: <name> online."
	_, name, _ := strings.Cut(c.summary, ": ")
	name, _, _ = strings.Cut(name, " ")
	if c := call(t, s, "host_checks", map[string]any{"server": name}); c.isError {
		t.Fatalf("host_checks: %s", c.summary)
	}
	if code := call(t, s, "list_projects", nil).errorCode(t); code != apitypes.CodeForbidden {
		t.Fatalf("list_projects: %s", code)
	}
}
