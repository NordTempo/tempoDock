package core

import (
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// execAs opens a session in ref as a and waits for its end, its output
// in out.
func (h *harness) execAs(a Actor, ref ServiceRef, cmd ...string) (apitypes.ExecEnd, string, error) {
	h.t.Helper()
	ex, err := h.c.OpenExec(h.ctx, a, ref, cmd, false)
	if err != nil {
		return apitypes.ExecEnd{}, "", err
	}
	var out strings.Builder
	return ex.Wait(h.ctx, &node.JobStream{Stdout: &out}), out.String(), nil
}

// A session runs in the active release's container, as it is, with its
// exit code; without a command it is a shell. Each is audited when it
// opens.
func TestExec(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	if _, _, err := h.execAs(testSystem, web, "ls"); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("without a release: %v", err)
	}
	h.deploy(web, "app:1")
	end, out, err := h.execAs(testSystem, web, "ls", "-la", "/app")
	if err != nil || end.ExitCode == nil || *end.ExitCode != 0 || out != "ls -la /app\n" || end.Release != 1 || end.Container == "" {
		t.Fatalf("exec = %+v %q, %v", end, out, err)
	}
	if end, _, err := h.execAs(testSystem, web, "exit", "3"); err != nil || end.ExitCode == nil || *end.ExitCode != 3 {
		t.Fatalf("exit 3 = %+v, %v", end, err)
	}
	if _, out, _ := h.execAs(testSystem, web); !strings.Contains(out, "exec bash; exec sh") {
		t.Fatalf("without a command it runs %q, not a shell", out)
	}
	evs, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: "service.exec"})
	if len(evs) != 3 || !strings.Contains(evs[0].Summary, "a command in web's container") || !strings.HasSuffix(evs[0].Summary, ": shell") ||
		!strings.HasSuffix(evs[2].Summary, "(release 1): ls -la /app") {
		t.Fatalf("audit = %+v", evs)
	}

	if _, _, err := h.execAs(testSystem, web, "", "x"); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("an empty program: %v", err)
	}
	ctrs, _ := h.rt.List(h.ctx)
	for _, c := range ctrs {
		if err := h.rt.Stop(h.ctx, c.ID, 0); err != nil {
			t.Fatal(err)
		}
	}
	if end, _, err := h.execAs(testSystem, web, "ls"); err != nil || end.ExitCode != nil || end.Code != apitypes.CodeConflict || !strings.Contains(end.Error, "isn't running") {
		t.Fatalf("a stopped container: %+v, %v", end, err)
	}
}

// A shell takes admin on the environment (PermShell): deploy isn't
// enough. A locked environment refuses tokens and files no request.
func TestExecPermissions(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	deployer := h.token(testSystem, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeDeploy})
	admin := h.token(testSystem, apitypes.CreateTokenRequest{Name: "ops", Scope: apitypes.ScopeAdmin})
	if _, _, err := h.execAs(deployer, web, "ls"); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a deploy token: %v", err)
	}
	if end, _, err := h.execAs(admin, web, "ls"); err != nil || end.ExitCode == nil {
		t.Fatalf("an admin token: %+v, %v", end, err)
	}
	h.lock("production")
	if _, _, err := h.execAs(admin, web, "ls"); CodeOf(err) != apitypes.CodeEnvironmentLocked {
		t.Fatalf("a token in a locked environment: %v", err)
	}
	if reqs, err := h.c.ListRequests(h.ctx, testSystem, RequestFilter{}); err != nil || len(reqs) != 0 {
		t.Fatalf("requests = %+v, %v", reqs, err)
	}
}

// An open session ends when its actor's access does.
func TestExecEndsWithAccess(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	// Revoked between opening and running: it never runs.
	gone := h.token(testSystem, apitypes.CreateTokenRequest{Name: "gone", Scope: apitypes.ScopeAdmin})
	early, err := h.c.OpenExec(h.ctx, gone, web, []string{"ls"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.c.RevokeToken(h.ctx, testSystem, gone.TokenID); err != nil {
		t.Fatal(err)
	}
	if end := early.Wait(h.ctx, &node.JobStream{Stdout: io.Discard}); end.ExitCode != nil || end.Code != apitypes.CodeForbidden {
		t.Fatalf("revoked before it ran: %+v", end)
	}

	ops := h.token(testSystem, apitypes.CreateTokenRequest{Name: "ops", Scope: apitypes.ScopeAdmin})
	ex, err := h.c.OpenExec(h.ctx, ops, web, []string{"sh"}, true)
	if err != nil {
		t.Fatal(err)
	}
	in, _ := io.Pipe() // never closed: the shell waits for input
	ended := make(chan apitypes.ExecEnd, 1)
	go func() { ended <- ex.Wait(h.ctx, &node.JobStream{Stdout: io.Discard, Stdin: in, TTY: true}) }()
	eventually(t, "the session runs", func() bool {
		return slices.ContainsFunc(h.rt.Ops(), func(op string) bool { return strings.HasPrefix(op, "exec ") })
	})
	if err := h.c.RevokeToken(h.ctx, testSystem, ops.TokenID); err != nil {
		t.Fatal(err)
	}
	select {
	case end := <-ended:
		if end.ExitCode != nil || end.Code != apitypes.CodeForbidden {
			t.Fatalf("ended %+v", end)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the session is still open")
	}
}

// Locking the environment ends a token's open session; the audit never
// quotes a secret value typed into a command.
func TestExecLockAndMask(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	if _, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"API_KEY": "sk-live-0123456789"}}); err != nil {
		t.Fatal(err)
	}
	h.deploy(web, "app:1")
	if _, _, err := h.execAs(testSystem, web, "curl", "-H", "Authorization: sk-live-0123456789", "x"); err != nil {
		t.Fatal(err)
	}
	evs, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: "service.exec"})
	if len(evs) != 1 || strings.Contains(evs[0].Summary, "sk-live") || !strings.Contains(evs[0].Summary, "curl -H Authorization:") {
		t.Fatalf("audit = %+v", evs)
	}

	// Locked between opening and running: it never runs.
	ops := h.token(testSystem, apitypes.CreateTokenRequest{Name: "ops", Scope: apitypes.ScopeAdmin})
	early, err := h.c.OpenExec(h.ctx, ops, web, []string{"ls"}, false)
	if err != nil {
		t.Fatal(err)
	}
	h.lock("production")
	if end := early.Wait(h.ctx, &node.JobStream{Stdout: io.Discard}); end.ExitCode != nil || end.Code != apitypes.CodeEnvironmentLocked {
		t.Fatalf("locked before it ran: %+v", end)
	}
	if _, err := h.c.UnlockEnvironment(h.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.UnlockEnvironmentRequest{}); err != nil {
		t.Fatal(err)
	}

	// Locked while it runs: it ends.
	ex, err := h.c.OpenExec(h.ctx, ops, web, []string{"sh"}, true)
	if err != nil {
		t.Fatal(err)
	}
	in, _ := io.Pipe()
	ended := make(chan apitypes.ExecEnd, 1)
	go func() { ended <- ex.Wait(h.ctx, &node.JobStream{Stdout: io.Discard, Stdin: in, TTY: true}) }()
	eventually(t, "the session runs", func() bool {
		return len(slices.DeleteFunc(h.rt.Ops(), func(op string) bool { return !strings.HasPrefix(op, "exec ") })) == 2
	})
	h.lock("production")
	select {
	case end := <-ended:
		if end.ExitCode != nil || end.Code != apitypes.CodeForbidden {
			t.Fatalf("ended %+v", end)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the session outlived the lock")
	}
}

// A token's shell opened while a locked environment is open for a while
// ends when that window closes: nothing writes then, the timer finds it.
func TestExecEndsWithTheOpenWindow(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	h.lock("production")
	ss, err := h.c.loadService(h.ctx, 1, web)
	if err != nil {
		t.Fatal(err)
	}
	setWindow := func(until time.Time) {
		t.Helper()
		if _, err := h.c.store.SetEnvironmentLock(h.ctx, store.SetEnvironmentLockParams{ID: ss.env.ID, LockedAt: ss.env.LockedAt, LockedBy: ss.env.LockedBy, OpenUntil: &until}); err != nil {
			t.Fatal(err)
		}
	}
	// Checked again every few milliseconds instead of at the window's end.
	defer func(w func(time.Time) time.Duration) { windowWait = w }(windowWait)
	windowWait = func(time.Time) time.Duration { return 5 * time.Millisecond }
	setWindow(now().Add(time.Hour))
	ops := h.token(testSystem, apitypes.CreateTokenRequest{Name: "ops", Scope: apitypes.ScopeAdmin})
	ex, err := h.c.OpenExec(h.ctx, ops, web, []string{"sh"}, true)
	if err != nil {
		t.Fatalf("a token in the open window: %v", err)
	}
	in, _ := io.Pipe()
	ended := make(chan apitypes.ExecEnd, 1)
	go func() { ended <- ex.Wait(h.ctx, &node.JobStream{Stdout: io.Discard, Stdin: in, TTY: true}) }()
	eventually(t, "the session runs", func() bool {
		return slices.ContainsFunc(h.rt.Ops(), func(op string) bool { return strings.HasPrefix(op, "exec ") })
	})
	select {
	case end := <-ended:
		t.Fatalf("ended in the open window: %+v", end)
	case <-time.After(50 * time.Millisecond):
	}
	setWindow(now().Add(-time.Second)) // closed, with no lock change
	select {
	case end := <-ended:
		if end.ExitCode != nil || end.Code != apitypes.CodeForbidden {
			t.Fatalf("ended %+v", end)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the session outlived the open window")
	}
}

// Shortening an open window while a token's shell runs moves its end:
// the lock change asks the session again, which follows the new window.
func TestExecFollowsAShortenedWindow(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	h.lock("production")
	ss, err := h.c.loadService(h.ctx, 1, web)
	if err != nil {
		t.Fatal(err)
	}
	setWindow := func(until time.Time) {
		t.Helper()
		params := store.SetEnvironmentLockParams{ID: ss.env.ID, LockedAt: ss.env.LockedAt, LockedBy: ss.env.LockedBy, OpenUntil: &until}
		if _, err := h.c.setLock(h.ctx, testSystem, ss.envScope, params, "environment.open", "opened"); err != nil {
			t.Fatal(err)
		}
	}
	setWindow(now().Add(time.Hour))
	ops := h.token(testSystem, apitypes.CreateTokenRequest{Name: "ops", Scope: apitypes.ScopeAdmin})
	ex, err := h.c.OpenExec(h.ctx, ops, web, []string{"sh"}, true)
	if err != nil {
		t.Fatal(err)
	}
	in, _ := io.Pipe()
	ended := make(chan apitypes.ExecEnd, 1)
	go func() { ended <- ex.Wait(h.ctx, &node.JobStream{Stdout: io.Discard, Stdin: in, TTY: true}) }()
	eventually(t, "the session runs", func() bool {
		return slices.ContainsFunc(h.rt.Ops(), func(op string) bool { return strings.HasPrefix(op, "exec ") })
	})
	setWindow(now().Add(200 * time.Millisecond)) // an hour became a moment
	select {
	case end := <-ended:
		if end.ExitCode != nil || end.Code != apitypes.CodeForbidden {
			t.Fatalf("ended %+v", end)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the session outlived its shortened window")
	}
}

// A session whose caller stopped reading ends with its access all the
// same: Cut frees the write that holds it.
func TestExecCutsAStalledCaller(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	ops := h.token(testSystem, apitypes.CreateTokenRequest{Name: "ops", Scope: apitypes.ScopeAdmin})
	ex, err := h.c.OpenExec(h.ctx, ops, web, []string{"cat"}, false)
	if err != nil {
		t.Fatal(err)
	}
	stalled := &stalledWriter{cut: make(chan struct{}), writing: make(chan struct{})}
	in, inW := io.Pipe()
	defer func() { _ = inW.Close() }()
	ended := make(chan apitypes.ExecEnd, 1)
	go func() {
		ended <- ex.Wait(h.ctx, &node.JobStream{Stdout: stalled, Stdin: in, Cut: stalled.Cut})
	}()
	go func() { _, _ = inW.Write([]byte("output nobody reads")) }()
	<-stalled.writing
	if err := h.c.RevokeToken(h.ctx, testSystem, ops.TokenID); err != nil {
		t.Fatal(err)
	}
	select {
	case end := <-ended:
		if end.ExitCode != nil || end.Code != apitypes.CodeForbidden {
			t.Fatalf("ended %+v", end)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the session is held by a caller who stopped reading")
	}
}

// stalledWriter is a caller who stopped reading: a write blocks until
// Cut.
type stalledWriter struct {
	once    sync.Once
	writing chan struct{}
	cut     chan struct{}
	cutOnce sync.Once
}

func (s *stalledWriter) Write([]byte) (int, error) {
	s.once.Do(func() { close(s.writing) })
	<-s.cut
	return 0, io.ErrClosedPipe
}

func (s *stalledWriter) Cut() { s.cutOnce.Do(func() { close(s.cut) }) }

// A deploy between opening a session and starting it is a conflict: the
// session would start in the container on its way out.
func TestExecRefusesAReplacedContainer(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	ex, err := h.c.OpenExec(h.ctx, testSystem, web, []string{"ls"}, false)
	if err != nil {
		t.Fatal(err)
	}
	h.deploy(web, "app:2")
	if end := ex.Wait(h.ctx, &node.JobStream{Stdout: io.Discard}); end.ExitCode != nil || end.Code != apitypes.CodeConflict || !strings.Contains(end.Error, "a deploy replaced") {
		t.Fatalf("after a deploy: %+v", end)
	}
}

// A token that expires while its shell is open ends it then: nothing
// writes at the expiry, so the session's deadline is it.
func TestExecEndsWhenItsTokenExpires(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	ops := h.token(testSystem, apitypes.CreateTokenRequest{Name: "ops", Scope: apitypes.ScopeAdmin})
	ex, err := h.c.OpenExec(h.ctx, ops, web, []string{"sh"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB.ExecContext(h.ctx, "UPDATE tokens SET expires_at = ? WHERE id = ?", now().Add(2*time.Second), ops.TokenID); err != nil {
		t.Fatal(err)
	}
	in, _ := io.Pipe()
	ended := make(chan apitypes.ExecEnd, 1)
	go func() { ended <- ex.Wait(h.ctx, &node.JobStream{Stdout: io.Discard, Stdin: in, TTY: true}) }()
	select {
	case end := <-ended:
		// The deadline is read once, at the start; a runner too slow to
		// start within the 2 s refuses the expired token there instead.
		// Either way the session doesn't outlive its token.
		if end.ExitCode != nil || end.Code != apitypes.CodeForbidden ||
			!strings.Contains(end.Error, "token expired") && !strings.Contains(end.Error, "before the session started") {
			t.Fatalf("ended %+v", end)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the session outlived its token")
	}
}

// A ticket opens a shell once, in the service it was made for, as the
// person it was made for; making one checks what the session will.
func TestExecTicket(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	api := h.service("api", apitypes.CreateServiceRequest{Image: "app:1"})
	if _, err := h.c.ExecTicket(h.ctx, testSystem, web); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("a ticket without a container: %v", err)
	}
	h.deploy(web, "app:1")
	deployer := h.token(testSystem, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeDeploy})
	if _, err := h.c.ExecTicket(h.ctx, deployer, web); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a deploy token's ticket: %v", err)
	}
	tk, err := h.c.ExecTicket(h.ctx, testSystem, web)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.RedeemExecTicket(h.ctx, tk.Ticket, api); CodeOf(err) != apitypes.CodeUnauthorized {
		t.Fatalf("another service: %v", err)
	}
	tk, _ = h.c.ExecTicket(h.ctx, testSystem, web)
	if a, err := h.c.RedeemExecTicket(h.ctx, tk.Ticket, web); err != nil || a.Name != testSystem.Name {
		t.Fatalf("redeemed as %+v, %v", a, err)
	}
	if _, err := h.c.RedeemExecTicket(h.ctx, tk.Ticket, web); CodeOf(err) != apitypes.CodeUnauthorized {
		t.Fatalf("a used ticket: %v", err)
	}
	// A deploy since the ticket makes it stale: the console showed the
	// release it named.
	tk, _ = h.c.ExecTicket(h.ctx, testSystem, web)
	h.deploy(web, "app:2")
	if _, err := h.c.RedeemExecTicket(h.ctx, tk.Ticket, web); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("a ticket from before a deploy: %v", err)
	}
}
