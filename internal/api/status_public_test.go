package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
)

// testStatusPage is a page with one component down and one idle.
func testStatusPage() apitypes.PublicStatusPage {
	up, part := 100.0, 99.5
	days := make([]apitypes.StatusDay, apitypes.StatusDays)
	for i := range days {
		days[i] = apitypes.StatusDay{Date: time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format(time.DateOnly), Uptime: &up}
	}
	days[89].Uptime = &part
	days[0].Uptime = nil
	return apitypes.PublicStatusPage{Title: "Acme <b>", Status: apitypes.StatusPartialOutage, ShowUptime: true,
		GeneratedAt: time.Date(2026, 11, 10, 14, 6, 0, 0, time.UTC),
		Groups: []apitypes.PublicStatusGroup{{Name: "Core", Components: []apitypes.PublicStatusComponent{
			{Name: "API", Status: apitypes.StatusPartialOutage, Uptime: &part, Days: days},
			{Name: "Demo", Status: apitypes.StatusIdle, Days: days},
		}}}}
}

// fakeStatus stands in for core: page is every page's view, inc its one
// incident. renders counts reads; fail makes them fail.
type fakeStatus struct {
	page    apitypes.PublicStatusPage
	inc     apitypes.PublicIncident
	renders atomic.Int32
	fail    atomic.Bool
}

func (f *fakeStatus) PublicStatusPage(context.Context, int64) (apitypes.PublicStatusPage, error) {
	f.renders.Add(1)
	if f.fail.Load() {
		return apitypes.PublicStatusPage{}, context.DeadlineExceeded
	}
	return f.page, nil
}

func (f *fakeStatus) PublicStatusIncident(_ context.Context, _ int64, id string) (apitypes.PublicIncident, string, error) {
	if id != f.inc.ID {
		return apitypes.PublicIncident{}, "", datapanel.Errorf(apitypes.CodeNotFound, "no status page here")
	}
	return f.inc, f.page.Title, nil
}

func (f *fakeStatus) PublicStatusHistory(context.Context, int64) (apitypes.PublicStatusHistory, error) {
	return apitypes.PublicStatusHistory{Title: f.page.Title, Incidents: []apitypes.PublicIncident{f.inc}}, nil
}

func (f *fakeStatus) PublicStatusFeed(context.Context, int64) (apitypes.PublicStatusFeed, error) {
	out := apitypes.PublicStatusFeed{Title: f.page.Title, Updated: f.page.GeneratedAt}
	for _, u := range f.inc.Updates {
		out.Entries = append(out.Entries, apitypes.PublicFeedEntry{Incident: f.inc.ID, Title: f.inc.Title, PublicUpdate: u})
	}
	return out, nil
}

// get asks the handler for path on host, with gzip unless plain.
func statusGet(h http.Handler, host, path string, plain bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Host = host
	if !plain {
		r.Header.Set("Accept-Encoding", "gzip")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// The page is served only for a published page's host, at / and
// /status.json, with a CSP that allows its style and nothing else; it
// escapes what people typed, and HEAD, 304 and clients without gzip work.
func TestStatusPagesServe(t *testing.T) {
	page := testStatusPage()
	srv := newStatusServer(func(host string) (int64, int64, bool) { return 7, 1, host == "status-acme.dock.example.net" },
		&fakeStatus{page: page}, slog.Default())
	h := srv.handler()
	const host = "status-acme.dock.example.net"

	w := statusGet(h, host, "/", false)
	if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "style-src 'sha256-") {
		t.Fatalf("page: %d %v", w.Code, w.Header())
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	html := string(body)
	for _, want := range []string{"Acme &lt;b&gt;", "Partial outage: API", "updated 14:06 UTC", "99.50%", `class="w" title="10 Nov: 99.50%"`, `title="13 Aug: no data"`, ">Idle<"} {
		if !strings.Contains(html, want) {
			t.Errorf("page has no %q", want)
		}
	}
	if strings.Contains(html, "<script") {
		t.Error("the page has a script")
	}
	if !strings.Contains(html, "<style>"+statusCSS+"</style>") {
		t.Error("the page's style isn't the one its CSP allows")
	}

	etag := w.Header().Get("ETag")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host, r.Header["If-None-Match"] = host, []string{etag}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotModified {
		t.Errorf("with its ETag: %d, want 304", w.Code)
	}
	if w := statusGet(h, host, "/", true); w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), "<!doctype html>") {
		t.Errorf("without gzip: %d %q", w.Code, w.Body.String()[:min(20, w.Body.Len())])
	}

	w = statusGet(h, host, "/status.json", true)
	var js statusJSONPage
	if err := json.Unmarshal(w.Body.Bytes(), &js); err != nil || js.Status != "partial_outage" || len(js.Components) != 2 ||
		js.Components[0].Group != "Core" || js.Components[0].Uptime == nil || w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("status.json: %s, %v", w.Body.String(), err)
	}

	for _, tc := range []struct{ host, path string }{{host, "/admin"}, {host, "/api/v1/me"}, {"status-other.dock.example.net", "/"}, {"dock.example.net", "/"}} {
		if w := statusGet(h, tc.host, tc.path, true); w.Code != http.StatusNotFound || w.Body.String() != "no status page here\n" {
			t.Errorf("%s%s: %d %q", tc.host, tc.path, w.Code, w.Body.String())
		}
	}
	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x"))
	r.Host = host
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == http.StatusOK {
		t.Error("a POST was answered")
	}
}

// A page hiding percentages shows no numbers, only whether a day had
// downtime.
func TestStatusPageHidesUptime(t *testing.T) {
	page := testStatusPage()
	page.ShowUptime = false
	b, err := renderStatusPage(page)
	if err != nil {
		t.Fatal(err)
	}
	_, html, _ := strings.Cut(string(b), "<main>")
	if strings.Contains(html, "%") || !strings.Contains(html, `title="10 Nov: downtime"`) {
		t.Errorf("page shows numbers: %s", html)
	}
}

// A page is rendered when asked for after it changed (a new generation)
// or once it is statusFresh old, at most once a statusMinAge, and a
// render that fails keeps the last bytes. One that isn't published any
// more goes from the cache.
func TestStatusPagesCache(t *testing.T) {
	src := &fakeStatus{page: testStatusPage()}
	renders, fail := &src.renders, &src.fail
	srv := newStatusServer(nil, src, slog.Default())
	ctx, k := context.Background(), statusKey{id: 7, format: statusHTML}
	first, err := srv.answer(ctx, k, 1)
	if err != nil || len(first.gz) == 0 {
		t.Fatal(err)
	}
	if _, _ = srv.answer(ctx, k, 1); renders.Load() != 1 {
		t.Errorf("an unchanged page was rendered again (%d)", renders.Load())
	}
	if _, _ = srv.answer(ctx, k, 2); renders.Load() != 1 {
		t.Errorf("a changed page was rendered again within statusMinAge (%d)", renders.Load())
	}
	srv.cache[k].at = time.Now().Add(-statusMinAge)
	if _, _ = srv.answer(ctx, k, 2); renders.Load() != 2 {
		t.Errorf("a changed page wasn't rendered again (%d)", renders.Load())
	}
	srv.cache[k].at = time.Now().Add(-statusFresh)
	fail.Store(true)
	if b, err := srv.answer(ctx, k, 2); err != nil || string(b.gz) != string(first.gz) && len(b.gz) == 0 {
		t.Errorf("a failed render: %v, want the last bytes", err)
	}
	if srv.bytes != len(srv.cache[k].gz) {
		t.Errorf("cache counts %d bytes, holds %d", srv.bytes, len(srv.cache[k].gz))
	}
}

// BenchmarkStatusServe is a status page answered from its cache: what
// every view but one in a while costs.
func BenchmarkStatusServe(b *testing.B) {
	srv := newStatusServer(func(string) (int64, int64, bool) { return 7, 1, true }, &fakeStatus{page: testStatusPage()}, slog.Default())
	h := srv.handler()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "status-acme.dock.example.net"
	r.Header.Set("Accept-Encoding", "gzip")
	b.ReportAllocs()
	for b.Loop() {
		clear(srv.limiter.buckets) // so the limiter never refuses
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			b.Fatalf("status %d", w.Code)
		}
	}
}

// BenchmarkRenderStatusPage is rendering a page of 100 components and
// gzipping it, as a render after a change does.
func BenchmarkRenderStatusPage(b *testing.B) {
	page := testStatusPage()
	c := page.Groups[0].Components[0]
	page.Groups = nil
	for range 10 {
		grp := apitypes.PublicStatusGroup{Name: "Group"}
		for range 10 {
			grp.Components = append(grp.Components, c)
		}
		page.Groups = append(page.Groups, grp)
	}
	srv := newStatusServer(nil, &fakeStatus{page: page}, slog.Default())
	b.ReportAllocs()
	var size int
	for b.Loop() {
		body, err := srv.render(context.Background(), statusKey{id: 7, format: statusHTML})
		if err != nil {
			b.Fatal(err)
		}
		size = len(body.gz)
	}
	b.ReportMetric(float64(size), "gzip-bytes")
}

// testIncident is an open incident with two updates, one edited, whose
// words hold a link and markup.
func testIncident() apitypes.PublicIncident {
	at := time.Date(2026, 11, 10, 13, 48, 0, 0, time.UTC)
	return apitypes.PublicIncident{ID: "4f2kqz7mna", Title: "API <errors>", State: apitypes.IncidentIdentified, StartedAt: at,
		Components: []apitypes.StatusImpact{{Component: "API", Impact: apitypes.StatusPartialOutage}},
		Updates: []apitypes.PublicUpdate{
			{State: apitypes.IncidentIdentified, Message: "Found it, see https://acme.example/post.\n\nMoving <b>traffic</b>.", At: at.Add(14 * time.Minute), Edited: true},
			{State: apitypes.IncidentInvestigating, Message: "Looking.", At: at},
		}}
}

// Open incidents lead the page and status.json; each has its own page,
// the history lists them and the feed has one entry per update. What
// people wrote is escaped, with https links made links, and a day an
// incident ran is at least its colour.
func TestStatusIncidentPages(t *testing.T) {
	page := testStatusPage()
	inc := testIncident()
	page.Open = []apitypes.PublicIncident{inc}
	page.Groups[0].Components[1].Days[89].Impact = apitypes.StatusMajorOutage
	page.Groups[0].Components[1].Days[89].Incident = `Jobs "stuck"`
	src := &fakeStatus{page: page, inc: inc}
	h := newStatusServer(func(string) (int64, int64, bool) { return 7, 1, true }, src, slog.Default()).handler()
	const host = "status-acme.dock.example.net"

	html := statusGet(h, host, "/", true).Body.String()
	for _, want := range []string{`<a href="/incidents/4f2kqz7mna">API &lt;errors&gt;</a>`, "Affects API", "b-partial_outage",
		`<a href="https://acme.example/post" rel="nofollow noopener">https://acme.example/post</a>.`, "Moving &lt;b&gt;traffic&lt;/b&gt;.",
		"<time>14:02</time>", "· edited", `class="r" title="10 Nov: 99.50%, Jobs &#34;stuck&#34;"`, `href="/history"`} {
		if !strings.Contains(html, want) {
			t.Errorf("page has no %q", want)
		}
	}
	if strings.Index(html, "Found it") > strings.Index(html, "Looking.") {
		t.Error("updates aren't newest first")
	}

	one := statusGet(h, host, "/incidents/4f2kqz7mna", true)
	if one.Code != http.StatusOK || !strings.Contains(one.Body.String(), "since 10 Nov 13:48 UTC") || strings.Contains(one.Body.String(), `href="/incidents/`) {
		t.Errorf("incident page: %d %s", one.Code, one.Body.String())
	}
	for _, path := range []string{"/incidents/zzzzzzzzzz", "/incidents/NOT-AN-ID", "/incidents/"} {
		if w := statusGet(h, host, path, true); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if w := statusGet(h, host, "/history", true); !strings.Contains(w.Body.String(), "<h2>November 2026</h2>") {
		t.Errorf("history: %s", w.Body.String())
	}

	w := statusGet(h, host, "/feed.atom", true)
	var feed atomFeed
	if err := xml.Unmarshal(w.Body.Bytes(), &feed); err != nil || len(feed.Entries) != 2 || feed.ID != "https://"+host+"/" ||
		feed.Entries[0].Link.Href != "https://"+host+"/incidents/4f2kqz7mna" || feed.Entries[0].Title != "API <errors>: Identified" ||
		!strings.HasPrefix(w.Header().Get("Content-Type"), "application/atom+xml") {
		t.Errorf("feed: %v %+v", err, feed)
	}

	var js statusJSONPage
	if err := json.Unmarshal(statusGet(h, host, "/status.json", true).Body.Bytes(), &js); err != nil || len(js.Incidents) != 1 ||
		js.Incidents[0].URL != "/incidents/4f2kqz7mna" || len(js.Incidents[0].Updates) != 2 {
		t.Errorf("status.json incidents: %+v, %v", js.Incidents, err)
	}
}
