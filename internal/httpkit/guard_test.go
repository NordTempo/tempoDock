package httpkit

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The CSRF rules on their own; Dock's API tests them end to end.
func TestCheckBrowserRequest(t *testing.T) {
	tarOnUpload := func(r *http.Request, mt string) bool {
		return strings.HasSuffix(r.URL.Path, "/upload") && mt == "application/x-tar"
	}
	for _, tc := range []struct {
		name, method, path string
		headers            map[string]string
		ok                 bool
	}{
		{"reads pass", "GET", "/api/x", map[string]string{"Origin": "https://evil.example"}, true},
		{"bearer is exempt", "POST", "/api/x", map[string]string{"Authorization": "Bearer t", "Origin": "https://evil.example"}, true},
		{"same origin JSON", "POST", "/api/x", map[string]string{"Sec-Fetch-Site": "same-origin", "Content-Type": "application/json"}, true},
		{"cross site", "POST", "/api/x", map[string]string{"Sec-Fetch-Site": "same-site", "Content-Type": "application/json"}, false},
		{"foreign origin", "POST", "/api/x", map[string]string{"Origin": "https://evil.example", "Content-Type": "application/json"}, false},
		{"form body", "POST", "/api/x", map[string]string{"Content-Type": "text/plain"}, false},
		{"raw body where allowed", "POST", "/api/upload", map[string]string{"Content-Type": "application/x-tar"}, true},
		{"raw body elsewhere", "POST", "/api/x", map[string]string{"Content-Type": "application/x-tar"}, false},
	} {
		r := httptest.NewRequest(tc.method, "http://dock.local"+tc.path, nil)
		for k, v := range tc.headers {
			r.Header.Set(k, v)
		}
		if err := CheckBrowserRequest(r, tarOnUpload); (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// Every response says no other site shares a window with it and which
// browser features it uses (none); only one that arrived over HTTPS tells
// the browser to stay on HTTPS, for this hostname alone.
func TestGuardHeaders(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	for _, secure := range []bool{true, false} {
		h := Guard(ok, nil, func(*http.Request) bool { return secure })
		for _, path := range []string{"/", "/api/v1/me"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://dock.local"+path, nil))
			got := w.Header()
			if got.Get("Cross-Origin-Opener-Policy") != "same-origin" || !strings.Contains(got.Get("Permissions-Policy"), "camera=()") {
				t.Errorf("%s: headers %v", path, got)
			}
			want := ""
			if secure {
				want = "max-age=31536000"
			}
			if sts := got.Get("Strict-Transport-Security"); sts != want {
				t.Errorf("%s, over HTTPS %v: Strict-Transport-Security %q, want %q", path, secure, sts, want)
			}
		}
	}
}
