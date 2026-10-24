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
func line(host, method, uri string, status int, ip, ua string) []byte {
	return fmt.Appendf(nil, `{"level":"info","ts":1.7e9,"logger":"http.log.access.dock","msg":"handled request",`+
		`"request":{"remote_ip":"127.0.0.1","client_ip":%q,"proto":"HTTP/2.0","method":%q,"host":%q,"uri":%q},`+
		`"duration":0.002,"size":1000,"status":%d,"ua":%q}`+"\n", ip, method, host, uri, status, ua)
}

const browser = "Mozilla/5.0 (Macintosh) Firefox/140.0"

func TestCounterAddsUpPerHostAndDrains(t *testing.T) {
	c := NewCounter()
	c.Add(line("shop.example.com", "GET", "/users/12?tab=1", 200, "1.1.1.1", browser))
	c.Add(line("shop.example.com:443", "GET", "/users/13", 0, "1.1.1.1", browser))
	c.Add(line("SHOP.example.com", "POST", "/cart", 302, "2.2.2.2", browser))
	c.Add(line("shop.example.com", "GET", "/missing", 404, "3.3.3.3", "Googlebot/2.1"))
	c.Add(line("shop.example.com", "BREW", "/", 503, "4.4.4.4", ""))
	c.Add(line("api.example.com", "GET", "/", 200, "1.1.1.1", browser))
	c.Add([]byte("not json"))

	got, err := c.DrainTraffic(context.Background())
	if err != nil || len(got) != 2 || got[0].Host != "api.example.com" || got[1].Host != "shop.example.com" {
		t.Fatalf("drain = %+v, %v; want api and shop", got, err)
	}
	shop := got[1]
	if shop.Requests != 5 || shop.Status != [4]uint64{2, 1, 1, 1} || shop.Bytes != 5000 || shop.Duration != 10*time.Millisecond {
		t.Errorf("shop counts = %d %v %d %v", shop.Requests, shop.Status, shop.Bytes, shop.Duration)
	}
	var s Sketch
	if err := s.UnmarshalBinary(shop.Visitors); err != nil || s.Estimate() != 2 {
		t.Errorf("shop visitors = %d, %v; want 2 (bots and empty user agents left out)", s.Estimate(), err)
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
		c.Add(line("a.example.com", "GET", "/home", 200, "1.1.1.1", browser))
	}
	for i := range 3 * maxPaths {
		c.Add(line("a.example.com", "GET", fmt.Sprintf("/probe-%c%c", 'a'+i%26, 'a'+i/26), 404, "9.9.9.9", "scanner"))
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

func TestSketchEstimateAndEncoding(t *testing.T) {
	for _, n := range []int{0, 1, 10, 1000, 100_000} {
		var s Sketch
		for i := range n {
			s.Add(Hash(fmt.Sprint("10.0.", i), browser))
			s.Add(Hash(fmt.Sprint("10.0.", i), browser)) // repeats don't count
		}
		est := float64(s.Estimate())
		if diff := est - float64(n); diff > 0.05*float64(n)+0.5 || -diff > 0.05*float64(n)+0.5 {
			t.Errorf("estimate of %d visitors = %v", n, est)
		}
		b, _ := s.MarshalBinary()
		var back Sketch
		if err := back.UnmarshalBinary(b); err != nil || back != s {
			t.Errorf("%d visitors: round trip = %v (%d bytes)", n, err, len(b))
		}
		if n <= 10 && len(b) > 1+3*n {
			t.Errorf("%d visitors encode to %d bytes; want the sparse form", n, len(b))
		}
	}

	var a, b Sketch
	for i := range 600 {
		a.Add(Hash(fmt.Sprint(i), ""))
		b.Add(Hash(fmt.Sprint(i+300), ""))
	}
	a.Merge(&b)
	if est := a.Estimate(); est < 855 || est > 945 {
		t.Errorf("merged estimate = %d, want about 900", est)
	}

	var s Sketch
	for _, bad := range [][]byte{{2}, {encDense, 1}, {encSparse, 0xff, 0xff, 1}, {encSparse, 0}} {
		if s.UnmarshalBinary(bad) == nil {
			t.Errorf("UnmarshalBinary(%v) = nil, want an error", bad)
		}
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
	if _, err := conn.Write(line("a.example.com", "GET", "/", 200, "1.1.1.1", browser)); err != nil {
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
