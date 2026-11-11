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
	// DefaultDecoders is how many bodies are decoded at once; past it a
	// request gets 503 and retries later.
	DefaultDecoders = 2
	// DefaultServiceRate is the spans a second a service may send, with
	// twice as many in a burst; past it spans are rejected.
	DefaultServiceRate = 5000
)

// Limits bound the ingest; zero values are the defaults.
type Limits struct {
	MaxBody, MaxDecoded, Decoders int
	ServiceRate                   float64
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
	readers  readers
	bufs     sync.Pool
	rates    rates
}

// init applies the limits' defaults.
func (in *Ingest) init() {
	in.once.Do(func() {
		l := &in.Limits
		l.MaxBody, l.MaxDecoded, l.Decoders = cmpOr(l.MaxBody, DefaultMaxBody), cmpOr(l.MaxDecoded, DefaultMaxDecoded), cmpOr(l.Decoders, DefaultDecoders)
		if l.ServiceRate <= 0 {
			l.ServiceRate = DefaultServiceRate
		}
		in.decoders = make(chan struct{}, l.Decoders)
		in.bufs.New = func() any { return new(bytes.Buffer) }
		in.rates.by = map[int64]*bucket{}
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

	var kept, rejected int
	line := in.bufs.Get().(*bytes.Buffer)
	defer in.bufs.Put(line)
	now := in.Now()
	add := func(sp *rawSpan) error {
		s, ok := convert(sp, claims)
		if !ok || !in.rates.take(claims.Service, in.Limits.ServiceRate, now) {
			rejected++
			return nil
		}
		line.Reset()
		err := json.MarshalWrite(line, &s)
		if err == nil {
			// MarshalWrite ends the line with a newline; the spool adds its own.
			err = in.Spool.Append(bytes.TrimSuffix(line.Bytes(), []byte{'\n'}))
		}
		if err != nil {
			rejected++
			if in.Log != nil {
				in.Log.Warn("traces: spool a span", "service", claims.Service, "err", err)
			}
			return nil
		}
		kept++
		return nil
	}
	if asJSON {
		err = decodeJSON(buf.Bytes(), add)
	} else {
		err = decodeProto(buf.Bytes(), add)
	}
	if err != nil && kept == 0 {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	msg := ""
	if rejected > 0 {
		msg = "Dock dropped " + strconv.Itoa(rejected) + " spans: past the service's rate, or without IDs or a start"
	}
	reply(w, asJSON, rejected, msg)
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
