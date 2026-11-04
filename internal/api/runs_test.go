package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/runframe"
)

// runRequest opens a streamed run of shop/staging/cache on a new
// connection and returns it with the response.
func (f *fixture) runRequest(query url.Values, token string) (net.Conn, *bufio.Reader, *http.Response) {
	f.t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(f.srv.URL, "http://"))
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	req := "GET " + shopStaging + "/services/cache/run?" + query.Encode() + " HTTP/1.1\r\nHost: dock\r\n" +
		"Connection: Upgrade\r\nUpgrade: " + apitypes.RunProtocol + "\r\nAuthorization: Bearer " + f.tokens[token] + "\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		f.t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	return conn, br, resp
}

// TestStreamRun: a streamed run passes input to the command and its
// output back as frames, then ends with the run; read tokens and locked
// environments are refused before the upgrade.
func TestStreamRun(t *testing.T) {
	f := newFixture(t)
	f.cacheService()
	q := url.Values{"command": {"cat"}, "stdin": {"true"}}

	_, _, resp := f.runRequest(q, "read")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read token: %d", resp.StatusCode)
	}

	conn, br, resp := f.runRequest(q, "deploy")
	_ = resp.Body.Close() // a 101 has none; the frames follow on conn
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade: %d", resp.StatusCode)
	}
	fw := runframe.NewWriter(conn)
	_ = fw.Frame(runframe.Stdin, []byte("hello"))
	_ = fw.Frame(runframe.CloseStdin, nil)
	fr := runframe.NewReader(br)
	var out strings.Builder
	var run apitypes.CommandRun
	for done := false; !done; {
		typ, p, err := fr.Next()
		if err != nil {
			t.Fatalf("after %q: %v", out.String(), err)
		}
		switch typ {
		case runframe.Stdout:
			out.Write(p)
		case runframe.Exit:
			if err := json.Unmarshal(p, &run); err != nil {
				t.Fatal(err)
			}
			done = true
		}
	}
	if out.String() != "hello" || run.Status != apitypes.RunSucceeded || run.Command != "cat" {
		t.Fatalf("output %q, run %+v", out.String(), run)
	}

	if _, err := f.core.LockEnvironment(context.Background(), sys, core.EnvRef{Project: "shop", Environment: "staging"}, apitypes.LockEnvironmentRequest{}); err != nil {
		t.Fatal(err)
	}
	_, _, resp = f.runRequest(q, "deploy")
	var p apitypes.Error
	_ = json.NewDecoder(resp.Body).Decode(&p)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusLocked || p.Code != apitypes.CodeEnvironmentLocked || p.Request == 0 {
		t.Fatalf("locked: %d %+v", resp.StatusCode, p)
	}
	// The request is the background run's: approving it runs the command.
	req, err := f.core.GetRequest(context.Background(), sys, p.Request)
	if err != nil || req.Operation != "run-command" {
		t.Fatalf("request = %+v, %v", req, err)
	}
}
