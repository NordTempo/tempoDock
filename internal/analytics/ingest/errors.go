package ingest

import (
	"math"
	"path"
	"strings"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// errorInfo completes an error event: its type, its top frame and the
// group it belongs to.
func errorInfo(typ, message, stack string) *analytics.ErrorInfo {
	if typ == "" {
		typ = "Error"
	}
	frame := topFrame(stack)
	return &analytics.ErrorInfo{Type: typ, Stack: stack, Frame: frame, Group: errorGroup(typ, message, frame)}
}

// errorGroup is an error's fingerprint: FNV-1a over its type, its message
// with every run of digits made "0" (ids and counts don't split a group)
// and its top frame.
func errorGroup(typ, message, frame string) uint64 {
	h := uint64(14695981039346656037)
	add := func(c byte) { h = (h ^ uint64(c)) * 1099511628211 }
	for i := range len(typ) {
		add(typ[i])
	}
	add(0)
	digits := false
	for i := range len(message) {
		c := message[i]
		if c >= '0' && c <= '9' {
			if !digits {
				add('0')
			}
			digits = true
			continue
		}
		digits = false
		add(c)
	}
	add(0)
	for i := range len(frame) {
		add(frame[i])
	}
	return h
}

// topFrame is a stack's first frame that names a script: "fn (file)" or
// "file", where file is the script's path without its line, column,
// query or a bundler's content hash, so a group survives deploys. It
// reads Chrome's ("at fn (url:1:2)") and Firefox's and Safari's
// ("fn@url:1:2") formats; "" when no frame names a script.
func topFrame(stack string) string {
	for line := range strings.Lines(stack) {
		line = strings.TrimSpace(line)
		var fn, loc string
		if rest, ok := strings.CutPrefix(line, "at "); ok {
			rest = strings.TrimPrefix(rest, "async ")
			if i := strings.LastIndexByte(rest, '('); i >= 0 && strings.HasSuffix(rest, ")") {
				fn, loc = strings.TrimSpace(rest[:i]), rest[i+1:len(rest)-1]
			} else {
				loc = rest
			}
		} else if i := strings.LastIndexByte(line, '@'); i >= 0 {
			fn, loc = line[:i], line[i+1:]
		} else {
			continue
		}
		file, ok := scriptFile(loc)
		if !ok {
			continue // <anonymous>, native code
		}
		if fn = cut(fn, 120); fn != "" {
			return fn + " (" + file + ")"
		}
		return file
	}
	return ""
}

// scriptFile reads a frame's location, "url:line:col": the URL's path
// without query and content hash. ok is false for a location without a
// line number.
func scriptFile(loc string) (string, bool) {
	numbers := 0
	for ; numbers < 2; numbers++ {
		i := strings.LastIndexByte(loc, ':')
		if i < 0 || !allDigits(loc[i+1:]) {
			break
		}
		loc = loc[:i]
	}
	if numbers == 0 || loc == "" || strings.HasPrefix(loc, "<") || loc == "native" {
		return "", false
	}
	if _, rest, ok := strings.Cut(loc, "://"); ok {
		loc = "/"
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			loc = rest[i:]
		}
	}
	if i := strings.IndexAny(loc, "?#"); i >= 0 {
		loc = loc[:i]
	}
	dir, base := path.Split(loc)
	return cut(dir+unhash(base), 200), true
}

// unhash drops content hashes from a script's file name:
// "index-D2vX9pQa.js" and "main.3f9a8c1e2b.js" become "index.js" and
// "main.js". A hash is a part of 8 or more letters and digits with at
// least one of each.
func unhash(base string) string {
	var b strings.Builder
	start := 0
	for i := 0; i <= len(base); i++ {
		if i < len(base) && base[i] != '.' && base[i] != '-' {
			continue
		}
		part := base[start:i]
		if !isHash(part) {
			if b.Len() > 0 && start > 0 {
				b.WriteByte(base[start-1])
			}
			b.WriteString(part)
		}
		start = i + 1
	}
	return b.String()
}

// isHash reports whether s looks like a content hash.
func isHash(s string) bool {
	if len(s) < 8 {
		return false
	}
	var letter, digit bool
	for i := range len(s) {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			digit = true
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
			letter = true
		default:
			return false
		}
	}
	return letter && digit
}

// allDigits reports whether s is a non-empty run of digits.
func allDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// clean keeps the vitals a browser could have measured: times from 0 to
// 10 minutes in whole milliseconds and a layout shift score up to 100;
// anything else is unmeasured (-1).
func (b *beaconVitals) clean() *analytics.Vitals {
	ms := func(v float64) float64 {
		if v >= 0 && v <= 600_000 {
			return math.Round(v)
		}
		return -1
	}
	cls := -1.0
	if b.CLS >= 0 && b.CLS <= 100 {
		cls = math.Round(b.CLS*10000) / 10000
	}
	return &analytics.Vitals{LCP: ms(b.LCP), INP: ms(b.INP), CLS: cls, FCP: ms(b.FCP), TTFB: ms(b.TTFB)}
}
