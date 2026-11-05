// Package oidctest is a fake OpenID Connect provider for tests of single
// sign-on (ADR 0058): discovery, an RSA key set, and a token endpoint
// that checks the client and the PKCE verifier and returns the ID token a
// test describes. Tests play the browser: they read the authorization
// URL Dock built and Authorize a code for it themselves.
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
	"sync"
	"testing"
	"time"
)

// ClientID and ClientSecret are the client Dock is registered as.
const (
	ClientID     = "dock-client"
	ClientSecret = "dock-client-secret"
)

// Provider is a running fake provider; Issuer is its URL.
type Provider struct {
	Issuer string
	key    *rsa.PrivateKey

	mu    sync.Mutex
	codes map[string]grant
}

// grant is an authorization code waiting to be exchanged.
type grant struct {
	challenge, redirectURI string
	claims                 map[string]any
}

// New starts a provider for the test's lifetime.
func New(t testing.TB) *Provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{key: key, codes: map[string]grant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer": p.Issuer, "authorization_endpoint": p.Issuer + "/authorize", "token_endpoint": p.Issuer + "/token",
			"jwks_uri": p.Issuer + "/jwks", "response_types_supported": []string{"code"},
			"subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("POST /token", p.token)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p.Issuer = srv.URL
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
