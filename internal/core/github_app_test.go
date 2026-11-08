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
	// octo is a GitHub user who can access installation 7; mallory can't.
	octo, mallory = "octo", "mallory"
)

// withApp makes the fake GitHub host a GitHub App: it converts manifest
// code "c0de", has installation 7 on account acme, which reads acme/shop
// (repository 42), and checks the App's JWTs against its key. Its OAuth
// client turns a code into the token of the user the code names, and
// only octo's token lists installation 7.
func (g *fakeGitHub) withApp(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	installation := map[string]any{"id": fakeInstall, "account": map[string]string{"login": "acme", "type": "Organization"},
		"repository_selection": "all", "html_url": "https://github.com/organizations/acme/settings/installations/7"}
	// jwtOK checks the request carries a valid JWT of the App's.
	jwtOK := func(w http.ResponseWriter, r *http.Request) bool {
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
			if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig) == nil && c.Iss == fmt.Sprint(fakeAppID) && c.Exp > time.Now().Unix() {
				return true
			}
		}
		http.Error(w, `{"message":"bad JWT"}`, http.StatusUnauthorized)
		return false
	}
	g.mux.HandleFunc("POST /app-manifests/{code}/conversions", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("code") != "c0de" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": fakeAppID, "slug": "tempo-dock-test", "name": "Tempo Dock test", "html_url": "https://github.com/apps/tempo-dock-test",
			"owner": map[string]string{"login": "acme"}, "webhook_secret": "app-secret", "pem": string(pemKey),
			"client_id": fakeClientID, "client_secret": fakeClientSecret,
		})
	})
	g.mux.HandleFunc("GET /app", func(w http.ResponseWriter, r *http.Request) {
		if jwtOK(w, r) {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": fakeAppID, "client_id": fakeClientID})
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
		case "Bearer ghu_" + octo:
			list = []any{installation}
		case "Bearer ghu_" + mallory:
		default:
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(list), "installations": list})
	})
	g.mux.HandleFunc("GET /app/installations", func(w http.ResponseWriter, r *http.Request) {
		if jwtOK(w, r) {
			_ = json.NewEncoder(w).Encode([]any{installation})
		}
	})
	g.mux.HandleFunc("GET /app/installations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !jwtOK(w, r) {
			return
		}
		if r.PathValue("id") != fmt.Sprint(fakeInstall) {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(installation)
	})
	g.mux.HandleFunc("GET /repos/acme/shop/installation", func(w http.ResponseWriter, r *http.Request) {
		if jwtOK(w, r) {
			_ = json.NewEncoder(w).Encode(installation)
		}
	})
	g.mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		if jwtOK(w, r) {
			_ = json.NewEncoder(w).Encode(map[string]any{"token": fakeAppToken, "expires_at": time.Now().Add(time.Hour)})
		}
	})
	g.mux.HandleFunc("GET /repos/acme/shop", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": fakeRepoID, "full_name": "acme/shop", "default_branch": "main", "private": true})
	})
	g.mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "repositories": []any{
			map[string]any{"id": fakeRepoID, "full_name": "acme/shop", "default_branch": "main", "private": true},
		}})
	})
}

// appHarness is a git harness whose Dock has created the GitHub App and
// assigned installation 7 to org 1. It returns the App's hook ID.
func appHarness(t *testing.T) (*harness, *fakeGitHub, string) {
	h, gh := gitHarness(t)
	gh.withApp(t)
	state, err := h.c.StartGitHubApp(h.ctx, testSystem, apitypes.GitHubAppRequest{})
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
	if err := h.installApp(testSystem, "test", fakeInstall, octo); err != nil {
		t.Fatal(err)
	}
	return h, gh, path.Base(m.HookAttributes.URL)
}

// installApp goes through the App's install flow as a, for org, the way
// a browser would after GitHub user login installed installation id:
// GitHub's redirect back, then (for an App that checks installs) the
// authorize page's redirect with login's code.
func (h *harness) installApp(a Actor, org string, id int64, login string) error {
	h.t.Helper()
	link, err := h.c.StartGitHubInstall(h.ctx, a, apitypes.GitHubOrgRequest{Org: org})
	if err != nil {
		return err
	}
	u, _ := url.Parse(link.URL)
	step, err := h.c.CompleteGitHubInstall(h.ctx, id, "install", u.Query().Get("state"), "https://dock.example.com")
	if err != nil || step.Redirect == "" {
		return err
	}
	r, _ := url.Parse(step.Redirect)
	_, err = h.c.CompleteGitHubVerify(h.ctx, login, r.Query().Get("state"))
	return err
}

// The manifest flow registers the App and the install flow assigns its
// installation; each link works once, and only operators start them.
func TestGitHubAppSetup(t *testing.T) {
	h, gh := gitHarness(t)
	gh.withApp(t)
	member := Actor{Kind: "user", Name: "ada", UserID: 5, OrgID: 1, Scope: apitypes.ScopeAdmin}
	if _, err := h.c.StartGitHubApp(h.ctx, member, apitypes.GitHubAppRequest{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("org admin started the App: %v", err)
	}
	if _, err := h.c.StartGitHubApp(h.ctx, testSystem, apitypes.GitHubAppRequest{Owner: "not/an/org"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("bad owner: %v", err)
	}
	state, err := h.c.StartGitHubApp(h.ctx, testSystem, apitypes.GitHubAppRequest{Owner: "acme"})
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
	if _, err := h.c.StartGitHubApp(h.ctx, testSystem, apitypes.GitHubAppRequest{}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("second App: %v", err)
	}

	st, err := h.c.GitHubStatus(h.ctx, testSystem)
	if err != nil {
		t.Fatal(err)
	}
	if st.App == nil || st.WebhookURL != m.HookAttributes.URL || st.InstallationsError != "" || !slices.Equal(st.Orgs, []string{"test"}) ||
		len(st.Installations) != 1 || st.Installations[0].Account != "acme" || st.Installations[0].Org != "" {
		t.Fatalf("status before install: %+v", st)
	}
	if _, err := h.c.StartGitHubInstall(h.ctx, testSystem, apitypes.GitHubOrgRequest{Org: "nope"}); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("install for a missing org: %v", err)
	}
	link, err := h.c.StartGitHubInstall(h.ctx, testSystem, apitypes.GitHubOrgRequest{Org: "test"})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link.URL)
	if u.Host != "github.com" || u.Path != "/apps/tempo-dock-test/installations/new" {
		t.Fatalf("install link %s", link.URL)
	}
	// A manifest state can't finish an install.
	if _, err := h.c.CompleteGitHubInstall(h.ctx, fakeInstall, "install", state, ""); CodeOf(err) != apitypes.CodeUnauthorized {
		t.Fatalf("manifest state for an install: %v", err)
	}
	// The App checks who installed it: GitHub's redirect goes on to the
	// App's authorize page, with a state of its own and PKCE.
	step, err := h.c.CompleteGitHubInstall(h.ctx, fakeInstall, "install", u.Query().Get("state"), "https://dock.example.com")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := url.Parse(step.Redirect)
	verify := r.Query().Get("state")
	if r.Path != "/login/oauth/authorize" || r.Query().Get("client_id") != fakeClientID || r.Query().Get("code_challenge_method") != "S256" ||
		r.Query().Get("redirect_uri") != "https://dock.example.com"+GitHubUserCallbackPath || verify == "" || verify == u.Query().Get("state") {
		t.Fatalf("authorize link %s", step.Redirect)
	}
	if _, err := h.c.CompleteGitHubVerify(h.ctx, octo, verify); err != nil {
		t.Fatal(err)
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

	if err := h.c.RemoveGitHubApp(h.ctx, testSystem); err != nil {
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
	if err := h.c.AssignGitHubInstallation(h.ctx, testSystem, fakeInstall, apitypes.GitHubOrgRequest{Org: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.GitDeploy(h.ctx, testSystem, web, apitypes.GitDeployRequest{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("deploy through another org's installation: %v", err)
	}
}

// GitHub's redirect back names the installation, which the install link's
// state doesn't vouch for: it can't take an installation from another
// org. The same org installing again still works.
func TestGitHubInstallKeepsItsOrg(t *testing.T) {
	h, _, _ := appHarness(t)
	if _, err := h.store.CreateOrg(h.ctx, store.CreateOrgParams{Slug: "other", Name: "other"}); err != nil {
		t.Fatal(err)
	}
	install := func(org string) error { return h.installApp(testSystem, org, fakeInstall, octo) }
	if err := install("other"); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("other org's install took installation 7: %v", err)
	}
	if inst, err := h.store.GetGitHubInstallation(h.ctx, fakeInstall); err != nil || inst.OrgID != 1 {
		t.Fatalf("installation after the refused install: %+v, %v", inst, err)
	}
	if err := install("test"); err != nil {
		t.Fatalf("same org installing again: %v", err)
	}
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
	if err := h.c.AssignGitHubInstallation(h.ctx, testSystem, fakeInstall, apitypes.GitHubOrgRequest{Org: "test"}); err != nil {
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

	link, err := h.c.StartGitHubInstall(h.ctx, admin, apitypes.GitHubOrgRequest{})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link.URL)
	step, err := h.c.CompleteGitHubInstall(h.ctx, fakeInstall, "install", u.Query().Get("state"), "https://dock.example.com")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := url.Parse(step.Redirect)
	verify := r.Query().Get("state")
	if _, err := h.c.CompleteGitHubVerify(h.ctx, "", verify); CodeOf(err) != apitypes.CodeForbidden || assigned() {
		t.Fatalf("no code: %v", err)
	}
	if _, err := h.c.CompleteGitHubVerify(h.ctx, octo, verify); CodeOf(err) != apitypes.CodeUnauthorized || assigned() {
		t.Fatalf("verify link used twice: %v", err)
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
	h.exec(`UPDATE github_app SET client_id = '', client_secret = NULL`)
	if err := h.c.UnassignGitHubInstallation(h.ctx, testSystem, fakeInstall); err != nil {
		t.Fatal(err)
	}
	admin := h.in(h.member("ann@x", map[string]apitypes.Scope{"test": apitypes.ScopeAdmin}), "test")
	if st, err := h.c.GitHubStatus(h.ctx, testSystem); err != nil || st.App.VerifiesInstalls || !strings.HasSuffix(st.CallbackURL, GitHubUserCallbackPath) {
		t.Fatalf("status of an App without its client: %+v, %v", st, err)
	}
	if _, err := h.c.StartGitHubInstall(h.ctx, admin, apitypes.GitHubOrgRequest{}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("org admin's install link without the client: %v", err)
	}
	// An operator's link claims it straight away, as before.
	if err := h.installApp(testSystem, "test", fakeInstall, ""); err != nil {
		t.Fatal(err)
	}

	if err := h.c.SetGitHubAppClientSecret(h.ctx, admin, apitypes.GitHubClientSecretRequest{ClientSecret: fakeClientSecret}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("org admin set the client secret: %v", err)
	}
	if err := h.c.SetGitHubAppClientSecret(h.ctx, testSystem, apitypes.GitHubClientSecretRequest{ClientSecret: "wrong"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("wrong client secret: %v", err)
	}
	if err := h.c.SetGitHubAppClientSecret(h.ctx, testSystem, apitypes.GitHubClientSecretRequest{ClientSecret: fakeClientSecret}); err != nil {
		t.Fatal(err)
	}
	if st, err := h.c.GitHubStatus(h.ctx, testSystem); err != nil || !st.App.VerifiesInstalls {
		t.Fatalf("status with the client: %+v, %v", st, err)
	}
	if err := h.installApp(admin, "", fakeInstall, octo); err != nil {
		t.Fatalf("org admin's install once installs are checked: %v", err)
	}
}
