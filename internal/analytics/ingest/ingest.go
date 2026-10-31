// Package ingest takes analytics events from browsers (ADR 0037). Every
// daemon serves it on its own loopback listener, where Caddy sends
// /_dock/a/* of analytics-enabled hostnames, having set two headers from
// the routing table (TagHeader, ClientIPHeader). It serves the script,
// checks and enriches each event (bots dropped, the visitor hashed with
// the day's salt, the address cut to its /24 or /48, the user agent
// parsed, the channel worked out) and appends it to the server's spool. It
// keeps no state of its own.
package ingest

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/analytics/spool"
)

// Headers Caddy sets on every request it sends here, replacing any a
// visitor sent.
const (
	// TagHeader is "site.service.release", the IDs the hostname's route
	// stands for.
	TagHeader = "X-Dock-Analytics"
	// ClientIPHeader is the visitor's address as Caddy determined it.
	// Spelled canonically, so looking it up allocates nothing.
	ClientIPHeader = "X-Dock-Client-Ip"
)

// Paths, the same on every analytics-enabled hostname.
const (
	ScriptPath = "/_dock/a/s.js"
	EventPath  = "/_dock/a/e"
	// Prefix is what Caddy routes here.
	Prefix = "/_dock/a/"
)

// maxBody caps an event's request body.
const maxBody = 8 << 10

//go:embed s.js
var script []byte

// Ingest is the handler. Spool and Salts must be set.
type Ingest struct {
	Spool *spool.Spool
	Salts *Salts
	// Rules leave sites' excluded paths and ignored addresses out; nil
	// is none.
	Rules *Rules
	Log   *slog.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	stats Stats

	gzOnce sync.Once
	gz     []byte
	etag   string
}

// Stats counts what the ingest saw since it started.
type Stats struct {
	Accepted, Bots, Invalid, NoSalt, Excluded atomic.Int64
}

// Stats returns the counts.
func (in *Ingest) Stats() *Stats { return &in.stats }

// ServeHTTP serves the script and takes events.
func (in *Ingest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == ScriptPath && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		in.serveScript(w, r)
	case r.URL.Path == EventPath && r.Method == http.MethodPost:
		in.serveEvent(w, r)
	default:
		http.NotFound(w, r)
	}
}

// serveScript sends s.js, gzipped when the browser takes it, cacheable
// for an hour and revalidated by ETag after.
func (in *Ingest) serveScript(w http.ResponseWriter, r *http.Request) {
	in.gzOnce.Do(func() {
		var b bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
		_, _ = zw.Write(script)
		_ = zw.Close()
		in.gz = b.Bytes()
		in.etag = `"` + strconv.FormatUint(uint64(len(script)), 36) + "-" + strconv.FormatUint(fnv(script), 36) + `"`
	})
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=3600")
	h.Set("ETag", in.etag)
	h.Set("Vary", "Accept-Encoding")
	if r.Header.Get("If-None-Match") == in.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := script
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		body = in.gz
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// GzippedScriptSize is the script's size as served to browsers, for the
// budget (under 2 KB).
func GzippedScriptSize() int {
	var in Ingest
	in.serveScript(discard{}, &http.Request{Method: http.MethodHead, Header: http.Header{"Accept-Encoding": {"gzip"}}})
	return len(in.gz)
}

// discard is a ResponseWriter that keeps nothing.
type discard struct{}

func (discard) Header() http.Header         { return http.Header{} }
func (discard) Write(b []byte) (int, error) { return len(b), nil }
func (discard) WriteHeader(int)             {}

// fnv is FNV-1a, for the script's ETag.
func fnv(b []byte) uint64 {
	h := uint64(14695981039346656037)
	for _, c := range b {
		h = (h ^ uint64(c)) * 1099511628211
	}
	return h
}

// beacon is what the script sends.
type beacon struct {
	Kind     int            `json:"k"`
	URL      string         `json:"u"`
	Referrer string         `json:"r"`
	Name     string         `json:"n"`
	Props    jsontext.Value `json:"p"`
	Width    int            `json:"w"`
	Height   int            `json:"h"`
	Lang     string         `json:"l"`
}

// bufs are reused for request bodies and spooled lines.
var bufs = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// serveEvent takes one event. It answers 202 whether or not the event is
// kept (bots, events without a salt), so a page learns nothing from it;
// 400 is for requests the script never sends.
func (in *Ingest) serveEvent(w http.ResponseWriter, r *http.Request) {
	body := bufs.Get().(*bytes.Buffer)
	defer bufs.Put(body)
	body.Reset()
	if _, err := body.ReadFrom(io.LimitReader(r.Body, maxBody+1)); err != nil || body.Len() > maxBody {
		in.reject(w)
		return
	}
	var e analytics.Event
	keep, ok := in.event(r, body.Bytes(), &e)
	if !ok {
		in.reject(w)
		return
	}
	if !keep {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	line := bufs.Get().(*bytes.Buffer)
	defer bufs.Put(line)
	line.Reset()
	if err := json.MarshalWrite(line, &e); err != nil {
		in.reject(w)
		return
	}
	if err := in.Spool.Append(line.Bytes()); err != nil {
		if in.Log != nil {
			in.Log.Error("analytics spool", "err", err)
		}
		http.Error(w, "the event couldn't be kept", http.StatusServiceUnavailable)
		return
	}
	in.stats.Accepted.Add(1)
	w.WriteHeader(http.StatusAccepted)
}

// reject answers a request the script wouldn't send.
func (in *Ingest) reject(w http.ResponseWriter) {
	in.stats.Invalid.Add(1)
	http.Error(w, "not a Dock Analytics event", http.StatusBadRequest)
}

// Limits on what an event may carry.
const (
	maxPath     = 512
	maxName     = 120
	maxOutbound = 500
	maxUTM      = 120
	maxLang     = 35
	maxProps    = 2 << 10
)

// event fills e with the event a request carries. ok is false for a
// malformed request; keep is false for one that isn't kept (a bot, no
// salt).
func (in *Ingest) event(r *http.Request, body []byte, e *analytics.Event) (keep, ok bool) {
	site, service, release, ok := parseTag(r.Header.Get(TagHeader))
	if !ok {
		return false, false
	}
	var b beacon
	if err := json.Unmarshal(body, &b); err != nil || b.Kind < 1 || b.Kind > 3 {
		return false, false
	}
	host := hostOnly(r.Host)
	u, err := url.Parse(b.URL)
	if err != nil || !strings.EqualFold(hostOnly(u.Host), host) {
		return false, false
	}
	*e = analytics.Event{Site: site, Service: service, Release: release, Hostname: host, Kind: analytics.Kind(b.Kind)}
	switch e.Kind {
	case analytics.KindEvent:
		if b.Name == "" {
			return false, false
		}
		e.Name = cut(b.Name, maxName)
		props, ok := cleanProps(b.Props)
		if !ok {
			return false, false
		}
		e.Props = props
	case analytics.KindOutbound:
		e.Name = cut(b.Name, maxOutbound)
	}

	agent := ParseAgent(r.Header.Get("User-Agent"))
	if agent.Bot {
		in.stats.Bots.Add(1)
		return false, true
	}
	now := time.Now
	if in.Now != nil {
		now = in.Now
	}
	t := now()
	salt, ok := in.Salts.get(Day(t))
	if !ok {
		// The control plane hasn't handed out today's salt yet: there is
		// no way to count this visitor without one.
		in.stats.NoSalt.Add(1)
		return false, true
	}
	ip := r.Header.Get(ClientIPHeader)
	path := cut(cmpStr(u.EscapedPath(), "/"), maxPath)
	if in.Rules.skip(site, path, ip) {
		in.stats.Excluded.Add(1)
		return false, true
	}
	e.Time = t.UnixMilli()
	e.Visitor = visitorID(salt, site, ip, r.Header.Get("User-Agent"))
	e.IPPrefix = ipPrefix(ip)
	e.Path = path
	if strings.Contains(u.RawQuery, "utm_") {
		utm(u.RawQuery, e)
	}
	if rh := referrerHost(b.Referrer); rh != "" && !sameSite(rh, host) {
		e.Referrer = cut(rh, 253)
	}
	if e.Kind == analytics.KindPageview {
		e.Channel = Channel(e.Referrer, e.UTMSource, e.UTMMedium)
	}
	e.Browser, e.BrowserVersion, e.OS, e.OSVersion, e.Device = agent.Browser, agent.BrowserVersion, agent.OS, agent.OSVersion, agent.Device
	e.ScreenW, e.ScreenH = uint16(min(max(b.Width, 0), 65535)), uint16(min(max(b.Height, 0), 65535))
	e.Language = cut(b.Lang, maxLang)
	return true, true
}

// utm reads the UTM parameters of a raw query into e, unescaping only
// values that need it.
func utm(query string, e *analytics.Event) {
	for query != "" {
		var kv string
		kv, query, _ = strings.Cut(query, "&")
		k, v, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(k, "utm_") {
			continue
		}
		if strings.ContainsAny(v, "%+") {
			if u, err := url.QueryUnescape(v); err == nil {
				v = u
			}
		}
		v = cut(v, maxUTM)
		switch k {
		case "utm_source":
			e.UTMSource = v
		case "utm_medium":
			e.UTMMedium = v
		case "utm_campaign":
			e.UTMCampaign = v
		case "utm_term":
			e.UTMTerm = v
		case "utm_content":
			e.UTMContent = v
		}
	}
}

// referrerHost is the lower-case host of a referrer URL
// ("https://host:port/path"), without parsing the rest of it.
func referrerHost(ref string) string {
	_, rest, ok := strings.Cut(ref, "://")
	if !ok {
		return ""
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndexByte(rest, '@'); i >= 0 {
		rest = rest[i+1:]
	}
	h := hostOnly(rest)
	for i := range len(h) {
		if c := h[i]; c >= 'A' && c <= 'Z' {
			return strings.ToLower(h)
		}
	}
	return h
}

// parseTag reads "site.service.release".
func parseTag(tag string) (site, service, release int64, ok bool) {
	a, rest, ok1 := strings.Cut(tag, ".")
	b, c, ok2 := strings.Cut(rest, ".")
	if !ok1 || !ok2 {
		return 0, 0, 0, false
	}
	var err1, err2, err3 error
	site, err1 = strconv.ParseInt(a, 10, 64)
	service, err2 = strconv.ParseInt(b, 10, 64)
	release, err3 = strconv.ParseInt(c, 10, 64)
	return site, service, release, err1 == nil && err2 == nil && err3 == nil && site > 0
}

// cleanProps re-encodes a custom event's properties: an object of at
// most analytics.MaxProps string, number or boolean values.
func cleanProps(raw jsontext.Value) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", true
	}
	var props map[string]any
	if err := json.Unmarshal(raw, &props); err != nil || len(props) > analytics.MaxProps {
		return "", false
	}
	for k, v := range props {
		switch v.(type) {
		case string, float64, bool:
		default:
			return "", false
		}
		if len(k) > 64 {
			return "", false
		}
	}
	b, err := json.Marshal(props, json.Deterministic(true))
	if err != nil || len(b) > maxProps {
		return "", false
	}
	return string(b), true
}

// ipPrefix cuts an address to its /24 (IPv6: /48), for the location
// lookup only.
func ipPrefix(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	a = a.Unmap()
	bits := 24
	if a.Is6() {
		bits = 48
	}
	p, err := a.Prefix(bits)
	if err != nil {
		return ""
	}
	return p.Addr().String()
}

// hostOnly is a host without its port.
func hostOnly(h string) string {
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return strings.TrimSuffix(h, ".")
}

// sameSite reports whether a referrer's host is the site's own: the same
// hostname with or without "www.".
func sameSite(ref, host string) bool {
	host = strings.ToLower(host)
	return ref == host || strings.TrimPrefix(ref, "www.") == strings.TrimPrefix(host, "www.")
}

// cut caps s at n bytes, on a rune boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xc0 == 0x80 {
		n--
	}
	return s[:n]
}

// cmpStr is s, or def when s is empty.
func cmpStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
