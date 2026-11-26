// Package inject is Caddy's dock_inject handler (ADR 0105): it adds Dock
// Analytics' script tag to the HTML pages of the hostnames it is given, so
// an app counts visits without adding the tag itself.
//
// Dock renders it into a route's handler chain, in front of the
// service's reverse_proxy:
//
//	{"handler": "dock_inject", "hosts": ["example.com"], "src": "/_dock/a/s.js"}
//
// Only page loads change: a GET for a document (Sec-Fetch-Dest document,
// iframe or none sent) on one of the hosts, answered 200 with text/html
// that isn't compressed, marked no-transform or barred by its
// Content-Security-Policy. The tag goes right after the opening <head>
// tag, found within the first maxScan bytes, and the page is streamed
// through, never held whole. The upstream is asked for an uncompressed
// page, which is gzipped here when the visitor takes gzip.
//
// A changed page's ETag gets a -dock suffix, and a revalidation passes the
// upstream only tags with it (taken off again): a page a browser kept from
// before the script was added is sent again whole, with the script, never
// confirmed with a 304. If-Modified-Since never reaches the upstream, and
// changed pages lose Last-Modified, for the same reason.
package inject

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/klauspost/compress/gzip"
)

func init() {
	caddy.RegisterModule(Handler{})
}

// maxScan is how far into a page the opening <head> tag is looked for;
// past it the page goes out unchanged.
const maxScan = 16 << 10

// gzipLevel trades a little size for speed: pages are compressed on every
// load.
const gzipLevel = 5

// Handler adds the script tag to pages on Hosts.
type Handler struct {
	// Hosts are the hostnames whose pages get the script; requests for
	// other hosts pass through untouched.
	Hosts []string `json:"hosts,omitempty"`
	// Src is the script's address, a path on the page's own host.
	Src string `json:"src,omitempty"`

	hosts map[string]bool
}

// CaddyModule registers the handler as http.handlers.dock_inject.
func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.dock_inject",
		New: func() caddy.Module { return new(Handler) },
	}
}

// Provision indexes Hosts.
func (h *Handler) Provision(caddy.Context) error {
	h.hosts = make(map[string]bool, len(h.Hosts))
	for _, host := range h.Hosts {
		h.hosts[strings.ToLower(host)] = true
	}
	return nil
}

// Validate refuses a Src that isn't a plain path: it goes into an HTML
// attribute as it is.
func (h *Handler) Validate() error {
	if !strings.HasPrefix(h.Src, "/") || strings.ContainsAny(h.Src, "\"'<> &") {
		return errors.New("dock_inject: src must be a path like /_dock/a/s.js")
	}
	return nil
}

// ServeHTTP passes the request on, rewriting the response of a page load.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if !h.ours(r) || r.Method != http.MethodGet {
		return next.ServeHTTP(w, r)
	}
	if !pageLoad(r) {
		// A fragment of the same address mustn't be cached for the page,
		// or the page for it.
		if r.Header.Get("Sec-Fetch-Dest") == "empty" {
			w.Header().Add("Vary", "Sec-Fetch-Dest")
		}
		return next.ServeHTTP(w, r)
	}
	gz := acceptsGzip(r.Header.Values("Accept-Encoding"))
	// An uncompressed page can be changed as it streams; gzip comes back
	// on here.
	r.Header.Del("Accept-Encoding")
	r.Header.Del("If-Modified-Since")
	if tags := upstreamTags(r.Header.Values("If-None-Match")); tags != "" {
		r.Header.Set("If-None-Match", tags)
	} else {
		r.Header.Del("If-None-Match")
	}
	iw := &writer{ResponseWriter: w, src: h.Src, gzip: gz}
	err := next.ServeHTTP(iw, r)
	if cerr := iw.close(); err == nil {
		err = cerr
	}
	return err
}

// pageLoad reports whether a GET loads a whole page.
func pageLoad(r *http.Request) bool {
	if r.Header.Get("Range") != "" || r.Header.Get("Upgrade") != "" {
		return false
	}
	// htmx, Turbo and fetch() ask for HTML fragments with "empty"; only
	// whole documents get the script.
	switch r.Header.Get("Sec-Fetch-Dest") {
	case "", "document", "iframe":
		return true
	}
	return false
}

// ours reports whether r is for one of the hosts.
func (h *Handler) ours(r *http.Request) bool {
	host := r.Host
	if hp, _, err := net.SplitHostPort(host); err == nil {
		host = hp
	}
	return h.hosts[strings.ToLower(host)]
}

// writer rewrites one response: it decides at WriteHeader whether the
// page gets the tag, then holds the start of the body until the opening
// <head> tag has gone by.
type writer struct {
	http.ResponseWriter
	src  string
	gzip bool // the visitor takes gzip

	wroteHeader bool
	rewrite     bool // this response gets the tag
	tag         []byte
	searching   bool   // the tag isn't in yet
	held        []byte // the body so far, while searching
	from        int    // where in held to look for <head next
	out         io.Writer
	zw          *gzip.Writer
}

// gzips are pooled: a writer's state is a few hundred KB.
var gzips sync.Pool

// WriteHeader decides whether the response is a page that gets the tag
// and fixes its headers for that.
func (w *writer) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	if code < 200 {
		// 103 Early Hints and the like; the real status follows.
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.wroteHeader = true
	hdr := w.Header()
	w.out = w.ResponseWriter
	if code == http.StatusNotModified {
		// The upstream matched a tag of a changed page: the browser keeps it.
		if etag := hdr.Get("ETag"); etag != "" {
			hdr.Set("ETag", dockTag(etag))
		}
		w.ResponseWriter.WriteHeader(code)
		return
	}
	nonce, allowed := cspNonce(hdr.Values("Content-Security-Policy"))
	w.rewrite = code == http.StatusOK && allowed && isHTML(hdr.Get("Content-Type")) &&
		hdr.Get("Content-Encoding") == "" && !hasToken(hdr.Values("Cache-Control"), "no-transform")
	if w.rewrite {
		w.searching = true
		w.tag = scriptTag(w.src, nonce)
		hdr.Del("Content-Length")
		hdr.Del("Accept-Ranges")
		if etag := hdr.Get("ETag"); etag != "" {
			hdr.Set("ETag", dockTag(etag))
		}
		hdr.Del("Last-Modified")
		for _, v := range []string{"Accept-Encoding", "Sec-Fetch-Dest"} {
			if !hasToken(hdr.Values("Vary"), v) {
				hdr.Add("Vary", v)
			}
		}
		if w.gzip {
			hdr.Set("Content-Encoding", "gzip")
			zw, _ := gzips.Get().(*gzip.Writer)
			if zw == nil {
				zw, _ = gzip.NewWriterLevel(w.ResponseWriter, gzipLevel)
			} else {
				zw.Reset(w.ResponseWriter)
			}
			w.zw = zw
			w.out = zw
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write passes the body on, putting the tag in after the opening <head>
// tag.
func (w *writer) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if !w.searching {
		return w.out.Write(p)
	}
	// Usually the tag is in the first write, which is then never copied.
	buf := p
	if len(w.held) > 0 {
		w.held = append(w.held, p...)
		buf = w.held
	}
	end, from := headEnd(buf[:min(len(buf), maxScan)], w.from)
	switch {
	case end >= 0:
		return len(p), w.release(buf[:end], w.tag, buf[end:])
	case len(buf) >= maxScan:
		return len(p), w.release(buf)
	}
	if len(w.held) == 0 {
		w.held = append([]byte(nil), p...) // p isn't ours to keep
	}
	w.from = from
	return len(p), nil
}

// release writes the held body (with the tag, or given up on) and stops
// searching.
func (w *writer) release(parts ...[]byte) error {
	w.searching = false
	for _, b := range parts {
		if _, err := w.out.Write(b); err != nil {
			return err
		}
	}
	w.held = nil
	return nil
}

// FlushError sends what there is: a page that flushes before its <head>
// tag goes out unchanged.
func (w *writer) FlushError() error {
	if w.searching {
		if err := w.release(w.held); err != nil {
			return err
		}
	}
	if w.zw != nil {
		if err := w.zw.Flush(); err != nil {
			return err
		}
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the connection underneath.
func (w *writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// close ends the body: a page without a <head> tag goes out as it was.
func (w *writer) close() error {
	var err error
	if w.searching {
		err = w.release(w.held)
	}
	if w.zw != nil {
		if cerr := w.zw.Close(); err == nil {
			err = cerr
		}
		w.zw.Reset(io.Discard)
		gzips.Put(w.zw)
		w.zw = nil
	}
	return err
}

// headEnd finds the end of the opening <head> tag in b, looking for its
// start from from on, past comments and quoted attribute values. It
// returns the index just past its ">", or -1 and where to look again once
// more of the page is in.
func headEnd(b []byte, from int) (end, next int) {
	for i := from; i < len(b); i++ {
		if b[i] != '<' {
			continue
		}
		if len(b)-i < 6 {
			return -1, i // "<head" and the byte after it, or "<!--", aren't all in
		}
		if string(b[i+1:i+4]) == "!--" {
			e := bytes.Index(b[i+4:], []byte("-->"))
			if e < 0 {
				return -1, i
			}
			i += 4 + e + 2
			continue
		}
		if !strings.EqualFold(string(b[i+1:i+5]), "head") || !endsName(b[i+5]) {
			continue
		}
		var quote byte
		for j := i + 5; j < len(b); j++ {
			switch c := b[j]; {
			case quote != 0:
				if c == quote {
					quote = 0
				}
			case c == '"' || c == '\'':
				quote = c
			case c == '>':
				return j + 1, i
			}
		}
		return -1, i
	}
	return -1, len(b)
}

// endsName reports whether c ends a tag name ("<head>" but not "<header>").
func endsName(c byte) bool {
	switch c {
	case '>', ' ', '\t', '\n', '\r', '\f', '/':
		return true
	}
	return false
}

// scriptTag is the tag put into pages. data-dock-auto tells s.js it was
// added here, so it gives way to a tag the page has itself.
func scriptTag(src, nonce string) []byte {
	t := `<script src="` + src + `" defer data-dock-auto`
	if nonce != "" {
		t += ` nonce="` + nonce + `"`
	}
	return []byte(t + `></script>`)
}

// isHTML reports whether a Content-Type is text/html.
func isHTML(ct string) bool {
	mt, _, _ := strings.Cut(ct, ";")
	return strings.EqualFold(strings.TrimSpace(mt), "text/html")
}

// hasToken reports whether comma-separated header values hold token,
// ignoring case and parameters.
func hasToken(values []string, token string) bool {
	for _, v := range values {
		for t := range strings.SplitSeq(v, ",") {
			t, _, _ = strings.Cut(t, "=")
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

// acceptsGzip reports whether Accept-Encoding takes gzip: named without
// q=0, or not named and * without q=0.
func acceptsGzip(values []string) bool {
	named, star := 0, 0 // 0 not listed, 1 refused, 2 taken
	for _, v := range values {
		for t := range strings.SplitSeq(v, ",") {
			name, params, _ := strings.Cut(t, ";")
			name = strings.TrimSpace(name)
			q := strings.ReplaceAll(params, " ", "")
			took := 2
			if q == "q=0" || strings.HasPrefix(q, "q=0.") && strings.Trim(q[4:], "0") == "" {
				took = 1
			}
			switch {
			case strings.EqualFold(name, "gzip"):
				named = took
			case name == "*":
				star = took
			}
		}
	}
	if named != 0 {
		return named == 2
	}
	return star == 2
}

// dockTag is the ETag of a changed page: the upstream's, weak, with -dock
// at the end of its value; "" for a malformed one.
func dockTag(etag string) string {
	v := strings.TrimPrefix(etag, "W/")
	if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		return ""
	}
	return `W/"` + v[1:len(v)-1] + `-dock"`
}

// upstreamTags are the If-None-Match tags of changed pages (dockTag), as
// the upstream made them; other tags are of pages from before the script,
// and are left out.
func upstreamTags(values []string) string {
	var out []string
	for _, v := range values {
		for t := range strings.SplitSeq(v, ",") {
			t = strings.TrimSpace(t)
			if inner, ok := strings.CutPrefix(t, `W/"`); ok && strings.HasSuffix(inner, `-dock"`) {
				out = append(out, `W/"`+strings.TrimSuffix(inner, `-dock"`)+`"`)
			}
		}
	}
	return strings.Join(out, ", ")
}

// Interface guards.
var (
	_ caddy.Provisioner           = (*Handler)(nil)
	_ caddy.Validator             = (*Handler)(nil)
	_ caddyhttp.MiddlewareHandler = (*Handler)(nil)
)
