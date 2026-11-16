package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	k.srv = httptest.NewServer(k.handler(h))
	h.t.Cleanup(k.srv.Close)
	if blocked {
		// Runs before srv.Close, which waits for handlers.
		h.t.Cleanup(func() { close(k.block) })
	}
	return k
}

// handler records the lines posted to it, once k.block is closed.
func (k *drainReceiver) handler(h *harness) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	})
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
// container when the drain is added and from a new release's container
// when it starts. A restarted container: TestLogDrainFollowsQuickRestart.
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
	want := drainLineJSON{Stream: "stdout", Text: `GET / "200"`, Project: "shop", Environment: "production", Service: "web", Release: 1}
	if got.Time.IsZero() || got.Text != want.Text || got.Stream != want.Stream || got.Project != want.Project ||
		got.Environment != want.Environment || got.Service != want.Service || got.Release != want.Release {
		t.Fatalf("line = %+v, want %+v", got, want)
	}
	if hdr.Get("X-Ingest-Key") != "s3cret" || hdr.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("headers = %v", hdr)
	}

	// A new release's container is followed from its start.
	h.deploy(web, "nginx:2")
	h.rt.AddLogs(h.ctr(web, 2), "release 2 up")
	eventually(t, "release 2's line", func() bool { return len(k.texts()) == 3 })
	k.mu.Lock()
	r2 := k.lines[2]
	k.mu.Unlock()
	if r2.Release != 2 {
		t.Fatalf("release 2's line = %+v", r2)
	}
	if texts := k.texts(); !slices.Equal(texts, []string{"listening on :80", `GET / "200"`, "release 2 up"}) {
		t.Fatalf("received %q", texts)
	}
	// The receiver has the lines before its answer reaches the sender,
	// which counts them after it.
	var list []apitypes.LogDrain
	eventually(t, "3 lines counted as sent", func() bool {
		list, _ = h.c.ListLogDrains(h.ctx, testSystem, EnvRef{"shop", "production"})
		return len(list) == 1 && list[0].Status.Sent == 3
	})
	if list[0].Status.Dropped != 0 || list[0].Status.LastSentAt == nil {
		t.Fatalf("drains = %+v", list)
	}

	// Without drains, nothing is drained; containers are still followed,
	// for their errors (ADR 0062).
	if err := h.c.RemoveLogDrain(h.ctx, testSystem, EnvRef{"shop", "production"}, d.ID); err != nil {
		t.Fatal(err)
	}
	if !h.followed(h.containerID(web, 2)) || h.drained("shop", "production") {
		t.Fatal("after its last drain, the environment should have followers and no drains")
	}
}

// drained reports whether an environment's followers send to drains.
func (h *harness) drained(project, env string) bool {
	h.t.Helper()
	es, err := h.c.envFor(h.ctx, testSystem, EnvRef{project, env}, apitypes.ScopeRead, "testing")
	if err != nil {
		h.t.Fatal(err)
	}
	m := h.c.logDrains
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.envs[es.env.ID] != nil && m.envs[es.env.ID].drained()
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
	h.c.drainContainerEvent(h.c.localID, node.ContainerEvent{ContainerID: ctr, Action: "start", Labels: c.Labels, Time: time.Now()})

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

// followed reports whether a container's output is being followed.
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
		// Private networks: the app firewall doesn't cover the control plane.
		{Kind: apitypes.DrainHTTP, URL: "http://10.0.0.5/ingest"},
		{Kind: apitypes.DrainLoki, URL: "http://100.64.1.1:3100"},
		{Kind: apitypes.DrainSyslog, URL: "tcp://[fd00::1]:514"},
		{Kind: apitypes.DrainSyslog, URL: "udp://172.17.0.2"},
	} {
		if _, err := h.c.AddLogDrain(h.ctx, testSystem, EnvRef{"shop", "production"}, req); CodeOf(err) != apitypes.CodeValidation {
			t.Errorf("drain to %s: %v", req.URL, err)
		}
	}

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

// A name that resolves to a private address is refused when connecting,
// for drains, unless apps may reach it; alert channels, which only
// operators configure, may still reach the LAN.
func TestLogDrainGuardRefusesPrivateAddresses(t *testing.T) {
	drains := newDrainManager([]netip.Prefix{netip.MustParsePrefix("10.9.0.0/16")})
	for _, addr := range []string{"10.0.0.5:80", "192.168.1.1:443", "100.64.1.1:3100", "[fd00::1]:514", "[::ffff:10.0.0.5]:80"} {
		if err := drains.guard.control("tcp", addr, nil); err == nil || !strings.Contains(err.Error(), "private address") {
			t.Errorf("drain connection to %s: %v", addr, err)
		}
	}
	if err := drains.guard.control("tcp", "203.0.113.7:443", nil); err != nil {
		t.Errorf("drain connection to a public address: %v", err)
	}
	// What the operator lets apps reach (`[network] allow_private`).
	if err := drains.guard.control("tcp", "10.9.4.2:3100", nil); err != nil {
		t.Errorf("drain connection to an allowed private address: %v", err)
	}
	alerts := egressGuard{what: "alerts"}
	if err := alerts.control("tcp", "192.168.1.1:443", nil); err != nil {
		t.Errorf("alert connection to the LAN: %v", err)
	}
}

// A drain that answers with an error shows its status, never its body:
// that would let whoever adds a drain read what a server says.
func TestLogDrainErrorHidesTheBody(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{})
	h.c.AllowLoopbackLogDrains(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "internal-secret")
	}))
	defer srv.Close()
	d := h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainHTTP, URL: srv.URL})
	res, err := h.c.TestLogDrain(h.ctx, testSystem, EnvRef{"shop", "production"}, d.ID)
	if err != nil || res.OK || !strings.Contains(res.Error, "403") || strings.Contains(res.Error, "internal-secret") {
		t.Fatalf("test of a failing drain = %+v, %v", res, err)
	}
}

// Readers see an environment's drains; changing or testing them takes an
// admin, and a token for another environment sees nothing.
func TestLogDrainPermissions(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{})
	d := h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainSyslog, URL: "udp://203.0.113.20"})
	if d.Target != "udp://203.0.113.20:514" {
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
		if len(f) != 8 || f[0] != "<14>1" || f[2] != "shop.production" || f[3] != "web" || f[4] != "-" || f[5] != "stdout" || f[6] != "-" || f[7] != text {
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

	src := newLogSource("shop", "production", "web", 1)
	msg := string(appendSyslog(nil, drainRecord{Time: time.Unix(0, 0), Stream: "stderr", Text: "boom", src: src}))
	if want := "<11>1 1970-01-01T00:00:00.000000Z shop.production web - stderr - boom"; msg != want {
		t.Fatalf("stderr message = %q, want %q", msg, want)
	}
}

// Loki gets one stream per service and output stream, labelled, with its
// lines in order and nanosecond timestamps.
func TestLokiPayload(t *testing.T) {
	web := newLogSource("shop", "production", "web", 3)
	worker := newLogSource("shop", "production", "worker", 7)
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
	src := newLogSource("shop", "production", "web", 1)
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

// drainStatus is a drain's status, by ID.
func (h *harness) drainStatus(id int64) apitypes.LogDrainStatus {
	h.t.Helper()
	list, err := h.c.ListLogDrains(h.ctx, testSystem, EnvRef{"shop", "production"})
	if err != nil {
		h.t.Fatal(err)
	}
	for _, d := range list {
		if d.ID == id {
			return d.Status
		}
	}
	h.t.Fatalf("no log drain %d", id)
	return apitypes.LogDrainStatus{}
}

// readSyslogFrame reads one octet-counted syslog message and returns its
// text (the part after the header).
func readSyslogFrame(t *testing.T, r *bufio.Reader) string {
	t.Helper()
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
	if len(f) != 8 {
		t.Fatalf("message = %q", msg)
	}
	return f[7]
}

// A tcp syslog drain whose server closed the connection between batches
// dials again rather than writing the next batch into the closed one,
// which loses it; a server that is down shows as the drain's error until
// it is back.
func TestSyslogDrainRedials(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "nginx:1")
	h.c.AllowLoopbackLogDrains(true)
	listen := func(addr string) *net.TCPListener {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Skipf("listen on %s: %v", addr, err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		return ln.(*net.TCPListener)
	}
	ln := listen("127.0.0.1:0")
	addr := ln.Addr().String()
	d := h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainSyslog, URL: "tcp://" + addr})
	// accept reads one message from a new connection, then closes it, as
	// a server restarting or dropping idle connections does.
	accept := func(want string) {
		t.Helper()
		_ = ln.SetDeadline(time.Now().Add(5 * time.Second))
		conn, err := ln.Accept()
		if err != nil {
			t.Fatalf("waiting for %q: %v", want, err)
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if got := readSyslogFrame(t, bufio.NewReader(conn)); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	h.rt.AddLogs(h.ctr(web, 1), "first")
	accept("first")
	eventually(t, "the first line counted", func() bool { return h.drainStatus(d.ID).Sent == 1 })
	h.rt.AddLogs(h.ctr(web, 1), "second")
	accept("second")
	eventually(t, "the second line counted", func() bool { return h.drainStatus(d.ID).Sent == 2 })

	// The server goes away: the drain fails and says so, then recovers.
	_ = ln.Close()
	h.rt.AddLogs(h.ctr(web, 1), "third")
	eventually(t, "the error", func() bool { return h.drainStatus(d.ID).LastError != "" })
	ln = listen(addr)
	accept("third")
	eventually(t, "the error to clear", func() bool { st := h.drainStatus(d.ID); return st.LastError == "" && st.Sent == 3 })
}

// stuckReceiver is an http drain's endpoint that never answers: each
// request waits until the sender gives up on it, and counts.
type stuckReceiver struct {
	srv       *httptest.Server
	in, ended atomic.Int32
}

func newStuckReceiver(h *harness) *stuckReceiver {
	h.c.AllowLoopbackLogDrains(true)
	k := &stuckReceiver{}
	release := make(chan struct{})
	k.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the body first: the server only notices the sender giving
		// up (r.Context ending) once it reads the connection again.
		_, _ = io.Copy(io.Discard, r.Body)
		k.in.Add(1)
		select {
		case <-r.Context().Done():
			k.ended.Add(1)
		case <-release:
		}
	}))
	h.t.Cleanup(k.srv.Close)
	h.t.Cleanup(func() { close(release) }) // before srv.Close, which waits for handlers
	return k
}

// A drain whose sink never answers holds up neither the followers nor
// the environment's other drains: those keep receiving while it drops
// and counts. Removing it ends its send in flight, and so does closing
// the daemon, without waiting for drainSendTimeout.
func TestLogDrainStuckSinkHoldsUpNothing(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "nginx:1")
	stuck := newStuckReceiver(h)
	ok := newDrainReceiver(h, false)
	s := h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainHTTP, Name: "stuck", URL: stuck.srv.URL})
	h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainHTTP, Name: "ok", URL: ok.srv.URL})

	h.rt.AddLogs(h.ctr(web, 1), "first")
	eventually(t, "the healthy drain's first line", func() bool { return slices.Contains(ok.texts(), "first") })
	eventually(t, "the stuck send", func() bool { return stuck.in.Load() == 1 })
	flood := make([]string, drainQueueLines+drainBatchLines)
	for i := range flood {
		flood[i] = "line " + strconv.Itoa(i)
	}
	h.rt.AddLogs(h.ctr(web, 1), flood...)
	h.rt.AddLogs(h.ctr(web, 1), "after the flood")
	eventually(t, "the healthy drain's line after the flood", func() bool { return slices.Contains(ok.texts(), "after the flood") })
	if st := h.drainStatus(s.ID); st.Dropped == 0 || st.Sent != 0 {
		t.Fatalf("stuck drain = %+v", st)
	}

	start := time.Now()
	if err := h.c.RemoveLogDrain(h.ctx, testSystem, EnvRef{"shop", "production"}, s.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the stuck send to end", func() bool { return stuck.ended.Load() == 1 })
	if d := time.Since(start); d > drainSendTimeout/3 {
		t.Fatalf("removing a stuck drain took %s", d)
	}

	// Closing the daemon ends a stuck send too.
	stuck2 := newStuckReceiver(h)
	h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainHTTP, Name: "stuck2", URL: stuck2.srv.URL})
	h.rt.AddLogs(h.ctr(web, 1), "last")
	eventually(t, "the second stuck send", func() bool { return stuck2.in.Load() == 1 })
	start = time.Now()
	h.restart()
	if d := time.Since(start); d > drainSendTimeout/3 {
		t.Fatalf("closing with a stuck send took %s", d)
	}
	eventually(t, "the second stuck send to end", func() bool { return stuck2.ended.Load() == 1 })
}

// A drain whose sink refuses connections backs off and shows the error;
// once the sink is back, the queued lines go through and the error
// clears.
func TestLogDrainRecoversWhenTheSinkComesBack(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "nginx:1")
	h.c.AllowLoopbackLogDrains(true)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	d := h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainHTTP, URL: "http://" + addr})
	h.rt.AddLogs(h.ctr(web, 1), "while down")
	eventually(t, "the error", func() bool { return strings.Contains(h.drainStatus(d.ID).LastError, "refused") })

	if ln, err = net.Listen("tcp", addr); err != nil {
		t.Skipf("the port was taken meanwhile: %v", err)
	}
	k := &drainReceiver{block: make(chan struct{})}
	close(k.block)
	k.srv = httptest.NewUnstartedServer(k.handler(h))
	_ = k.srv.Listener.Close()
	k.srv.Listener = ln
	k.srv.Start()
	t.Cleanup(k.srv.Close)
	eventually(t, "the queued line", func() bool { return slices.Equal(k.texts(), []string{"while down"}) })
	eventually(t, "the error to clear", func() bool {
		st := h.drainStatus(d.ID)
		return st.LastError == "" && st.LastErrorAt == nil && st.Sent == 1
	})
}

// Every path a container's lines take to a drain masks its release's
// secret values: the follower started when the drain is added, the one a
// container start event starts, and the one a server resync starts. A
// new release with a changed value is masked with its own values.
func TestLogDrainMasksOnEveryPath(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.set(web.Env(), "web", map[string]string{"STRIPE_KEY": "sk_live_old"}, false)
	h.deploy(web, "nginx:1")
	k := newDrainReceiver(h, false)
	h.addDrain(apitypes.AddLogDrainRequest{Kind: apitypes.DrainHTTP, URL: k.srv.URL})
	h.rt.AddLogs(h.ctr(web, 1), "added sk_live_old")
	eventually(t, "the line", func() bool { return len(k.texts()) == 1 })

	// A container start event (a restart).
	ctr := h.containerID(web, 1)
	h.rt.Emit(node.ContainerEvent{ContainerID: ctr, Action: "die", ExitCode: 1})
	eventually(t, "the follower to end", func() bool { return !h.followed(ctr) })
	h.rt.Emit(node.ContainerEvent{ContainerID: ctr, Action: "start"})
	h.rt.AddLogs(h.ctr(web, 1), "restarted sk_live_old")
	eventually(t, "the line after the restart", func() bool { return len(k.texts()) == 2 })

	// A resync: the container runs again without an event dock saw.
	h.rt.Emit(node.ContainerEvent{ContainerID: ctr, Action: "die", ExitCode: 1})
	eventually(t, "the follower to end", func() bool { return !h.followed(ctr) })
	c, err := h.rt.Inspect(h.ctx, ctr)
	if err != nil {
		t.Fatal(err)
	}
	c.State = node.StateRunning
	h.rt.AddContainer(c)
	h.c.followServer(h.ctx, h.c.localID)
	h.rt.AddLogs(h.ctr(web, 1), "resynced sk_live_old")
	eventually(t, "the line after the resync", func() bool { return len(k.texts()) == 3 })

	// A new release with a new value.
	h.set(web.Env(), "web", map[string]string{"STRIPE_KEY": "sk_live_new"}, false)
	h.deploy(web, "nginx:2")
	h.rt.AddLogs(h.ctr(web, 2), "new sk_live_new, old sk_live_old")
	eventually(t, "release 2's line", func() bool { return len(k.texts()) == 4 })

	want := []string{"added " + secretMask, "restarted " + secretMask, "resynced " + secretMask, "new " + secretMask + ", old sk_live_old"}
	if got := k.texts(); !slices.Equal(got, want) {
		t.Fatalf("drained %q, want %q", got, want)
	}
}

// A syslog write blocked on a server that stopped reading ends as soon
// as the delivery's context does, not at drainSendTimeout: removing the
// drain or closing the daemon would otherwise wait for it.
func TestSyslogDrainStuckWriteEndsOnCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	srv, err := ln.Accept() // never read
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	_ = client.(*net.TCPConn).SetWriteBuffer(4096)
	_ = srv.(*net.TCPConn).SetReadBuffer(4096)

	d := &drain{ctx: t.Context(), cfg: drainConfig{URL: "tcp://" + ln.Addr().String()}}
	d.conn.conn, d.conn.used = client, time.Now()
	src := newLogSource("shop", "production", "web", 1)
	batch := make([]drainRecord, 64) // 4 MB: far past both buffers
	for i := range batch {
		batch[i] = drainRecord{Time: time.Now(), Stream: "stdout", Text: strings.Repeat("x", 64<<10), src: src}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan error, 1)
	go func() { done <- newDrainManager(nil).sendSyslog(ctx, d, batch, new(bytes.Buffer)) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a write to a server that stopped reading succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the write didn't end with its context")
	}
}
