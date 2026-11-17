package caddy

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/NordTempo/tempoDock/internal/node"
)

// fakeAdmin serves Caddy's admin API on a Unix socket: /load accepts any
// config and /config/apps/http/servers returns running.
func fakeAdmin(t *testing.T, running *atomic.Pointer[string]) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "caddyadmin") // short path for the socket
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "admin.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/load":
		case "/config/apps/http/servers":
			cfg := running.Load()
			if cfg == nil {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			_, _ = io.WriteString(w, *cfg)
		default:
			http.NotFound(w, r)
		}
	}))
	s.Listener = l
	s.Start()
	t.Cleanup(s.Close)
	return sock
}

func TestAskHandler(t *testing.T) {
	var running atomic.Pointer[string]
	opts := testOptions()
	opts.AdminSocket = fakeAdmin(t, &running)
	p, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ask := func(host string) int {
		rec := httptest.NewRecorder()
		p.AskHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/internal/caddy/ask?domain="+host, nil))
		return rec.Code
	}

	// Caddy unreachable before any routes were applied: refuse, don't cache.
	if code := ask("a.test"); code != http.StatusServiceUnavailable {
		t.Errorf("unreadable running config: got %d, want 503", code)
	}

	// After a restart, the hostnames come from Caddy's running config.
	cfg := render(t, opts, []node.Route{
		{Hostnames: []string{"a.test"}, Upstream: "127.0.0.1:1"},
		{Hostnames: []string{"www.a.test"}, RedirectTo: "a.test"},
	})
	servers, err := json.Marshal(cfg.Apps.HTTP.Servers)
	if err != nil {
		t.Fatal(err)
	}
	running.Store(new(string(servers)))
	for host, want := range map[string]int{"a.test": 200, "www.a.test": 200, "A.TEST": 200, "b.test": 404, "": 404} {
		if code := ask(host); code != want {
			t.Errorf("seeded ask %q: got %d, want %d", host, code, want)
		}
	}

	// Applied routes replace what was read from Caddy.
	if err := p.ApplyRoutes(context.Background(), []node.Route{{Hostnames: []string{"b.test"}, Upstream: "127.0.0.1:1"}}, nil); err != nil {
		t.Fatal(err)
	}
	running.Store(nil) // never read again
	for host, want := range map[string]int{"b.test": 200, "a.test": 404} {
		if code := ask(host); code != want {
			t.Errorf("applied ask %q: got %d, want %d", host, code, want)
		}
	}
}
