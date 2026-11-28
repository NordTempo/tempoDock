// Package wire is the slice of MongoDB's protocol the data panel's MongoDB
// engine uses (ADR 0110): BSON documents, OP_MSG commands and SCRAM-SHA-256
// logins, over a connection the caller dialled. It works with MongoDB
// 6.0+. Replies are read within caps (MaxReply, maxDepth), so a huge
// batch or a hostile server can't make Dock hold more than it allows.
//
// A Conn is one connection used by one caller at a time; there is no pool,
// no monitoring goroutine and no timer. Deadlines are the caller's
// (NetConn().SetDeadline).
package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// D is a BSON document: its fields in order.
type D []E

// E is one field of a document.
type E struct {
	Key   string
	Value any
}

// Get is the value of the field named key, and whether there is one.
func (d D) Get(key string) (any, bool) {
	for _, e := range d {
		if e.Key == key {
			return e.Value, true
		}
	}
	return nil, false
}

// A is a BSON array.
type A []any

// The BSON types beyond Go's own (nil, bool, int32, int64, float64,
// string, D, A).
type (
	// ObjectID is a 12-byte object ID.
	ObjectID [12]byte
	// DateTime is milliseconds since the Unix epoch, UTC; kept as the
	// number so dates outside time.Time's range survive.
	DateTime int64
	// Binary is binary data with its subtype (4 is a UUID).
	Binary struct {
		Subtype byte
		Data    []byte
	}
	// Regex is a regular expression and its options.
	Regex struct{ Pattern, Options string }
	// Timestamp is MongoDB's internal timestamp: seconds and an ordinal.
	Timestamp struct{ T, I uint32 }
	// Decimal128 is an IEEE 754-2008 128-bit decimal in BID encoding.
	Decimal128 struct{ H, L uint64 }
	// JavaScript is code.
	JavaScript string
	// Symbol is the deprecated symbol type.
	Symbol string
	// CodeWithScope is the deprecated code with a scope document.
	CodeWithScope struct {
		Code  string
		Scope D
	}
	// DBPointer is the deprecated database pointer.
	DBPointer struct {
		Ns string
		ID ObjectID
	}
	// MinKey and MaxKey compare below and above every other value.
	MinKey struct{}
	MaxKey struct{}
	// Undefined is the deprecated undefined value.
	Undefined struct{}
)

// BSON element types.
const (
	tDouble     = 0x01
	tString     = 0x02
	tDoc        = 0x03
	tArray      = 0x04
	tBinary     = 0x05
	tUndefined  = 0x06
	tObjectID   = 0x07
	tBool       = 0x08
	tDateTime   = 0x09
	tNull       = 0x0A
	tRegex      = 0x0B
	tDBPointer  = 0x0C
	tJavaScript = 0x0D
	tSymbol     = 0x0E
	tCodeScope  = 0x0F
	tInt32      = 0x10
	tTimestamp  = 0x11
	tInt64      = 0x12
	tDecimal    = 0x13
	tMinKey     = 0xFF
	tMaxKey     = 0x7F
)

// maxDepth is how deeply documents and arrays may nest (MongoDB's own
// limit is 100 for stored documents).
const maxDepth = 128

// ErrBSON is a malformed BSON document.
var ErrBSON = errors.New("malformed BSON")

// AppendDoc appends d as BSON. A value of a type BSON doesn't have is an
// error.
func AppendDoc(b []byte, d D) ([]byte, error) { return appendDoc(b, d, 0) }

func appendDoc(b []byte, d D, depth int) ([]byte, error) {
	if depth > maxDepth {
		return b, errors.New("document nested too deeply")
	}
	start := len(b)
	b = append(b, 0, 0, 0, 0)
	var err error
	for _, e := range d {
		if b, err = appendElem(b, e.Key, e.Value, depth); err != nil {
			return b, err
		}
	}
	b = append(b, 0)
	binary.LittleEndian.PutUint32(b[start:], uint32(len(b)-start))
	return b, nil
}

func appendArray(b []byte, a A, depth int) ([]byte, error) {
	d := make(D, len(a))
	for i, v := range a {
		d[i] = E{Key: strconv.Itoa(i), Value: v}
	}
	return appendDoc(b, d, depth)
}

func appendCString(b []byte, s string) ([]byte, error) {
	if strings.IndexByte(s, 0) >= 0 {
		return b, fmt.Errorf("a field name or pattern can't hold a NUL byte: %q", s)
	}
	return append(append(b, s...), 0), nil
}

func appendString(b []byte, s string) []byte {
	b = binary.LittleEndian.AppendUint32(b, uint32(len(s)+1))
	return append(append(b, s...), 0)
}

func appendElem(b []byte, key string, v any, depth int) ([]byte, error) {
	kind, err := typeOf(v)
	if err != nil {
		return b, fmt.Errorf("%s: %w", key, err)
	}
	b = append(b, kind)
	if b, err = appendCString(b, key); err != nil {
		return b, err
	}
	switch v := v.(type) {
	case float64:
		b = binary.LittleEndian.AppendUint64(b, math.Float64bits(v))
	case string:
		b = appendString(b, v)
	case D:
		return appendDoc(b, v, depth+1)
	case A:
		return appendArray(b, v, depth+1)
	case Binary:
		b = binary.LittleEndian.AppendUint32(b, uint32(len(v.Data)))
		b = append(append(b, v.Subtype), v.Data...)
	case ObjectID:
		b = append(b, v[:]...)
	case bool:
		if v {
			b = append(b, 1)
		} else {
			b = append(b, 0)
		}
	case DateTime:
		b = binary.LittleEndian.AppendUint64(b, uint64(v))
	case Regex:
		if b, err = appendCString(b, v.Pattern); err != nil {
			return b, err
		}
		return appendCString(b, v.Options)
	case DBPointer:
		b = append(appendString(b, v.Ns), v.ID[:]...)
	case JavaScript:
		b = appendString(b, string(v))
	case Symbol:
		b = appendString(b, string(v))
	case CodeWithScope:
		start := len(b)
		b = appendString(append(b, 0, 0, 0, 0), v.Code)
		if b, err = appendDoc(b, v.Scope, depth+1); err != nil {
			return b, err
		}
		binary.LittleEndian.PutUint32(b[start:], uint32(len(b)-start))
	case int32:
		b = binary.LittleEndian.AppendUint32(b, uint32(v))
	case Timestamp:
		b = binary.LittleEndian.AppendUint32(b, v.I)
		b = binary.LittleEndian.AppendUint32(b, v.T)
	case int64:
		b = binary.LittleEndian.AppendUint64(b, uint64(v))
	case Decimal128:
		b = binary.LittleEndian.AppendUint64(b, v.L)
		b = binary.LittleEndian.AppendUint64(b, v.H)
	}
	return b, nil
}

// typeOf is the BSON type of v.
func typeOf(v any) (byte, error) {
	switch v.(type) {
	case float64:
		return tDouble, nil
	case string:
		return tString, nil
	case D:
		return tDoc, nil
	case A:
		return tArray, nil
	case Binary:
		return tBinary, nil
	case Undefined:
		return tUndefined, nil
	case ObjectID:
		return tObjectID, nil
	case bool:
		return tBool, nil
	case DateTime:
		return tDateTime, nil
	case nil:
		return tNull, nil
	case Regex:
		return tRegex, nil
	case DBPointer:
		return tDBPointer, nil
	case JavaScript:
		return tJavaScript, nil
	case Symbol:
		return tSymbol, nil
	case CodeWithScope:
		return tCodeScope, nil
	case int32:
		return tInt32, nil
	case Timestamp:
		return tTimestamp, nil
	case int64:
		return tInt64, nil
	case Decimal128:
		return tDecimal, nil
	case MinKey:
		return tMinKey, nil
	case MaxKey:
		return tMaxKey, nil
	}
	return 0, fmt.Errorf("BSON has no type for %T", v)
}

// ReadDoc decodes one BSON document that fills b exactly.
func ReadDoc(b []byte) (D, error) {
	r := reader{b: b}
	d, err := r.doc(0)
	if err != nil {
		return nil, err
	}
	if r.i != len(b) {
		return nil, fmt.Errorf("%w: %d bytes after the document", ErrBSON, len(b)-r.i)
	}
	return d, nil
}

// reader decodes BSON from b, checking every length against what is
// left.
type reader struct {
	b []byte
	i int
}

func (r *reader) fail(what string) error {
	return fmt.Errorf("%w: %s at byte %d", ErrBSON, what, r.i)
}

func (r *reader) take(n int) ([]byte, error) {
	if n < 0 || n > len(r.b)-r.i {
		return nil, r.fail("length past the end")
	}
	p := r.b[r.i : r.i+n]
	r.i += n
	return p, nil
}

func (r *reader) u32() (uint32, error) {
	p, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(p), nil
}

func (r *reader) u64() (uint64, error) {
	p, err := r.take(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(p), nil
}

func (r *reader) cstring() (string, error) {
	j := r.i
	for j < len(r.b) && r.b[j] != 0 {
		j++
	}
	if j == len(r.b) {
		return "", r.fail("unterminated name")
	}
	s := string(r.b[r.i:j])
	r.i = j + 1
	return s, nil
}

func (r *reader) str() (string, error) {
	n, err := r.u32()
	if err != nil {
		return "", err
	}
	if n < 1 || int64(n) > int64(len(r.b)-r.i) {
		return "", r.fail("bad string length")
	}
	p, _ := r.take(int(n))
	if p[n-1] != 0 {
		return "", r.fail("string without its NUL")
	}
	return string(p[:n-1]), nil
}

// doc reads a document: its length, elements and terminator, all within
// the length it states.
func (r *reader) doc(depth int) (D, error) {
	if depth > maxDepth {
		return nil, r.fail("nested too deeply")
	}
	start := r.i
	n, err := r.u32()
	if err != nil {
		return nil, err
	}
	if n < 5 || int64(n) > int64(len(r.b)-start) {
		return nil, r.fail("bad document length")
	}
	end := start + int(n)
	sub := reader{b: r.b[:end], i: r.i}
	d := D{}
	for {
		if sub.i >= end {
			return nil, sub.fail("document without its terminator")
		}
		kind := sub.b[sub.i]
		sub.i++
		if kind == 0 {
			break
		}
		key, err := sub.cstring()
		if err != nil {
			return nil, err
		}
		v, err := sub.value(kind, depth)
		if err != nil {
			return nil, err
		}
		d = append(d, E{Key: key, Value: v})
	}
	if sub.i != end {
		return nil, sub.fail("bytes after the terminator")
	}
	r.i = end
	return d, nil
}

func (r *reader) value(kind byte, depth int) (any, error) {
	switch kind {
	case tDouble:
		u, err := r.u64()
		return math.Float64frombits(u), err
	case tString:
		return r.str()
	case tDoc:
		return r.doc(depth + 1)
	case tArray:
		d, err := r.doc(depth + 1)
		if err != nil {
			return nil, err
		}
		a := make(A, len(d))
		for i, e := range d {
			a[i] = e.Value
		}
		return a, nil
	case tBinary:
		n, err := r.u32()
		if err != nil {
			return nil, err
		}
		if int64(n) >= int64(len(r.b)-r.i) {
			return nil, r.fail("bad binary length")
		}
		st, _ := r.take(1)
		p, _ := r.take(int(n))
		return Binary{Subtype: st[0], Data: append([]byte(nil), p...)}, nil
	case tUndefined:
		return Undefined{}, nil
	case tObjectID:
		p, err := r.take(12)
		var id ObjectID
		copy(id[:], p)
		return id, err
	case tBool:
		p, err := r.take(1)
		if err != nil {
			return nil, err
		}
		return p[0] != 0, nil
	case tDateTime:
		u, err := r.u64()
		return DateTime(int64(u)), err
	case tNull:
		return nil, nil
	case tRegex:
		p, err := r.cstring()
		if err != nil {
			return nil, err
		}
		o, err := r.cstring()
		return Regex{Pattern: p, Options: o}, err
	case tDBPointer:
		ns, err := r.str()
		if err != nil {
			return nil, err
		}
		p, err := r.take(12)
		var id ObjectID
		copy(id[:], p)
		return DBPointer{Ns: ns, ID: id}, err
	case tJavaScript:
		s, err := r.str()
		return JavaScript(s), err
	case tSymbol:
		s, err := r.str()
		return Symbol(s), err
	case tCodeScope:
		start := r.i
		n, err := r.u32()
		if err != nil {
			return nil, err
		}
		if n < 14 || int64(n) > int64(len(r.b)-start) {
			return nil, r.fail("bad code with scope length")
		}
		code, err := r.str()
		if err != nil {
			return nil, err
		}
		scope, err := r.doc(depth + 1)
		if err != nil {
			return nil, err
		}
		if r.i != start+int(n) {
			return nil, r.fail("code with scope length doesn't match")
		}
		return CodeWithScope{Code: code, Scope: scope}, nil
	case tInt32:
		u, err := r.u32()
		return int32(u), err
	case tTimestamp:
		u, err := r.u64()
		return Timestamp{T: uint32(u >> 32), I: uint32(u)}, err
	case tInt64:
		u, err := r.u64()
		return int64(u), err
	case tDecimal:
		l, err := r.u64()
		if err != nil {
			return nil, err
		}
		h, err := r.u64()
		return Decimal128{H: h, L: l}, err
	case tMinKey:
		return MinKey{}, nil
	case tMaxKey:
		return MaxKey{}, nil
	}
	return nil, r.fail(fmt.Sprintf("unknown type 0x%02x", kind))
}
