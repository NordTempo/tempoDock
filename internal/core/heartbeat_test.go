package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// heartbeatReceiver is a dead man's switch on 127.0.0.1 that counts the
// pings it gets; while down is set it answers 500.
type heartbeatReceiver struct {
	srv   *httptest.Server
	pings atomic.Int64
	down  atomic.Bool
}

// newHeartbeatReceiver starts a receiver and lets h's heartbeat reach it.
func newHeartbeatReceiver(h *harness) *heartbeatReceiver {
	h.c.AllowLoopbackAlertChannels(true)
	k := &heartbeatReceiver{}
	k.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if k.down.Load() {
			http.Error(w, "gone", http.StatusInternalServerError)
		}
		k.pings.Add(1)
	}))
	h.t.Cleanup(k.srv.Close)
	return k
}

// heartbeatState reads the heartbeat's failures in a row and when its
// last ping was started.
func (h *harness) heartbeatState() (failures int, lastAttempt time.Time) {
	hb := &h.c.heartbeat
	hb.mu.Lock()
	defer hb.mu.Unlock()
	return hb.failures, hb.lastAttempt
}

// heartbeatSending reports whether the tick's ping is still on its way.
func (h *harness) heartbeatSending() bool {
	hb := &h.c.heartbeat
	hb.mu.Lock()
	defer hb.mu.Unlock()
	return hb.sending
}

// A ping that got no answer is worded for the person reading the alert:
// a slow receiver by the time it had, and never with the URL.
func TestHeartbeatSendErr(t *testing.T) {
	const u = "https://monitor.example.com/ping/check-3f2a9c"
	slow := heartbeatSendErr(&url.Error{Op: "Get", URL: u, Err: context.DeadlineExceeded})
	if got, want := slow.Error(), "the heartbeat URL didn't answer within 15 seconds"; got != want {
		t.Fatalf("a slow receiver reads %q, want %q", got, want)
	}
	refused := heartbeatSendErr(&url.Error{Op: "Get", URL: u, Err: errors.New("connection refused")})
	if got, want := refused.Error(), "GET the heartbeat URL: connection refused"; got != want {
		t.Fatalf("a refused connection reads %q, want %q", got, want)
	}
}

// Times in these tests start at the real clock: the harness's own
// statistics tick may ping too, and then counts as one of the test's.

// A ping goes out when one is due and not again before the interval,
// which a tick may be 30 seconds early for.
func TestHeartbeatPingsWhenDue(t *testing.T) {
	h := newHarness(t)
	k := newHeartbeatReceiver(h)
	if _, err := h.c.SetHeartbeat(h.ctx, testSystem, apitypes.SetHeartbeatRequest{URL: k.srv.URL + "/ping/abc"}); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	sent := func(n int64) {
		t.Helper()
		eventually(t, "the ping's outcome", func() bool {
			hb, err := h.c.GetHeartbeat(h.ctx, testSystem)
			return err == nil && k.pings.Load() == n && hb.LastSentAt != nil && !h.heartbeatSending()
		})
	}
	h.c.heartbeatTick(h.ctx, t0)
	sent(1)
	_, first := h.heartbeatState()

	h.c.heartbeatTick(h.ctx, t0.Add(4*time.Minute))
	if _, at := h.heartbeatState(); !at.Equal(first) {
		t.Fatalf("a ping was started %v after the last one, with one due every 5 minutes", at.Sub(first))
	}
	h.c.heartbeatTick(h.ctx, t0.Add(5*time.Minute-20*time.Second))
	sent(2)
	if hb, _ := h.c.GetHeartbeat(h.ctx, testSystem); hb.Minutes != 5 || hb.Failures != 0 {
		t.Fatalf("heartbeat = %+v, want the default 5 minutes and no failures", hb)
	}
}

// The alert opens after three failed pings in a row and resolves with the
// next one that works; removing the heartbeat resolves it too.
func TestHeartbeatAlertAfterThreeFailures(t *testing.T) {
	h := newHarness(t)
	k := newHeartbeatReceiver(h)
	k.down.Store(true)
	set := func() {
		t.Helper()
		if _, err := h.c.SetHeartbeat(h.ctx, testSystem, apitypes.SetHeartbeatRequest{URL: k.srv.URL, Minutes: 1}); err != nil {
			t.Fatal(err)
		}
	}
	set()
	at := time.Now()
	// fail ticks until n pings in a row have failed. The harness's own
	// tick may have sent one of them before the test's first tick (once
	// the test's run ahead of the clock, none is due for it), so it
	// counts the outcomes rather than its ticks.
	fail := func(n int) {
		t.Helper()
		for {
			failures, _ := h.heartbeatState()
			if failures >= n && !h.heartbeatSending() {
				return
			}
			at = at.Add(time.Minute)
			h.c.heartbeatTick(h.ctx, at)
			eventually(t, "the failed ping's outcome", func() bool {
				now, _ := h.heartbeatState()
				return now > failures && !h.heartbeatSending()
			})
		}
	}
	fail(2)
	h.openAlert(heartbeatAlertKey, false)
	fail(3)
	a := h.openAlert(heartbeatAlertKey, true)
	if a.Kind != string(apitypes.AlertConnection) || !strings.Contains(a.Message, "500") || a.Command != "dock alerts heartbeat test" {
		t.Fatalf("alert = %+v", a)
	}

	k.down.Store(false)
	at = at.Add(time.Minute)
	h.c.heartbeatTick(h.ctx, at)
	h.openAlert(heartbeatAlertKey, false)

	k.down.Store(true)
	set() // failures start over with a new setting
	fail(3)
	h.openAlert(heartbeatAlertKey, true)
	if err := h.c.RemoveHeartbeat(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	h.openAlert(heartbeatAlertKey, false)
	if _, err := h.c.GetHeartbeat(h.ctx, testSystem); CodeOf(err) != apitypes.CodeNotFound || !strings.Contains(err.Error(), "dock alerts heartbeat set") {
		t.Fatalf("reading a removed heartbeat: %v", err)
	}
}

// The URL is a secret: it is sealed in the store and in no output, error,
// alert or audit event, also after a restart. Only operators with
// platform.alerts reach the heartbeat.
func TestHeartbeatURLStaysSecret(t *testing.T) {
	h := newHarness(t)
	k := newHeartbeatReceiver(h)
	k.down.Store(true)
	const id = "check-3f2a9c"
	for _, a := range []Actor{
		{Kind: "token", OrgID: 1, Name: "ops", Scope: apitypes.ScopeAdmin},
		{Kind: "user", OrgID: 1, Name: "viewer", Scope: apitypes.ScopeAdmin, Platform: apitypes.PlatformViewer},
	} {
		_, get := h.c.GetHeartbeat(h.ctx, a)
		_, set := h.c.SetHeartbeat(h.ctx, a, apitypes.SetHeartbeatRequest{URL: k.srv.URL})
		_, test := h.c.TestHeartbeat(h.ctx, a)
		for _, err := range []error{get, set, test, h.c.RemoveHeartbeat(h.ctx, a)} {
			if CodeOf(err) != apitypes.CodeForbidden {
				t.Fatalf("%s reached the heartbeat: %v", a.Name, err)
			}
		}
	}

	var seen []any
	hb, err := h.c.SetHeartbeat(h.ctx, testSystem, apitypes.SetHeartbeatRequest{URL: k.srv.URL + "/ping/" + id + "?key=" + id})
	if err != nil {
		t.Fatal(err)
	}
	if hb.Target != k.srv.URL {
		t.Fatalf("target = %q, want scheme and host %q", hb.Target, k.srv.URL)
	}
	res, err := h.c.TestHeartbeat(h.ctx, testSystem)
	if err != nil || res.OK || !strings.Contains(res.Error, "500") {
		t.Fatalf("test against a failing receiver = %+v, %v", res, err)
	}
	seen = append(seen, hb, res)
	// A receiver that is gone fails in the transport, whose errors name
	// the URL unless it is taken out.
	k.srv.Close()
	for range heartbeatAlertAfter {
		res, err = h.c.TestHeartbeat(h.ctx, testSystem)
		if err != nil || res.OK {
			t.Fatalf("test against a closed receiver = %+v, %v", res, err)
		}
		seen = append(seen, res)
	}
	seen = append(seen, h.openAlert(heartbeatAlertKey, true))

	h.restart()
	hb, err = h.c.GetHeartbeat(h.ctx, testSystem)
	if err != nil || hb.Target != k.srv.URL || hb.Minutes != 5 {
		t.Fatalf("heartbeat after a restart = %+v, %v", hb, err)
	}
	events, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Platform: true})
	if err != nil || len(events) == 0 {
		t.Fatalf("platform audit = %v, %v", events, err)
	}
	row, err := h.store.GetHeartbeat(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen = append(seen, hb, events, string(row.Url), row.Target)
	out, err := json.Marshal(seen)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), id) {
		t.Fatalf("the heartbeat's URL shows: %s", out)
	}
}
