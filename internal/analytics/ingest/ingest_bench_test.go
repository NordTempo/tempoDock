package ingest

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/analytics/spool"
	"github.com/NordTempo/tempoDock/internal/node"
)

// BenchmarkIngestEvent is one page view through the ingest: parsing,
// enriching and spooling it (ADR 0037 budget: under 20 µs and a few
// allocations).
func BenchmarkIngestEvent(b *testing.B) {
	benchIngest(b, pageview)
}

// pageview is a page view as the script sends it.
const pageview = `{"k":1,"u":"https://shop.example.com/products/kayak-pro?utm_source=hn","r":"https://news.ycombinator.com/item?id=1","w":1440,"h":900,"l":"nb-NO"}`

// BenchmarkIngestError is an uncaught error with a stack trace: its top
// frame parsed and its group hashed as well.
func BenchmarkIngestError(b *testing.B) {
	benchIngest(b, `{"k":4,"u":"https://shop.example.com/cart","n":"Cannot read properties of undefined (reading 'id')","t":"TypeError",`+
		`"s":"TypeError: Cannot read properties of undefined (reading 'id')\n    at renderCart (https://shop.example.com/assets/index-D2vX9pQa.js:1:3456)\n    at https://shop.example.com/assets/vendor-a1b2c3d4.js:2:10","w":1440,"h":900,"l":"nb-NO"}`)
}

// BenchmarkIngestVitals is a page load's Web Vitals.
func BenchmarkIngestVitals(b *testing.B) {
	benchIngest(b, `{"k":5,"u":"https://shop.example.com/products/kayak-pro","v":{"lcp":1834.6,"inp":72,"cls":0.04213,"fcp":912,"ttfb":180},"w":1440,"h":900,"l":"nb-NO"}`)
}

// benchIngest posts body to the ingest b.N times.
func benchIngest(b *testing.B, body string) {
	in, _ := newIngest(b)
	in.Now = benchClock()
	r := httptest.NewRequest(http.MethodPost, EventPath, nil)
	r.Host = "shop.example.com"
	r.Header.Set(TagHeader, "3.40.981")
	r.Header.Set(ClientIPHeader, "203.0.113.77")
	r.Header.Set("User-Agent", chromeMac)
	w := httptest.NewRecorder()
	body2 := strings.NewReader(body)
	b.ReportAllocs()
	for b.Loop() {
		body2.Reset(body)
		r.Body = noClose{body2}
		in.ServeHTTP(w, r)
	}
	if w.Code != http.StatusAccepted || in.Stats().Accepted.Load() == 0 {
		b.Fatal(w.Code)
	}
}

// BenchmarkIngestEventParallel is page views from eight clients at once,
// each from its own address: shows contention on the rate limits' lock
// and the spool's that one client doesn't.
func BenchmarkIngestEventParallel(b *testing.B) {
	b.Run("parallel=8", func(b *testing.B) {
		in, _ := newIngest(b)
		in.Now = benchClock()
		var client atomic.Int32
		b.SetParallelism(max(1, 8/runtime.GOMAXPROCS(0)))
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			r := httptest.NewRequest(http.MethodPost, EventPath, nil)
			r.Host = "shop.example.com"
			r.Header.Set(TagHeader, "3.40.981")
			r.Header.Set(ClientIPHeader, "203.0.113."+strconv.Itoa(int(client.Add(1))))
			r.Header.Set("User-Agent", chromeMac)
			w := httptest.NewRecorder()
			rd := strings.NewReader(pageview)
			for pb.Next() {
				rd.Reset(pageview)
				r.Body = noClose{rd}
				in.ServeHTTP(w, r)
			}
		})
		if in.Stats().Accepted.Load() == 0 {
			b.Fatal("no page view was kept")
		}
	})
}

type noClose struct{ *strings.Reader }

func (noClose) Close() error { return nil }

// BenchmarkIngestReplay is a 20 KB replay chunk through the ingest: its
// query read, its body checked and base64-encoded into the replay spool.
func BenchmarkIngestReplay(b *testing.B) {
	benchReplay(b, 20<<10)
}

// BenchmarkIngestReplayMax is a chunk of analytics.MaxReplayChunk, the
// most one request holds: B/op is what an in-flight replay allocates
// beyond the pooled buffers it reuses.
func BenchmarkIngestReplayMax(b *testing.B) {
	benchReplay(b, analytics.MaxReplayChunk)
}

// benchReplay posts a replay chunk of size bytes to the ingest b.N times.
func benchReplay(b *testing.B, size int) {
	in, _ := newIngest(b)
	in.Now = benchClock()
	rp, err := spool.Open(b.TempDir(), spool.Options{MaxLine: analytics.MaxReplayLine, MaxBytes: 64 << 20})
	if err != nil {
		b.Fatal(err)
	}
	in.Replay, in.Rules = rp, &Rules{}
	in.Rules.load(map[int64]node.SiteRules{3: {ReplayPercent: 20}})
	body := bytes.Repeat([]byte{0x1f, 0x8b, 8, 0, 1, 2, 3, 4}, size/8)
	r := httptest.NewRequest(http.MethodPost, ReplayPath+"?g=0123456789abcdef&q=3&a=1000&b=4000&c=5000&n=120&u=https%3A%2F%2Fshop.example.com%2Fcart", nil)
	r.Host = "shop.example.com"
	r.Header.Set(TagHeader, "3.40.981")
	r.Header.Set(ClientIPHeader, "203.0.113.77")
	r.Header.Set("User-Agent", chromeMac)
	r.ContentLength = int64(len(body))
	w := httptest.NewRecorder()
	rd := bytes.NewReader(body)
	b.ReportAllocs()
	for b.Loop() {
		rd.Reset(body)
		r.Body = io.NopCloser(rd)
		in.ServeHTTP(w, r)
	}
	if w.Code != http.StatusAccepted || in.Stats().Replays.Load() == 0 {
		b.Fatal(w.Code)
	}
}

// benchClock is a clock that moves a second on every call, wrapping
// within the hour so the day's salt holds: every request is its
// address's first in its second, as on a server with many visitors, so
// the rate limits never cut a benchmark short.
func benchClock() func() time.Time {
	var calls atomic.Int64
	return func() time.Time { return now.Add(time.Duration(calls.Add(1)%3600) * time.Second) }
}
