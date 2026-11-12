package traces

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// Sampling, at ingest (ADR 0075). Apps send every span and the ingest
// decides, per trace:
//
//   - A trace's first span decides it: kept when the trace ID's random
//     bits fall under the service's threshold, so every span of a trace
//     gets the same answer for the same threshold. The threshold adapts
//     each minute to keep about Budget traces a minute a service, and
//     never more than that in a minute. Later spans, of any service on
//     this server, follow the decision.
//   - A span with an error, an exception or a 5xx, or an entry span at
//     least Slow long, rescues its trace: the trace's dropped spans are
//     pulled out of a ring of recently dropped ones, and later spans
//     follow. Children end before their parents, so a slow or failing
//     root still finds its children there.
//   - Dock's health probe sends its own trace context, with a trace ID
//     that says so, and its uptime probe a user agent that does: a probe's
//     trace is never sampled and a slow one isn't rescued, but an error
//     still rescues it, so a failing health check stays visible.
//
// The ring, the decisions and the per-service state exist only while
// spans arrive: one timer frees them two quiet minutes after the last.

// Defaults of the sampler's limits (Limits' zero values).
const (
	// DefaultBudget is the traces a minute a service keeps by sampling.
	DefaultBudget = 3000
	// DefaultSlow is how long an entry span takes to rescue its trace.
	DefaultSlow = time.Second
	// DefaultRescues caps the traces a service keeps for an error or for
	// being slow a minute, beyond those it samples: past it they are
	// dropped like any, so a flood of failing requests (a 5xx route on
	// the internet) can't spend the day's error cap in seconds.
	DefaultRescues = 600
	// DefaultRingBytes is the ring of dropped spans, per server.
	DefaultRingBytes = 4 << 20
)

// ProbeTraceHi is the first half of the trace IDs Dock's health probe
// (cmd/dock-probe) sends: "dockprbe".
const ProbeTraceHi = 0x646f636b70726265

const (
	// ringChunk is the unit the ring is allocated and recycled in.
	ringChunk = 1 << 20
	// decidedCap is how many decisions each generation of dropped and of
	// kept traces holds: about 3 MB at most for all four. Kept traces
	// are bounded per service and minute (budget and rescues), so a flood
	// of new traces turns the dropped ones over, never the kept.
	decidedCap = 32 << 10
	// idleRelease is how long the sampler keeps its memory after the
	// last span.
	idleRelease = 2 * time.Minute
	// randomBits are the bits of a trace ID W3C requires to be random,
	// the rightmost 56.
	randomBits = 56
)

// sampler holds the ingest's sampling state.
type sampler struct {
	mu       sync.Mutex
	services map[int64]*serviceSample
	// dropped and kept are the decisions, by org and trace ID: one org's
	// app can't reach another's traces even with their IDs.
	dropped, kept generations
	ring          ring
	// last is the last span's arrival, release the timer that frees the
	// state after idleRelease without one.
	last    time.Time
	release *time.Timer
	// line is the buffer a rescued span is rewritten in.
	line []byte
}

// traceKey is a trace in an org.
type traceKey struct {
	org   int64
	trace analytics.TraceID
}

// generations holds decisions in two maps: when the newer fills it
// becomes the older and the older is dropped, so there is no timer.
type generations struct{ cur, old map[traceKey]decision }

func (g *generations) get(k traceKey) (decision, bool) {
	if d, ok := g.cur[k]; ok {
		return d, true
	}
	d, ok := g.old[k]
	return d, ok
}

func (g *generations) put(k traceKey, d decision) {
	if g.cur == nil {
		g.cur = make(map[traceKey]decision, 1024)
	}
	if _, ok := g.cur[k]; !ok && len(g.cur) >= decidedCap {
		g.old, g.cur = g.cur, make(map[traceKey]decision, decidedCap)
	}
	g.cur[k] = d
	delete(g.old, k)
}

func (g *generations) remove(k traceKey) {
	delete(g.cur, k)
	delete(g.old, k)
}

// decision is what a trace got: dropped, or kept for a reason.
type decision struct {
	kept   bool
	reason analytics.Kept
}

// serviceSample is a service's sampling state in the current minute.
type serviceSample struct {
	minute time.Time
	// seen and kept count the service's new traces this minute;
	// rescued its rescues.
	seen, kept, rescued int
	// threshold keeps a trace when its random bits are below it; set
	// at each minute's start from the minute before.
	threshold uint64
}

// sampleLimits are the sampler's limits with their defaults applied.
type sampleLimits struct {
	budget, rescues, ringBytes int
	slow                       time.Duration
}

// emit is where kept spans go, as JSON lines.
type emit func(line []byte) error

// signals are what the ingest saw in a span beyond analytics.Span.
type signals struct {
	// exception: the span has an exception event.
	exception bool
	// probe: Dock's health or uptime probe sent the request (its user
	// agent), for SDKs that don't take the probe's trace context.
	probe bool
}

// signalsOf reads a decoded span's signals.
func signalsOf(sp *rawSpan) signals {
	var sig signals
	for _, e := range sp.events {
		sig.exception = sig.exception || e.name == "exception"
	}
	for _, a := range sp.attrs {
		if (a.key == "user_agent.original" || a.key == "http.user_agent") && a.val.kind == valueString &&
			(strings.HasPrefix(a.val.s, "dock-probe") || strings.HasPrefix(a.val.s, "dock-uptime/")) {
			sig.probe = true
		}
	}
	return sig
}

// span decides one span: emits it (and any of its trace's dropped spans
// it rescues), or keeps it in the ring. line is the span as JSON, with
// Kept unset. A kept span says the strongest reason its trace has so far
// (error, then slow, then sampled), so a trace's entry span, which ends
// last, says whether anything in it failed.
func (sm *sampler) span(s *analytics.Span, line []byte, sig signals, l sampleLimits, now time.Time, out emit) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.touch(now)
	svc := sm.service(s.Service, l, now)
	hi, _ := s.Trace.Halves()
	probe := sig.probe || hi == ProbeTraceHi
	k := traceKey{s.Org, s.Trace}
	d, ok := sm.lookup(k)
	if !ok {
		d = sm.decide(k, svc, probe, l)
	}
	reason, rescue := trigger(s, sig.exception, probe, l)
	switch {
	case rescue && !d.kept && svc.rescued < l.rescues:
		svc.rescued++
		d = decision{kept: true, reason: reason}
		sm.remember(k, d)
		if err := sm.rescue(k, reason, out); err != nil {
			return err
		}
	case rescue && d.kept && strength(reason) > strength(d.reason):
		d.reason = reason
		sm.remember(k, d)
	case probe && d.kept && d.reason == analytics.KeptSampled:
		// Sampled before its probe span said what it was: the rest of
		// the trace, its entry span with it, isn't kept.
		d = decision{}
		sm.remember(k, d)
	}
	if !d.kept {
		sm.ring.add(k, line, l.ringBytes)
		return nil
	}
	return out(withKept(&sm.line, line, d.reason))
}

// trigger reports whether s rescues its trace, and why: an error, or a
// slow entry span, but for a probe's trace an error only.
func trigger(s *analytics.Span, exception, probe bool, l sampleLimits) (analytics.Kept, bool) {
	switch {
	case s.Error() || exception:
		return analytics.KeptError, true
	case s.Entry && !probe && s.Duration >= l.slow.Microseconds():
		return analytics.KeptSlow, true
	}
	return 0, false
}

// strength orders the reasons to keep a trace: forced, error, slow, then
// sampled.
func strength(k analytics.Kept) int {
	return [...]int{analytics.KeptSampled: 0, analytics.KeptSlow: 1, analytics.KeptError: 2, analytics.KeptForced: 3}[min(int(k), 3)]
}

// decide decides a trace from its first span, of service svc; a probe's
// trace is never sampled.
func (sm *sampler) decide(id traceKey, svc *serviceSample, probe bool, l sampleLimits) decision {
	_, lo := id.trace.Halves()
	svc.seen++
	d := decision{}
	if !probe && lo&(1<<randomBits-1) < svc.threshold && svc.kept < l.budget {
		svc.kept++
		d = decision{kept: true, reason: analytics.KeptSampled}
	}
	sm.remember(id, d)
	return d
}

// service is a service's state, rolled over to now's minute: the new
// minute's threshold keeps about the budget of the last minute's traces.
func (sm *sampler) service(id int64, l sampleLimits, now time.Time) *serviceSample {
	minute := now.Truncate(time.Minute)
	svc := sm.services[id]
	if svc == nil {
		svc = &serviceSample{minute: minute, threshold: 1 << randomBits}
		sm.services[id] = svc
	}
	if minute.After(svc.minute) {
		share := 1.0
		if minute.Sub(svc.minute) == time.Minute && svc.seen > l.budget {
			share = float64(l.budget) / float64(svc.seen)
		}
		*svc = serviceSample{minute: minute, threshold: uint64(share * (1 << randomBits))}
	}
	return svc
}

// lookup is a trace's decision, if it has one.
func (sm *sampler) lookup(k traceKey) (decision, bool) {
	if d, ok := sm.kept.get(k); ok {
		return d, true
	}
	return sm.dropped.get(k)
}

// remember records a trace's decision, with the kept or the dropped.
func (sm *sampler) remember(k traceKey, d decision) {
	if d.kept {
		sm.kept.put(k, d)
		sm.dropped.remove(k)
		return
	}
	sm.dropped.put(k, d)
	sm.kept.remove(k)
}

// rescue emits the trace's spans in the ring, kept for reason.
func (sm *sampler) rescue(id traceKey, reason analytics.Kept, out emit) error {
	var err error
	sm.ring.take(id, func(line []byte) {
		if err == nil {
			err = out(withKept(&sm.line, line, reason))
		}
	})
	return err
}

// withKept is a span's JSON line with its reason for being kept: the
// line has none (Kept's zero value, sampled, is omitted), so a reason is
// written first in the object. buf is reused.
func withKept(buf *[]byte, line []byte, reason analytics.Kept) []byte {
	if reason == analytics.KeptSampled || len(line) < 2 {
		return line
	}
	b := append((*buf)[:0], `{"kept":`...)
	b = strconv.AppendUint(b, uint64(reason), 10)
	b = append(b, ',')
	b = append(b, line[1:]...)
	*buf = b
	return b
}

// touch notes a span's arrival, allocating the state on the first and
// arming the timer that frees it.
func (sm *sampler) touch(now time.Time) {
	sm.last = now
	if sm.services != nil {
		return
	}
	sm.services = map[int64]*serviceSample{}
	if sm.release == nil {
		// Freeing the sampler's memory after the last span: one timer,
		// armed while spans arrive (docs/performance.md).
		sm.release = time.AfterFunc(idleRelease, sm.free)
	} else {
		sm.release.Reset(idleRelease)
	}
}

// free drops the sampler's state once spans stopped two minutes ago,
// and otherwise waits for the rest of the two minutes.
func (sm *sampler) free() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if left := idleRelease - time.Since(sm.last); left > 0 {
		sm.release.Reset(left)
		return
	}
	sm.services, sm.dropped, sm.kept, sm.line = nil, generations{}, generations{}, nil
	sm.ring = ring{}
}

// ring holds recently dropped spans, so a trace that turns out to matter
// can be rescued: chunks of 1 MB, allocated as needed up to its size,
// the oldest recycled whole when they're all full.
type ring struct {
	chunks []*chunk
	cur    int
}

// chunk is a ring's unit: span lines back to back, with their traces.
// orgs[i] is recs[i]'s org, apart because a rescue scans every record and
// reads the org only where the trace ID matches: 24-byte records instead
// of 32 keep that scan of the whole ring a third smaller
// (BenchmarkSampleRescue).
type chunk struct {
	data []byte
	recs []rec
	orgs []int64
}

// rec is a span line in a chunk; a taken one has a zero trace.
type rec struct {
	trace     analytics.TraceID
	off, size int32
}

// add keeps a dropped span's line, for a ring of at most size bytes.
func (r *ring) add(key traceKey, line []byte, size int) {
	if len(line) > ringChunk {
		return
	}
	if len(r.chunks) == 0 {
		r.chunks = append(r.chunks, newChunk())
	}
	c := r.chunks[r.cur]
	if len(c.data)+len(line) > ringChunk {
		r.cur++
		if r.cur == len(r.chunks) && len(r.chunks) < max(size/ringChunk, 1) {
			r.chunks = append(r.chunks, newChunk())
		}
		r.cur %= len(r.chunks)
		c = r.chunks[r.cur]
		c.data, c.recs, c.orgs = c.data[:0], c.recs[:0], c.orgs[:0]
	}
	c.recs = append(c.recs, rec{trace: key.trace, off: int32(len(c.data)), size: int32(len(line))})
	c.orgs = append(c.orgs, key.org)
	c.data = append(c.data, line...)
}

// newChunk allocates a ring chunk.
func newChunk() *chunk {
	return &chunk{data: make([]byte, 0, ringChunk), recs: make([]rec, 0, ringChunk/512), orgs: make([]int64, 0, ringChunk/512)}
}

// take calls fn with each of the trace's lines, oldest first, and
// forgets them.
func (r *ring) take(key traceKey, fn func(line []byte)) {
	for i := range r.chunks {
		c := r.chunks[(r.cur+1+i)%len(r.chunks)]
		for j := range c.recs {
			rc := &c.recs[j]
			if rc.trace != key.trace || c.orgs[j] != key.org {
				continue
			}
			fn(c.data[rc.off : rc.off+rc.size])
			rc.trace = analytics.TraceID{}
		}
	}
}
