package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NordTempo/tempoDock/internal/core"
)

func TestPublicTakesTheClientFromCaddy(t *testing.T) {
	var addr, header string
	h := Public(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		addr, header = r.RemoteAddr, r.Header.Get(core.ClientIPHeader)
	}), http.NotFoundHandler())
	for set, want := range map[string]string{"203.0.113.9": "203.0.113.9:0", "2001:db8::1": "[2001:db8::1]:0", "not an ip": "127.0.0.1:5555"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "127.0.0.1:5555"
		r.Header.Set(core.ClientIPHeader, set)
		h.ServeHTTP(httptest.NewRecorder(), r)
		if addr != want || header != "" {
			t.Errorf("%q: RemoteAddr %q, header %q; want %q and no header", set, addr, header, want)
		}
	}
}

func TestPublicServesWebhooks(t *testing.T) {
	var got string
	h := Public(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { got = "api" }),
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { got = "hooks" }))
	for path, want := range map[string]string{"/hooks/github/abc": "hooks", "/api/v1/health": "api", "/hooks": "api"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil))
		if got != want {
			t.Errorf("%s went to %s, want %s", path, got, want)
		}
	}
}
