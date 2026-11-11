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

// OTLP/HTTP paths. Traces are kept; metrics and logs are answered and
// dropped (Dock sets their exporters to none: logs come from stdout).
const (
	TracesPath  = "/v1/traces"
	MetricsPath = "/v1/metrics"
	LogsPath    = "/v1/logs"
)

// Defaults of the ingest's limits (Limits' zero values).
const (
	// DefaultMaxBody caps a request body as sent, and DefaultMaxDecoded
	// once a gzipped one is unpacked. SDKs send batches of 512 spans,
	// 150 to 400 KB.
	DefaultMaxBody    = 2 << 20
	DefaultMaxDecoded = 4 << 20
	// DefaultDecoders is how many bodies are read and decoded at once;
	// past it a request gets 503 and retries later.
	DefaultDecoders = 2
	// DefaultServiceRate is the spans a second a service may send, with
	// twice as many in a burst; past it spans are rejected.
	DefaultServiceRate = 5000
)

// Limits bound the ingest; zero values are the defaults.
type Limits struct {
	MaxBody, MaxDecoded, Decoders int
	ServiceRate                   float64
	// Budget, Slow, Rescues and RingBytes are the sampler's (sample.go).
	Budget, Rescues, RingBytes int
	Slow                       time.Duration
}

// Spool is where kept spans go, a JSON line each: the server's spans
// spool.
type Spool interface {
	Append(line []byte) error
}

// Ingest serves OTLP/HTTP. Spool and Key are required.
type Ingest struct {
	Spool Spool
	Key   *Key
	Log   *slog.Logger
	// Limits bound it; set before it serves.
	Limits Limits
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	once     sync.Once
	decoders chan struct{}
	bufs     sync.Pool
	rates    rates
	sample   sampler
	sampling sampleLimits
}

// init applies the limits' defaults.
func (in *Ingest) init() {
	in.once.Do(func() {
		l := &in.Limits
		l.MaxBody, l.MaxDecoded, l.Decoders = cmpOr(l.MaxBody, DefaultMaxBody), cmpOr(l.MaxDecoded, DefaultMaxDecoded), cmpOr(l.Decoders, DefaultDecoders)
		if l.ServiceRate <= 0 {
			l.ServiceRate = DefaultServiceRate
		}
		l.Budget, l.Rescues, l.RingBytes = cmpOr(l.Budget, DefaultBudget), cmpOr(l.Rescues, DefaultRescues), cmpOr(l.RingBytes, DefaultRingBytes)
		if l.Slow <= 0 {
			l.Slow = DefaultSlow
		}
		in.sampling = sampleLimits{budget: l.Budget, rescues: l.Rescues, ringBytes: l.RingBytes, slow: l.Slow}
		in.decoders = make(chan struct{}, l.Decoders)
		in.bufs.New = func() any { return new(bytes.Buffer) }
		in.rates.by = map[int64]*bucket{}
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
	switch r.URL.Path {
	case TracesPath:
		in.serveTraces(w, r)
	case MetricsPath, LogsPath:
		// Dropped: nothing in Dock keeps them (yet). 200, so SDKs don't
		// send them again.
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, int64(in.Limits.MaxBody)))
		reply(w, isJSON(r), 0, "")
	default:
		http.NotFound(w, r)
	}
}

// serveTraces takes one export of spans.
func (in *Ingest) serveTraces(w http.ResponseWriter, r *http.Request) {
	key := in.Key.Get()
	if key == nil {
		http.Error(w, "tracing is off on this Dock; an operator turns it on with `dock tracing on`", http.StatusNotFound)
		return
	}
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims, err := Parse(key, strings.TrimSpace(tok))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	asJSON := isJSON(r)
	select {
	case in.decoders <- struct{}{}:
		defer func() { <-in.decoders }()
	default:
		w.Header().Set("Retry-After", "5")
		http.Error(w, "busy; send again in a few seconds", http.StatusServiceUnavailable)
		return
	}
	buf := in.bufs.Get().(*bytes.Buffer)
	buf.Reset()
	defer in.bufs.Put(buf)
	if err := in.readBody(buf, r); err != nil {
		var tooBig *tooBigError
		if errors.As(err, &tooBig) {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// accepted counts spans taken, whether sampling kept or dropped them.
	var accepted, rejected int
	line := in.bufs.Get().(*bytes.Buffer)
	defer in.bufs.Put(line)
	now := in.Now()
	add := func(_ []attr, sp *rawSpan) error {
		s, ok := convert(sp, claims)
		if !ok || !in.rates.take(claims.Service, in.Limits.ServiceRate, now) {
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
		err = decodeJSON(buf.Bytes(), add)
	} else {
		err = decodeProto(buf.Bytes(), add)
	}
	if err != nil && accepted == 0 {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	msg := ""
	if rejected > 0 {
		msg = "Dock dropped " + strconv.Itoa(rejected) + " spans: past the service's rate, or without IDs or a start"
	}
	reply(w, asJSON, rejected, msg)
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

// reply answers an export: success, or partial success with rejected
// spans, in the request's encoding.
func reply(w http.ResponseWriter, asJSON bool, rejected int, msg string) {
	if asJSON {
		w.Header().Set("Content-Type", "application/json")
		if rejected == 0 {
			_, _ = w.Write([]byte("{}"))
			return
		}
		b, _ := jsontext.AppendQuote([]byte(`{"partialSuccess":{"rejectedSpans":"`+strconv.Itoa(rejected)+`","errorMessage":`), msg)
		_, _ = w.Write(append(b, "}}"...))
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	if rejected == 0 {
		return
	}
	// ExportTraceServiceResponse{partial_success: {rejected_spans, error_message}}
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

// bucket is a token bucket: up to twice the rate in a burst.
type bucket struct {
	tokens float64
	last   time.Time
}

// take reports whether service may send one more span at now.
func (r *rates) take(service int64, rate float64, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.by[service]
	if b == nil {
		b = &bucket{tokens: 2 * rate, last: now}
		r.by[service] = b
	}
	b.tokens = min(2*rate, b.tokens+now.Sub(b.last).Seconds()*rate)
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
