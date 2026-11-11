package traces

import (
	"bytes"
	"compress/gzip"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// memSpool keeps appended lines.
type memSpool struct {
	mu    sync.Mutex
	lines [][]byte
}

func (m *memSpool) Append(line []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lines = append(m.lines, append([]byte(nil), line...))
	return nil
}

// spans decodes the spooled lines.
func (m *memSpool) spans(t *testing.T) []analytics.Span {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]analytics.Span, len(m.lines))
	for i, l := range m.lines {
		if err := json.Unmarshal(l, &out[i]); err != nil {
			t.Fatalf("line %d: %v: %s", i, err, l)
		}
	}
	return out
}

var (
	testKey    = []byte("0123456789abcdef0123456789abcdef")
	testClaims = Claims{Org: 1, Project: 2, Env: 3, Service: 4, Release: 5}
)

// newIngest is an ingest with the test key and a memory spool.
func newIngest() (*Ingest, *memSpool) {
	sp := &memSpool{}
	in := &Ingest{Spool: sp, Key: &Key{}}
	in.Key.Set(testKey)
	return in, sp
}

func str(k, v string) *common.KeyValue {
	return &common.KeyValue{Key: k, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: v}}}
}

func num(k string, v int64) *common.KeyValue {
	return &common.KeyValue{Key: k, Value: &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: v}}}
}

// testRequest is an export of a server span with an exception and a
// database span under it, as an SDK sends them.
func testRequest(start time.Time) *collector.ExportTraceServiceRequest {
	trace := bytes.Repeat([]byte{0xab}, 16)
	ns := uint64(start.UnixNano())
	server := &tracepb.Span{
		TraceId: trace, SpanId: []byte{1, 2, 3, 4, 5, 6, 7, 8}, Name: "POST /orders", Kind: tracepb.Span_SPAN_KIND_SERVER,
		StartTimeUnixNano: ns, EndTimeUnixNano: ns + 250_000_000,
		Attributes: []*common.KeyValue{
			str("http.request.method", "POST"), str("http.route", "/orders"), num("http.response.status_code", 500),
			str("url.full", "https://shop.example/orders?id=7&api_key=hunter2#x"),
			str("http.request.header.authorization", "Bearer s3cret"), str("http.request.header.user-agent", "curl/8"),
			str("db.password", "hunter2"), num("llm.usage.total_tokens", 42),
			{Key: "tags", Value: &common.AnyValue{Value: &common.AnyValue_ArrayValue{ArrayValue: &common.ArrayValue{Values: []*common.AnyValue{
				{Value: &common.AnyValue_StringValue{StringValue: "a"}}, {Value: &common.AnyValue_BoolValue{BoolValue: true}},
			}}}}},
		},
		Events: []*tracepb.Span_Event{
			{Name: "log", TimeUnixNano: ns + 1000},
			{Name: "exception", TimeUnixNano: ns + 200_000_000, Attributes: []*common.KeyValue{
				str("exception.type", "QueryFailedError"), str("exception.message", "canceling statement due to statement timeout"),
				str("exception.stacktrace", strings.Repeat("at x\n", 5000)),
			}},
		},
		Status: &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "boom"},
	}
	db := &tracepb.Span{
		TraceId: trace, SpanId: []byte{9, 9, 9, 9, 9, 9, 9, 9}, ParentSpanId: server.SpanId, Name: "INSERT orders",
		Kind: tracepb.Span_SPAN_KIND_CLIENT, StartTimeUnixNano: ns + 1_000_000, EndTimeUnixNano: ns + 200_000_000,
		Attributes: []*common.KeyValue{str("db.system", "postgresql")},
	}
	return &collector.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource:   &resource.Resource{Attributes: []*common.KeyValue{str("service.name", "api")}},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{server, db}}},
	}}}
}

// post sends body to the ingest and returns the answer.
func post(in *Ingest, path, contentType, token string, body []byte, gz bool) *httptest.ResponseRecorder {
	if gz {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		_, _ = zw.Write(body)
		_ = zw.Close()
		body = b.Bytes()
	}
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if gz {
		r.Header.Set("Content-Encoding", "gzip")
	}
	w := httptest.NewRecorder()
	in.ServeHTTP(w, r)
	return w
}

// checkSpans checks what testRequest becomes.
func checkSpans(t *testing.T, got []analytics.Span, start time.Time) {
	t.Helper()
	if len(got) != 2 {
		t.Fatalf("%d spans, want 2", len(got))
	}
	s, db := got[0], got[1]
	if s.Org != 1 || s.Project != 2 || s.Env != 3 || s.Service != 4 || s.Release != 5 {
		t.Errorf("claims = %+v", s)
	}
	if s.Trace != (analytics.TraceID{0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab}) ||
		s.ID != 0x0102030405060708 || s.Parent != 0 || db.Parent != s.ID {
		t.Errorf("IDs = %v %v %v, db parent %v", s.Trace, s.ID, s.Parent, db.Parent)
	}
	if s.Start != start.UnixMicro() || s.Duration != 250_000 || !s.Entry || s.Kind != analytics.SpanServer || s.Status != analytics.StatusError ||
		s.Method != "POST" || s.Route != "/orders" || s.HTTPStatus != 500 || s.Name != "POST /orders" {
		t.Errorf("server span = %+v", s)
	}
	if db.Entry || db.Kind != analytics.SpanClient || db.Duration != 199_000 {
		t.Errorf("db span = %+v", db)
	}
	var attrs map[string]any
	if err := json.Unmarshal(s.Attrs, &attrs); err != nil {
		t.Fatalf("attrs %s: %v", s.Attrs, err)
	}
	for k, want := range map[string]any{
		"url.full":                          "https://shop.example/orders?id=7&api_key=*****#x",
		"http.request.header.authorization": "*****",
		"http.request.header.user-agent":    "curl/8",
		"db.password":                       "*****",
		"llm.usage.total_tokens":            float64(42),
		"otel.status_description":           "boom",
	} {
		if attrs[k] != want {
			t.Errorf("attrs[%q] = %v, want %v", k, attrs[k], want)
		}
	}
	if tags, _ := attrs["tags"].([]any); len(tags) != 2 || tags[1] != true {
		t.Errorf("tags = %v", attrs["tags"])
	}
	var events []struct {
		Name  string            `json:"name"`
		At    int64             `json:"at"`
		Attrs map[string]string `json:"attrs"`
	}
	if err := json.Unmarshal(s.Events, &events); err != nil {
		t.Fatalf("events %s: %v", s.Events, err)
	}
	if len(events) != 2 || events[0].Name != "exception" || events[0].At != 200_000 || events[1].Name != "log" ||
		events[0].Attrs["exception.type"] != "QueryFailedError" || len(events[0].Attrs["exception.stacktrace"]) != maxStack {
		t.Errorf("events = %+v", events)
	}
}

func TestIngestProtobuf(t *testing.T) {
	in, sp := newIngest()
	start := time.Unix(1_800_000_000, 123_000)
	body, err := proto.Marshal(testRequest(start))
	if err != nil {
		t.Fatal(err)
	}
	for _, gz := range []bool{false, true} {
		sp.lines = nil
		w := post(in, TracesPath, "application/x-protobuf", Mint(testKey, testClaims), body, gz)
		if w.Code != http.StatusOK || w.Body.Len() != 0 {
			t.Fatalf("gzip %v: %d %q", gz, w.Code, w.Body)
		}
		checkSpans(t, sp.spans(t), start)
	}
}

func TestIngestJSON(t *testing.T) {
	in, sp := newIngest()
	start := time.Unix(1_800_000_000, 123_000)
	// The OTLP JSON mapping: hex IDs, 64-bit integers as strings, enums
	// by name or number.
	body := []byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}}]},"scopeSpans":[{"spans":[
	{"traceId":"abababababababababababababababab","spanId":"0102030405060708","name":"POST /orders","kind":"SPAN_KIND_SERVER",
	 "startTimeUnixNano":"1800000000000123000","endTimeUnixNano":"1800000000250123000",
	 "attributes":[{"key":"http.request.method","value":{"stringValue":"POST"}},{"key":"http.route","value":{"stringValue":"/orders"}},
	   {"key":"http.response.status_code","value":{"intValue":"500"}},
	   {"key":"url.full","value":{"stringValue":"https://shop.example/orders?id=7&api_key=hunter2#x"}},
	   {"key":"http.request.header.authorization","value":{"stringValue":"Bearer s3cret"}},
	   {"key":"http.request.header.user-agent","value":{"stringValue":"curl/8"}},
	   {"key":"db.password","value":{"stringValue":"hunter2"}},{"key":"llm.usage.total_tokens","value":{"intValue":42}},
	   {"key":"tags","value":{"arrayValue":{"values":[{"stringValue":"a"},{"boolValue":true}]}}}],
	 "events":[{"name":"log","timeUnixNano":"1800000000000124000"},
	   {"name":"exception","timeUnixNano":"1800000000200123000","attributes":[{"key":"exception.type","value":{"stringValue":"QueryFailedError"}},
	   {"key":"exception.message","value":{"stringValue":"canceling statement due to statement timeout"}},
	   {"key":"exception.stacktrace","value":{"stringValue":"` + strings.Repeat(`at x\n`, 5000) + `"}}]}],
	 "status":{"code":2,"message":"boom"}},
	{"traceId":"abababababababababababababababab","spanId":"0909090909090909","parentSpanId":"0102030405060708","name":"INSERT orders","kind":3,
	 "startTimeUnixNano":1800000000001123000,"endTimeUnixNano":"1800000000200123000"}]}]}]}`)
	w := post(in, TracesPath, "application/json", Mint(testKey, testClaims), body, false)
	if w.Code != http.StatusOK || w.Body.String() != "{}" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	checkSpans(t, sp.spans(t), start)
}

func TestIngestRefuses(t *testing.T) {
	in, sp := newIngest()
	body, _ := proto.Marshal(testRequest(time.Now()))
	good := Mint(testKey, testClaims)
	for name, c := range map[string]struct {
		token string
		want  int
	}{
		"no token":        {"", http.StatusUnauthorized},
		"another key":     {Mint([]byte("another key of 32 bytes or so!!!"), testClaims), http.StatusUnauthorized},
		"changed release": {strings.Replace(good, ".5.", ".6.", 1), http.StatusUnauthorized},
	} {
		if w := post(in, TracesPath, "application/x-protobuf", c.token, body, false); w.Code != c.want {
			t.Errorf("%s: %d, want %d", name, w.Code, c.want)
		}
	}
	in.Key.Set(nil)
	if w := post(in, TracesPath, "application/x-protobuf", good, body, false); w.Code != http.StatusNotFound {
		t.Errorf("tracing off: %d", w.Code)
	}
	in.Key.Set(testKey)
	if w := post(in, TracesPath, "application/x-protobuf", good, []byte("\xff\xff"), false); w.Code != http.StatusBadRequest {
		t.Errorf("garbage: %d", w.Code)
	}
	in.Limits.MaxBody = 64
	if w := post(in, TracesPath, "application/x-protobuf", good, body, false); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("too big: %d", w.Code)
	}
	if len(sp.lines) != 0 {
		t.Errorf("spooled %d spans", len(sp.lines))
	}
	// Metrics and logs are answered and dropped.
	if w := post(in, LogsPath, "application/x-protobuf", good, nil, false); w.Code != http.StatusOK {
		t.Errorf("logs: %d", w.Code)
	}
}

// Past a service's rate its spans are rejected with OTLP's partial
// success, which SDKs don't send again.
func TestIngestServiceRate(t *testing.T) {
	in, sp := newIngest()
	in.Limits.ServiceRate = 1 // a burst of 2
	at := time.Unix(1_800_000_000, 0)
	in.Now = func() time.Time { return at }
	req := testRequest(at)
	req.ResourceSpans[0].ScopeSpans[0].Spans = append(req.ResourceSpans[0].ScopeSpans[0].Spans, req.ResourceSpans[0].ScopeSpans[0].Spans[1])
	body, _ := proto.Marshal(req)
	w := post(in, TracesPath, "application/x-protobuf", Mint(testKey, testClaims), body, false)
	var res collector.ExportTraceServiceResponse
	if err := proto.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != http.StatusOK {
		t.Fatalf("%d: %v", w.Code, err)
	}
	if res.GetPartialSuccess().GetRejectedSpans() != 1 || len(sp.lines) != 2 || !strings.Contains(res.GetPartialSuccess().GetErrorMessage(), "rate") {
		t.Errorf("partial success = %v, spooled %d", res.GetPartialSuccess(), len(sp.lines))
	}
}

func TestSensitive(t *testing.T) {
	for k, want := range map[string]bool{
		"db.password": true, "x-api-key": true, "auth_token": true, "http.request.header.cookie": true,
		"http.request.header.content-type": false, "llm.usage.total_tokens": false, "session.id": false, "user.id": false,
		"private_key": true, "aws.secret": true,
	} {
		if sensitive(k) != want {
			t.Errorf("sensitive(%q) = %v", k, !want)
		}
	}
	if got := maskQuery("token=abc&page=2"); got != "token=*****&page=2" {
		t.Errorf("bare query: %q", got)
	}
	if got := maskQuery("/orders/7"); got != "/orders/7" {
		t.Errorf("a path: %q", got)
	}
}

func TestToken(t *testing.T) {
	key := KeyFrom("the install's analytics token")
	tok := Mint(key, testClaims)
	if c, err := Parse(key, tok); err != nil || c != testClaims {
		t.Fatalf("parse = %+v, %v", c, err)
	}
	for _, bad := range []string{tok[:len(tok)-1] + "0", "dtr1.1.2.3.4.5", strings.TrimPrefix(tok, "dtr1."), tok + "x"} {
		if _, err := Parse(key, bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}
