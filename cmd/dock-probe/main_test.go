package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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

	for _, tc := range []struct {
		args []string
		want string // "" passes
	}{
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
