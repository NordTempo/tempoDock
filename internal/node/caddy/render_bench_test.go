package caddy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/NordTempo/tempoDock/internal/node"
)

// benchRoutes are 500 routes, as a busy server has: 450 proxied services
// with two hostnames each (keyed, ADR 0091, when keyed is set) and 50
// redirects.
func benchRoutes(keyed bool) []node.Route {
	routes := make([]node.Route, 0, 500)
	for i := range 450 {
		r := node.Route{
			Hostnames: []string{fmt.Sprintf("svc%d.example.com", i), fmt.Sprintf("svc%d-prod.apps.example.com", i)},
			Upstream:  fmt.Sprintf("127.0.0.1:%d", 20000+i),
		}
		if keyed {
			r.Key = fmt.Sprintf("s%d", i)
		}
		routes = append(routes, r)
	}
	for i := range 50 {
		routes = append(routes, node.Route{
			Hostnames:  []string{fmt.Sprintf("www.svc%d.example.com", i)},
			RedirectTo: fmt.Sprintf("svc%d.example.com", i),
		})
	}
	return routes
}

// BenchmarkRender renders the full Caddy config for 500 routes, as every
// domain change and release cutover does, with upstreams in the config or
// in files (keyed).
func BenchmarkRender(b *testing.B) {
	for _, c := range []struct {
		tls, keyed bool
	}{{true, false}, {false, false}, {true, true}} {
		name := fmt.Sprintf("routes=500,tls=%t", c.tls)
		if c.keyed {
			name += ",keyed"
		}
		b.Run(name, func(b *testing.B) {
			opts := testOptions()
			opts.DisableTLS = !c.tls
			if c.keyed {
				opts.UpstreamsDir = "/var/lib/dock-upstreams"
			}
			routes := benchRoutes(c.keyed)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Render(opts, routes, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkApplyRoutesSwitch is a release cutover on a server with 450
// keyed services: one route moves to another port, which writes one
// upstream file (synced to disk) and loads the unchanged config, here
// into an admin socket that answers at once.
func BenchmarkApplyRoutesSwitch(b *testing.B) {
	dir, err := os.MkdirTemp("", "caddybench") // short path for the socket
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "admin.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		b.Fatal(err)
	}
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	s.Listener = l
	s.Start()
	b.Cleanup(s.Close)
	ups := filepath.Join(dir, "upstreams")
	if err := os.Mkdir(ups, 0o750); err != nil {
		b.Fatal(err)
	}
	opts := testOptions()
	opts.AdminSocket, opts.UpstreamsDir = sock, ups
	p, err := New(opts)
	if err != nil {
		b.Fatal(err)
	}
	routes := benchRoutes(true)
	ctx := context.Background()
	if err := p.ApplyRoutes(ctx, routes, nil); err != nil {
		b.Fatal(err)
	}
	b.Run("routes=500,io=disk", func(b *testing.B) {
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			i++
			routes[7].Upstream = fmt.Sprintf("127.0.0.1:%d", 21000+i%2)
			if err := p.ApplyRoutes(ctx, routes, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
}
