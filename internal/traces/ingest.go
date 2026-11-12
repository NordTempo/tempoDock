package traces

import (
	"bytes"
	"compress/gzip"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// OTLP/HTTP paths. Traces and metrics are kept; logs are answered and
// dropped (Dock keeps a container's stdout).
const (
	// TokenPathPrefix starts the path an endpoint Dock sets has: the
	// release's token, then the OTLP path.
	TokenPathPrefix = "/t/"
	TracesPath      = "/v1/traces"
	MetricsPath     = "/v1/metrics"
	LogsPath        = "/v1/logs"
)

// Defaults of the ingest's limits (Limits' zero values).
const (
	// DefaultMaxBody caps a request body as sent, and DefaultMaxDecoded
	// once a gzipped one is unpacked. SDKs send batches of 512 spans,
	// 150 to 400 KB.
	DefaultMaxBody    = 2 << 20
	DefaultMaxDecoded = 4 << 20
	// DefaultDecoders is how many bodies are decoded at once; past it a
	// request gets 503 and retries later.
	DefaultDecoders = 2
	// DefaultServiceRate is the spans a second a service may send, with
	// twice as many in a burst; past it spans are rejected.
	DefaultServiceRate = 5000
	// DefaultMetricRate is the metric points a second a service may send,
	// past a burst of metricBurst: SDKs export every series at once, once
	// a minute, so a service at the store's 2,000 series sends a burst
	// a minute and 33 points a second.
	DefaultMetricRate = 100
	metricBurst       = 4000
)

// Limits bound the ingest; zero values are the defaults.
type Limits struct {
	MaxBody, MaxDecoded, Decoders int
	ServiceRate                   float64
	// MetricRate is the metric points a second a service may send.
	MetricRate float64
	// Budget, Slow, Rescues and RingBytes are the sampler's (sample.go).
	Budget, Rescues, RingBytes int
	Slow                       time.Duration
}

// Spool is where kept spans go, a JSON line each: the server's spans
// spool.
type Spool interface {
	Append(line []byte) error
}

// Ingest serves OTLP/HTTP. Spool and Key are required; Metrics is the
// metrics spool, without which metrics are answered and dropped.
type Ingest struct {
	Spool   Spool
	Metrics Spool
	Key     *Key
	Log     *slog.Logger
	// Limits bound it; set before it serves.
	Limits Limits
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	once     sync.Once
	decoders chan struct{}
	readers  readers
	bufs     sync.Pool
	rates    rates
	// metricRates are the services' points buckets, apart from spans'.
	metricRates rates
	sample      sampler
	sampling    sampleLimits
}

// init applies the limits' defaults.
func (in *Ingest) init() {
	in.once.Do(func() {
		l := &in.Limits
		l.MaxBody, l.MaxDecoded, l.Decoders = cmpOr(l.MaxBody, DefaultMaxBody), cmpOr(l.MaxDecoded, DefaultMaxDecoded), cmpOr(l.Decoders, DefaultDecoders)
		if l.ServiceRate <= 0 {
			l.ServiceRate = DefaultServiceRate
		}
		if l.MetricRate <= 0 {
			l.MetricRate = DefaultMetricRate
		}
		l.Budget, l.Rescues, l.RingBytes = cmpOr(l.Budget, DefaultBudget), cmpOr(l.Rescues, DefaultRescues), cmpOr(l.RingBytes, DefaultRingBytes)
		if l.Slow <= 0 {
			l.Slow = DefaultSlow
		}
		in.sampling = sampleLimits{budget: l.Budget, rescues: l.Rescues, ringBytes: l.RingBytes, slow: l.Slow}
		in.decoders = make(chan struct{}, l.Decoders)
		in.bufs.New = func() any { return new(bytes.Buffer) }
		in.rates.by, in.metricRates.by = map[int64]*bucket{}, map[int64]*bucket{}
		in.readers.by = map[int64]int{}
		if in.Now == nil {
			in.Now = time.Now
		}
	})
}

// ServeHTTP serves the OTLP/HTTP paths.
func (in *Ingest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	in.init()
	if r.Method != http.MethodPost {
		http.Error(w, "OTLP takes POST", http.StatusMethodNotAllowed)
		return
	}
	// The token rides in the path, /t/<token>/v1/traces, as Dock sets the
	// endpoint: an app whose exporter is configured in code never sends
	// it anywhere, as it would a header the SDK merges in (ADR 0075).
	// Apps started before still send it as a bearer header.
	path, tok := r.URL.Path, ""
	if rest, ok := strings.CutPrefix(path, TokenPathPrefix); ok {
		tok, path, _ = strings.Cut(rest, "/")
		path = "/" + path
	}
	if tok == "" {
		tok, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	switch {
	case path == TracesPath:
		in.serve(w, r, strings.TrimSpace(tok), spansSignal)
	case path == MetricsPath && in.Metrics != nil:
		in.serve(w, r, strings.TrimSpace(tok), metricsSignal)
	case path == MetricsPath, path == LogsPath:
		// Dropped: nothing in Dock keeps them. 200, so SDKs don't send
		// them again.
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, int64(in.Limits.MaxBody)))
		reply(w, isJSON(r), spansSignal, 0)
	default:
		http.NotFound(w, r)
	}
}

// signal is what the ingest takes from one OTLP path.
type signal struct {
	// take decodes an export's body for the service c names, spooling
	// what it keeps; line is a buffer to encode lines in.
	take func(in *Ingest, body []byte, asJSON bool, c Claims, line *bytes.Buffer) (accepted, rejected int, err error)
	// rejectedField is the partial success's count in OTLP/JSON, and
	// dropped what its message says was dropped and why.
	rejectedField, dropped string
}

var (
	spansSignal   = signal{take: (*Ingest).takeSpans, rejectedField: "rejectedSpans", dropped: "spans: past the service's rate, or without IDs or a start"}
	metricsSignal = signal{take: (*Ingest).takeMetrics, rejectedField: "rejectedDataPoints", dropped: "metric points: past the service's rate, summaries (send histograms), or without a name, a time or a numeric value"}
)

// serve takes one export of sig.
func (in *Ingest) serve(w http.ResponseWriter, r *http.Request, tok string, sig signal) {
	key := in.Key.Get()
	if key == nil {
		http.Error(w, "tracing is off on this Dock; an operator turns it on with `dock tracing on`", http.StatusNotFound)
		return
	}
	claims, err := Parse(key, tok)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	asJSON := isJSON(r)
	// The body is read before taking a decoder, within bodyTimeout and the
	// service's share of readers: an app that sends slowly holds only its
	// own share, never the decoders every app needs.
	if !in.readers.acquire(claims.Service) {
		busy(w)
		return
	}
	buf := in.bufs.Get().(*bytes.Buffer)
	buf.Reset()
	defer in.bufs.Put(buf)
	_ = http.NewResponseController(w).SetReadDeadline(in.Now().Add(bodyTimeout))
	err = in.readBody(buf, r)
	in.readers.release(claims.Service)
	if err != nil {
		var tooBig *tooBigError
		if errors.As(err, &tooBig) {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	select {
	case in.decoders <- struct{}{}:
		defer func() { <-in.decoders }()
	default:
		busy(w)
		return
	}

	line := in.bufs.Get().(*bytes.Buffer)
	defer in.bufs.Put(line)
	accepted, rejected, err := sig.take(in, buf.Bytes(), asJSON, claims, line)
	if err != nil && accepted == 0 {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	reply(w, asJSON, sig, rejected)
}

// takeSpans is the spans signal's export: every span decoded, converted,
// sampled and spooled, under the service's rate. accepted counts spans
// taken, whether sampling kept or dropped them.
func (in *Ingest) takeSpans(body []byte, asJSON bool, claims Claims, line *bytes.Buffer) (accepted, rejected int, err error) {
	now := in.Now()
	add := func(sp *rawSpan) error {
		s, ok := convert(sp, claims)
		if !ok || !in.rates.take(claims.Service, in.Limits.ServiceRate, 2*in.Limits.ServiceRate, now) {
			rejected++
			return nil
		}
		line.Reset()
		err := json.MarshalWrite(line, &s)
		if err == nil {
			// MarshalWrite ends the line with a newline; the spool adds its own.
			err = in.sample.span(&s, bytes.TrimSuffix(line.Bytes(), []byte{'\n'}), signalsOf(sp), in.sampling, now, in.Spool.Append)
		}
		if err != nil {
			rejected++
			if in.Log != nil {
				in.Log.Warn("traces: spool a span", "service", claims.Service, "err", err)
			}
			return nil
		}
		accepted++
		return nil
	}
	if asJSON {
		err = decodeJSON(body, add)
	} else {
		err = decodeProto(body, add)
	}
	return accepted, rejected, err
}

// busy answers a request the ingest has no room for: SDKs send it again.
func busy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	http.Error(w, "busy; send again in a few seconds", http.StatusServiceUnavailable)
}

// How long a body may take to arrive, and how many are read at once: in
// all and for one service. Bodies come over the server's own networks, a
// 2 MB batch in milliseconds.
const (
	bodyTimeout       = 5 * time.Second
	maxReaders        = 8
	maxServiceReaders = 2
)

// readers counts the bodies being read, in all and by service.
type readers struct {
	mu    sync.Mutex
	total int
	by    map[int64]int
}

// acquire takes a reader for service, false when there is none: maxReaders
// in all, maxServiceReaders for the service.
func (r *readers) acquire(service int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.total == maxReaders || r.by[service] == maxServiceReaders {
		return false
	}
	r.total++
	r.by[service]++
	return true
}

// release gives back what acquire took.
func (r *readers) release(service int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total--
	if r.by[service]--; r.by[service] == 0 {
		delete(r.by, service)
	}
}

// tooBigError is a body over a cap.
type tooBigError struct{ max int }

func (e *tooBigError) Error() string {
	return "the body is over " + strconv.Itoa(e.max>>20) + " MB; send smaller batches (OTEL_BSP_MAX_EXPORT_BATCH_SIZE)"
}

// readBody reads the request's body into buf, unpacking gzip, within the
// caps.
func (in *Ingest) readBody(buf *bytes.Buffer, r *http.Request) error {
	body := io.LimitReader(r.Body, int64(in.Limits.MaxBody)+1)
	limit := in.Limits.MaxBody
	switch strings.ToLower(r.Header.Get("Content-Encoding")) {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(body)
		if err != nil {
			return errors.New("the body isn't gzip")
		}
		defer func() { _ = zr.Close() }()
		body, limit = io.LimitReader(zr, int64(in.Limits.MaxDecoded)+1), in.Limits.MaxDecoded
	default:
		return errors.New("Content-Encoding must be gzip or none")
	}
	n, err := buf.ReadFrom(body)
	if err != nil {
		return err
	}
	if n > int64(limit) {
		return &tooBigError{limit}
	}
	return nil
}

// isJSON reports whether the request is OTLP/JSON rather than protobuf.
func isJSON(r *http.Request) bool {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return mt == "application/json"
}

// reply answers an export of sig: success, or partial success with what
// was rejected, in the request's encoding.
func reply(w http.ResponseWriter, asJSON bool, sig signal, rejected int) {
	msg := "Dock dropped " + strconv.Itoa(rejected) + " " + sig.dropped
	if asJSON {
		w.Header().Set("Content-Type", "application/json")
		if rejected == 0 {
			_, _ = w.Write([]byte("{}"))
			return
		}
		b, _ := jsontext.AppendQuote([]byte(`{"partialSuccess":{"`+sig.rejectedField+`":"`+strconv.Itoa(rejected)+`","errorMessage":`), msg)
		_, _ = w.Write(append(b, "}}"...))
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	if rejected == 0 {
		return
	}
	// Export{Trace,Metrics}ServiceResponse{partial_success: {rejected_spans
	// or rejected_data_points, error_message}}: the same fields.
	var ps []byte
	ps = protowire.AppendTag(ps, 1, protowire.VarintType)
	ps = protowire.AppendVarint(ps, uint64(rejected))
	ps = protowire.AppendTag(ps, 2, protowire.BytesType)
	ps = protowire.AppendString(ps, msg)
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendBytes(b, ps)
	_, _ = w.Write(b)
}

// rates are each service's token buckets.
type rates struct {
	mu sync.Mutex
	// by holds a bucket per service that sent spans: bounded by the
	// services with a valid token.
	by map[int64]*bucket
}

// bucket is a token bucket.
type bucket struct {
	tokens float64
	last   time.Time
}

// take reports whether service may send one more span or point at now,
// at rate a second past a burst.
func (r *rates) take(service int64, rate, burst float64, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.by[service]
	if b == nil {
		b = &bucket{tokens: burst, last: now}
		r.by[service] = b
	}
	b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// cmpOr is v, or def when v is 0.
func cmpOr(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
