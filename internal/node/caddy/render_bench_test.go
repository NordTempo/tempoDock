package caddy

import (
	"fmt"
	"testing"

	"github.com/NordTempo/tempoDock/internal/node"
)

// BenchmarkRender renders the full Caddy config for 500 routes, as every
// domain change and release cutover does: 450 proxied services with two
// hostnames each and 50 redirects.
func BenchmarkRender(b *testing.B) {
	routes := make([]node.Route, 0, 500)
	for i := range 450 {
		routes = append(routes, node.Route{
			Hostnames: []string{fmt.Sprintf("svc%d.example.com", i), fmt.Sprintf("svc%d-prod.apps.example.com", i)},
			Upstream:  fmt.Sprintf("127.0.0.1:%d", 20000+i),
		})
	}
	for i := range 50 {
		routes = append(routes, node.Route{
			Hostnames:  []string{fmt.Sprintf("www.svc%d.example.com", i)},
			RedirectTo: fmt.Sprintf("svc%d.example.com", i),
		})
	}
	for _, tls := range []bool{true, false} {
		b.Run(fmt.Sprintf("routes=500,tls=%t", tls), func(b *testing.B) {
			opts := testOptions()
			opts.DisableTLS = !tls
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Render(opts, routes, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
