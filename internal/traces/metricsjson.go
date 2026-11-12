package traces

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"math"
	"strconv"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// OTLP/JSON metrics, decoded a metric at a time within the protobuf
// caps: at most maxMetricPoints points a metric (the rest rejected) and
// maxJSONBuckets bucket counts or bounds a point.

type jsonMetric struct {
	Name                 string          `json:"name"`
	Unit                 string          `json:"unit"`
	Gauge                *jsonMetricData `json:"gauge"`
	Sum                  *jsonMetricData `json:"sum"`
	Histogram            *jsonMetricData `json:"histogram"`
	ExponentialHistogram *jsonMetricData `json:"exponentialHistogram"`
	Summary              *jsonMetricData `json:"summary"`
}

type jsonMetricData struct {
	DataPoints             jsonPoints     `json:"dataPoints"`
	AggregationTemporality jsontext.Value `json:"aggregationTemporality"`
	IsMonotonic            bool           `json:"isMonotonic"`
}

type jsonPoint struct {
	Attributes     jsonKVs         `json:"attributes"`
	TimeUnixNano   jsontext.Value  `json:"timeUnixNano"`
	AsDouble       jsontext.Value  `json:"asDouble"`
	AsInt          jsontext.Value  `json:"asInt"`
	Flags          jsontext.Value  `json:"flags"`
	Count          jsontext.Value  `json:"count"`
	Sum            jsontext.Value  `json:"sum"`
	Min            jsontext.Value  `json:"min"`
	Max            jsontext.Value  `json:"max"`
	BucketCounts   jsonNums        `json:"bucketCounts"`
	ExplicitBounds jsonNums        `json:"explicitBounds"`
	Scale          jsontext.Value  `json:"scale"`
	ZeroCount      jsontext.Value  `json:"zeroCount"`
	Positive       *jsonExpBuckets `json:"positive"`
	Negative       *jsonExpBuckets `json:"negative"`
}

type jsonExpBuckets struct {
	Offset       jsontext.Value `json:"offset"`
	BucketCounts jsonNums       `json:"bucketCounts"`
}

// jsonPoints are a metric's points, still JSON, at most maxMetricPoints;
// jsonNums a point's bucket counts or bounds, at most maxJSONBuckets.
type (
	jsonPoints []jsontext.Value
	jsonNums   []jsontext.Value
)

// maxJSONBuckets caps a JSON histogram point's bucket counts and bounds;
// SDKs send at most 160 exponential buckets.
const maxJSONBuckets = 256

// temporalities are OTLP's AggregationTemporality names.
var temporalities = map[string]int{"AGGREGATION_TEMPORALITY_UNSPECIFIED": 0, "AGGREGATION_TEMPORALITY_DELTA": 1, "AGGREGATION_TEMPORALITY_CUMULATIVE": 2}

// decodeMetricsJSON reads an OTLP/JSON ExportMetricsServiceRequest,
// calling add for every point. skipped counts points past a metric's cap.
func decodeMetricsJSON(b []byte, add func(m *rawMetric, p *rawPoint) error) (skipped int, err error) {
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
		json.UnmarshalFromFunc(func(dec *jsontext.Decoder, v *jsonPoints) error {
			return eachItem(dec, func() error {
				if len(*v) == maxMetricPoints {
					skipped++
					return dec.SkipValue()
				}
				x, err := dec.ReadValue()
				*v = append(*v, bytes.Clone(x))
				return err
			})
		}),
		json.UnmarshalFromFunc(func(dec *jsontext.Decoder, v *jsonNums) error {
			return capped(dec, opts, (*[]jsontext.Value)(v), maxJSONBuckets, nil, func(l []jsontext.Value, x jsontext.Value) []jsontext.Value { return append(l, x) })
		}),
	))
	dec := jsontext.NewDecoder(bytes.NewReader(b))
	err = eachField(dec, "resourceMetrics", func() error {
		return eachItem(dec, func() error {
			return eachField(dec, "scopeMetrics", func() error {
				return eachItem(dec, func() error {
					return eachField(dec, "metrics", func() error {
						return eachItem(dec, func() error {
							bud = spanBudget
							var jm jsonMetric
							if err := json.UnmarshalDecode(dec, &jm, opts); err != nil {
								return err
							}
							addErr = jsonMetricPoints(&jm, opts, add)
							return addErr
						})
					})
				})
			})
		})
	})
	switch {
	case addErr != nil:
		return skipped, addErr
	case err != nil:
		return skipped, errBadMetricsJSON
	}
	return skipped, nil
}

// jsonMetricPoints calls add for each of a metric's points.
func jsonMetricPoints(jm *jsonMetric, opts json.Options, add func(m *rawMetric, p *rawPoint) error) error {
	m := rawMetric{name: jm.Name, unit: jm.Unit}
	var data *jsonMetricData
	switch {
	case jm.Gauge != nil:
		m.kind, data = analytics.MetricGauge, jm.Gauge
	case jm.Sum != nil:
		m.kind, data, m.monotonic = analytics.MetricSum, jm.Sum, jm.Sum.IsMonotonic
	case jm.Histogram != nil:
		m.kind, data = analytics.MetricHistogram, jm.Histogram
	case jm.ExponentialHistogram != nil:
		m.kind, data = analytics.MetricHistogram, jm.ExponentialHistogram
	case jm.Summary != nil:
		data = jm.Summary // kind 0: each point rejected
	default:
		return nil
	}
	m.cumulative = m.kind != analytics.MetricGauge && jsonEnum(data.AggregationTemporality, temporalities) == temporalityCumulative
	scale := analytics.MetricBucketScale(m.unit)
	for _, raw := range data.DataPoints {
		var jp jsonPoint
		if json.Unmarshal(raw, &jp, opts) != nil {
			if err := add(&rawMetric{name: m.name}, &rawPoint{}); err != nil { // rejected
				return err
			}
			continue
		}
		p := rawPoint{attrs: jsonAttrs(jp.Attributes, 0), time: jsonUint(jp.TimeUnixNano), noValue: jsonUint(jp.Flags)&1 != 0}
		switch m.kind {
		case analytics.MetricGauge, analytics.MetricSum:
			if len(jp.AsInt) > 0 {
				n, _ := strconv.ParseInt(unquote(jp.AsInt), 10, 64)
				p.value = float64(n)
			} else {
				p.value = jsonFloat(jp.AsDouble)
			}
		case analytics.MetricHistogram:
			p.histogram, p.count, p.value = true, jsonUint(jp.Count), jsonFloat(jp.Sum)
			if len(jp.Min) > 0 {
				p.min, p.hasMin = jsonFloat(jp.Min), true
			}
			if len(jp.Max) > 0 {
				p.max, p.hasMax = jsonFloat(jp.Max), true
			}
			if jm.Histogram != nil {
				bound := func(i int) float64 { return jsonFloat(jp.ExplicitBounds[i]) }
				for i, c := range jp.BucketCounts {
					p.countAt(scale, explicitMiddle(i, len(jp.ExplicitBounds), bound, &p), jsonUint(c))
				}
			} else {
				p.countAt(scale, 0, jsonUint(jp.ZeroCount))
				width := math.Ldexp(1, -int(min(max(jsonInt(jp.Scale), -10), 20)))
				if b := jp.Negative; b != nil {
					for _, c := range b.BucketCounts {
						p.countAt(scale, 0, jsonUint(c))
					}
				}
				if b := jp.Positive; b != nil {
					k := jsonInt(b.Offset)
					for _, c := range b.BucketCounts {
						p.countAt(scale, math.Exp2((float64(k)+0.5)*width), jsonUint(c))
						k++
					}
				}
			}
		}
		if err := add(&m, &p); err != nil {
			return err
		}
	}
	return nil
}

// jsonFloat reads a double given as a number or a string ("NaN" too).
func jsonFloat(v jsontext.Value) float64 {
	f, _ := strconv.ParseFloat(unquote(v), 64)
	return f
}

// jsonInt reads a signed integer given as a number or a string.
func jsonInt(v jsontext.Value) int64 {
	n, _ := strconv.ParseInt(unquote(v), 10, 64)
	return n
}
