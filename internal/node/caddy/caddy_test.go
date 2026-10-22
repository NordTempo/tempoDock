package caddy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/NordTempo/tempoDock/internal/node"
)

// dropFirstConn hangs up the first connection it accepts after reading part
// of the request, like a Caddy admin server that accepted it just before a
// reload shut it down.
type dropFirstConn struct {
	net.Listener
	dropped atomic.Bool
}

func (l *dropFirstConn) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil || l.dropped.Swap(true) {
			return c, err
		}
		_, _ = c.Read(make([]byte, 64))
		_ = c.Close()
	}
}

func TestLoadResendsDroppedConnection(t *testing.T) {
	for name, tc := range map[string]struct {
		status  int
		body    string
		wantErr string
	}{
		"applied on resend": {status: http.StatusOK},
		// A config error is Caddy's answer: not resent.
		"rejection not resent": {status: http.StatusBadRequest, body: `{"error":"bad route"}`, wantErr: "caddy rejected the config: bad route"},
	} {
		t.Run(name, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "caddyload") // short path for the socket
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			sock := filepath.Join(dir, "admin.sock")
			l, err := net.Listen("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			var loads atomic.Int64
			s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				loads.Add(1)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			s.Listener = &dropFirstConn{Listener: l}
			s.Start()
			t.Cleanup(s.Close)

			opts := testOptions()
			opts.AdminSocket = sock
			p, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			err = p.ApplyRoutes(context.Background(), []node.Route{{Hostnames: []string{"a.test"}, Upstream: "127.0.0.1:1"}})
			if tc.wantErr == "" && err != nil {
				t.Fatalf("ApplyRoutes: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("ApplyRoutes: got %v, want %q", err, tc.wantErr)
			}
			// The first connection was dropped before the handler ran.
			if got := loads.Load(); got != 1 {
				t.Errorf("Caddy answered %d loads, want 1", got)
			}
		})
	}
}
