package fake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"sync"

	"github.com/NordTempo/tempoDock/internal/analytics/fakestore"
	"github.com/NordTempo/tempoDock/internal/node"
)

// fakeAnalytics is the fake's node.Analytics: an in-memory spool whose
// positions are line numbers, the salts it was handed and the store's
// spec. Tests append events with AppendSpool.
type fakeAnalytics struct {
	mu sync.Mutex
	// spools are the events' and the replay spool's lines (node.Spool).
	spools map[node.Spool]*fakeSpool
	salts  map[int64][]byte
	// traceKey is the trace ingest's key SetAnalytics handed over.
	traceKey []byte
	sites    map[int64]node.SiteRules
	store    *node.AnalyticsStoreSpec
	// fakeStore runs while the store does, on storeLn: an in-memory store
	// with the real one's API, which DialAnalyticsStore reaches.
	fakeStore *fakestore.Store
	storeLn   net.Listener
	failSet   error
	// failEnsure makes EnsureAnalyticsStore fail.
	failEnsure error
	// traces is the trace store's spec, and traceStore the in-memory
	// trace store that runs for it on traceLn.
	traces     *node.AnalyticsStoreSpec
	traceStore *fakestore.Traces
	traceLn    net.Listener
	// demo, demoSpans and demoMetrics append DOCK_FAKE_SPOOL's,
	// DOCK_FAKE_SPANS's and DOCK_FAKE_METRICS's lines once, on the first
	// tail of their spool.
	demo, demoSpans, demoMetrics sync.Once
}

// fakeSpool is one in-memory spool.
type fakeSpool struct {
	lines [][]byte
	acked uint64
	// appended is closed by the next append, waking tails.
	appended chan struct{}
}

// spool returns the named spool. Called with mu held.
func (a *fakeAnalytics) spool(which node.Spool) *fakeSpool {
	if a.spools == nil {
		a.spools = map[node.Spool]*fakeSpool{}
	}
	sp := a.spools[which]
	if sp == nil {
		sp = &fakeSpool{}
		a.spools[which] = sp
	}
	return sp
}

// AppendSpool adds events (JSON lines) to the fake spool, as the ingest
// would.
func (r *Runtime) AppendSpool(lines ...[]byte) { r.appendTo(node.SpoolEvents, lines) }

// AppendReplay adds replay chunks (JSON lines) to the fake replay spool.
func (r *Runtime) AppendReplay(lines ...[]byte) { r.appendTo(node.SpoolReplay, lines) }

// AppendSpans adds spans (analytics.Span JSON lines) to the spans spool,
// as the trace ingest does.
func (r *Runtime) AppendSpans(lines ...[]byte) { r.appendTo(node.SpoolSpans, lines) }

// AppendMetrics adds points (analytics.MetricPoint JSON lines) to the
// metrics spool, as the trace ingest does.
func (r *Runtime) AppendMetrics(lines ...[]byte) { r.appendTo(node.SpoolMetrics, lines) }

// TraceKey is the trace key SetAnalytics last handed over, nil while
// tracing is off.
func (r *Runtime) TraceKey() []byte {
	r.analytics.mu.Lock()
	defer r.analytics.mu.Unlock()
	return r.analytics.traceKey
}

// appendTo adds lines to a spool and wakes its tails.
func (r *Runtime) appendTo(which node.Spool, lines [][]byte) {
	a := &r.analytics
	a.mu.Lock()
	defer a.mu.Unlock()
	sp := a.spool(which)
	for _, l := range lines {
		sp.lines = append(sp.lines, bytes.Clone(l))
	}
	if sp.appended != nil {
		close(sp.appended)
		sp.appended = nil
	}
}

// SpoolAcked is the events spool's position last acknowledged.
func (r *Runtime) SpoolAcked() uint64 { return r.Acked(node.SpoolEvents) }

// Acked is a spool's position last acknowledged.
func (r *Runtime) Acked(which node.Spool) uint64 {
	r.analytics.mu.Lock()
	defer r.analytics.mu.Unlock()
	return r.analytics.spool(which).acked
}

// AnalyticsSalts are the salts SetAnalytics last handed over.
func (r *Runtime) AnalyticsSalts() map[int64][]byte {
	r.analytics.mu.Lock()
	defer r.analytics.mu.Unlock()
	return maps.Clone(r.analytics.salts)
}

// AnalyticsStoreSpec is the store's spec, nil when it doesn't run.
func (r *Runtime) AnalyticsStoreSpec() *node.AnalyticsStoreSpec {
	r.analytics.mu.Lock()
	defer r.analytics.mu.Unlock()
	return r.analytics.store
}

// AnalyticsStore is the in-memory store running for the store's spec, nil
// when none runs.
func (r *Runtime) AnalyticsStore() *fakestore.Store {
	r.analytics.mu.Lock()
	defer r.analytics.mu.Unlock()
	return r.analytics.fakeStore
}

// TraceStoreSpec is the trace store's spec, nil when it doesn't run.
func (r *Runtime) TraceStoreSpec() *node.AnalyticsStoreSpec {
	r.analytics.mu.Lock()
	defer r.analytics.mu.Unlock()
	return r.analytics.traces
}

// TraceStore is the in-memory trace store running for its spec, nil when
// none runs.
func (r *Runtime) TraceStore() *fakestore.Traces {
	r.analytics.mu.Lock()
	defer r.analytics.mu.Unlock()
	return r.analytics.traceStore
}

// FailSetAnalytics makes SetAnalytics return err.
func (r *Runtime) FailSetAnalytics(err error) {
	r.analytics.mu.Lock()
	r.analytics.failSet = err
	r.analytics.mu.Unlock()
}

// FailEnsureAnalyticsStore makes EnsureAnalyticsStore return err (nil:
// work again).
func (r *Runtime) FailEnsureAnalyticsStore(err error) {
	r.analytics.mu.Lock()
	r.analytics.failEnsure = err
	r.analytics.mu.Unlock()
}

// SetAnalytics implements node.Analytics.
func (r *Runtime) SetAnalytics(_ context.Context, cfg node.AnalyticsConfig) error {
	a := &r.analytics
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failSet != nil {
		return a.failSet
	}
	a.salts = maps.Clone(cfg.Salts)
	a.traceKey = bytes.Clone(cfg.TraceKey)
	if cfg.Sites != nil {
		a.sites = maps.Clone(cfg.Sites)
	}
	return nil
}

// AnalyticsSiteRules are the site rules SetAnalytics last handed over.
func (r *Runtime) AnalyticsSiteRules() map[int64]node.SiteRules {
	r.analytics.mu.Lock()
	defer r.analytics.mu.Unlock()
	return maps.Clone(r.analytics.sites)
}

// TailSpool implements node.Analytics, without the real one's second of
// batching: what is there goes at once.
func (r *Runtime) TailSpool(ctx context.Context, which node.Spool, after uint64) (<-chan node.SpoolBatch, error) {
	a := &r.analytics
	switch which {
	case node.SpoolEvents:
		a.demo.Do(func() { r.appendDemoSpool("DOCK_FAKE_SPOOL", which) })
	case node.SpoolSpans:
		a.demoSpans.Do(func() { r.appendDemoSpool("DOCK_FAKE_SPANS", which) })
	case node.SpoolMetrics:
		a.demoMetrics.Do(func() { r.appendDemoSpool("DOCK_FAKE_METRICS", which) })
	}
	a.mu.Lock()
	sp := a.spool(which)
	a.mu.Unlock()
	out := make(chan node.SpoolBatch)
	go func() {
		defer close(out)
		pos := after
		for {
			a.mu.Lock()
			end := min(uint64(len(sp.lines)), pos+node.SpoolBatchEvents)
			if pos >= end {
				if sp.appended == nil {
					sp.appended = make(chan struct{})
				}
				ch := sp.appended
				a.mu.Unlock()
				select {
				case <-ch:
					continue
				case <-ctx.Done():
					return
				}
			}
			b := node.SpoolBatch{Position: end, Events: bytes.Join(sp.lines[pos:end], []byte{'\n'}), Count: int(end - pos)}
			a.mu.Unlock()
			select {
			case out <- b:
				pos = end
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// AckSpool implements node.Analytics.
func (r *Runtime) AckSpool(_ context.Context, which node.Spool, position uint64, _ int) error {
	r.analytics.mu.Lock()
	defer r.analytics.mu.Unlock()
	sp := r.analytics.spool(which)
	sp.acked = max(sp.acked, position)
	return nil
}

// EnsureAnalyticsStore implements node.Analytics: it records spec and
// runs an in-memory store (fakestore) of spec's kind with spec's token, on
// a loopback port. A new token starts a new, empty store.
func (r *Runtime) EnsureAnalyticsStore(_ context.Context, spec node.AnalyticsStoreSpec) error {
	if spec.Binary == "" || len(spec.SHA256) != 64 {
		return errors.New("the analytics store needs its binary and its SHA-256")
	}
	a := &r.analytics
	a.mu.Lock()
	if a.failEnsure != nil {
		a.mu.Unlock()
		return a.failEnsure
	}
	op := "ensure-analytics-store"
	if spec.Kind == node.StoreTraces {
		op = "ensure-trace-store"
		if a.traces != nil && a.traces.Token != spec.Token {
			a.stopTraces()
		}
		if a.traces == nil {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				a.mu.Unlock()
				return err
			}
			ts := fakestore.NewTraces(spec.Token)
			go func() { _ = http.Serve(ln, ts.Handler()) }()
			a.traceStore, a.traceLn = ts, ln
		}
		a.traces = &spec
	} else {
		if a.store != nil && a.store.Token != spec.Token {
			a.stopStore()
		}
		if a.store == nil {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				a.mu.Unlock()
				return err
			}
			fs := fakestore.New(spec.Token)
			go func() { _ = http.Serve(ln, fs.Handler()) }()
			a.fakeStore, a.storeLn = fs, ln
		}
		a.store = &spec
	}
	a.mu.Unlock()
	r.mu.Lock()
	r.ops = append(r.ops, op)
	r.mu.Unlock()
	return nil
}

// RemoveAnalyticsStore implements node.Analytics: the in-memory store of
// kind and its data go.
func (r *Runtime) RemoveAnalyticsStore(_ context.Context, kind node.StoreKind) error {
	op := "remove-analytics-store"
	r.analytics.mu.Lock()
	if kind == node.StoreTraces {
		op = "remove-trace-store"
		r.analytics.stopTraces()
	} else {
		r.analytics.stopStore()
	}
	r.analytics.mu.Unlock()
	r.mu.Lock()
	r.ops = append(r.ops, op)
	r.mu.Unlock()
	return nil
}

// StopAnalyticsStore implements node.Analytics, recorded as
// "stop-analytics-store" or "stop-trace-store". The fake keeps no data
// apart from the store, so it stops it the same way.
func (r *Runtime) StopAnalyticsStore(_ context.Context, kind node.StoreKind) error {
	op := "stop-analytics-store"
	r.analytics.mu.Lock()
	if kind == node.StoreTraces {
		op = "stop-trace-store"
		r.analytics.stopTraces()
	} else {
		r.analytics.stopStore()
	}
	r.analytics.mu.Unlock()
	r.mu.Lock()
	r.ops = append(r.ops, op)
	r.mu.Unlock()
	return nil
}

// stopStore stops the in-memory store; mu is held.
func (a *fakeAnalytics) stopStore() {
	if a.storeLn != nil {
		_ = a.storeLn.Close()
	}
	a.store, a.fakeStore, a.storeLn = nil, nil, nil
}

// stopTraces stops the in-memory trace store; mu is held.
func (a *fakeAnalytics) stopTraces() {
	if a.traceLn != nil {
		_ = a.traceLn.Close()
	}
	a.traces, a.traceStore, a.traceLn = nil, nil, nil
}

// DialAnalyticsStore implements node.Analytics: it connects to the
// in-memory store of kind.
func (r *Runtime) DialAnalyticsStore(ctx context.Context, kind node.StoreKind) (net.Conn, error) {
	r.analytics.mu.Lock()
	ln := r.analytics.storeLn
	if kind == node.StoreTraces {
		ln = r.analytics.traceLn
	}
	r.analytics.mu.Unlock()
	if ln == nil {
		return nil, fmt.Errorf("the %q store isn't running on this server", kind)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", ln.Addr().String())
}

// appendDemoSpool appends the lines of the file the environment variable
// env names (JSON lines, as the ingest writes them) to a spool, so a
// `make dev` daemon's analytics pages have visits, errors and vitals to
// show (DOCK_FAKE_SPOOL, the events spool) and its traces spans
// (DOCK_FAKE_SPANS, analytics.Span lines). Read once, when the collector
// first tails the spool; a missing file adds nothing.
func (r *Runtime) appendDemoSpool(env string, which node.Spool) {
	path := os.Getenv(env)
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var lines [][]byte
	for line := range bytes.SplitSeq(bytes.TrimSpace(b), []byte{'\n'}) {
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}
	r.appendTo(which, lines)
}
