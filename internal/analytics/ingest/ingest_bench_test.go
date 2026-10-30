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
	in, _ := newIngest(b)
	body := `{"k":1,"u":"https://shop.example.com/products/kayak-pro?utm_source=hn","r":"https://news.ycombinator.com/item?id=1","w":1440,"h":900,"l":"nb-NO"}`
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
