package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
	"github.com/NordTempo/tempoDock/internal/core"
)

// syncBuffer is a bytes.Buffer safe to write from a running command while
// the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestConnect: `dock connect` serves a local port whose connections reach
// the service through the API, until it is stopped.
func TestConnect(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = echo.Close() }()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	srv.rt.DialTo(echo.Addr().String())
	ctx := context.Background()
	if _, err := srv.core.CreateProject(ctx, core.System, apitypes.CreateProjectRequest{Name: "shop"}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.core.CreateEnvironment(ctx, core.System, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	ref := core.ServiceRef{Project: "shop", Environment: "staging", Service: "cache"}
	if _, err := srv.core.CreateService(ctx, core.System, ref.Env(), apitypes.CreateServiceRequest{Name: "cache", Port: 6379}); err != nil {
		t.Fatal(err)
	}
	rel, err := srv.core.DeployImage(ctx, core.System, ref, apitypes.DeployImageRequest{Image: "redis:8"})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); rel.Status != apitypes.ReleaseActive; time.Sleep(2 * time.Millisecond) {
		if rel, err = srv.core.GetRelease(ctx, core.System, ref, rel.Number); err != nil || time.Now().After(deadline) {
			t.Fatalf("deploy: %+v %v", rel, err)
		}
	}
	t.Setenv(clientconfig.EnvURL, srv.url)

	// Without access it fails before listening.
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeDeploy])
	code, _, stderr := exec(t, "connect", "cache", "-p", "shop", "-e", "staging")
	if code != int(output.ExitAuth) || !strings.Contains(stderr, "sensitive visible") || !strings.Contains(stderr, "dock db status") {
		t.Fatalf("deploy token: exit %d\n%s", code, stderr)
	}

	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	free, _ := net.Listen("tcp", "127.0.0.1:0")
	port := free.Addr().(*net.TCPAddr).Port
	_ = free.Close()
	runCtx, stop := context.WithCancel(ctx)
	var out, errOut syncBuffer
	exited := make(chan int)
	go func() {
		exited <- run(runCtx, []string{"connect", "cache", "-p", "shop", "-e", "staging", "--port", strconv.Itoa(port), "--json"},
			&output.IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	}()

	var conn net.Conn
	for deadline := time.Now().Add(5 * time.Second); conn == nil; time.Sleep(5 * time.Millisecond) {
		if conn, err = net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port)); err != nil && time.Now().After(deadline) {
			t.Fatalf("never listened: %v\n%s", err, errOut.String())
		}
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("PING")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "PING" {
		t.Fatalf("echo: %q %v\n%s", buf, err, errOut.String())
	}

	// Stopping closes open connections too.
	stop()
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, errOut.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connect didn't stop")
	}
	if _, err := conn.Read(buf); err == nil {
		t.Error("connection still open after stopping")
	}
	var got connectOutput
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil || got.Local != "127.0.0.1:"+strconv.Itoa(port) || got.Port != 6379 || got.Service != "shop/staging/cache" {
		t.Fatalf("output: %+v %v\n%s", got, err, out.String())
	}
}

func TestLocalURL(t *testing.T) {
	for raw, want := range map[string]string{
		"postgres://postgres:p%40ss@db:5432/app?sslmode=disable": "postgres://postgres:p%40ss@127.0.0.1:6543/app?sslmode=disable",
		"redis://default:pw@cache:6379":                          "redis://default:pw@127.0.0.1:6543",
	} {
		if got := localURL(raw, "127.0.0.1:6543"); got != want {
			t.Errorf("%s: %s, want %s", raw, got, want)
		}
	}
}
