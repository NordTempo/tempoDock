package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/s3"
)

// maxStuck bounds the stuck blobs remembered.
const maxStuck = 1024

// get answers a HEAD or GET of key: from the spool when the object is
// there, else from the bucket.
func (g *Gateway) get(ctx context.Context, w http.ResponseWriter, r *http.Request, key string, head bool) error {
	g.mu.Lock()
	e := g.sp.idx[key]
	var path string
	var size int64
	var etag string
	var mod time.Time
	if e != nil {
		path, size, etag, mod = g.sp.path(key, e.local), e.size, e.etag, e.mod
	}
	g.mu.Unlock()
	if e != nil {
		f, err := os.Open(path)
		if err == nil {
			defer func() { _ = f.Close() }()
			return serveFile(w, r, f, size, etag, mod, head)
		}
		// Uploaded and removed in between: the bucket has it now.
	}
	if isLocal(key) {
		return errNoSuchKey
	}
	c, err := g.client()
	if err != nil {
		return err
	}
	return g.readThrough(ctx, w, r, c, key, head)
}

// serveFile answers from a spool file, with Distribution's ranges
// ("bytes=N-", and "bytes=N-M").
func serveFile(w http.ResponseWriter, r *http.Request, f *os.File, size int64, etag string, mod time.Time, head bool) error {
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Last-Modified", mod.UTC().Format(http.TimeFormat))
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Accept-Ranges", "bytes")
	from, to, ranged, err := parseRange(r.Header.Get("Range"), size)
	if err != nil {
		return err
	}
	n := to - from
	h.Set("Content-Length", strconv.FormatInt(n, 10))
	if ranged {
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, to-1, size))
		w.WriteHeader(http.StatusPartialContent)
	}
	if head {
		return nil
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return err
	}
	_, _ = io.Copy(w, io.LimitReader(f, n))
	return nil
}

// errInvalidRange is S3's answer to a range past the end, which
// Distribution's driver reads as an empty object at that offset.
var errInvalidRange = &s3Error{http.StatusRequestedRangeNotSatisfiable, "InvalidRange", "The requested range is not satisfiable"}

// parseRange reads a Range header for size bytes: [from, to) and whether
// it was a range at all.
func parseRange(h string, size int64) (from, to int64, ranged bool, _ error) {
	if h == "" {
		return 0, size, false, nil
	}
	spec, ok := strings.CutPrefix(h, "bytes=")
	a, b, ok2 := strings.Cut(spec, "-")
	if !ok || !ok2 || strings.Contains(spec, ",") {
		return 0, size, false, nil // unsupported: the whole object, as S3 does
	}
	from, err := strconv.ParseInt(a, 10, 64)
	if err != nil || from < 0 {
		return 0, size, false, nil
	}
	to = size
	if b != "" {
		last, err := strconv.ParseInt(b, 10, 64)
		if err != nil || last < from {
			return 0, size, false, nil
		}
		to = min(last+1, size)
	}
	if from >= size {
		return 0, 0, true, errInvalidRange
	}
	return from, to, true, nil
}

// readThrough answers from the bucket. Each attempt has ReadTimeout to
// its first byte, then the body streams with the Stall watchdog. Missing
// keys and bad ranges are answers, not failures.
//
// A blob (or a link, reuploadable) that times out every attempt is stuck.
// During a push (and not in garbage collection) it gets PushAttempts of
// PushReadTimeout and is then reported missing, then and for StuckFor
// afterwards, so Docker uploads it again into the spool; while the bucket
// is failing, a push doesn't wait on it at all. Otherwise it gets every
// attempt and fails as unavailable: a pull waits longer rather than being
// told the blob is gone.
func (g *Gateway) readThrough(ctx context.Context, w http.ResponseWriter, r *http.Request, c *s3.Client, key string, head bool) error {
	blob := reuploadable(key)
	g.mu.Lock()
	pushing := blob && g.pushes > 0 && !g.strict
	pretend := g.pretendNowLocked(key)
	attempts, timeout := g.t.ReadAttempts, g.t.ReadTimeout
	if pushing {
		attempts, timeout = g.t.PushAttempts, g.t.PushReadTimeout
	}
	g.mu.Unlock()
	if pretend {
		return errNoSuchKey
	}
	var lastErr error
	timeouts := 0
	for attempt := range attempts {
		if attempt > 0 && !sleep(ctx, min(time.Duration(attempt)*250*time.Millisecond, g.t.ReadTimeout)) {
			return ctxErr(ctx, ctx.Err())
		}
		done, timedOut, err := g.readOnce(ctx, w, r, c, key, head, timeout)
		if done {
			g.mu.Lock()
			delete(g.stuck, key)
			g.mu.Unlock()
			return err
		}
		if ctx.Err() != nil {
			return ctxErr(ctx, ctx.Err())
		}
		lastErr = err
		if timedOut {
			timeouts++
		}
	}
	if blob && timeouts == attempts {
		g.mu.Lock()
		g.markStuckLocked(key)
		pretend = g.pretendLocked(key)
		g.mu.Unlock()
		g.log.Warn("registry gateway: a blob is stuck in the bucket", "key", key, "attempts", attempts)
		g.notify()
		if pretend {
			return errNoSuchKey
		}
	}
	return lastErr
}

// readOnce is one attempt, timeout to the bucket's first byte: done when it answered the request (with the
// object, or a missing key or range), else the error and whether the
// bucket didn't answer in time.
func (g *Gateway) readOnce(ctx context.Context, w http.ResponseWriter, r *http.Request, c *s3.Client, key string, head bool, timeout time.Duration) (done, timedOut bool, _ error) {
	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	first := time.AfterFunc(timeout, cancel)
	start := g.now()
	if head {
		info, err := c.Head(actx, key)
		timedOut = !first.Stop()
		g.record(start, err, timedOut)
		if err != nil {
			return answered(err), timedOut, err
		}
		h := w.Header()
		h.Set("Content-Length", strconv.FormatInt(info.Size, 10))
		h.Set("Content-Type", "application/octet-stream")
		if info.ETag != "" {
			h.Set("ETag", info.ETag)
		}
		if !info.LastModified.IsZero() {
			h.Set("Last-Modified", info.LastModified.UTC().Format(http.TimeFormat))
		}
		w.WriteHeader(http.StatusOK)
		return true, false, nil
	}
	resp, err := c.Open(actx, key, r.Header.Get("Range"))
	timedOut = !first.Stop()
	g.record(start, err, timedOut)
	if err != nil {
		return answered(err), timedOut, err
	}
	defer func() { _ = resp.Body.Close() }()
	for _, k := range []string{"Content-Length", "Content-Range", "Content-Type", "ETag", "Last-Modified", "Accept-Ranges"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	// The body streams as fast as the registry takes it; only a read that
	// waits on the bucket longer than Stall cancels it.
	wd := newWatchdog(g.t.Stall, cancel)
	defer wd.stop()
	_, err = io.Copy(w, &stallReader{r: resp.Body, w: wd})
	if err != nil && wd.fired() {
		g.record(start, errStalled, true)
	}
	return true, false, nil // the status is sent: a cut body is the registry's error
}

// answered: the bucket's answer is final (a missing key, a bad range or
// another client error), not worth another attempt.
func answered(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	return !s3.IsTransient(err)
}

// stallReader arms its watchdog only while a read waits on the bucket.
type stallReader struct {
	r io.Reader
	w *watchdog
}

func (s *stallReader) Read(b []byte) (int, error) {
	s.w.poke()
	n, err := s.r.Read(b)
	s.w.t.Stop()
	return n, err
}

// reuploadable reports whether a push writes key again when told it's
// missing: a blob's content in Distribution's blob store
// (<root>/docker/registry/v2/blobs/<alg>/<xx>/<hex>/data), or one of a
// repository's links Distribution reads on the way: to a layer
// (_layers/<alg>/<hex>/link), to a manifest (_manifests/revisions/<alg>/<hex>/link)
// and a tag's (_manifests/tags/<tag>/current/link).
func reuploadable(key string) bool {
	if _, rest, ok := strings.Cut(key, "/docker/registry/v2/blobs/"); ok {
		return strings.HasSuffix(rest, "/data") && strings.Count(rest, "/") == 3
	}
	_, rest, ok := strings.Cut(key, "/docker/registry/v2/repositories/")
	if !ok || !strings.HasSuffix(rest, "/link") {
		return false
	}
	for _, kind := range []string{"/_layers/", "/_manifests/revisions/"} {
		if _, link, ok := strings.Cut(rest, kind); ok {
			return strings.Count(link, "/") == 2
		}
	}
	_, tag, ok := strings.Cut(rest, "/_manifests/tags/")
	return ok && strings.Count(tag, "/") == 2 && strings.HasSuffix(tag, "/current/link")
}

// pretendLocked reports whether key is a stuck blob to report missing
// now: during a push, outside garbage collection, within StuckFor.
func (g *Gateway) pretendLocked(key string) bool {
	t, ok := g.stuck[key]
	if !ok {
		return false
	}
	if g.now().Sub(t) > g.t.StuckFor {
		delete(g.stuck, key)
		return false
	}
	return g.pushes > 0 && !g.strict
}

// pretendNowLocked reports whether key is to be reported missing without
// asking the bucket: a reuploadable key during a push that is stuck, or
// while the bucket is failing (it answers nothing; a push doesn't wait).
func (g *Gateway) pretendNowLocked(key string) bool {
	if !reuploadable(key) {
		return false
	}
	if g.pretendLocked(key) {
		return true
	}
	return g.pushes > 0 && !g.strict && g.health.state == Failing
}

// markStuckLocked remembers key as stuck, forgetting the oldest beyond
// maxStuck.
func (g *Gateway) markStuckLocked(key string) {
	if _, ok := g.stuck[key]; !ok && len(g.stuck) >= maxStuck {
		var oldest string
		var at time.Time
		for k, t := range g.stuck {
			if oldest == "" || t.Before(at) {
				oldest, at = k, t
			}
		}
		delete(g.stuck, oldest)
	}
	g.stuck[key] = g.now()
}
