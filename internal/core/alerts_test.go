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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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

func (h *harness) channel(req apitypes.CreateAlertChannelRequest) apitypes.CreatedAlertChannel {
	h.t.Helper()
	ch, err := h.c.CreateAlertChannel(h.ctx, testSystem, req)
	if err != nil {
		h.t.Fatal(err)
	}
	return ch
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
	h.deploy(web, "app:3")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(h.openAlert("deploy:service:1", true).Message, "Release 3") {
		if time.Now().After(deadline) {
			t.Fatal("the open alert should follow the newest failure")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if all, _ := h.c.ListAlerts(h.ctx, testSystem, true, 0, 0); len(all) != 1 || all[0].ID != a.ID {
		t.Fatalf("a failure while the alert is open raised another: %+v", all)
	}

	h.rt.FailPull(nil)
	h.deploy(web, "app:4")
	h.openAlert("deploy:service:1", false)
	all, _ := h.c.ListAlerts(h.ctx, testSystem, true, 0, 0)
	if len(all) != 1 || all[0].ResolvedAt == nil {
		t.Fatalf("alerts after a good deploy = %+v", all)
	}
	if open, _ := h.c.ListAlerts(h.ctx, testSystem, false, 0, 0); len(open) != 0 {
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
	created := h.channel(apitypes.CreateAlertChannelRequest{Name: "ops", Kind: apitypes.ChannelWebhook, URL: k.srv.URL + "/hook?token=abc"})
	if len(created.Secret) != 64 || created.Channel.Target != k.srv.URL {
		t.Fatalf("created = %+v (the secret is generated, the target has no path)", created)
	}
	srv := h.c.local
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

// A known signature, so receivers in other languages can check theirs.
func TestSignWebhook(t *testing.T) {
	got := signWebhook("s3cret", time.Unix(1700000000, 0), []byte(`{"event":"test"}`))
	want := "t=1700000000,v1=" + func() string {
		m := hmac.New(sha256.New, []byte("s3cret"))
		m.Write([]byte(`1700000000.{"event":"test"}`))
		return hex.EncodeToString(m.Sum(nil))
	}()
	if got != want {
		t.Fatalf("signature = %s, want %s", got, want)
	}
}

// A delivery is signed when it is attempted, so a retry long after the
// alert still passes a receiver's timestamp check.
func TestWebhookSignedAtAttempt(t *testing.T) {
	h := newHarness(t)
	k := newReceiver(h)
	created := h.channel(apitypes.CreateAlertChannelRequest{Name: "hook", Kind: apitypes.ChannelWebhook, URL: k.srv.URL})
	ch, err := h.store.GetAlertChannel(h.ctx, "hook")
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

// A backup that failed with nothing more to say still reads as a sentence.
func TestBackupFailedMessage(t *testing.T) {
	for _, reason := range []string{"restic reported no snapshot", "exit code 1: Fatal: no key found.", "restic reported no snapshot: "} {
		m := backupFailed("p", "e", "s", 1, reason, 0).Message
		if strings.HasSuffix(m, ":.") || strings.HasSuffix(m, "..") || !strings.HasSuffix(m, ".") {
			t.Errorf("message for %q = %q", reason, m)
		}
	}
	if got := withOutput("\n{\"message_type\":\"status\"}\n"); got != "" {
		t.Errorf("output with nothing to show = %q", got)
	}
}

// Alert text can quote build output; Slack must show it, not act on it.
func TestSlackTextIsEscaped(t *testing.T) {
	got := slackText(apitypes.AlertNotification{Event: "opened", Alert: apitypes.Alert{
		Severity: apitypes.SeverityWarning, Title: "Deploy of a/b/c failed", Message: "<!channel> <https://evil.example|log in> & retry",
	}})
	if want := "[warning] Deploy of a/b/c failed\n&lt;!channel&gt; &lt;https://evil.example|log in&gt; &amp; retry"; got != want {
		t.Fatalf("slack text = %q", got)
	}
}

// Channels never reach the server itself (its loopback-only internal
// listener and registry) or cloud metadata: literal addresses are refused
// when added, names when they resolve.
func TestChannelsNeverReachTheServer(t *testing.T) {
	h := newHarness(t)
	for _, u := range []string{
		"http://127.0.0.1:7071/internal/caddy/ask", "http://localhost:5000/v2/", "http://[::1]/", "http://0.0.0.0/",
		"http://169.254.169.254/latest/meta-data", "http://[fd00:ec2::254]/", "http://[::ffff:127.0.0.1]/",
	} {
		if _, err := h.c.CreateAlertChannel(h.ctx, testSystem, apitypes.CreateAlertChannelRequest{Name: "x", Kind: apitypes.ChannelWebhook, URL: u}); CodeOf(err) != apitypes.CodeValidation {
			t.Errorf("channel to %s: %v", u, err)
		}
	}
	h.channel(apitypes.CreateAlertChannelRequest{Name: "lan", Kind: apitypes.ChannelSlack, URL: "http://192.168.1.20/hooks/dock"})

	// A name that resolves to loopback is refused when dialing.
	k := newReceiver(h)
	h.channel(apitypes.CreateAlertChannelRequest{Name: "self", Kind: apitypes.ChannelSlack, URL: k.srv.URL})
	h.c.AllowLoopbackAlertChannels(false)
	res, err := h.c.TestAlertChannel(h.ctx, testSystem, "self")
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

// Any event can carry an alert signal (a failed backup), and a signal
// resolves it again.
func TestAlertSignalOnEvent(t *testing.T) {
	h := newHarness(t)
	sig := alertSignal{
		Key: "backup:shop/production/db", Kind: apitypes.AlertBackupFailed, Severity: apitypes.SeverityCritical,
		Title: "Backup of shop/production/db failed", Message: "pg_dump: connection refused", Project: "shop",
	}
	h.c.bus.publish(busEvent{ev: apitypes.Event{Type: apitypes.EventAudit}, alert: &sig})
	if a := h.openAlert(sig.Key, true); a.Kind != "backup_failed" || a.Message != sig.Message {
		t.Fatalf("alert = %+v", a)
	}
	h.c.signalAlert(h.ctx, alertSignal{Key: sig.Key, Resolve: true})
	h.openAlert(sig.Key, false)
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
	h.channel(apitypes.CreateAlertChannelRequest{Name: "flaky", Kind: apitypes.ChannelSlack, URL: k.srv.URL})
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
			chs, err := h.c.ListAlertChannels(h.ctx, testSystem)
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
	test, err := h.c.TestAlertChannel(h.ctx, testSystem, "flaky")
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
	h.channel(apitypes.CreateAlertChannelRequest{Name: "pager", Kind: apitypes.ChannelWebhook, URL: k.srv.URL, Secret: "x", MinSeverity: apitypes.SeverityCritical, RenotifyMinutes: &one})
	t0 := time.Now()
	h.c.raiseAlert(h.ctx, alertSignal{Key: "w", Kind: "test", Severity: apitypes.SeverityWarning, Title: "minor"}, t0)
	h.c.raiseAlert(h.ctx, alertSignal{Key: "c", Kind: "test", Severity: apitypes.SeverityCritical, Title: "major"}, t0)
	k.events(t, 1)
	h.c.remind(h.ctx, t0.Add(30*time.Second))
	h.c.remind(h.ctx, t0.Add(61*time.Second))
	evs := k.events(t, 2)
	time.Sleep(20 * time.Millisecond)
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.got) != 2 || strings.Join(evs, ",") != "opened,reminder" || k.got[0].Alert.Title != "major" || k.got[1].Alert.Title != "major" {
		t.Fatalf("deliveries = %+v", k.got)
	}
}

// Alerts and channels are for unlimited admins; channel secrets are
// sealed in the DB and never come back.
func TestAlertPermissionsAndSecrets(t *testing.T) {
	h := newHarness(t)
	for _, a := range []Actor{
		{Kind: "token", OrgID: 1, Name: "ci", Scope: apitypes.ScopeDeploy},
		{Kind: "token", OrgID: 1, Name: "ops", Scope: apitypes.ScopeAdmin, ProjectID: 1, ProjectName: "shop"},
	} {
		if _, err := h.c.ListAlerts(h.ctx, a, false, 0, 0); CodeOf(err) != apitypes.CodeForbidden {
			t.Fatalf("%s listed alerts: %v", a, err)
		}
		if _, err := h.c.CreateAlertChannel(h.ctx, a, apitypes.CreateAlertChannelRequest{Name: "x", Kind: apitypes.ChannelSlack, URL: "https://hooks.example/x"}); CodeOf(err) != apitypes.CodeForbidden {
			t.Fatalf("%s added a channel: %v", a, err)
		}
		if _, err := h.c.ListAlertChannels(h.ctx, a); CodeOf(err) != apitypes.CodeForbidden {
			t.Fatalf("%s listed channels: %v", a, err)
		}
	}

	h.channel(apitypes.CreateAlertChannelRequest{Name: "mail", Kind: apitypes.ChannelEmail, SMTP: &apitypes.SMTPSettings{
		Host: "smtp.example", Username: "u", Password: "hunter22", From: "dock@example.com", To: []string{"ops@example.com"},
	}})
	h.channel(apitypes.CreateAlertChannelRequest{Name: "slack", Kind: apitypes.ChannelSlack, URL: "https://hooks.slack.com/services/T0/B0/XYZSECRET"})
	chs, err := h.c.ListAlertChannels(h.ctx, testSystem)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(chs)
	if bytes.Contains(out, []byte("XYZSECRET")) || bytes.Contains(out, []byte("hunter22")) {
		t.Fatalf("channels leak secrets: %s", out)
	}
	rows, _ := h.store.ListAlertChannels(h.ctx)
	for _, r := range rows {
		if bytes.Contains(r.Config, []byte("XYZSECRET")) || bytes.Contains(r.Config, []byte("hunter22")) {
			t.Fatalf("channel %s stored in plain text", r.Name)
		}
	}
	for _, bad := range []apitypes.CreateAlertChannelRequest{
		{Name: "a", Kind: apitypes.ChannelWebhook, URL: "ftp://x"},
		{Name: "b", Kind: apitypes.ChannelSlack, URL: "https://x", Secret: "s"},
		{Name: "c", Kind: apitypes.ChannelEmail, SMTP: &apitypes.SMTPSettings{Host: "h", From: "x", To: []string{"y@z"}}},
		{Name: "slack", Kind: apitypes.ChannelSlack, URL: "https://x"},
	} {
		if _, err := h.c.CreateAlertChannel(h.ctx, testSystem, bad); err == nil {
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
	h.openAlert("disk:"+itoa(h.c.local.ID), true)
}
