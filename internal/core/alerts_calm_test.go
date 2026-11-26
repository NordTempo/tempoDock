package core

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Calmer alerts (ADR 0097): settling, counts, repeats, grouping, and
// resolutions that are never dropped.

// Channels send at once in tests; the grouping test holds its receiver
// instead of waiting for the window.
func init() { groupWindow = 0 }

// settling restarts h's daemon with [alerts] settle_minutes = 3, which
// the harness turns off.
func (h *harness) settling() {
	h.t.Helper()
	h.opts.Config.Alerts.SettleMinutes = 3
	h.restart()
}

// heldReceiver is a channel endpoint whose first delivery waits for
// release, so what is raised meanwhile queues up behind it. It records
// every body.
type heldReceiver struct {
	srv *httptest.Server
	// arrived is closed when the first delivery is in.
	arrived chan struct{}
	release func()
	mu      sync.Mutex
	bodies  []string
}

// newHeldReceiver starts one on 127.0.0.1 and lets h's channels deliver
// there.
func newHeldReceiver(h *harness) *heldReceiver {
	h.c.AllowLoopbackAlertChannels(true)
	k := &heldReceiver{arrived: make(chan struct{})}
	gate := make(chan struct{})
	k.release = sync.OnceFunc(func() { close(gate) })
	var first sync.Once
	k.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		first.Do(func() {
			close(k.arrived)
			<-gate
		})
		k.mu.Lock()
		k.bodies = append(k.bodies, string(body))
		k.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	h.t.Cleanup(k.srv.Close)
	h.t.Cleanup(k.release) // runs first: a failed test doesn't hang Close
	return k
}

// got is every body received so far.
func (k *heldReceiver) got() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.bodies...)
}

// An alert of a kind that flaps stays open when its condition clears: back
// within the settling time it is the same alert, counted again, with
// nothing sent; once it stayed clear it resolves, saying how often it
// happened. A cleared alert keeps settling across a restart.
func TestFlappingAlertSettles(t *testing.T) {
	h := newHarness(t)
	h.settling()
	k := newReceiver(h)
	h.platformChannel(apitypes.CreateAlertChannelRequest{Name: "hook", Kind: apitypes.ChannelWebhook, URL: k.srv.URL, Secret: "x"})
	sig := alertSignal{Key: "server:99", Kind: apitypes.AlertServerOffline, Severity: apitypes.SeverityCritical, Title: "Server edge is offline", Server: "edge"}
	t0 := time.Now()
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

	h.c.raiseAlert(h.ctx, sig, t0)
	k.events(t, 1)
	h.c.resolveAlert(h.ctx, sig.Key, at(1))
	if a := h.openAlert(sig.Key, true); a.ClearedAt == nil || a.Count != 1 || toAlert(a).ClearedAt == nil {
		t.Fatalf("a cleared alert = %+v, want open and cleared", a)
	}
	h.c.resolveAlert(h.ctx, sig.Key, at(2)) // clearing again doesn't restart the wait
	h.c.settleAlerts(h.ctx, at(3))
	h.openAlert(sig.Key, true)

	h.c.raiseAlert(h.ctx, sig, at(3))
	a := h.openAlert(sig.Key, true)
	if a.ClearedAt != nil || a.Count != 2 {
		t.Fatalf("the alert after its condition came back = %+v, want count 2 and not cleared", a)
	}
	h.c.settleAlerts(h.ctx, at(30))
	h.openAlert(sig.Key, true)

	h.c.resolveAlert(h.ctx, sig.Key, at(31))
	// The opening's delivery has come through before the restart: one cut
	// short by it is sent again after, as it should be.
	h.outboxSent()
	h.restart()
	h.c.AllowLoopbackAlertChannels(true)
	if b := h.openAlert(sig.Key, true); b.ID != a.ID || b.ClearedAt == nil || b.Count != 2 {
		t.Fatalf("the alert after a restart = %+v, want the same one, cleared, count 2", b)
	}
	h.c.settleAlerts(h.ctx, at(33))
	h.openAlert(sig.Key, true)
	h.c.settleAlerts(h.ctx, at(34))
	h.openAlert(sig.Key, false)

	evs := k.events(t, 2)
	h.outboxSent()
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.got) != 2 || strings.Join(evs, ",") != "opened,resolved" || k.got[1].Alert.Count != 2 {
		t.Fatalf("deliveries = %+v, want one opened and one resolved with count 2", k.got)
	}
	if got := headline(k.got[1]); got != "[resolved] Server edge is offline (happened 2 times)" {
		t.Fatalf("resolution's headline = %q", got)
	}
}

// Resolving by hand, and a signal that says the thing is gone, close a
// settling kind's alert at once.
func TestResolveByHandSkipsSettling(t *testing.T) {
	h := newHarness(t)
	h.settling()
	now := time.Now()
	sig := alertSignal{Key: "uptime:99", Kind: apitypes.AlertUptimeDown, Severity: apitypes.SeverityCritical, Title: "https://shop.test is down"}
	h.c.raiseAlert(h.ctx, sig, now)
	got, err := h.c.ResolveAlert(h.ctx, testSystem, h.openAlert(sig.Key, true).ID)
	if err != nil || got.ResolvedAt == nil {
		t.Fatalf("resolved by hand = %+v, %v", got, err)
	}
	h.openAlert(sig.Key, false)

	h.c.raiseAlert(h.ctx, sig, now)
	h.c.signalAlert(h.ctx, alertSignal{Key: sig.Key, Resolve: true})
	h.openAlert(sig.Key, true)
	h.c.signalAlert(h.ctx, alertSignal{Key: sig.Key, Resolve: true, Gone: true})
	h.openAlert(sig.Key, false)
}

// A deploy that fails again while its alert is open is the same alert,
// counted, and its channels hear "repeated".
func TestRepeatedDeployFailureNotifies(t *testing.T) {
	h := newHarness(t)
	k := newReceiver(h)
	h.channel(apitypes.CreateAlertChannelRequest{Name: "hook", Kind: apitypes.ChannelWebhook, URL: k.srv.URL, Secret: "x"})
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	h.rt.FailPull(errors.New("manifest unknown"))
	h.deploy(web, "app:2")
	k.events(t, 1)
	h.deploy(web, "app:3")
	evs := k.events(t, 2)
	h.outboxSent()
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.got) != 2 || strings.Join(evs, ",") != "opened,repeated" {
		t.Fatalf("deliveries = %+v, want opened then repeated", k.got)
	}
	n := k.got[1]
	if n.Alert.ID != k.got[0].Alert.ID || n.Alert.Count != 2 || n.Alert.Target == nil || n.Alert.Target.Release != 3 {
		t.Fatalf("repeated = %+v, want the same alert with count 2 about release 3", n.Alert)
	}
	if got := headline(n); got != "[warning, again] Deploy of shop/production/web failed (2 times)" {
		t.Fatalf("headline = %q", got)
	}
}

// Alerts queued for a Slack channel together arrive as one message.
func TestSlackGroupsQueuedAlerts(t *testing.T) {
	h := newHarness(t)
	k := newHeldReceiver(h)
	h.platformChannel(apitypes.CreateAlertChannelRequest{Name: "ops", Kind: apitypes.ChannelSlack, URL: k.srv.URL})
	raise := func(key string) {
		h.c.signalAlert(h.ctx, alertSignal{Key: key, Kind: "test", Severity: apitypes.SeverityWarning, Title: "Broken " + key})
	}
	raise("first")
	<-k.arrived
	for _, key := range []string{"a", "b", "c"} {
		raise(key)
	}
	k.release()
	h.outboxSent()
	got := k.got()
	if len(got) != 2 {
		t.Fatalf("got %d messages %q, want the first alert and one for the other three", len(got), got)
	}
	var msg struct{ Text string }
	if err := json.Unmarshal([]byte(got[1]), &msg); err != nil {
		t.Fatal(err)
	}
	if want := "3 alerts\n\n[warning] Broken a\n\n[warning] Broken b\n\n[warning] Broken c"; msg.Text != want {
		t.Fatalf("grouped message = %q, want %q", msg.Text, want)
	}
}

// A daemon that stops while a channel has alerts queued sends them before
// it is gone, once: the delivery it was cut off in and what waited behind
// it go out as one last message, and the next daemon sends nothing again.
func TestStoppingDaemonSendsQueuedAlerts(t *testing.T) {
	h := newHarness(t)
	h.c.AllowLoopbackAlertChannels(true)
	// The first delivery hangs at the receiver until the test ends; what
	// comes after it is answered and kept.
	arrived, gate, got := make(chan struct{}), make(chan struct{}), make(chan string, 4)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if requests.Add(1) == 1 {
			close(arrived)
			<-gate
			return
		}
		got <- string(body)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(gate) }) // runs first: srv.Close waits for the handler
	h.platformChannel(apitypes.CreateAlertChannelRequest{Name: "ops", Kind: apitypes.ChannelSlack, URL: srv.URL})
	for _, key := range []string{"first", "a", "b"} {
		if key == "a" {
			<-arrived
		}
		h.c.signalAlert(h.ctx, alertSignal{Key: key, Kind: "test", Severity: apitypes.SeverityWarning, Title: "Broken " + key})
	}
	h.restart()
	h.outboxSent()
	if n := requests.Load(); n != 2 {
		t.Fatalf("the receiver got %d requests, want the one cut off and one sent while stopping", n)
	}
	var msg struct{ Text string }
	if err := json.Unmarshal([]byte(<-got), &msg); err != nil {
		t.Fatal(err)
	}
	if want := "3 alerts\n\n[warning] Broken first\n\n[warning] Broken a\n\n[warning] Broken b"; msg.Text != want {
		t.Fatalf("message sent while stopping = %q, want %q", msg.Text, want)
	}
}

// A channel whose queue is full drops what opens, never a resolution.
func TestResolutionSurvivesFullQueue(t *testing.T) {
	h := newHarness(t)
	k := newHeldReceiver(h)
	h.platformChannel(apitypes.CreateAlertChannelRequest{Name: "hook", Kind: apitypes.ChannelWebhook, URL: k.srv.URL, Secret: "x"})
	raise := func(key string) {
		h.c.signalAlert(h.ctx, alertSignal{Key: key, Kind: "test", Severity: apitypes.SeverityWarning, Title: "Broken " + key})
	}
	raise("first")
	<-k.arrived
	const more = maxQueued + 5
	for i := range more {
		raise("n" + itoa(int64(i)))
	}
	h.c.signalAlert(h.ctx, alertSignal{Key: "first", Resolve: true})
	k.release()
	h.outboxSent()
	var opened, resolved int
	for _, body := range k.got() {
		var n apitypes.AlertNotification
		if err := json.Unmarshal([]byte(body), &n); err != nil {
			t.Fatal(err)
		}
		switch {
		case n.Event == "opened":
			opened++
		case n.Event == "resolved" && n.Alert.Title == "Broken first":
			resolved++
		}
	}
	// The first is still queued while it is being sent, so one fewer fits.
	if opened != maxQueued || resolved != 1 {
		t.Fatalf("got %d opened and %d resolved, want %d (the rest dropped) and the resolution", opened, resolved, maxQueued)
	}
}
