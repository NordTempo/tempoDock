package walarchive

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/s3"
)

// Port is where the archiver listens on the environment network.
const Port = 8432

// MaxObject is the largest file the archiver takes: a segment of the
// template's 16 MB, with room for a larger one.
const MaxObject = 64 << 20

// Health thresholds: the archiver is unhealthy while a finished segment
// has waited longer than MaxWait, or the WAL waiting to be archived takes
// more than MaxWaitingBytes or MaxWaitingShare of the volume. Postgres
// keeps every unarchived segment, so a stalled archive grows pg_wal until
// the disk is full; these fire long before.
const (
	MaxWait         = 5 * time.Minute
	MaxWaitingBytes = 1 << 30
	MaxWaitingShare = 0.10
)

// Status is what the archiver reports (GET /status), and why it is
// unhealthy (GET /health answers 503 with Problem).
type Status struct {
	Healthy bool   `json:"healthy"`
	Problem string `json:"problem,omitempty"`
	// Through is when the newest archived segment was last written:
	// every commit until then is in the archive. Nil before the first.
	Through *time.Time `json:"through,omitempty"`
	Last    string     `json:"last,omitempty"`
	// Waiting are the finished files Postgres hasn't archived yet, their
	// size and when the oldest finished.
	Waiting       int        `json:"waiting"`
	WaitingBytes  int64      `json:"waiting_bytes"`
	OldestWaiting *time.Time `json:"oldest_waiting,omitempty"`
	// DiskBytes and DiskFree are the database volume's file system.
	DiskBytes int64 `json:"disk_bytes,omitempty"`
	DiskFree  int64 `json:"disk_free,omitempty"`
	// LastError is the last upload's error, cleared by the next success.
	LastError   string     `json:"last_error,omitempty"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
}

// Archiver uploads what archive_command hands it (wal-push) to the
// archive, and reports how far behind Postgres's archiving is.
type Archiver struct {
	cfg    Config
	s3     *s3.Client
	pgdata string
	now    func() time.Time

	// put serializes uploads: Postgres archives one file at a time.
	put sync.Mutex

	mu      sync.Mutex
	through time.Time
	last    string
	lastErr string
	errAt   time.Time
}

// NewArchiver returns the archiver of cfg's archive for the data
// directory pgdata (mounted read-only).
func NewArchiver(cfg Config, pgdata string) *Archiver {
	return &Archiver{cfg: cfg, s3: &s3.Client{Bucket: cfg.Bucket}, pgdata: pgdata, now: time.Now}
}

// Load reads the archive once to know the newest archived segment, so
// the status is right after a restart. A failure leaves it unknown.
func (a *Archiver) Load(ctx context.Context) error {
	var newest Entry
	err := a.s3.List(ctx, a.cfg.Prefix, func(objs []s3.Object) error {
		for _, o := range objs {
			if file, mtime, ok := ParseKey(a.cfg.Prefix, o.Key); ok && IsSegment(file) && mtime.After(newest.MTime) {
				newest = Entry{File: file, MTime: mtime}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.mu.Lock()
	if newest.MTime.After(a.through) {
		a.through, a.last = newest.MTime, newest.File
	}
	a.mu.Unlock()
	return nil
}

// Handler serves PUT /wal/<file>, GET /health and GET /status.
func (a *Archiver) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /wal/{file}", a.handlePut)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		st := a.Status()
		if !st.Healthy {
			http.Error(w, st.Problem, http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(a.Status())
	})
	return mux
}

// handlePut archives one file. The answer is 200 only once the object
// is stored, so Postgres keeps the file until it is off the server.
func (a *Archiver) handlePut(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	if !ValidName(file) {
		http.Error(w, file+" is not a WAL file name", http.StatusBadRequest)
		return
	}
	ms, err := strconv.ParseInt(r.Header.Get("X-Dock-Mtime"), 10, 64)
	if err != nil {
		http.Error(w, "X-Dock-Mtime must be the file's mtime in Unix milliseconds", http.StatusBadRequest)
		return
	}
	if r.ContentLength < 0 || r.ContentLength > MaxObject {
		http.Error(w, fmt.Sprintf("%s must come with its length, at most %d bytes", file, MaxObject), http.StatusRequestEntityTooLarge)
		return
	}
	mtime := time.UnixMilli(ms)
	if err := a.Put(r.Context(), file, mtime, io.LimitReader(r.Body, r.ContentLength)); err != nil {
		slog.Warn("archive", "file", file, "err", err)
		http.Error(w, "dock-wal: "+err.Error(), http.StatusBadGateway)
		return
	}
	_, _ = io.WriteString(w, "ok\n")
}

// Put stores file, last written at mtime, from r.
func (a *Archiver) Put(ctx context.Context, file string, mtime time.Time, r io.Reader) error {
	a.put.Lock()
	defer a.put.Unlock()
	var buf bytes.Buffer
	err := Seal(&buf, r, a.cfg.Key, file)
	if err == nil {
		err = a.s3.Put(ctx, Key(a.cfg.Prefix, file, mtime), &buf, int64(buf.Len()))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.lastErr, a.errAt = err.Error(), a.now()
		return err
	}
	a.lastErr = ""
	if IsSegment(file) && mtime.After(a.through) {
		a.through, a.last = mtime, file
	}
	return nil
}

// Status reads pg_wal and says how the archiving does.
func (a *Archiver) Status() Status {
	st := Status{Healthy: true}
	a.mu.Lock()
	if !a.through.IsZero() {
		t := a.through
		st.Through, st.Last = &t, a.last
	}
	if a.lastErr != "" {
		t := a.errAt
		st.LastError, st.LastErrorAt = a.lastErr, &t
	}
	a.mu.Unlock()
	if err := waiting(a.pgdata, &st); err != nil {
		st.Healthy, st.Problem = false, "can't read the database's pg_wal: "+err.Error()
		return st
	}
	st.DiskBytes, st.DiskFree = diskSpace(a.pgdata)
	st.Healthy, st.Problem = judge(st, a.now())
	return st
}

// waiting counts the files Postgres marked ready to archive
// (pg_wal/archive_status/<file>.ready) and their sizes.
func waiting(pgdata string, st *Status) error {
	dir := filepath.Join(pgdata, "pg_wal")
	ents, err := os.ReadDir(filepath.Join(dir, "archive_status"))
	if err != nil {
		return err
	}
	for _, e := range ents {
		file, ok := strings.CutSuffix(e.Name(), ".ready")
		if !ok {
			continue
		}
		st.Waiting++
		if info, err := e.Info(); err == nil {
			if t := info.ModTime(); st.OldestWaiting == nil || t.Before(*st.OldestWaiting) {
				st.OldestWaiting = &t
			}
		}
		if info, err := os.Stat(filepath.Join(dir, file)); err == nil {
			st.WaitingBytes += info.Size()
		}
	}
	return nil
}

// judge says whether st is healthy and, if not, why, in words an alert
// can carry.
func judge(st Status, now time.Time) (bool, string) {
	var problems []string
	if st.OldestWaiting != nil && now.Sub(*st.OldestWaiting) > MaxWait {
		problems = append(problems, fmt.Sprintf("%d WAL files wait to be archived, the oldest for %s", st.Waiting, now.Sub(*st.OldestWaiting).Round(time.Minute)))
	}
	if st.WaitingBytes > MaxWaitingBytes || (st.DiskBytes > 0 && float64(st.WaitingBytes) > MaxWaitingShare*float64(st.DiskBytes)) {
		problems = append(problems, fmt.Sprintf("unarchived WAL takes %s of the database's disk (%s free)", humanBytes(st.WaitingBytes), humanBytes(st.DiskFree)))
	}
	if len(problems) == 0 {
		return true, ""
	}
	p := strings.Join(problems, "; ")
	if st.LastError != "" {
		p += "; the last upload failed: " + st.LastError
	}
	return false, p
}

// humanBytes is a size in words.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", cmp.Or(n>>10, 0))
}
