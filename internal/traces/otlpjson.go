package traces

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"strconv"
	"strings"
)

// OTLP/JSON (the protobuf JSON mapping, with hex trace and span IDs):
// 64-bit integers may come as strings or numbers, and enums as numbers or
// their names.

type jsonSpan struct {
	TraceID           string         `json:"traceId"`
	SpanID            string         `json:"spanId"`
	ParentSpanID      string         `json:"parentSpanId"`
	Name              string         `json:"name"`
	Kind              jsontext.Value `json:"kind"`
	StartTimeUnixNano jsontext.Value `json:"startTimeUnixNano"`
	EndTimeUnixNano   jsontext.Value `json:"endTimeUnixNano"`
	Attributes        jsonKVs        `json:"attributes"`
	Events            jsonEvents     `json:"events"`
	Status            struct {
		Code    jsontext.Value `json:"code"`
		Message string         `json:"message"`
	} `json:"status"`
}

type jsonEvent struct {
	TimeUnixNano jsontext.Value `json:"timeUnixNano"`
	Name         string         `json:"name"`
	Attributes   jsonKVs        `json:"attributes"`
}

type jsonKV struct {
	Key   string  `json:"key"`
	Value jsonAny `json:"value"`
}

type jsonAny struct {
	StringValue *string        `json:"stringValue"`
	BoolValue   *bool          `json:"boolValue"`
	IntValue    jsontext.Value `json:"intValue"`
	DoubleValue jsontext.Value `json:"doubleValue"`
	BytesValue  *string        `json:"bytesValue"`
	ArrayValue  *struct {
		Values jsonAnys `json:"values"`
	} `json:"arrayValue"`
	KvlistValue *struct {
		Values jsonKVList `json:"values"`
	} `json:"kvlistValue"`
}

// jsonKVs, jsonEvents, jsonAnys and jsonKVList are capped lists, as
// decodeSpan caps protobuf: decodeJSON's unmarshalers keep at most
// maxAttrs attributes a span or event, maxEvents events (preferring
// exceptions) and maxList entries a nested list or map, each nested one
// spending a value of the span's budget, and skip the rest unread.
type (
	jsonKVs    []jsonKV
	jsonEvents []jsonEvent
	jsonAnys   []jsonAny
	jsonKVList []jsonKV
)

// errBadJSON is a JSON body that isn't an OTLP trace export.
var errBadJSON = errors.New("the body isn't an OTLP/JSON ExportTraceServiceRequest")

// spanKinds and statusCodes are OTLP's enum names, for JSON that names
// them instead of numbering them.
var (
	spanKinds   = map[string]int{"SPAN_KIND_UNSPECIFIED": 0, "SPAN_KIND_INTERNAL": 1, "SPAN_KIND_SERVER": 2, "SPAN_KIND_CLIENT": 3, "SPAN_KIND_PRODUCER": 4, "SPAN_KIND_CONSUMER": 5}
	statusCodes = map[string]int{"STATUS_CODE_UNSET": 0, "STATUS_CODE_OK": 1, "STATUS_CODE_ERROR": 2}
)

// decodeJSON reads an OTLP/JSON ExportTraceServiceRequest, calling add for
// every span. It streams: one span is decoded at a time, within the same
// caps as protobuf (decodeSpan), and resources' attributes are skipped. A
// span whose IDs aren't hex is passed with zero IDs, which the caller
// rejects.
func decodeJSON(b []byte, add func(sp *rawSpan) error) error {
	var bud budget
	var addErr error
	var opts json.Options
	opts = json.WithUnmarshalers(json.JoinUnmarshalers(
		json.UnmarshalFromFunc(func(dec *jsontext.Decoder, v *jsonKVs) error {
			return capped(dec, opts, (*[]jsonKV)(v), maxAttrs, nil, func(l []jsonKV, x jsonKV) []jsonKV { return append(l, x) })
		}),
		json.UnmarshalFromFunc(func(dec *jsontext.Decoder, v *jsonAnys) error {
			return capped(dec, opts, (*[]jsonAny)(v), maxList, &bud, func(l []jsonAny, x jsonAny) []jsonAny { return append(l, x) })
		}),
		json.UnmarshalFromFunc(func(dec *jsontext.Decoder, v *jsonKVList) error {
			return capped(dec, opts, (*[]jsonKV)(v), maxList, &bud, func(l []jsonKV, x jsonKV) []jsonKV { return append(l, x) })
		}),
		json.UnmarshalFromFunc(func(dec *jsontext.Decoder, v *jsonEvents) error {
			return capped(dec, opts, (*[]jsonEvent)(v), -1, nil, func(l []jsonEvent, x jsonEvent) []jsonEvent {
				if len(l) < maxEvents {
					return append(l, x)
				}
				if x.Name == "exception" {
					for i := len(l) - 1; i >= 0; i-- {
						if l[i].Name != "exception" {
							l[i] = x
							break
						}
					}
				}
				return l
			})
		}),
	))
	dec := jsontext.NewDecoder(bytes.NewReader(b))
	err := eachField(dec, "resourceSpans", func() error {
		return eachItem(dec, func() error {
			return eachField(dec, "scopeSpans", func() error {
				return eachItem(dec, func() error {
					return eachField(dec, "spans", func() error {
						return eachItem(dec, func() error {
							bud = spanBudget
							var js jsonSpan
							if err := json.UnmarshalDecode(dec, &js, opts); err != nil {
								return err
							}
							sp := rawSpan{
								name: js.Name, kind: jsonEnum(js.Kind, spanKinds),
								start: jsonUint(js.StartTimeUnixNano), end: jsonUint(js.EndTimeUnixNano),
								attrs: jsonAttrs(js.Attributes, 0), statusCode: jsonEnum(js.Status.Code, statusCodes), statusMessage: js.Status.Message,
							}
							if id, err := hex.DecodeString(js.TraceID); err == nil && len(id) == 16 {
								copy(sp.traceID[:], id)
							}
							sp.spanID, sp.parentID = hexID(js.SpanID), hexID(js.ParentSpanID)
							for _, je := range js.Events {
								sp.events = append(sp.events, event{time: jsonUint(je.TimeUnixNano), name: je.Name, attrs: jsonAttrs(je.Attributes, 0)})
							}
							addErr = add(&sp)
							return addErr
						})
					})
				})
			})
		})
	})
	switch {
	case addErr != nil:
		return addErr
	case err != nil:
		return errBadJSON
	}
	return nil
}

// capped decodes a JSON array (or null) into list, its items with opts
// (decodeJSON's unmarshalers), and keep adds each: past n items (n < 0: no
// cap) or bud (nil: none) the rest are skipped.
func capped[T any](dec *jsontext.Decoder, opts json.Options, list *[]T, n int, bud *budget, keep func([]T, T) []T) error {
	return eachItem(dec, func() error {
		if len(*list) == n || (bud != nil && !bud.take()) {
			return dec.SkipValue()
		}
		var x T
		if err := json.UnmarshalDecode(dec, &x, opts); err != nil {
			return err
		}
		*list = keep(*list, x)
		return nil
	})
}

// eachItem calls f for each item of the JSON array dec is at, f reading
// it; null is an empty array.
func eachItem(dec *jsontext.Decoder, f func() error) error {
	if dec.PeekKind() == 'n' {
		_, err := dec.ReadToken()
		return err
	}
	if tok, err := dec.ReadToken(); err != nil {
		return err
	} else if tok.Kind() != '[' {
		return errBadJSON
	}
	for dec.PeekKind() != ']' {
		if err := f(); err != nil {
			return err
		}
	}
	_, err := dec.ReadToken()
	return err
}

// eachField reads the JSON object (or null) dec is at, calling f to read
// the value of its member name and skipping the others.
func eachField(dec *jsontext.Decoder, name string, f func() error) error {
	if dec.PeekKind() == 'n' {
		_, err := dec.ReadToken()
		return err
	}
	if tok, err := dec.ReadToken(); err != nil {
		return err
	} else if tok.Kind() != '{' {
		return errBadJSON
	}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return err
		}
		if tok.String() == name {
			err = f()
		} else {
			err = dec.SkipValue()
		}
		if err != nil {
			return err
		}
	}
	_, err := dec.ReadToken()
	return err
}

// jsonAttrs converts key-values.
func jsonAttrs(kvs []jsonKV, depth int) []attr {
	if len(kvs) == 0 || depth >= maxDepth {
		return nil
	}
	out := make([]attr, len(kvs))
	for i, kv := range kvs {
		out[i] = attr{key: kv.Key, val: jsonValue(kv.Value, depth)}
	}
	return out
}

// jsonValue converts an AnyValue.
func jsonValue(v jsonAny, depth int) value {
	switch {
	case v.StringValue != nil:
		return value{kind: valueString, s: *v.StringValue}
	case v.BoolValue != nil:
		return value{kind: valueBool, b: *v.BoolValue}
	case len(v.IntValue) > 0:
		n, _ := strconv.ParseInt(unquote(v.IntValue), 10, 64)
		return value{kind: valueInt, i: n}
	case len(v.DoubleValue) > 0:
		f, _ := strconv.ParseFloat(unquote(v.DoubleValue), 64)
		return value{kind: valueDouble, f: f}
	case v.BytesValue != nil:
		b, _ := base64.StdEncoding.DecodeString(*v.BytesValue)
		return value{kind: valueBytes, s: string(b)}
	case v.ArrayValue != nil && depth < maxDepth:
		out := value{kind: valueList}
		for _, x := range v.ArrayValue.Values {
			out.list = append(out.list, jsonValue(x, depth+1))
		}
		return out
	case v.KvlistValue != nil:
		return value{kind: valueMap, kv: jsonAttrs(v.KvlistValue.Values, depth+1)}
	}
	return value{}
}

// jsonUint reads a 64-bit unsigned integer given as a number or a string.
func jsonUint(v jsontext.Value) uint64 {
	n, _ := strconv.ParseUint(unquote(v), 10, 64)
	return n
}

// jsonEnum reads an enum given as its number or its name.
func jsonEnum(v jsontext.Value, names map[string]int) int {
	s := unquote(v)
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return names[s]
}

// unquote is a JSON number or string's text.
func unquote(v jsontext.Value) string {
	return strings.Trim(string(v), `"`)
}

// hexID reads a 16-digit hex span ID, 0 for anything else.
func hexID(s string) uint64 {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 8 {
		return 0
	}
	return be64(b)
}
