package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A guesser hammering one account from their address is refused, but the
// account's owner still signs in from theirs.
func TestLoginGuesserCantLockOutTheOwner(t *testing.T) {
	h := newHarness(t)
	h.user("ada@example.com", apitypes.ScopeAdmin)
	var err error
	for range 50 {
		_, err = h.c.Login(h.ctx, "ada@example.com", "wrong guess", "198.51.100.7")
	}
	if CodeOf(err) != apitypes.CodeRateLimited {
		t.Fatalf("guesser: %v", err)
	}
	if _, err := h.c.Login(h.ctx, "ADA@example.com", "password-123456", "192.0.2.1"); err != nil {
		t.Fatalf("owner from their own address: %v", err)
	}
}

// An IPv6 site (a /48) is one client to the sign-in limits, whichever of
// its /64s an attempt comes from; the next site is another.
func TestLoginLimitCountsAnIPv6SiteAsOneClient(t *testing.T) {
	h := newHarness(t)
	t0 := now()
	admitted := 0
	for subnet := range 256 {
		// A different unknown address each time: only the client's
		// bucket can fill.
		if h.c.loginLimit.Allow(fmt.Sprintf("nobody-%d@example.com", subnet), fmt.Sprintf("2001:db8:ab:%x::1", subnet), t0) {
			admitted++
		}
	}
	if admitted != 20 {
		t.Fatalf("one /48 had %d attempts admitted at once, want the client's 20", admitted)
	}
	if !h.c.loginLimit.Allow("someone@example.com", "2001:db8:ac::1", t0) {
		t.Fatal("the next /48 was limited too")
	}
}

// Password checks wait for a slot behind at most maxBcryptWaiting
// others. Past that a sign-in is turned away at once, to try again, and
// a waiter whose request ends leaves the line.
func TestPasswordChecksWaitInABoundedLine(t *testing.T) {
	h := newHarness(t)
	h.user("ada@example.com", apitypes.ScopeAdmin)
	for range cap(h.c.bcryptSem) { // every slot busy
		h.c.bcryptSem <- struct{}{}
	}
	ctx, cancel := context.WithCancel(h.ctx)
	left := make(chan error, maxBcryptWaiting)
	for range maxBcryptWaiting {
		go func() {
			_, err := h.c.checkPassword(ctx, "ada@example.com", "password-123456")
			left <- err
		}()
	}
	for deadline := time.Now().Add(10 * time.Second); h.c.bcryptWaiting.Load() < maxBcryptWaiting; {
		if time.Now().After(deadline) {
			t.Fatalf("%d waiting, want %d", h.c.bcryptWaiting.Load(), maxBcryptWaiting)
		}
		time.Sleep(time.Millisecond)
	}

	// The line is full: the next is refused without waiting, though its
	// own context would let it wait.
	late, stop := context.WithTimeout(h.ctx, 2*time.Second)
	defer stop()
	if _, err := h.c.Login(late, "ada@example.com", "password-123456", "192.0.2.1"); CodeOf(err) != apitypes.CodeRateLimited {
		t.Fatalf("a sign-in past a full line: %v", err)
	}
	if _, err := h.c.hashPassword(late, "a-new-password-123"); CodeOf(err) != apitypes.CodeRateLimited {
		t.Fatalf("a new password past a full line: %v", err)
	}

	cancel()
	for range maxBcryptWaiting {
		if err := <-left; !errors.Is(err, context.Canceled) {
			t.Fatalf("a waiter whose request ended: %v", err)
		}
	}
	if n := h.c.bcryptWaiting.Load(); n != 0 {
		t.Fatalf("%d still counted as waiting", n)
	}
	for range cap(h.c.bcryptSem) {
		<-h.c.bcryptSem
	}
	if _, err := h.c.Login(h.ctx, "ada@example.com", "password-123456", "192.0.2.1"); err != nil {
		t.Fatalf("a sign-in once the line cleared: %v", err)
	}
}
