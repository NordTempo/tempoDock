package inject

import (
	"net/http"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// BenchmarkInject is a 33 KB page through the handler in 4 KB writes, as
// is and gzipped for the visitor: the cost every page load of an
// analytics site pays (ADR 0106).
func BenchmarkInject(b *testing.B) {
	big := strings.Replace(page, "<body>", "<body>"+strings.Repeat("<p>Lorem ipsum dolor sit amet</p>", 1000), 1)
	h := &Handler{Hosts: []string{"example.com"}, Src: "/_dock/a/s.js"}
	_ = h.Provision(caddyCtx())
	for _, enc := range []string{"", "gzip"} {
		b.Run("encoding="+enc, func(b *testing.B) {
			a := &app{status: 200, bytes: []byte(big), chunk: 4096}
			req := visit("example.com")
			b.ReportAllocs()
			for b.Loop() {
				req.Header.Set("Accept-Encoding", enc)
				w := discard{h: http.Header{}}
				if err := h.ServeHTTP(&w, req, caddyhttp.HandlerFunc(a.ServeHTTP)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// discard is a ResponseWriter that keeps nothing.
type discard struct{ h http.Header }

func (d *discard) Header() http.Header         { return d.h }
func (d *discard) Write(p []byte) (int, error) { return len(p), nil }
func (d *discard) WriteHeader(int)             {}
