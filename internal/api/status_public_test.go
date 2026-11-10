package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
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
		func(context.Context, int64) (apitypes.PublicStatusPage, error) { return page, nil }, slog.Default())
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
	var renders atomic.Int32
	var fail atomic.Bool
	srv := newStatusServer(nil, func(context.Context, int64) (apitypes.PublicStatusPage, error) {
		renders.Add(1)
		if fail.Load() {
			return apitypes.PublicStatusPage{}, context.DeadlineExceeded
		}
		return testStatusPage(), nil
	}, slog.Default())
	ctx, k := context.Background(), statusKey{7, statusHTML}
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
	srv := newStatusServer(func(string) (int64, int64, bool) { return 7, 1, true },
		func(context.Context, int64) (apitypes.PublicStatusPage, error) { return testStatusPage(), nil }, slog.Default())
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
	srv := newStatusServer(nil, func(context.Context, int64) (apitypes.PublicStatusPage, error) { return page, nil }, slog.Default())
	b.ReportAllocs()
	var size int
	for b.Loop() {
		body, err := srv.render(context.Background(), statusKey{7, statusHTML})
		if err != nil {
			b.Fatal(err)
		}
		size = len(body.gz)
	}
	b.ReportMetric(float64(size), "gzip-bytes")
}
