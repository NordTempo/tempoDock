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

func visit(host string) *http.Request {
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
		a := &app{body: page, chunk: chunk, hdr: http.Header{"Content-Length": {"999"}, "Etag": {`"v1"`}, "Accept-Ranges": {"bytes"}, "Last-Modified": {"Mon, 02 Jan 2006 15:04:05 GMT"}}}
		rec := serve(t, a, visit("example.com:443"))
		if got := body(t, rec); got != want {
			t.Fatalf("chunk %d: body\n%s\nwant\n%s", chunk, got, want)
		}
		h := rec.Header()
		if h.Get("Content-Length") != "" || h.Get("Accept-Ranges") != "" || h.Get("Last-Modified") != "" || h.Get("Etag") != `W/"v1-dock"` ||
			strings.Join(h.Values("Vary"), ", ") != "Accept-Encoding, Sec-Fetch-Dest" {
			t.Fatalf("chunk %d: headers %v", chunk, h)
		}
	}
}

func TestHeadWithAttributesNotHeader(t *testing.T) {
	p := `<html><!-- <head> --><HEADER></HEADER><Head data-x=">" data-y='>'><title>x</title>`
	for _, chunk := range []int{1, 5, 4096} {
		rec := serve(t, &app{body: p, chunk: chunk}, visit("example.com"))
		if got, want := body(t, rec), strings.Replace(p, `data-y='>'>`, `data-y='>'>`+tag, 1); got != want {
			t.Fatalf("chunk %d: got %s", chunk, got)
		}
	}
}

// A page kept from before the script was added is sent again whole;
// one kept with it is confirmed.
func TestRevalidation(t *testing.T) {
	r := visit("example.com")
	r.Header.Set("If-None-Match", `"v1", W/"v2,b-dock"`)
	r.Header.Set("If-Modified-Since", "Mon, 02 Jan 2006 15:04:05 GMT")
	a := &app{status: http.StatusNotModified, hdr: http.Header{"Etag": {`"v2"`}}}
	rec := serve(t, a, r)
	if got := a.got.Header.Get("If-None-Match"); got != `W/"v2,b"` || a.got.Header.Get("If-Modified-Since") != "" {
		t.Errorf("the upstream got If-None-Match %q, If-Modified-Since %q", got, a.got.Header.Get("If-Modified-Since"))
	}
	if rec.Code != http.StatusNotModified || rec.Header().Get("Etag") != `W/"v2-dock"` {
		t.Errorf("304 with ETag %q", rec.Header().Get("Etag"))
	}
	r.Header.Set("If-None-Match", `"v1"`)
	b := &app{body: page}
	if serve(t, b, r); b.got.Header.Get("If-None-Match") != "" {
		t.Errorf("a tag from before the script reached the upstream: %q", b.got.Header.Get("If-None-Match"))
	}
}

// infoRecorder takes 1xx responses as net/http does: not final.
type infoRecorder struct{ *httptest.ResponseRecorder }

func (r infoRecorder) WriteHeader(code int) {
	if code >= 200 {
		r.ResponseRecorder.WriteHeader(code)
	}
}

// HTML that isn't a page load varies on Sec-Fetch-Dest, so a cache keeps
// it apart from the page, also after a 103; other responses don't.
func TestNotPageLoadsVary(t *testing.T) {
	for _, dest := range []string{"empty", "object"} {
		r := visit("example.com")
		r.Header.Set("Sec-Fetch-Dest", dest)
		h := &Handler{Hosts: []string{"example.com"}, Src: "/_dock/a/s.js"}
		_ = h.Provision(caddyCtx())
		rec := httptest.NewRecorder()
		err := h.ServeHTTP(infoRecorder{rec}, r, caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
			w.WriteHeader(http.StatusEarlyHints)
			for k := range w.Header() {
				delete(w.Header(), k) // as Caddy's proxy does after a 1xx
			}
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Vary", "Cookie")
			_, err := io.WriteString(w, page)
			return err
		}))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(rec.Header().Values("Vary"), ", "); got != "Cookie, Sec-Fetch-Dest" || strings.Contains(rec.Body.String(), "data-dock-auto") {
			t.Errorf("%s: Vary %q", dest, got)
		}
	}
	r := visit("example.com")
	r.Header.Set("Sec-Fetch-Dest", "image")
	if rec := serve(t, &app{body: "png", hdr: http.Header{"Content-Type": {"image/png"}}}, r); rec.Header().Get("Vary") != "" {
		t.Errorf("an image varies: %q", rec.Header().Get("Vary"))
	}
}

func TestGzipsForVisitorsWhoTakeIt(t *testing.T) {
	r := visit("example.com")
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
	post := visit("example.com")
	post.Method = http.MethodPost
	fragment := visit("example.com")
	fragment.Header.Set("Sec-Fetch-Dest", "empty")
	ranged := visit("example.com")
	ranged.Header.Set("Range", "bytes=0-10")
	for name, c := range map[string]struct {
		req *http.Request
		a   *app
	}{
		"other host":    {visit("other.com"), &app{body: page}},
		"post":          {post, &app{body: page}},
		"fragment":      {fragment, &app{body: page}},
		"range":         {ranged, &app{body: page}},
		"not found":     {visit("example.com"), &app{status: 404, body: page}},
		"json":          {visit("example.com"), &app{body: page, hdr: http.Header{"Content-Type": {"application/json"}}}},
		"compressed":    {visit("example.com"), &app{body: page, hdr: http.Header{"Content-Encoding": {"br"}}}},
		"no-transform":  {visit("example.com"), &app{body: page, hdr: http.Header{"Cache-Control": {"public, No-Transform"}}}},
		"csp":           {visit("example.com"), &app{body: page, hdr: http.Header{"Content-Security-Policy": {"script-src https://cdn.example.net"}}}},
		"no head":       {visit("example.com"), &app{body: "<p>hello</p>"}},
		"head too late": {visit("example.com"), &app{body: strings.Repeat(" ", maxScan) + page, chunk: 1000}},
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
	err := h.ServeHTTP(rec, visit("example.com"), caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
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
	if got := body(t, serve(t, a, visit("example.com"))); !strings.Contains(got, `data-dock-auto nonce="n0Nce"></script>`) {
		t.Fatalf("no nonce: %s", got)
	}
}

func TestAcceptsGzip(t *testing.T) {
	for v, want := range map[string]bool{"gzip": true, "deflate, GZIP": true, "*": true, "gzip;q=0": false, "gzip; q=0.000": false, "gzip;q=0.5": true, "br": false, "": false, "gzip;q=0, *": false, "*;q=0, gzip": true} {
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
