package inject

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/klauspost/compress/gzip"
)

const page = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>x</title></head><body><header>hi</header></body></html>`

// app answers like an upstream: body in chunks of size bytes, with
// headers set by hdr, and records the request it got.
type app struct {
	status int
	hdr    http.Header
	body   string
	bytes  []byte // the body, if set in place of body
	chunk  int
	got    *http.Request
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) error {
	a.got = r
	for k, v := range a.hdr {
		w.Header()[k] = v
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	w.WriteHeader(a.status)
	b := a.bytes
	if b == nil {
		b = []byte(a.body)
	}
	for len(b) > 0 {
		n := min(len(b), a.chunk)
		if _, err := w.Write(b[:n]); err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

// serve runs req through a dock_inject for example.com in front of a.
func serve(t *testing.T, a *app, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	h := &Handler{Hosts: []string{"Example.com"}, Src: "/_dock/a/s.js"}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := h.Provision(caddyCtx()); err != nil {
		t.Fatal(err)
	}
	if a.status == 0 {
		a.status = 200
	}
	if a.chunk == 0 {
		a.chunk = len(a.body) + 1
	}
	rec := httptest.NewRecorder()
	if err := h.ServeHTTP(rec, req, caddyhttp.HandlerFunc(a.ServeHTTP)); err != nil {
		t.Fatal(err)
	}
	return rec
}

func pageLoad(host string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://"+host+"/", nil)
	r.Header.Set("Sec-Fetch-Dest", "document")
	return r
}

func body(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Header().Get("Content-Encoding") != "gzip" {
		return rec.Body.String()
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const tag = `<script src="/_dock/a/s.js" defer data-dock-auto></script>`

func TestInjectsAfterHead(t *testing.T) {
	want := strings.Replace(page, "<head>", "<head>"+tag, 1)
	for _, chunk := range []int{1, 3, 7, 4096} {
		a := &app{body: page, chunk: chunk, hdr: http.Header{"Content-Length": {"999"}, "Etag": {`"v1"`}, "Accept-Ranges": {"bytes"}}}
		rec := serve(t, a, pageLoad("example.com:443"))
		if got := body(t, rec); got != want {
			t.Fatalf("chunk %d: body\n%s\nwant\n%s", chunk, got, want)
		}
		h := rec.Header()
		if h.Get("Content-Length") != "" || h.Get("Accept-Ranges") != "" || h.Get("Etag") != `W/"v1"` || h.Get("Vary") != "Accept-Encoding" {
			t.Fatalf("chunk %d: headers %v", chunk, h)
		}
	}
}

func TestHeadWithAttributesNotHeader(t *testing.T) {
	p := `<html><HEADER></HEADER><Head data-x="1"><title>x</title>`
	rec := serve(t, &app{body: p, chunk: 5}, pageLoad("example.com"))
	if got, want := body(t, rec), strings.Replace(p, `<Head data-x="1">`, `<Head data-x="1">`+tag, 1); got != want {
		t.Fatalf("got %s", got)
	}
}

func TestGzipsForVisitorsWhoTakeIt(t *testing.T) {
	r := pageLoad("example.com")
	r.Header.Set("Accept-Encoding", "br, gzip;q=0.8")
	a := &app{body: page}
	rec := serve(t, a, r)
	if a.got.Header.Get("Accept-Encoding") != "" {
		t.Fatal("the upstream was asked for a compressed page")
	}
	if rec.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(body(t, rec), tag) {
		t.Fatalf("headers %v", rec.Header())
	}
	r.Header.Set("Accept-Encoding", "gzip;q=0")
	if rec := serve(t, &app{body: page}, r); rec.Header().Get("Content-Encoding") != "" {
		t.Fatal("gzipped for a visitor who refused it")
	}
}

func TestLeftAlone(t *testing.T) {
	post := pageLoad("example.com")
	post.Method = http.MethodPost
	fragment := pageLoad("example.com")
	fragment.Header.Set("Sec-Fetch-Dest", "empty")
	ranged := pageLoad("example.com")
	ranged.Header.Set("Range", "bytes=0-10")
	for name, c := range map[string]struct {
		req *http.Request
		a   *app
	}{
		"other host":    {pageLoad("other.com"), &app{body: page}},
		"post":          {post, &app{body: page}},
		"fragment":      {fragment, &app{body: page}},
		"range":         {ranged, &app{body: page}},
		"not found":     {pageLoad("example.com"), &app{status: 404, body: page}},
		"json":          {pageLoad("example.com"), &app{body: page, hdr: http.Header{"Content-Type": {"application/json"}}}},
		"compressed":    {pageLoad("example.com"), &app{body: page, hdr: http.Header{"Content-Encoding": {"br"}}}},
		"no-transform":  {pageLoad("example.com"), &app{body: page, hdr: http.Header{"Cache-Control": {"public, No-Transform"}}}},
		"csp":           {pageLoad("example.com"), &app{body: page, hdr: http.Header{"Content-Security-Policy": {"script-src https://cdn.example.net"}}}},
		"no head":       {pageLoad("example.com"), &app{body: "<p>hello</p>"}},
		"head too late": {pageLoad("example.com"), &app{body: strings.Repeat(" ", maxScan) + page, chunk: 1000}},
	} {
		rec := serve(t, c.a, c.req)
		if got := body(t, rec); strings.Contains(got, "data-dock-auto") || !strings.HasSuffix(got, c.a.body) {
			t.Errorf("%s: changed: %.200s", name, got)
		}
	}
}

func TestFlushBeforeHeadGivesUp(t *testing.T) {
	h := &Handler{Hosts: []string{"example.com"}, Src: "/_dock/a/s.js"}
	_ = h.Provision(caddyCtx())
	rec := httptest.NewRecorder()
	err := h.ServeHTTP(rec, pageLoad("example.com"), caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<!doctype html><ht")
		if err := http.NewResponseController(w).Flush(); err != nil {
			return err
		}
		_, err := io.WriteString(w, "ml><head></head>")
		return err
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.Body.String(); got != "<!doctype html><html><head></head>" || !rec.Flushed {
		t.Fatalf("got %q, flushed %v", got, rec.Flushed)
	}
}

func TestCSP(t *testing.T) {
	for _, c := range []struct {
		headers []string
		nonce   string
		ok      bool
	}{
		{nil, "", true},
		{[]string{"img-src 'self'"}, "", true},
		{[]string{"default-src 'self'"}, "", true},
		{[]string{"default-src 'none'"}, "", false},
		{[]string{"default-src 'self'; script-src https://cdn.example.net"}, "", false},
		{[]string{"script-src 'self'; script-src-elem 'none'"}, "", false},
		{[]string{"script-src 'nonce-abc123' 'strict-dynamic'"}, "abc123", true},
		{[]string{"script-src 'self' 'strict-dynamic'"}, "", false},
		{[]string{"script-src 'nonce-a\"b'"}, "", false},
		{[]string{"script-src 'self'", "script-src 'nonce-n1'"}, "n1", true},
		{[]string{"script-src 'self', script-src https://x"}, "", false},
	} {
		nonce, ok := cspNonce(c.headers)
		if nonce != c.nonce || ok != c.ok {
			t.Errorf("%q: got %q %v, want %q %v", c.headers, nonce, ok, c.nonce, c.ok)
		}
	}
	a := &app{body: page, hdr: http.Header{"Content-Security-Policy": {"script-src 'nonce-n0Nce' 'strict-dynamic'"}}}
	if got := body(t, serve(t, a, pageLoad("example.com"))); !strings.Contains(got, `data-dock-auto nonce="n0Nce"></script>`) {
		t.Fatalf("no nonce: %s", got)
	}
}

func TestAcceptsGzip(t *testing.T) {
	for v, want := range map[string]bool{"gzip": true, "deflate, GZIP": true, "*": true, "gzip;q=0": false, "gzip; q=0.000": false, "gzip;q=0.5": true, "br": false, "": false} {
		if got := acceptsGzip([]string{v}); got != want {
			t.Errorf("%q: %v", v, got)
		}
	}
}

func TestValidate(t *testing.T) {
	for _, src := range []string{"", "s.js", `/a"onload="x`, "/a b"} {
		if (&Handler{Src: src}).Validate() == nil {
			t.Errorf("%q passed", src)
		}
	}
}

// caddyCtx is the zero caddy.Context: Provision reads nothing from it.
func caddyCtx() caddy.Context { return caddy.Context{} }

func BenchmarkInject(b *testing.B) {
	big := strings.Replace(page, "<body>", "<body>"+strings.Repeat("<p>Lorem ipsum dolor sit amet</p>", 1000), 1)
	h := &Handler{Hosts: []string{"example.com"}, Src: "/_dock/a/s.js"}
	_ = h.Provision(caddyCtx())
	for _, enc := range []string{"", "gzip"} {
		b.Run("encoding="+enc, func(b *testing.B) {
			a := &app{status: 200, bytes: []byte(big), chunk: 4096}
			req := pageLoad("example.com")
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
