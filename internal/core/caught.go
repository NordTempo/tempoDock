package core

import (
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"
)

// caught runs fn and turns a panic in it into an error: work a background
// worker does on what a repository or a tenant sent (a pushed dock.toml, a
// source archive) fails as that piece of work, never as the daemon, which
// has no one above a worker's goroutine to recover it. The panic and its
// stack are logged once; the error's text holds no internals. what names
// the work, as in "deploying a push".
func caught[T any](log *slog.Logger, what string, fn func() (T, error)) (out T, err error) {
	defer func() {
		if p := recover(); p != nil {
			log.Error("panic in a background worker", "work", what, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			var zero T
			out, err = zero, fmt.Errorf("%s failed on a bug in Dock", what)
		}
	}()
	return fn()
}

// recovered is deferred where a background goroutine does one piece of
// work with nothing above it to recover: `defer recovered(log, what)`. A
// panic ends that piece of work only and is logged once with its stack,
// like caught's. Deferring it allocates nothing, so it suits work done per
// event; a named result set before the work is what the caller gets after
// a panic.
func recovered(log *slog.Logger, what string) {
	if p := recover(); p != nil {
		log.Error("panic in a background worker", "work", what, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
	}
}

// goCaught starts fn in a goroutine that recovers a panic (recovered), so
// one in work on what a tenant or a repository sent ends that work and not
// the daemon. Every `go` statement in core is this one, goLoop's, or on
// goAllowed's list with its reason (TestEveryGoroutineRecovers). fn's own
// defers (c.wg.Done, releasing a claim) run as usual. A panic ends the
// goroutine: a loop that must outlive one recovers per item inside
// (safely, caught, recovered) or starts with goLoop.
func (c *Core) goCaught(what string, fn func()) {
	go func() {
		defer recovered(c.log, what)
		fn()
	}()
}

// safely runs one piece of a loop's work (a pass, a tick, one delivery)
// and recovers a panic in it, so the loop goes on with the next. Not for
// work done per log line or per event: a function that defers recovered
// needs no closure there.
func (c *Core) safely(what string, fn func()) {
	defer recovered(c.log, what)
	fn()
}

// Waits before goLoop starts a loop again after a panic: doubling from
// the first to the last, and back to the first once a run lasted longer
// than the last. Variables for tests.
var (
	loopRestartMin = time.Second
	loopRestartMax = time.Minute
)

// goLoop starts a long-lived loop that can't recover per item, and starts
// it again when a panic ends it, so a panic is not a quiet end of what the
// loop does. loop is counted in c.wg by the caller and calls c.wg.Done
// itself when it ends, as core's loops do; a normal return ends goLoop
// too. The only timer is the wait before a restart (loopRestartMin to
// loopRestartMax, docs/performance.md): none runs while the loop does, and
// a loop that panics at once restarts at most once a minute.
func (c *Core) goLoop(what string, loop func()) {
	// goLoop's own count: a restart's Add then never races Close's Wait.
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		wait := loopRestartMin
		for {
			started := time.Now()
			if c.ranLoop(what, loop) {
				return
			}
			if time.Since(started) > loopRestartMax {
				wait = loopRestartMin
			}
			t := time.NewTimer(wait)
			select {
			case <-c.ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
			wait = min(wait*2, loopRestartMax)
			c.wg.Add(1) // the restarted loop's Done
		}
	}()
}

// ranLoop runs loop and reports whether it returned; false after a panic.
func (c *Core) ranLoop(what string, loop func()) (ended bool) {
	defer recovered(c.log, what)
	loop()
	return true
}
