package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The spool is a directory on the control plane's disk:
//
//	obj/<key>     objects waiting to upload to the bucket
//	tmp/<key>     unfinished uploads' objects (under _uploads/), never uploaded
//	mp/<id>/<n>   parts of multipart uploads, never uploaded
//	in/           files being written, renamed into obj/ or tmp/ when complete
//
// A file in obj/ is deleted only after the bucket confirmed its upload,
// so the directory is the uploader's queue across restarts. Open empties
// tmp/, mp/ and in/: an upload doesn't survive the daemon's restart (the
// registry's connections to the gateway broke with it, and Docker starts
// the layer over).

// entry is an object in the spool.
type entry struct {
	size int64
	mod  time.Time
	etag string
	// gen counts the object's writes: an upload finished for an older one
	// leaves the file for the next.
	gen uint64
	// local: an unfinished upload's object, never uploaded.
	local bool
	// queued is when it started waiting to upload.
	queued time.Time

	// The uploader's state (upload.go).
	busy     chan struct{} // closed when the running upload ends; nil when none runs
	cancel   func()
	failures int
	retryAt  time.Time
	resume   *resume
}

// spool is the gateway's local store; its fields are guarded by
// Gateway.mu.
type spool struct {
	dir     string
	max     int64
	minFree float64
	idx     map[string]*entry
	// used is the bytes the spool holds and has reserved for writes in
	// progress (objects and parts).
	used    int64
	uploads map[string]*mpUpload
	// refusing is why writes are refused ("spool" full or "disk" nearly
	// full), "" when they aren't; since when.
	refusing      string
	refusingSince time.Time
}

// open prepares dir and loads what waits to upload.
func (s *spool) open(dir string, max int64, minFree float64) error {
	s.dir, s.idx, s.uploads = dir, map[string]*entry{}, map[string]*mpUpload{}
	for _, sub := range []string{"tmp", "mp", "in"} {
		if err := os.RemoveAll(filepath.Join(dir, sub)); err != nil {
			return fmt.Errorf("registry spool: %w", err)
		}
	}
	for _, sub := range []string{"obj", "tmp", "mp", "in"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return fmt.Errorf("registry spool: %w", err)
		}
	}
	_, total, err := diskStat(dir)
	if err != nil {
		return err
	}
	if max <= 0 {
		max = min(20<<30, total/4)
	}
	if minFree <= 0 {
		minFree = 0.10
	}
	s.max, s.minFree = max, minFree
	root := filepath.Join(dir, "obj")
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		key := filepath.ToSlash(rel)
		s.idx[key] = &entry{size: info.Size(), mod: info.ModTime(), etag: weakETag(info.Size(), info.ModTime()), queued: info.ModTime()}
		s.used += info.Size()
		return nil
	})
}

// isLocal reports whether key is an unfinished upload's (Distribution's
// <repository>/_uploads/<id>/...), which stays in the spool.
func isLocal(key string) bool { return strings.Contains(key, "/_uploads/") }

// path is key's file.
func (s *spool) path(key string, local bool) string {
	sub := "obj"
	if local {
		sub = "tmp"
	}
	return filepath.Join(s.dir, sub, filepath.FromSlash(key))
}

// reserveLocked makes room for n more bytes, or says why there is none:
// the spool is at its cap, or the write would leave the disk with less
// than minFree. now stamps the refusal.
func (s *spool) reserveLocked(n int64, now time.Time) error {
	free, total, err := diskStat(s.dir)
	reason := ""
	switch {
	case err != nil:
		return err
	case s.used+n > s.max:
		reason = "spool"
	case float64(free-n) < s.minFree*float64(total):
		reason = "disk"
	}
	if reason != "" {
		if s.refusing != reason {
			s.refusing, s.refusingSince = reason, now
		}
		return errFull(reason)
	}
	if s.refusing != "" {
		s.refusing, s.refusingSince = "", time.Time{}
	}
	s.used += n
	return nil
}

// errFull is the answer to a write the spool has no room for. The
// registry's driver retries it and then fails the push; core's
// diagnosis says why (Status.Refusing).
func errFull(reason string) error {
	msg := "the registry's spool is full: the bucket is too slow to take what was pushed"
	if reason == "disk" {
		msg = "the control plane's disk is nearly full, so the registry's spool takes no more"
	}
	return &s3Error{503, "SlowDown", msg}
}

// incoming creates a file in in/ for a write in progress.
func (s *spool) incoming() (*os.File, error) {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return os.OpenFile(filepath.Join(s.dir, "in", hex.EncodeToString(b[:])), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
}

// commitLocked moves the complete file tmp to key's place (size bytes,
// already reserved) and returns its entry. A previous object at key is
// replaced; an upload of it in flight finishes for the old generation.
func (s *spool) commitLocked(key, tmp string, size int64, etag string, now time.Time) (*entry, error) {
	local := isLocal(key)
	dst := s.path(key, local)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return nil, err
	}
	e := s.idx[key]
	if e == nil {
		e = &entry{local: local, queued: now}
		s.idx[key] = e
	} else {
		s.used -= e.size
	}
	e.size, e.mod, e.etag = size, now, etag
	e.gen++
	return e, nil
}

// removeLocked deletes key's file and entry; it returns the running
// upload's done channel, already canceled, when one runs (nil
// otherwise).
func (s *spool) removeLocked(key string) (wait chan struct{}) {
	e := s.idx[key]
	if e == nil {
		return nil
	}
	delete(s.idx, key)
	s.used -= e.size
	_ = os.Remove(s.path(key, e.local))
	s.pruneDirs(filepath.Dir(s.path(key, e.local)))
	if e.cancel != nil {
		e.cancel()
	}
	return e.busy
}

// pruneDirs removes dir and its parents while empty, up to obj/ or tmp/.
func (s *spool) pruneDirs(dir string) {
	stop := []string{filepath.Join(s.dir, "obj"), filepath.Join(s.dir, "tmp")}
	for !slices.Contains(stop, dir) && strings.HasPrefix(dir, s.dir) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// localKeys are the spool's keys under prefix, sorted.
func (s *spool) localKeys(prefix string) []string {
	var keys []string
	for k := range s.idx {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

// syncDir fsyncs dir, so a rename into it survives a crash.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	_ = d.Close()
	return err
}

// weakETag is an ETag from an object's size and time, for objects whose
// content wasn't hashed (or was hashed before a restart).
func weakETag(size int64, mod time.Time) string {
	return fmt.Sprintf(`"%x-%x"`, size, mod.UnixNano())
}

// write stores body (n bytes) as key: in in/ first, fsynced, then renamed
// into place. The bytes are reserved before anything is written, so a
// full spool refuses the write at once. It returns the new entry's ETag.
func (g *Gateway) write(ctx context.Context, key string, n int64, fill func(*os.File) (etag string, err error)) (string, error) {
	g.mu.Lock()
	err := g.sp.reserveLocked(n, g.now())
	g.mu.Unlock()
	g.notify()
	if err != nil {
		return "", err
	}
	f, err := g.sp.incoming()
	if err != nil {
		g.release(n)
		return "", err
	}
	tmp := f.Name()
	etag, err := fill(f)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && ctx.Err() != nil {
		err = ctxErr(ctx, ctx.Err())
	}
	if err != nil {
		_ = os.Remove(tmp)
		g.release(n)
		return "", err
	}
	g.mu.Lock()
	e, err := g.sp.commitLocked(key, tmp, n, etag, g.now())
	if err != nil {
		g.sp.used -= n
		g.mu.Unlock()
		_ = os.Remove(tmp)
		return "", err
	}
	delete(g.stuck, key) // the spool's copy is what reads get now
	if !e.local {
		g.up.enqueue(key, e, g.now())
		g.kickLocked()
	}
	g.mu.Unlock()
	if !isLocal(key) {
		// The registry is told it's stored only once the rename is durable.
		if err := syncDir(filepath.Dir(g.sp.path(key, false))); err != nil {
			return "", err
		}
	}
	return etag, nil
}

// release gives back n reserved bytes.
func (g *Gateway) release(n int64) {
	g.mu.Lock()
	g.sp.used -= n
	g.mu.Unlock()
}
