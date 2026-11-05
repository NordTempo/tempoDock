package core

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// drainLineJSON is one line an http drain receives.
type drainLineJSON struct {
	Time        time.Time `json:"time"`
	Stream      string    `json:"stream"`
	Text        string    `json:"text"`
	Project     string    `json:"project"`
	Environment string    `json:"environment"`
	Service     string    `json:"service"`
	Release     int64     `json:"release"`
	Server      string    `json:"server"`
}

// drainReceiver is an http drain's endpoint on 127.0.0.1; block holds
// every request until it is closed.
type drainReceiver struct {
	srv     *httptest.Server
	mu      sync.Mutex
	lines   []drainLineJSON
	headers []http.Header
	block   chan struct{}
}

func newDrainReceiver(h *harness, blocked bool) *drainReceiver {
	h.c.AllowLoopbackLogDrains(true)
	k := &drainReceiver{block: make(chan struct{})}
	if !blocked {
		close(k.block)
	}
	k.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-k.block
		// A delivery cut short (the drain gave up or the test ended while
		// the request was held) was never delivered: skip its partial body.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		sc := bufio.NewScanner(bytes.NewReader(body))
		k.mu.Lock()
		defer k.mu.Unlock()
		k.headers = append(k.headers, r.Header.Clone())
		for sc.Scan() {
			var l drainLineJSON
			if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
				h.t.Errorf("drain line %q: %v", sc.Text(), err)
			}
			k.lines = append(k.lines, l)
		}
	}))
	h.t.Cleanup(k.srv.Close)
	if blocked {
		// Runs before srv.Close, which waits for handlers.
		h.t.Cleanup(func() { close(k.block) })
	}
	return k
}

// texts returns the texts received so far.
func (k *drainReceiver) texts() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]string, len(k.lines))
	for i, l := range k.lines {
		out[i] = l.Text
	}
	return out
}

func (h *harness) addDrain(req apitypes.AddLogDrainRequest) apitypes.LogDrain {
	h.t.Helper()
	d, err := h.c.AddLogDrain(h.ctx, testSystem, EnvRef{"shop", "production"}, req)
	if err != nil {
		h.t.Fatal(err)
	}
	return d
}

// An http drain gets every line of the environment's running services as
// JSON lines labelled with where they come from, from the running
// container when the drain is added, from a new release's container when
// it starts, and after a restart without repeating lines.
func TestLogDrainDeliversLabelledLines(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "nginx:1")
	k := newDrainReceiver(h, false)
	d := h.addDrain(apitypes.AddLogDrainRequest{
		Kind: apitypes.DrainHTTP, URL: k.srv.URL + "/ingest", Headers: map[string]string{"X-Ingest-Key": "s3cret"},
	})
	if d.Name != "http" || d.Target != k.srv.URL {
		t.Fatalf("drain = %+v", d)
	}

	h.rt.AddLogs(h.ctr(web, 1), "listening on :80", `GET / "200"`)
	eventually(t, "two lines", func() bool { return len(k.texts()) == 2 })
	k.mu.Lock()
	got, hdr := k.lines[1], k.headers[0]
	k.mu.Unlock()
	want := drainLineJSON{Stream: "stdout", Text: `GET / "200"`, Project: "shop", Environment: "production", Service: "web", Release: 1, Server: h.c.local.Name}
	if got.Time.IsZero() || got.Text != want.Text || got.Stream != want.Stream || got.Project != want.Project ||
		got.Environment != want.Environment || got.Service != want.Service || got.Release != want.Release || got.Server != want.Server {
		t.Fatalf("line = %+v, want %+v", got, want)
	}
	if hdr.Get("X-Ingest-Key") != "s3cret" || hdr.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("headers = %v", hdr)
	}

	// The container restarts: lines resume after the last one sent.
	ctr := h.containerID(web, 1)
	h.rt.Emit(node.ContainerEvent{ContainerID: ctr, Action: "die", ExitCode: 1})
	eventually(t, "the follower to end", func() bool { return h.followed(ctr) == false })
	h.rt.Emit(node.ContainerEvent{ContainerID: ctr, Action: "start"})
	h.rt.AddLogs(h.ctr(web, 1), "restarted")
	eventually(t, "the line after the restart", func() bool { return len(k.texts()) == 3 })

	// A new release's container is followed from its start.
	h.deploy(web, "nginx:2")
	h.rt.AddLogs(h.ctr(web, 2), "release 2 up")
	eventually(t, "release 2's line", func() bool { return len(k.texts()) == 4 })
	k.mu.Lock()
	r2 := k.lines[3]
	k.mu.Unlock()
	if r2.Release != 2 {
		t.Fatalf("release 2's line = %+v", r2)
	}
	if texts := k.texts(); !slices.Equal(texts, []string{"listening on :80", `GET / "200"`, "restarted", "release 2 up"}) {
		t.Fatalf("received %q", texts)
	}
	// The receiver has the lines before its answer reaches the sender,
	// which counts them after it.
	var list []apitypes.LogDrain
	eventually(t, "4 lines counted as sent", func() bool {
		list, _ = h.c.ListLogDrains(h.ctx, testSystem, EnvRef{"shop", "production"})
		return len(list) == 1 && list[0].Status.Sent == 4
	})
	if list[0].Status.Dropped != 0 || list[0].Status.LastSentAt == nil {
		t.Fatalf("drains = %+v", list)
	}

	// Without drains, nothing is followed.
	if err := h.c.RemoveLogDrain(h.ctx, testSystem, EnvRef{"shop", "production"}, d.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "followers to stop", func() bool { return h.followed(h.containerID(web, 2)) == false })
}

// A container that starts again before its old log stream has ended
// (the follower is still registered) is followed once that stream ends.
func TestLogDrainFollowsQuickRestart(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "nginx:1")
	k := newDrainReceiver(h, false)
	h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainHTTP, URL: k.srv.URL})
	ctr := h.containerID(web, 1)
	h.rt.AddLogs(h.ctr(web, 1), "first run")
	eventually(t, "the first line", func() bool { return len(k.texts()) == 1 })

	// The new run's start event arrives while the old follower runs.
	c, err := h.rt.Inspect(h.ctx, ctr)
	if err != nil {
		t.Fatal(err)
	}
	h.c.drainContainerEvent(h.c.local.ID, node.ContainerEvent{ContainerID: ctr, Action: "start", Labels: c.Labels, Time: time.Now()})

	// The old stream ends, and the container is running again (without
	// another event) before the old follower cleans up.
	m := h.c.logDrains
	m.mu.Lock()
	h.rt.Emit(node.ContainerEvent{ContainerID: ctr, Action: "die", ExitCode: 1})
	time.Sleep(100 * time.Millisecond)
	c.State = node.StateRunning
	h.rt.AddContainer(c)
	m.mu.Unlock()

	eventually(t, "the restarted container to be followed", func() bool { return h.followed(ctr) })
	h.rt.AddLogs(h.ctr(web, 1), "second run")
	eventually(t, "the line after the restart", func() bool { return len(k.texts()) == 2 })
	if texts := k.texts(); !slices.Equal(texts, []string{"first run", "second run"}) {
		t.Fatalf("received %q", texts)
	}
}

// A release's secret values are masked in what drains get, as `dock logs`
// masks them for those who can't reveal them: a drain has no actor.
func TestLogDrainMasksSecrets(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.set(web.Env(), "web", map[string]string{"STRIPE_KEY": "sk_live_51abcdef"}, false)
	h.deploy(web, "nginx:1")
	k := newDrainReceiver(h, false)
	h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainHTTP, URL: k.srv.URL})
	h.rt.AddLogs(h.ctr(web, 1), "charging with sk_live_51abcdef now")
	eventually(t, "the line", func() bool { return len(k.texts()) == 1 })
	if got := k.texts()[0]; got != "charging with "+secretMask+" now" {
		t.Fatalf("drained %q", got)
	}
}

// containerID is the ID of a service release's container.
func (h *harness) containerID(ref ServiceRef, number int64) string {
	h.t.Helper()
	ss, err := h.c.serviceFor(h.ctx, testSystem, ref, apitypes.ScopeRead, "testing")
	if err != nil {
		h.t.Fatal(err)
	}
	r, err := h.store.GetReleaseByNumber(h.ctx, store.GetReleaseByNumberParams{ServiceID: ss.svc.ID, Number: number})
	if err != nil {
		h.t.Fatal(err)
	}
	return r.ContainerID
}

// followed reports whether a container's output is being drained.
func (h *harness) followed(id string) bool {
	m := h.c.logDrains
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.follows[id] != nil
}

// A sink that doesn't answer never holds up the follower: once the queue
// is full, new lines are dropped and counted.
func TestLogDrainDropsWhenTheSinkIsStuck(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "nginx:1")
	k := newDrainReceiver(h, true)
	h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainHTTP, URL: k.srv.URL})

	lines := make([]string, drainQueueLines+drainBatchLines+500)
	for i := range lines {
		lines[i] = "line " + strconv.Itoa(i)
	}
	h.rt.AddLogs(h.ctr(web, 1), lines...)
	eventually(t, "lines to be dropped", func() bool {
		list, err := h.c.ListLogDrains(h.ctx, testSystem, EnvRef{"shop", "production"})
		return err == nil && list[0].Status.Dropped >= 500
	})
}

// Drains never reach the server itself or cloud metadata: literal
// addresses are refused when added, names when they connect, for every
// kind of drain.
func TestLogDrainsNeverReachTheServer(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{})
	for _, req := range []apitypes.AddLogDrainRequest{
		{Kind: apitypes.DrainHTTP, URL: "http://127.0.0.1:7071/internal/caddy/ask"},
		{Kind: apitypes.DrainHTTP, URL: "http://169.254.169.254/latest/meta-data"},
		{Kind: apitypes.DrainLoki, URL: "http://localhost:3100"},
		{Kind: apitypes.DrainSyslog, URL: "tcp://127.0.0.1:514"},
		{Kind: apitypes.DrainSyslog, URL: "udp://[::1]"},
		{Kind: apitypes.DrainSyslog, URL: "tls://[fd00:ec2::254]:6514"},
	} {
		if _, err := h.c.AddLogDrain(h.ctx, testSystem, EnvRef{"shop", "production"}, req); CodeOf(err) != apitypes.CodeValidation {
			t.Errorf("drain to %s: %v", req.URL, err)
		}
	}
	h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainHTTP, Name: "lan", URL: "http://192.168.1.20/ingest"})

	// Added while loopback was allowed (as a name would resolve), refused
	// when connecting.
	k := newDrainReceiver(h, false)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	ids := []int64{
		h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainHTTP, Name: "self", URL: k.srv.URL}).ID,
		h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainSyslog, Name: "self-syslog", URL: "tcp://" + ln.Addr().String()}).ID,
	}
	h.c.AllowLoopbackLogDrains(false)
	for _, id := range ids {
		res, err := h.c.TestLogDrain(h.ctx, testSystem, EnvRef{"shop", "production"}, id)
		if err != nil || res.OK || !strings.Contains(res.Error, "this server itself") || res.Drain.Status.LastError == "" {
			t.Fatalf("test of a loopback drain = %+v, %v", res, err)
		}
	}
	if len(k.texts()) != 0 {
		t.Fatalf("the loopback receiver got %q", k.texts())
	}
}

// Readers see an environment's drains; changing or testing them takes an
// admin, and a token for another environment sees nothing.
func TestLogDrainPermissions(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{})
	d := h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainSyslog, URL: "udp://192.168.1.20"})
	if d.Target != "udp://192.168.1.20:514" {
		t.Fatalf("target = %q", d.Target)
	}
	env := EnvRef{"shop", "production"}
	reader := Actor{Kind: "token", OrgID: 1, Name: "ro", TokenID: 1, Scope: apitypes.ScopeRead}
	deployer := Actor{Kind: "token", OrgID: 1, Name: "ci", TokenID: 2, Scope: apitypes.ScopeDeploy}
	if list, err := h.c.ListLogDrains(h.ctx, reader, env); err != nil || len(list) != 1 {
		t.Fatalf("reader's list = %+v, %v", list, err)
	}
	for _, a := range []Actor{reader, deployer} {
		if _, err := h.c.AddLogDrain(h.ctx, a, env, apitypes.AddLogDrainRequest{Kind: apitypes.DrainHTTP, URL: "https://logs.example.com"}); CodeOf(err) != apitypes.CodeForbidden {
			t.Errorf("%s adds a drain: %v", a, err)
		}
		if err := h.c.RemoveLogDrain(h.ctx, a, env, d.ID); CodeOf(err) != apitypes.CodeForbidden {
			t.Errorf("%s removes a drain: %v", a, err)
		}
		if _, err := h.c.TestLogDrain(h.ctx, a, env, d.ID); CodeOf(err) != apitypes.CodeForbidden {
			t.Errorf("%s tests a drain: %v", a, err)
		}
	}
	other := Actor{Kind: "token", OrgID: 1, Name: "dev", Scope: apitypes.ScopeAdmin, ProjectID: 99, ProjectName: "blog"}
	if _, err := h.c.ListLogDrains(h.ctx, other, env); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("another project's token lists drains: %v", err)
	}
	if _, err := h.c.ListLogDrains(h.ctx, Actor{Kind: "token", OrgID: 2, Name: "x", Scope: apitypes.ScopeAdmin}, env); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("another org's token lists drains: %v", err)
	}
}

// A tcp syslog drain sends RFC 5424 messages framed by octet counting:
// APP-NAME the service, MSGID the stream, HOSTNAME the server.
func TestSyslogDrainFraming(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "nginx:1")
	h.c.AllowLoopbackLogDrains(true)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainSyslog, URL: "tcp://" + ln.Addr().String()})
	h.rt.AddLogs(h.ctr(web, 1), "first", "second line")

	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(conn)
	for _, text := range []string{"first", "second line"} {
		n, err := r.ReadString(' ')
		if err != nil {
			t.Fatal(err)
		}
		size, err := strconv.Atoi(strings.TrimSuffix(n, " "))
		if err != nil {
			t.Fatalf("frame length %q", n)
		}
		msg := make([]byte, size)
		if _, err := io.ReadFull(r, msg); err != nil {
			t.Fatal(err)
		}
		f := strings.SplitN(string(msg), " ", 8)
		if len(f) != 8 || f[0] != "<14>1" || f[2] != h.c.local.Name || f[3] != "web" || f[4] != "-" || f[5] != "stdout" || f[6] != "-" || f[7] != text {
			t.Fatalf("message = %q", msg)
		}
		if _, err := time.Parse(time.RFC3339Nano, f[1]); err != nil {
			t.Fatalf("timestamp %q: %v", f[1], err)
		}
	}
}

// A udp syslog drain sends a datagram per message; stderr is severity err.
func TestSyslogDrainUDP(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{})
	h.c.AllowLoopbackLogDrains(true)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()
	d := h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainSyslog, URL: "udp://" + pc.LocalAddr().String()})
	res, err := h.c.TestLogDrain(h.ctx, testSystem, EnvRef{"shop", "production"}, d.ID)
	if err != nil || !res.OK || res.Drain.Status.Sent != 1 {
		t.Fatalf("test = %+v, %v", res, err)
	}
	buf := make([]byte, 2048)
	_ = pc.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if msg := string(buf[:n]); !strings.HasPrefix(msg, "<14>1 ") || !strings.HasSuffix(msg, " dock - stdout - Test line from dock: log drain syslog works") {
		t.Fatalf("datagram = %q", msg)
	}

	src := newLogSource("shop", "production", "web", 1, "vps 1")
	msg := string(appendSyslog(nil, drainRecord{Time: time.Unix(0, 0), Stream: "stderr", Text: "boom", src: src}))
	if want := "<11>1 1970-01-01T00:00:00.000000Z vps_1 web - stderr - boom"; msg != want {
		t.Fatalf("stderr message = %q, want %q", msg, want)
	}
}

// Loki gets one stream per service and output stream, labelled, with its
// lines in order and nanosecond timestamps.
func TestLokiPayload(t *testing.T) {
	web := newLogSource("shop", "production", "web", 3, "vps-1")
	worker := newLogSource("shop", "production", "worker", 7, "vps-1")
	at := time.Unix(1700000000, 123)
	batch := []drainRecord{
		{Time: at, Stream: "stdout", Text: "a", src: web},
		{Time: at.Add(1), Stream: "stderr", Text: "b \"quoted\"", src: web},
		{Time: at.Add(2), Stream: "stdout", Text: "c", src: worker},
		{Time: at.Add(3), Stream: "stdout", Text: "d", src: web},
	}
	var got struct {
		Streams []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"`
		} `json:"streams"`
	}
	body := appendLoki(nil, batch)
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	type stream struct {
		labels string
		values [][2]string
	}
	var streams []stream
	for _, s := range got.Streams {
		streams = append(streams, stream{s.Stream["project"] + "/" + s.Stream["environment"] + "/" + s.Stream["service"] + "/" + s.Stream["stream"], s.Values})
	}
	ns := func(d int64) string { return strconv.FormatInt(at.UnixNano()+d, 10) }
	want := []stream{
		{"shop/production/web/stdout", [][2]string{{ns(0), "a"}, {ns(3), "d"}}},
		{"shop/production/web/stderr", [][2]string{{ns(1), "b \"quoted\""}}},
		{"shop/production/worker/stdout", [][2]string{{ns(2), "c"}}},
	}
	if len(streams) != len(want) {
		t.Fatalf("streams = %+v", streams)
	}
	for i := range want {
		if streams[i].labels != want[i].labels || !slices.Equal(streams[i].values, want[i].values) {
			t.Fatalf("stream %d = %+v, want %+v", i, streams[i], want[i])
		}
	}
}

// JSON lines are valid JSON whatever an app writes: quotes, control
// characters, invalid UTF-8.
func TestNDJSONEscaping(t *testing.T) {
	src := newLogSource("shop", "production", "web", 1, "vps-1")
	texts := []string{`say "hi"\n`, "tab\there\x01\x1f", "bad \xff utf-8", "ünïcode ✓", "</script>"}
	var batch []drainRecord
	for _, s := range texts {
		batch = append(batch, drainRecord{Time: time.Now(), Stream: "stdout", Text: s, src: src})
	}
	lines := strings.Split(strings.TrimSuffix(string(appendNDJSON(nil, batch)), "\n"), "\n")
	if len(lines) != len(texts) {
		t.Fatalf("%d lines for %d texts", len(lines), len(texts))
	}
	for i, l := range lines {
		var got drainLineJSON
		if err := json.Unmarshal([]byte(l), &got); err != nil {
			t.Fatalf("line %q: %v", l, err)
		}
		want, _ := json.Marshal(texts[i]) // encoding/json's view of the text
		var wantText string
		_ = json.Unmarshal(want, &wantText)
		if got.Text != wantText || got.Service != "web" || got.Release != 1 {
			t.Fatalf("line %d = %+v, want text %q", i, got, wantText)
		}
	}
}

// URLs are checked when a drain is added, and secrets never show.
func TestLogDrainSettings(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{})
	for _, req := range []apitypes.AddLogDrainRequest{
		{Kind: apitypes.DrainHTTP, URL: "ftp://logs.example.com"},
		{Kind: apitypes.DrainSyslog, URL: "https://logs.example.com"},
		{Kind: apitypes.DrainSyslog, URL: "tcp://logs.example.com:514", Token: "x"},
		{Kind: apitypes.DrainHTTP, URL: "https://logs.example.com", Headers: map[string]string{"Bad Header": "x"}},
		{Kind: apitypes.DrainHTTP, URL: "https://logs.example.com", Headers: map[string]string{"X-Key": "a\r\nInjected: 1"}},
		{Kind: "kafka", URL: "https://logs.example.com"},
	} {
		if _, err := h.c.AddLogDrain(h.ctx, testSystem, EnvRef{"shop", "production"}, req); CodeOf(err) != apitypes.CodeValidation {
			t.Errorf("%+v: %v", req, err)
		}
	}
	d := h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainLoki, URL: "https://user:pw@loki.example.com/", Token: "tok"})
	if d.Target != "https://loki.example.com" {
		t.Fatalf("target = %q", d.Target)
	}
	if got := h.c.logDrains.get(d.ID).cfg.URL; got != "https://user:pw@loki.example.com/loki/api/v1/push" {
		t.Fatalf("push URL = %q", got)
	}
	if _, err := h.c.AddLogDrain(h.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.AddLogDrainRequest{Kind: apitypes.DrainLoki, URL: "https://loki.example.com"}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("second drain named loki: %v", err)
	}
	// Drains survive a restart.
	h.restart()
	if got := h.c.logDrains.get(d.ID); got == nil || got.cfg.Token != "tok" {
		t.Fatalf("after a restart: %+v", got)
	}
}
