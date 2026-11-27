package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/runframe"
)

// TestExecSocket: the console gets a ticket with its session and opens
// the shell's WebSocket with it, from its own origin, once. A deploy
// token gets no ticket, and another origin no socket.
func TestExecSocket(t *testing.T) {
	f := newFixture(t)
	f.cacheService()
	cookie := f.login()
	path := shopStaging + "/services/cache/exec"
	ticket := func() string {
		t.Helper()
		resp, body := f.as(cookie, http.MethodPost, path+"-tickets", nil)
		var tk apitypes.ExecTicket
		if err := json.Unmarshal(body, &tk); resp.StatusCode != http.StatusOK || err != nil || tk.Ticket == "" {
			t.Fatalf("ticket: %d %s", resp.StatusCode, body)
		}
		return tk.Ticket
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dial := func(tk, origin string) (*websocket.Conn, *http.Response, error) {
		return websocket.Dial(ctx, "ws"+strings.TrimPrefix(f.srv.URL, "http")+path+"/ws?cols=80&rows=24", &websocket.DialOptions{
			Subprotocols: []string{apitypes.ExecProtocol, tk}, HTTPHeader: http.Header{"Origin": {origin}},
		})
	}

	tk := ticket()
	ws, _, err := dial(tk, f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	fw := runframe.NewWriter(conn)
	_ = fw.Frame(runframe.Stdin, []byte("hello"))
	_ = fw.Frame(runframe.CloseStdin, nil)
	fr := runframe.NewReader(conn)
	var out strings.Builder
	var end apitypes.ExecEnd
	for done := false; !done; {
		typ, p, err := fr.Next()
		if err != nil {
			t.Fatalf("after %q: %v", out.String(), err)
		}
		switch typ {
		case runframe.Stdout:
			out.Write(p)
		case runframe.Exit:
			if err := json.Unmarshal(p, &end); err != nil {
				t.Fatal(err)
			}
			done = true
		}
	}
	if out.String() != "hello" || end.ExitCode == nil || *end.ExitCode != 0 {
		t.Fatalf("output %q, end %+v", out.String(), end)
	}

	if _, resp, err := dial(tk, f.srv.URL); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a used ticket: %v %+v", err, resp)
	}
	if _, resp, err := dial(ticket(), "https://evil.example"); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("another origin: %v %+v", err, resp)
	}
	if resp, _ := f.do("deploy", http.MethodPost, path+"-tickets", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a deploy token gets a ticket: %d", resp.StatusCode)
	}
}
