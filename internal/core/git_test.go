package core

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// fakeGitHub serves the GitHub API calls push-to-deploy makes for
// repository acme/shop: commits, tarballs and commit statuses.
type fakeGitHub struct {
	*httptest.Server
	// mux serves the API; withApp adds the GitHub App's endpoints.
	mux *http.ServeMux
	mu  sync.Mutex
	// files are the repository's files at every commit.
	files map[string]string
	// statuses are "<sha7> <state> <context> <target_url>" as posted.
	statuses []string
	// auth is the Authorization header of every request.
	auth []string
	// tarballs counts tarball downloads.
	tarballs int
	// slowPending delays answering "pending" statuses, so a later status
	// would overtake it unless posts are ordered.
	slowPending time.Duration
}

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	shaC = "cccccccccccccccccccccccccccccccccccccccc"
)

func newFakeGitHub(t *testing.T) *fakeGitHub {
	g := &fakeGitHub{files: map[string]string{
		"README.md":           "root",
		"apps/web/Dockerfile": "FROM scratch",
		"apps/web/dock.toml":  "[deploy]\nport = 3000\n",
		"apps/api/main.go":    "package main",
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/shop/tarball/{sha}", func(w http.ResponseWriter, r *http.Request) {
		sha := r.PathValue("sha")
		g.mu.Lock()
		g.tarballs++
		files := g.files
		g.mu.Unlock()
		gz := gzip.NewWriter(w)
		tw := tar.NewWriter(gz)
		top := "acme-shop-" + sha[:7] + "/"
		_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": sha}})
		_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: top, Mode: 0o755})
		for _, name := range slices.Sorted(func(yield func(string) bool) {
			for k := range files {
				if !yield(k) {
					return
				}
			}
		}) {
			_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: top + name, Mode: 0o644, Size: int64(len(files[name]))})
			_, _ = tw.Write([]byte(files[name]))
		}
		_ = tw.Close()
		_ = gz.Close()
	})
	mux.HandleFunc("GET /repos/acme/shop/commits", func(w http.ResponseWriter, r *http.Request) {
		sha := r.URL.Query().Get("sha")
		if sha == "main" {
			sha = shaC
		}
		_ = json.NewEncoder(w).Encode([]any{map[string]any{
			"sha":    sha,
			"commit": map[string]any{"message": "ship it\n\nlong body", "author": map[string]string{"name": "Ada L"}},
			"author": map[string]string{"login": "ada"},
		}})
	})
	mux.HandleFunc("POST /repos/acme/shop/statuses/{sha}", func(w http.ResponseWriter, r *http.Request) {
		var st githubStatus
		_ = json.NewDecoder(r.Body).Decode(&st)
		g.mu.Lock()
		slow := g.slowPending
		g.mu.Unlock()
		if st.State == "pending" {
			time.Sleep(slow)
		}
		g.mu.Lock()
		g.statuses = append(g.statuses, strings.TrimSpace(fmt.Sprintf("%s %s %s %s", r.PathValue("sha")[:7], st.State, st.Context, st.TargetURL)))
		g.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	})
	g.mux = mux
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.auth = append(g.auth, r.Header.Get("Authorization"))
		g.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *fakeGitHub) seen() (statuses, auth []string, tarballs int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.statuses), slices.Clone(g.auth), g.tarballs
}

// gitHarness is a harness whose core talks to a fake GitHub and has a
// public webhook host.
func gitHarness(t *testing.T) (*harness, *fakeGitHub) {
	h := newHarness(t)
	gh := newFakeGitHub(t)
	h.opts.GitHubAPI = gh.URL
	h.opts.Config.Hooks.PublicHost = "hooks.example.com"
	h.opts.Config.Hooks.UIURL = "https://dock.example.com"
	h.restart()
	return h, gh
}

// hook is a connected webhook as GitHub knows it.
type hook struct {
	id, secret string
	n          int
}

func (h *harness) connect(ref ServiceRef, req apitypes.GitConnectRequest) *hook {
	h.t.Helper()
	res, err := h.c.GitConnect(h.ctx, testSystem, ref, req)
	if err != nil {
		h.t.Fatal(err)
	}
	if res.Secret == "" || !strings.HasPrefix(res.Connection.WebhookURL, "https://hooks.example.com/hooks/github/") {
		h.t.Fatalf("connect result: %+v", res)
	}
	return &hook{id: path.Base(res.Connection.WebhookURL), secret: res.Secret}
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// pushBody is a push event of sha to ref, changing files.
func pushBody(ref, sha string, files ...string) []byte {
	b, _ := json.Marshal(map[string]any{
		"ref": ref, "after": sha, "repository": map[string]string{"full_name": "acme/shop"},
		"pusher":      map[string]string{"name": "octocat"},
		"head_commit": map[string]any{"id": sha, "message": "fix checkout\n\ndetails", "author": map[string]string{"name": "Octo Cat", "username": "octocat"}},
		"commits":     []any{map[string]any{"id": sha, "modified": files}},
	})
	return b
}

// push delivers body as a new signed push delivery.
func (h *harness) push(k *hook, body []byte) (string, error) {
	k.n++
	return h.c.GitWebhook(h.ctx, GitDelivery{
		HookID: k.id, Event: "push", ID: fmt.Sprintf("delivery-%s-%d", k.id[:6], k.n), Signature: sign(k.secret, body), Body: body,
	})
}

// waitReleases waits until a service has n releases and returns them,
// newest first.
func (h *harness) waitReleases(ref ServiceRef, n int) []apitypes.Release {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rels, err := h.c.ListReleases(h.ctx, testSystem, ref, 100)
		if err != nil {
			h.t.Fatal(err)
		}
		if len(rels) >= n {
			return rels
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("%d releases, want %d", len(rels), n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestValidSignature(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	good := sign("s3cret", body)
	for name, tc := range map[string]struct {
		header string
		want   bool
	}{
		"good":          {good, true},
		"other secret":  {sign("other", body), false},
		"other body":    {sign("s3cret", []byte("{}")), false},
		"missing":       {"", false},
		"sha1":          {"sha1=" + strings.TrimPrefix(good, "sha256="), false},
		"not hex":       {"sha256=zz", false},
		"truncated":     {good[:len(good)-2], false},
		"bare hex only": {strings.TrimPrefix(good, "sha256="), false},
	} {
		if got := validSignature([]byte("s3cret"), body, tc.header); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

func TestPushDecision(t *testing.T) {
	conn := store.GitConnection{Repo: "acme/shop", Branch: "main"}
	inWeb := store.GitConnection{Repo: "acme/shop", Branch: "main", Dir: "apps/web"}
	for name, tc := range map[string]struct {
		conn    store.GitConnection
		body    []byte
		ignored string // substring; "" means deployed
	}{
		"branch":             {conn, pushBody("refs/heads/main", shaA, "x"), ""},
		"other branch":       {conn, pushBody("refs/heads/dev", shaA, "x"), "push to refs/heads/dev"},
		"tag":                {conn, pushBody("refs/tags/main", shaA, "x"), "push to refs/tags/main"},
		"deleted":            {conn, pushBody("refs/heads/main", strings.Repeat("0", 40), "x"), "deleted"},
		"bad sha":            {conn, pushBody("refs/heads/main", "HEAD", "x"), "no valid commit"},
		"other repo":         {store.GitConnection{Repo: "acme/other", Branch: "main"}, pushBody("refs/heads/main", shaA), "connected to acme/other"},
		"repo case":          {store.GitConnection{Repo: "Acme/Shop", Branch: "main"}, pushBody("refs/heads/main", shaA), ""},
		"in dir":             {inWeb, pushBody("refs/heads/main", shaA, "apps/web/src/a.ts"), ""},
		"outside dir":        {inWeb, pushBody("refs/heads/main", shaA, "apps/api/main.go", "apps/webby/x"), "nothing changed in apps/web/"},
		"no commits, in dir": {inWeb, []byte(`{"ref":"refs/heads/main","after":"` + shaA + `","repository":{"full_name":"acme/shop"}}`), ""},
	} {
		var p pushPayload
		if err := json.Unmarshal(tc.body, &p); err != nil {
			t.Fatal(err)
		}
		commit, ignored := pushDecision(tc.conn, p)
		switch {
		case tc.ignored == "" && ignored != "":
			t.Errorf("%s: ignored (%s), want deployed", name, ignored)
		case tc.ignored != "" && !strings.Contains(ignored, tc.ignored):
			t.Errorf("%s: got %q, want %q", name, ignored, tc.ignored)
		case tc.ignored == "" && name == "branch" && (commit != apitypes.Commit{SHA: shaA, Message: "fix checkout", Author: "octocat"}):
			t.Errorf("%s: commit %+v", name, commit)
		}
	}
}

// A push becomes a source release of the connected subdirectory with the
// commit on it; statuses go back to GitHub; replays, duplicates and bad
// signatures deploy nothing.
func TestGitWebhookDeploys(t *testing.T) {
	h, gh := gitHarness(t)
	// The pending status answers late: success must still come after it.
	gh.mu.Lock()
	gh.slowPending = 100 * time.Millisecond
	gh.mu.Unlock()
	web := h.service("web", apitypes.CreateServiceRequest{})
	token, on := "github_pat_secret", true
	k := h.connect(web, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main", Dir: "apps/web", Token: &token, Status: &on})

	body := pushBody("refs/heads/main", shaA, "apps/web/server.js")
	msg, err := h.push(k, body)
	if err != nil || msg != "push aaaaaaa: queued" {
		t.Fatalf("push: %q, %v", msg, err)
	}
	r := h.wait(web, h.waitReleases(web, 1)[0].Number)
	if r.Status != apitypes.ReleaseActive || r.Source != apitypes.SourceUpload || r.CreatedBy != "github:octocat" {
		t.Fatalf("release: %+v", r)
	}
	// Recorded before the deploy started, so by the time it is live.
	if src, err := h.c.GitStatus(h.ctx, testSystem, web); err != nil || src.Connection == nil || src.Connection.LastEvent != "push aaaaaaa: release 1" {
		t.Fatalf("status once live: %+v, %v", src.Connection, err)
	}
	if r.Commit == nil || *r.Commit != (apitypes.Commit{SHA: shaA, Message: "fix checkout", Author: "octocat"}) {
		t.Fatalf("commit: %+v", r.Commit)
	}
	// The build context is apps/web: its dock.toml applied.
	if r.Config.Port != 3000 || !slices.Equal(r.Config.FromRepoFile, []string{"port"}) {
		t.Fatalf("config: %+v", r.Config)
	}
	logs, _ := h.c.BuildLog(h.ctx, testSystem, web, r.Number)
	b, _ := io.ReadAll(logs)
	_ = logs.Close()
	if !strings.Contains(string(b), "Received 3072 bytes") { // two files and the tar trailer, nothing from apps/api
		t.Fatalf("build log: %s", b)
	}

	// Same delivery again (a replay): nothing happens.
	k.n--
	if msg, err := h.push(k, body); err != nil || !strings.Contains(msg, "already received") {
		t.Fatalf("replayed delivery: %q, %v", msg, err)
	}
	// A new delivery of the live commit: nothing to deploy.
	if msg, err := h.push(k, body); err != nil || !strings.Contains(msg, "already live in release 1") {
		t.Fatalf("same commit: %q, %v", msg, err)
	}
	// Bad, missing and foreign signatures; an unknown hook.
	for _, sig := range []string{"", "sha256=00", sign("guess", body)} {
		_, err := h.c.GitWebhook(h.ctx, GitDelivery{HookID: k.id, Event: "push", ID: "x-1", Signature: sig, Body: body})
		if CodeOf(err) != apitypes.CodeUnauthorized {
			t.Fatalf("signature %q: %v", sig, err)
		}
	}
	if _, err := h.c.GitWebhook(h.ctx, GitDelivery{HookID: "nope", Event: "push", ID: "x-2", Signature: sign(k.secret, body), Body: body}); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("unknown hook: %v", err)
	}
	if rels := h.waitReleases(web, 1); len(rels) != 1 {
		t.Fatalf("%d releases after ignored deliveries", len(rels))
	}

	statuses, auth, tarballs := gh.seen()
	ctx := "dock/shop/production/web"
	want := []string{
		"aaaaaaa pending " + ctx + " https://dock.example.com/test/shop/production/web/releases/1",
		"aaaaaaa success " + ctx + " https://dock.example.com/test/shop/production/web/releases/1",
	}
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Equal(statuses, want) && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
		statuses, auth, tarballs = gh.seen()
	}
	if !slices.Equal(statuses, want) {
		t.Fatalf("statuses: %q", statuses)
	}
	if tarballs != 1 || len(auth) == 0 || auth[0] != "Bearer "+token {
		t.Fatalf("tarballs %d, auth %q", tarballs, auth)
	}

	// The token never comes back; status shows what happened (the worker
	// records it in the background).
	var src apitypes.GitSource
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		src, err = h.c.GitStatus(h.ctx, testSystem, web)
		if err != nil || (src.Connection != nil && strings.Contains(src.Connection.LastEvent, "already live")) || time.Now().After(deadline) {
			break
		}
	}
	if err != nil || src.Connection == nil || !src.Connection.TokenSet || !strings.Contains(src.Connection.LastEvent, "already live") {
		t.Fatalf("status: %+v, %v", src, err)
	}
	if j, _ := json.Marshal(src); bytes.Contains(j, []byte(token)) || bytes.Contains(j, []byte(k.secret)) {
		t.Fatalf("status leaks a credential: %s", j)
	}
}

// While a deploy runs, newer pushes replace the queued one; the running
// deploy is never cancelled and only the newest push deploys after it.
func TestGitNewestPushWins(t *testing.T) {
	h, _ := gitHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	k := h.connect(web, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main"})
	hold := h.rt.HoldHealthChecks()
	if _, err := h.push(k, pushBody("refs/heads/main", shaA)); err != nil {
		t.Fatal(err)
	}
	h.waitReleases(web, 1)
	h.waitStatus(web, 1, apitypes.ReleaseHealthChecking)
	if _, err := h.push(k, pushBody("refs/heads/main", shaB)); err != nil {
		t.Fatal(err)
	}
	msg, err := h.push(k, pushBody("refs/heads/main", shaC))
	if err != nil || msg != "push ccccccc: queued, replacing bbbbbbb" {
		t.Fatalf("third push: %q, %v", msg, err)
	}
	if src, _ := h.c.GitStatus(h.ctx, testSystem, web); src.Connection.Pending == nil || src.Connection.Pending.SHA != shaC {
		t.Fatalf("pending: %+v", src.Connection.Pending)
	}
	hold()
	h.waitReleases(web, 2)
	second := h.wait(web, 2)
	if first := h.wait(web, 1); first.Status != apitypes.ReleaseSuperseded || first.Commit.SHA != shaA {
		t.Fatalf("first release: %s %+v", first.Status, first.Commit)
	}
	if second.Status != apitypes.ReleaseActive || second.Commit.SHA != shaC {
		t.Fatalf("second release: %s %+v", second.Status, second.Commit)
	}
	time.Sleep(20 * time.Millisecond)
	if rels, _ := h.c.ListReleases(h.ctx, testSystem, web, 10); len(rels) != 2 {
		t.Fatalf("%d releases, want 2 (B never deploys)", len(rels))
	}
	if src, _ := h.c.GitStatus(h.ctx, testSystem, web); src.Connection.Pending != nil {
		t.Fatalf("still pending: %+v", src.Connection.Pending)
	}
}

// A queued push survives a restart and deploys after it.
func TestGitQueuedPushSurvivesRestart(t *testing.T) {
	h, _ := gitHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	k := h.connect(web, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main"})
	h.deploy(web, "app:1")
	hold := h.rt.HoldHealthChecks()
	if _, err := h.c.Restart(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	h.waitStatus(web, 2, apitypes.ReleaseHealthChecking)
	if _, err := h.push(k, pushBody("refs/heads/main", shaB)); err != nil {
		t.Fatal(err)
	}
	h.crash() // release 2 is interrupted; the push stays queued
	hold()
	// The crashed daemon's goroutines still run here (a real one is gone,
	// and the data dir's lock keeps two from running), so it may deploy
	// the push too: what matters is that it ends up active.
	eventually(t, "the queued push to deploy after the restart", func() bool {
		rels, err := h.c.ListReleases(h.ctx, testSystem, web, 100)
		return err == nil && len(rels) > 0 && rels[0].Status == apitypes.ReleaseActive &&
			rels[0].Commit != nil && rels[0].Commit.SHA == shaB
	})
}

// Manual deploys resolve the branch head on GitHub; a missing directory
// fails the push with a reason and an error status.
func TestGitDeployAndFetchFailure(t *testing.T) {
	h, gh := gitHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	token, on := "tok", true
	h.connect(web, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main", Dir: "apps/missing", Token: &token, Status: &on})
	res, err := h.c.GitDeploy(h.ctx, testSystem, web, apitypes.GitDeployRequest{})
	if err != nil || res.Commit != (apitypes.Commit{SHA: shaC, Message: "ship it", Author: "ada"}) {
		t.Fatalf("deploy: %+v, %v", res, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		src, err := h.c.GitStatus(h.ctx, testSystem, web)
		if err != nil {
			t.Fatal(err)
		}
		if c := src.Connection; c.Pending == nil && strings.Contains(c.LastEvent, "not deployed") {
			if !strings.Contains(c.LastEvent, "directory apps/missing/ is not in the commit") {
				t.Fatalf("last event: %s", c.LastEvent)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("push never failed: %+v", src.Connection)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if rels, _ := h.c.ListReleases(h.ctx, testSystem, web, 10); len(rels) != 0 {
		t.Fatalf("releases: %+v", rels)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		if statuses, _, _ := gh.seen(); len(statuses) == 1 && strings.HasPrefix(statuses[0], "ccccccc error dock/shop/production/web") {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("statuses: %q", statuses)
		}
	}

	// With the right directory, the same commit deploys.
	if _, err := h.c.GitConnect(h.ctx, testSystem, web, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main", Dir: "apps/web"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.GitDeploy(h.ctx, testSystem, web, apitypes.GitDeployRequest{Ref: shaA}); err != nil {
		t.Fatal(err)
	}
	if r := h.wait(web, h.waitReleases(web, 1)[0].Number); r.Status != apitypes.ReleaseActive || r.Commit.SHA != shaA || r.CreatedBy != "system" {
		t.Fatalf("release: %+v", r)
	}
}

func TestGitConnectPermissions(t *testing.T) {
	h, _ := gitHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	req := apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main"}
	reader := Actor{Kind: "token", OrgID: 1, Name: "ro", TokenID: 1, Scope: apitypes.ScopeRead}
	if _, err := h.c.GitConnect(h.ctx, reader, web, req); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("read token connects: %v", err)
	}
	if _, err := h.c.GitDeploy(h.ctx, reader, web, apitypes.GitDeployRequest{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("read token deploys: %v", err)
	}
	staging := Actor{Kind: "token", OrgID: 1, Name: "ci", TokenID: 2, Scope: apitypes.ScopeDeploy, ProjectID: 1, EnvironmentID: 999, ProjectName: "shop", EnvironmentName: "staging"}
	if _, err := h.c.GitConnect(h.ctx, staging, web, req); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("token for another environment connects: %v", err)
	}
	deployer := Actor{Kind: "token", OrgID: 1, Name: "ci", TokenID: 3, Scope: apitypes.ScopeDeploy}
	on := true
	if _, err := h.c.GitConnect(h.ctx, deployer, web, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main", Status: &on}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("statuses without a token: %v", err)
	}
	for _, bad := range []apitypes.GitConnectRequest{
		{Repo: "acme", Branch: "main"}, {Repo: "acme/shop", Branch: "a..b"}, {Repo: "acme/shop", Branch: "main", Dir: "../etc"},
	} {
		if _, err := h.c.GitConnect(h.ctx, deployer, web, bad); CodeOf(err) != apitypes.CodeValidation {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	first, err := h.c.GitConnect(h.ctx, deployer, web, req)
	if err != nil || !first.Created || first.Secret == "" {
		t.Fatalf("connect: %+v, %v", first, err)
	}
	// Changing the branch keeps the webhook; rotating replaces it.
	req.Branch = "release"
	again, err := h.c.GitConnect(h.ctx, deployer, web, req)
	if err != nil || again.Created || again.Secret != "" || again.Connection.WebhookURL != first.Connection.WebhookURL || again.Connection.Branch != "release" {
		t.Fatalf("update: %+v, %v", again, err)
	}
	req.RotateSecret = true
	rotated, err := h.c.GitConnect(h.ctx, deployer, web, req)
	if err != nil || rotated.Secret == "" || rotated.Secret == first.Secret || rotated.Connection.WebhookURL == first.Connection.WebhookURL {
		t.Fatalf("rotate: %+v, %v", rotated, err)
	}
	if err := h.c.GitDisconnect(h.ctx, reader, web); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("read token disconnects: %v", err)
	}
	if err := h.c.GitDisconnect(h.ctx, deployer, web); err != nil {
		t.Fatal(err)
	}
	if src, err := h.c.GitStatus(h.ctx, reader, web); err != nil || src.Connection != nil || !src.HooksReady {
		t.Fatalf("status after disconnect: %+v, %v", src, err)
	}
}

// The webhook host is routed on the control plane, only under
// /hooks/github/, and can't be taken as a domain.
func TestGitHookRoute(t *testing.T) {
	h, _ := gitHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	var found bool
	for _, r := range h.rt.Routes() {
		if slices.Contains(r.Hostnames, "hooks.example.com") {
			found = r.Upstream == "127.0.0.1:7072" && r.PathPrefix == gitHookPath
		}
	}
	if !found {
		t.Fatalf("routes: %+v", h.rt.Routes())
	}
	if ok, _ := h.c.AllowCertificate(h.ctx, "hooks.example.com"); !ok {
		t.Fatal("no certificate for the webhook host")
	}
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "hooks.example.com"}); CodeOf(err) != apitypes.CodeHostnameTaken {
		t.Fatalf("add webhook host as a domain: %v", err)
	}
}

func TestRerootTar(t *testing.T) {
	var src bytes.Buffer
	tw := tar.NewWriter(&src)
	for _, name := range []string{"o-r-1/", "o-r-1/a/", "o-r-1/a/x", "o-r-1/ab/y", "o-r-1/z"} {
		typ := byte(tar.TypeReg)
		if strings.HasSuffix(name, "/") {
			typ = tar.TypeDir
		}
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: typ, Mode: 0o644})
	}
	_ = tw.WriteHeader(&tar.Header{Name: "o-r-1/a/l", Typeflag: tar.TypeLink, Linkname: "o-r-1/a/x"})
	_ = tw.Close()
	names := func(dir string) ([]string, error) {
		var out bytes.Buffer
		if err := rerootTar(tar.NewReader(bytes.NewReader(src.Bytes())), &out, dir); err != nil {
			return nil, err
		}
		var got []string
		tr := tar.NewReader(&out)
		for {
			h, err := tr.Next()
			if err != nil {
				return got, nil
			}
			got = append(got, h.Name+">"+h.Linkname)
		}
	}
	if got, _ := names(""); !slices.Equal(got, []string{"a/>", "a/x>", "ab/y>", "z>", "a/l>a/x"}) {
		t.Fatalf("root: %q", got)
	}
	if got, _ := names("a"); !slices.Equal(got, []string{"x>", "l>x"}) {
		t.Fatalf("dir a: %q", got)
	}
	if _, err := names("nope"); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("missing dir: %v", err)
	}
}
