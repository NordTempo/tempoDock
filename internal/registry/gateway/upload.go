package gateway

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/s3"
)

const (
	// multipartFrom is the size from which an object uploads in parts,
	// partSize each, so a failed part is retried alone and a failed
	// upload resumes after the parts the bucket has.
	multipartFrom = 64 << 20
	partSize      = 32 << 20
	// partAttempts is how often a part is tried, RetryMin and twice that
	// apart, before the object's upload fails (and backs off).
	partAttempts = 3
)

// uploader moves the spool's objects to the bucket. Its fields are
// guarded by Gateway.mu. Workers run only while something is queued; a
// failed object waits its backoff on its own timer, so an idle spool has
// neither goroutines nor timers.
type uploader struct {
	queue   []string
	queued  map[string]bool
	timers  map[string]*time.Timer
	running int
	closed  bool
	wg      sync.WaitGroup
	// drained are Drain's waiters, closed when nothing waits to upload.
	drained []chan struct{}
}

// resume is a multipart upload in the bucket that failed part way: the
// next attempt for the same generation continues after its parts.
type resume struct {
	id    string
	gen   uint64
	size  int64
	parts []s3.Part
}

// enqueue queues key's upload unless it is queued or waits a retry.
func (u *uploader) enqueue(key string, e *entry, _ time.Time) {
	if e.local || u.queued[key] || u.timers[key] != nil {
		return
	}
	if u.queued == nil {
		u.queued = map[string]bool{}
	}
	u.queued[key] = true
	u.queue = append(u.queue, key)
}

// stopTimersLocked stops every retry timer (Close).
func (u *uploader) stopTimersLocked() {
	for k, t := range u.timers {
		t.Stop()
		delete(u.timers, k)
	}
}

// kickLocked starts workers for what is queued, up to Timing.Workers.
func (g *Gateway) kickLocked() {
	u := &g.up
	if g.bucket == nil || u.closed {
		return
	}
	// A worker that just finished takes from the queue too; one started
	// for nothing finds it empty and exits.
	for range min(g.t.Workers-u.running, len(u.queue)) {
		u.running++
		u.wg.Add(1)
		go g.worker()
	}
}

// worker uploads queued objects until the queue is empty.
func (g *Gateway) worker() {
	defer g.up.wg.Done()
	for {
		g.mu.Lock()
		key, e, c := g.nextLocked()
		if e == nil {
			g.up.running--
			g.mu.Unlock()
			g.notify()
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		e.busy, e.cancel = make(chan struct{}), cancel
		gen, path, res := e.gen, g.sp.path(key, false), e.resume
		g.mu.Unlock()

		res, err := g.upload(ctx, c, key, path, gen, res)
		cancel()
		g.finish(key, e, gen, res, err)
	}
}

// nextLocked pops the next object to upload.
func (g *Gateway) nextLocked() (string, *entry, *s3.Client) {
	u := &g.up
	for len(u.queue) > 0 && g.bucket != nil && !u.closed {
		key := u.queue[0]
		u.queue = u.queue[1:]
		delete(u.queued, key)
		if e := g.sp.idx[key]; e != nil && e.busy == nil && !e.local {
			return key, e, g.bucket
		}
	}
	return "", nil, nil
}

// upload sends path's object to the bucket as key: in one request, or in
// parts, continuing res when it is the same generation's.
func (g *Gateway) upload(ctx context.Context, c *s3.Client, key, path string, gen uint64, res *resume) (*resume, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err // removed meanwhile: finish sees the entry gone
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size < multipartFrom {
		return nil, g.call(ctx, func(ctx context.Context, body func(io.Reader) io.Reader) error {
			return c.Put(ctx, key, body(io.NewSectionReader(f, 0, size)), size)
		})
	}
	if res != nil && (res.gen != gen || res.size != size) {
		_ = c.AbortMultipart(context.WithoutCancel(ctx), key, res.id)
		res = nil
	}
	if res == nil {
		var id string
		err := g.call(ctx, func(ctx context.Context, _ func(io.Reader) io.Reader) (err error) {
			id, err = c.CreateMultipart(ctx, key)
			return err
		})
		if err != nil {
			return nil, err
		}
		res = &resume{id: id, gen: gen, size: size}
	}
	for off := int64(len(res.parts)) * partSize; off < size; off += partSize {
		n := min(partSize, size-off)
		var p s3.Part
		for attempt := range partAttempts {
			err = g.call(ctx, func(ctx context.Context, body func(io.Reader) io.Reader) (err error) {
				p, err = c.UploadPart(ctx, key, res.id, len(res.parts)+1, body(io.NewSectionReader(f, off, n)), n)
				return err
			})
			if err == nil || !s3.IsTransient(err) || ctx.Err() != nil || attempt == partAttempts-1 {
				break
			}
			if !sleep(ctx, time.Duration(attempt+1)*g.t.RetryMin) {
				break
			}
		}
		if err != nil {
			return res, err
		}
		res.parts = append(res.parts, p)
	}
	err = g.call(ctx, func(ctx context.Context, _ func(io.Reader) io.Reader) error {
		return c.CompleteMultipart(ctx, key, res.id, res.parts)
	})
	if err != nil {
		return res, err
	}
	return nil, nil
}

// finish records an upload's outcome: on success the spool's file goes
// (unless a newer write replaced it), on failure the object retries after
// its backoff.
func (g *Gateway) finish(key string, e *entry, gen uint64, res *resume, err error) {
	g.mu.Lock()
	close(e.busy)
	e.busy, e.cancel = nil, nil
	switch {
	case g.sp.idx[key] != e:
		// Deleted while uploading; the delete waited for this to end.
	case err == nil && e.gen == gen:
		e.resume = nil
		g.sp.removeLocked(key)
		g.afterRemoveLocked()
	case err == nil:
		e.resume, e.failures = nil, 0
		g.up.enqueue(key, e, g.now())
	case g.up.closed:
		e.resume = res
	default:
		e.resume = res
		e.failures++
		delay := backoff(g.t.RetryMin, g.t.RetryMax, e.failures)
		e.retryAt = g.now().Add(delay)
		if g.up.timers == nil {
			g.up.timers = map[string]*time.Timer{}
		}
		g.up.timers[key] = time.AfterFunc(delay, func() {
			g.mu.Lock()
			delete(g.up.timers, key)
			if g.sp.idx[key] == e {
				g.up.enqueue(key, e, g.now())
				g.kickLocked()
			}
			g.mu.Unlock()
		})
		g.log.Warn("registry gateway: upload to the bucket failed; retrying", "key", key, "attempt", e.failures, "in", delay, "err", err)
	}
	g.kickLocked()
	g.mu.Unlock()
	g.notify()
}

// afterRemoveLocked ends Drain's waits when nothing waits to upload any
// more, and a refusal once there is room again.
func (g *Gateway) afterRemoveLocked() {
	if g.sp.refusing != "" {
		if free, total, err := diskStat(g.sp.dir); err == nil && g.sp.used < g.sp.max && float64(free) >= g.sp.minFree*float64(total) {
			g.sp.refusing, g.sp.refusingSince = "", time.Time{}
		}
	}
	if g.waitingLocked() > 0 {
		return
	}
	for _, ch := range g.up.drained {
		close(ch)
	}
	g.up.drained = nil
}

// waitingLocked counts the objects waiting to upload.
func (g *Gateway) waitingLocked() int {
	n := 0
	for _, e := range g.sp.idx {
		if !e.local {
			n++
		}
	}
	return n
}

// Drain waits until nothing waits to upload (a migration away from the
// bucket needs everything in it), or ctx ends.
func (g *Gateway) Drain(ctx context.Context) error {
	g.mu.Lock()
	n := g.waitingLocked()
	if n == 0 {
		g.mu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	g.up.drained = append(g.up.drained, ch)
	g.retryNowLocked()
	g.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		g.mu.Lock()
		n = g.waitingLocked()
		g.mu.Unlock()
		return &DrainError{Waiting: n}
	}
}

// retryNowLocked queues every object waiting out its backoff now: the
// bucket just answered a call, or Drain wants everything uploaded.
func (g *Gateway) retryNowLocked() {
	if len(g.up.timers) == 0 {
		return
	}
	for k, t := range g.up.timers {
		if t.Stop() {
			delete(g.up.timers, k)
			if e := g.sp.idx[k]; e != nil {
				g.up.enqueue(k, e, g.now())
			}
		}
	}
	g.kickLocked()
}

// DrainError says how many objects still wait to upload when Drain gave
// up.
type DrainError struct{ Waiting int }

func (e *DrainError) Error() string {
	return "objects still wait on this server's disk to upload to the bucket"
}

// call makes one bucket request with the stall watchdog: body wraps a
// request body so each read pushes the deadline Timing.Stall out, and the
// request is canceled once nothing moved for that long (also while
// waiting for the answer). The outcome feeds health.
func (g *Gateway) call(ctx context.Context, do func(ctx context.Context, body func(io.Reader) io.Reader) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := newWatchdog(g.t.Stall, cancel)
	defer w.stop()
	start := g.now()
	err := do(ctx, func(r io.Reader) io.Reader { return &progress{r: r, w: w} })
	if errors.Is(err, context.Canceled) && w.fired() {
		err = errStalled
	}
	if !errors.Is(err, context.Canceled) {
		g.record(start, err, w.fired())
	}
	return err
}

// errStalled is a bucket request the watchdog canceled.
var errStalled = errors.New("the bucket stopped answering")

// watchdog cancels after d without a poke.
type watchdog struct {
	d    time.Duration
	t    *time.Timer
	mu   sync.Mutex
	done bool
}

func newWatchdog(d time.Duration, cancel func()) *watchdog {
	w := &watchdog{d: d}
	w.t = time.AfterFunc(d, func() {
		w.mu.Lock()
		w.done = true
		w.mu.Unlock()
		cancel()
	})
	return w
}

func (w *watchdog) poke() { w.t.Reset(w.d) }
func (w *watchdog) stop() { w.t.Stop() }
func (w *watchdog) fired() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.done
}

// progress pokes a watchdog on every read.
type progress struct {
	r io.Reader
	w *watchdog
}

func (p *progress) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.w.poke()
	return n, err
}

// backoff is lo doubled per failure, capped at hi.
func backoff(lo, hi time.Duration, failures int) time.Duration {
	d := lo
	for i := 1; i < failures && d < hi; i++ {
		d *= 2
	}
	return min(d, hi)
}

// sleep waits d or until ctx ends; false if it ended.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
