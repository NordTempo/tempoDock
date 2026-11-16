// Package gateway is the registry's storage gateway (ADR 0084): a small
// S3-compatible endpoint the daemon serves on loopback, between
// Distribution's S3 driver and the bucket, when the registry's images are
// in a bucket (ADR 0034).
//
// # Why
//
// Distribution waits on the bucket for every request, without deadlines,
// so a slow or partly degraded store makes pushes hang until Docker gives
// up. The gateway makes the control plane's disk the first stop instead:
//
//   - Writes land in a spool on local disk, fsynced, and are acknowledged
//     at once. An uploader moves them to the bucket in the background,
//     with retries and backoff, multipart for large objects, resuming
//     where a failed upload stopped. A spool file is deleted only once
//     the bucket confirmed it.
//   - Unfinished uploads (everything under a repository's _uploads/, and
//     multipart uploads) never leave the spool: they are a push's
//     scratch space, most of its requests, and the commit that hung.
//   - Reads are served from the spool when the object is there (the pull
//     right after a push), otherwise from the bucket with a deadline per
//     attempt and retries.
//   - A blob whose bucket reads keep timing out is stuck. While core has a
//     push running (PushStarted) it is reported missing, so Docker uploads
//     it again into the spool, which repairs it. Pulls never see that:
//     outside a push it gets the full deadlines and a plain error.
//
// The registry signs its requests with keys derived for the gateway
// (Distribution only knows those); the bucket's own keys stay in the
// daemon.
//
// # Health
//
// Every bucket call the gateway makes anyway is an outcome in a window
// (health.go): failures and slow calls make the store degraded or
// failing, a full spool makes it refuse writes. Nothing polls a healthy
// store; while it is unhealthy and idle, one probe at a time with backoff
// notices when it recovers. OnChange reports transitions (a platform
// alert in core).
package gateway

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/NordTempo/tempoDock/internal/s3"
)

// Options configure a Gateway.
type Options struct {
	// Dir is the spool's directory, on the control plane's disk.
	Dir string
	// MaxBytes caps the spool; 0 is the smaller of 20 GB and a quarter of
	// Dir's filesystem.
	MaxBytes int64
	// MinFree is the share of Dir's filesystem that must stay free after
	// a write; 0 is 10%. Past it or MaxBytes, writes are refused.
	MinFree float64
	// AccessKey and Secret are what the registry signs its requests with
	// (Keys derives them).
	AccessKey, Secret string
	// OnChange is called, in its own goroutine, when Status's state or
	// refusal changes.
	OnChange func(Status)
	// Log receives failures and state changes; nil discards them.
	Log *slog.Logger
	// HTTP reaches the bucket; nil is the s3 package's default.
	HTTP *http.Client
	// Timing overrides the deadlines and backoffs; zero fields are the
	// defaults (tests shorten them).
	Timing Timing
}

// Timing is the gateway's deadlines, retries and backoffs.
type Timing struct {
	// ReadTimeout bounds a read attempt until the bucket's first byte;
	// ReadAttempts is how many a read gets. During a push, a read the push
	// writes again when told it's missing (reuploadable) gets PushAttempts
	// of PushReadTimeout instead: Docker waits about 30 s for an answer,
	// and a needless upload into the spool costs less than a failed push.
	ReadTimeout, PushReadTimeout time.Duration
	ReadAttempts, PushAttempts   int
	// Stall cancels a request whose body hasn't moved for that long, a
	// read's or an upload's.
	Stall time.Duration
	// MetaTimeout bounds a listing or delete attempt; MetaAttempts is how
	// many each gets.
	MetaTimeout  time.Duration
	MetaAttempts int
	// RetryMin and RetryMax bound the uploader's backoff per object.
	RetryMin, RetryMax time.Duration
	// ProbeMin and ProbeMax bound the probe's backoff while unhealthy and
	// idle.
	ProbeMin, ProbeMax time.Duration
	// Slow is when a call counts as slow for health; StuckFor how long a
	// blob stays stuck without a new attempt clearing it; Window how far
	// back health looks; Waiting is when an object waiting to upload
	// makes the store degraded.
	Slow, StuckFor, Window, Waiting time.Duration
	// Workers is how many objects upload at once.
	Workers int
}

func (t Timing) withDefaults() Timing {
	def := func(v *time.Duration, d time.Duration) {
		if *v == 0 {
			*v = d
		}
	}
	defInt := func(v *int, d int) {
		if *v == 0 {
			*v = d
		}
	}
	def(&t.ReadTimeout, 20*time.Second)
	defInt(&t.ReadAttempts, 3)
	def(&t.PushReadTimeout, 5*time.Second)
	defInt(&t.PushAttempts, 1)
	def(&t.Stall, time.Minute)
	def(&t.MetaTimeout, 30*time.Second)
	defInt(&t.MetaAttempts, 3)
	def(&t.RetryMin, 5*time.Second)
	def(&t.RetryMax, 2*time.Minute)
	def(&t.ProbeMin, time.Minute)
	def(&t.ProbeMax, 5*time.Minute)
	def(&t.Slow, 10*time.Second)
	def(&t.StuckFor, 10*time.Minute)
	def(&t.Window, 10*time.Minute)
	def(&t.Waiting, 5*time.Minute)
	defInt(&t.Workers, 4)
	return t
}

// Gateway is the registry's storage gateway; an http.Handler for the
// registry's S3 driver. Open makes one.
type Gateway struct {
	opt Options
	t   Timing
	log *slog.Logger
	now func() time.Time

	mu     sync.Mutex
	bucket *s3.Client // nil until SetBucket
	sp     spool
	up     uploader
	health health
	stuck  map[string]time.Time
	// pushes counts the pushes core has running (PushStarted); strict
	// turns the stuck-blob answer off whatever it says (garbage
	// collection).
	pushes int
	strict bool
	// reported is the last state OnChange was told; zero until the first
	// (a new daemon doesn't know what the last one reported).
	reported stateKey
}

// Open opens the spool in opt.Dir: unfinished uploads from before are
// removed, and objects that were waiting to upload are queued again,
// for when SetBucket gives the gateway its bucket.
func Open(opt Options) (*Gateway, error) {
	g := &Gateway{opt: opt, t: opt.Timing.withDefaults(), log: opt.Log, now: time.Now, stuck: map[string]time.Time{}}
	if g.log == nil {
		g.log = slog.New(slog.DiscardHandler)
	}
	if err := g.sp.open(opt.Dir, opt.MaxBytes, opt.MinFree); err != nil {
		return nil, err
	}
	for key, e := range g.sp.idx {
		g.up.enqueue(key, e, g.now())
	}
	return g, nil
}

// SetBucket points the gateway at its bucket (nil: none, every request
// fails) and starts uploading what waits. New keys for the same bucket
// apply to the next request.
func (g *Gateway) SetBucket(b *s3.Bucket) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if b == nil {
		g.bucket = nil
		return
	}
	g.bucket = &s3.Client{Bucket: *b, HTTP: g.opt.HTTP}
	g.kickLocked()
}

// PushStarted tells the gateway a push to the registry runs until done is
// called: meanwhile a stuck blob is reported missing so the push uploads
// it again.
func (g *Gateway) PushStarted() (done func()) {
	g.mu.Lock()
	g.pushes++
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.pushes--
			g.mu.Unlock()
		})
	}
}

// SetStrict turns the stuck-blob answer off while garbage collection
// runs: a blob reported missing there would look unreferenced.
func (g *Gateway) SetStrict(on bool) {
	g.mu.Lock()
	g.strict = on
	g.mu.Unlock()
}

// client is the bucket's client, or an error when there is none.
func (g *Gateway) client() (*s3.Client, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.bucket == nil {
		return nil, errNoBucket
	}
	return g.bucket, nil
}

var errNoBucket = &s3Error{http.StatusServiceUnavailable, "ServiceUnavailable", "the registry's storage is not a bucket"}

// ServeHTTP implements the S3 requests Distribution's S3 driver makes,
// path style: /<bucket>/<key>. The bucket name is ignored.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	payload, err := s3.Verify(r, g.opt.AccessKey, g.opt.Secret, g.now())
	if err != nil {
		writeError(w, r, &s3Error{http.StatusForbidden, "SignatureDoesNotMatch", err.Error()})
		return
	}
	_, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if key != "" && !validKey(key) {
		writeError(w, r, &s3Error{http.StatusBadRequest, "InvalidArgument", "invalid key"})
		return
	}
	q := r.URL.Query()
	body := &checkedBody{r: r.Body, want: payload}
	r.Body = body
	switch {
	case key == "" && r.Method == http.MethodGet && q.Has("uploads"):
		err = g.listUploads(w, q.Get("prefix"))
	case key == "" && r.Method == http.MethodGet:
		err = g.list(r.Context(), w, q)
	case key == "" && r.Method == http.MethodPost && q.Has("delete"):
		err = g.deleteObjects(r.Context(), w, r.Body)
	case key == "":
		err = &s3Error{http.StatusNotImplemented, "NotImplemented", "not supported by the registry's gateway"}
	case r.Method == http.MethodHead:
		err = g.get(r.Context(), w, r, key, true)
	case r.Method == http.MethodGet && q.Has("uploadId"):
		err = g.listParts(w, key, q.Get("uploadId"))
	case r.Method == http.MethodGet:
		err = g.get(r.Context(), w, r, key, false)
	case r.Method == http.MethodPut && q.Has("uploadId") && r.Header.Get("X-Amz-Copy-Source") != "":
		err = g.uploadPartCopy(r.Context(), w, r, key, q)
	case r.Method == http.MethodPut && q.Has("uploadId"):
		err = g.uploadPart(w, r, key, q)
	case r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source") != "":
		err = g.copyObject(r.Context(), w, r, key)
	case r.Method == http.MethodPut:
		err = g.putObject(w, r, key)
	case r.Method == http.MethodPost && q.Has("uploads"):
		err = g.createUpload(w, key)
	case r.Method == http.MethodPost && q.Has("uploadId"):
		err = g.completeUpload(w, r, key, q.Get("uploadId"))
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		err = g.abortUpload(w, q.Get("uploadId"))
	case r.Method == http.MethodDelete:
		err = g.deleteKeys(r.Context(), []string{key})
		if err == nil {
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		err = &s3Error{http.StatusNotImplemented, "NotImplemented", "not supported by the registry's gateway"}
	}
	if err != nil {
		writeError(w, r, err)
	}
}

// validKey refuses keys that could leave the spool's directory.
func validKey(key string) bool {
	if strings.HasPrefix(key, "/") || strings.ContainsRune(key, 0) || strings.Contains(key, "\\") {
		return false
	}
	for seg := range strings.SplitSeq(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// copySource is the key an x-amz-copy-source header names
// ("<bucket>/<key>", URL-escaped).
func copySource(h string) (string, error) {
	src, err := url.PathUnescape(h)
	if err != nil {
		return "", &s3Error{http.StatusBadRequest, "InvalidArgument", "bad copy source"}
	}
	src, _, _ = strings.Cut(src, "?") // a versionId
	_, key, ok := strings.Cut(strings.TrimPrefix(src, "/"), "/")
	if !ok || !validKey(key) {
		return "", &s3Error{http.StatusBadRequest, "InvalidArgument", "bad copy source"}
	}
	return key, nil
}

// s3Error is an error answer in S3's XML.
type s3Error struct {
	status        int
	code, message string
}

func (e *s3Error) Error() string { return e.code + ": " + e.message }

// errNoSuchKey is the answer for a missing object.
var errNoSuchKey = &s3Error{http.StatusNotFound, "NoSuchKey", "The specified key does not exist."}

// writeError answers err: an *s3Error as is, a bucket's *s3.Error with
// its status and code, a full spool or anything else as 503 so the
// registry's driver retries and then fails the request.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	e, ok := errors.AsType[*s3Error](err)
	if !ok {
		if se, isS3 := errors.AsType[*s3.Error](err); isS3 && se.Status/100 == 4 {
			e = &s3Error{se.Status, cmpStr(se.Code, http.StatusText(se.Status)), se.Message}
		} else {
			e = &s3Error{http.StatusServiceUnavailable, "ServiceUnavailable", err.Error()}
		}
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(e.status)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(e.status)
	_ = xml.NewEncoder(w).Encode(struct {
		XMLName xml.Name `xml:"Error"`
		Code    string
		Message string
	}{Code: e.code, Message: e.message})
}

// writeXML answers 200 with v as XML.
func writeXML(w http.ResponseWriter, v any) error {
	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, xml.Header)
	return xml.NewEncoder(w).Encode(v)
}

func cmpStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// diskStat is the free (for unprivileged writers) and total bytes of
// path's filesystem.
func diskStat(path string) (free, total int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	if st.Bsize <= 0 || st.Blocks > math.MaxInt64 || st.Bavail > st.Blocks {
		return 0, 0, fmt.Errorf("statfs %s: implausible block counts", path)
	}
	blocks, avail := int64(st.Blocks), int64(st.Bavail)
	return avail * st.Bsize, blocks * st.Bsize, nil
}

// Close stops the uploader's and the probe's timers and waits for
// uploads in flight to stop. The spool stays for the next Open.
func (g *Gateway) Close() {
	g.mu.Lock()
	g.up.closed = true
	g.up.stopTimersLocked()
	g.health.stopProbeLocked()
	for _, e := range g.sp.idx {
		if e.cancel != nil {
			e.cancel()
		}
	}
	g.mu.Unlock()
	g.up.wg.Wait()
}

// ctxErr turns a canceled request into an error the registry sees as one.
func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return &s3Error{http.StatusServiceUnavailable, "RequestCanceled", "the request was canceled"}
	}
	return err
}
