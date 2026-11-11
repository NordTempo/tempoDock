package traces

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// discard is a spool that keeps nothing.
type discard struct{}

func (discard) Append([]byte) error { return nil }

// benchBatch is an SDK's batch of 512 spans: 64 requests of a server span
// and 7 database and cache calls, each with the attributes their
// instrumentation sets.
func benchBatch() []byte {
	var spans []*tracepb.Span
	ns := uint64(time.Unix(1_800_000_000, 0).UnixNano())
	for r := range 64 {
		trace := bytes.Repeat([]byte{byte(r + 1)}, 16)
		server := &tracepb.Span{
			TraceId: trace, SpanId: []byte{byte(r), 1, 2, 3, 4, 5, 6, 7}, Name: "GET /orders/{id}", Kind: tracepb.Span_SPAN_KIND_SERVER,
			StartTimeUnixNano: ns, EndTimeUnixNano: ns + 20_000_000,
			Attributes: []*common.KeyValue{
				str("http.request.method", "GET"), str("http.route", "/orders/{id}"), num("http.response.status_code", 200),
				str("url.path", "/orders/"+strconv.Itoa(r)), str("url.scheme", "http"), str("server.address", "api"),
				num("server.port", 8080), str("user_agent.original", "Mozilla/5.0 (X11; Linux x86_64)"), str("network.protocol.version", "1.1"),
			},
		}
		spans = append(spans, server)
		for c := range 7 {
			spans = append(spans, &tracepb.Span{
				TraceId: trace, SpanId: []byte{byte(r), byte(c), 9, 9, 9, 9, 9, 9}, ParentSpanId: server.SpanId,
				Name: "SELECT orders", Kind: tracepb.Span_SPAN_KIND_CLIENT, StartTimeUnixNano: ns + 1_000_000, EndTimeUnixNano: ns + 3_000_000,
				Attributes: []*common.KeyValue{
					str("db.system", "postgresql"), str("db.query.text", "SELECT * FROM orders WHERE id = $1 AND org_id = $2"),
					str("server.address", "postgres"), num("server.port", 5432),
				},
			})
		}
	}
	b, _ := proto.Marshal(&collector.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}}}}})
	return b
}

// BenchmarkIngestProto512 is one export of 512 spans through the handler:
// token, decode, conversion with caps and masking, a JSON line each.
// Reported per span.
func BenchmarkIngestProto512(b *testing.B) {
	in := &Ingest{Spool: discard{}, Key: &Key{}, Limits: Limits{ServiceRate: 1e12}}
	in.Key.Set(testKey)
	body := benchBatch()
	tok := "Bearer " + Mint(testKey, testClaims)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		r := httptest.NewRequest(http.MethodPost, TracesPath, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/x-protobuf")
		r.Header.Set("Authorization", tok)
		w := httptest.NewRecorder()
		in.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			b.Fatal(w.Code, w.Body)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/512, "ns/span")
}
