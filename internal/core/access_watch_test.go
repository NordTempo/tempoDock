package core

import (
	"context"
	"errors"
	"go/ast"
	"go/types"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// followAs opens a's event stream and returns how it ends, once it is live.
func (h *harness) followAs(a Actor) <-chan error {
	h.t.Helper()
	stream, err := h.c.Events(h.ctx, a, EventFilter{})
	if err != nil {
		h.t.Fatal(err)
	}
	ready, ended := make(chan struct{}), make(chan error, 1)
	go func() {
		ended <- stream(h.ctx, func() { close(ready) }, func(...LiveEvent) error { return nil })
	}()
	<-ready
	return ended
}

// endsWithAccess fails unless the stream ended because access changed.
func endsWithAccess(t *testing.T, what string, ended <-chan error) {
	t.Helper()
	select {
	case err := <-ended:
		if !errors.Is(err, ErrAccessChanged) {
			t.Fatalf("%s: the stream ended with %v", what, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: the stream is still open", what)
	}
}

// staysOpen fails if the stream has ended.
func staysOpen(t *testing.T, what string, ended <-chan error) {
	t.Helper()
	select {
	case err := <-ended:
		t.Fatalf("%s: the stream ended: %v", what, err)
	default:
	}
}

// What someone follows ends when their access does: a disabled account,
// a revoked token, a member removed from the org. Other people's and
// other tokens' streams stay open.
func TestOpenStreamsEndWithAccess(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	vi := h.cliUser("vi@example.com", apitypes.ScopeAdmin)
	login, err := h.c.AuthenticateToken(h.ctx, h.cliToken(vi), "")
	if err != nil {
		t.Fatal(err)
	}
	ola := h.cliUser("ola@example.com", apitypes.ScopeRead)
	ci := h.token(testSystem, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeRead})
	other := h.token(testSystem, apitypes.CreateTokenRequest{Name: "other", Scope: apitypes.ScopeRead})
	viSession, viLogin, olaSession, ciStream, otherStream := h.followAs(vi), h.followAs(login), h.followAs(ola), h.followAs(ci), h.followAs(other)

	if _, err := h.c.SetUserDisabled(h.ctx, System, vi.UserID, true); err != nil {
		t.Fatal(err)
	}
	endsWithAccess(t, "a disabled account's session", viSession)
	endsWithAccess(t, "a disabled account's dock login token", viLogin)
	staysOpen(t, "someone else", olaSession)

	if err := h.c.RevokeToken(h.ctx, testSystem, ci.TokenID); err != nil {
		t.Fatal(err)
	}
	endsWithAccess(t, "a revoked token", ciStream)
	staysOpen(t, "another token", otherStream)

	if err := h.c.RemoveMember(h.ctx, testSystem, ola.UserID); err != nil {
		t.Fatal(err)
	}
	endsWithAccess(t, "a removed member", olaSession)

	// A change to the org's groups, grants or roles ends what its people
	// follow: opening it again shows what they may see now.
	mia := h.followAs(h.cliUser("mia@example.com", apitypes.ScopeRead))
	h.c.accessChanged(accessChange{orgID: other.OrgID})
	endsWithAccess(t, "a member after the org's access changed", mia)
}

// An attached run and a tunnel end with their actor's access too.
func TestRunsAndTunnelsEndWithAccess(t *testing.T) {
	h, _, web, _ := connectFixture(t)
	h.user("owner@x", apitypes.ScopeAdmin)
	admin := h.user("admin@x", apitypes.ScopeAdmin)
	tun, err := h.c.OpenTunnel(h.ctx, admin, web, TunnelRequest{From: "x"})
	if err != nil {
		t.Fatal(err)
	}
	client, server := tcpPair(t)
	piped := make(chan struct{})
	go func() { tun.Pipe(server, nil); close(piped) }()

	started := make(chan struct{})
	h.rt.OnJobContext(func(ctx context.Context, _ node.JobSpec) (node.JobResult, error) {
		close(started)
		<-ctx.Done()
		return node.JobResult{}, ctx.Err()
	})
	r, err := h.c.OpenRun(h.ctx, admin, web, apitypes.RunCommandRequest{Command: "sleep 600"}, false)
	if err != nil {
		t.Fatal(err)
	}
	ran := make(chan apitypes.CommandRun, 1)
	go func() { ran <- r.Wait(h.ctx, &node.JobStream{Stdout: &strings.Builder{}}) }()
	<-started

	// The org's access changed and theirs still allows both: they stay.
	h.c.accessChanged(accessChange{orgID: admin.OrgID})
	eventually(t, "the run and the tunnel were checked again", func() bool { return h.c.access.asked.Load() == 1 })
	select {
	case <-piped:
		t.Fatal("the tunnel closed though its actor may still hold it")
	case run := <-ran:
		t.Fatalf("the run ended (%s) though its actor may still run it", run.Status)
	default:
	}
	// It changed so that they may no longer: both end.
	if _, err := h.c.SetMemberRole(h.ctx, testSystem, admin.UserID, apitypes.SetMemberRoleRequest{Role: apitypes.ScopeRead}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-piped:
	case <-time.After(10 * time.Second):
		t.Fatal("the tunnel is still open")
	}
	_ = client.Close()
	select {
	case run := <-ran:
		if run.Status != apitypes.RunCanceled {
			t.Fatalf("the run ended %s", run.Status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the run still runs")
	}
}

// A log follow takes one of its actor's places among live streams, as an
// event stream does.
func TestLogFollowsAreBounded(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	tok := h.token(testSystem, apitypes.CreateTokenRequest{Name: "watcher", Scope: apitypes.ScopeRead})
	for range maxActorStreams {
		if _, err := h.c.Events(h.ctx, tok, EventFilter{}); err != nil {
			t.Fatal(err)
		}
	}
	logs, err := h.c.ServiceLogs(h.ctx, tok, web, LogQuery{Follow: true})
	if err != nil {
		t.Fatal(err)
	}
	err = logs(h.ctx, func() { t.Error("a follow past the bound went live") }, func(...apitypes.LogLine) error { return nil })
	if CodeOf(err) != apitypes.CodeRateLimited {
		t.Fatalf("one follow more than the bound: %v", err)
	}
}

// Every operation that returns a Stream returns it through held, so it
// ends when its actor's access does; and every write that ends a user's
// sessions says so to what they hold open.
func TestEveryStreamIsHeld(t *testing.T) {
	_, funcs := coreFuncs(t)
	calls := func(fd *ast.FuncDecl, name string) bool {
		found := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				switch fn := call.Fun.(type) {
				case *ast.Ident:
					found = found || fn.Name == name
				case *ast.SelectorExpr:
					found = found || fn.Sel.Name == name
				}
			}
			return !found
		})
		return found
	}
	streams := 0
	for _, fd := range funcs {
		name := fd.Name.Name
		if fd.Type.Results != nil {
			for _, res := range fd.Type.Results.List {
				if strings.HasPrefix(types.ExprString(res.Type), "Stream[") && name != "held" && name != "counted" {
					streams++
					if !calls(fd, "held") {
						t.Errorf("%s returns a Stream that isn't held: return it through held(c, a, ...), so it ends when its actor's access does", name)
					}
				}
			}
		}
		if name != "endUserAccess" && calls(fd, "endUserAccess") && !calls(fd, "accessChanged") {
			t.Errorf("%s ends a user's access without c.accessChanged: what they hold open would stay open", name)
		}
	}
	if streams < 7 {
		t.Fatalf("found %d operations returning a Stream; the scan no longer reads them", streams)
	}
}
