// Package ingest takes analytics events from browsers (ADR 0037). Every
// daemon serves it on its own loopback listener, where Caddy sends
// /_dock/a/* of analytics-enabled hostnames, having set two headers from
// the routing table (TagHeader, ClientIPHeader). It serves the script,
// checks and enriches each event (bots dropped, the visitor hashed with
// the day's salt, the address cut to its /24 or /48, the user agent
// parsed, the channel worked out) and appends it to the server's spool. It
// keeps no state of its own but its rate limits' counts of the current
// second (limits.go).
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

// ScriptHeader is the response header that marks s.js as Dock's, so a
// tracking check can tell it from an app's own answer at the same path (a
// hostname without analytics, or a proxy in front that doesn't pass
// /_dock/a/).
const ScriptHeader = "X-Dock-Script"

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
	// RecorderPath is the session recorder s.js loads for sampled page
	// loads, and ReplayPath where it sends its chunks (phase A3).
	RecorderPath = "/_dock/a/r.js"
	ReplayPath   = "/_dock/a/r"
	// Prefix is what Caddy routes here.
	Prefix = "/_dock/a/"
)

// maxBody caps an event's request body.
const maxBody = 8 << 10

// The script: s.js is the core every site gets, ev.js its errors and
// Web Vitals, spliced in at evMarker for sites that collect them. The
// site's settings go in at configMarker.
var (
	//go:embed s.js
	coreScript []byte
	//go:embed ev.js
	evScript []byte
	// recorderScript is rrweb's recorder with Dock's chunking, built from
	// web/src/recorder by `pnpm build:recorder`.
	//go:embed recorder/r.js
	recorderScript []byte
)

const (
	evMarker     = "/*ev*/"
	configMarker = "/*c*/{}"
)

// Ingest is the handler. Spool and Salts must be set; Replay is the
// replay spool, nil where replays aren't kept.
type Ingest struct {
	Spool  *spool.Spool
	Replay *spool.Spool
	Salts  *Salts
	// Rules leave sites' excluded paths and ignored addresses out and
	// say what each site's script collects; nil is none.
	Rules *Rules
	Log   *slog.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	stats  Stats
	limits limiter
	// replaying counts replay chunks in flight, up to maxReplays.
	replaying atomic.Int32

	// scripts are the script's variants as served, built on first use:
	// one per combination of settings, so a handful.
	scriptMu sync.Mutex
	scripts  map[scriptVariant]*builtScript
	// recorder is r.js, built on first use.
	recorderOnce sync.Once
	recorder     *builtScript
}

// scriptVariant is what a site's script differs in: at most 2 x 101 x 2
// of them, a few KB each.
type scriptVariant struct {
	// errors splices in ev.js.
	errors bool
	// replay is the percent of page loads that record; mask masks all
	// text in them.
	replay int
	mask   bool
	// consent is what the script holds back until dock.consent(true):
	// 0 nothing, consentReplay or consentAll.
	consent uint8
}

// builtScript is one variant: its source, gzipped, and its ETag.
type builtScript struct {
	src, gz []byte
	etag    string
}

// Stats counts what the ingest saw since it started; Replays are replay
// chunks kept, Limited events and chunks dropped by the rate limits or
// the cap on replays in flight.
type Stats struct {
	Accepted, Bots, Invalid, NoSalt, Excluded, Replays, Limited atomic.Int64
}

// Stats returns the counts.
func (in *Ingest) Stats() *Stats { return &in.stats }

// now is the ingest's clock.
func (in *Ingest) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}

// ServeHTTP serves the script and takes events.
func (in *Ingest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == ScriptPath && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		in.serveScript(w, r)
	case r.URL.Path == EventPath && r.Method == http.MethodPost:
		in.serveEvent(w, r)
	case r.URL.Path == RecorderPath && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		in.recorderOnce.Do(func() { in.recorder = compress(recorderScript) })
		serveBuilt(w, r, in.recorder, "public, max-age=86400")
	case r.URL.Path == ReplayPath && r.Method == http.MethodPost:
		in.serveReplay(w, r)
	default:
		http.NotFound(w, r)
	}
}

// script returns the variant v, building it on first use.
func (in *Ingest) script(v scriptVariant) *builtScript {
	in.scriptMu.Lock()
	defer in.scriptMu.Unlock()
	if s := in.scripts[v]; s != nil {
		return s
	}
	s := buildScript(v)
	if in.scripts == nil {
		in.scripts = map[scriptVariant]*builtScript{}
	}
	in.scripts[v] = s
	return s
}

// buildScript assembles and compresses a variant.
func buildScript(v scriptVariant) *builtScript {
	var ev []byte
	if v.errors {
		ev = bytes.TrimSpace(evScript)
	}
	src := bytes.Replace(coreScript, []byte(evMarker), ev, 1)
	var config []string
	if v.replay > 0 {
		config = append(config, `"r":`+strconv.Itoa(v.replay))
		if v.mask {
			config = append(config, `"m":1`)
		}
	}
	if v.consent > 0 {
		config = append(config, `"c":`+strconv.Itoa(int(v.consent)))
	}
	src = bytes.Replace(src, []byte(configMarker), []byte("{"+strings.Join(config, ",")+"}"), 1)
	return compress(src)
}

// compress gzips a script and names its version for the ETag.
func compress(src []byte) *builtScript {
	var b bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	_, _ = zw.Write(src)
	_ = zw.Close()
	etag := `"` + strconv.FormatUint(uint64(len(src)), 36) + "-" + strconv.FormatUint(fnv(src), 36) + `"`
	return &builtScript{src: src, gz: b.Bytes(), etag: etag}
}

// serveScript sends the site's variant of s.js, gzipped when the browser
// takes it, cacheable for an hour (so a changed setting reaches visitors
// within the hour) and revalidated by ETag after.
func (in *Ingest) serveScript(w http.ResponseWriter, r *http.Request) {
	site, _, _, _ := parseTag(r.Header.Get(TagHeader))
	v := scriptVariant{errors: in.Rules.collectsErrors(site), consent: in.Rules.consent(site)}
	if in.Replay != nil {
		v.replay, v.mask = in.Rules.replay(site)
	}
	w.Header().Set(ScriptHeader, "analytics")
	serveBuilt(w, r, in.script(v), "public, max-age=3600")
}

// serveBuilt sends a built script, gzipped when the browser takes it, and
// answers revalidations by ETag.
func serveBuilt(w http.ResponseWriter, r *http.Request, sc *builtScript, cache string) {
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", cache)
	h.Set("ETag", sc.etag)
	h.Set("Vary", "Accept-Encoding")
	if r.Header.Get("If-None-Match") == sc.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := sc.src
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		body = sc.gz
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// ScriptSizes are the scripts' sizes as served to browsers, gzipped, for
// the budgets: the core (under 2 KB), with errors and Web Vitals (about
// 2 KB more) and the session recorder sampled page loads load.
func ScriptSizes() (core, withErrors, recorder int) {
	return len(buildScript(scriptVariant{}).gz), len(buildScript(scriptVariant{errors: true}).gz), len(compress(recorderScript).gz)
}

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
	// An error's type and stack.
	Type  string `json:"t"`
	Stack string `json:"s"`
	// A page load's vitals.
	Vitals *beaconVitals `json:"v"`
}

// beaconVitals are the vitals the script sends; -1 is unmeasured.
type beaconVitals struct {
	LCP  float64 `json:"lcp"`
	INP  float64 `json:"inp"`
	CLS  float64 `json:"cls"`
	FCP  float64 `json:"fcp"`
	TTFB float64 `json:"ttfb"`
}

// bufs are reused for events' request bodies and spooled lines, all
// under maxBody; replay chunks have replayBufs.
var bufs = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// serveEvent takes one event. It answers 202 whether or not the event is
// kept (bots, events without a salt, over the rate limits), so a page
// learns nothing from it; 400 is for requests the script never sends.
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
	maxMessage  = 500
	maxType     = 80
	maxStack    = 4 << 10
)

// event fills e with the event a request carries. ok is false for a
// malformed request; keep is false for one that isn't kept (a bot, no
// salt, over the rate limits).
func (in *Ingest) event(r *http.Request, body []byte, e *analytics.Event) (keep, ok bool) {
	site, service, release, ok := parseTag(r.Header.Get(TagHeader))
	if !ok {
		return false, false
	}
	var b beacon
	if err := json.Unmarshal(body, &b); err != nil || b.Kind < 1 || b.Kind > 5 {
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
		// Without its query or fragment, which can carry tokens or
		// someone's email address.
		link, _, _ := strings.Cut(b.Name, "#")
		link, _, _ = strings.Cut(link, "?")
		e.Name = cut(link, maxOutbound)
	case analytics.KindError:
		typ := cut(b.Type, maxType)
		e.Name = cut(errorMessage(typ, b.Name), maxMessage)
		e.Error = errorInfo(typ, e.Name, cut(b.Stack, maxStack))
	case analytics.KindVitals:
		if b.Vitals == nil {
			return false, false
		}
		e.Vitals = b.Vitals.clean()
	}

	agent := ParseAgent(r.Header.Get("User-Agent"))
	if agent.Bot {
		in.stats.Bots.Add(1)
		return false, true
	}
	t := in.now()
	salt, ok := in.Salts.get(Day(t))
	if !ok {
		// The control plane hasn't handed out today's salt yet: there is
		// no way to count this visitor without one.
		in.stats.NoSalt.Add(1)
		return false, true
	}
	if (e.Kind == analytics.KindError || e.Kind == analytics.KindVitals) && !in.Rules.collectsErrors(site) {
		// The site turned them off; a cached script may still send them.
		in.stats.Excluded.Add(1)
		return false, true
	}
	ip := r.Header.Get(ClientIPHeader)
	path := cut(cmpStr(u.EscapedPath(), "/"), maxPath)
	if in.Rules.skip(site, path, ip) {
		in.stats.Excluded.Add(1)
		return false, true
	}
	if !in.limits.allow(t, false, site, ip, int64(len(body))) {
		in.stats.Limited.Add(1)
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
