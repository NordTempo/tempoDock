package tbhttp

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/httpkit"
	"github.com/NordTempo/tempoDock/internal/tempobase"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// testServer runs tempoBase over a temp data directory and returns it
// with its setup code.
func testServer(t testing.TB) (*httptest.Server, *tempobase.App, string) {
	return testServerAt(t, "")
}

// testServerAt is testServer with a base URL: an https one makes every
// client a secure one, as behind a TLS proxy.
func testServerAt(t testing.TB, baseURL string) (*httptest.Server, *tempobase.App, string) {
	t.Helper()
	cfg, err := tempobase.LoadConfig("", func(k string) string {
		switch k {
		case "TEMPOBASE_DATA_DIR":
			return t.TempDir()
		case "TEMPOBASE_BASE_URL":
			return baseURL
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	app, err := tempobase.Open(t.Context(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	code, _, err := app.StartSetup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(app, Options{Logger: slog.New(slog.DiscardHandler)}))
	t.Cleanup(srv.Close)
	return srv, app, code
}

// apiClient calls the API as a browser (cookie, same-origin) or with a
// bearer token.
type apiClient struct {
	t      testing.TB
	srv    *httptest.Server
	cookie *http.Cookie
	bearer string
}

// call sends a request and decodes a 2xx JSON answer into out; it
// returns the status and the body.
func (c *apiClient) call(method, path string, body, out any) (int, []byte) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.srv.URL+Prefix+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	} else {
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	res, err := c.srv.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	for _, ck := range res.Cookies() {
		if ck.Name == httpkit.CookieName(sessionCookie, ck.Secure) {
			c.cookie = ck
		}
	}
	if out != nil && res.StatusCode < 300 {
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("%s %s: %v in %s", method, path, err, b)
		}
	}
	return res.StatusCode, b
}

// must is call that fails the test unless the status is want.
func (c *apiClient) must(want int, method, path string, body, out any) {
	c.t.Helper()
	if status, b := c.call(method, path, body, out); status != want {
		c.t.Fatalf("%s %s = %d, want %d: %s", method, path, status, want, b)
	}
}

// The non-data API end to end: setup, session cookie, CSRF, connections
// without their password, tokens over bearer auth.
func TestAPI(t *testing.T) {
	srv, _, code := testServer(t)
	anon := &apiClient{t: t, srv: srv}
	var status tbtypes.SetupStatus
	anon.must(http.StatusOK, http.MethodGet, "/setup", nil, &status)
	if !status.Needed {
		t.Fatal("setup not needed on a new server")
	}
	anon.must(http.StatusUnauthorized, http.MethodGet, "/me", nil, nil)

	admin := &apiClient{t: t, srv: srv}
	var me tbtypes.Me
	admin.must(http.StatusOK, http.MethodPost, "/setup", tbtypes.SetupRequest{Code: code, Email: "admin@example.com", Name: "Admin", Password: "correct horse battery"}, &me)
	if me.Role != tbtypes.RoleAdmin || admin.cookie == nil || !admin.cookie.HttpOnly || admin.cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("setup: %+v, cookie %+v", me, admin.cookie)
	}
	admin.must(http.StatusOK, http.MethodGet, "/me", nil, &me)

	// A cross-site request can't ride on the cookie.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+Prefix+"/invites", strings.NewReader(`{"email":"x@example.com","role":"viewer"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(admin.cookie)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site POST = %d", res.StatusCode)
	}

	var conn tbtypes.Connection
	admin.must(http.StatusOK, http.MethodPost, "/connections", tbtypes.CreateConnectionRequest{Name: "orders", URL: "postgres://app:hunter2-secret@db.example.com/orders"}, &conn)
	if conn.TLS != tbtypes.TLSPrefer || conn.Access.Level != apitypes.LevelWrite {
		t.Fatalf("connection %+v", conn)
	}
	_, body := admin.call(http.MethodGet, "/connections", nil, nil)
	if bytes.Contains(body, []byte("hunter2")) {
		t.Fatal("connection list contains the password")
	}
	var failed apitypes.Error
	got, body := admin.call(http.MethodPost, "/connections", tbtypes.CreateConnectionRequest{Name: "bad", URL: "postgres://app:pw@db/x?foo=1"}, nil)
	if _ = json.Unmarshal(body, &failed); got != http.StatusBadRequest || failed.Code != apitypes.CodeValidation {
		t.Fatalf("bad URL = %d %s", got, body)
	}

	// A token works over bearer auth only, and its connection limit shows.
	var created tbtypes.CreatedToken
	admin.must(http.StatusOK, http.MethodPost, "/tokens", tbtypes.CreateTokenRequest{Name: "agent", Connections: []string{"orders"}}, &created)
	agent := &apiClient{t: t, srv: srv, bearer: created.Secret}
	agent.must(http.StatusOK, http.MethodGet, "/me", nil, &me)
	if me.Kind != "token" || len(me.TokenConnections) != 1 {
		t.Fatalf("token me %+v", me)
	}
	agent.must(http.StatusForbidden, http.MethodGet, "/users", nil, nil)
	agent.must(http.StatusNotFound, http.MethodGet, "/connections/orders", nil, nil)

	admin.must(http.StatusNoContent, http.MethodPost, "/auth/logout", nil, nil)
	admin.must(http.StatusUnauthorized, http.MethodGet, "/me", nil, nil)

	// Everything outside /api/ is the UI.
	res, err = srv.Client().Get(srv.URL + "/c/orders")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("UI route = %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
}

// Over HTTPS the session cookie is __Host-tempobase_session, which an app
// on a sibling hostname can't set, and one sent under the bare name
// neither replaces it nor signs a new session in.
func TestSessionCookieOverHTTPS(t *testing.T) {
	srv, _, code := testServerAt(t, "https://tempobase.example.com")
	admin := &apiClient{t: t, srv: srv}
	admin.must(http.StatusOK, http.MethodPost, "/setup", tbtypes.SetupRequest{Code: code, Email: "admin@example.com", Name: "Admin", Password: "correct horse battery"}, nil)
	session := admin.cookie
	if session == nil || session.Name != httpkit.HostPrefix+sessionCookie || !session.Secure || !session.HttpOnly || session.Path != "/" || session.Domain != "" {
		t.Fatalf("the session cookie over HTTPS: %+v", session)
	}
	me := func(cookies ...*http.Cookie) int {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+Prefix+"/me", nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	if got := me(session); got != http.StatusOK {
		t.Fatalf("the session = %d", got)
	}
	// What a browser sends once another host set the bare name for the
	// parent domain with a longer path: that one first.
	if got := me(&http.Cookie{Name: sessionCookie, Value: "set-by-another-host"}, session); got != http.StatusOK {
		t.Fatalf("with a cookie under the bare name ahead of the session's = %d", got)
	}
	if got := me(&http.Cookie{Name: sessionCookie, Value: session.Value}); got != http.StatusUnauthorized {
		t.Fatalf("a new session under the bare name = %d", got)
	}
	// Signing out ends the session.
	admin.must(http.StatusNoContent, http.MethodPost, "/auth/logout", nil, nil)
	if got := me(session); got != http.StatusUnauthorized {
		t.Fatalf("after logout = %d", got)
	}
}
