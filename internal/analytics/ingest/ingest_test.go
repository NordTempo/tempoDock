package ingest

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/analytics/spool"
	"github.com/NordTempo/tempoDock/internal/node"
)

const chromeMac = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// newIngest returns an ingest with today's salt and its spool.
func newIngest(t testing.TB) (*Ingest, *spool.Spool) {
	t.Helper()
	sp, err := spool.Open(t.TempDir(), spool.Options{})
	if err != nil {
		t.Fatal(err)
	}
	salts, err := OpenSalts("")
	if err != nil {
		t.Fatal(err)
	}
	if err := salts.Set(map[int64][]byte{Day(now): bytes.Repeat([]byte{1}, SaltBytes)}, now); err != nil {
		t.Fatal(err)
	}
	return &Ingest{Spool: sp, Salts: salts, Now: func() time.Time { return now }}, sp
}

// post sends a beacon as Caddy would forward it.
func post(in *Ingest, host, body, ua, ip string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, EventPath, strings.NewReader(body))
	r.Host = host
	r.Header.Set(TagHeader, "3.40.981")
	r.Header.Set(ClientIPHeader, ip)
	r.Header.Set("User-Agent", ua)
	w := httptest.NewRecorder()
	in.ServeHTTP(w, r)
	return w
}

// spooled reads back what the ingest appended.
func spooled(t *testing.T, sp *spool.Spool) []analytics.Event {
	t.Helper()
	b, err := sp.Read(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []analytics.Event
	for _, l := range b.Lines {
		var e analytics.Event
		if err := json.Unmarshal(l, &e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func TestPageview(t *testing.T) {
	in, sp := newIngest(t)
	w := post(in, "shop.example.com",
		`{"k":1,"u":"https://shop.example.com/pricing?utm_source=newsletter&utm_medium=email","r":"https://www.google.com/search?q=x","w":1440,"h":900,"l":"nb-NO"}`,
		chromeMac, "203.0.113.77")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	ev := spooled(t, sp)
	if len(ev) != 1 {
		t.Fatalf("%d events spooled", len(ev))
	}
	e := ev[0]
	want := analytics.Event{
		Time: now.UnixMilli(), Site: 3, Service: 40, Release: 981, Hostname: "shop.example.com", Kind: analytics.KindPageview,
		Path: "/pricing", Referrer: "www.google.com", Channel: ChannelEmail, UTMSource: "newsletter", UTMMedium: "email",
		Visitor: e.Visitor, IPPrefix: "203.0.113.0", Browser: "Chrome", BrowserVersion: "130", OS: "macOS", OSVersion: "10",
		Device: "desktop", ScreenW: 1440, ScreenH: 900, Language: "nb-NO",
	}
	if e != want {
		t.Errorf("event = %+v\nwant    %+v", e, want)
	}
	if e.Visitor == 0 || strings.Contains(string(mustLine(t, sp)), "203.0.113.77") {
		t.Error("the full address reached the spool, or no visitor ID")
	}
}

func mustLine(t *testing.T, sp *spool.Spool) []byte {
	b, _ := sp.Read(0, 1)
	return b.Lines[0]
}

func TestVisitorIDs(t *testing.T) {
	salt := bytes.Repeat([]byte{1}, SaltBytes)
	other := bytes.Repeat([]byte{2}, SaltBytes)
	a := visitorID(salt, 3, "203.0.113.77", chromeMac)
	switch a {
	case visitorID(other, 3, "203.0.113.77", chromeMac):
		t.Error("the salt doesn't change the ID")
	case visitorID(salt, 4, "203.0.113.77", chromeMac):
		t.Error("the site doesn't change the ID")
	case visitorID(salt, 3, "203.0.113.78", chromeMac):
		t.Error("the address doesn't change the ID")
	}
	if a != visitorID(salt, 3, "203.0.113.77", chromeMac) {
		t.Error("the same visitor gets two IDs")
	}
}

func TestEventsNotKept(t *testing.T) {
	in, sp := newIngest(t)
	page := `{"k":1,"u":"https://shop.example.com/"}`
	for name, c := range map[string]struct {
		host, body, ua string
		status         int
	}{
		"bot":                         {"shop.example.com", page, "Googlebot/2.1 (+http://www.google.com/bot.html)", http.StatusAccepted},
		"no user agent":               {"shop.example.com", page, "", http.StatusAccepted},
		"another site's URL":          {"shop.example.com", `{"k":1,"u":"https://evil.example/"}`, chromeMac, http.StatusBadRequest},
		"unknown kind":                {"shop.example.com", `{"k":9,"u":"https://shop.example.com/"}`, chromeMac, http.StatusBadRequest},
		"custom event without a name": {"shop.example.com", `{"k":2,"u":"https://shop.example.com/"}`, chromeMac, http.StatusBadRequest},
		"nested props":                {"shop.example.com", `{"k":2,"n":"x","u":"https://shop.example.com/","p":{"a":{"b":1}}}`, chromeMac, http.StatusBadRequest},
		"not JSON":                    {"shop.example.com", `k=1`, chromeMac, http.StatusBadRequest},
	} {
		if w := post(in, c.host, c.body, c.ua, "203.0.113.77"); w.Code != c.status {
			t.Errorf("%s: status %d, want %d", name, w.Code, c.status)
		}
	}
	if ev := spooled(t, sp); len(ev) != 0 {
		t.Errorf("%d events kept", len(ev))
	}
	// Without the routing table's tag, it didn't come through Caddy.
	r := httptest.NewRequest(http.MethodPost, EventPath, strings.NewReader(page))
	r.Host = "shop.example.com"
	r.Header.Set("User-Agent", chromeMac)
	w := httptest.NewRecorder()
	in.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("no tag: status %d", w.Code)
	}
}

func TestNoSaltNoEvent(t *testing.T) {
	in, sp := newIngest(t)
	in.Now = func() time.Time { return now.Add(48 * time.Hour) }
	if w := post(in, "shop.example.com", `{"k":1,"u":"https://shop.example.com/"}`, chromeMac, "203.0.113.77"); w.Code != http.StatusAccepted {
		t.Fatalf("status %d", w.Code)
	}
	if ev := spooled(t, sp); len(ev) != 0 || in.Stats().NoSalt.Load() != 1 {
		t.Errorf("an event was kept without its day's salt")
	}
}

func TestCustomEventAndOutbound(t *testing.T) {
	in, sp := newIngest(t)
	post(in, "shop.example.com", `{"k":2,"n":"signup","u":"https://shop.example.com/pricing","p":{"plan":"pro","seats":3}}`, chromeMac, "203.0.113.77")
	post(in, "shop.example.com", `{"k":3,"n":"https://github.com/nordtempo?invite=ola%40example.com#top","u":"https://shop.example.com/"}`, chromeMac, "203.0.113.77")
	ev := spooled(t, sp)
	if len(ev) != 2 || ev[0].Name != "signup" || ev[0].Props != `{"plan":"pro","seats":3}` || ev[1].Name != "https://github.com/nordtempo" {
		t.Errorf("events = %+v", ev)
	}
}

func TestSaltsKeepTodayAndLater(t *testing.T) {
	path := t.TempDir() + "/salts.json"
	s, err := OpenSalts(path)
	if err != nil {
		t.Fatal(err)
	}
	salt := bytes.Repeat([]byte{7}, SaltBytes)
	today := Day(now)
	if err := s.Set(map[int64][]byte{today - 1: salt, today: salt, today + 1: salt}, now); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSalts(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.get(today - 1); ok {
		t.Error("yesterday's salt was kept: its visitor IDs could be recomputed")
	}
	if _, ok := s.get(today + 1); !ok {
		t.Error("tomorrow's salt was lost")
	}
}

func TestScript(t *testing.T) {
	in, _ := newIngest(t)
	r := httptest.NewRequest(http.MethodGet, ScriptPath, nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	in.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("status %d, encoding %q", w.Code, w.Header().Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(zr)
	if !bytes.Contains(b, []byte(`"largest-contentful-paint"`)) || bytes.Contains(b, []byte(evMarker)) || !bytes.HasSuffix(bytes.TrimSpace(b), []byte("})({});")) {
		t.Errorf("a site that collects errors gets the core with ev.js spliced in; got:\n%s", b)
	}
	if core, withErrors, _ := ScriptSizes(); core > 2048 || withErrors-core > 2048 {
		t.Errorf("the script is %d bytes gzipped and errors add %d, over the 2 KB budgets (ADR 0037)", core, withErrors-core)
	}
	r = httptest.NewRequest(http.MethodGet, ScriptPath, nil)
	r.Header.Set("If-None-Match", w.Header().Get("ETag"))
	w = httptest.NewRecorder()
	in.ServeHTTP(w, r)
	if w.Code != http.StatusNotModified {
		t.Errorf("revalidation: status %d", w.Code)
	}
}

func TestScriptWithoutErrors(t *testing.T) {
	in, _ := newIngest(t)
	in.Rules = &Rules{}
	in.Rules.load(map[int64]node.SiteRules{3: {ErrorsOff: true}})
	r := httptest.NewRequest(http.MethodGet, ScriptPath, nil)
	r.Header.Set(TagHeader, "3.40.981")
	w := httptest.NewRecorder()
	in.ServeHTTP(w, r)
	if b := w.Body.String(); strings.Contains(b, "largest-contentful-paint") || strings.Contains(b, evMarker) {
		t.Errorf("a site with errors off got ev.js:\n%s", b)
	}
	// A cached script may still send them; they are dropped.
	if w := post(in, "shop.example.com", `{"k":4,"u":"https://shop.example.com/","n":"boom","t":"Error"}`, chromeMac, "203.0.113.9"); w.Code != http.StatusAccepted {
		t.Fatalf("status %d", w.Code)
	}
	if in.Stats().Accepted.Load() != 0 {
		t.Error("an error of a site with errors off was kept")
	}
}

func TestErrorEvent(t *testing.T) {
	in, sp := newIngest(t)
	stack := "TypeError: Cannot read properties of undefined (reading 'id')\n" +
		"    at renderCart (https://shop.example.com/assets/index-D2vX9pQa.js:1:3456)\n" +
		"    at https://shop.example.com/assets/vendor-a1b2c3d4.js:2:10"
	body, _ := json.Marshal(map[string]any{"k": 4, "u": "https://shop.example.com/cart/1234", "n": "Cannot read properties of undefined (reading 'id')", "t": "TypeError", "s": stack})
	if w := post(in, "shop.example.com", string(body), chromeMac, "203.0.113.9"); w.Code != http.StatusAccepted {
		t.Fatalf("status %d", w.Code)
	}
	evs := spooled(t, sp)
	if len(evs) != 1 || evs[0].Error == nil {
		t.Fatalf("spooled %+v", evs)
	}
	e := evs[0]
	if e.Kind != analytics.KindError || e.Path != "/cart/1234" || e.Error.Type != "TypeError" || e.Error.Frame != "renderCart (/assets/index.js)" || e.Error.Group == 0 {
		t.Errorf("error event %+v %+v", e, e.Error)
	}
	// The same error from the next release's bundle, about another cart,
	// is in the same group.
	if g := errorGroup("TypeError", "Cannot read properties of undefined (reading 'id')", topFrame(strings.ReplaceAll(stack, "D2vX9pQa", "Zq81LmNo"))); g != e.Error.Group {
		t.Error("a new bundle hash split the group")
	}
}

func TestErrorMessage(t *testing.T) {
	for in, want := range map[string]string{
		"Uncaught TypeError: price is undefined": "price is undefined",
		"TypeError: price is undefined":          "price is undefined",
		"Script error.":                          "Script error.",
	} {
		if got := errorMessage("TypeError", in); got != want {
			t.Errorf("errorMessage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTopFrame(t *testing.T) {
	for stack, want := range map[string]string{
		"Error: x\n    at JSON.parse (<anonymous>)\n    at load (https://a.example/app.js?v=3:10:2)": "load (/app.js)",
		"load@https://a.example/static/js/main.3f9a8c1e2b.js:1:200\n@https://a.example/x.js:1:1":     "load (/static/js/main.js)",
		"at https://a.example/chunk-ABCD1234.js:5:6":                                                 "/chunk.js",
		"Script error.": "",
		"    at async Promise.all (index 0)\n    at async submit (/src/form.ts:12:3)": "submit (/src/form.ts)",
	} {
		if got := topFrame(stack); got != want {
			t.Errorf("topFrame(%q) = %q, want %q", stack, got, want)
		}
	}
}

func TestVitalsEvent(t *testing.T) {
	in, sp := newIngest(t)
	body := `{"k":5,"u":"https://shop.example.com/p","v":{"lcp":1834.6,"inp":-1,"cls":0.04213,"fcp":912,"ttfb":1e9}}`
	if w := post(in, "shop.example.com", body, chromeMac, "203.0.113.9"); w.Code != http.StatusAccepted {
		t.Fatalf("status %d", w.Code)
	}
	evs := spooled(t, sp)
	if len(evs) != 1 || evs[0].Vitals == nil {
		t.Fatalf("spooled %+v", evs)
	}
	if v := *evs[0].Vitals; v != (analytics.Vitals{LCP: 1835, INP: -1, CLS: 0.0421, FCP: 912, TTFB: -1}) {
		t.Errorf("vitals %+v", v)
	}
	if w := post(in, "shop.example.com", `{"k":5,"u":"https://shop.example.com/p"}`, chromeMac, "203.0.113.9"); w.Code != http.StatusBadRequest {
		t.Errorf("vitals without values: status %d", w.Code)
	}
}

func TestParseAgent(t *testing.T) {
	for ua, want := range map[string]Agent{
		chromeMac: {Browser: "Chrome", BrowserVersion: "130", OS: "macOS", OSVersion: "10", Device: "desktop"},
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1": {Browser: "Safari", BrowserVersion: "17", OS: "iOS", OSVersion: "17", Device: "mobile"},
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0":           {Browser: "Edge", BrowserVersion: "130", OS: "Windows", Device: "desktop"},
		"Mozilla/5.0 (Linux; Android 14; SM-X710) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/25.0 Chrome/121.0.0.0 Safari/537.36":      {Browser: "Samsung Internet", BrowserVersion: "25", OS: "Android", OSVersion: "14", Device: "tablet"},
		"Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0":                                                                  {Browser: "Firefox", BrowserVersion: "131", OS: "Linux", Device: "desktop"},
		"curl/8.5.0": {Bot: true},
	} {
		if got := ParseAgent(ua); got != want {
			t.Errorf("%s\n got %+v\nwant %+v", ua, got, want)
		}
	}
}

func TestChannel(t *testing.T) {
	for _, c := range []struct{ ref, src, medium, want string }{
		{"", "", "", ChannelDirect},
		{"www.google.co.uk", "", "", ChannelSearch},
		{"duckduckgo.com", "", "", ChannelSearch},
		{"news.ycombinator.com", "", "", ChannelSocial},
		{"t.co", "", "", ChannelSocial},
		{"blog.example.org", "", "", ChannelReferral},
		{"www.google.com", "google", "cpc", ChannelPaid},
		{"", "newsletter", "email", ChannelEmail},
		{"", "partner", "", ChannelReferral},
	} {
		if got := Channel(c.ref, c.src, c.medium); got != c.want {
			t.Errorf("Channel(%q, %q, %q) = %s, want %s", c.ref, c.src, c.medium, got, c.want)
		}
	}
}

func TestSiteRules(t *testing.T) {
	in, sp := newIngest(t)
	rules, err := OpenRules(t.TempDir() + "/rules.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := rules.Set(map[int64]node.SiteRules{3: {ExcludedPaths: []string{"/admin/*", "/healthz"}, IgnoredIPs: []string{"198.51.100.0/24"}}}); err != nil {
		t.Fatal(err)
	}
	in.Rules = rules
	for _, c := range []struct{ path, ip string }{{"/admin/users", "203.0.113.7"}, {"/healthz", "203.0.113.7"}, {"/", "198.51.100.9"}} {
		post(in, "shop.example.com", `{"k":1,"u":"https://shop.example.com`+c.path+`"}`, chromeMac, c.ip)
	}
	post(in, "shop.example.com", `{"k":1,"u":"https://shop.example.com/admins"}`, chromeMac, "203.0.113.7")
	if ev := spooled(t, sp); len(ev) != 1 || ev[0].Path != "/admins" || in.Stats().Excluded.Load() != 3 {
		t.Errorf("kept %+v, excluded %d; want only /admins", ev, in.Stats().Excluded.Load())
	}
}

func TestGlobMatch(t *testing.T) {
	for _, c := range []struct {
		pattern, s string
		want       bool
	}{
		{"/admin/*", "/admin/x/y", true}, {"/admin/*", "/admin", false}, {"/healthz", "/healthz", true},
		{"/healthz", "/healthz/x", false}, {"*.php", "/wp/login.php", true}, {"/a/*/b", "/a/x/y/b", true},
	} {
		if got := globMatch(c.pattern, c.s); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v", c.pattern, c.s, got)
		}
	}
}

// gz gzips s, as the recorder's CompressionStream does.
func gz(s string) []byte {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_, _ = zw.Write([]byte(s))
	_ = zw.Close()
	return b.Bytes()
}

// chunkQuery is a replay chunk's query as the recorder sends it.
var chunkQuery = "g=0123456789abcdef&q=0&a=1000&b=4000&c=5000&n=12&u=" + url.QueryEscape("https://shop.example.com/cart?x=1")

// replayIngest returns an ingest with a replay spool, and no site rules.
func replayIngest(t *testing.T) (*Ingest, *spool.Spool) {
	t.Helper()
	in, _ := newIngest(t)
	rp, err := spool.Open(t.TempDir(), spool.Options{MaxLine: analytics.MaxReplayLine})
	if err != nil {
		t.Fatal(err)
	}
	in.Replay, in.Rules = rp, &Rules{}
	return in, rp
}

// postChunk sends a replay chunk as Caddy would forward it, its body of
// size bytes (-1: unknown) read from body.
func postChunk(in *Ingest, query, ip, ua string, body io.Reader, size int64) int {
	r := httptest.NewRequest(http.MethodPost, ReplayPath+"?"+query, body)
	r.ContentLength = size
	r.Host = "shop.example.com"
	r.Header.Set(TagHeader, "3.40.981")
	r.Header.Set(ClientIPHeader, ip)
	r.Header.Set("User-Agent", ua)
	w := httptest.NewRecorder()
	in.ServeHTTP(w, r)
	return w.Code
}

func TestReplayChunks(t *testing.T) {
	in, rp := replayIngest(t)
	send := func(query string, body []byte) int {
		return postChunk(in, query, "203.0.113.9", chromeMac, bytes.NewReader(body), int64(len(body)))
	}
	q := chunkQuery
	data := gz(`[{"type":4,"timestamp":1000}]`)
	in.Rules.load(map[int64]node.SiteRules{3: {ReplayPercent: 20, MaskAllText: true, Consent: "replay"}})
	if code := send(q, data); code != http.StatusAccepted {
		t.Fatalf("status %d", code)
	}
	b, err := rp.Read(0, 10)
	if err != nil || len(b.Lines) != 1 {
		t.Fatalf("replay spool: %+v, %v", b, err)
	}
	var ch analytics.ReplayChunk
	if err := json.Unmarshal(b.Lines[0], &ch); err != nil {
		t.Fatal(err)
	}
	if ch.Site != 3 || ch.Segment != 0x0123456789abcdef || ch.Path != "/cart" || ch.Events != 12 || ch.Clock != 5000 ||
		ch.Visitor == 0 || ch.Browser != "Chrome" || !bytes.Equal(ch.Data, data) {
		t.Errorf("chunk = %+v", ch)
	}
	for _, bad := range []struct {
		q    string
		body []byte
	}{
		{q, []byte("not gzip")},
		{strings.Replace(q, "g=0123456789abcdef", "g=xyz", 1), data},
		{strings.Replace(q, "shop.example.com", "evil.example", 1), data},
		{q, append(gz(""), make([]byte, analytics.MaxReplayChunk)...)},
	} {
		if code := send(bad.q, bad.body); code != http.StatusBadRequest {
			t.Errorf("%q: status %d", bad.q, code)
		}
	}
	// The script tells sampled page loads to record, masking all text,
	// once the visitor consented.
	r := httptest.NewRequest(http.MethodGet, ScriptPath, nil)
	r.Header.Set(TagHeader, "3.40.981")
	w := httptest.NewRecorder()
	in.ServeHTTP(w, r)
	if !strings.HasSuffix(strings.TrimSpace(w.Body.String()), `})({"r":20,"m":1,"c":1});`) {
		t.Errorf("script ends %q", w.Body.String()[max(0, w.Body.Len()-40):])
	}
	r = httptest.NewRequest(http.MethodGet, RecorderPath, nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w = httptest.NewRecorder()
	in.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "gzip" || w.Body.Len() > 50<<10 {
		t.Errorf("recorder: %d, %q, %d bytes", w.Code, w.Header().Get("Content-Encoding"), w.Body.Len())
	}
}

// unread is a request body that fails the test when read: for chunks the
// ingest must turn away before reading their bodies.
type unread struct{ t *testing.T }

func (u unread) Read([]byte) (int, error) {
	u.t.Error("the body of a chunk that isn't kept was read")
	return 0, io.EOF
}

func TestReplayRejectsBeforeReading(t *testing.T) {
	records := node.SiteRules{ReplayPercent: 20}
	for name, c := range map[string]struct {
		rules  node.SiteRules
		later  time.Duration
		ua     string
		query  string
		size   int64
		status int
		stat   func(*Stats) *atomic.Int64
	}{
		"site doesn't record": {status: http.StatusAccepted, stat: func(s *Stats) *atomic.Int64 { return &s.Excluded }},
		"bot":                 {rules: records, ua: "curl/8.5.0", status: http.StatusAccepted, stat: func(s *Stats) *atomic.Int64 { return &s.Bots }},
		"no salt":             {rules: records, later: 48 * time.Hour, status: http.StatusAccepted, stat: func(s *Stats) *atomic.Int64 { return &s.NoSalt }},
		"excluded path":       {rules: node.SiteRules{ReplayPercent: 20, ExcludedPaths: []string{"/cart"}}, status: http.StatusAccepted, stat: func(s *Stats) *atomic.Int64 { return &s.Excluded }},
		"malformed":           {rules: records, query: "g=xyz", status: http.StatusBadRequest, stat: func(s *Stats) *atomic.Int64 { return &s.Invalid }},
		"too big":             {rules: records, size: analytics.MaxReplayChunk + 1, status: http.StatusBadRequest, stat: func(s *Stats) *atomic.Int64 { return &s.Invalid }},
	} {
		in, _ := replayIngest(t)
		in.Rules.load(map[int64]node.SiteRules{3: c.rules})
		in.Now = func() time.Time { return now.Add(c.later) }
		code := postChunk(in, cmpStr(c.query, chunkQuery), "203.0.113.9", cmpStr(c.ua, chromeMac), unread{t}, cmp.Or(c.size, 100))
		if n := c.stat(in.Stats()).Load(); code != c.status || n != 1 {
			t.Errorf("%s: status %d, want %d; counted %d", name, code, c.status, n)
		}
	}
}

func TestReplaysInFlight(t *testing.T) {
	in, _ := replayIngest(t)
	in.Rules.load(map[int64]node.SiteRules{3: {ReplayPercent: 20}})
	in.replaying.Store(maxReplays)
	if code := postChunk(in, chunkQuery, "203.0.113.9", chromeMac, unread{t}, 100); code != http.StatusServiceUnavailable || in.Stats().Limited.Load() != 1 {
		t.Fatalf("with %d chunks in flight: status %d, %d limited", maxReplays, code, in.Stats().Limited.Load())
	}
	in.replaying.Store(maxReplays - 1)
	data := gz(`[{"type":4,"timestamp":1000}]`)
	if code := postChunk(in, chunkQuery, "203.0.113.9", chromeMac, bytes.NewReader(data), int64(len(data))); code != http.StatusAccepted ||
		in.Stats().Replays.Load() != 1 || in.replaying.Load() != maxReplays-1 {
		t.Errorf("a free slot: status %d, %d kept, %d in flight after", code, in.Stats().Replays.Load(), in.replaying.Load())
	}
}

func TestRateLimits(t *testing.T) {
	in, _ := replayIngest(t)
	in.limits.seed = 1 // the addresses below get slots of their own
	in.Rules.load(map[int64]node.SiteRules{3: {ReplayPercent: 20}})
	page := `{"k":1,"u":"https://shop.example.com/"}`
	for range eventsPerIP + 1 {
		if w := post(in, "shop.example.com", page, chromeMac, "203.0.113.9"); w.Code != http.StatusAccepted {
			t.Fatalf("status %d", w.Code)
		}
	}
	if in.Stats().Accepted.Load() != eventsPerIP || in.Stats().Limited.Load() != 1 {
		t.Errorf("%d events kept and %d limited, want %d and 1", in.Stats().Accepted.Load(), in.Stats().Limited.Load(), eventsPerIP)
	}
	post(in, "shop.example.com", page, chromeMac, "203.0.113.10")
	in.Now = func() time.Time { return now.Add(time.Second) }
	post(in, "shop.example.com", page, chromeMac, "203.0.113.9")
	if in.Stats().Accepted.Load() != eventsPerIP+2 {
		t.Error("another address, or the same in the next second, was limited")
	}

	in.Now = func() time.Time { return now }
	data := gz(`[{"type":4,"timestamp":1000}]`)
	for range replaysPerIP {
		if code := postChunk(in, chunkQuery, "203.0.113.9", chromeMac, bytes.NewReader(data), int64(len(data))); code != http.StatusAccepted {
			t.Fatalf("status %d", code)
		}
	}
	if code := postChunk(in, chunkQuery, "203.0.113.9", chromeMac, unread{t}, int64(len(data))); code != http.StatusTooManyRequests {
		t.Errorf("a chunk over the limit: status %d", code)
	}
	in.Now = func() time.Time { return now.Add(time.Second) }
	if code := postChunk(in, chunkQuery, "203.0.113.9", chromeMac, bytes.NewReader(data), int64(len(data))); code != http.StatusAccepted ||
		in.Stats().Replays.Load() != replaysPerIP+1 {
		t.Errorf("the next second: status %d, %d kept", code, in.Stats().Replays.Load())
	}
}

func TestLimiterSiteBytes(t *testing.T) {
	l := limiter{seed: 1}
	// Ten full chunks fit a site's second, from addresses of their own.
	for i := range replayBytesPerSite / analytics.MaxReplayChunk {
		if !l.allow(now, true, 3, "203.0.113."+strconv.Itoa(i), analytics.MaxReplayChunk) {
			t.Fatalf("chunk %d was limited", i)
		}
	}
	switch {
	case l.allow(now, true, 3, "198.51.100.1", analytics.MaxReplayChunk):
		t.Error("a chunk over the site's budget was allowed")
	case !l.allow(now, true, 4, "198.51.100.1", analytics.MaxReplayChunk):
		t.Error("another site's chunk was limited")
	case !l.allow(now, false, 3, "198.51.100.1", 300):
		t.Error("the site's events were limited by its replays")
	case !l.allow(now.Add(time.Second), true, 3, "198.51.100.1", analytics.MaxReplayChunk):
		t.Error("the site's next second was limited")
	}
	if !l.allow(now, false, 5, "198.51.100.1", eventBytesPerSite) || l.allow(now, false, 5, "198.51.100.2", 1) {
		t.Error("events past the site's budget were allowed")
	}
}

func TestLimiterIPv6(t *testing.T) {
	l := limiter{seed: 1}
	for i := range replaysPerIP {
		l.allow(now, true, 3, "2001:db8:1:2::"+strconv.Itoa(i+1), 100)
	}
	if l.allow(now, true, 3, "2001:db8:1:2:ffff::1", 100) {
		t.Error("another address in the same /64 was counted apart")
	}
	if !l.allow(now, true, 3, "2001:db8:1:3::1", 100) {
		t.Error("the next /64 was limited")
	}
}
