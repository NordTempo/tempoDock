package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/NordTempo/tempoDock/internal/s3"
)

// checkedBody hashes a request body the client signed the SHA-256 of, and
// fails the read at its end if it doesn't match.
type checkedBody struct {
	r    io.ReadCloser
	want string
	h    hash.Hash
}

func (b *checkedBody) Read(p []byte) (int, error) {
	if b.h == nil && b.want != "UNSIGNED-PAYLOAD" {
		b.h = sha256.New()
	}
	n, err := b.r.Read(p)
	if b.h != nil {
		b.h.Write(p[:n])
	}
	if err == io.EOF && b.h != nil && hex.EncodeToString(b.h.Sum(nil)) != b.want {
		return n, &s3Error{http.StatusBadRequest, "XAmzContentSHA256Mismatch", "the body doesn't match its signed hash"}
	}
	return n, err
}

func (b *checkedBody) Close() error { return b.r.Close() }

// sum is the body's hash as an ETag, or "" when it wasn't signed.
func (b *checkedBody) sum() string {
	if b.h == nil {
		return ""
	}
	return `"` + hex.EncodeToString(b.h.Sum(nil)[:16]) + `"`
}

// putObject stores the body as key in the spool.
func (g *Gateway) putObject(w http.ResponseWriter, r *http.Request, key string) error {
	if r.ContentLength < 0 {
		return &s3Error{http.StatusLengthRequired, "MissingContentLength", "Content-Length is required"}
	}
	body, _ := r.Body.(*checkedBody)
	etag, err := g.write(r.Context(), key, r.ContentLength, func(f *os.File) (string, error) {
		n, err := io.Copy(f, r.Body)
		if err == nil && n != r.ContentLength {
			err = &s3Error{http.StatusBadRequest, "IncompleteBody", "the body is shorter than its Content-Length"}
		}
		if err != nil {
			return "", err
		}
		if body != nil && body.sum() != "" {
			return body.sum(), nil
		}
		return weakETag(n, time.Now()), nil
	})
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
	return nil
}

// copyObject copies an object to key: a hard link when the source is in
// the spool (Distribution's commit of an upload into the blob store),
// else a download from the bucket into the spool.
func (g *Gateway) copyObject(ctx context.Context, w http.ResponseWriter, r *http.Request, key string) error {
	src, err := copySource(r.Header.Get("X-Amz-Copy-Source"))
	if err != nil {
		return err
	}
	etag, err := g.copyInto(ctx, src, key)
	if err != nil {
		return err
	}
	return writeXML(w, struct {
		XMLName      xml.Name `xml:"CopyObjectResult"`
		ETag         string
		LastModified string
	}{ETag: etag, LastModified: g.now().UTC().Format(time.RFC3339Nano)})
}

// copyInto stores a copy of src as dst and returns its ETag.
func (g *Gateway) copyInto(ctx context.Context, src, dst string) (string, error) {
	g.mu.Lock()
	e := g.sp.idx[src]
	var path string
	var size int64
	if e != nil {
		path, size = g.sp.path(src, e.local), e.size
	}
	g.mu.Unlock()
	if e != nil {
		etag, err := g.write(ctx, dst, size, func(f *os.File) (string, error) {
			// Replace the empty file with a link to the source: no copy
			// of the bytes. (The spool counts them twice until the source
			// goes, right after.)
			name := f.Name()
			if err := os.Remove(name); err != nil {
				return "", err
			}
			if err := os.Link(path, name); err != nil {
				return "", err
			}
			return weakETag(size, time.Now()), nil
		})
		if err == nil || !os.IsNotExist(err) {
			return etag, err
		}
		// The source uploaded and left the spool in between: from the bucket.
	}
	if isLocal(src) {
		return "", errNoSuchKey
	}
	c, err := g.client()
	if err != nil {
		return "", err
	}
	return g.fetch(ctx, c, src, dst, "")
}

// fetch downloads src (or rng of it) from the bucket into the spool as
// dst, with ReadTimeout to the first byte and the Stall watchdog after.
func (g *Gateway) fetch(ctx context.Context, c *s3.Client, src, dst, rng string) (string, error) {
	resp, err := g.retryOpen(ctx, c, src, rng)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.ContentLength < 0 {
		return "", fmt.Errorf("the bucket sent %s without a length", src)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wd := newWatchdog(g.t.Stall, cancel)
	defer wd.stop()
	return g.write(ctx, dst, resp.ContentLength, func(f *os.File) (string, error) {
		n, err := io.Copy(f, &progress{r: resp.Body, w: wd})
		if err == nil && n != resp.ContentLength {
			err = io.ErrUnexpectedEOF
		}
		return weakETag(n, time.Now()), err
	})
}

// retryOpen GETs key (rng of it) from the bucket with ReadTimeout to its
// answer, retrying transient failures up to ReadAttempts. The body is
// the caller's to read and close; the deadline only covers the wait for
// the answer.
func (g *Gateway) retryOpen(ctx context.Context, c *s3.Client, key, rng string) (*http.Response, error) {
	var err error
	for attempt := range g.t.ReadAttempts {
		if attempt > 0 && !sleep(ctx, time.Duration(attempt)*250*time.Millisecond) {
			break
		}
		actx, cancel := context.WithCancel(ctx)
		first := time.AfterFunc(g.t.ReadTimeout, cancel)
		start := g.now()
		var resp *http.Response
		resp, err = c.Open(actx, key, rng)
		timedOut := !first.Stop()
		g.record(start, err, timedOut)
		if err == nil {
			context.AfterFunc(ctx, cancel) // the body's context ends with the request's
			return resp, nil
		}
		cancel()
		if answered(err) || ctx.Err() != nil {
			break
		}
	}
	return nil, ctxErr(ctx, err)
}

// deleteObjects answers DeleteObjects (quiet or not).
func (g *Gateway) deleteObjects(ctx context.Context, w http.ResponseWriter, body io.Reader) error {
	var in struct {
		Quiet   bool
		Objects []struct{ Key string } `xml:"Object"`
	}
	if err := xml.NewDecoder(io.LimitReader(body, 4<<20)).Decode(&in); err != nil {
		return &s3Error{http.StatusBadRequest, "MalformedXML", err.Error()}
	}
	keys := make([]string, 0, len(in.Objects))
	for _, o := range in.Objects {
		if !validKey(o.Key) {
			return &s3Error{http.StatusBadRequest, "InvalidArgument", "invalid key"}
		}
		keys = append(keys, o.Key)
	}
	if err := g.deleteKeys(ctx, keys); err != nil {
		return err
	}
	type deleted struct{ Key string }
	out := struct {
		XMLName xml.Name  `xml:"DeleteResult"`
		Deleted []deleted `xml:"Deleted"`
	}{}
	if !in.Quiet {
		for _, k := range keys {
			out.Deleted = append(out.Deleted, deleted{k})
		}
	}
	return writeXML(w, out)
}

// deleteKeys deletes keys from the spool and the bucket. An upload of one
// in flight is canceled and waited for first, so it can't land after the
// bucket's delete. An unfinished upload's key the spool had doesn't go
// to the bucket: it was never there.
func (g *Gateway) deleteKeys(ctx context.Context, keys []string) error {
	var waits []chan struct{}
	var remote []string
	g.mu.Lock()
	for _, k := range keys {
		_, had := g.sp.idx[k]
		if ch := g.sp.removeLocked(k); ch != nil {
			waits = append(waits, ch)
		}
		if t := g.up.timers[k]; t != nil {
			t.Stop()
			delete(g.up.timers, k)
		}
		if !had || !isLocal(k) {
			remote = append(remote, k)
		}
	}
	g.afterRemoveLocked()
	g.mu.Unlock()
	for _, ch := range waits {
		select {
		case <-ch:
		case <-ctx.Done():
			return ctxErr(ctx, ctx.Err())
		}
	}
	if len(remote) == 0 {
		return nil
	}
	c, err := g.client()
	if err != nil {
		return err
	}
	return g.retryMeta(ctx, func(ctx context.Context) error { return c.Delete(ctx, remote) })
}
