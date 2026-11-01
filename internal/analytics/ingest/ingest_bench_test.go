package ingest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// BenchmarkIngestEvent is one page view through the ingest: parsing,
// enriching and spooling it (ADR 0037 budget: under 20 µs and a few
// allocations).
func BenchmarkIngestEvent(b *testing.B) {
	benchIngest(b, `{"k":1,"u":"https://shop.example.com/products/kayak-pro?utm_source=hn","r":"https://news.ycombinator.com/item?id=1","w":1440,"h":900,"l":"nb-NO"}`)
}

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
	if w.Code != http.StatusAccepted {
		b.Fatal(w.Code)
	}
}

type noClose struct{ *strings.Reader }

func (noClose) Close() error { return nil }
