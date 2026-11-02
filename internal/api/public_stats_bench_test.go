package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// BenchmarkPublicPage is a public dashboard answered from its cache: what
// every request but one a minute costs.
func BenchmarkPublicPage(b *testing.B) {
	page, err := renderPublic(publicSample(true, "boom"))
	if err != nil {
		b.Fatal(err)
	}
	p := &publicStats{limiter: &ipLimiter{buckets: map[string]*bucket{}}, cache: map[publicKey]*publicEntry{}}
	p.load = func(context.Context, publicKey) ([]byte, bool, error) { return page, true, nil }
	p.dashboard = func(string) (int64, bool) { return 1, true }
	r := httptest.NewRequest(http.MethodGet, "/?range=30d", nil)
	r.Host = "stats.example.com"
	b.ReportAllocs()
	for b.Loop() {
		clear(p.limiter.buckets) // so the limiter never refuses
		w := httptest.NewRecorder()
		p.page(w, r)
		if w.Code != http.StatusOK {
			b.Fatalf("status %d", w.Code)
		}
	}
}

// BenchmarkRenderPublic is rendering a dashboard's page, once a minute
// per dashboard and range.
func BenchmarkRenderPublic(b *testing.B) {
	st := publicSample(true, "boom")
	b.ReportAllocs()
	for b.Loop() {
		if _, err := renderPublic(st); err != nil {
			b.Fatal(err)
		}
	}
}
