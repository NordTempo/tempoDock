// Package oidctest is a fake OpenID Connect provider for tests of single
// sign-on (ADR 0058, 0065): discovery, an RSA key set, and a token
// endpoint that checks the client and the PKCE verifier and returns the
// ID token a test describes; and a fake GitHub (NewGitHub): its OAuth
// token endpoint and the API's /user and /user/emails. Tests play the
// browser: they read the authorization URL Dock built and Authorize a
// code for it themselves.
package oidctest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// ClientID and ClientSecret are the client Dock is registered as.
const (
	ClientID     = "dock-client"
	ClientSecret = "dock-client-secret"
)

// Provider is a running fake provider: URL is where it runs, Issuer its
// issuer there.
type Provider struct {
	URL, Issuer string
	key         *rsa.PrivateKey

	mu    sync.Mutex
	codes map[string]grant
}

// grant is an authorization code waiting to be exchanged.
type grant struct {
	challenge, redirectURI string
	claims                 map[string]any
}

// New starts a provider for the test's lifetime.
func New(t testing.TB) *Provider { return NewAt(t, "") }

// NewAt starts a provider whose issuer is its URL with path (like
// "/realms/staff", or "/" for an issuer with a trailing slash). Each of
// aliases (like "/contoso.com/v2.0") serves the same discovery document,
// as Microsoft does for a tenant's domains.
func NewAt(t testing.TB, path string, aliases ...string) *Provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{key: key, codes: map[string]grant{}}
	mux := http.NewServeMux()
	discovery := func(w http.ResponseWriter, _ *http.Request) {
		base := strings.TrimSuffix(p.Issuer, "/")
		writeJSON(w, map[string]any{
			"issuer": p.Issuer, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token",
			"jwks_uri": base + "/jwks", "response_types_supported": []string{"code"},
			"subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}
	base := strings.TrimSuffix(path, "/")
	for _, at := range append([]string{base}, aliases...) {
		mux.HandleFunc("GET "+at+"/.well-known/openid-configuration", discovery)
	}
	mux.HandleFunc("GET "+base+"/jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("POST "+base+"/token", p.token)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p.URL = srv.URL
	p.Issuer = srv.URL + path
	return p
}

// Request is what Dock's authorization URL asked the provider for.
type Request struct {
	State, Nonce, Challenge, RedirectURI string
}

// Read parses Dock's authorization URL, failing the test unless it asks
// for the code flow with PKCE S256 and the openid scope.
func Read(t testing.TB, authURL string) Request {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") != ClientID || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" || q.Get("scope") != "openid email profile" {
		t.Fatalf("authorization URL %s", authURL)
	}
	return Request{State: q.Get("state"), Nonce: q.Get("nonce"), Challenge: q.Get("code_challenge"), RedirectURI: q.Get("redirect_uri")}
}

// Claims are an ID token's claims for req: the provider's issuer, Dock's
// client as audience, five minutes to live, req's nonce, and the person
// with email, verified. Tests change them before Authorize.
func (p *Provider) Claims(req Request, subject, email string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": p.Issuer, "aud": ClientID, "sub": subject, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"nonce": req.Nonce, "email": email, "email_verified": true, "name": "Test Person",
	}
}

// Authorize returns a code for req whose exchange gives an ID token with
// claims: the person signed in and agreed.
func (p *Provider) Authorize(req Request, claims map[string]any) string {
	code := rand.Text()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.codes[code] = grant{challenge: req.Challenge, redirectURI: req.RedirectURI, claims: claims}
	return code
}

// token exchanges a code once, for the registered client with the PKCE
// verifier of its challenge and the same redirect URI.
func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	p.mu.Lock()
	g, found := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code"))
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	switch {
	case id != ClientID || secret != ClientSecret:
		oauthError(w, http.StatusUnauthorized, "invalid_client")
		return
	case !found || r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("redirect_uri") != g.redirectURI:
		oauthError(w, http.StatusBadRequest, "invalid_grant")
		return
	case b64(sum[:]) != g.challenge:
		oauthError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	writeJSON(w, map[string]any{"access_token": rand.Text(), "token_type": "Bearer", "expires_in": 300, "id_token": p.Sign(g.claims)})
}

// Sign makes a JWT of claims signed with the provider's key (RS256).
func (p *Provider) Sign(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	signing := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return signing + "." + b64(sig)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

// GitHub is a running fake of GitHub's OAuth and API, for the github
// kind: AuthURL, TokenURL and API are where Dock finds it.
type GitHub struct {
	AuthURL, TokenURL, API string

	mu     sync.Mutex
	codes  map[string]githubGrant
	tokens map[string]GitHubUser
}

// GitHubUser is a GitHub account as the API describes it.
type GitHubUser struct {
	ID     int64
	Login  string
	Emails []GitHubEmail
}

// GitHubEmail is one of an account's emails.
type GitHubEmail struct {
	Email             string
	Primary, Verified bool
}

type githubGrant struct {
	challenge, redirectURI string
	user                   GitHubUser
}

// NewGitHub starts a fake GitHub for the test's lifetime.
func NewGitHub(t testing.TB) *GitHub {
	t.Helper()
	g := &GitHub{codes: map[string]githubGrant{}, tokens: map[string]GitHubUser{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", g.token)
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if u, ok := g.user(r); ok {
			writeJSON(w, map[string]any{"id": u.ID, "login": u.Login, "name": nil})
		} else {
			http.Error(w, "bad credentials", http.StatusUnauthorized)
		}
	})
	mux.HandleFunc("GET /user/emails", func(w http.ResponseWriter, r *http.Request) {
		u, ok := g.user(r)
		if !ok {
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		out := make([]map[string]any, len(u.Emails))
		for i, e := range u.Emails {
			out[i] = map[string]any{"email": e.Email, "primary": e.Primary, "verified": e.Verified}
		}
		writeJSON(w, out)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	g.AuthURL, g.TokenURL, g.API = srv.URL+"/login/oauth/authorize", srv.URL+"/login/oauth/access_token", srv.URL
	return g
}

// ReadGitHub parses Dock's GitHub authorization URL, failing the test
// unless it asks for a code with PKCE S256 and the user:email scope.
func ReadGitHub(t testing.TB, authURL string) Request {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") != ClientID || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("scope") != "read:user user:email" {
		t.Fatalf("authorization URL %s", authURL)
	}
	return Request{State: q.Get("state"), Challenge: q.Get("code_challenge"), RedirectURI: q.Get("redirect_uri")}
}

// Authorize returns a code for req whose exchange gives a token for user.
func (g *GitHub) Authorize(req Request, user GitHubUser) string {
	code := rand.Text()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.codes[code] = githubGrant{challenge: req.Challenge, redirectURI: req.RedirectURI, user: user}
	return code
}

// token exchanges a code once, like GitHub: client credentials in the
// form, the PKCE verifier, and a form-encoded answer.
func (g *GitHub) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	gr, found := g.codes[r.PostForm.Get("code")]
	delete(g.codes, r.PostForm.Get("code"))
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if r.PostForm.Get("client_id") != ClientID || r.PostForm.Get("client_secret") != ClientSecret || !found ||
		r.PostForm.Get("redirect_uri") != gr.redirectURI || b64(sum[:]) != gr.challenge {
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		_, _ = w.Write([]byte("error=bad_verification_code"))
		return
	}
	tok := rand.Text()
	g.tokens[tok] = gr.user
	w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
	_, _ = w.Write([]byte(url.Values{"access_token": {tok}, "token_type": {"bearer"}, "scope": {"read:user,user:email"}}.Encode()))
}

// user is the account a request's bearer token is for.
func (g *GitHub) user(r *http.Request) (GitHubUser, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	u, ok := g.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	return u, ok
}
