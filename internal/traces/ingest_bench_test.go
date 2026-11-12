package traces

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	metricscollector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/NordTempo/tempoDock/internal/analytics"
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

// BenchmarkSampleRescue is a rescue from a full ring of 4 MB: one span's
// decision and a scan of every chunk.
func BenchmarkSampleRescue(b *testing.B) {
	var sm sampler
	l := sampleLimits{budget: 1, rescues: 1 << 30, ringBytes: DefaultRingBytes, slow: time.Second}
	now := time.Unix(1_800_000_000, 0)
	line := make([]byte, 600)
	line[0], line[len(line)-1] = '{', '}'
	drop := func([]byte) error { return nil }
	sp := analytics.Span{Service: 1, Trace: trace(1), ID: 1, Entry: true}
	_ = sm.span(&sp, line, signals{}, l, now, drop) // the budget's one trace
	for i := range 8000 {
		sp := analytics.Span{Service: 1, Trace: trace(uint64(i%400) + 2), ID: 2}
		_ = sm.span(&sp, line, signals{}, l, now, drop)
	}
	b.ReportAllocs()
	i := uint64(0)
	for b.Loop() {
		i++
		sp := analytics.Span{Service: 1, Trace: trace(1<<40 + i), ID: 1, Entry: true, HTTPStatus: 500}
		_ = sm.span(&sp, line, signals{}, l, now, drop)
	}
}

// benchMetrics is an SDK's export of 256 points: gauges and sums with
// two attributes, and histograms of a request duration by route and
// status, each with the SDK's default 15 bounds.
func benchMetrics() []byte {
	ns := uint64(time.Unix(1_800_000_000, 0).UnixNano())
	var metrics []*metricspb.Metric
	var gauges, sums []*metricspb.NumberDataPoint
	var hists []*metricspb.HistogramDataPoint
	bounds := []float64{0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 2500, 5000, 7500, 10000}
	for i := range 64 {
		attrs := []*common.KeyValue{str("pool.name", "pool-"+strconv.Itoa(i%8)), str("state", "used")}
		gauges = append(gauges, &metricspb.NumberDataPoint{TimeUnixNano: ns, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: float64(i)}, Attributes: attrs})
		sums = append(sums, &metricspb.NumberDataPoint{TimeUnixNano: ns, Value: &metricspb.NumberDataPoint_AsInt{AsInt: int64(i)}, Attributes: attrs})
		for s := range 2 {
			hists = append(hists, &metricspb.HistogramDataPoint{
				TimeUnixNano: ns, Count: 100, Sum: proto.Float64(1234), ExplicitBounds: bounds,
				BucketCounts: []uint64{0, 10, 20, 30, 20, 10, 5, 3, 1, 1, 0, 0, 0, 0, 0, 0},
				Attributes:   []*common.KeyValue{str("http.route", "/orders/"+strconv.Itoa(i)), num("http.response.status_code", int64(200+300*s))},
			})
		}
	}
	metrics = append(metrics,
		&metricspb.Metric{Name: "db.client.connection.count", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: gauges}}},
		&metricspb.Metric{Name: "orders.created", Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{AggregationTemporality: 1, IsMonotonic: true, DataPoints: sums}}},
		&metricspb.Metric{Name: "http.server.duration", Unit: "ms", Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{AggregationTemporality: 1, DataPoints: hists}}},
	)
	b, _ := proto.Marshal(&metricscollector.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: metrics}}}}})
	return b
}

// BenchmarkIngestMetrics is one export of 256 points through the
// handler: token, decode, buckets, attributes and a JSON line each.
// Reported per point.
func BenchmarkIngestMetrics(b *testing.B) {
	in := &Ingest{Spool: discard{}, Metrics: discard{}, Key: &Key{}, Limits: Limits{MetricRate: 1e12}}
	in.Key.Set(testKey)
	body := benchMetrics()
	path := TokenPathPrefix + Mint(testKey, testClaims) + MetricsPath
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/x-protobuf")
		w := httptest.NewRecorder()
		in.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Body.Len() != 0 {
			b.Fatal(w.Code, w.Body)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/256, "ns/point")
}
