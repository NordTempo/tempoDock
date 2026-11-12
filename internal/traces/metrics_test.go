package traces

import (
	"encoding/json/v2"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	collector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// newMetricsIngest is an ingest with the test key and memory spools.
func newMetricsIngest() (*Ingest, *memSpool) {
	in, _ := newIngest()
	m := &memSpool{}
	in.Metrics = m
	return in, m
}

// points decodes the spooled lines.
func (m *memSpool) points(t *testing.T) []analytics.MetricPoint {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]analytics.MetricPoint, len(m.lines))
	for i, l := range m.lines {
		if err := json.Unmarshal(l, &out[i]); err != nil {
			t.Fatalf("line %d: %v: %s", i, err, l)
		}
	}
	return out
}

// testMetrics is an export of every kind of metric, a summary and a
// point that isn't a number, as an SDK sends them.
func testMetrics(at time.Time) *collector.ExportMetricsServiceRequest {
	ns := uint64(at.UnixNano())
	gauge := func(name string, v float64) *metricspb.Metric {
		return &metricspb.Metric{Name: name, Unit: "By", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{
			{TimeUnixNano: ns, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: v}, Attributes: []*common.KeyValue{
				str("v8js.heap.space.name", "old_space"), str("db.password", "hunter2"), str("a", "first"), str("a", "second"),
			}},
		}}}}
	}
	max := 400.0
	return &collector.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{
		gauge("v8js.memory.heap.used", 148e6),
		{Name: "orders.created", Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
			AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA, IsMonotonic: true,
			DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: ns, Value: &metricspb.NumberDataPoint_AsInt{AsInt: 38}, Attributes: []*common.KeyValue{num("shard", 2)}}},
		}}},
		{Name: "jobs.done", Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
			AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
			DataPoints:             []*metricspb.NumberDataPoint{{TimeUnixNano: ns, Value: &metricspb.NumberDataPoint_AsInt{AsInt: 1200}}},
		}}},
		{Name: "http.client.duration", Unit: "ms", Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
			AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
			DataPoints: []*metricspb.HistogramDataPoint{{
				TimeUnixNano: ns, Count: 7, Sum: proto.Float64(560), Max: &max,
				ExplicitBounds: []float64{5, 10, 25, 50, 100}, BucketCounts: []uint64{1, 2, 3, 0, 0, 1},
			}},
		}}},
		{Name: "queue.wait", Unit: "s", Data: &metricspb.Metric_ExponentialHistogram{ExponentialHistogram: &metricspb.ExponentialHistogram{
			AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
			DataPoints: []*metricspb.ExponentialHistogramDataPoint{{
				TimeUnixNano: ns, Count: 8, Sum: proto.Float64(0.01), Scale: 0, ZeroCount: 1,
				Positive: &metricspb.ExponentialHistogramDataPoint_Buckets{Offset: -10, BucketCounts: []uint64{1, 2}},
				Negative: &metricspb.ExponentialHistogramDataPoint_Buckets{BucketCounts: []uint64{4}},
			}},
		}}},
		{Name: "rpc.latency", Data: &metricspb.Metric_Summary{Summary: &metricspb.Summary{DataPoints: []*metricspb.SummaryDataPoint{{TimeUnixNano: ns}}}}},
		gauge("broken", math.NaN()),
	}}}}}}
}

// checkMetrics checks what testMetrics becomes.
func checkMetrics(t *testing.T, got []analytics.MetricPoint, at time.Time) {
	t.Helper()
	if len(got) != 5 {
		t.Fatalf("%d points, want 5: %+v", len(got), got)
	}
	byName := map[string]analytics.MetricPoint{}
	for _, p := range got {
		if p.Org != 1 || p.Project != 2 || p.Env != 3 || p.Service != 4 || p.Release != 5 || p.Time != at.Unix() {
			t.Errorf("%s: claims or time = %+v", p.Name, p)
		}
		byName[p.Name] = p
	}
	heap := byName["v8js.memory.heap.used"]
	if heap.Kind != analytics.MetricGauge || heap.Value != 148e6 || heap.Unit != "By" {
		t.Errorf("gauge = %+v", heap)
	}
	// Sorted, each key once (its first value), sensitive values masked.
	if want := `{"a":"first","db.password":"*****","v8js.heap.space.name":"old_space"}`; string(heap.Attrs) != want {
		t.Errorf("attrs = %s, want %s", heap.Attrs, want)
	}
	if s := byName["orders.created"]; s.Kind != analytics.MetricSum || !s.Monotonic || s.Cumulative || s.Value != 38 || string(s.Attrs) != `{"shard":"2"}` {
		t.Errorf("delta sum = %+v", s)
	}
	if s := byName["jobs.done"]; !s.Cumulative || s.Monotonic || s.Value != 1200 {
		t.Errorf("cumulative sum = %+v", s)
	}
	buckets := func(at map[int]uint64) []uint64 {
		b := make([]uint64, analytics.MetricBuckets)
		for i, n := range at {
			b[i] = n
		}
		return b
	}
	// Bounds 5, 10, 25, 50, 100 ms: middles 5, 7.5, 17.5 and the max, 400,
	// in buckets of 0.5 ms · 2^i.
	h := byName["http.client.duration"]
	if h.Kind != analytics.MetricHistogram || h.Count != 7 || h.Value != 560 || h.Max == nil || *h.Max != 400 || h.Min != nil ||
		!reflect.DeepEqual(h.Buckets, buckets(map[int]uint64{4: 3, 6: 3, 10: 1})) {
		t.Errorf("explicit histogram = %+v", h)
	}
	// Scale 0 buckets from 2^-10 s: middles 1.4 and 2.8 ms; zeros and
	// negatives first.
	if e := byName["queue.wait"]; e.Count != 8 || !reflect.DeepEqual(e.Buckets, buckets(map[int]uint64{0: 5, 2: 1, 3: 2})) {
		t.Errorf("exponential histogram = %+v", e)
	}
}

func TestIngestMetrics(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	tok := Mint(testKey, testClaims)
	req := testMetrics(at)
	body, _ := proto.Marshal(req)
	in, m := newMetricsIngest()
	w := post(in, TokenPathPrefix+tok+MetricsPath, "application/x-protobuf", "", body, true)
	if w.Code != http.StatusOK {
		t.Fatalf("protobuf: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "Dock dropped 2 metric points") {
		t.Errorf("protobuf answer %q doesn't say the summary and NaN were dropped", w.Body)
	}
	proto := m.points(t)
	checkMetrics(t, proto, at)

	jsonBody, err := protojson.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	in, m = newMetricsIngest()
	w = post(in, TokenPathPrefix+tok+MetricsPath, "application/json", "", jsonBody, false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"rejectedDataPoints":"2"`) {
		t.Fatalf("JSON: %d %s", w.Code, w.Body)
	}
	if got := m.points(t); !reflect.DeepEqual(got, proto) {
		t.Errorf("JSON points differ from protobuf's:\n%+v\n%+v", got, proto)
	}
}

func TestIngestMetricsWithoutSpoolDrops(t *testing.T) {
	in, sp := newIngest()
	body, _ := proto.Marshal(testMetrics(time.Now()))
	if w := post(in, MetricsPath, "application/x-protobuf", Mint(testKey, testClaims), body, false); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(sp.lines) != 0 {
		t.Error("metrics reached the spans spool")
	}
}

func TestIngestMetricsRate(t *testing.T) {
	in, m := newMetricsIngest()
	in.Now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	ns := uint64(in.Now().UnixNano())
	var metrics []*metricspb.Metric
	for i := range 3 {
		var pts []*metricspb.NumberDataPoint
		for j := range 1500 {
			pts = append(pts, &metricspb.NumberDataPoint{TimeUnixNano: ns, Value: &metricspb.NumberDataPoint_AsInt{AsInt: 1}, Attributes: []*common.KeyValue{num("j", int64(j))}})
		}
		metrics = append(metrics, &metricspb.Metric{Name: "m" + string(rune('a'+i)), Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: pts}}})
	}
	body, _ := proto.Marshal(&collector.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: metrics}}}}})
	w := post(in, MetricsPath, "application/x-protobuf", Mint(testKey, testClaims), body, false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Dock dropped 500 metric points") {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	if n := len(m.points(t)); n != metricBurst {
		t.Errorf("kept %d points, want the burst of %d", n, metricBurst)
	}
}

func TestMetricBucket(t *testing.T) {
	for _, c := range []struct {
		v    float64
		want int
	}{
		{-3, 0}, {0, 0}, {math.NaN(), 0}, {1, 0}, {1.5, 1}, {2, 1}, {2.01, 2}, {4, 2}, {5, 3},
		{1 << 18, 18}, {1<<18 + 1, 19}, {1e12, 19}, {math.Inf(1), 19},
	} {
		if got := analytics.MetricBucket(1, c.v); got != c.want {
			t.Errorf("MetricBucket(1, %v) = %d, want %d", c.v, got, c.want)
		}
	}
}
