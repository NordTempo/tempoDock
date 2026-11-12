package traces

import (
	"bytes"
	"encoding/binary"
	"encoding/json/jsontext"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// App metrics (ADR 0075, phase 6): an export's points become
// analytics.MetricPoint lines in the server's metrics spool. Gauges, sums
// and histograms are kept; an explicit or exponential histogram becomes
// Dock's MetricBuckets log2 buckets here, so the store keeps one shape.
// Summaries (deprecated in OpenTelemetry) are refused. Like spans, the
// ingest keeps no state per series: Dock asks SDKs for delta temporality
// and the store differences cumulative points when it reads them. The
// series caps are the store's, which sees every server's points.

// Metric caps on the way in: attributes name a series, so fewer and
// shorter than a span's.
const (
	maxMetricAttrs = 16
	maxMetricValue = 128
	maxMetricUnit  = 32
	// maxMetricPoints caps one metric's points in an export (the store's
	// series cap); past it they are rejected.
	maxMetricPoints = 2000
)

// rawMetric is a metric's description, which its points share.
type rawMetric struct {
	name, unit string
	// kind is 0 for a metric Dock doesn't keep (a summary).
	kind       analytics.MetricKind
	monotonic  bool
	cumulative bool
}

// rawPoint is one data point as decoded.
type rawPoint struct {
	attrs []attr
	time  uint64 // Unix nanoseconds
	// value is a gauge's or sum's, or a histogram's sum; noValue marks a
	// point the SDK flagged as having none.
	value   float64
	noValue bool
	// A histogram's count, min and max, and its counts in Dock's buckets.
	count          uint64
	min, max       float64
	hasMin, hasMax bool
	buckets        [analytics.MetricBuckets]uint64
	histogram      bool
}

// errBadMetricsProto and errBadMetricsJSON are bodies that aren't an OTLP
// metrics export.
var (
	errBadMetricsProto = errors.New("the body isn't an OTLP ExportMetricsServiceRequest")
	errBadMetricsJSON  = errors.New("the body isn't an OTLP/JSON ExportMetricsServiceRequest")
)

// OTLP's aggregation temporality: cumulative sums and histograms are
// totals since the start.
const temporalityCumulative = 2

// decodeMetricsProto reads an ExportMetricsServiceRequest, calling add
// for every data point. Resources' attributes are skipped: the token
// names the service.
func decodeMetricsProto(b []byte, add func(m *rawMetric, p *rawPoint) error) error {
	err := fields(b, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
		if num != 1 || typ != protowire.BytesType { // resource_metrics
			return nil
		}
		return fields(v, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
			if num != 2 || typ != protowire.BytesType { // scope_metrics
				return nil
			}
			return fields(v, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
				if num != 2 || typ != protowire.BytesType { // metrics
					return nil
				}
				return decodeMetric(v, add)
			})
		})
	})
	if errors.Is(err, errBadProto) {
		return errBadMetricsProto
	}
	return err
}

// decodeMetric reads one Metric: its name and unit first, then its data,
// whatever order the fields come in.
func decodeMetric(b []byte, add func(m *rawMetric, p *rawPoint) error) error {
	var m rawMetric
	var data []byte
	var dataNum protowire.Number
	err := fields(b, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
		if typ != protowire.BytesType {
			return nil
		}
		switch num {
		case 1:
			m.name = string(v)
		case 3:
			m.unit = string(v)
		case 5, 7, 9, 10, 11: // gauge, sum, histogram, exponential_histogram, summary
			data, dataNum = v, num
		}
		return nil
	})
	if err != nil || data == nil {
		return err
	}
	switch dataNum {
	case 5:
		m.kind = analytics.MetricGauge
	case 7:
		m.kind = analytics.MetricSum
	case 9, 10:
		m.kind = analytics.MetricHistogram
	}
	// The data's own fields (temporality, monotonic) before its points.
	err = fields(data, func(num protowire.Number, typ protowire.Type, _ []byte, n uint64) error {
		switch {
		case num == 2 && typ == protowire.VarintType && dataNum != 5 && dataNum != 11:
			m.cumulative = n == temporalityCumulative
		case num == 3 && typ == protowire.VarintType && dataNum == 7:
			m.monotonic = n != 0
		}
		return nil
	})
	if err != nil {
		return err
	}
	scale := analytics.MetricBucketScale(m.unit)
	points := 0
	// One point, its attributes' slice reused: add copies what it keeps.
	var p rawPoint
	return fields(data, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
		if num != 1 || typ != protowire.BytesType {
			return nil
		}
		p = rawPoint{attrs: p.attrs[:0]}
		if points++; points > maxMetricPoints || m.kind == 0 {
			return add(&rawMetric{name: m.name}, &p) // counted as rejected
		}
		var err error
		switch dataNum {
		case 5, 7:
			err = decodeNumberPoint(v, &p)
		case 9:
			err = decodeHistogramPoint(v, &p, scale)
		case 10:
			err = decodeExpHistogramPoint(v, &p, scale)
		}
		if err != nil {
			return err
		}
		return add(&m, &p)
	})
}

// decodeNumberPoint reads a NumberDataPoint.
func decodeNumberPoint(b []byte, p *rawPoint) error {
	bud := budget(spanBudget)
	return fields(b, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error {
		switch {
		case num == 7 && typ == protowire.BytesType:
			return p.addAttr(v, &bud)
		case num == 3 && typ == protowire.Fixed64Type:
			p.time = n
		case num == 4 && typ == protowire.Fixed64Type:
			p.value = math.Float64frombits(n)
		case num == 6 && typ == protowire.Fixed64Type:
			p.value = float64(int64(n))
		case num == 8 && typ == protowire.VarintType:
			p.noValue = n&1 != 0
		}
		return nil
	})
}

// addAttr decodes one KeyValue into p's attributes, at most maxAttrs.
func (p *rawPoint) addAttr(v []byte, bud *budget) error {
	if len(p.attrs) == maxAttrs {
		return nil
	}
	a, err := decodeKeyValue(v, 0, bud)
	if err == nil {
		p.attrs = append(p.attrs, a)
	}
	return err
}

// decodeHistogramPoint reads a HistogramDataPoint, its explicit buckets
// counted into Dock's: each by its middle (the first by its bound, the
// last by the max when there is one).
func decodeHistogramPoint(b []byte, p *rawPoint, scale float64) error {
	bud := budget(spanBudget)
	p.histogram = true
	var counts, bounds []byte // packed fixed64 and doubles
	err := fields(b, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error {
		switch {
		case num == 9 && typ == protowire.BytesType:
			return p.addAttr(v, &bud)
		case num == 3 && typ == protowire.Fixed64Type:
			p.time = n
		case num == 4 && typ == protowire.Fixed64Type:
			p.count = n
		case num == 5 && typ == protowire.Fixed64Type:
			p.value = math.Float64frombits(n)
		case num == 6 && typ == protowire.BytesType:
			counts = v
		case num == 7 && typ == protowire.BytesType:
			bounds = v
		case num == 10 && typ == protowire.VarintType:
			p.noValue = n&1 != 0
		case num == 11 && typ == protowire.Fixed64Type:
			p.min, p.hasMin = math.Float64frombits(n), true
		case num == 12 && typ == protowire.Fixed64Type:
			p.max, p.hasMax = math.Float64frombits(n), true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(counts)%8 != 0 || len(bounds)%8 != 0 {
		return errBadMetricsProto
	}
	nb := len(bounds) / 8
	bound := func(i int) float64 { return math.Float64frombits(binary.LittleEndian.Uint64(bounds[8*i:])) }
	for i := range len(counts) / 8 {
		p.countAt(scale, explicitMiddle(i, nb, bound, p), binary.LittleEndian.Uint64(counts[8*i:]))
	}
	return nil
}

// explicitMiddle is the value explicit bucket i of a histogram with nb
// bounds stands for.
func explicitMiddle(i, nb int, bound func(int) float64, p *rawPoint) float64 {
	switch {
	case nb == 0:
		if p.count > 0 {
			return p.value / float64(p.count)
		}
		return 0
	case i == 0:
		return bound(0)
	case i >= nb:
		if p.hasMax && p.max > bound(nb-1) {
			return p.max
		}
		return bound(nb - 1)
	}
	return (bound(i-1) + bound(i)) / 2
}

// countAt adds n to the bucket of v.
func (p *rawPoint) countAt(scale, v float64, n uint64) {
	if n > 0 {
		p.buckets[analytics.MetricBucket(scale, v)] += n
	}
}

// decodeExpHistogramPoint reads an ExponentialHistogramDataPoint, its
// buckets counted into Dock's by their middles: bucket k of scale s holds
// (2^(k·2^-s), 2^((k+1)·2^-s)]. Negative values and zeros go in the
// first bucket.
func decodeExpHistogramPoint(b []byte, p *rawPoint, scale float64) error {
	bud := budget(spanBudget)
	p.histogram = true
	var positive, negative []byte
	var expScale int64
	var zeros uint64
	err := fields(b, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error {
		switch {
		case num == 1 && typ == protowire.BytesType:
			return p.addAttr(v, &bud)
		case num == 3 && typ == protowire.Fixed64Type:
			p.time = n
		case num == 4 && typ == protowire.Fixed64Type:
			p.count = n
		case num == 5 && typ == protowire.Fixed64Type:
			p.value = math.Float64frombits(n)
		case num == 6 && typ == protowire.VarintType:
			expScale = protowire.DecodeZigZag(n)
		case num == 7 && typ == protowire.Fixed64Type:
			zeros = n
		case num == 8 && typ == protowire.BytesType:
			positive = v
		case num == 9 && typ == protowire.BytesType:
			negative = v
		case num == 10 && typ == protowire.VarintType:
			p.noValue = n&1 != 0
		case num == 12 && typ == protowire.Fixed64Type:
			p.min, p.hasMin = math.Float64frombits(n), true
		case num == 13 && typ == protowire.Fixed64Type:
			p.max, p.hasMax = math.Float64frombits(n), true
		}
		return nil
	})
	if err != nil {
		return err
	}
	p.countAt(scale, 0, zeros)
	if err := expBuckets(negative, func(_ int64, n uint64) { p.countAt(scale, 0, n) }); err != nil {
		return err
	}
	width := math.Ldexp(1, -int(min(max(expScale, -10), 20)))
	return expBuckets(positive, func(k int64, n uint64) {
		p.countAt(scale, math.Exp2((float64(k)+0.5)*width), n)
	})
}

// expBuckets walks an exponential histogram's Buckets: f gets each
// bucket's index and count.
func expBuckets(b []byte, f func(k int64, n uint64)) error {
	var offset int64
	var counts []byte
	var unpacked []uint64
	err := fields(b, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error {
		switch {
		case num == 1 && typ == protowire.VarintType:
			offset = protowire.DecodeZigZag(n)
		case num == 2 && typ == protowire.BytesType:
			counts = v
		case num == 2 && typ == protowire.VarintType:
			unpacked = append(unpacked, n)
		}
		return nil
	})
	if err != nil {
		return err
	}
	k := offset
	for len(counts) > 0 {
		n, w := protowire.ConsumeVarint(counts)
		if w < 0 {
			return errBadMetricsProto
		}
		f(k, n)
		counts, k = counts[w:], k+1
	}
	for _, n := range unpacked {
		f(k, n)
		k++
	}
	return nil
}

// toPoint turns a decoded point into the line the store keeps, for the
// service c names; ok is false for one that can't be kept: no name, no
// time, or a value that isn't a number.
func toPoint(m *rawMetric, p *rawPoint, c Claims, attrs []byte) (analytics.MetricPoint, bool) {
	if m.kind == 0 || m.name == "" || p.time == 0 || math.IsNaN(p.value) || math.IsInf(p.value, 0) {
		return analytics.MetricPoint{}, false
	}
	out := analytics.MetricPoint{
		Org: c.Org, Project: c.Project, Env: c.Env, Service: c.Service, Release: c.Release,
		Name: cut(m.name, maxName), Unit: cut(m.unit, maxMetricUnit), Kind: m.kind,
		Monotonic: m.monotonic, Cumulative: m.cumulative,
		Attrs: metricAttrs(attrs, p.attrs), Time: int64(p.time / 1e9), Value: p.value,
	}
	if p.histogram {
		out.Count, out.Buckets = p.count, p.buckets[:]
		if p.hasMin && !math.IsNaN(p.min) && !math.IsInf(p.min, 0) {
			out.Min = &p.min
		}
		if p.hasMax && !math.IsNaN(p.max) && !math.IsInf(p.max, 0) {
			out.Max = &p.max
		}
	}
	return out, true
}

// metricAttrs appends a point's attributes to b as a JSON object of
// strings, keys sorted and each once, at most maxMetricAttrs of them,
// values masked as span attributes are and cut to maxMetricValue: the
// same attributes always make the same bytes, which name the series. Nil
// when there are none.
func metricAttrs(b []byte, attrs []attr) jsontext.Value {
	slices.SortStableFunc(attrs, func(x, y attr) int { return strings.Compare(x.key, y.key) })
	b = append(b, '{')
	kept := 0
	for i, a := range attrs {
		if kept == maxMetricAttrs {
			break
		}
		if a.key == "" || i > 0 && attrs[i-1].key == a.key {
			continue
		}
		if kept > 0 {
			b = append(b, ',')
		}
		b, _ = jsontext.AppendQuote(b, cut(a.key, maxMetricValue))
		b = append(b, ':')
		b, _ = jsontext.AppendQuote(b, cut(metricValue(a), maxMetricValue))
		kept++
	}
	if kept == 0 {
		return nil
	}
	return append(b, '}')
}

// metricValue is an attribute's value as a series names it: a string,
// masked when the name is sensitive.
func metricValue(a attr) string {
	v := a.val
	switch {
	case sensitive(a.key):
		return mask
	case v.kind == valueString && urlValues[a.key]:
		return maskQuery(v.s)
	case v.kind == valueString:
		return v.s
	case v.kind == valueBool:
		return strconv.FormatBool(v.b)
	case v.kind == valueInt:
		return strconv.FormatInt(v.i, 10)
	case v.kind == valueDouble:
		return strconv.FormatFloat(v.f, 'g', -1, 64)
	}
	return string(appendValue(nil, v, maxMetricValue, 0))
}

// appendMetricLine appends p as its JSON line (analytics.MetricPoint),
// written by hand: an export's points are the ingest's hot path.
func appendMetricLine(b []byte, p *analytics.MetricPoint) []byte {
	num := func(key string, v int64) {
		b = append(b, key...)
		b = strconv.AppendInt(b, v, 10)
	}
	b = append(b, '{')
	num(`"org":`, p.Org)
	num(`,"project":`, p.Project)
	num(`,"env":`, p.Env)
	num(`,"service":`, p.Service)
	num(`,"release":`, p.Release)
	b = append(b, `,"name":`...)
	b, _ = jsontext.AppendQuote(b, p.Name)
	if p.Unit != "" {
		b = append(b, `,"unit":`...)
		b, _ = jsontext.AppendQuote(b, p.Unit)
	}
	num(`,"kind":`, int64(p.Kind))
	if p.Monotonic {
		b = append(b, `,"mono":true`...)
	}
	if p.Cumulative {
		b = append(b, `,"cum":true`...)
	}
	if len(p.Attrs) > 0 {
		b = append(append(b, `,"attrs":`...), p.Attrs...)
	}
	num(`,"t":`, p.Time)
	float := func(key string, v float64) {
		b = append(b, key...)
		b = strconv.AppendFloat(b, v, 'g', -1, 64)
	}
	if p.Value != 0 {
		float(`,"v":`, p.Value)
	}
	if p.Count != 0 {
		b = append(b, `,"n":`...)
		b = strconv.AppendUint(b, p.Count, 10)
	}
	if p.Min != nil {
		float(`,"min":`, *p.Min)
	}
	if p.Max != nil {
		float(`,"max":`, *p.Max)
	}
	if len(p.Buckets) > 0 {
		b = append(b, `,"b":[`...)
		for i, n := range p.Buckets {
			if i > 0 {
				b = append(b, ',')
			}
			b = strconv.AppendUint(b, n, 10)
		}
		b = append(b, ']')
	}
	return append(b, '}')
}

// takeMetrics is the metrics signal's export: every point decoded,
// converted and spooled, under the service's points rate.
func (in *Ingest) takeMetrics(body []byte, asJSON bool, c Claims, line *bytes.Buffer) (accepted, rejected int, err error) {
	now := in.Now()
	var attrs []byte
	add := func(m *rawMetric, p *rawPoint) error {
		if p.noValue {
			return nil // a point the SDK says has no value: nothing to keep
		}
		pt, ok := toPoint(m, p, c, attrs[:0])
		if !ok || !in.metricRates.take(c.Service, in.Limits.MetricRate, metricBurst, now) {
			rejected++
			return nil
		}
		attrs = pt.Attrs[:0] // reused for the next point's
		line.Reset()
		line.Write(appendMetricLine(line.AvailableBuffer(), &pt))
		if err := in.Metrics.Append(line.Bytes()); err != nil {
			rejected++
			if in.Log != nil {
				in.Log.Warn("traces: spool a metric point", "service", c.Service, "err", err)
			}
			return nil
		}
		accepted++
		return nil
	}
	if asJSON {
		var skipped int
		skipped, err = decodeMetricsJSON(body, add)
		rejected += skipped
	} else {
		err = decodeMetricsProto(body, add)
	}
	return accepted, rejected, err
}
