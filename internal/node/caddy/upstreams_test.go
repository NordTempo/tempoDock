package caddy

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/node"
)

// keyedRoute is a service's route on release rel at port.
func keyedRoute(port, rel string) node.Route {
	return node.Route{
		Hostnames: []string{"a.test"}, Upstream: "127.0.0.1:" + port, Key: "s7",
		Analytics: []node.AnalyticsHosts{{Hostnames: []string{"a.test"}, Tag: "3.7." + rel}},
	}
}

// TestRenderKeyedRoutesIgnoreTheRelease: switching a keyed route to
// another port and release renders the same config, with the upstream and
// the analytics tag's release read from files, and a refused connection
// retried whether or not a move holds it (ADR 0091).
func TestRenderKeyedRoutesIgnoreTheRelease(t *testing.T) {
	opts := testOptions()
	opts.UpstreamsDir, opts.AnalyticsAddr = "/var/lib/dock-upstreams", "127.0.0.1:7077"
	before, err := Render(opts, []node.Route{keyedRoute("20001", "40")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	held := keyedRoute("20002", "41")
	held.Hold = true
	after, err := Render(opts, []node.Route{held}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("a switch changed the config:\n%s\n%s", before, after)
	}
	for _, want := range []string{
		`"dial":"{file./var/lib/dock-upstreams/s7}"`,
		`"3.7.{file./var/lib/dock-upstreams/s7.release}"`,
		`"try_duration":"` + keyedRetryFor.String() + `"`,
	} {
		if !bytes.Contains(before, []byte(want)) {
			t.Errorf("config lacks %s:\n%s", want, before)
		}
	}
	// Without the directory, upstreams stay in the config.
	opts.UpstreamsDir = ""
	if static, _ := Render(opts, []node.Route{keyedRoute("20001", "40")}, nil); !bytes.Contains(static, []byte(`"dial":"127.0.0.1:20001"`)) {
		t.Errorf("without an upstreams directory: %s", static)
	}
}

// loadRecorder answers /load on a Unix socket, recording what the upstream
// file held at each load, and rejecting loads while reject is set.
type loadRecorder struct {
	file   string
	seen   []string
	reject bool
}

func (f *loadRecorder) start(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "caddyup") // short path for the socket
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
		if r.URL.Path == "/reverse_proxy/upstreams" {
			_, _ = io.WriteString(w, `[{"address":"{file.`+filepath.Dir(f.file)+`/s7}","num_requests":3},{"address":"127.0.0.1:9","num_requests":1}]`)
			return
		}
		b, _ := os.ReadFile(f.file)
		f.seen = append(f.seen, string(b))
		if f.reject {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"no"}`)
		}
	}))
	s.Listener = l
	s.Start()
	t.Cleanup(s.Close)
	return sock
}

// TestApplyRoutesWritesFilesAroundTheLoad: a new key's file exists before
// the config that reads it loads; a changed one changes only after a load
// worked, so a rejected config never moves traffic; a gone one is removed,
// as are files left by routes from before a restart.
func TestApplyRoutesWritesFilesAroundTheLoad(t *testing.T) {
	dir := t.TempDir()
	if err := CheckUpstreamsDir(dir); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "s99")
	if err := os.WriteFile(stale, []byte("127.0.0.1:1"), 0o640); err != nil {
		t.Fatal(err)
	}
	f := &loadRecorder{file: filepath.Join(dir, "s7")}
	opts := testOptions()
	opts.AdminSocket, opts.UpstreamsDir = f.start(t), dir
	p, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	apply := func(r ...node.Route) error { return p.ApplyRoutes(ctx, r, nil) }
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(dir, name)); return string(b) }

	if err := apply(keyedRoute("20001", "40")); err != nil {
		t.Fatal(err)
	}
	if f.seen[0] != "127.0.0.1:20001" {
		t.Errorf("at the first load the file held %q, want it written before", f.seen[0])
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("a file no route reads survived the first apply: %v", err)
	}

	f.reject = true
	if err := apply(keyedRoute("20002", "41")); err == nil {
		t.Fatal("a rejected load reported no error")
	}
	if got := read("s7"); got != "127.0.0.1:20001" {
		t.Errorf("after a rejected load the upstream is %q, want the running one", got)
	}

	f.reject = false
	if err := apply(keyedRoute("20002", "41")); err != nil {
		t.Fatal(err)
	}
	if last := f.seen[len(f.seen)-1]; last != "127.0.0.1:20001" {
		t.Errorf("the switch's load saw %q, want the old upstream until it loaded", last)
	}
	if read("s7") != "127.0.0.1:20002" || read("s7.release") != "41" {
		t.Errorf("after the switch: %q, release %q", read("s7"), read("s7.release"))
	}
	// Sleep's check asks by address and gets the route's requests.
	if n, err := p.OpenRequests(ctx, []string{"127.0.0.1:20002"}); err != nil || n != 3 {
		t.Errorf("OpenRequests = %d, %v; want the keyed route's 3", n, err)
	}

	if err := apply(node.Route{Hostnames: []string{"b.test"}, Upstream: "127.0.0.1:9"}); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	var left []string
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), ".") {
			left = append(left, e.Name())
		}
	}
	if len(left) > 0 {
		t.Errorf("files of gone routes: %v", left)
	}
}
