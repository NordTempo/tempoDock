package core

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
)

const (
	fakeAppID    = 99
	fakeInstall  = 7
	fakeRepoID   = 42
	fakeAppToken = "ghs_installtoken"
	// The App's OAuth client (ADR 0066).
	fakeClientID     = "Iv1.fake"
	fakeClientSecret = "client-secret"
	// octo is a GitHub user who can access installation 7; mallory can't;
	// dev can access some of its repositories (fakeGitHub.installRepos).
	octo, mallory, dev = "octo", "mallory", "dev"
	// acmeID is the GitHub account ID of acme, which installation 7 is on.
	acmeID = 500
	// The org's own App (withOrgApp): App 100, owned by acme, with
	// installation 9 on acme and 10 on another account.
	orgAppID, orgInstall, foreignInstall = 100, 9, 10
	orgAppToken                          = "ghs_orginstall"
)

// fakeApp is a GitHub App the fake GitHub hosts: its key, what its
// manifest code converts to, and its installations.
type fakeApp struct {
	key       *rsa.PrivateKey
	code      string
	created   map[string]any
	installs  []map[string]any
	tokenOf   map[string]string // installation ID to its token
	readsShop int64             // the installation that reads acme/shop
}

// fakeInstallation is an installation as GitHub describes it.
func fakeInstallation(id int64, login string, accountID int64) map[string]any {
	return map[string]any{"id": id, "account": map[string]any{"id": accountID, "login": login, "type": "Organization"},
		"repository_selection": "all", "html_url": fmt.Sprintf("https://github.com/organizations/%s/settings/installations/%d", login, id)}
}

// newFakeApp makes an App with a new key, its manifest code converting to
// the App with slug, owned by acme.
func newFakeApp(t *testing.T, id int64, code, slug string) *fakeApp {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return &fakeApp{key: key, code: code, tokenOf: map[string]string{}, created: map[string]any{
		"id": id, "slug": slug, "name": slug, "html_url": "https://github.com/apps/" + slug,
		"owner": map[string]any{"login": "acme", "id": acmeID}, "webhook_secret": "app-secret", "pem": string(pemKey),
		"client_id": fakeClientID, "client_secret": fakeClientSecret,
	}}
}

// withApp makes the fake GitHub host a GitHub App: it converts manifest
// code "c0de", has installation 7 on account acme, which reads acme/shop
// (repository 42), and checks the App's JWTs against its key. Its OAuth
// client turns a code into the token of the user the code names, and
// only octo's token lists installation 7. Each App's endpoints answer for
// the App the JWT is of, so withOrgApp can add another.
func (g *fakeGitHub) withApp(t *testing.T) {
	platform := newFakeApp(t, fakeAppID, "c0de", "tempo-dock-test")
	installation := fakeInstallation(fakeInstall, "acme", acmeID)
	platform.installs, platform.tokenOf[fmt.Sprint(fakeInstall)], platform.readsShop = []map[string]any{installation}, fakeAppToken, fakeInstall
	g.apps = map[int64]*fakeApp{fakeAppID: platform}
	// appOf is the App whose valid JWT the request carries, or nil after
	// answering 401.
	appOf := func(w http.ResponseWriter, r *http.Request) *fakeApp {
		parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
		if len(parts) == 3 {
			sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
			sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var c struct {
				Iss string `json:"iss"`
				Exp int64  `json:"exp"`
			}
			_ = json.Unmarshal(claims, &c)
			g.mu.Lock()
			var app *fakeApp
			for id, a := range g.apps {
				if fmt.Sprint(id) == c.Iss {
					app = a
				}
			}
			g.mu.Unlock()
			if app != nil && rsa.VerifyPKCS1v15(&app.key.PublicKey, crypto.SHA256, sum[:], sig) == nil && c.Exp > time.Now().Unix() {
				return app
			}
		}
		http.Error(w, `{"message":"bad JWT"}`, http.StatusUnauthorized)
		return nil
	}
	g.mux.HandleFunc("POST /app-manifests/{code}/conversions", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		for _, app := range g.apps {
			if app.code == r.PathValue("code") {
				_ = json.NewEncoder(w).Encode(app.created)
				return
			}
		}
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	g.mux.HandleFunc("GET /app", func(w http.ResponseWriter, r *http.Request) {
		if app := appOf(w, r); app != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": app.created["id"], "client_id": fakeClientID})
		}
	})
	g.mux.HandleFunc("POST /applications/{client}/token", func(w http.ResponseWriter, r *http.Request) {
		if id, secret, _ := r.BasicAuth(); id != fakeClientID || secret != fakeClientSecret || r.PathValue("client") != fakeClientID {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	g.mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.FormValue("client_id") != fakeClientID || r.FormValue("client_secret") != fakeClientSecret || r.FormValue("code_verifier") == "" ||
			r.FormValue("redirect_uri") != "https://dock.example.com"+GitHubUserCallbackPath {
			_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "ghu_" + r.FormValue("code"), "token_type": "bearer"})
	})
	g.mux.HandleFunc("GET /user/installations", func(w http.ResponseWriter, r *http.Request) {
		var list []any
		switch r.Header.Get("Authorization") {
		case "Bearer ghu_" + octo, "Bearer ghu_" + dev:
			list = []any{installation}
		case "Bearer ghu_" + mallory:
		default:
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(list), "installations": list})
	})
	// octo can access every repository of installation 7, dev only one.
	g.mux.HandleFunc("GET /user/installations/{id}/repositories", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		n := max(g.installRepos, 1)
		g.mu.Unlock()
		if r.Header.Get("Authorization") == "Bearer ghu_"+dev {
			n = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": n, "repositories": []any{}})
	})
	g.mux.HandleFunc("GET /app/installations", func(w http.ResponseWriter, r *http.Request) {
		if app := appOf(w, r); app != nil {
			_ = json.NewEncoder(w).Encode(app.installs)
		}
	})
	g.mux.HandleFunc("GET /app/installations/{id}", func(w http.ResponseWriter, r *http.Request) {
		app := appOf(w, r)
		if app == nil {
			return
		}
		for _, i := range app.installs {
			if fmt.Sprint(i["id"]) == r.PathValue("id") {
				_ = json.NewEncoder(w).Encode(i)
				return
			}
		}
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	g.mux.HandleFunc("GET /repos/acme/shop/installation", func(w http.ResponseWriter, r *http.Request) {
		app := appOf(w, r)
		if app == nil {
			return
		}
		for _, i := range app.installs {
			if fmt.Sprint(i["id"]) == fmt.Sprint(app.readsShop) {
				_ = json.NewEncoder(w).Encode(i)
				return
			}
		}
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	g.mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		app := appOf(w, r)
		if app == nil {
			return
		}
		token, ok := app.tokenOf[r.PathValue("id")]
		if !ok && app.code == "c0de" {
			token, ok = fakeAppToken, true // the platform App's other installations (TestGitAppAdoptsStaleConnections)
		}
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"token": token, "expires_at": time.Now().Add(time.Hour)})
	})
	g.mux.HandleFunc("GET /repos/acme/shop", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": fakeRepoID, "full_name": "acme/shop", "default_branch": "main", "private": true})
	})
	g.mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		n := max(g.installRepos, 1)
		g.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": n, "repositories": []any{
			map[string]any{"id": fakeRepoID, "full_name": "acme/shop", "default_branch": "main", "private": true},
		}})
	})
}

// withOrgApp adds an org's own App to the fake GitHub (after withApp):
// manifest code "0rg", App 100 owned by acme, with installation 9 on acme,
// which reads acme/shop, and installation 10 on another account.
func (g *fakeGitHub) withOrgApp(t *testing.T) {
	app := newFakeApp(t, orgAppID, "0rg", "tempo-dock-test-org")
	delete(app.created, "client_id")
	delete(app.created, "client_secret")
	app.installs = []map[string]any{fakeInstallation(orgInstall, "acme", acmeID), fakeInstallation(foreignInstall, "elsewhere", 600)}
	app.tokenOf[fmt.Sprint(orgInstall)], app.tokenOf[fmt.Sprint(foreignInstall)], app.readsShop = orgAppToken, "ghs_foreign", orgInstall
	g.mu.Lock()
	g.apps[orgAppID] = app
	g.mu.Unlock()
}

// appHarness is a git harness whose Dock has created the GitHub App and
// assigned installation 7 to org 1. It returns the App's hook ID.
func appHarness(t *testing.T) (*harness, *fakeGitHub, string) {
	h, gh := gitHarness(t)
	gh.withApp(t)
	state, err := h.c.StartGitHubApp(h.ctx, testSystem, true, apitypes.GitHubAppRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, manifest, err := h.c.GitHubManifest(state, "http://100.64.0.1:7070")
	if err != nil {
		t.Fatal(err)
	}
	var m githubManifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CompleteGitHubApp(h.ctx, "c0de", state); err != nil {
		t.Fatal(err)
	}
	if err := h.installApp(h.operator("ops@x"), "test", fakeInstall, octo); err != nil {
		t.Fatal(err)
	}
	return h, gh, path.Base(m.HookAttributes.URL)
}

// installApp goes through the App's install flow as a, for org, the way
// a's browser would after GitHub user login installed installation id:
// the start page, GitHub's redirect back, then (for an App that checks
// installs) the authorize page's redirect with login's code, and the
// confirm page.
func (h *harness) installApp(a Actor, org string, id int64, login string) error {
	h.t.Helper()
	state, binding, err := h.startInstall(a, org)
	if err != nil {
		return err
	}
	step, err := h.c.CompleteGitHubInstall(h.ctx, id, "install", state, binding, "https://dock.example.com")
	if err != nil {
		return err
	}
	if step.Redirect != "" {
		r, _ := url.Parse(step.Redirect)
		if step, err = h.c.CompleteGitHubVerify(h.ctx, login, r.Query().Get("state"), binding); err != nil {
			return err
		}
	}
	if step.Confirm == nil {
		h.t.Fatalf("no confirm page: %+v", step)
	}
	_, err = h.c.ConfirmGitHubInstall(h.ctx, step.Confirm.State, binding)
	return err
}

// startInstall asks for a's install link for org and opens it in a's
// browser: the flow's state and the browser's binding.
func (h *harness) startInstall(a Actor, org string) (state, binding string, err error) {
	h.t.Helper()
	link, err := h.c.StartGitHubInstall(h.ctx, a, apitypes.GitHubInstallRequest{Org: org}, "https://dock.example.com")
	if err != nil {
		return "", "", err
	}
	u, _ := url.Parse(link.URL)
	state = u.Query().Get("state")
	_, binding, err = h.c.BindGitHubInstall(h.ctx, a, state)
	return state, binding, err
}

// The manifest flow registers the App and the install flow assigns its
// installation; each link works once, and only operators start them.
func TestGitHubAppSetup(t *testing.T) {
	h, gh := gitHarness(t)
	gh.withApp(t)
	member := Actor{Kind: "user", Name: "ada", UserID: 5, OrgID: 1, Scope: apitypes.ScopeAdmin}
	if _, err := h.c.StartGitHubApp(h.ctx, member, true, apitypes.GitHubAppRequest{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("org admin started a platform App: %v", err)
	}
	if _, err := h.c.StartGitHubApp(h.ctx, testSystem, true, apitypes.GitHubAppRequest{Owner: "not/an/org"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("bad owner: %v", err)
	}
	state, err := h.c.StartGitHubApp(h.ctx, testSystem, true, apitypes.GitHubAppRequest{Owner: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	action, manifest, err := h.c.GitHubManifest(state, "http://100.64.0.1:7070")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://github.com/organizations/acme/settings/apps/new?state=" + state; action != want {
		t.Errorf("action %s, want %s", action, want)
	}
	var m githubManifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	// No public address: GitHub sends the browser back where it came from.
	if m.RedirectURL != "http://100.64.0.1:7070"+GitHubAppCallbackPath || !strings.HasPrefix(m.HookAttributes.URL, "https://hooks.example.com/hooks/github/") ||
		m.Public || m.DefaultPermissions["contents"] != "read" || m.DefaultPermissions["statuses"] != "write" ||
		!slices.Equal(m.CallbackURLs, []string{"http://100.64.0.1:7070" + GitHubUserCallbackPath}) {
		t.Errorf("manifest: %s", manifest)
	}
	if _, err := h.c.CompleteGitHubApp(h.ctx, "wrong", "not-a-state"); CodeOf(err) != apitypes.CodeUnauthorized {
		t.Fatalf("unknown state: %v", err)
	}
	app, err := h.c.CompleteGitHubApp(h.ctx, "c0de", state)
	if err != nil {
		t.Fatal(err)
	}
	if app.ID != fakeAppID || app.Slug != "tempo-dock-test" || app.CreatedBy != "system" || !app.VerifiesInstalls {
		t.Errorf("app %+v", app)
	}
	if _, err := h.c.CompleteGitHubApp(h.ctx, "c0de", state); CodeOf(err) != apitypes.CodeUnauthorized {
		t.Fatalf("state used twice: %v", err)
	}

	st, err := h.c.GitHubStatus(h.ctx, testSystem)
	if err != nil {
		t.Fatal(err)
	}
	if st.App == nil || st.WebhookURL != m.HookAttributes.URL || st.InstallationsError != "" || !slices.Equal(st.Orgs, []string{"test"}) ||
		len(st.Installations) != 1 || st.Installations[0].Account != "acme" || st.Installations[0].Org != "" {
		t.Fatalf("status before install: %+v", st)
	}
	op := h.operator("ops@x")
	if _, err := h.c.StartGitHubInstall(h.ctx, op, apitypes.GitHubInstallRequest{Org: "nope"}, ""); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("install for a missing org: %v", err)
	}
	// A credential of no one's has no browser to finish in.
	if _, err := h.c.StartGitHubInstall(h.ctx, testSystem, apitypes.GitHubInstallRequest{Org: "test"}, ""); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("install by the system: %v", err)
	}
	// The link is Dock's start page, where Dock was reached.
	link, err := h.c.StartGitHubInstall(h.ctx, op, apitypes.GitHubInstallRequest{Org: "test"}, "http://100.64.0.1:7070")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link.URL)
	install := u.Query().Get("state")
	if u.Host != "100.64.0.1:7070" || u.Path != GitHubInstallStartPath || install == "" {
		t.Fatalf("install link %s", link.URL)
	}
	// Only the person who asked for it opens it; it goes on to GitHub.
	if _, _, err := h.c.BindGitHubInstall(h.ctx, h.member("eve@x", nil), install); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("someone else opened the install link: %v", err)
	}
	to, binding, err := h.c.BindGitHubInstall(h.ctx, op, install)
	if g, _ := url.Parse(to); err != nil || binding == "" || g.Host != "github.com" || g.Path != "/apps/tempo-dock-test/installations/new" || g.Query().Get("state") != install {
		t.Fatalf("start page = %s, %v", to, err)
	}
	// A manifest state can't finish an install.
	if _, err := h.c.CompleteGitHubInstall(h.ctx, fakeInstall, "install", state, binding, ""); CodeOf(err) != apitypes.CodeUnauthorized {
		t.Fatalf("manifest state for an install: %v", err)
	}
	// The App checks who installed it: GitHub's redirect goes on to the
	// App's authorize page, with a state of its own and PKCE.
	step, err := h.c.CompleteGitHubInstall(h.ctx, fakeInstall, "install", install, binding, "https://dock.example.com")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := url.Parse(step.Redirect)
	verify := r.Query().Get("state")
	if r.Path != "/login/oauth/authorize" || r.Query().Get("client_id") != fakeClientID || r.Query().Get("code_challenge_method") != "S256" ||
		r.Query().Get("redirect_uri") != "https://dock.example.com"+GitHubUserCallbackPath || verify == "" || verify == install {
		t.Fatalf("authorize link %s", step.Redirect)
	}
	// Then Dock asks the person to confirm the org, and assigns it.
	step, err = h.c.CompleteGitHubVerify(h.ctx, octo, verify, binding)
	if err != nil || step.Confirm == nil || step.Confirm.Org != "test" || step.Confirm.Account != "acme" {
		t.Fatalf("after authorizing: %+v, %v", step, err)
	}
	if st, _ := h.c.GitHubStatus(h.ctx, testSystem); st.Installations[0].Org != "" {
		t.Fatalf("assigned before confirming: %+v", st.Installations)
	}
	if msg, err := h.c.ConfirmGitHubInstall(h.ctx, step.Confirm.State, binding); err != nil || !strings.Contains(msg, "org test") {
		t.Fatalf("confirm = %q, %v", msg, err)
	}
	if st, _ := h.c.GitHubStatus(h.ctx, testSystem); len(st.Installations) != 1 || st.Installations[0].Org != "test" {
		t.Fatalf("status after install: %+v", st)
	}
	// Uninstalling on GitHub unassigns it; it shows as unassigned.
	body := []byte(`{"action":"deleted","installation":{"id":7}}`)
	hookID := path.Base(m.HookAttributes.URL)
	if msg, err := h.c.GitWebhook(h.ctx, GitDelivery{HookID: hookID, Event: "installation", ID: "d1", Signature: sign("app-secret", body), Body: body}); err != nil || msg != "installation 7 removed" {
		t.Fatalf("installation deleted: %q, %v", msg, err)
	}
	if st, _ := h.c.GitHubStatus(h.ctx, testSystem); st.Installations[0].Org != "" {
		t.Fatalf("still assigned: %+v", st.Installations)
	}

	if err := h.c.RemoveGitHubApp(h.ctx, member, "tempo-dock-test"); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("org admin removed the platform's App: %v", err)
	}
	if err := h.c.RemoveGitHubApp(h.ctx, testSystem, "tempo-dock-test"); err != nil {
		t.Fatal(err)
	}
	if err := h.c.GitHookExists(h.ctx, hookID); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("hook of a removed App: %v", err)
	}
}

// With an installation assigned, connecting needs no token or webhook,
// and one push to the App deploys every service of the repository whose
// branch and directory it touches.
func TestGitAppConnectAndPush(t *testing.T) {
	h, gh, appHook := appHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Name: "web"})
	api := h.service("api", apitypes.CreateServiceRequest{Name: "api"})
	if src, err := h.c.GitStatus(h.ctx, testSystem, web); err != nil || !src.AppReady || !src.HooksReady {
		t.Fatalf("source before connecting: %+v, %v", src, err)
	}
	if _, err := h.c.GitConnect(h.ctx, testSystem, web, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main", Token: new("ghp_x")}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("token with the App: %v", err)
	}
	res, err := h.c.GitConnect(h.ctx, testSystem, web, apitypes.GitConnectRequest{Repo: "https://github.com/acme/shop.git", Branch: "main", Dir: "apps/web"})
	if err != nil {
		t.Fatal(err)
	}
	conn := res.Connection
	if conn.Mode != apitypes.GitModeApp || conn.Repo != "acme/shop" || conn.WebhookURL != "" || !conn.Status || res.Secret != "" || res.HooksHint != "" {
		t.Fatalf("App connection: %+v", res)
	}
	if _, err := h.c.GitConnect(h.ctx, testSystem, api, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main", Dir: "apps/api", Status: new(false)}); err != nil {
		t.Fatal(err)
	}
	// An App connection's own hook ID isn't a webhook.
	var hookID string
	if err := h.store.DB.QueryRowContext(h.ctx, "SELECT hook_id FROM git_connections WHERE installation_id > 0 LIMIT 1").Scan(&hookID); err != nil {
		t.Fatal(err)
	}
	if err := h.c.GitHookExists(h.ctx, hookID); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("App connection's hook: %v", err)
	}

	push := func(id string, files ...string) string {
		var p map[string]any
		_ = json.Unmarshal(pushBody("refs/heads/main", shaA, files...), &p)
		p["repository"] = map[string]any{"id": fakeRepoID, "full_name": "acme/renamed"}
		p["installation"] = map[string]any{"id": fakeInstall}
		body, _ := json.Marshal(p)
		msg, err := h.c.GitWebhook(h.ctx, GitDelivery{HookID: appHook, Event: "push", ID: id, Signature: sign("app-secret", body), Body: body})
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	msg := push("p1", "apps/web/index.html")
	if !strings.Contains(msg, "shop/production/web: push aaaaaaa: queued") || !strings.Contains(msg, "shop/production/api: push aaaaaaa: ignored, nothing changed in apps/api/") {
		t.Fatalf("delivery answer: %q", msg)
	}
	rels := h.waitReleases(web, 1)
	h.wait(web, rels[0].Number)
	if rels[0].Commit == nil || rels[0].Commit.SHA != shaA {
		t.Errorf("release %+v", rels[0])
	}
	statuses, auth, tarballs := gh.seen()
	if tarballs != 1 || !slices.Contains(auth, "Bearer "+fakeAppToken) {
		t.Errorf("tarballs %d, auth %v", tarballs, auth)
	}
	waitFor(t, func() bool {
		statuses, _, _ = gh.seen()
		return len(statuses) == 2
	})
	if !strings.HasPrefix(statuses[0], "aaaaaaa pending dock/shop/production/web") || !strings.HasPrefix(statuses[1], "aaaaaaa success") {
		t.Errorf("statuses %v", statuses)
	}
	if rels, _ := h.c.ListReleases(h.ctx, testSystem, api, 10); len(rels) != 0 {
		t.Errorf("api deployed: %+v", rels)
	}
	// Signed with something else, refused.
	if _, err := h.c.GitWebhook(h.ctx, GitDelivery{HookID: appHook, Event: "push", ID: "p2", Signature: sign("other", []byte("{}")), Body: []byte("{}")}); CodeOf(err) != apitypes.CodeUnauthorized {
		t.Fatalf("bad signature: %v", err)
	}

	// Switching to a webhook gives the service its own URL and secret.
	res, err = h.c.GitConnect(h.ctx, testSystem, api, apitypes.GitConnectRequest{Mode: apitypes.GitModeWebhook, Repo: "acme/shop", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Connection.Mode != apitypes.GitModeWebhook || res.Secret == "" || res.Connection.WebhookURL == "" || res.Connection.Status {
		t.Fatalf("switched to a webhook: %+v", res)
	}
}

// An installation only serves the org it is assigned to: another org
// can't connect its repositories, and unassigning it stops fetches.
func TestGitAppStaysInItsOrg(t *testing.T) {
	h, _, _ := appHarness(t)
	other, err := h.store.CreateOrg(h.ctx, store.CreateOrgParams{Slug: "other", Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	them := System.InOrg(other.ID)
	if _, err := h.c.CreateProject(h.ctx, them, apitypes.CreateProjectRequest{Name: "theirs"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CreateService(h.ctx, them, EnvRef{"theirs", "production"}, apitypes.CreateServiceRequest{Name: "web"}); err != nil {
		t.Fatal(err)
	}
	theirs := ServiceRef{"theirs", "production", "web"}
	if src, _ := h.c.GitStatus(h.ctx, them, theirs); src.AppReady {
		t.Error("other org sees the App as ready")
	}
	if _, err := h.c.GitConnect(h.ctx, them, theirs, apitypes.GitConnectRequest{Mode: apitypes.GitModeApp, Repo: "acme/shop", Branch: "main"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("other org connected acme/shop: %v", err)
	}
	if repos, err := h.c.GitHubRepos(h.ctx, them); err != nil || len(repos.Repos) != 0 {
		t.Fatalf("other org's repos: %+v, %v", repos, err)
	}
	if repos, err := h.c.GitHubRepos(h.ctx, testSystem); err != nil || len(repos.Repos) != 1 || repos.Repos[0].FullName != "acme/shop" {
		t.Fatalf("own repos: %+v, %v", repos, err)
	}

	web := h.service("web", apitypes.CreateServiceRequest{Name: "web"})
	if _, err := h.c.GitConnect(h.ctx, testSystem, web, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := h.c.UnassignGitHubInstallation(h.ctx, testSystem, fakeInstall); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.GitDeploy(h.ctx, testSystem, web, apitypes.GitDeployRequest{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("deploy through an unassigned installation: %v", err)
	}
	// Assigned to the other org instead, it still doesn't serve this one.
	if err := h.c.AssignGitHubInstallation(h.ctx, testSystem, fakeInstall, apitypes.GitHubInstallRequest{Org: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.GitDeploy(h.ctx, testSystem, web, apitypes.GitDeployRequest{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("deploy through another org's installation: %v", err)
	}
}

// Connections through an installation Dock forgot (the App was replaced)
// move to the installation a push for their repository comes from, when
// that one is assigned to their org, and deploy again. Another org's
// installation doesn't take them.
func TestGitAppAdoptsStaleConnections(t *testing.T) {
	h, _, appHook := appHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Name: "web"})
	if _, err := h.c.GitConnect(h.ctx, testSystem, web, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	// The old App's installation is gone; the new App's is 8.
	const newInstall = fakeInstall + 1
	if _, err := h.store.DB.ExecContext(h.ctx, "DELETE FROM github_installations"); err != nil {
		t.Fatal(err)
	}
	other, err := h.store.CreateOrg(h.ctx, store.CreateOrgParams{Slug: "other", Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	assign := func(org int64) {
		t.Helper()
		if err := h.store.SetGitHubInstallation(h.ctx, store.SetGitHubInstallationParams{InstallationID: newInstall, AppID: 1, OrgID: org, Account: "acme", AssignedBy: "test", AssignedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	push := func(id, sha string) string {
		var p map[string]any
		_ = json.Unmarshal(pushBody("refs/heads/main", sha), &p)
		p["repository"] = map[string]any{"id": fakeRepoID, "full_name": "acme/shop"}
		p["installation"] = map[string]any{"id": newInstall}
		body, _ := json.Marshal(p)
		msg, err := h.c.GitWebhook(h.ctx, GitDelivery{HookID: appHook, Event: "push", ID: id, Signature: sign("app-secret", body), Body: body})
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	assign(other.ID)
	if msg := push("p1", shaA); !strings.Contains(msg, "no service deploys") {
		t.Fatalf("another org's installation: %q", msg)
	}
	if _, err := h.store.DB.ExecContext(h.ctx, "DELETE FROM github_installations"); err != nil {
		t.Fatal(err)
	}
	assign(1)
	if msg := push("p2", shaB); !strings.Contains(msg, "shop/production/web: push bbbbbbb: queued") {
		t.Fatalf("own org's installation: %q", msg)
	}
	h.wait(web, h.waitReleases(web, 1)[0].Number)
}

// An installation event resent under a new delivery ID does nothing: a
// captured "deleted" can't unassign a reassigned installation again.
func TestGitAppInstallationReplay(t *testing.T) {
	h, _, appHook := appHarness(t)
	body := []byte(`{"action":"deleted","installation":{"id":7}}`)
	deliver := func(id string) (string, error) {
		return h.c.GitWebhook(h.ctx, GitDelivery{HookID: appHook, Event: "installation", ID: id, Signature: sign("app-secret", body), Body: body})
	}
	if msg, err := deliver("d1"); err != nil || msg != "installation 7 removed" {
		t.Fatalf("installation deleted: %q, %v", msg, err)
	}
	if err := h.c.AssignGitHubInstallation(h.ctx, testSystem, fakeInstall, apitypes.GitHubInstallRequest{Org: "test"}); err != nil {
		t.Fatal(err)
	}
	if msg, err := deliver("d2"); err != nil || !strings.Contains(msg, "already received") {
		t.Fatalf("replayed body: %q, %v", msg, err)
	}
	if inst, err := h.store.GetGitHubInstallation(h.ctx, fakeInstall); err != nil || inst.OrgID != 1 {
		t.Fatalf("installation after the replay: %+v, %v", inst, err)
	}
}

func TestAppJWT(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	parsed, err := parseAppKey(pemKey)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1_700_000_000, 0)
	jwt, err := appJWT(12, parsed, at)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if string(claims) != `{"iat":1699999940,"exp":1700000540,"iss":"12"}` {
		t.Errorf("claims %s", claims)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Errorf("signature: %v", err)
	}
	if _, err := parseAppKey([]byte("not a key")); err == nil {
		t.Error("parsed garbage")
	}
}

// waitFor polls cond for up to 5 seconds.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Dock claims an installation only for someone GitHub says can access it
// (ADR 0066): a redirect naming another installation, or a person who
// can't see it, gets nothing, and a verify link works once.
func TestGitHubInstallChecksTheInstaller(t *testing.T) {
	h, _, _ := appHarness(t)
	if err := h.c.UnassignGitHubInstallation(h.ctx, testSystem, fakeInstall); err != nil {
		t.Fatal(err)
	}
	admin := h.in(h.member("ann@x", map[string]apitypes.Scope{"test": apitypes.ScopeAdmin}), "test")
	assigned := func() bool {
		_, err := h.store.GetGitHubInstallation(h.ctx, fakeInstall)
		return err == nil
	}
	if err := h.installApp(admin, "", fakeInstall, mallory); CodeOf(err) != apitypes.CodeForbidden || assigned() {
		t.Fatalf("someone who can't access installation 7 claimed it: %v", err)
	}

	state, binding, err := h.startInstall(admin, "")
	if err != nil {
		t.Fatal(err)
	}
	step, err := h.c.CompleteGitHubInstall(h.ctx, fakeInstall, "install", state, binding, "https://dock.example.com")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := url.Parse(step.Redirect)
	verify := r.Query().Get("state")
	if _, err := h.c.CompleteGitHubVerify(h.ctx, "", verify, binding); CodeOf(err) != apitypes.CodeForbidden || assigned() {
		t.Fatalf("no code: %v", err)
	}
	if _, err := h.c.CompleteGitHubVerify(h.ctx, octo, verify, binding); CodeOf(err) != apitypes.CodeUnauthorized || assigned() {
		t.Fatalf("verify link used twice: %v", err)
	}

	// The admin sends the link to octo, who owns the GitHub account: on
	// Dock's start page it is someone else's link, and GitHub's redirect
	// in octo's browser, which the admin's binding cookie isn't in, gets
	// nowhere, nor does a confirm from it.
	link, err := h.c.StartGitHubInstall(h.ctx, admin, apitypes.GitHubInstallRequest{}, "https://dock.example.com")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link.URL)
	state = u.Query().Get("state")
	if _, _, err := h.c.BindGitHubInstall(h.ctx, h.member("octo@x", nil), state); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("octo opened the admin's link: %v", err)
	}
	if _, _, err := h.c.BindGitHubInstall(h.ctx, admin, state); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CompleteGitHubInstall(h.ctx, fakeInstall, "install", state, "", "https://dock.example.com"); CodeOf(err) != apitypes.CodeUnauthorized || assigned() {
		t.Fatalf("GitHub's redirect in another browser: %v", err)
	}
	state, binding, _ = h.startInstall(admin, "")
	step, _ = h.c.CompleteGitHubInstall(h.ctx, fakeInstall, "install", state, binding, "https://dock.example.com")
	r, _ = url.Parse(step.Redirect)
	if _, err := h.c.CompleteGitHubVerify(h.ctx, octo, r.Query().Get("state"), "another"); CodeOf(err) != apitypes.CodeUnauthorized || assigned() {
		t.Fatalf("authorize redirect in another browser: %v", err)
	}
	state, binding, _ = h.startInstall(admin, "")
	step, _ = h.c.CompleteGitHubInstall(h.ctx, fakeInstall, "install", state, binding, "https://dock.example.com")
	r, _ = url.Parse(step.Redirect)
	if step, err = h.c.CompleteGitHubVerify(h.ctx, octo, r.Query().Get("state"), binding); err != nil || step.Confirm == nil {
		t.Fatalf("verify: %+v, %v", step, err)
	}
	if _, err := h.c.ConfirmGitHubInstall(h.ctx, step.Confirm.State, "another"); CodeOf(err) != apitypes.CodeUnauthorized || assigned() {
		t.Fatalf("confirm from another browser: %v", err)
	}

	// An edited redirect names installation 8, which octo can't access.
	if err := h.installApp(admin, "", 8, octo); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("edited installation ID: %v", err)
	}
	if err := h.installApp(admin, "", fakeInstall, octo); err != nil || !assigned() {
		t.Fatalf("octo's install: %v", err)
	}
}

// An App made before Dock kept its OAuth client claims installations
// only for operators' links until an operator gives it the client
// secret, which Dock checks with GitHub first.
func TestGitHubAppClientSecret(t *testing.T) {
	h, _, _ := appHarness(t)
	h.exec(`UPDATE github_apps SET client_id = '', client_secret = NULL`)
	if err := h.c.UnassignGitHubInstallation(h.ctx, testSystem, fakeInstall); err != nil {
		t.Fatal(err)
	}
	admin := h.in(h.member("ann@x", map[string]apitypes.Scope{"test": apitypes.ScopeAdmin}), "test")
	if st, err := h.c.GitHubStatus(h.ctx, testSystem); err != nil || st.App.VerifiesInstalls || !strings.HasSuffix(st.CallbackURL, GitHubUserCallbackPath) {
		t.Fatalf("status of an App without its client: %+v, %v", st, err)
	}
	if _, err := h.c.StartGitHubInstall(h.ctx, admin, apitypes.GitHubInstallRequest{}, ""); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("org admin's install link without the client: %v", err)
	}
	// An operator's link claims it straight away, as before.
	if err := h.installApp(h.operator("ops2@x"), "test", fakeInstall, ""); err != nil {
		t.Fatal(err)
	}

	if err := h.c.SetGitHubAppClientSecret(h.ctx, admin, "tempo-dock-test", apitypes.GitHubClientSecretRequest{ClientSecret: fakeClientSecret}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("org admin set the client secret: %v", err)
	}
	if err := h.c.SetGitHubAppClientSecret(h.ctx, testSystem, "tempo-dock-test", apitypes.GitHubClientSecretRequest{ClientSecret: "wrong"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("wrong client secret: %v", err)
	}
	if err := h.c.SetGitHubAppClientSecret(h.ctx, testSystem, "tempo-dock-test", apitypes.GitHubClientSecretRequest{ClientSecret: fakeClientSecret}); err != nil {
		t.Fatal(err)
	}
	if st, err := h.c.GitHubStatus(h.ctx, testSystem); err != nil || !st.App.VerifiesInstalls {
		t.Fatalf("status with the client: %+v, %v", st, err)
	}
	if err := h.installApp(admin, "", fakeInstall, octo); err != nil {
		t.Fatalf("org admin's install once installs are checked: %v", err)
	}
}

// An installer who can access only some of the installation's
// repositories can't give it to an org (ADR 0066): GitHub lists it to
// them, but Dock would read the rest too. One who can access all can.
func TestGitHubInstallNeedsEveryRepository(t *testing.T) {
	h, gh, _ := appHarness(t)
	if err := h.c.UnassignGitHubInstallation(h.ctx, testSystem, fakeInstall); err != nil {
		t.Fatal(err)
	}
	admin := h.in(h.member("ann@x", map[string]apitypes.Scope{"test": apitypes.ScopeAdmin}), "test")
	assigned := func() bool {
		_, err := h.store.GetGitHubInstallation(h.ctx, fakeInstall)
		return err == nil
	}
	gh.mu.Lock()
	gh.installRepos = 3
	gh.mu.Unlock()
	err := h.installApp(admin, "", fakeInstall, dev)
	if CodeOf(err) != apitypes.CodeForbidden || !strings.Contains(err.Error(), "covers 3 repositories and your GitHub account can access 1") || assigned() {
		t.Fatalf("a member with one of three repositories claimed installation 7: %v", err)
	}
	if err := h.installApp(admin, "", fakeInstall, octo); err != nil || !assigned() {
		t.Fatalf("someone who can access every repository: %v", err)
	}
}

// startAppInstall is startInstall for an install request naming the App.
func (h *harness) startAppInstall(a Actor, req apitypes.GitHubInstallRequest) (state, binding string) {
	h.t.Helper()
	link, err := h.c.StartGitHubInstall(h.ctx, a, req, "https://dock.example.com")
	if err != nil {
		h.t.Fatal(err)
	}
	u, _ := url.Parse(link.URL)
	state = u.Query().Get("state")
	if _, binding, err = h.c.BindGitHubInstall(h.ctx, a, state); err != nil {
		h.t.Fatal(err)
	}
	return state, binding
}

// An org's admins make the org's own App (ADR 0105): it is private, its
// installation on the owner account is the org's with nothing to assign
// or check, one on another account counts for nothing, connecting
// prefers it to the platform's App, and other orgs don't see it.
func TestGitHubOrgApp(t *testing.T) {
	h, gh, _ := appHarness(t) // the platform's App, installation 7 org test's
	gh.withOrgApp(t)
	admin := h.in(h.member("ann@x", map[string]apitypes.Scope{"test": apitypes.ScopeAdmin}), "test")
	if _, err := h.c.StartGitHubApp(h.ctx, admin, false, apitypes.GitHubAppRequest{Public: true}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("a public org App: %v", err)
	}
	state, err := h.c.StartGitHubApp(h.ctx, admin, false, apitypes.GitHubAppRequest{Owner: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	_, manifest, err := h.c.GitHubManifest(state, "https://dock.example.com")
	if err != nil {
		t.Fatal(err)
	}
	var m githubManifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.Public || m.CallbackURLs != nil || m.Name != "Tempo Dock test" {
		t.Errorf("org App's manifest: %s", manifest)
	}
	app, err := h.c.CompleteGitHubApp(h.ctx, "0rg", state)
	if err != nil || app.Org != "test" || app.VerifiesInstalls {
		t.Fatalf("org App: %+v, %v", app, err)
	}
	orgHook := path.Base(m.HookAttributes.URL)

	// The org may install two Apps now: it names one.
	if _, err := h.c.StartGitHubInstall(h.ctx, admin, apitypes.GitHubInstallRequest{}, ""); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("install without naming the App: %v", err)
	}
	install := func(id int64) (GitHubInstallStep, error) {
		state, binding := h.startAppInstall(admin, apitypes.GitHubInstallRequest{App: app.Slug})
		return h.c.CompleteGitHubInstall(h.ctx, id, "install", state, binding, "https://dock.example.com")
	}
	if _, err := install(foreignInstall); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("installation on another account: %v", err)
	}
	if step, err := install(orgInstall); err != nil || !strings.Contains(step.Message, "org test can now deploy") {
		t.Fatalf("installation on the owner account: %+v, %v", step, err)
	}
	if inst, err := h.store.GetGitHubInstallation(h.ctx, orgInstall); err != nil || inst.AppID == 1 || inst.OrgID != 1 {
		t.Fatalf("org App's installation: %+v, %v", inst, err)
	}

	// Both Apps read acme/shop; the org's own wins.
	web := h.service("web", apitypes.CreateServiceRequest{Name: "web"})
	if _, err := h.c.GitConnect(h.ctx, testSystem, web, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	var through int64
	if err := h.store.DB.QueryRowContext(h.ctx, "SELECT installation_id FROM git_connections").Scan(&through); err != nil || through != orgInstall {
		t.Fatalf("connected through installation %d, %v; want the org App's", through, err)
	}
	if repos, err := h.c.GitHubRepos(h.ctx, testSystem); err != nil || len(repos.Repos) != 1 {
		t.Fatalf("a repository two Apps read, listed: %+v, %v", repos, err)
	}

	st, err := h.c.GitHubOrgStatus(h.ctx, admin)
	if err != nil || len(st.Apps) != 2 || st.Apps[0].App.Org != "test" || st.Apps[1].App.Org != "" || st.App == nil || st.App.Slug != "tempo-dock-test" {
		t.Fatalf("org status: %+v, %v", st, err)
	}
	if own := st.Apps[0].Installations; len(own) != 2 || own[0].ID != orgInstall || own[0].Org != "test" || own[0].Connections != 1 || own[1].Org != "" {
		t.Fatalf("the org App's installations: %+v", own)
	}

	other, err := h.store.CreateOrg(h.ctx, store.CreateOrgParams{Slug: "other", Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	theirs := h.in(h.member("bob@x", map[string]apitypes.Scope{other.Slug: apitypes.ScopeAdmin}), other.Slug)
	if st, err := h.c.GitHubOrgStatus(h.ctx, theirs); err != nil || len(st.Apps) != 1 || st.Apps[0].App.Org != "" {
		t.Fatalf("another org's view: %+v, %v", st, err)
	}
	if _, err := h.c.StartGitHubInstall(h.ctx, theirs, apitypes.GitHubInstallRequest{App: app.Slug}, ""); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("another org installs this org's App: %v", err)
	}
	if err := h.c.RemoveGitHubApp(h.ctx, theirs, app.Slug); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("another org removed this org's App: %v", err)
	}

	// The App's own deliveries: an uninstall forgets its installation (and
	// can't touch the platform App's), an install on the owner account is
	// the org's again, one on another account is ignored.
	deliver := func(id, body string) string {
		t.Helper()
		msg, err := h.c.GitWebhook(h.ctx, GitDelivery{HookID: orgHook, Event: "installation", ID: id, Signature: sign("app-secret", []byte(body)), Body: []byte(body)})
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	assigned := func(id int64) bool {
		_, err := h.store.GetGitHubInstallation(h.ctx, id)
		return err == nil
	}
	deliver("o1", `{"action":"deleted","installation":{"id":9}}`)
	deliver("o2", `{"action":"deleted","installation":{"id":7}}`)
	if assigned(orgInstall) || !assigned(fakeInstall) {
		t.Fatalf("after uninstall events: 9 %v, 7 %v", assigned(orgInstall), assigned(fakeInstall))
	}
	if msg := deliver("o3", `{"action":"created","installation":{"id":10,"account":{"id":600,"login":"elsewhere"}},"sender":{"login":"octo"}}`); !strings.HasPrefix(msg, "ignored") || assigned(foreignInstall) {
		t.Fatalf("installed on another account: %q", msg)
	}
	deliver("o4", `{"action":"created","installation":{"id":9,"account":{"id":500,"login":"acme"}},"sender":{"login":"octo"}}`)
	if !assigned(orgInstall) {
		t.Fatal("installed on the owner account: not the org's")
	}

	if err := h.c.RemoveGitHubApp(h.ctx, admin, app.Slug); err != nil {
		t.Fatal(err)
	}
	if assigned(orgInstall) || !assigned(fakeInstall) {
		t.Fatalf("after removing the org App: 9 %v, 7 %v", assigned(orgInstall), assigned(fakeInstall))
	}
}

// The App migration 0134 moved into github_apps carries values sealed for
// the one-row table; the next start binds them to its row.
func TestGitHubAppValuesBoundToTheirRow(t *testing.T) {
	h, _, _ := appHarness(t)
	app, err := h.store.GetGitHubApp(h.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	legacy := func(v []byte, bound, old secrets.AAD) []byte {
		plain, err := h.c.box.Open(v, bound)
		if err != nil {
			t.Fatal(err)
		}
		sealed, err := h.c.box.Seal(plain, old)
		if err != nil {
			t.Fatal(err)
		}
		return sealed
	}
	h.exec(`UPDATE github_apps SET private_key = ?, webhook_secret = ?, client_secret = ?`,
		legacy(app.PrivateKey, appKeyAAD(app.HookID), legacyAppKeyAAD),
		legacy(app.WebhookSecret, appWebhookSecretAAD(app.HookID), legacyAppWebhookSecretAAD),
		legacy(app.ClientSecret, appClientSecretAAD(app.HookID), legacyAppClientSecretAAD))
	h.exec(`UPDATE install SET secrets_format = 2`)
	if err := h.c.resealSecrets(h.ctx); err != nil {
		t.Fatal(err)
	}
	if app, err = h.store.GetGitHubApp(h.ctx, 1); err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		b   []byte
		aad secrets.AAD
	}{{app.PrivateKey, appKeyAAD(app.HookID)}, {app.WebhookSecret, appWebhookSecretAAD(app.HookID)}, {app.ClientSecret, appClientSecretAAD(app.HookID)}} {
		if _, err := h.c.box.Open(v.b, v.aad); err != nil {
			t.Errorf("not bound to its row: %v", err)
		}
	}
	if format, err := h.store.GetSecretsFormat(h.ctx); err != nil || format != secretsAppsBound {
		t.Errorf("secrets format %d, %v", format, err)
	}
}

// The App limit holds when an App is stored, not only when its flow
// starts: a flow started with room left is refused if the owner filled up
// meanwhile.
func TestGitHubAppLimitIsCheckedWhenStored(t *testing.T) {
	h, gh := gitHarness(t)
	gh.withApp(t)
	gh.withOrgApp(t)
	admin := h.in(h.member("ann@x", map[string]apitypes.Scope{"test": apitypes.ScopeAdmin}), "test")
	state, err := h.c.StartGitHubApp(h.ctx, admin, false, apitypes.GitHubAppRequest{})
	if err != nil {
		t.Fatal(err)
	}
	org := int64(1)
	for i := range maxGitHubApps {
		if _, err := h.store.CreateGitHubApp(h.ctx, store.CreateGitHubAppParams{
			OrgID: &org, AppID: int64(1000 + i), HookID: fmt.Sprint("hook", i), Slug: fmt.Sprint("app", i),
			PrivateKey: []byte{1}, WebhookSecret: []byte{1}, CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.c.StartGitHubApp(h.ctx, admin, false, apitypes.GitHubAppRequest{}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("an App past the limit: %v", err)
	}
	if _, err := h.c.CompleteGitHubApp(h.ctx, "0rg", state); CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "delete it in its settings on GitHub") {
		t.Fatalf("a flow started before the limit was reached: %v", err)
	}
	if _, err := h.store.GetGitHubAppBySlug(h.ctx, "tempo-dock-test-org"); !store.IsNotFound(err) {
		t.Fatalf("stored past the limit: %v", err)
	}
}
