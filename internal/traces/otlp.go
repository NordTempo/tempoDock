// Package traces is a server's trace ingest: OTLP/HTTP, protobuf and
// JSON, from the apps Dock runs (tracing over OTLP). Each request carries
// a service token (token.go) that names the org, project, environment,
// service and release its spans belong to; spans become analytics.Span
// lines in the server's spans spool, which the control plane forwards to
// the trace store.
//
// The ingest decodes only what it keeps, caps every span on the way in
// (names, attributes, events, a span's size) and masks the values of
// sensitive attributes before anything is stored (attrs.go). Past its
// caps it answers 200 with the spans counted as rejected (OTLP's partial
// success), so SDKs drop them instead of retrying: apps never wait and
// nothing queues without a bound.
package traces

import (
	"errors"
	"math"

	"google.golang.org/protobuf/encoding/protowire"
)

// rawSpan is an OTLP span as decoded, before it becomes an analytics.Span.
type rawSpan struct {
	traceID          [16]byte
	spanID, parentID uint64
	name             string
	kind             int
	start, end       uint64 // Unix nanoseconds
	attrs            []attr
	events           []event
	statusCode       int
	statusMessage    string
}

// event is a span event: an exception, or a log-like annotation.
type event struct {
	time  uint64 // Unix nanoseconds
	name  string
	attrs []attr
}

// attr is a key and its value.
type attr struct {
	key string
	val value
}

// value is an OTLP AnyValue: a string, bool, integer, float, bytes, list
// or key-value list.
type value struct {
	kind valueKind
	s    string
	i    int64
	f    float64
	b    bool
	list []value
	kv   []attr
}

// valueKind says which of value's fields is set.
type valueKind uint8

const (
	valueNone valueKind = iota
	valueString
	valueBool
	valueInt
	valueDouble
	valueBytes
	valueList
	valueMap
)

// errBadProto is a request body that isn't an OTLP trace export.
var errBadProto = errors.New("the body isn't an OTLP ExportTraceServiceRequest")

// maxDepth bounds nested lists and maps in attribute values.
const maxDepth = 8

// decodeProto reads an ExportTraceServiceRequest, calling add for every
// span with its resource's attributes.
func decodeProto(b []byte, add func(res []attr, sp *rawSpan) error) error {
	return fields(b, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
		if num != 1 || typ != protowire.BytesType { // resource_spans
			return nil
		}
		return decodeResourceSpans(v, add)
	})
}

// decodeResourceSpans reads one ResourceSpans: its resource's attributes
// first, wherever they come, then its scopes' spans.
func decodeResourceSpans(b []byte, add func(res []attr, sp *rawSpan) error) error {
	var res []attr
	err := fields(b, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
		if num == 1 && typ == protowire.BytesType { // resource
			return fields(v, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
				if num == 1 && typ == protowire.BytesType {
					a, err := decodeKeyValue(v, 0)
					if err != nil {
						return err
					}
					res = append(res, a)
				}
				return nil
			})
		}
		return nil
	})
	if err != nil {
		return err
	}
	return fields(b, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
		if num != 2 || typ != protowire.BytesType { // scope_spans
			return nil
		}
		return fields(v, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
			if num != 2 || typ != protowire.BytesType { // spans
				return nil
			}
			var sp rawSpan
			if err := decodeSpan(v, &sp); err != nil {
				return err
			}
			return add(res, &sp)
		})
	})
}

// decodeSpan reads one Span.
func decodeSpan(b []byte, sp *rawSpan) error {
	return fields(b, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error {
		switch {
		case num == 1 && typ == protowire.BytesType && len(v) == 16:
			copy(sp.traceID[:], v)
		case num == 2 && typ == protowire.BytesType && len(v) == 8:
			sp.spanID = be64(v)
		case num == 4 && typ == protowire.BytesType && len(v) == 8:
			sp.parentID = be64(v)
		case num == 5 && typ == protowire.BytesType:
			sp.name = string(v)
		case num == 6 && typ == protowire.VarintType:
			sp.kind = int(n)
		case num == 7 && typ == protowire.Fixed64Type:
			sp.start = n
		case num == 8 && typ == protowire.Fixed64Type:
			sp.end = n
		case num == 9 && typ == protowire.BytesType:
			a, err := decodeKeyValue(v, 0)
			if err != nil {
				return err
			}
			sp.attrs = append(sp.attrs, a)
		case num == 11 && typ == protowire.BytesType:
			e, err := decodeEvent(v)
			if err != nil {
				return err
			}
			sp.events = append(sp.events, e)
		case num == 15 && typ == protowire.BytesType: // status
			return fields(v, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error {
				switch {
				case num == 2 && typ == protowire.BytesType:
					sp.statusMessage = string(v)
				case num == 3 && typ == protowire.VarintType:
					sp.statusCode = int(n)
				}
				return nil
			})
		}
		return nil
	})
}

// decodeEvent reads one Span.Event.
func decodeEvent(b []byte) (event, error) {
	var e event
	err := fields(b, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error {
		switch {
		case num == 1 && typ == protowire.Fixed64Type:
			e.time = n
		case num == 2 && typ == protowire.BytesType:
			e.name = string(v)
		case num == 3 && typ == protowire.BytesType:
			a, err := decodeKeyValue(v, 0)
			if err != nil {
				return err
			}
			e.attrs = append(e.attrs, a)
		}
		return nil
	})
	return e, err
}

// decodeKeyValue reads one KeyValue.
func decodeKeyValue(b []byte, depth int) (attr, error) {
	var a attr
	err := fields(b, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
		switch {
		case num == 1 && typ == protowire.BytesType:
			a.key = string(v)
		case num == 2 && typ == protowire.BytesType:
			var err error
			a.val, err = decodeAnyValue(v, depth)
			return err
		}
		return nil
	})
	return a, err
}

// decodeAnyValue reads one AnyValue.
func decodeAnyValue(b []byte, depth int) (value, error) {
	if depth >= maxDepth {
		return value{}, nil
	}
	var out value
	err := fields(b, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error {
		switch {
		case num == 1 && typ == protowire.BytesType:
			out = value{kind: valueString, s: string(v)}
		case num == 2 && typ == protowire.VarintType:
			out = value{kind: valueBool, b: n != 0}
		case num == 3 && typ == protowire.VarintType:
			out = value{kind: valueInt, i: int64(n)}
		case num == 4 && typ == protowire.Fixed64Type:
			out = value{kind: valueDouble, f: math.Float64frombits(n)}
		case num == 5 && typ == protowire.BytesType: // array_value
			out = value{kind: valueList}
			return fields(v, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
				if num == 1 && typ == protowire.BytesType {
					x, err := decodeAnyValue(v, depth+1)
					if err != nil {
						return err
					}
					out.list = append(out.list, x)
				}
				return nil
			})
		case num == 6 && typ == protowire.BytesType: // kvlist_value
			out = value{kind: valueMap}
			return fields(v, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
				if num == 1 && typ == protowire.BytesType {
					a, err := decodeKeyValue(v, depth+1)
					if err != nil {
						return err
					}
					out.kv = append(out.kv, a)
				}
				return nil
			})
		case num == 7 && typ == protowire.BytesType:
			out = value{kind: valueBytes, s: string(v)}
		}
		return nil
	})
	return out, err
}

// fields walks a message's fields, calling f with each one's number,
// type and value: v for length-delimited fields, n for varints and fixed
// widths. Groups and unknown fields are skipped.
func fields(b []byte, f func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error) error {
	for len(b) > 0 {
		num, typ, k := protowire.ConsumeTag(b)
		if k < 0 {
			return errBadProto
		}
		b = b[k:]
		var v []byte
		var n uint64
		switch typ {
		case protowire.VarintType:
			n, k = protowire.ConsumeVarint(b)
		case protowire.Fixed64Type:
			n, k = protowire.ConsumeFixed64(b)
		case protowire.Fixed32Type:
			var x uint32
			x, k = protowire.ConsumeFixed32(b)
			n = uint64(x)
		case protowire.BytesType:
			v, k = protowire.ConsumeBytes(b)
		default:
			k = protowire.ConsumeFieldValue(num, typ, b)
		}
		if k < 0 {
			return errBadProto
		}
		b = b[k:]
		if err := f(num, typ, v, n); err != nil {
			return err
		}
	}
	return nil
}

// be64 reads 8 big-endian bytes, as OTLP's span IDs are.
func be64(b []byte) uint64 {
	return uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
		uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
}
