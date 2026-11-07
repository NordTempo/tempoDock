package errparse

import (
	"encoding/json"
	"strconv"
	"strings"
)

// maxNormalized caps a normalized message: the fingerprint of a long
// message is its start's.
const maxNormalized = 512

// maxJSONLine is the longest line read as JSON for its message; a longer
// one's title is its text.
const maxJSONLine = 16 << 10

// lineMessage is what an error line says, without its timestamp and
// level: a JSON line's msg (and err) fields and the stack one carried, a
// logfmt line's msg, a console format's message column, or a plain
// line's text.
func lineMessage(s string) (msg, stack string) {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "{") && len(t) <= maxJSONLine {
		if msg, stack, ok := jsonMessage(t); ok {
			return msg, stack
		}
	}
	if m, ok := logfmtValue(t, "msg", "message"); ok {
		if e, ok := logfmtValue(t, "err", "error"); ok && e != "" {
			m += ": " + e
		}
		return m, ""
	}
	if strings.IndexByte(t, '\t') >= 0 {
		if m, ok := consoleMessage(t); ok {
			return m, ""
		}
	}
	return plainMessage(t), ""
}

// jsonMessage reads a structured logger's line: msg or message, with
// error or err (a string, or an object with a type, message and stack,
// as pino writes it) after it, and a stack or stacktrace field (zap).
func jsonMessage(s string) (msg, stack string, ok bool) {
	var j struct {
		Msg        string          `json:"msg"`
		Message    string          `json:"message"`
		Error      json.RawMessage `json:"error"`
		Err        json.RawMessage `json:"err"`
		Stack      string          `json:"stack"`
		Stacktrace string          `json:"stacktrace"`
	}
	if json.Unmarshal([]byte(s), &j) != nil {
		return "", "", false
	}
	msg = j.Msg
	if msg == "" {
		msg = j.Message
	}
	stack = j.Stack
	if stack == "" {
		stack = j.Stacktrace
	}
	for _, raw := range []json.RawMessage{j.Err, j.Error} {
		if len(raw) == 0 {
			continue
		}
		var text string
		var obj struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Stack   string `json:"stack"`
		}
		switch {
		case json.Unmarshal(raw, &text) == nil:
		case json.Unmarshal(raw, &obj) == nil:
			text = obj.Message
			if obj.Type != "" && !strings.HasPrefix(text, obj.Type) {
				text = obj.Type + ": " + text
			}
			if stack == "" {
				stack = obj.Stack
			}
		}
		if text != "" && text != msg {
			if msg == "" {
				msg = text
			} else {
				msg += ": " + text
			}
		}
		break
	}
	if msg == "" {
		return "", "", false
	}
	return msg, stack, true
}

// logfmtValue returns the value of the first of keys in a logfmt line:
// key=word or key="quoted".
func logfmtValue(s string, keys ...string) (string, bool) {
	for _, key := range keys {
		at := 0
		for {
			i := strings.Index(s[at:], key+"=")
			if i < 0 {
				break
			}
			i += at
			if i > 0 && !isSpace(s[i-1]) {
				at = i + len(key)
				continue
			}
			v := s[i+len(key)+1:]
			if strings.HasPrefix(v, `"`) {
				if q, err := strconv.QuotedPrefix(v); err == nil {
					if u, err := strconv.Unquote(q); err == nil {
						return u, true
					}
				}
				return strings.Trim(v, `"`), true
			}
			if end := strings.IndexAny(v, " \t"); end >= 0 {
				v = v[:end]
			}
			return v, true
		}
	}
	return "", false
}

// consoleMessage reads a tab-separated console line (zap's, Caddy's):
// time, level, then a logger or caller and the message, then JSON fields.
// The message is the last column after the level that isn't JSON.
func consoleMessage(s string) (string, bool) {
	cols := strings.Split(s, "\t")
	level := -1
	for i, c := range cols {
		if _, ok := tabLevel(c + "\t"); ok {
			level = i
			break
		}
	}
	if level < 0 {
		return "", false
	}
	for i := len(cols) - 1; i > level; i-- {
		if c := strings.TrimSpace(cols[i]); c != "" && !strings.HasPrefix(c, "{") {
			return c, true
		}
	}
	return "", false
}

// plainMessage is a plain line without a leading timestamp and level
// ("2026-11-07 12:00:01 ERROR [main] x" is "[main] x").
func plainMessage(s string) string {
	i := 0
	for i < len(s) && strings.IndexByte("0123456789-:./TZ+, ", s[i]) >= 0 {
		i++
	}
	// Only a run that held digits was a timestamp ("- - [01/Jan..." isn't).
	if !strings.ContainsAny(s[:i], "0123456789") {
		i = 0
	}
	rest := strings.TrimLeft(s[i:], " ")
	word := strings.TrimLeft(rest, "[")
	n := letters(word)
	if lv, known := levelWord(word[:n]); n > 0 && known && lv != LevelNone {
		after := strings.TrimLeft(word[n:], "]:")
		if after == "" || after[0] == ' ' {
			rest = strings.TrimSpace(after)
		}
	}
	if rest == "" {
		return s
	}
	return rest
}

// Normalize makes a message or frame alike across occurrences: numbers
// become 0, hex IDs (0x..., or 6+ hex digits with a digit among them)
// <hex>, UUIDs <uuid>, quoted values <str>, and whitespace one space.
// IP addresses and ports become 0.0.0.0:0 by the number rule. The result
// is at most maxNormalized bytes.
func Normalize(s string) string {
	b := make([]byte, 0, min(len(s), maxNormalized))
	space := false
	for i := 0; i < len(s) && len(b) < maxNormalized; {
		c := s[i]
		switch {
		case isSpace(c):
			space = true
			i++
			continue
		case space && len(b) > 0:
			b = append(b, ' ')
		}
		space = false
		switch {
		case isUUID(s[i:]) && (i == 0 || !isWord(s[i-1])):
			b = append(b, "<uuid>"...)
			i += 36
		case (c == '\'' || c == '"' || c == '`') && (i == 0 || !isWord(s[i-1])):
			end := strings.IndexByte(s[i+1:], c)
			if end < 0 {
				b = append(b, c)
				i++
				continue
			}
			b = append(b, "<str>"...)
			i += end + 2
		case isWord(c):
			j := i
			for j < len(s) && isWord(s[j]) {
				j++
			}
			b = appendToken(b, s[i:j])
			i = j
		default:
			b = append(b, c)
			i++
		}
	}
	return string(b[:min(len(b), maxNormalized)])
}

// appendToken appends a word with its numbers made alike.
func appendToken(b []byte, w string) []byte {
	if len(w) > 2 && w[0] == '0' && w[1]|0x20 == 'x' && allHex(w[2:]) {
		return append(b, "<hex>"...)
	}
	if digits(w) == len(w) {
		return append(b, '0')
	}
	if len(w) >= 6 && allHex(w) && strings.ContainsAny(w, "0123456789") {
		return append(b, "<hex>"...)
	}
	for i := 0; i < len(w); {
		if n := digits(w[i:]); n > 0 {
			b = append(b, '0')
			i += n
			continue
		}
		b = append(b, w[i])
		i++
	}
	return b
}

// allHex reports whether s is all hex digits.
func allHex(s string) bool {
	for i := range len(s) {
		c := s[i] | 0x20
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return s != ""
}

// isUUID reports whether s starts with a UUID (8-4-4-4-12 hex digits)
// not followed by more of a word.
func isUUID(s string) bool {
	if len(s) < 36 || len(s) > 36 && isWord(s[36]) {
		return false
	}
	for i := range 36 {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			if !allHex(s[i : i+1]) {
				return false
			}
		}
	}
	return true
}

// Fingerprint groups errors: an FNV-1a hash of the type, the message
// normalized and the top frames (already normalized: their line numbers
// are zeros), as 16 hex digits. Line numbers are left out so an issue
// keeps its fingerprint when code above it moves in a deploy.
func Fingerprint(typ, msg string, frames []string) string {
	h := uint64(14695981039346656037)
	add := func(s string) {
		for i := range len(s) {
			h = (h ^ uint64(s[i])) * 1099511628211
		}
		h *= 1099511628211 // a zero byte between fields
	}
	add(typ)
	add(Normalize(msg))
	for _, f := range frames[:min(len(frames), maxFrames)] {
		add(f)
	}
	const hex = "0123456789abcdef"
	var out [16]byte
	for i := range out {
		out[15-i] = hex[h&0xf]
		h >>= 4
	}
	return string(out[:])
}
