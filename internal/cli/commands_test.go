package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/api"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/fake"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
)

// testServer is the real API and core on the fake runtime.
type testServer struct {
	url    string
	rt     *fake.Runtime
	core   *core.Core
	tokens map[apitypes.Scope]string
}

// sys is the system actor in the test org, the first one created.
var sys = core.System.InOrg(1)

func newTestServer(t *testing.T) *testServer {
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
	cfg.BaseDomain = "apps.test"
	cfg.PublicIP = "203.0.113.10"
	rt := fake.New()
	c, err := core.New(ctx, core.Options{Config: cfg, Store: st, Secrets: box, Runtime: rt, Logger: slog.New(slog.DiscardHandler), CutoverDrain: time.Nanosecond})
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
	ts := &testServer{url: srv.URL, rt: rt, core: c, tokens: map[apitypes.Scope]string{}}
	// The admin token is the install's, like bootstrap's: an operator's.
	for _, scope := range []apitypes.Scope{apitypes.ScopeRead, apitypes.ScopeDeploy, apitypes.ScopeAdmin} {
		tok, err := c.CreateToken(ctx, sys, apitypes.CreateTokenRequest{Name: string(scope), Scope: scope, Operator: scope == apitypes.ScopeAdmin})
		if err != nil {
			t.Fatal(err)
		}
		ts.tokens[scope] = tok.Secret
	}
	return ts
}

// ok runs a command that must succeed and decodes its --json output.
func ok[T any](t *testing.T, args ...string) T {
	t.Helper()
	code, stdout, stderr := exec(t, append(args, "--json")...)
	if code != 0 {
		t.Fatalf("%v: exit %d\n%s", args, code, stderr)
	}
	var v T
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("%v: stdout is not one JSON value: %v\n%s", args, err, stdout)
	}
	return v
}

// appDir writes a small app to deploy and makes it the working directory.
func appDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"Dockerfile":    "FROM scratch\n",
		"index.html":    "hello\n",
		"secret.env":    "TOKEN=x\n",
		".dockerignore": "secret.env\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	return dir
}

func TestDeployFlow(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	appDir(t)

	code, stdout, stderr := execStdin(t, srv.tokens[apitypes.ScopeAdmin]+"\n", "login", srv.url, "--token-stdin", "--json")
	var login loginView
	_ = json.Unmarshal([]byte(stdout), &login)
	if code != 0 || login.Me.Scope != apitypes.ScopeAdmin || login.Context != "127.0.0.1" {
		t.Fatalf("login = %d %+v\n%s", code, login, stderr)
	}
	ok[apitypes.Project](t, "project", "create", "shop")

	// The first deploy names the service, which creates it.
	code, stdout, stderr = exec(t, "deploy", "-p", "shop", "-e", "production", "-s", "web")
	if code != 0 || !strings.Contains(stderr, "Uploading ") || !strings.Contains(stderr, "Uploaded ") {
		t.Fatalf("deploy: exit %d\n%s%s", code, stdout, stderr)
	}
	rel := ok[apitypes.Release](t, "release", "show", "1", "-p", "shop", "-e", "production", "-s", "web")
	if rel.Number != 1 || rel.Status != apitypes.ReleaseActive || rel.Source != apitypes.SourceUpload || len(rel.Events) == 0 {
		t.Fatalf("deploy = %+v", rel)
	}

	// Link (the project's only environment is picked without asking), then
	// commands need no flags.
	link := ok[linkView](t, "link", "-p", "shop", "-s", "web")
	if link.Environment.Name != "production" || link.Service.Name != "web" {
		t.Fatalf("link = %+v", link)
	}
	st := ok[apitypes.ServiceOverview](t, "status")
	if st.ActiveRelease == nil || st.ActiveRelease.Number != 1 || st.Service.Status.State != apitypes.ServiceRunning {
		t.Fatalf("status = %+v", st)
	}

	// Variables are staged, then applied with a config release.
	staged := ok[apitypes.ChangeResult](t, "env", "set", "GREETING=hello world", "LEVEL=debug")
	if staged.Pending.Count != 2 || staged.Applied != nil {
		t.Fatalf("env set = %+v", staged)
	}
	applied := ok[apitypes.AppliedChanges](t, "apply")
	if len(applied.Releases) != 1 || applied.Releases[0].Status != apitypes.ReleaseActive || applied.Releases[0].Source != apitypes.SourceConfig {
		t.Fatalf("apply = %+v", applied)
	}
	if keys := applied.Releases[0].Config.VariableKeys; !strings.Contains(strings.Join(keys, " "), "GREETING") {
		t.Errorf("release 2 variables = %v", keys)
	}

	// Rollback defaults to the release that served before the active one.
	back := ok[apitypes.Release](t, "rollback")
	if back.Number != 3 || back.FromRelease != 1 || back.Status != apitypes.ReleaseActive {
		t.Fatalf("rollback = %+v", back)
	}
	if rels := ok[[]apitypes.Release](t, "releases"); len(rels) != 3 || rels[0].Status != apitypes.ReleaseActive {
		t.Fatalf("releases = %+v", rels)
	}

	// Adding a domain prints the record to create and waits until it's live.
	code, stdout, stderr = exec(t, "domain", "add", "shop.example.com")
	if code != 0 || !strings.Contains(stdout, "Point DNS:  A  shop.example.com  →  203.0.113.10") || !strings.Contains(stdout, "https://shop.example.com is live.") {
		t.Fatalf("domain add = %d\n%s\n%s", code, stdout, stderr)
	}

	// Traffic before anyone visited, and a range the API doesn't know.
	if tr := ok[apitypes.ServiceTraffic](t, "traffic", "--range", "7d"); tr.Range != apitypes.TrafficRange7d || tr.ResolutionSeconds != 3600 || tr.Totals.Requests != 0 {
		t.Fatalf("traffic = %+v", tr)
	}
	if code, stdout, _ := exec(t, "traffic"); code != 0 || !strings.Contains(stdout, "served no requests in the last 24h") {
		t.Fatalf("traffic = %d\n%s", code, stdout)
	}
	if code, _, _ := exec(t, "traffic", "--range", "1y"); code != int(output.ExitUsage) {
		t.Errorf("traffic --range 1y: exit %d, want %d", code, output.ExitUsage)
	}

	// Protect the service with a password from stdin, then open it again.
	code, stdout, stderr = execStdin(t, "correct horse battery\n", "access", "password", "--username", "team", "--password-stdin", "--json")
	var acc apitypes.Access
	_ = json.Unmarshal([]byte(stdout), &acc)
	if code != 0 || acc.Mode != apitypes.AccessPassword || acc.Username != "team" {
		t.Fatalf("access password = %d %+v\n%s", code, acc, stderr)
	}
	if a := ok[apitypes.Access](t, "access"); a.Mode != apitypes.AccessPassword || a.UpdatedBy == "" {
		t.Fatalf("access = %+v", a)
	}
	if a := ok[apitypes.Access](t, "access", "public"); a.Mode != apitypes.AccessPublic {
		t.Fatalf("access public = %+v", a)
	}
}

// execStdin is exec with stdin (still not a terminal).
func execStdin(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, &output.IO{In: strings.NewReader(stdin), Out: &out, Err: &errOut})
	return code, out.String(), errOut.String()
}

// Tokens stay out of argv when possible and never reach another server.
func TestLoginTokenHandling(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	admin := srv.tokens[apitypes.ScopeAdmin]
	tests := []struct {
		name, stdin string
		args        []string
		exit        output.ExitCode
		stderr      string
	}{
		{"no token and no terminal", "", []string{"login", srv.url}, output.ExitUsage, "--token-stdin"},
		{"both token flags", admin, []string{"login", srv.url, "--token", admin, "--token-stdin"}, output.ExitUsage, "not both"},
		{"empty stdin", "\n", []string{"login", srv.url, "--token-stdin"}, output.ExitUsage, "exactly one token"},
		{"wrong token", "dock_wrong", []string{"login", srv.url, "--token-stdin"}, output.ExitAuth, "Check the token"},
		{"token on the command line still works", "", []string{"login", srv.url, "--token", admin}, output.ExitOK, ""},
	}
	for _, tt := range tests {
		code, _, stderr := execStdin(t, tt.stdin, tt.args...)
		if code != int(tt.exit) || !strings.Contains(stderr, tt.stderr) {
			t.Errorf("%s: exit %d, stderr %q; want exit %d mentioning %q", tt.name, code, stderr, tt.exit, tt.stderr)
		}
	}

	// DOCK_TOKEN is only ever sent to DOCK_URL, never to a context's server.
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeRead])
	if code, _, stderr := exec(t, "whoami"); code != int(output.ExitUsage) || !strings.Contains(stderr, clientconfig.EnvURL) {
		t.Errorf("DOCK_TOKEN without DOCK_URL: %d %q", code, stderr)
	}
	t.Setenv(clientconfig.EnvURL, srv.url)
	if me := ok[apitypes.Me](t, "whoami"); me.Scope != apitypes.ScopeRead {
		t.Errorf("DOCK_URL with DOCK_TOKEN: %+v", me)
	}
}

// `dock me password` needs both passwords, and a token that acts for a
// user (from a browser login).
func TestMePassword(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	tests := []struct {
		name, stdin string
		args        []string
		exit        output.ExitCode
		stderr      string
	}{
		{"no terminal", "", []string{"me", "password"}, output.ExitUsage, "--password-stdin"},
		{"one line", "old password here\n", []string{"me", "password", "--password-stdin"}, output.ExitUsage, "two lines"},
		{"API token", "old password here\na new long password\n", []string{"me", "password", "--password-stdin"}, output.ExitUsage, "API tokens"},
	}
	for _, tt := range tests {
		code, _, stderr := execStdin(t, tt.stdin, tt.args...)
		if code != int(tt.exit) || !strings.Contains(stderr, tt.stderr) {
			t.Errorf("%s: exit %d, stderr %q; want exit %d mentioning %q", tt.name, code, stderr, tt.exit, tt.stderr)
		}
	}
}

func TestDeployFailureExitCodes(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	appDir(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeDeploy])
	exec(t, "project", "create", "shop")
	ok[apitypes.Release](t, "deploy", "-p", "shop", "-e", "production", "-s", "web")

	srv.rt.FailHealthCheck(func(node.ContainerSpec) error { return errors.New("connection refused") })
	code, stdout, stderr := exec(t, "deploy", "-p", "shop", "-e", "production", "-s", "web", "--json")
	var failed apitypes.Release
	_ = json.Unmarshal([]byte(stdout), &failed)
	var e struct {
		Error struct{ Code, Message, Hint string }
	}
	_ = json.Unmarshal([]byte(stderr), &e)
	if code != int(output.ExitDeployFailed) || failed.Status != apitypes.ReleaseFailed || e.Error.Code != "health_check_failed" ||
		!strings.Contains(e.Error.Hint, "Release 1 is still serving") || !strings.Contains(e.Error.Hint, "dock release show 2") {
		t.Fatalf("health check failure: exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}

	srv.rt.FailHealthCheck(nil)
	srv.rt.FailBuild(errors.New("npm ERR! missing script: build"))
	code, _, stderr = exec(t, "deploy", "-p", "shop", "-e", "production", "-s", "web")
	if code != int(output.ExitDeployFailed) || !strings.Contains(stderr, "npm ERR!") || !strings.Contains(stderr, "dock logs --build --release 3") {
		t.Fatalf("build failure: exit %d\n%s", code, stderr)
	}
}

func TestCommandErrors(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	appDir(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	exec(t, "project", "create", "shop")
	exec(t, "environment", "create", "staging", "-p", "shop")

	tests := []struct {
		name   string
		token  apitypes.Scope
		args   []string
		exit   output.ExitCode
		stderr string
	}{
		{"read token can't deploy", apitypes.ScopeRead, []string{"deploy", "-p", "shop", "-e", "production", "-s", "web"}, output.ExitAuth, "requires deploy"},
		{"deploy token can't archive", apitypes.ScopeDeploy, []string{"service", "archive", "web", "-p", "shop", "-e", "production", "--yes"}, output.ExitAuth, "requires admin"},
		{"deploy token can't archive a project", apitypes.ScopeDeploy, []string{"project", "archive", "shop", "--yes"}, output.ExitAuth, "requires admin"},
		{"unknown service", apitypes.ScopeAdmin, []string{"status", "-p", "shop", "-e", "production", "-s", "nope"}, output.ExitNotFound, `service "nope" not found`},
		{"unknown project", apitypes.ScopeAdmin, []string{"service", "list", "-p", "nope", "-e", "production"}, output.ExitNotFound, "not found"},
		{"nothing linked", apitypes.ScopeAdmin, []string{"deploy"}, output.ExitUsage, "--project"},
		{"no environment", apitypes.ScopeAdmin, []string{"logs", "-p", "shop"}, output.ExitUsage, "--environment"},
		{"several environments, no prompt", apitypes.ScopeAdmin, []string{"link", "-p", "shop"}, output.ExitUsage, "--environment"},
		{"bad KEY=value", apitypes.ScopeAdmin, []string{"env", "set", "NOVALUE", "-p", "shop", "-e", "production", "--shared"}, output.ExitUsage, "KEY=value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(clientconfig.EnvToken, srv.tokens[tt.token])
			code, _, stderr := exec(t, tt.args...)
			if code != int(tt.exit) || !strings.Contains(stderr, tt.stderr) {
				t.Errorf("exit %d, stderr %q; want exit %d mentioning %q", code, stderr, tt.exit, tt.stderr)
			}
		})
	}

	// Destructive commands ask first, and can't without a terminal.
	exec(t, "env", "set", "A=1", "--shared", "-p", "shop", "-e", "production")
	if code, _, stderr := exec(t, "discard", "-p", "shop", "-e", "production"); code != int(output.ExitUsage) || !strings.Contains(stderr, "--yes") {
		t.Errorf("discard without --yes: %d %q", code, stderr)
	}
}

// On a terminal, link picks what the flags leave out from numbered lists.
func TestLinkPicksOnTerminal(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	dir := appDir(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	exec(t, "project", "create", "shop")
	exec(t, "environment", "create", "staging", "-p", "shop")
	exec(t, "service", "create", "api", "-p", "shop", "-e", "staging")

	var out, errOut bytes.Buffer
	answers := strings.NewReader("1\nstaging\n1\n") // project shop, environment by name, service api
	code := run(context.Background(), []string{"link"}, &output.IO{In: answers, Out: &out, Err: &errOut, Interactive: true})
	if code != 0 || !strings.Contains(out.String(), "shop / staging / api") {
		t.Fatalf("link = %d\n%s\n%s", code, out.String(), errOut.String())
	}
	path, _ := clientconfig.DefaultPath()
	cfg, _ := clientconfig.Load(path)
	if _, l, ok := cfg.FindLink(dir); !ok || l.Service.Name != "api" || l.Environment.ID == "" {
		t.Errorf("stored link = %+v", l)
	}
}

func TestParseDotenvLine(t *testing.T) {
	tests := []struct {
		line, key, value string
		ok               bool
	}{
		{"# comment", "", "", false},
		{"", "", "", false},
		{"A=1", "A", "1", true},
		{"export B = two words # note", "B", "two words", true},
		{`C="line\nbreak # kept"`, "C", "line\nbreak # kept", true},
		{"D='${{shared.X}}'", "D", "${{shared.X}}", true},
		{"E=", "E", "", true},
	}
	for _, tt := range tests {
		k, v, ok, err := parseDotenvLine(tt.line)
		if err != nil || k != tt.key || v != tt.value || ok != tt.ok {
			t.Errorf("%q = %q %q %v %v", tt.line, k, v, ok, err)
		}
	}
	if _, _, _, err := parseDotenvLine("NOEQUALS"); err == nil {
		t.Error("a line without = should fail")
	}
}
