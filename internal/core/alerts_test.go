package core

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// openAlert waits until the alert for key is open (or, with want false,
// not open) and returns it.
func (h *harness) openAlert(key string, want bool) store.Alert {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.c.alerts.mu.Lock()
		a, ok := h.c.alerts.open[key]
		h.c.alerts.mu.Unlock()
		if ok == want {
			return a
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("alert %s open = %v, want %v", key, ok, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// channelIdle waits until alert channel id has nothing queued or being
// sent, so what it was sent so far has had its outcome recorded.
func (h *harness) channelIdle(id int64) {
	h.t.Helper()
	eventually(h.t, "the channel's queue to empty", func() bool {
		q := &h.c.alerts.outbox
		q.mu.Lock()
		defer q.mu.Unlock()
		_, busy := q.queues[id]
		return !busy
	})
}

// receiver is a webhook endpoint that records what it is sent. fail makes
// the next that many deliveries answer 500.
type receiver struct {
	srv  *httptest.Server
	mu   sync.Mutex
	got  []apitypes.AlertNotification
	sigs []string
	fail int
}

// It is on 127.0.0.1, so h's channels may deliver there.
func newReceiver(h *harness) *receiver {
	t := h.t
	h.c.AllowLoopbackAlertChannels(true)
	k := &receiver{}
	k.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		k.mu.Lock()
		defer k.mu.Unlock()
		if k.fail > 0 {
			k.fail--
			http.Error(w, "down for maintenance", http.StatusInternalServerError)
			return
		}
		var n apitypes.AlertNotification
		if _ = json.Unmarshal(body, &n); n.Event == "" {
			// A Slack body: keep its text as the title.
			var s struct{ Text string }
			_ = json.Unmarshal(body, &s)
			n.Alert.Title = s.Text
		}
		k.got = append(k.got, n)
		k.sigs = append(k.sigs, r.Header.Get(SignatureHeader)+"|"+string(body))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(k.srv.Close)
	return k
}

// events waits for n deliveries and returns their events.
func (k *receiver) events(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		k.mu.Lock()
		got := len(k.got)
		var evs []string
		for _, g := range k.got {
			evs = append(evs, g.Event)
		}
		k.mu.Unlock()
		if got >= n {
			return evs
		}
		if time.Now().After(deadline) {
			t.Fatalf("got %d deliveries %v, want %d", got, evs, n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// outboxSent waits until the alert outbox is empty: each channel's sender
// removes its queue once everything in it was sent or given up on. raise
// and remind queue before they return, so after this nothing they caused
// is still to be delivered.
func (h *harness) outboxSent() {
	h.t.Helper()
	o, k := &h.c.alerts.outbox, &h.c.alerts.hooks
	eventually(h.t, "the alert outbox and the status hooks' queue to empty", func() bool {
		o.mu.Lock()
		k.mu.Lock()
		defer o.mu.Unlock()
		defer k.mu.Unlock()
		return len(o.queues) == 0 && len(k.queues) == 0
	})
}

// channel adds a channel to org 1, for its alerts.
func (h *harness) channel(req apitypes.CreateAlertChannelRequest) apitypes.CreatedAlertChannel {
	return h.addChannel(false, req)
}

// platformChannel adds a channel for the platform's alerts.
func (h *harness) platformChannel(req apitypes.CreateAlertChannelRequest) apitypes.CreatedAlertChannel {
	return h.addChannel(true, req)
}

func (h *harness) addChannel(platform bool, req apitypes.CreateAlertChannelRequest) apitypes.CreatedAlertChannel {
	h.t.Helper()
	ch, err := h.c.CreateAlertChannel(h.ctx, testSystem, platform, req)
	if err != nil {
		h.t.Fatal(err)
	}
	return ch
}

// A failed deploy's alert says the release before it keeps serving only
// when one does, from the bus event and from reconcileAlerts alike.
func TestDeployFailureSaysWhatServes(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	api := h.service("api", apitypes.CreateServiceRequest{})
	h.rt.FailPull(errors.New("manifest unknown"))
	h.deploy(web, "app:2")
	h.deploy(api, "app:2")
	webKey, apiKey := "deploy:service:"+itoa(h.serviceID(web)), "deploy:service:"+itoa(h.serviceID(api))
	check := func(from string) {
		t.Helper()
		if m := h.openAlert(webKey, true).Message; !strings.Contains(m, "The release before it keeps serving.") {
			t.Errorf("%s: web's alert = %q", from, m)
		}
		if m := h.openAlert(apiKey, true).Message; !strings.Contains(m, "Nothing served before it, so the service has no running release.") {
			t.Errorf("%s: api's alert = %q", from, m)
		}
	}
	check("bus event")
	for _, key := range []string{webKey, apiKey} {
		if _, err := h.c.ResolveAlert(h.ctx, testSystem, h.openAlert(key, true).ID); err != nil {
			t.Fatal(err)
		}
	}
	h.c.reconcileAlerts(h.ctx, time.Now())
	check("reconcile")
}

// A failed deploy opens one alert however often the service fails again,
// and the next deploy that goes live resolves it.
func TestDeployFailureAlertDedupesAndResolves(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")

	h.rt.FailPull(errors.New("manifest unknown"))
	h.deploy(web, "app:2")
	a := h.openAlert("deploy:service:1", true)
	if a.Kind != string(apitypes.AlertDeployFailed) || a.Service != "web" || !strings.Contains(a.Message, "Release 2 failed") {
		t.Fatalf("alert = %+v", a)
	}
	// Its diagnosis named the cause (ADR 0071), so it points there.
	if out := toAlert(a); out.Target == nil || out.Target.Release != 2 || out.Command != "dock diagnose -r 2 -p shop -e production -s web" ||
		!strings.Contains(out.Message, "Likely cause: The image or its tag doesn't exist in the registry.") {
		t.Fatalf("target %+v, command %q, message %q", out.Target, out.Command, out.Message)
	}
	h.deploy(web, "app:3")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(h.openAlert("deploy:service:1", true).Message, "Release 3") {
		if time.Now().After(deadline) {
			t.Fatal("the open alert should follow the newest failure")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if all, _ := h.c.ListAlerts(h.ctx, testSystem, false, true, 0, 0); len(all) != 1 || all[0].ID != a.ID {
		t.Fatalf("a failure while the alert is open raised another: %+v", all)
	}

	h.rt.FailPull(nil)
	h.deploy(web, "app:4")
	h.openAlert("deploy:service:1", false)
	all, _ := h.c.ListAlerts(h.ctx, testSystem, false, true, 0, 0)
	if len(all) != 1 || all[0].ResolvedAt == nil {
		t.Fatalf("alerts after a good deploy = %+v", all)
	}
	if open, _ := h.c.ListAlerts(h.ctx, testSystem, false, false, 0, 0); len(open) != 0 {
		t.Fatalf("open alerts = %+v", open)
	}
}

// A release's active event can arrive after a newer release failed (its
// cutover publishes it last): it doesn't resolve the newer failure's
// alert; a release that goes live after it does.
func TestLateActiveEventKeepsDeployAlert(t *testing.T) {
	h := newHarness(t)
	ev := func(number int64, status apitypes.ReleaseStatus) LiveEvent {
		return newLiveEvent(apitypes.Event{
			Type: apitypes.EventRelease, Project: "shop", Environment: "production", Service: "web",
			Release: &apitypes.Release{Number: number, Status: status, Error: "pull failed"},
		})
	}
	key := "deploy:shop/production/web"
	now := time.Now()
	h.c.onAlertEvent(h.ctx, ev(2, apitypes.ReleaseFailed), now)
	h.c.onAlertEvent(h.ctx, ev(1, apitypes.ReleaseActive), now)
	h.openAlert(key, true)
	h.c.onAlertEvent(h.ctx, ev(3, apitypes.ReleaseActive), now)
	h.openAlert(key, false)
}

// Alerts survive a restart; one whose condition cleared meanwhile (the
// deploy alert's service now has a good release) is resolved on start.
func TestAlertsReconcileOnStart(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.rt.FailPull(errors.New("manifest unknown"))
	h.deploy(web, "app:1")
	h.openAlert("deploy:service:1", true)
	h.restart()
	h.openAlert("deploy:service:1", true)

	// The alert engine misses what happens while the daemon is down: a
	// release that succeeded is only in the DB.
	h.c.Close()
	ss, err := h.c.serviceFor(h.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := h.store.ListReleases(h.ctx, store.ListReleasesParams{ServiceID: ss.svc.ID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.ActivateRelease(h.ctx, store.ActivateReleaseParams{FinishedAt: ptr(now()), ID: rel[0].ID}); err != nil {
		t.Fatal(err)
	}
	h.c = h.start()
	h.openAlert("deploy:service:1", false)
}

// Disk alerts warn and then go critical as a disk fills, and resolve only
// once it drops clearly below the warning line. Channels hear about the
// opening, the escalation and the resolution, each signed.
func TestDiskAlertEscalatesAndResolves(t *testing.T) {
	h := newHarness(t)
	k := newReceiver(h)
	created := h.platformChannel(apitypes.CreateAlertChannelRequest{Name: "ops", Kind: apitypes.ChannelWebhook, URL: k.srv.URL + "/hook?token=abc"})
	if len(created.Secret) != 64 || created.Channel.Target != k.srv.URL {
		t.Fatalf("created = %+v (the secret is generated, the target has no path)", created)
	}
	srv, err := h.store.GetServer(h.ctx, h.c.localID)
	if err != nil {
		t.Fatal(err)
	}
	usage := func(pct int64) apitypes.ServerUsage {
		return apitypes.ServerUsage{Disks: []apitypes.DiskUsage{{Paths: []string{"/var/lib/docker"}, UsedBytes: pct << 30, TotalBytes: 100 << 30}}}
	}
	at := time.Now()
	step := func(pct int64) {
		at = at.Add(time.Minute)
		h.c.diskAlert(h.ctx, srv, usage(pct), at)
	}
	key := "disk:" + itoa(srv.ID)
	step(80)
	h.openAlert(key, false)
	step(86)
	if a := h.openAlert(key, true); a.Severity != "warning" || a.Title != "Disk 86% full on "+srv.Name {
		t.Fatalf("alert = %+v", a)
	}
	step(96)
	if a := h.openAlert(key, true); a.Severity != "critical" {
		t.Fatalf("alert = %+v", a)
	}
	step(84) // under the warning line, within the hysteresis: still open
	h.openAlert(key, true)
	step(82)
	h.openAlert(key, false)

	if evs := k.events(t, 3); strings.Join(evs, ",") != "opened,escalated,resolved" {
		t.Fatalf("deliveries = %v", evs)
	}
	// Each body is signed with the channel's secret over "<t>.<body>".
	for _, s := range k.sigs {
		header, body, _ := strings.Cut(s, "|")
		ts, sig, _ := strings.Cut(strings.TrimPrefix(header, "t="), ",v1=")
		mac := hmac.New(sha256.New, []byte(created.Secret))
		mac.Write([]byte(ts + "." + body))
		if !hmac.Equal([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil)))) {
			t.Fatalf("bad signature %q", header)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// A delivery is signed when it is attempted, so a retry long after the
// alert still passes a receiver's timestamp check.
func TestWebhookSignedAtAttempt(t *testing.T) {
	h := newHarness(t)
	k := newReceiver(h)
	created := h.channel(apitypes.CreateAlertChannelRequest{Name: "hook", Kind: apitypes.ChannelWebhook, URL: k.srv.URL})
	ch, err := h.store.GetAlertChannel(h.ctx, store.GetAlertChannelParams{Org: 1, Name: "hook"})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := h.c.send(h.ctx, ch, apitypes.AlertNotification{Event: "opened", SentAt: old, Alert: apitypes.Alert{Title: "x"}}); err != nil {
		t.Fatal(err)
	}
	k.mu.Lock()
	header, body, _ := strings.Cut(k.sigs[0], "|")
	k.mu.Unlock()
	ts, _, _ := strings.Cut(strings.TrimPrefix(header, "t="), ",")
	sec, _ := strconv.ParseInt(ts, 10, 64)
	if time.Since(time.Unix(sec, 0)) > time.Minute || header != signWebhook(created.Secret, time.Unix(sec, 0), []byte(body)) {
		t.Fatalf("signature %q for a retry of an alert sent at %s", header, old)
	}
}

// Alert text can quote build output; Slack must show it, not act on it.
// The one link is Dock's own, to the UI.
func TestSlackTextIsEscaped(t *testing.T) {
	got := slackText(apitypes.AlertNotification{Event: "opened", Alert: apitypes.Alert{
		Severity: apitypes.SeverityWarning, Title: "Deploy of a/b/c failed", Message: "<!channel> <https://evil.example|log in> & retry",
	}}, "https://dock.example.com/a&b")
	if want := "[warning] Deploy of a/b/c failed\n&lt;!channel&gt; &lt;https://evil.example|log in&gt; &amp; retry\n<https://dock.example.com/a&amp;b|Open in Dock>"; got != want {
		t.Fatalf("slack text = %q", got)
	}
}

// Discord text can't format, link or mention: only Dock's own command and
// link are markdown.
func TestDiscordTextIsEscaped(t *testing.T) {
	got := discordText(apitypes.AlertNotification{Event: "opened", Alert: apitypes.Alert{
		Severity: apitypes.SeverityWarning, Title: "Deploy of a/b/c failed", Message: "@everyone [log in](https://evil.example) **now**",
		Command: "dock logs web",
	}}, "https://dock.example.com/a")
	want := "\\[warning\\] Deploy of a/b/c failed\n@\u200beveryone \\[log in\\](https://evil.example) \\*\\*now\\*\\*\n`dock logs web`\n[Open in Dock](<https://dock.example.com/a>)"
	if got != want {
		t.Fatalf("discord text = %q, want %q", got, want)
	}
}

// A group too long for one message keeps whole alerts and counts the rest.
func TestJoinFitting(t *testing.T) {
	if got := joinFitting([]string{"aaaa", "bbbb", "cccc"}, "\n", 30); got != "aaaa\nbbbb\ncccc" {
		t.Fatalf("fits = %q", got)
	}
	a, b, c := strings.Repeat("a", 20), strings.Repeat("b", 20), strings.Repeat("c", 20)
	if got := joinFitting([]string{a, b, c}, "\n", 55); got != a+"\n"+b+"\nand 1 more" {
		t.Fatalf("cut = %q", got)
	}
	if got := joinFitting([]string{strings.Repeat("x", 20)}, "\n", 10); utf8.RuneCountInString(got) != 10 {
		t.Fatalf("one long text = %q, want 10 characters", got)
	}
}

// Discord and Pushover channels get their own bodies: Discord's content
// with mentions off, Pushover's keys, the alert as title and message, and
// high priority while a critical alert is open.
func TestDiscordAndPushoverChannels(t *testing.T) {
	h := newHarness(t)
	h.c.AllowLoopbackAlertChannels(true)
	var mu sync.Mutex
	bodies := map[string][]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies[r.URL.Path] = append(bodies[r.URL.Path], string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	h.c.alerts.pushoverURL = srv.URL + "/pushover"
	user, token := strings.Repeat("u", 30), strings.Repeat("t", 30)
	h.platformChannel(apitypes.CreateAlertChannelRequest{Name: "discord", Kind: apitypes.ChannelDiscord, URL: srv.URL + "/discord"})
	created := h.platformChannel(apitypes.CreateAlertChannelRequest{Name: "push", Kind: apitypes.ChannelPushover, User: user, Token: token})
	if created.Channel.Target != "pushover.net" {
		t.Fatalf("pushover target = %q", created.Channel.Target)
	}
	h.c.signalAlert(h.ctx, alertSignal{Key: "k", Kind: "test", Severity: apitypes.SeverityCritical, Title: "Server down", Message: "No answer for 5 minutes."})
	h.outboxSent()
	mu.Lock()
	defer mu.Unlock()

	var d discordMessage
	if len(bodies["/discord"]) != 1 || json.Unmarshal([]byte(bodies["/discord"][0]), &d) != nil {
		t.Fatalf("discord got %q", bodies["/discord"])
	}
	if d.Content != "\\[critical\\] Server down\nNo answer for 5 minutes." || d.AllowedMentions.Parse == nil || len(d.AllowedMentions.Parse) != 0 {
		t.Fatalf("discord message = %+v", d)
	}
	var p pushoverMessage
	if len(bodies["/pushover"]) != 1 || json.Unmarshal([]byte(bodies["/pushover"][0]), &p) != nil {
		t.Fatalf("pushover got %q", bodies["/pushover"])
	}
	if want := (pushoverMessage{Token: token, User: user, Title: "[critical] Server down", Message: "No answer for 5 minutes.", Priority: 1}); p != want {
		t.Fatalf("pushover message = %+v, want %+v", p, want)
	}
}

// Slack alerts link to the page that shows them, once Dock knows its UI's
// address: the release a deploy alert names, a platform alert's server,
// else the alerts page.
func TestAlertLinks(t *testing.T) {
	h := newHarness(t)
	org := int64(1)
	deploy := apitypes.Alert{Project: "shop", Environment: "production", Service: "web", Target: &apitypes.AlertTarget{Release: 7}}
	if got := h.c.alertLink(h.ctx, &org, deploy); got != "" {
		t.Fatalf("link without a UI address = %q", got)
	}
	h.opts.Config.Hooks.UIURL = "https://dock.example.com"
	h.restart()
	o, err := h.store.GetOrg(h.ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	ui := "https://dock.example.com"
	for _, tc := range []struct {
		org  *int64
		a    apitypes.Alert
		want string
	}{
		{&org, deploy, ui + "/" + o.Slug + "/shop/production/web/releases/7"},
		{&org, apitypes.Alert{Project: "shop", Environment: "production", Service: "web"}, ui + "/" + o.Slug + "/shop/production/web"},
		{&org, apitypes.Alert{Title: "Cloudflare connection cf is failing"}, ui + "/" + o.Slug + "/alerts"},
		{nil, apitypes.Alert{Server: "edge"}, ui + "/console/servers/edge"},
		{nil, apitypes.Alert{Title: "Registry push failing"}, ui + "/console/alerts"},
	} {
		if got := h.c.alertLink(h.ctx, tc.org, tc.a); got != tc.want {
			t.Errorf("link for %+v = %q, want %q", tc.a, got, tc.want)
		}
	}
}

// A channel that gets a final answer (Slack's 404 for a removed webhook,
// 410 for an archived channel) is marked broken: its error says so in
// Slack's words and an alert of its org stays open until a test works or
// the channel is removed. The channel is never removed by Dock.
func TestFinalAnswerMarksChannelBroken(t *testing.T) {
	saved := deliveryBackoff
	deliveryBackoff = []time.Duration{0, time.Millisecond}
	t.Cleanup(func() { deliveryBackoff = saved })
	h := newHarness(t)
	h.c.AllowLoopbackAlertChannels(true)
	var mu sync.Mutex
	status, answer := http.StatusNotFound, "no_service"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	answers := func(s int, a string) {
		mu.Lock()
		status, answer = s, a
		mu.Unlock()
	}
	h.channel(apitypes.CreateAlertChannelRequest{Name: "team", Kind: apitypes.ChannelSlack, URL: srv.URL})
	ch, err := h.store.GetAlertChannel(h.ctx, store.GetAlertChannelParams{Org: 1, Name: "team"})
	if err != nil {
		t.Fatal(err)
	}
	key := channelAlertKey(ch.ID)

	h.c.signalAlert(h.ctx, alertSignal{Key: "x", Kind: "test", Severity: apitypes.SeverityWarning, Title: "Something broke", orgID: 1})
	h.openAlert(key, true)
	// The broken alert is sent to the channel too, and answered 404: its
	// outcome lands before the next phase's answers.
	h.channelIdle(ch.ID)
	a := h.openAlert(key, true)
	if a.Kind != string(apitypes.AlertConnection) || alertOrgID(a) != 1 || a.Title != "Alert channel team is broken" ||
		!strings.Contains(a.Message, "Slack says this webhook no longer exists (404 no_service)") || a.Command != "dock alerts channel test team" {
		t.Fatalf("broken channel alert = %+v", a)
	}
	chs, err := h.c.ListAlertChannels(h.ctx, testSystem, false)
	if err != nil || len(chs) != 1 || !strings.HasPrefix(chs[0].LastError, "Slack says this webhook no longer exists") {
		t.Fatalf("channels = %+v, %v", chs, err)
	}

	answers(http.StatusOK, "ok")
	if res, err := h.c.TestAlertChannel(h.ctx, testSystem, false, "team"); err != nil || !res.OK {
		t.Fatalf("test = %+v, %v", res, err)
	}
	h.openAlert(key, false)
	h.channelIdle(ch.ID) // the resolve, sent to it

	answers(http.StatusGone, "channel_is_archived")
	h.c.signalAlert(h.ctx, alertSignal{Key: "y", Kind: "test", Severity: apitypes.SeverityWarning, Title: "Something else", orgID: 1})
	h.openAlert(key, true)
	h.channelIdle(ch.ID)
	if a := h.openAlert(key, true); !strings.Contains(a.Message, "Slack channel is archived (410 channel_is_archived)") {
		t.Fatalf("archived channel alert = %+v", a)
	}
	if err := h.c.RemoveAlertChannel(h.ctx, testSystem, false, "team"); err != nil {
		t.Fatal(err)
	}
	h.openAlert(key, false)
}

// Slack's rate limit (429 with Retry-After) makes the next attempt wait as
// long as it asks; an answer retrying can't change (404 no_service for a
// removed webhook, 410 channel_is_archived) ends the retries at once.
func TestDeliveryRetryAfterAndFinalAnswers(t *testing.T) {
	h := newHarness(t)
	h.c.AllowLoopbackAlertChannels(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, "rate_limited")
	}))
	t.Cleanup(srv.Close)
	err := h.c.alerts.post(h.ctx, true, srv.URL, []byte(`{"text":"x"}`), nil)
	if de, ok := errors.AsType[*deliveryError](err); !ok || de.retryAfter != 30*time.Second || de.final() || !strings.Contains(de.Error(), "429") {
		t.Fatalf("post to a rate limited webhook = %v", err)
	}

	synctest.Test(t, func(t *testing.T) {
		c := &Core{ctx: t.Context()}
		start := time.Now()
		var at []time.Duration
		answers := []error{&deliveryError{status: http.StatusTooManyRequests, retryAfter: 2 * time.Minute}, &deliveryError{status: http.StatusServiceUnavailable}, nil}
		err := c.withRetries(func() error {
			at = append(at, time.Since(start))
			e := answers[0]
			answers = answers[1:]
			return e
		})
		// The 10 s backoff stretched to the 2 minutes asked, then 1 minute.
		if want := []time.Duration{0, 2 * time.Minute, 3 * time.Minute}; err != nil || !slices.Equal(at, want) {
			t.Fatalf("attempts at %v (%v), want %v", at, err, want)
		}
		for _, status := range []int{400, 403, 404, 410} {
			calls := 0
			err := c.withRetries(func() error { calls++; return &deliveryError{status: status} })
			if calls != 1 || err == nil {
				t.Errorf("a %d answer was tried %d times (%v)", status, calls, err)
			}
		}
	})
}

// Channels never reach the server itself (its loopback-only internal
// listener and registry) or cloud metadata: literal addresses are refused
// when added, names when they resolve.
func TestChannelsNeverReachTheServer(t *testing.T) {
	h := newHarness(t)
	for _, u := range []string{
		"http://127.0.0.1:7071/internal/caddy/ask", "http://localhost:5000/v2/", "http://[::1]/", "http://0.0.0.0/",
		"http://169.254.169.254/latest/meta-data", "http://[fd00:ec2::254]/", "http://[::ffff:127.0.0.1]/",
		"http://168.63.129.16/machine?comp=goalstate",
	} {
		if _, err := h.c.CreateAlertChannel(h.ctx, testSystem, false, apitypes.CreateAlertChannelRequest{Name: "x", Kind: apitypes.ChannelWebhook, URL: u}); CodeOf(err) != apitypes.CodeValidation {
			t.Errorf("channel to %s: %v", u, err)
		}
	}
	// The platform's channels may reach a homelab's LAN; an org's may not
	// (the LAN, other orgs' containers), nor its status page hooks, unless
	// the operator allows the range for apps. Email channels name no host:
	// Dock's email server sends.
	h.platformChannel(apitypes.CreateAlertChannelRequest{Name: "lan", Kind: apitypes.ChannelSlack, URL: "http://192.168.1.20/hooks/dock"})
	for _, req := range []apitypes.CreateAlertChannelRequest{
		{Name: "lan", Kind: apitypes.ChannelSlack, URL: "http://192.168.1.20/hooks/dock"},
		{Name: "bridge", Kind: apitypes.ChannelWebhook, URL: "http://172.17.0.5:8080/admin"},
	} {
		if _, err := h.c.CreateAlertChannel(h.ctx, testSystem, false, req); CodeOf(err) != apitypes.CodeValidation {
			t.Errorf("org channel %s: %v", req.Name, err)
		}
	}
	allowed := newAlertEngine([]netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")})
	if _, _, _, err := allowed.channelSettings(apitypes.CreateAlertChannelRequest{Name: "lan", Kind: apitypes.ChannelSlack, URL: "http://192.168.1.20/hooks/dock"}, true); err != nil {
		t.Errorf("an org channel in [network] allow_private: %v", err)
	}

	// A name that resolves to loopback is refused when dialing.
	k := newReceiver(h)
	h.channel(apitypes.CreateAlertChannelRequest{Name: "self", Kind: apitypes.ChannelSlack, URL: k.srv.URL})
	h.c.AllowLoopbackAlertChannels(false)
	res, err := h.c.TestAlertChannel(h.ctx, testSystem, false, "self")
	if err != nil || res.OK || !strings.Contains(res.Error, "this server itself") {
		t.Fatalf("test of a loopback channel = %+v, %v", res, err)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.got) != 0 {
		t.Fatalf("the loopback receiver got %d deliveries", len(k.got))
	}
}

// Restarts in quick succession are a crash loop; slow ones aren't. The
// alert resolves once the service stays up for the window.
func TestCrashLoopAlert(t *testing.T) {
	h := newHarness(t)
	ev := func(restarts int) LiveEvent {
		return newLiveEvent(apitypes.Event{
			Type: apitypes.EventServiceStatus, Project: "shop", Environment: "production", Service: "web",
			ServiceStatus: &apitypes.ServiceStatus{State: apitypes.ServiceRunning, RestartCount: restarts, ExitCode: 137, OOMKilled: true, MemoryLimitMB: 256},
		})
	}
	key := "crash:shop/production/web"
	t0 := time.Now()
	h.c.onAlertEvent(h.ctx, ev(7), t0) // first sight: history
	for i, at := range []time.Duration{0, 6 * time.Minute, 12 * time.Minute, 18 * time.Minute} {
		h.c.onAlertEvent(h.ctx, ev(8+i), t0.Add(at))
	}
	h.openAlert(key, false)

	t1 := t0.Add(time.Hour)
	for i := range 3 {
		h.c.onAlertEvent(h.ctx, ev(12+i), t1.Add(time.Duration(i)*time.Minute))
	}
	a := h.openAlert(key, true)
	if a.Severity != "critical" || !strings.Contains(a.Message, "256 MB") {
		t.Fatalf("alert = %+v", a)
	}
	h.c.calmCrashLoops(h.ctx, t1.Add(5*time.Minute))
	h.openAlert(key, true)
	h.c.calmCrashLoops(h.ctx, t1.Add(13*time.Minute))
	h.openAlert(key, false)
}

// A server is reported offline only after server_offline_minutes, and
// the alert resolves when it reconnects.
func TestServerOfflineAlert(t *testing.T) {
	h := newHarness(t)
	// Last seen before the daemon started: offline counts from the start.
	seen := time.Now().Add(-time.Hour).UTC()
	srv, err := h.store.CreateServer(h.ctx, store.CreateServerParams{Name: "edge", AcceptsProduction: true, LastSeenAt: &seen})
	if err != nil {
		t.Fatal(err)
	}
	key := "server:" + itoa(srv.ID)
	started := h.c.alerts.started
	h.c.offlineAlert(h.ctx, srv, started.Add(4*time.Minute))
	h.openAlert(key, false)
	h.c.offlineAlert(h.ctx, srv, started.Add(6*time.Minute))
	if a := h.openAlert(key, true); a.Server != "edge" || a.Severity != "critical" {
		t.Fatalf("alert = %+v", a)
	}
	h.c.onAlertEvent(h.ctx, newLiveEvent(apitypes.Event{Type: apitypes.EventServer, Server: &apitypes.Server{ID: srv.ID, Status: apitypes.ServerOnline}}), time.Now())
	h.openAlert(key, false)
}

// A delivery that fails is retried; one that keeps failing shows on the
// channel with its error.
func TestDeliveryRetriesAndRecordsFailures(t *testing.T) {
	saved := deliveryBackoff
	deliveryBackoff = []time.Duration{0, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { deliveryBackoff = saved })
	h := newHarness(t)
	k := newReceiver(h)
	k.fail = 2
	h.platformChannel(apitypes.CreateAlertChannelRequest{Name: "flaky", Kind: apitypes.ChannelSlack, URL: k.srv.URL})
	h.c.signalAlert(h.ctx, alertSignal{Key: "x", Kind: "test", Severity: apitypes.SeverityWarning, Title: "Something broke"})
	k.events(t, 1)
	k.mu.Lock()
	text := k.got[0].Alert.Title
	k.mu.Unlock()
	if text != "[warning] Something broke" {
		t.Fatalf("slack text = %q", text)
	}
	waitChannel := func(name string, ok func(apitypes.AlertChannel) bool) apitypes.AlertChannel {
		deadline := time.Now().Add(5 * time.Second)
		for {
			chs, err := h.c.ListAlertChannels(h.ctx, testSystem, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, ch := range chs {
				if ch.Name == name && ok(ch) {
					return ch
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("channels = %+v", chs)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	waitChannel("flaky", func(ch apitypes.AlertChannel) bool { return ch.LastSentAt != nil && ch.Failures == 0 })

	k.mu.Lock()
	k.fail = 100
	k.mu.Unlock()
	h.c.signalAlert(h.ctx, alertSignal{Key: "y", Kind: "test", Severity: apitypes.SeverityWarning, Title: "Something else"})
	ch := waitChannel("flaky", func(ch apitypes.AlertChannel) bool { return ch.Failures == 1 })
	if !strings.Contains(ch.LastError, "500") {
		t.Fatalf("channel = %+v", ch)
	}
	test, err := h.c.TestAlertChannel(h.ctx, testSystem, true, "flaky")
	if err != nil || test.OK || test.Channel.Failures != 2 {
		t.Fatalf("test = %+v, %v", test, err)
	}
}

// Channels only hear alerts at or above their severity, and an open alert
// is sent again after the channel's renotify interval.
func TestSeverityFilterAndReminders(t *testing.T) {
	h := newHarness(t)
	k := newReceiver(h)
	one := 1
	h.platformChannel(apitypes.CreateAlertChannelRequest{Name: "pager", Kind: apitypes.ChannelWebhook, URL: k.srv.URL, Secret: "x", MinSeverity: apitypes.SeverityCritical, RenotifyMinutes: &one})
	t0 := time.Now()
	h.c.raiseAlert(h.ctx, alertSignal{Key: "w", Kind: "test", Severity: apitypes.SeverityWarning, Title: "minor"}, t0)
	h.c.raiseAlert(h.ctx, alertSignal{Key: "c", Kind: "test", Severity: apitypes.SeverityCritical, Title: "major"}, t0)
	k.events(t, 1)
	h.c.remind(h.ctx, t0.Add(30*time.Second))
	h.c.remind(h.ctx, t0.Add(61*time.Second))
	evs := k.events(t, 2)
	h.outboxSent()
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.got) != 2 || strings.Join(evs, ",") != "opened,reminder" || k.got[0].Alert.Title != "major" || k.got[1].Alert.Title != "major" {
		t.Fatalf("deliveries = %+v", k.got)
	}
}

// Members read their org's alerts; channels are for its unlimited admins,
// platform alerts and channels for operators. Channel secrets are sealed
// in the DB and never come back.
func TestAlertPermissionsAndSecrets(t *testing.T) {
	h := newHarness(t)
	for _, a := range []Actor{
		{Kind: "token", OrgID: 1, Name: "ci", Scope: apitypes.ScopeDeploy},
		{Kind: "token", OrgID: 1, Name: "ops", Scope: apitypes.ScopeAdmin, ProjectID: 1, ProjectName: "shop"},
	} {
		if _, err := h.c.ListAlerts(h.ctx, a, false, false, 0, 0); err != nil {
			t.Fatalf("%s can't list its org's alerts: %v", a, err)
		}
		if _, err := h.c.ListAlerts(h.ctx, a, true, false, 0, 0); CodeOf(err) != apitypes.CodeForbidden {
			t.Fatalf("%s listed the platform's alerts: %v", a, err)
		}
		if _, err := h.c.CreateAlertChannel(h.ctx, a, false, apitypes.CreateAlertChannelRequest{Name: "x", Kind: apitypes.ChannelSlack, URL: "https://hooks.example/x"}); CodeOf(err) != apitypes.CodeForbidden {
			t.Fatalf("%s added a channel: %v", a, err)
		}
		if _, err := h.c.ListAlertChannels(h.ctx, a, false); CodeOf(err) != apitypes.CodeForbidden {
			t.Fatalf("%s listed channels: %v", a, err)
		}
	}

	h.channel(apitypes.CreateAlertChannelRequest{Name: "mail", Kind: apitypes.ChannelEmail, To: []string{"ops@example.com"}})
	h.channel(apitypes.CreateAlertChannelRequest{Name: "slack", Kind: apitypes.ChannelSlack, URL: "https://hooks.slack.com/services/T0/B0/XYZSECRET"})
	chs, err := h.c.ListAlertChannels(h.ctx, testSystem, false)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(chs)
	if bytes.Contains(out, []byte("XYZSECRET")) || bytes.Contains(out, []byte("hunter22")) {
		t.Fatalf("channels leak secrets: %s", out)
	}
	rows, _ := h.store.ListAlertChannels(h.ctx, 1)
	for _, r := range rows {
		if bytes.Contains(r.Config, []byte("XYZSECRET")) || bytes.Contains(r.Config, []byte("hunter22")) {
			t.Fatalf("channel %s stored in plain text", r.Name)
		}
	}
	for _, bad := range []apitypes.CreateAlertChannelRequest{
		{Name: "a", Kind: apitypes.ChannelWebhook, URL: "ftp://x"},
		{Name: "b", Kind: apitypes.ChannelSlack, URL: "https://x", Secret: "s"},
		{Name: "c", Kind: apitypes.ChannelEmail, To: []string{"not an address"}},
		{Name: "d", Kind: apitypes.ChannelEmail},
		{Name: "slack", Kind: apitypes.ChannelSlack, URL: "https://x"},
		{Name: "e", Kind: apitypes.ChannelPushover, User: "short", Token: strings.Repeat("t", 30)},
		{Name: "f", Kind: apitypes.ChannelPushover, User: strings.Repeat("u", 30), Token: strings.Repeat("t", 30), URL: "https://x"},
		{Name: "g", Kind: apitypes.ChannelDiscord, URL: "https://x", User: strings.Repeat("u", 30)},
	} {
		if _, err := h.c.CreateAlertChannel(h.ctx, testSystem, false, bad); err == nil {
			t.Fatalf("channel %+v was accepted", bad)
		}
	}
}

// Fake hosts with a disk past prune_disk_percent get their dangling images
// pruned (keeping the active release's) and their build cache trimmed.
func TestFullDiskIsPruned(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	<-h.c.stats.firstTick
	disk := []node.Disk{{Paths: []string{"/var/lib/docker"}, Used: 90 << 30, Total: 100 << 30}}
	col := newCollector()
	at := time.Now().Add(time.Hour)
	for i := uint64(1); i <= 2; i++ {
		h.rt.SetHostStats(node.HostStats{CPUs: 1, CPUBusy: i, CPUTotal: 10 * i, MemoryTotal: 1 << 30, MemoryAvailable: 1 << 29, Disks: disk})
		h.c.statsTick(h.ctx, col, at.Add(time.Duration(i)*time.Minute))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		ops := strings.Join(h.rt.Ops(), "\n")
		if strings.Contains(ops, "prune images keep=sha256:") && strings.Contains(ops, "prune cache 2147483648") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ops = %s", ops)
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.openAlert("disk:"+itoa(h.c.localID), true)
}

// Archiving a service resolves its open alerts at once.
func TestArchiveServiceResolvesAlerts(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.rt.FailPull(errors.New("manifest unknown"))
	h.deploy(web, "app:1")
	h.openAlert("deploy:service:1", true)

	if _, err := h.c.ArchiveService(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	h.openAlert("deploy:service:1", false)
}

// The registry's bucket (ADR 0084) opens a warning while degraded, turns
// critical when pushes are refused, and resolves once healthy.
func TestRegistryBucketAlert(t *testing.T) {
	h := newHarness(t)
	h.c.RegistryBucketChanged(h.ctx, &apitypes.RegistryBucket{State: "degraded", Bad: 9, Calls: 64, Waiting: 3, WaitingBytes: 412 << 20})
	a := h.openAlert(registryBucketAlertKey, true)
	if a.Severity != string(apitypes.SeverityWarning) || !strings.Contains(a.Message, "9 of the last 64 calls") {
		t.Fatalf("degraded alert = %+v", a)
	}
	h.c.RegistryBucketChanged(h.ctx, &apitypes.RegistryBucket{State: "failing", Refusing: "spool", WaitingBytes: 20 << 30})
	deadline := time.Now().Add(5 * time.Second)
	for a = h.openAlert(registryBucketAlertKey, true); a.Severity != string(apitypes.SeverityCritical); a = h.openAlert(registryBucketAlertKey, true) {
		if time.Now().After(deadline) {
			t.Fatalf("refusing alert = %+v", a)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !strings.Contains(a.Title, "refuses pushes") {
		t.Errorf("title = %q", a.Title)
	}
	h.c.RegistryBucketChanged(h.ctx, &apitypes.RegistryBucket{State: "healthy"})
	h.openAlert(registryBucketAlertKey, false)
}
