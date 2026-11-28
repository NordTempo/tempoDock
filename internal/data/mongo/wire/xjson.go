package wire

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Extended JSON (MongoDB's Extended JSON v2): AppendJSON writes values in
// its canonical form, which keeps every type ({"$numberInt": "2"} is not
// {"$numberLong": "2"}), with one relaxation: dates between the years
// 1970 and 9999 are ISO strings ({"$date": "2026-11-27T09:14:02.000Z"}),
// which read back to the same instant. ParseJSON reads canonical and
// relaxed Extended JSON, and what people type in the mongo shell: keys
// without quotes, strings in single quotes, a trailing comma, and
// ObjectId("..."), ISODate("..."), NumberLong(..), NumberInt(..),
// NumberDecimal("..") and UUID("..").

// AppendJSON appends v as canonical Extended JSON.
func AppendJSON(b []byte, v any) []byte {
	switch v := v.(type) {
	case nil:
		return append(b, "null"...)
	case bool:
		return strconv.AppendBool(b, v)
	case string:
		return appendQuoted(b, v)
	case int32:
		return append(strconv.AppendInt(append(b, `{"$numberInt":"`...), int64(v), 10), `"}`...)
	case int64:
		return append(strconv.AppendInt(append(b, `{"$numberLong":"`...), v, 10), `"}`...)
	case float64:
		return wrap(b, "$numberDouble", formatDouble(v))
	case Decimal128:
		return wrap(b, "$numberDecimal", v.String())
	case ObjectID:
		return append(hex.AppendEncode(append(b, `{"$oid":"`...), v[:]), `"}`...)
	case DateTime:
		if v >= 0 && v <= 253402300799999 {
			b = time.UnixMilli(int64(v)).UTC().AppendFormat(append(b, `{"$date":"`...), "2006-01-02T15:04:05.000Z")
			return append(b, `"}`...)
		}
		b = append(b, `{"$date":`...)
		return append(wrap(b, "$numberLong", strconv.FormatInt(int64(v), 10)), '}')
	case Binary:
		b = append(b, `{"$binary":{"base64":`...)
		b = appendQuoted(b, base64.StdEncoding.EncodeToString(v.Data))
		b = append(b, `,"subType":`...)
		return append(appendQuoted(b, fmt.Sprintf("%02x", v.Subtype)), "}}"...)
	case Regex:
		b = append(b, `{"$regularExpression":{"pattern":`...)
		b = appendQuoted(b, v.Pattern)
		b = append(b, `,"options":`...)
		return append(appendQuoted(b, v.Options), "}}"...)
	case Timestamp:
		return fmt.Appendf(b, `{"$timestamp":{"t":%d,"i":%d}}`, v.T, v.I)
	case JavaScript:
		return wrap(b, "$code", string(v))
	case Symbol:
		return wrap(b, "$symbol", string(v))
	case CodeWithScope:
		b = append(b, `{"$code":`...)
		b = appendQuoted(b, v.Code)
		b = append(b, `,"$scope":`...)
		return append(AppendJSON(b, v.Scope), '}')
	case DBPointer:
		b = append(b, `{"$dbPointer":{"$ref":`...)
		b = appendQuoted(b, v.Ns)
		b = append(b, `,"$id":`...)
		return append(AppendJSON(b, v.ID), "}}"...)
	case MinKey:
		return append(b, `{"$minKey":1}`...)
	case MaxKey:
		return append(b, `{"$maxKey":1}`...)
	case Undefined:
		return append(b, `{"$undefined":true}`...)
	case D:
		b = append(b, '{')
		for i, e := range v {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(appendQuoted(b, e.Key), ':')
			b = AppendJSON(b, e.Value)
		}
		return append(b, '}')
	case A:
		b = append(b, '[')
		for i, e := range v {
			if i > 0 {
				b = append(b, ',')
			}
			b = AppendJSON(b, e)
		}
		return append(b, ']')
	}
	return append(b, "null"...)
}

func wrap(b []byte, key, s string) []byte {
	b = append(b, `{"`...)
	b = append(b, key...)
	b = append(b, `":`...)
	return append(appendQuoted(b, s), '}')
}

// appendQuoted appends s as a JSON string, as encoding/json writes it
// (<, > and & escaped too), with invalid UTF-8 as U+FFFD; no allocation.
func appendQuoted(b []byte, s string) []byte {
	const hexDigits = "0123456789abcdef"
	b = append(b, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch {
			case c == '"' || c == '\\':
				b = append(b, '\\', c)
			case c == '\n':
				b = append(b, '\\', 'n')
			case c == '\r':
				b = append(b, '\\', 'r')
			case c == '\t':
				b = append(b, '\\', 't')
			case c < 0x20 || c == '<' || c == '>' || c == '&':
				b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
			default:
				b = append(b, c)
			}
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			b = append(b, "\uFFFD"...)
		case r == '\u2028' || r == '\u2029':
			b = append(b, '\\', 'u', '2', '0', '2', hexDigits[r&0xF])
		default:
			b = append(b, s[i:i+n]...)
		}
		i += n
	}
	return append(b, '"')
}

// formatDouble is the spec's text of a double: "1.0", "-0.0",
// "1.5E+40", "Infinity", "NaN".
func formatDouble(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	s := strconv.FormatFloat(f, 'G', -1, 64)
	if !strings.ContainsAny(s, ".EN") {
		s += ".0"
	}
	return s
}

// maxJSONDepth bounds nesting in ParseJSON, as in BSON.
const maxJSONDepth = maxDepth

// ParseJSON reads one value of (shell-flavoured) Extended JSON: a
// document is D, an array A.
func ParseJSON(s string) (any, error) {
	p := parser{s: s}
	p.space()
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.space()
	if p.i < len(p.s) {
		return nil, p.fail("more after the value")
	}
	return v, nil
}

// ParseDoc reads a document; an empty s is the empty document.
func ParseDoc(s string) (D, error) {
	if strings.TrimSpace(s) == "" {
		return D{}, nil
	}
	v, err := ParseJSON(s)
	if err != nil {
		return nil, err
	}
	d, ok := v.(D)
	if !ok {
		return nil, errors.New("expected a document: { ... }")
	}
	return d, nil
}

type parser struct {
	s string
	i int
}

func (p *parser) fail(what string) error {
	near := p.s[p.i:]
	if len(near) > 20 {
		near = near[:20] + "..."
	}
	if near == "" {
		return fmt.Errorf("%s at the end", what)
	}
	return fmt.Errorf("%s at %d (near %q)", what, p.i+1, near)
}

func (p *parser) space() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *parser) eat(c byte) bool {
	p.space()
	if p.i < len(p.s) && p.s[p.i] == c {
		p.i++
		return true
	}
	return false
}

func (p *parser) value(depth int) (any, error) {
	if depth > maxJSONDepth {
		return nil, p.fail("nested too deeply")
	}
	p.space()
	if p.i >= len(p.s) {
		return nil, p.fail("expected a value")
	}
	switch c := p.s[p.i]; {
	case c == '{':
		return p.object(depth)
	case c == '[':
		return p.array(depth)
	case c == '"' || c == '\'':
		return p.str()
	case c == '-' || c == '+' || c == '.' || (c >= '0' && c <= '9'):
		return p.number()
	case isIdentStart(c):
		return p.word(depth)
	}
	return nil, p.fail("unexpected character")
}

func (p *parser) object(depth int) (any, error) {
	p.i++ // {
	d := D{}
	for !p.eat('}') {
		p.space()
		var key string
		var err error
		if p.i < len(p.s) && (p.s[p.i] == '"' || p.s[p.i] == '\'') {
			key, err = p.str()
		} else {
			key, err = p.ident()
		}
		if err != nil {
			return nil, err
		}
		if !p.eat(':') {
			return nil, p.fail("expected : after the key " + strconv.Quote(key))
		}
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		d = append(d, E{Key: key, Value: v})
		if p.eat(',') {
			continue
		}
		if !p.eat('}') {
			return nil, p.fail("expected , or }")
		}
		break
	}
	return unwrap(d)
}

func (p *parser) array(depth int) (any, error) {
	p.i++ // [
	a := A{}
	for {
		if p.eat(']') {
			return a, nil
		}
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		a = append(a, v)
		if p.eat(',') {
			continue
		}
		if !p.eat(']') {
			return nil, p.fail("expected , or ]")
		}
		return a, nil
	}
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

func isIdent(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') || c == '.' }

func (p *parser) ident() (string, error) {
	start := p.i
	if p.i >= len(p.s) || !isIdentStart(p.s[p.i]) {
		return "", p.fail("expected a key")
	}
	for p.i < len(p.s) && isIdent(p.s[p.i]) {
		p.i++
	}
	return p.s[start:p.i], nil
}

// str reads a string in double or single quotes, with JSON's escapes.
func (p *parser) str() (string, error) {
	q := p.s[p.i]
	p.i++
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch c {
		case q:
			p.i++
			return b.String(), nil
		case '\\':
			if p.i+1 >= len(p.s) {
				return "", p.fail("unterminated string")
			}
			p.i++
			switch e := p.s[p.i]; e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'u':
				if p.i+4 >= len(p.s) {
					return "", p.fail("bad \\u escape")
				}
				n, err := strconv.ParseUint(p.s[p.i+1:p.i+5], 16, 16)
				if err != nil {
					return "", p.fail("bad \\u escape")
				}
				r := rune(n)
				p.i += 4
				if r >= 0xD800 && r < 0xDC00 && p.i+6 < len(p.s) && p.s[p.i+1] == '\\' && p.s[p.i+2] == 'u' {
					if lo, err := strconv.ParseUint(p.s[p.i+3:p.i+7], 16, 16); err == nil && lo >= 0xDC00 && lo < 0xE000 {
						r = (r-0xD800)<<10 | (rune(lo) - 0xDC00) + 0x10000
						p.i += 6
					}
				}
				b.WriteRune(r)
			default:
				b.WriteByte(e)
			}
			p.i++
		default:
			_, n := utf8.DecodeRuneInString(p.s[p.i:])
			b.WriteString(p.s[p.i : p.i+n])
			p.i += n
		}
	}
	return "", p.fail("unterminated string")
}

// number reads a JSON number: an integer is int32 when it fits, else
// int64; one with a point or an exponent is a double.
func (p *parser) number() (any, error) {
	start := p.i
	for p.i < len(p.s) && strings.IndexByte("+-.0123456789eE", p.s[p.i]) >= 0 {
		p.i++
	}
	t := p.s[start:p.i]
	if !strings.ContainsAny(t, ".eE") {
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			if n >= math.MinInt32 && n <= math.MaxInt32 {
				return int32(n), nil
			}
			return n, nil
		}
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		p.i = start
		return nil, p.fail("bad number")
	}
	return f, nil
}

// word reads true, false, null or a shell helper.
func (p *parser) word(depth int) (any, error) {
	start := p.i
	name, _ := p.ident()
	switch name {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	case "new":
		p.space()
		name, _ = p.ident()
	}
	if !p.eat('(') {
		p.i = start
		return nil, p.fail("unknown word")
	}
	var args A
	for !p.eat(')') {
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		args = append(args, v)
		if p.eat(',') {
			continue
		}
		if !p.eat(')') {
			return nil, p.fail("expected , or )")
		}
		break
	}
	arg := func() string {
		if len(args) == 1 {
			switch a := args[0].(type) {
			case string:
				return a
			case int32:
				return strconv.FormatInt(int64(a), 10)
			case int64:
				return strconv.FormatInt(a, 10)
			}
		}
		return "\x00"
	}
	bad := func() error {
		p.i = start
		return p.fail(name + "() takes one value")
	}
	switch name {
	case "ObjectId", "ObjectID":
		return parseObjectID(arg())
	case "ISODate", "Date":
		if len(args) == 0 {
			return DateTime(time.Now().UnixMilli()), nil
		}
		return parseDate(arg())
	case "NumberLong":
		n, err := strconv.ParseInt(arg(), 10, 64)
		if err != nil {
			return nil, bad()
		}
		return n, nil
	case "NumberInt":
		n, err := strconv.ParseInt(arg(), 10, 32)
		if err != nil {
			return nil, bad()
		}
		return int32(n), nil
	case "NumberDecimal", "Decimal128":
		return ParseDecimal128(arg())
	case "UUID":
		return parseUUID(arg())
	}
	p.i = start
	return nil, p.fail("unknown function " + name)
}

func parseObjectID(s string) (ObjectID, error) {
	var id ObjectID
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 12 {
		return id, fmt.Errorf("an ObjectId is 24 hex digits, not %q", s)
	}
	copy(id[:], b)
	return id, nil
}

func parseDate(s string) (DateTime, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05Z0700", "2006-01-02T15:04:05.000Z0700", "2006-01-02T15:04Z07:00", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return DateTime(t.UnixMilli()), nil
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return DateTime(n), nil
	}
	return 0, fmt.Errorf("not a date: %q (use 2026-11-27T09:14:02Z)", s)
}

func parseUUID(s string) (Binary, error) {
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 {
		return Binary{}, fmt.Errorf("not a UUID: %q", s)
	}
	return Binary{Subtype: 4, Data: b}, nil
}

// unwrap turns an Extended JSON wrapper ({"$oid": "..."}) into its value;
// any other document, query operators included, stays a document.
func unwrap(d D) (any, error) {
	if len(d) == 2 && d[0].Key == "$code" && d[1].Key == "$scope" {
		code, ok1 := d[0].Value.(string)
		scope, ok2 := d[1].Value.(D)
		if ok1 && ok2 {
			return CodeWithScope{Code: code, Scope: scope}, nil
		}
	}
	if len(d) != 1 {
		return d, nil
	}
	v := d[0].Value
	str, isStr := v.(string)
	sub, isDoc := v.(D)
	bad := fmt.Errorf("bad %s value", d[0].Key)
	switch d[0].Key {
	case "$oid":
		if !isStr {
			return nil, bad
		}
		return parseObjectID(str)
	case "$numberInt":
		n, err := strconv.ParseInt(str, 10, 32)
		if !isStr || err != nil {
			return nil, bad
		}
		return int32(n), nil
	case "$numberLong":
		n, err := strconv.ParseInt(str, 10, 64)
		if !isStr || err != nil {
			return nil, bad
		}
		return n, nil
	case "$numberDouble":
		if !isStr {
			return nil, bad
		}
		switch str {
		case "Infinity":
			return math.Inf(1), nil
		case "-Infinity":
			return math.Inf(-1), nil
		case "NaN":
			return math.NaN(), nil
		}
		f, err := strconv.ParseFloat(str, 64)
		if err != nil {
			return nil, bad
		}
		return f, nil
	case "$numberDecimal":
		if !isStr {
			return nil, bad
		}
		return ParseDecimal128(str)
	case "$date":
		switch x := v.(type) {
		case string:
			return parseDate(x)
		case int32:
			return DateTime(x), nil
		case int64:
			return DateTime(x), nil
		}
		return nil, bad
	case "$binary":
		b64, _ := getStr(sub, "base64")
		st, _ := getStr(sub, "subType")
		data, err := base64.StdEncoding.DecodeString(b64)
		n, err2 := strconv.ParseUint(st, 16, 8)
		if !isDoc || err != nil || err2 != nil {
			return nil, bad
		}
		return Binary{Subtype: byte(n), Data: data}, nil
	case "$uuid":
		if !isStr {
			return nil, bad
		}
		return parseUUID(str)
	case "$regularExpression":
		pat, ok1 := getStr(sub, "pattern")
		opt, ok2 := getStr(sub, "options")
		if !isDoc || !ok1 || !ok2 {
			return nil, bad
		}
		return Regex{Pattern: pat, Options: opt}, nil
	case "$timestamp":
		t, ok1 := sub.Get("t")
		i, ok2 := sub.Get("i")
		tn, ok3 := asUint32(t)
		in, ok4 := asUint32(i)
		if !isDoc || !ok1 || !ok2 || !ok3 || !ok4 {
			return nil, bad
		}
		return Timestamp{T: tn, I: in}, nil
	case "$code":
		if !isStr {
			return nil, bad
		}
		return JavaScript(str), nil
	case "$symbol":
		if !isStr {
			return nil, bad
		}
		return Symbol(str), nil
	case "$minKey":
		return MinKey{}, nil
	case "$maxKey":
		return MaxKey{}, nil
	case "$undefined":
		return Undefined{}, nil
	case "$dbPointer":
		ns, ok1 := getStr(sub, "$ref")
		idv, _ := sub.Get("$id")
		id, ok2 := idv.(ObjectID)
		if !isDoc || !ok1 || !ok2 {
			return nil, bad
		}
		return DBPointer{Ns: ns, ID: id}, nil
	}
	return d, nil
}

func getStr(d D, key string) (string, bool) {
	v, _ := d.Get(key)
	s, ok := v.(string)
	return s, ok
}

func asUint32(v any) (uint32, bool) {
	switch n := v.(type) {
	case int32:
		return uint32(n), n >= 0
	case int64:
		return uint32(n), n >= 0 && n <= math.MaxUint32
	}
	return 0, false
}

// MarshalJSON writes d as canonical Extended JSON, so documents go into
// API types (json.RawMessage) as they are.
func (d D) MarshalJSON() ([]byte, error) { return AppendJSON(nil, d), nil }

// Compact is v's Extended JSON, as AppendJSON writes it.
func Compact(v any) string { return string(AppendJSON(nil, v)) }

// Equal reports whether two values are the same BSON (same types, same
// order), as their encodings compare.
func Equal(a, b any) bool { return bytes.Equal(AppendJSON(nil, a), AppendJSON(nil, b)) }

// ParseArgs reads a call's arguments, "(value, ...)", at the start of s
// (spaces before it allowed) and returns them and the rest of s.
func ParseArgs(s string) (A, string, error) {
	p := parser{s: s}
	if !p.eat('(') {
		return nil, "", p.fail("expected (")
	}
	args := A{}
	for !p.eat(')') {
		v, err := p.value(0)
		if err != nil {
			return nil, "", err
		}
		args = append(args, v)
		if p.eat(',') {
			continue
		}
		if !p.eat(')') {
			return nil, "", p.fail("expected , or )")
		}
		break
	}
	return args, p.s[p.i:], nil
}
