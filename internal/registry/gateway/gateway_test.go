package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/s3"
)

const (
	blobKey   = "dock-registry/docker/registry/v2/blobs/sha256/ab/abcdef/data"
	linkKey   = "dock-registry/docker/registry/v2/repositories/dock/p1/_layers/sha256/abcdef/link"
	uploadKey = "dock-registry/docker/registry/v2/repositories/dock/p1/_uploads/u1/data"
)

// harness is a gateway in front of a fake bucket, and a client signing
// like the registry does.
type harness struct {
	t       *testing.T
	g       *Gateway
	bucket  *fakeS3
	reg     *s3.Client
	dir     string
	mu      sync.Mutex
	changes []Status
}

func newHarness(t *testing.T, mut ...func(*Options)) *harness {
	t.Helper()
	h := &harness{t: t, bucket: newFakeS3(t), dir: t.TempDir()}
	opt := Options{
		Dir: h.dir, MaxBytes: 1 << 30, AccessKey: "registry", Secret: "s3cret",
		OnChange: func(s Status) { h.mu.Lock(); h.changes = append(h.changes, s); h.mu.Unlock() },
		Timing: Timing{
			ReadTimeout: 200 * time.Millisecond, PushReadTimeout: 100 * time.Millisecond, Stall: 300 * time.Millisecond, MetaTimeout: 300 * time.Millisecond,
			RetryMin: 30 * time.Millisecond, RetryMax: 100 * time.Millisecond, ProbeMin: 30 * time.Millisecond, ProbeMax: 60 * time.Millisecond,
			Slow: 150 * time.Millisecond, Waiting: time.Hour,
		},
	}
	for _, m := range mut {
		m(&opt)
	}
	h.open(opt)
	return h
}

// open (re)opens the gateway on h.dir.
func (h *harness) open(opt Options) {
	g, err := Open(opt)
	if err != nil {
		h.t.Fatal(err)
	}
	g.SetBucket(&s3.Bucket{Endpoint: h.bucket.srv.URL, Name: "real", PathStyle: true, AccessKeyID: "k", SecretAccessKey: "s"})
	srv := httptest.NewServer(g)
	h.t.Cleanup(func() { srv.Close(); g.Close() })
	h.g = g
	h.reg = &s3.Client{Bucket: s3.Bucket{Endpoint: srv.URL, Name: "dock", PathStyle: true, AccessKeyID: "registry", SecretAccessKey: "s3cret"}}
}

func (h *harness) put(key, body string) error {
	return h.reg.Put(context.Background(), key, strings.NewReader(body), int64(len(body)))
}

func (h *harness) read(key string) (string, error) {
	resp, err := h.reg.Open(context.Background(), key, "")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// eventually fails the test unless ok turns true within 15 s: far above
// what any wait here takes (well under a second, 1.4 s on a loaded shared
// runner), and free when ok turns true early.
func (h *harness) eventually(what string, ok func() bool) {
	h.t.Helper()
	defer func(start time.Time) { h.t.Logf("%s after %v", what, time.Since(start)) }(time.Now())
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	h.t.Fatalf("timed out waiting for %s; status %+v", what, h.g.Status())
}

func (h *harness) lastChange() Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.changes) == 0 {
		return Status{}
	}
	return h.changes[len(h.changes)-1]
}

// A push while the bucket hangs succeeds from the spool, reads come from
// the spool, the store turns degraded, and once the bucket answers again
// the object reaches it, leaves the spool and the alert state resolves.
func TestWritesLandLocallyWhileTheBucketHangs(t *testing.T) {
	h := newHarness(t)
	h.bucket.set(func(f *fakeS3) { f.down = true })

	// The fake bucket answers nothing while down, so a put that waited on
	// it would never succeed: acknowledged without the bucket having it
	// means it landed in the spool.
	if err := h.put(blobKey, "layer"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.bucket.object(blobKey); ok {
		t.Fatal("the bucket has the object though it was down")
	}
	if got, err := h.read(blobKey); err != nil || got != "layer" {
		t.Fatalf("read from the spool = %q, %v", got, err)
	}
	h.eventually("degraded", func() bool { return h.lastChange().State != "" && h.lastChange().State != Healthy })
	if st := h.g.Status(); st.Waiting != 1 || st.WaitingBytes != 5 {
		t.Errorf("waiting = %d (%d bytes), want 1 (5)", st.Waiting, st.WaitingBytes)
	}

	h.bucket.set(func(f *fakeS3) { f.down = false })
	h.eventually("the upload", func() bool { b, ok := h.bucket.object(blobKey); return ok && string(b) == "layer" })
	h.eventually("the spool to empty", func() bool { return h.g.Status().Waiting == 0 })
	if _, err := os.Stat(filepath.Join(h.dir, "obj", filepath.FromSlash(blobKey))); !os.IsNotExist(err) {
		t.Errorf("the spool file is still there after the upload: %v", err)
	}
	h.eventually("healthy again", func() bool { return h.lastChange().State == Healthy })
	if got, err := h.read(blobKey); err != nil || got != "layer" {
		t.Errorf("read from the bucket = %q, %v", got, err)
	}
}

// Unfinished uploads never reach the bucket, and their listing and
// deletion (a push's commit) don't ask it.
func TestUnfinishedUploadsStayLocal(t *testing.T) {
	h := newHarness(t)
	if err := h.put(uploadKey, "partial"); err != nil {
		t.Fatal(err)
	}
	h.bucket.set(func(f *fakeS3) { f.down = true })
	// A call that asked the bucket would hang until this deadline; one that
	// doesn't answers at once, however slow the runner.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	page, err := h.reg.ListPage(ctx, s3.ListInput{Prefix: "dock-registry/docker/registry/v2/repositories/dock/p1/_uploads/u1/"})
	if err != nil || len(page.Objects) != 1 {
		t.Fatalf("list of the upload = %+v, %v", page, err)
	}
	if err := h.reg.Delete(ctx, []string{uploadKey}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.bucket.object(uploadKey); ok {
		t.Error("an unfinished upload's object reached the bucket")
	}
	if st := h.g.Status(); st.SpoolBytes != 0 {
		t.Errorf("spool holds %d bytes after the delete", st.SpoolBytes)
	}
}

// A blob the bucket hangs on fails a pull as unavailable, never as
// missing; during a push it is reported missing, a new copy lands in the
// spool and is served from there; garbage collection never sees the
// pretense.
func TestStuckBlobs(t *testing.T) {
	h := newHarness(t)
	h.bucket.set(func(f *fakeS3) { f.objects[blobKey] = []byte("old"); f.stall[blobKey] = true })
	ctx := context.Background()

	_, err := h.reg.Head(ctx, blobKey)
	if err == nil || s3.IsNotFound(err) {
		t.Fatalf("pull's HEAD of a stuck blob = %v, want unavailable", err)
	}
	if st := h.g.Status(); len(st.Stuck) != 1 || st.Stuck[0].Key != blobKey {
		t.Fatalf("stuck = %+v", st.Stuck)
	}

	h.g.SetStrict(true)
	done := h.g.PushStarted()
	if _, err := h.reg.Head(ctx, blobKey); s3.IsNotFound(err) {
		t.Fatal("garbage collection was told a stuck blob is missing")
	}
	h.g.SetStrict(false)
	if _, err := h.reg.Head(ctx, blobKey); !s3.IsNotFound(err) {
		t.Fatalf("push's HEAD of a stuck blob = %v, want missing", err)
	}
	page, err := h.reg.ListPage(ctx, s3.ListInput{Prefix: blobKey, Max: 1})
	if err != nil || len(page.Objects) != 0 {
		t.Fatalf("a push's listing shows the stuck blob: %+v, %v", page, err)
	}
	// A repository's layer link is read first; it gets the same answer.
	h.bucket.set(func(f *fakeS3) { f.objects[linkKey] = []byte("sha256:abcdef"); f.stall[linkKey] = true })
	if _, err := h.read(linkKey); !s3.IsNotFound(err) {
		t.Fatalf("push's read of a stuck layer link = %v, want missing", err)
	}
	for _, k := range []string{blobKey, linkKey} { // what Docker's upload writes
		if err := h.put(k, "new"); err != nil {
			t.Fatal(err)
		}
	}
	done()
	if got, err := h.read(blobKey); err != nil || got != "new" {
		t.Fatalf("read after the new copy = %q, %v", got, err)
	}
	if st := h.g.Status(); len(st.Stuck) != 0 {
		t.Errorf("still stuck after a new copy: %+v", st.Stuck)
	}
	h.bucket.set(func(f *fakeS3) { f.stall[blobKey] = false })
	h.eventually("the repair upload", func() bool { b, _ := h.bucket.object(blobKey); return string(b) == "new" })
}

// A full spool refuses writes and says so; room again clears it.
func TestFullSpoolRefuses(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.MaxBytes = 8 })
	h.bucket.set(func(f *fakeS3) { f.down = true })
	if err := h.put(blobKey, "12345"); err != nil {
		t.Fatal(err)
	}
	err := h.put(linkKey, "6789")
	if e, ok := errors.AsType[*s3.Error](err); !ok || e.Status != http.StatusServiceUnavailable || e.Code != "SlowDown" {
		t.Fatalf("put into a full spool = %v, want 503 SlowDown", err)
	}
	if st := h.g.Status(); st.Refusing != "spool" {
		t.Fatalf("refusing = %q", st.Refusing)
	}
	h.eventually("the refusal reported", func() bool { return h.lastChange().Refusing == "spool" })
	h.bucket.set(func(f *fakeS3) { f.down = false })
	h.eventually("room again", func() bool { return h.g.Status().Refusing == "" })
}

// While a push runs, an object the bucket has keeps its spool file: the
// pull after the push reads it from disk even with the bucket down. A
// write that needs the room drops it, and so does the push's end.
func TestUploadedKeptWhilePushing(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.MaxBytes = 8 })
	spooled := func(key string) bool {
		_, err := os.Stat(filepath.Join(h.dir, "obj", filepath.FromSlash(key)))
		return err == nil
	}
	done := h.g.PushStarted()
	if err := h.put(blobKey, "12345"); err != nil {
		t.Fatal(err)
	}
	h.eventually("the upload", func() bool { _, ok := h.bucket.object(blobKey); return ok && h.g.Status().Waiting == 0 })
	if !spooled(blobKey) {
		t.Fatal("the spool file went while a push runs")
	}
	h.bucket.set(func(f *fakeS3) { f.down = true })
	if got, err := h.read(blobKey); err != nil || got != "12345" {
		t.Fatalf("read with the bucket down = %q, %v; want the spool's copy", got, err)
	}
	h.bucket.set(func(f *fakeS3) { f.down = false })

	if err := h.put(linkKey, "6789"); err != nil { // 9 bytes > MaxBytes with the kept file
		t.Fatalf("a write that needs the kept file's room: %v", err)
	}
	if spooled(blobKey) {
		t.Error("the kept file stayed though the spool needed its room")
	}
	h.eventually("the second upload", func() bool { _, ok := h.bucket.object(linkKey); return ok && h.g.Status().Waiting == 0 })
	done()
	if spooled(linkKey) {
		t.Error("the kept file stayed after the push ended")
	}
}

// What waited to upload when the daemon stopped uploads after the next
// start, and unfinished uploads from before are gone.
func TestUploadsResumeAfterRestart(t *testing.T) {
	h := newHarness(t)
	h.bucket.set(func(f *fakeS3) { f.down = true })
	if err := h.put(blobKey, "layer"); err != nil {
		t.Fatal(err)
	}
	if err := h.put(uploadKey, "partial"); err != nil {
		t.Fatal(err)
	}
	h.g.Close()

	h.bucket.set(func(f *fakeS3) { f.down = false })
	h.open(Options{Dir: h.dir, AccessKey: "registry", Secret: "s3cret", Timing: h.g.t})
	h.eventually("the upload after the restart", func() bool { _, ok := h.bucket.object(blobKey); return ok })
	if _, err := os.Stat(filepath.Join(h.dir, "tmp", filepath.FromSlash(uploadKey))); !os.IsNotExist(err) {
		t.Errorf("an unfinished upload survived the restart: %v", err)
	}
}

// Large objects upload in parts; a failing part retries alone and the
// upload resumes after the parts the bucket has.
func TestLargeObjectsResume(t *testing.T) {
	h := newHarness(t)
	body := bytes.Repeat([]byte("x"), multipartFrom+partSize/2)
	var parts int
	var mu sync.Mutex
	failSecond := true
	h.bucket.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("partNumber") != "" {
			mu.Lock()
			parts++
			fail := failSecond && r.URL.Query().Get("partNumber") == "2"
			failSecond = failSecond && parts < 4 // three tries of part 2 fail, then it works
			mu.Unlock()
			if fail {
				_, _ = io.Copy(io.Discard, r.Body)
				http.Error(w, "<Error><Code>InternalError</Code></Error>", http.StatusInternalServerError)
				return
			}
		}
		h.bucket.ServeHTTP(w, r)
	})
	if err := h.reg.Put(context.Background(), blobKey, bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
	h.eventually("the multipart upload", func() bool { b, ok := h.bucket.object(blobKey); return ok && len(b) == len(body) })
	mu.Lock()
	defer mu.Unlock()
	// 1 and 2 (failing 3 times), backoff, then 2 and 3: part 1 isn't sent again.
	if parts != 6 {
		t.Errorf("sent %d parts, want 6 (part 1 once, part 2 four times, part 3 once)", parts)
	}
}

// Listings merge the spool and the bucket in order, across pages.
func TestListMerges(t *testing.T) {
	h := newHarness(t)
	const root = "dock-registry/docker/registry/v2/repositories/"
	h.bucket.set(func(f *fakeS3) {
		for _, k := range []string{"a/x", "c/x", "c/y", "e"} {
			f.objects[root+k] = []byte("b")
		}
	})
	h.bucket.set(func(f *fakeS3) { f.down = true })
	for _, k := range []string{"b/x", "c/z", "d"} {
		if err := h.put(root+k, "s"); err != nil {
			t.Fatal(err)
		}
	}
	h.bucket.set(func(f *fakeS3) { f.down = false })
	var got []string
	token := ""
	for range 10 {
		page, err := h.reg.ListPage(context.Background(), s3.ListInput{Prefix: root, Delimiter: "/", Token: token, Max: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range page.Objects {
			got = append(got, strings.TrimPrefix(o.Key, root))
		}
		for _, p := range page.Prefixes {
			got = append(got, strings.TrimPrefix(p, root))
		}
		if token = page.Next; token == "" {
			break
		}
	}
	slices.Sort(got) // a page lists its objects and prefixes apart
	if want := "a/ b/ c/ d e"; strings.Join(got, " ") != want {
		t.Errorf("listing = %q, want %q", strings.Join(got, " "), want)
	}
}

// A multipart upload into the spool joins its parts into the object.
func TestMultipartIntoTheSpool(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id, err := h.reg.CreateMultipart(ctx, uploadKey)
	if err != nil {
		t.Fatal(err)
	}
	var parts []s3.Part
	for i, s := range []string{"hello ", "world"} {
		p, err := h.reg.UploadPart(ctx, uploadKey, id, i+1, strings.NewReader(s), int64(len(s)))
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, p)
	}
	if err := h.reg.CompleteMultipart(ctx, uploadKey, id, parts); err != nil {
		t.Fatal(err)
	}
	if got, err := h.read(uploadKey); err != nil || got != "hello world" {
		t.Fatalf("joined = %q, %v", got, err)
	}
	if st := h.g.Status(); st.SpoolBytes != int64(len("hello world")) {
		t.Errorf("spool counts %d bytes, want %d", st.SpoolBytes, len("hello world"))
	}
}

// Requests not signed with the registry's keys are refused.
func TestRefusesOtherKeys(t *testing.T) {
	h := newHarness(t)
	other := &s3.Client{Bucket: h.reg.Bucket}
	other.Bucket.SecretAccessKey = "guess"
	if _, err := other.Head(context.Background(), blobKey); err == nil || s3.IsNotFound(err) {
		t.Fatalf("a wrongly signed request = %v, want refused", err)
	}
}
