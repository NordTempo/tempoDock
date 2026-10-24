package traffic

import (
	"bytes"
	"strings"
)

// maxPathLen caps a counted path; longer ones are cut.
const maxPathLen = 160

// normalizePath turns a request URI into the path it is counted under:
// without the query, IDs replaced by ":id" so /users/12 and /users/13 are
// one endpoint, and at most maxPathLen bytes. It allocates only when it
// changes something.
func normalizePath(uri string) string {
	if i := strings.IndexAny(uri, "?#"); i >= 0 {
		uri = uri[:i]
	}
	if uri == "" {
		return "/"
	}
	if needsRewrite(uri) {
		var b strings.Builder
		b.Grow(len(uri))
		for i, seg := range strings.Split(uri, "/") {
			if i > 0 {
				b.WriteByte('/')
			}
			if idLike(seg) {
				b.WriteString(":id")
			} else {
				b.WriteString(seg)
			}
		}
		uri = b.String()
	}
	if len(uri) > maxPathLen {
		uri = strings.ToValidUTF8(uri[:maxPathLen], "")
	}
	return uri
}

// needsRewrite reports whether a segment of p is an ID.
func needsRewrite(p string) bool {
	for p != "" {
		seg, rest, _ := strings.Cut(p, "/")
		if idLike(seg) {
			return true
		}
		p = rest
	}
	return false
}

// idLike reports whether a path segment looks like an ID rather than a
// name: a number, a UUID, a long hex string (hashes, object IDs) or a long
// token with digits (base64url). Slugs, with their hyphens, stay names.
func idLike(seg string) bool {
	if seg == "" {
		return false
	}
	digits, hex, token := 0, true, true
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		case c == '-' && len(seg) == 36:
			token = false // a UUID's hyphens keep it hex
		case c >= 'g' && c <= 'z', c >= 'G' && c <= 'Z', c == '_':
			hex = false
		default:
			hex, token = false, false
		}
	}
	switch {
	case digits == len(seg):
		return true
	case hex && (len(seg) == 36 || len(seg) >= 16) && digits > 0:
		return true
	}
	return token && len(seg) >= 20 && digits > 0
}

// methods are the request methods counted by name; others count as OTHER,
// so a client can't fill the table with made-up ones.
var methods = [...]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE"}

// normalizeMethod returns m if it is a known method, else "OTHER".
func normalizeMethod(m string) string {
	for _, k := range methods {
		if m == k {
			return k
		}
	}
	return "OTHER"
}

// botMarks are lower-case fragments of user agents that aren't people:
// crawlers, monitors, previews and HTTP libraries.
var botMarks = [...]string{
	"bot", "crawl", "spider", "slurp", "curl/", "wget/", "python", "go-http-client", "java/",
	"okhttp", "headless", "monitor", "uptime", "pingdom", "scan", "preview", "facebookexternalhit",
	"httpclient", "axios/", "node-fetch", "undici", "libwww", "lighthouse", "feed",
}

// isBot reports whether a user agent belongs to a program rather than a
// person's browser. An empty one does. Only the first 256 bytes are looked
// at, lower-cased on the stack.
func isBot(ua string) bool {
	if ua == "" {
		return true
	}
	var buf [256]byte
	lower := buf[:copy(buf[:], ua)]
	for i, c := range lower {
		if c >= 'A' && c <= 'Z' {
			lower[i] = c + 'a' - 'A'
		}
	}
	for _, m := range botMarks {
		if bytes.Contains(lower, []byte(m)) {
			return true
		}
	}
	return false
}
