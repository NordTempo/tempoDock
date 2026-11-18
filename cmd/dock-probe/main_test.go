package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRun probes real listeners on loopback: what passes, and what each
// failure says.
func TestRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The probe's trace context: a probe trace ID.
		tp := r.Header.Get("traceparent")
		if r.URL.Path != "/up" || len(tp) != 55 || !strings.HasPrefix(tp, "00-646f636b70726265") || !strings.HasSuffix(tp, "-01") {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	up := port(t, srv.Listener)

	// hung accepts connections and never answers.
	hung, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hung.Close() }()
	held := make(chan net.Conn, 4) // kept open, unanswered, until the test ends
	go func() {
		for {
			c, err := hung.Accept()
			if err != nil {
				return
			}
			held <- c
		}
	}()

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := port(t, closed)
	_ = closed.Close()

	// Four PostgreSQL servers, by their first answer to a startup message.
	starting := pgServer(t, pgError("57P03", "the database system is starting up"))
	asksAuth := pgServer(t, []byte{'R', 0, 0, 0, 8, 0, 0, 0, 3})
	badLogin := pgServer(t, pgError("28P01", "password authentication failed"))
	cutShort := pgServer(t, []byte{'E', 0, 0, 0, 40, 'S'})

	for _, tc := range []struct {
		args []string
		want string // "" passes
	}{
		{[]string{"pg", starting, "1000"}, "PostgreSQL is not accepting connections yet: the database system is starting up"},
		{[]string{"pg", asksAuth, "1000"}, ""},
		{[]string{"pg", badLogin, "1000"}, ""},
		{[]string{"pg", cutShort, "1000"}, "connection closed without a whole answer"},
		{[]string{"pg", port(t, hung), "200"}, "timed out"},
		{[]string{"pg", refused, "200"}, "connection refused"},
		{[]string{"http", up, "/up", "1000"}, ""},
		{[]string{"http", up, "/down", "1000"}, "GET /down returned 503"},
		{[]string{"http", port(t, hung), "/up", "200"}, "GET /up: timed out"},
		{[]string{"http", refused, "/up", "200"}, "connection refused"},
		{[]string{"tcp", up, "1000"}, ""},
		{[]string{"tcp", refused, "200"}, "connection refused"},
	} {
		err := run(tc.args)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%v: %v", tc.args, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%v = %v, want %q", tc.args, err, tc.want)
		}
	}
}

// pgServer listens on loopback and answers every connection's startup
// message (protocol 3.0, user and database postgres) with answer, then
// closes. It returns its port.
func pgServer(t *testing.T, answer []byte) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	want := "\x00\x00\x00\x29\x00\x03\x00\x00user\x00postgres\x00database\x00postgres\x00\x00"
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			got := make([]byte, len(want))
			if _, err := io.ReadFull(c, got); err != nil || string(got) != want {
				t.Errorf("startup message = %q, %v", got, err)
			}
			_, _ = c.Write(answer)
			_ = c.Close()
		}
	}()
	return port(t, l)
}

// pgError is an ErrorResponse with the severity, SQLSTATE code and
// message fields, as PostgreSQL sends them.
func pgError(code, message string) []byte {
	body := "SFATAL\x00VFATAL\x00C" + code + "\x00M" + message + "\x00\x00"
	n := 4 + len(body)
	return append([]byte{'E', byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}, body...)
}

// port is l's port as the probe takes it.
func port(t *testing.T, l net.Listener) string {
	t.Helper()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

// TestRelay joins a TCP connection to a Unix socket both ways, passing
// the end of stream on, and stops once standard input closes.
func TestRelay(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "r.sock")
	ul, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ul.Close() }()
	go func() {
		for {
			c, err := ul.Accept()
			if err != nil {
				return
			}
			go func() { // echoes, upper-cased, until the client's end
				defer func() { _ = c.Close() }()
				b, _ := io.ReadAll(c)
				_, _ = c.Write(bytes.ToUpper(b))
			}()
		}
	}()
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := free.Addr().String()
	_ = free.Close()

	stdin, closeStdin := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- relay(addr, sock, stdin) }()
	var c net.Conn
	for range 100 {
		if c, err = net.Dial("tcp", addr); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("push"))
	_ = c.(*net.TCPConn).CloseWrite()
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "PUSH" {
		t.Fatalf("relayed %q, %v; want PUSH", got, err)
	}
	_ = closeStdin.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relay still runs after its standard input closed")
	}
}

// TestWalPush: archive_command sends the file with its length and mtime,
// succeeds on 200 only, and carries the archiver's error otherwise.
func TestWalPush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "000000010000000000000003")
	if err := os.WriteFile(path, []byte("segment"), 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.UnixMilli(1_763_400_000_123)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPut || r.URL.Path != "/wal/000000010000000000000003" || string(body) != "segment" || r.Header.Get("X-Dock-Mtime") != "1763400000123" {
			t.Errorf("got %s %s %q mtime %s", r.Method, r.URL.Path, body, r.Header.Get("X-Dock-Mtime"))
		}
		if fail {
			http.Error(w, "dock-wal: s3: connection refused", http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	if err := walPush(addr, path, "000000010000000000000003"); err != nil {
		t.Fatal(err)
	}
	fail = true
	err := walPush(addr, path, "000000010000000000000003")
	if err == nil || !strings.Contains(err.Error(), "answered 502: dock-wal: s3: connection refused") {
		t.Fatalf("a failed upload: %v", err)
	}
}
