package bench

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// MetricWriter is where seeded metric points go.
type MetricWriter interface {
	WriteMetrics(ctx context.Context, source string, position uint64, points []analytics.MetricPoint, lim analytics.MetricLimits) (analytics.MetricWriteResult, error)
}

// SeedMetricsOptions shape the synthetic app metrics.
type SeedMetricsOptions struct {
	// Series is how many series BusyService sends, a point each a minute
	// for Minutes minutes ending at End.
	Series  int
	Minutes int
	End     time.Time
	Seed    uint64
	// Batch is the points per write; 0 is 10,000.
	Batch int
	// Source names the writes' spool; "" is "seed-metrics".
	Source string
	// Progress, when set, is called after every day of minutes.
	Progress func(minute, minutes, written int)
}

// metricNames are the seed's metrics: a stock Node SDK's runtime
// metrics, a database pool, and the app's own, a third of them
// histograms.
var metricNames = []struct {
	name, unit string
	kind       analytics.MetricKind
}{
	{"v8js.memory.heap.used", "By", analytics.MetricGauge},
	{"v8js.memory.heap.limit", "By", analytics.MetricGauge},
	{"nodejs.eventloop.delay.p99", "s", analytics.MetricGauge},
	{"v8js.gc.duration", "s", analytics.MetricHistogram},
	{"db.client.connection.count", "{connection}", analytics.MetricGauge},
	{"db.client.operation.duration", "s", analytics.MetricHistogram},
	{"http.client.request.duration", "s", analytics.MetricHistogram},
	{"orders.created", "{order}", analytics.MetricSum},
	{"orders.value", "NOK", analytics.MetricHistogram},
	{"queue.depth", "{job}", analytics.MetricGauge},
	{"jobs.processed", "{job}", analytics.MetricSum},
	{"cache.hits", "{hit}", analytics.MetricSum},
}

// SeedMetrics writes opts.Series series of BusyService (org TraceOrg),
// spread over the seed's metrics by an attribute with as many values as
// each needs, a point a minute each: values wander, histograms spread
// over a few buckets.
func SeedMetrics(ctx context.Context, w MetricWriter, opts SeedMetricsOptions) (int, error) {
	r := rand.New(rand.NewPCG(opts.Seed, opts.Seed^0x3e7c))
	if opts.Batch <= 0 {
		opts.Batch = 10_000
	}
	if opts.Source == "" {
		opts.Source = "seed-metrics"
	}
	type series struct {
		name, unit string
		kind       analytics.MetricKind
		attrs      []byte
		level      float64
	}
	all := make([]series, opts.Series)
	for i := range all {
		m := metricNames[i%len(metricNames)]
		all[i] = series{m.name, m.unit, m.kind, fmt.Appendf(nil, `{"instance":"i%d","route":"/r%d"}`, i/len(metricNames)%8, i/len(metricNames)), 10 + r.Float64()*1000}
	}
	start := opts.End.Truncate(time.Minute).Add(-time.Duration(opts.Minutes) * time.Minute)
	batch := make([]analytics.MetricPoint, 0, opts.Batch)
	var pos uint64
	written := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		pos++
		res, err := w.WriteMetrics(ctx, opts.Source, pos, batch, analytics.MetricLimits{})
		if err != nil {
			return err
		}
		written += res.Written
		batch = batch[:0]
		return nil
	}
	for m := range opts.Minutes {
		at := start.Add(time.Duration(m) * time.Minute).Unix()
		for i := range all {
			s := &all[i]
			s.level = math.Max(1, s.level*(1+r.NormFloat64()*0.02))
			p := analytics.MetricPoint{
				Org: TraceOrg, Project: 1, Env: 1, Service: BusyService, Release: 1000 + int64(m/1440),
				Name: s.name, Unit: s.unit, Kind: s.kind, Monotonic: s.kind == analytics.MetricSum, Attrs: s.attrs, Time: at, Value: s.level,
			}
			if s.kind == analytics.MetricHistogram {
				p.Count = uint64(20 + r.IntN(200))
				p.Value = s.level * float64(p.Count)
				p.Buckets = make([]uint64, analytics.MetricBuckets)
				mid := analytics.MetricBucket(analytics.MetricBucketScale(s.unit), s.level/1000)
				for k := range p.Count {
					p.Buckets[min(analytics.MetricBuckets-1, max(0, mid+int(k%5)-2))]++
				}
			}
			batch = append(batch, p)
			if len(batch) == opts.Batch {
				if err := flush(); err != nil {
					return written, err
				}
			}
		}
		if opts.Progress != nil && (m+1)%1440 == 0 {
			opts.Progress(m+1, opts.Minutes, written)
		}
	}
	return written, flush()
}
