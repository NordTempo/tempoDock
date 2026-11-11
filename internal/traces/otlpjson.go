package traces

import (
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

type jsonRequest struct {
	ResourceSpans []jsonResourceSpans `json:"resourceSpans"`
}

type jsonResourceSpans struct {
	Resource struct {
		Attributes []jsonKV `json:"attributes"`
	} `json:"resource"`
	ScopeSpans []struct {
		Spans []jsonSpan `json:"spans"`
	} `json:"scopeSpans"`
}

type jsonSpan struct {
	TraceID           string         `json:"traceId"`
	SpanID            string         `json:"spanId"`
	ParentSpanID      string         `json:"parentSpanId"`
	Name              string         `json:"name"`
	Kind              jsontext.Value `json:"kind"`
	StartTimeUnixNano jsontext.Value `json:"startTimeUnixNano"`
	EndTimeUnixNano   jsontext.Value `json:"endTimeUnixNano"`
	Attributes        []jsonKV       `json:"attributes"`
	Events            []jsonEvent    `json:"events"`
	Status            struct {
		Code    jsontext.Value `json:"code"`
		Message string         `json:"message"`
	} `json:"status"`
}

type jsonEvent struct {
	TimeUnixNano jsontext.Value `json:"timeUnixNano"`
	Name         string         `json:"name"`
	Attributes   []jsonKV       `json:"attributes"`
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
		Values []jsonAny `json:"values"`
	} `json:"arrayValue"`
	KvlistValue *struct {
		Values []jsonKV `json:"values"`
	} `json:"kvlistValue"`
}

// errBadJSON is a JSON body that isn't an OTLP trace export.
var errBadJSON = errors.New("the body isn't an OTLP/JSON ExportTraceServiceRequest")

// spanKinds and statusCodes are OTLP's enum names, for JSON that names
// them instead of numbering them.
var (
	spanKinds   = map[string]int{"SPAN_KIND_UNSPECIFIED": 0, "SPAN_KIND_INTERNAL": 1, "SPAN_KIND_SERVER": 2, "SPAN_KIND_CLIENT": 3, "SPAN_KIND_PRODUCER": 4, "SPAN_KIND_CONSUMER": 5}
	statusCodes = map[string]int{"STATUS_CODE_UNSET": 0, "STATUS_CODE_OK": 1, "STATUS_CODE_ERROR": 2}
)

// decodeJSON reads an OTLP/JSON ExportTraceServiceRequest, calling add for
// every span with its resource's attributes. A span whose IDs aren't hex
// is passed with zero IDs, which the caller rejects.
func decodeJSON(b []byte, add func(res []attr, sp *rawSpan) error) error {
	var req jsonRequest
	if err := json.Unmarshal(b, &req); err != nil {
		return errBadJSON
	}
	for _, rs := range req.ResourceSpans {
		res := jsonAttrs(rs.Resource.Attributes, 0)
		for _, ss := range rs.ScopeSpans {
			for i := range ss.Spans {
				js := &ss.Spans[i]
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
				if err := add(res, &sp); err != nil {
					return err
				}
			}
		}
	}
	return nil
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
