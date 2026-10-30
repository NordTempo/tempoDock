package fake

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"net"
	"net/http"
	"sync"

	"github.com/NordTempo/tempoDock/internal/analytics/fakestore"
	"github.com/NordTempo/tempoDock/internal/node"
)

// fakeAnalytics is the fake's node.Analytics: an in-memory spool whose
// positions are line numbers, the salts it was handed and the store's
// spec. Tests append events with AppendSpool.
type fakeAnalytics struct {
	mu    sync.Mutex
	lines [][]byte
	acked uint64
	salts map[int64][]byte
	store *node.AnalyticsStoreSpec
	// fakeStore runs while the store does, on storeLn: an in-memory store
	// with the real one's API, which DialAnalyticsStore reaches.
	fakeStore *fakestore.Store
	storeLn   net.Listener
	// appended is closed by the next append, waking tails.
	appended chan struct{}
	failSet  error
}

// AppendSpool adds events (JSON lines) to the fake spool, as the ingest
// would.
func (r *Runtime) AppendSpool(lines ...[]byte) {
	a := &r.analytics
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, l := range lines {
		a.lines = append(a.lines, bytes.Clone(l))
	}
	if a.appended != nil {
		close(a.appended)
		a.appended = nil
	}
}

// SpoolAcked is the position last acknowledged.
func (r *Runtime) SpoolAcked() uint64 {
	r.analytics.mu.Lock()
	defer r.analytics.mu.Unlock()
	return r.analytics.acked
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

// FailSetAnalytics makes SetAnalytics return err.
func (r *Runtime) FailSetAnalytics(err error) {
	r.analytics.mu.Lock()
	r.analytics.failSet = err
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
	return nil
}

// TailSpool implements node.Analytics, without the real one's second of
// batching: what is there goes at once.
func (r *Runtime) TailSpool(ctx context.Context, after uint64) (<-chan node.SpoolBatch, error) {
	a := &r.analytics
	out := make(chan node.SpoolBatch)
	go func() {
		defer close(out)
		pos := after
		for {
			a.mu.Lock()
			end := min(uint64(len(a.lines)), pos+node.SpoolBatchEvents)
			if pos >= end {
				if a.appended == nil {
					a.appended = make(chan struct{})
				}
				ch := a.appended
				a.mu.Unlock()
				select {
				case <-ch:
					continue
				case <-ctx.Done():
					return
				}
			}
			b := node.SpoolBatch{Position: end, Events: bytes.Join(a.lines[pos:end], []byte{'\n'}), Count: int(end - pos)}
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
func (r *Runtime) AckSpool(_ context.Context, position uint64, _ int) error {
	r.analytics.mu.Lock()
	defer r.analytics.mu.Unlock()
	r.analytics.acked = max(r.analytics.acked, position)
	return nil
}

// EnsureAnalyticsStore implements node.Analytics: it records spec and
// runs an in-memory store (fakestore) with spec's token, on a loopback
// port, until spec is nil. A new token starts a new, empty store.
func (r *Runtime) EnsureAnalyticsStore(_ context.Context, spec *node.AnalyticsStoreSpec) error {
	a := &r.analytics
	a.mu.Lock()
	if spec == nil || (a.store != nil && a.store.Token != spec.Token) {
		if a.storeLn != nil {
			_ = a.storeLn.Close()
		}
		a.store, a.fakeStore, a.storeLn = nil, nil, nil
	}
	if spec != nil && a.store == nil {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			a.mu.Unlock()
			return err
		}
		fs := fakestore.New(spec.Token)
		go func() { _ = http.Serve(ln, fs.Handler()) }()
		a.fakeStore, a.storeLn = fs, ln
	}
	if spec != nil {
		c := *spec
		a.store = &c
	}
	a.mu.Unlock()
	r.mu.Lock()
	r.ops = append(r.ops, "ensure-analytics-store")
	r.mu.Unlock()
	return nil
}

// DialAnalyticsStore implements node.Analytics: it connects to the
// in-memory store.
func (r *Runtime) DialAnalyticsStore(ctx context.Context) (net.Conn, error) {
	r.analytics.mu.Lock()
	ln := r.analytics.storeLn
	r.analytics.mu.Unlock()
	if ln == nil {
		return nil, errors.New("the analytics store isn't running on this server")
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", ln.Addr().String())
}
