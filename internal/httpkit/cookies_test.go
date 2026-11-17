package httpkit

import (
	"net/http"
	"testing"
	"time"
)

// jar is a request's cookies for SessionSecret, in the order sent.
func jar(cookies ...*http.Cookie) func(string) (*http.Cookie, error) {
	return func(name string) (*http.Cookie, error) {
		for _, c := range cookies {
			if c.Name == name {
				return c, nil
			}
		}
		return nil, http.ErrNoCookie
	}
}

// Over HTTPS the session is read from its host-locked name first, and a
// cookie under the bare name is only ever a legacy one; over plain HTTP
// there is only the bare name.
func TestSessionSecret(t *testing.T) {
	bare := &http.Cookie{Name: "app_session", Value: "bare"}
	locked := &http.Cookie{Name: "__Host-app_session", Value: "locked"}
	for _, tc := range []struct {
		name    string
		cookies []*http.Cookie
		secure  bool
		secret  string
		legacy  bool
	}{
		{"https, both sent, the bare one first", []*http.Cookie{bare, locked}, true, "locked", false},
		{"https, only the bare one", []*http.Cookie{bare}, true, "bare", true},
		{"https, none", nil, true, "", false},
		{"http, the bare one", []*http.Cookie{bare}, false, "bare", false},
		{"http never reads the prefixed one", []*http.Cookie{locked}, false, "", false},
	} {
		secret, legacy := SessionSecret(jar(tc.cookies...), "app_session", tc.secure)
		if secret != tc.secret || legacy != tc.legacy {
			t.Errorf("%s: %q, legacy %v; want %q, %v", tc.name, secret, legacy, tc.secret, tc.legacy)
		}
	}
}

// Over HTTPS the session cookie is one a browser locks to the host, and
// the bare-name one from before is cleared with it.
func TestSessionCookies(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	set := SessionCookies("app_session", "s3cret", expires, true)
	if len(set) != 2 {
		t.Fatalf("over HTTPS: %v", set)
	}
	if c := set[0]; c.Name != "__Host-app_session" || c.Value != "s3cret" || !c.Secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" || c.SameSite != http.SameSiteStrictMode || !c.Expires.Equal(expires) {
		t.Errorf("the session cookie over HTTPS: %+v", c)
	}
	if c := set[1]; c.Name != "app_session" || c.Value != "" || c.MaxAge >= 0 {
		t.Errorf("the bare name isn't cleared: %+v", c)
	}
	plain := SessionCookies("app_session", "s3cret", expires, false)
	if len(plain) != 1 || plain[0].Name != "app_session" || plain[0].Secure {
		t.Errorf("over plain HTTP: %v", plain)
	}
	if gone := SessionCookies("app_session", "", time.Time{}, true); gone[0].Name != "__Host-app_session" || gone[0].MaxAge >= 0 {
		t.Errorf("signing out: %+v", gone[0])
	}
}
