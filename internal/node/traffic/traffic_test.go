package traffic

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"testing"
	"time"
)

// line is a Caddy access log line as caddy.Render's filter leaves it.
func line(host, method, uri string, status int) []byte {
	return fmt.Appendf(nil, `{"level":"info","ts":1.7e9,"logger":"http.log.access.dock","msg":"handled request",`+
		`"request":{"method":%q,"host":%q,"uri":%q},"duration":0.002,"size":1000,"status":%d}`+"\n", method, host, uri, status)
}

func TestCounterAddsUpPerHostAndDrains(t *testing.T) {
	c := NewCounter()
	c.Add(line("shop.example.com", "GET", "/users/12?tab=1", 200))
	c.Add(line("shop.example.com:443", "GET", "/users/13", 0))
	c.Add(line("SHOP.example.com", "POST", "/cart", 302))
	c.Add(line("shop.example.com", "GET", "/missing", 404))
	c.Add(line("shop.example.com", "BREW", "/", 503))
	c.Add(line("api.example.com", "GET", "/", 200))
	c.Add([]byte("not json"))

	got, err := c.DrainTraffic(context.Background())
	if err != nil || len(got) != 2 || got[0].Host != "api.example.com" || got[1].Host != "shop.example.com" {
		t.Fatalf("drain = %+v, %v; want api and shop", got, err)
	}
	shop := got[1]
	if shop.Requests != 5 || shop.Status != [4]uint64{2, 1, 1, 1} || shop.Bytes != 5000 || shop.Duration != 10*time.Millisecond {
		t.Errorf("shop counts = %d %v %d %v", shop.Requests, shop.Status, shop.Bytes, shop.Duration)
	}
	want := []string{"GET /users/:id 2", "OTHER / 1", "POST /cart 1", "GET /missing 1"}
	if len(shop.Paths) != len(want) {
		t.Fatalf("paths = %+v", shop.Paths)
	}
	for i, p := range shop.Paths {
		if fmt.Sprintf("%s %s %d", p.Method, p.Path, p.Requests) != want[i] {
			t.Errorf("path %d = %+v, want %s", i, p, want[i])
		}
	}

	if again, _ := c.DrainTraffic(context.Background()); len(again) != 0 {
		t.Errorf("second drain = %+v, want nothing", again)
	}
}

// A scanner probing many paths can't push out a path that is really used.
func TestCounterKeepsBusyPathsPastTheCap(t *testing.T) {
	c := NewCounter()
	for range 100 {
		c.Add(line("a.example.com", "GET", "/home", 200))
	}
	for i := range 3 * maxPaths {
		c.Add(line("a.example.com", "GET", fmt.Sprintf("/probe-%c%c", 'a'+i%26, 'a'+i/26), 404))
	}
	got, _ := c.DrainTraffic(context.Background())
	if p := got[0].Paths; len(p) != DrainPaths || p[0].Path != "/home" || p[0].Requests != 100 {
		t.Fatalf("paths = %d, first %+v; want /home first with 100", len(p), p[0])
	}
}

func TestNormalizePath(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "/",
		"/":                      "/",
		"/api/v1/users/42/posts": "/api/v1/users/:id/posts",
		"/orders/550e8400-e29b-41d4-a716-446655440000":     "/orders/:id",
		"/commit/9fceb02d0ae598e95dc970b74767f19372d61af8": "/commit/:id",
		"/s/V1StGXR8Z5jdHi6BmyT2x":                         "/s/:id",
		"/blog/how-we-ship-2024-edition-of-dock":           "/blog/how-we-ship-2024-edition-of-dock",
		"/assets/index-BYhQ3k9a.js?v=1":                    "/assets/index-BYhQ3k9a.js",
		"/2024-01-05/report":                               "/2024-01-05/report",
		"/search#top":                                      "/search",
	} {
		if got := normalizePath(in); got != want {
			t.Errorf("normalizePath(%q) = %q, want %q", in, got, want)
		}
	}
	long := "/" + string(make([]byte, 300))
	if got := normalizePath(long); len(got) != maxPathLen {
		t.Errorf("long path kept %d bytes, want %d", len(got), maxPathLen)
	}
}

func TestListenCountsDatagrams(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := Listen(ctx, "127.0.0.1:0", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("udp", c.addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write(line("a.example.com", "GET", "/", 200)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := c.DrainTraffic(ctx)
		if len(got) == 1 && got[0].Requests == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("datagram not counted")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A traced request's line, with Caddy's trace and span IDs, goes to
// OnEdge as well as into the counts; an untraced one only counts.
func TestCounterSendsTracedRequests(t *testing.T) {
	c := NewCounter()
	var got []Edge
	c.OnEdge(func(e Edge) { got = append(got, e) })
	c.Add([]byte(`{"ts":1800000000.25,"request":{"method":"GET","host":"Shop.example.com:443","uri":"/orders/12?x=1"},"duration":0.05,"status":502,"traceID":"0102030405060708090a0b0c0d0e0f10","spanID":"0102030405060708"}`))
	c.Add([]byte(`{"request":{"method":"GET","host":"shop.example.com","uri":"/"},"status":200}`))
	if len(got) != 1 {
		t.Fatalf("edges = %+v", got)
	}
	e := got[0]
	if e.Host != "shop.example.com" || e.Method != "GET" || e.Route != "/orders/:id" || e.Status != 502 || e.Duration != 50*time.Millisecond ||
		e.End.UnixMilli() != 1_800_000_000_250 || e.TraceID != "0102030405060708090a0b0c0d0e0f10" || e.SpanID != "0102030405060708" {
		t.Errorf("edge = %+v", e)
	}
	if h, _ := c.DrainTraffic(t.Context()); len(h) != 1 || h[0].Requests != 2 {
		t.Errorf("counts = %+v", h)
	}
}
