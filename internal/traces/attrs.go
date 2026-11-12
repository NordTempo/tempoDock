package traces

import (
	"encoding/json/jsontext"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/errparse"
)

// Caps on what a span keeps (the ingest's, before anything is stored).
const (
	maxName      = 256
	maxAttrs     = 64
	maxValue     = 1 << 10
	maxLongValue = 2 << 10 // URLs and database statements
	maxStack     = 8 << 10 // an exception's stack
	maxAttrsJSON = 4 << 10
	maxEvents    = 16
	maxEventJSON = 12 << 10
	maxList      = 32
)

// mask replaces a sensitive value: the same as secret values in logs.
const mask = "*****"

// longValues are attributes that keep up to maxLongValue.
var longValues = map[string]bool{
	"url.full": true, "http.url": true, "url.original": true, "db.query.text": true, "db.statement": true,
}

// urlValues are attributes holding URLs or query strings, whose sensitive
// query parameters are masked.
var urlValues = map[string]bool{
	"url.full": true, "http.url": true, "url.original": true, "url.query": true, "http.target": true,
}

// sensitiveWords are the words of an attribute or parameter name whose
// value is masked: "db.password", "x-api-key", "auth_token". Words, not
// substrings, so "llm.usage.total_tokens" keeps its count.
var sensitiveWords = map[string]bool{
	"password": true, "passwd": true, "pwd": true, "secret": true, "token": true, "authorization": true,
	"cookie": true, "credential": true, "credentials": true, "apikey": true, "signature": true, "sig": true,
}

// sensitivePairs are two consecutive words that mark a name sensitive.
var sensitivePairs = map[[2]string]bool{
	{"api", "key"}: true, {"private", "key"}: true, {"access", "key"}: true, {"secret", "key"}: true, {"set", "cookie"}: true,
}

// headerAllow are the request and response headers whose values are kept;
// every other http.*.header.* value is masked.
var headerAllow = map[string]bool{
	"content-type": true, "content-length": true, "content-encoding": true, "user-agent": true, "accept": true,
	"accept-encoding": true, "accept-language": true, "host": true, "x-request-id": true, "cache-control": true,
}

// sensitive reports whether the attribute or parameter name k holds a
// value that is masked.
func sensitive(k string) bool {
	k = strings.ToLower(k)
	for _, prefix := range []string{"http.request.header.", "http.response.header."} {
		if h, ok := strings.CutPrefix(k, prefix); ok {
			return !headerAllow[strings.ReplaceAll(h, "_", "-")]
		}
	}
	words := strings.FieldsFunc(k, func(r rune) bool { return r == '.' || r == '_' || r == '-' || r == ' ' })
	for i, w := range words {
		if sensitiveWords[w] || i > 0 && sensitivePairs[[2]string{words[i-1], w}] {
			return true
		}
	}
	return false
}

// maskQuery masks the values of sensitive parameters in a URL's query, or
// in a bare query string.
func maskQuery(s string) string {
	base, q, ok := strings.Cut(s, "?")
	if !ok {
		if !strings.Contains(s, "=") || strings.Contains(s, "/") {
			return s
		}
		base, q = "", s
	}
	q, frag, hasFrag := strings.Cut(q, "#")
	params := strings.Split(q, "&")
	changed := false
	for i, p := range params {
		if k, _, ok := strings.Cut(p, "="); ok && sensitive(k) {
			params[i], changed = k+"="+mask, true
		}
	}
	if !changed {
		return s
	}
	out := strings.Join(params, "&")
	if ok {
		out = base + "?" + out
	}
	if hasFrag {
		out += "#" + frag
	}
	return out
}

// cut is s cut to at most n bytes, on a rune boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// convert turns a decoded span into the span the store keeps, for the
// service c names. ok is false for a span that can't be kept: no trace
// or span ID, or no start.
func convert(sp *rawSpan, c Claims) (analytics.Span, bool) {
	if sp.traceID == ([16]byte{}) || sp.spanID == 0 || sp.start == 0 {
		return analytics.Span{}, false
	}
	out := analytics.Span{
		Org: c.Org, Project: c.Project, Env: c.Env, Service: c.Service, Release: c.Release,
		Trace: analytics.TraceID(sp.traceID), ID: analytics.SpanID(sp.spanID), Parent: analytics.SpanID(sp.parentID),
		Start: int64(sp.start / 1000), Name: cut(sp.name, maxName),
		Kind:   analytics.SpanKind(min(max(sp.kind, 0), int(analytics.SpanConsumer))),
		Status: analytics.SpanStatus(min(max(sp.statusCode, 0), int(analytics.StatusError))),
	}
	if sp.end > sp.start {
		out.Duration = int64((sp.end - sp.start) / 1000)
	}
	out.Entry = out.Kind == analytics.SpanServer || out.Kind == analytics.SpanConsumer || sp.parentID == 0
	for _, a := range sp.attrs {
		switch a.key {
		case "http.request.method", "http.method":
			if a.val.kind == valueString {
				out.Method = cut(a.val.s, 16)
			}
		case "http.route":
			if a.val.kind == valueString {
				out.Route = cut(a.val.s, maxName)
			}
		case "http.response.status_code", "http.status_code":
			if a.val.kind == valueInt && a.val.i > 0 && a.val.i < 1000 {
				out.HTTPStatus = int(a.val.i)
			}
		}
	}
	if sp.statusMessage != "" {
		sp.attrs = append(sp.attrs, attr{key: "otel.status_description", val: value{kind: valueString, s: sp.statusMessage}})
	}
	out.Attrs = appendAttrs(nil, sp.attrs, maxAttrs, maxAttrsJSON)
	out.Events = appendEvents(nil, sp)
	out.ErrorFP = exceptionFingerprint(sp)
	return out, true
}

// exceptionFingerprint is the Errors fingerprint of a span's first
// exception, "" without one: its stack trace read as a log would be
// (errparse), so an exception the app also logs has the same fingerprint
// as the issue its log line opened, and the issue finds its traces
// (ADR 0075). A span's exception never opens an issue of its own.
func exceptionFingerprint(sp *rawSpan) string {
	for _, e := range sp.events {
		if e.name != "exception" {
			continue
		}
		var typ, msg, stack string
		for _, a := range e.attrs {
			if a.val.kind != valueString {
				continue
			}
			switch a.key {
			case "exception.type":
				typ = a.val.s
			case "exception.message":
				msg = a.val.s
			case "exception.stacktrace":
				stack = cut(a.val.s, maxStack)
			}
		}
		if typ == "" && msg == "" && stack == "" {
			return ""
		}
		var as errparse.Assembler
		var ev errparse.Event
		ok := false
		for line := range strings.Lines(stack) {
			if e, done := as.Line(strings.TrimRight(line, "\r\n")); done && !ok {
				ev, ok = e, true
			}
		}
		if e, done := as.Flush(); done && !ok {
			ev, ok = e, true
		}
		if ok {
			return ev.Fingerprint
		}
		return errparse.Fingerprint(typ, msg, nil)
	}
	return ""
}

// appendAttrs appends attrs as a JSON object, at most n of them and about
// budget bytes; nil when there are none.
func appendAttrs(b []byte, attrs []attr, n, budget int) jsontext.Value {
	if len(attrs) == 0 {
		return nil
	}
	start := len(b)
	b = append(b, '{')
	kept := 0
	for _, a := range attrs {
		if kept == n || len(b)-start >= budget || a.key == "" {
			continue
		}
		mark := len(b)
		if kept > 0 {
			b = append(b, ',')
		}
		b, _ = jsontext.AppendQuote(b, cut(a.key, maxName))
		b = append(b, ':')
		b = appendAttrValue(b, a)
		if len(b)-start > budget {
			b = b[:mark] // it doesn't fit: the next, shorter ones may
			continue
		}
		kept++
	}
	b = append(b, '}')
	if kept == 0 {
		return nil
	}
	return b
}

// appendAttrValue appends a's value: masked when its name is sensitive,
// its URL's sensitive parameters masked, cut to its cap.
func appendAttrValue(b []byte, a attr) []byte {
	if sensitive(a.key) {
		b, _ = jsontext.AppendQuote(b, mask)
		return b
	}
	limit := maxValue
	switch {
	case a.key == "exception.stacktrace":
		limit = maxStack
	case longValues[a.key]:
		limit = maxLongValue
	}
	v := a.val
	if v.kind == valueString && urlValues[a.key] {
		v.s = maskQuery(v.s)
	}
	return appendValue(b, v, limit, 0)
}

// appendValue appends v as JSON, strings cut to limit.
func appendValue(b []byte, v value, limit, depth int) []byte {
	switch v.kind {
	case valueString:
		b, _ = jsontext.AppendQuote(b, cut(v.s, limit))
	case valueBool:
		b = strconv.AppendBool(b, v.b)
	case valueInt:
		b = strconv.AppendInt(b, v.i, 10)
	case valueDouble:
		if math.IsNaN(v.f) || math.IsInf(v.f, 0) {
			return append(b, "null"...)
		}
		b = strconv.AppendFloat(b, v.f, 'g', -1, 64)
	case valueBytes:
		b, _ = jsontext.AppendQuote(b, "("+strconv.Itoa(len(v.s))+" bytes)")
	case valueList:
		b = append(b, '[')
		for i, x := range v.list {
			if i == maxList || depth >= maxDepth {
				break
			}
			if i > 0 {
				b = append(b, ',')
			}
			b = appendValue(b, x, limit, depth+1)
		}
		b = append(b, ']')
	case valueMap:
		b = append(b, '{')
		for i, a := range v.kv {
			if i == maxList || depth >= maxDepth {
				break
			}
			if i > 0 {
				b = append(b, ',')
			}
			b, _ = jsontext.AppendQuote(b, cut(a.key, maxName))
			b = append(b, ':')
			if sensitive(a.key) {
				b, _ = jsontext.AppendQuote(b, mask)
			} else {
				b = appendValue(b, a.val, limit, depth+1)
			}
		}
		b = append(b, '}')
	default:
		b = append(b, "null"...)
	}
	return b
}

// appendEvents appends sp's events as a JSON array, exceptions first, at
// most maxEvents and about maxEventJSON bytes; nil when there are none.
// Each event's time is microseconds after the span's start.
func appendEvents(b []byte, sp *rawSpan) jsontext.Value {
	if len(sp.events) == 0 {
		return nil
	}
	start := len(b)
	b = append(b, '[')
	kept := 0
	for _, exceptions := range []bool{true, false} {
		for _, e := range sp.events {
			if (e.name == "exception") != exceptions || kept == maxEvents || len(b)-start >= maxEventJSON {
				continue
			}
			mark := len(b)
			if kept > 0 {
				b = append(b, ',')
			}
			b = append(b, `{"name":`...)
			b, _ = jsontext.AppendQuote(b, cut(e.name, maxName))
			if e.time > sp.start {
				b = append(b, `,"at":`...)
				b = strconv.AppendUint(b, (e.time-sp.start)/1000, 10)
			}
			if attrs := appendAttrs(nil, e.attrs, maxAttrs, maxEventJSON-(len(b)-start)-2); attrs != nil {
				b = append(b, `,"attrs":`...)
				b = append(b, attrs...)
			}
			b = append(b, '}')
			if len(b)-start > maxEventJSON {
				b = b[:mark]
				continue
			}
			kept++
		}
	}
	b = append(b, ']')
	if kept == 0 {
		return nil
	}
	return b
}
