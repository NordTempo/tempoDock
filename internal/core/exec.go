package core

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Shells in a service's running container (`dock exec`, ADR 0111). Unlike
// a run (ADR 0048) it is the container that serves traffic, so it takes
// admin on the environment (PermShell). A session is audited as
// service.exec when it opens, keeps nothing of what was typed or shown,
// and ends with its caller's access.

// actionShell is the check of every exec session.
const actionShell = "opening a shell"

// maxExecSeconds bounds one session.
const maxExecSeconds = apitypes.MaxRunTimeoutSeconds

// windowWait is how long shellWindow waits for an open window to
// close. A variable so a test can check again sooner than an hour.
var windowWait = time.Until

// defaultShell is what a session runs without a command: bash where the
// image has it, sh otherwise.
var defaultShell = []string{"/bin/sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash; exec sh"}

// Exec is a session checked and audited, ready to run once: Wait runs it.
// OpenExec makes one.
type Exec struct {
	c     *Core
	actor Actor
	ss    svcScope
	rel   store.Release
	cmd   []string
	once  sync.Once
	// release gives back what the session holds (its stream place, the
	// environment kept awake).
	release func()
}

// OpenExec checks that the actor may open a shell in the service (admin
// on its environment; a locked one refuses tokens), that the service has
// a running container, and audits the session. cmd is the command and its
// arguments, run as they are; empty runs a shell. tty is whether it gets
// a terminal, for the audit.
func (c *Core) OpenExec(ctx context.Context, a Actor, ref ServiceRef, cmd []string, tty bool) (*Exec, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeAdmin, actionShell)
	if err != nil {
		return nil, err
	}
	if len(cmd) == 0 {
		cmd = defaultShell
	} else if err := checkExecCommand(cmd); err != nil {
		return nil, err
	}
	if ss.svc.ArchivedAt != nil {
		return nil, invalid("service %s is archived; restore it to open a shell in it", ss.svc.Name)
	}
	// A request to use the environment: an asleep one wakes, and stays
	// awake while the session lasts.
	c.wakeFor(ctx, ss.envScope, a.Name)
	rel, err := c.execRelease(ctx, ss)
	if err != nil {
		return nil, err
	}
	closed, err := c.streams.open(a)
	if err != nil {
		return nil, err
	}
	endAwake := c.holdAwake(ss.env.ID)
	e := &Exec{c: c, actor: a, ss: ss, rel: rel, cmd: cmd, release: func() { closed(); endAwake() }}
	how := "a command"
	if tty {
		how = "a terminal"
	}
	c.audit(ctx, a, "service.exec", &ss.project, &ss.env, ss.String(),
		fmt.Sprintf("opened %s in %s's container %s (release %d): %s", how, ss.svc.Name, shortID(rel.ContainerID), rel.Number, c.shownCommand(rel, execLine(cmd), runAuditCommand)))
	return e, nil
}

// execRelease is the release whose container a session in ss runs in:
// the active one, with a container.
func (c *Core) execRelease(ctx context.Context, ss svcScope) (store.Release, error) {
	rel, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
	if store.IsNotFound(err) || err == nil && rel.ContainerID == "" {
		return rel, conflict("%s has no running container to open a shell in; deploy it first, or use `dock run`, which starts a new one", ss.svc.Name)
	}
	return rel, err
}

// checkExecCommand checks a command's arguments: UTF-8 without NUL bytes,
// at most MaxCommandBytes in all.
func checkExecCommand(cmd []string) error {
	n := 0
	for _, arg := range cmd {
		n += len(arg)
		if !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) {
			return invalid("the command must be UTF-8 text without NUL bytes")
		}
	}
	if n > apitypes.MaxCommandBytes {
		return invalid("the command is %d bytes; at most %d", n, apitypes.MaxCommandBytes)
	}
	if strings.TrimSpace(cmd[0]) == "" {
		return invalid("the command's first word is empty; give a program to run, e.g. `dock exec web -- ls`")
	}
	return nil
}

// execLine is cmd as one line for the audit: the default shell as
// "shell", otherwise the arguments joined.
func execLine(cmd []string) string {
	if len(cmd) == len(defaultShell) && strings.Join(cmd, "\x00") == strings.Join(defaultShell, "\x00") {
		return "shell"
	}
	return strings.Join(cmd, " ")
}

// shortID is a container ID as Docker shows it, 12 characters.
func shortID(id string) string { return id[:min(len(id), 12)] }

// Abandon gives up a session that never ran. It does nothing once Wait
// was called.
func (e *Exec) Abandon() { e.once.Do(e.release) }

// Wait runs the session connected to stream and blocks until it ended:
// the command exited, ctx ended (the caller went away), the actor's
// access changed, the control plane stopped or it ran past
// maxExecSeconds. Only the first call runs it.
func (e *Exec) Wait(ctx context.Context, stream *node.JobStream) apitypes.ExecEnd {
	end := apitypes.ExecEnd{Container: shortID(e.rel.ContainerID), Release: e.rel.Number, Error: "the session already ran"}
	e.once.Do(func() {
		defer e.release()
		end = e.wait(ctx, stream)
	})
	return end
}

func (e *Exec) wait(ctx context.Context, stream *node.JobStream) apitypes.ExecEnd {
	c, ss := e.c, e.ss
	end := apitypes.ExecEnd{Container: shortID(e.rel.ContainerID), Release: e.rel.Number}
	// It ends when the actor's access does, and when the environment is
	// locked against them meanwhile: the check reads its lock as it is now,
	// and follows its open window, if any, with a timer (shellWindow).
	win := c.newShellWindow(ctx, e.actor, ss.env.ID)
	defer win.stop()
	// One check at a time reads the environment and follows it, so the
	// timer is always the newest read's: checks overlap (the start, a lock
	// change, the timer's own).
	var reading sync.Mutex
	check := func(ctx context.Context, now Actor) error {
		if err := now.requireScope(apitypes.ScopeAdmin, actionShell); err != nil {
			return err
		}
		reading.Lock()
		env, err := c.store.GetEnvironment(ctx, ss.env.ID)
		if err == nil {
			win.follow(env)
		}
		reading.Unlock()
		if err != nil {
			return err
		}
		return now.checkEnv(apitypes.ScopeAdmin, actionShell, ss.project, env)
	}
	win.check = check
	ctx, unwatch := c.watched(win.ctx, e.actor, ss.env.ID, check)
	defer unwatch()
	// Once the watch is in place, with the actor read again: a lock
	// written or a token revoked since OpenExec reached no watch. This
	// check also starts the window's timer.
	cur, err := e.actor, error(nil)
	if cur.UserID != 0 || cur.TokenID != 0 { // the system's access doesn't end
		cur, err = c.actorNow(ctx, e.actor)
	}
	// A shell opened from a browser session (an exec ticket) needs that
	// session still signed in: a password reset or a sign-out ends it.
	if err == nil && e.actor.SessionID != 0 {
		if n, serr := c.store.SessionLive(ctx, store.SessionLiveParams{ID: e.actor.SessionID, Now: now()}); serr != nil || n == 0 {
			err = errors.New("the session ended")
		}
	}
	if err != nil {
		err = errf(apitypes.CodeForbidden, "your access changed before the session started; open it again")
	} else {
		err = check(ctx, cur)
	}
	// A deploy since OpenExec made another release active: its container
	// is the one that serves, and the old one is on its way out.
	if err == nil {
		if rel, rerr := c.execRelease(ctx, ss); rerr != nil {
			err = rerr
		} else if rel.ID != e.rel.ID {
			err = conflict("a deploy replaced %s's container since the session was opened; open it again", ss.svc.Name)
		}
	}
	if err != nil {
		end.Code, end.Error = CodeOf(err), err.Error()
		return end
	}
	// At most maxExecSeconds, and no longer than its token lasts: nothing
	// writes when a token expires, so no access change ends the session.
	// A failed read is doubt, which refuses, as actorNow's does.
	deadline, expires := now().Add(maxExecSeconds*time.Second), false
	if e.actor.TokenID != 0 {
		row, err := c.store.GetTokenByIDWithOwner(ctx, e.actor.TokenID)
		if err != nil {
			err = errf(apitypes.CodeForbidden, "your token couldn't be checked before the session started; open it again")
			end.Code, end.Error = CodeOf(err), err.Error()
			return end
		}
		if row.Token.ExpiresAt != nil && row.Token.ExpiresAt.Before(deadline) {
			deadline, expires = *row.Token.ExpiresAt, true
		}
	}
	sctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	if stream.Cut != nil {
		// Ended early: a write to a caller who stopped reading would hold
		// the session (on a remote server nothing else frees it). Stopped
		// before cancel runs, so a session that ended on its own isn't cut.
		defer context.AfterFunc(sctx, stream.Cut)()
	}

	code, err := caught(c.log, actionShell, func() (int, error) {
		rt, err := c.runtimeFor(ss.env.ServerID)
		if err != nil {
			return 0, err
		}
		return rt.Exec(sctx, node.ExecRequest{
			ContainerID: e.rel.ContainerID, Labels: c.labels(ss, e.rel.ID), Cmd: e.cmd, Session: rand.Text(), Stream: stream,
		})
	})
	switch {
	case err == nil:
		end.ExitCode = &code
		return end
	case errors.Is(context.Cause(ctx), ErrAccessChanged):
		err = errf(apitypes.CodeForbidden, "your access changed, or the environment was locked, while the session was open, so it was closed")
	case c.ctx.Err() != nil:
		err = errf(apitypes.CodeServerOffline, "the control plane stopped; open the session again once it is back")
	case ctx.Err() != nil:
		err = errors.New("the session's caller went away, so it was closed")
	case errors.Is(sctx.Err(), context.DeadlineExceeded) && expires:
		err = errf(apitypes.CodeForbidden, "your token expired while the session was open, so it was closed")
	case errors.Is(sctx.Err(), context.DeadlineExceeded):
		err = fmt.Errorf("the session was open for %s, the longest one may be, and was closed", maxExecSeconds*time.Second)
	case errors.Is(err, node.ErrNotRunning):
		err = conflict("%s isn't running (stopped, asleep or restarting); start it, or use `dock run`, which starts a new container", ss.svc.Name)
	case errors.Is(err, node.ErrNotFound):
		err = conflict("%s's container is gone (a deploy may have replaced it); open the session again", ss.svc.Name)
	default:
		err = c.nodeErr(ss.env.ServerID, err)
	}
	if code := CodeOf(err); code != apitypes.CodeInternal {
		end.Code = code
	}
	end.Error = err.Error()
	return end
}

// shellWindow ends a session (ctx, with ErrAccessChanged) when the open
// window of its locked environment (ADR 0044: opened for a while) closes
// and the session's check refuses its actor then. Such a window ends with
// no write, so no lock change asks what is held open: a timer per
// session, armed only while a window is open, fires at its end. Every
// check (at the start and on each lock change) passes the environment as
// it read it to follow, so a window opened, extended or shortened while
// the session runs moves the timer too.
type shellWindow struct {
	c      *Core
	a      Actor
	envID  int64
	ctx    context.Context
	cancel context.CancelCauseFunc
	// check is the session's; set before the first follow.
	check func(context.Context, Actor) error
	mu    sync.Mutex
	timer *time.Timer
}

func (c *Core) newShellWindow(ctx context.Context, a Actor, envID int64) *shellWindow {
	w := &shellWindow{c: c, a: a, envID: envID}
	w.ctx, w.cancel = context.WithCancelCause(ctx)
	return w
}

// follow arms the timer for env's open window, or stops it without one.
func (w *shellWindow) follow(env store.Environment) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	if w.ctx.Err() != nil || env.LockedAt == nil || env.OpenUntil == nil || !now().Before(*env.OpenUntil) {
		return
	}
	w.timer = w.c.afterCaught(windowWait(*env.OpenUntil), "checking a shell when its environment's open window closes", func() {
		// The check reads the environment and follows it (a window
		// extended meanwhile arms the timer again); a refusal or a
		// failed read ends the session.
		if err := w.check(w.ctx, w.a); err != nil {
			w.cancel(ErrAccessChanged)
		}
	})
}

// stop ends the timer with the session.
func (w *shellWindow) stop() {
	w.mu.Lock()
	if w.timer != nil {
		w.timer.Stop()
	}
	w.mu.Unlock()
	w.cancel(nil)
}
