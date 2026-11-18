package core

import (
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
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

// caughtErr is caught for work whose only result is its error.
func caughtErr(log *slog.Logger, what string, fn func() error) error {
	_, err := caught(log, what, func() (struct{}, error) { return struct{}{}, fn() })
	return err
}

// goCaught starts fn in a goroutine that recovers a panic (recovered), so
// one in work on what a tenant or a repository sent ends that work and not
// the daemon. Every goroutine core starts (a `go` statement, a WaitGroup's
// or errgroup's Go, an AfterFunc) starts through this file's helpers or is
// on goAllowed's list with its reason (TestEveryGoroutineRecovers). fn's
// own defers (c.wg.Done, releasing a claim) run as usual. A panic ends the
// goroutine: a loop that must outlive one recovers per item inside
// (safely, caught, recovered) or starts with goLoop.
//
// A recovered worker returns nothing: what it was to leave behind (a flag
// cleared, a claim released, a result or an error in its slot) is set in a
// defer, or the work runs through caughtErr so its panic is the error the
// rest of the worker records.
func (c *Core) goCaught(what string, fn func()) {
	go func() {
		defer recovered(c.log, what)
		fn()
	}()
}

// goCounted is goCaught for a goroutine Close waits for: c.wg.Go with the
// recovery.
func (c *Core) goCounted(what string, fn func()) {
	goIn(&c.wg, c.log, what, fn)
}

// goIn starts fn in wg (wg.Go) and recovers a panic in it, for workers a
// caller starts side by side and waits for. A worker that panicked wrote
// no result: the caller's slot for it holds what it held before, so use
// goErr where a missing result must not read as success.
func goIn(wg *sync.WaitGroup, log *slog.Logger, what string, fn func()) {
	wg.Go(func() {
		defer recovered(log, what)
		fn()
	})
}

// goErr starts fn in wg and stores its error in *err, a panic as an error
// too (caught): for workers side by side whose errors the caller joins,
// where a worker that stopped half-way must fail the whole.
func goErr(wg *sync.WaitGroup, log *slog.Logger, what string, err *error, fn func() error) {
	wg.Go(func() { *err = caughtErr(log, what, fn) })
}

// goGroup starts fn in an errgroup; a panic in it is the error the group
// returns (caught), so the group's context ends as for any failed member.
func goGroup(g *errgroup.Group, log *slog.Logger, what string, fn func() error) {
	g.Go(func() error { return caughtErr(log, what, fn) })
}

// afterCaught is time.AfterFunc with the recovery: the timer's goroutine
// runs fn through safely.
func (c *Core) afterCaught(d time.Duration, what string, fn func()) *time.Timer {
	return time.AfterFunc(d, func() { c.safely(what, fn) })
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
