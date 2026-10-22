package registry

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var testCfg = Config{Service: "registry.example.com", Realm: "https://dock.example.com/registry/token"}

func testSigner(t *testing.T) *Signer {
	t.Helper()
	key, err := LoadOrCreateKey(filepath.Join(t.TempDir(), KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	return NewSigner(key, testCfg)
}

func TestMintVerify(t *testing.T) {
	s := testSigner(t)
	now := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { return now }

	token := s.Mint("server 2", TokenTTL, Pull("p1/web"))
	c, err := s.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	want := Claims{
		Issuer: "dock", Subject: "server 2", Audience: "registry.example.com",
		Expires: now.Add(15 * time.Minute).Unix(), NotBefore: now.Unix(), IssuedAt: now.Unix(), ID: c.ID,
		Access: []Access{{Type: "repository", Name: "p1/web", Actions: []string{"pull"}}},
	}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("claims = %+v, want %+v", c, want)
	}
	var h header
	if err := decodeSegment(strings.Split(token, ".")[0], &h); err != nil {
		t.Fatal(err)
	}
	var set struct{ Keys []map[string]string }
	if err := json.Unmarshal(JWKS(s), &set); err != nil {
		t.Fatal(err)
	}
	if h.Alg != "EdDSA" || len(set.Keys) != 1 || set.Keys[0]["kid"] != h.Kid {
		t.Errorf("header %+v doesn't name the JWKS key %v", h, set.Keys)
	}

	now = now.Add(TokenTTL)
	if _, err := s.Verify(token); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expired token: err = %v", err)
	}
}

func TestVerifyRejects(t *testing.T) {
	s := testSigner(t)
	token := s.Mint("x", TokenTTL, Push("p1/web"))

	other := testSigner(t)
	elsewhere := NewSigner(s.key, Config{Service: "registry.other.com", Realm: testCfg.Realm})
	parts := strings.Split(token, ".")
	tampered := parts[0] + "." + b64([]byte(`{"iss":"dock","aud":"registry.example.com","exp":9999999999,"access":[{"type":"repository","name":"p2/db","actions":["pull"]}]}`)) + "." + parts[2]

	for name, tc := range map[string]struct {
		s     *Signer
		token string
	}{
		"other key":      {other, token},
		"other registry": {elsewhere, token},
		"tampered":       {s, tampered},
		"garbage":        {s, "a.b"},
	} {
		if _, err := tc.s.Verify(tc.token); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: err = %v, want ErrInvalidToken", name, err)
		}
	}
}

func TestLoadOrCreateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", KeyFile)
	k1, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v, %v", fi, err)
	}
	k2, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !k1.Equal(k2) {
		t.Error("reloaded key differs")
	}
	_ = os.WriteFile(path, []byte("short"), 0o600)
	if _, err := LoadOrCreateKey(path); err == nil {
		t.Error("a truncated key file loaded")
	}
}

func TestHandler(t *testing.T) {
	s := testSigner(t)
	token := s.Mint("server 2", TokenTTL, Pull("p1/web"))
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	get := func(user, pass string) (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"?service=registry.example.com&scope=repository:p1/web:pull", nil)
		if pass != "" {
			req.SetBasicAuth(user, pass)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}

	if code, body := get("dock", token); code != http.StatusOK || body["token"] != token || body["expires_in"].(float64) < 890 {
		t.Errorf("valid token: %d %v", code, body)
	}
	if code, _ := get("", ""); code != http.StatusUnauthorized {
		t.Errorf("no credentials: %d", code)
	}
	forged := testSigner(t).Mint("me", TokenTTL, Pull("p1/web"))
	if code, _ := get("dock", forged); code != http.StatusUnauthorized {
		t.Errorf("token of another key: %d", code)
	}
}

func TestWriteConfig(t *testing.T) {
	dir := t.TempDir()
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	s := NewSigner(key, testCfg)
	for i, want := range []bool{true, false} {
		changed, err := WriteConfig(dir, s)
		if err != nil || changed != want {
			t.Fatalf("write %d: changed = %v, %v; want %v", i, changed, err, want)
		}
	}
	yml, _ := os.ReadFile(filepath.Join(dir, "config.yml"))
	for _, want := range []string{
		`realm: "https://dock.example.com/registry/token"`,
		`service: "registry.example.com"`,
		`issuer: "dock"`,
		"jwks: /etc/distribution/jwks.json",
		// Derived from the (zero) key, so stable across runs and installs.
		"secret: 38b828fc6d2807e52edbb2233ff0aa667e615b1229da923aa8315d3dffddf6ea",
	} {
		if !strings.Contains(string(yml), want) {
			t.Errorf("config.yml lacks %q:\n%s", want, yml)
		}
	}
	if _, err := WriteConfig(dir, NewSigner(key, Config{Service: "x"})); err == nil {
		t.Error("config without a realm written")
	}
}
