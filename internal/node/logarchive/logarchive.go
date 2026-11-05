// Package logarchive keeps the output of removed containers on the server
// that ran them (ADR 0046): one gzip'd JSON lines file per release under
// <data dir>/logs/<service id>/<release id>.jsonl.gz, bounded per service
// in bytes and age.
//
// Each line is {"t","s","x"}: time (RFC 3339), stream (stdout, stderr)
// and text. A release that had several containers gets one gzip member
// per container appended to its file; gzip readers read them as one
// stream. Writing and reading both stream: memory is bounded by a line,
// or by the tail a reader asked for.
//
// The docker runtime writes and reads archives (docker.Runtime's
// ArchiveLogs, ArchivedLogs and PruneLogs); nothing here talks to Docker.
package logarchive

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Dir is where a data dir keeps its log archives.
func Dir(dataDir string) string { return filepath.Join(dataDir, "logs") }

const (
	// ext is an archive's file name suffix, after the release ID.
	ext = ".jsonl.gz"
	// tempPrefix starts a member being written; Prune removes ones a
	// crash left behind once they are tempStale old.
	tempPrefix = ".archive-"
	tempStale  = time.Hour
	// maxText truncates one line's text when it is archived, as the node
	// link does (remote's maxLogText).
	maxText = 64 << 10
	// maxLine is the longest archive line a reader accepts: maxText of
	// text, JSON escaped. A longer one ends the read.
	maxLine = 1 << 20
	// minFreeBytes and minFreePercent are the free space below which no
	// archive is written (ErrLowDisk): log history must never be what
	// fills a server's disk, and on a nearly full one the apps need what
	// is left. A container's archive is a few MB, well inside either.
	minFreeBytes   = 1 << 30
	minFreePercent = 5
)

// ErrLowDisk is Write refusing to archive on a nearly full disk.
var ErrLowDisk = errors.New("the disk is nearly full")

// freeSpace reads a filesystem's free and total bytes (diskFree; tests
// replace it).
var freeSpace = diskFree

// checkFree returns ErrLowDisk (wrapped, with the numbers) when dir's
// filesystem has less than minFreeBytes or minFreePercent free. Unknown
// free space doesn't stop a write.
func checkFree(dir string) error {
	free, total, ok := freeSpace(dir)
	if !ok || total == 0 {
		return nil
	}
	if free < minFreeBytes || free*100 < total*minFreePercent {
		return fmt.Errorf("%w (%d MB of %d MB free): logs aren't kept below 1 GB or 5%% free; free some space or prune", ErrLowDisk, free>>20, total>>20)
	}
	return nil
}

// mu serialises committing members and applying limits, so an archive
// being appended to is never removed halfway and two members of one
// release don't interleave. A process has one data dir.
var mu sync.Mutex

// record is one archived line.
type record struct {
	T time.Time `json:"t"`
	S string    `json:"s"`
	X string    `json:"x"`
}

// validID reports whether s is a decimal ID as dock writes them (no sign,
// no leading zero), so it can't name anything but one path element.
func validID(s string) bool {
	n, err := strconv.ParseUint(s, 10, 64)
	return err == nil && strconv.FormatUint(n, 10) == s
}

// path is a release's archive under root, or an error for IDs that aren't.
func path(root, service, release string) (string, error) {
	if !validID(service) || !validID(release) {
		return "", fmt.Errorf("log archive: service %q and release %q must be numeric IDs", service, release)
	}
	return filepath.Join(root, service, release+ext), nil
}

// Write appends one gzip member to the release's archive under root, with
// the lines fill adds, then applies limits to the service's archives. The
// member is written to a temporary file first and renamed into place (or
// appended to the release's file), so a reader never sees half of one
// unless the process dies while appending. It returns the compressed
// bytes added. On a nearly full disk nothing is written (ErrLowDisk);
// a write that fails partway (a disk filling meanwhile) leaves nothing.
func Write(root, service, release string, limits node.LogLimits, fill func(add func(node.LogLine) error) error) (int64, error) {
	target, err := path(root, service, release)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return 0, err
	}
	if err := checkFree(root); err != nil {
		return 0, err
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(dir, tempPrefix+"*")
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	bw := bufio.NewWriterSize(tmp, 64<<10)
	gz, err := gzip.NewWriterLevel(bw, gzip.BestSpeed)
	if err != nil {
		return 0, err
	}
	enc := json.NewEncoder(gz)
	enc.SetEscapeHTML(false)
	err = fill(func(l node.LogLine) error {
		return enc.Encode(record{T: l.Time, S: l.Stream, X: truncate(l.Text, maxText)})
	})
	if err != nil {
		return 0, err
	}
	if err := gz.Close(); err != nil {
		return 0, err
	}
	if err := bw.Flush(); err != nil {
		return 0, err
	}
	size, err := tmp.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}

	mu.Lock()
	defer mu.Unlock()
	if err := commit(tmp, target); err != nil {
		return 0, err
	}
	now := time.Now()
	if _, err := enforce(dir, limits, now); err != nil {
		return size, err
	}
	if _, err := enforceTotal(root, limits.MaxTotalBytes); err != nil {
		return size, err
	}
	return size, nil
}

// commit makes the member in tmp part of target: a rename when target is
// new, else an append (mu held). A failed append leaves target as it was.
func commit(tmp *os.File, target string) error {
	if _, err := os.Stat(target); errors.Is(err, fs.ErrNotExist) {
		return os.Rename(tmp.Name(), target)
	} else if err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	before, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		_ = f.Close()
		return err
	}
	if _, err := copyMember(f, tmp); err != nil {
		// Half a member (a disk filling meanwhile) would end every read
		// there, hiding the members appended after it: cut it off.
		_ = f.Truncate(before)
		_ = f.Close()
		return err
	}
	return f.Close()
}

// copyMember appends a member to an archive (io.Copy; tests replace it).
var copyMember = io.Copy

// archiveFile is one release's archive: its name in a service's
// directory, or <service>/<name> under the root for enforceTotal.
type archiveFile struct {
	name  string
	size  int64
	mtime time.Time
}

// enforce removes a service directory's archives past limits: those last
// written before now-MaxAge, then the oldest while the rest are over
// MaxBytes (mu held). Stale temporary files go too.
func enforce(dir string, limits node.LogLimits, now time.Time) (node.PruneResult, error) {
	var out node.PruneResult
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	files := make([]archiveFile, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		name := e.Name()
		switch {
		case strings.HasPrefix(name, tempPrefix):
			if now.Sub(info.ModTime()) > tempStale {
				_ = os.Remove(filepath.Join(dir, name))
			}
		case strings.HasSuffix(name, ext) && validID(strings.TrimSuffix(name, ext)):
			files = append(files, archiveFile{name: name, size: info.Size(), mtime: info.ModTime()})
		}
	}
	// Newest first; what doesn't fit, or is too old, goes.
	slices.SortFunc(files, func(a, b archiveFile) int { return b.mtime.Compare(a.mtime) })
	var total int64
	var errs []error
	for _, f := range files {
		total += f.size
		tooOld := limits.MaxAge > 0 && now.Sub(f.mtime) > limits.MaxAge
		tooBig := limits.MaxBytes > 0 && total > limits.MaxBytes
		if !tooOld && !tooBig {
			continue
		}
		total -= f.size
		if err := os.Remove(filepath.Join(dir, f.name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		out.Removed++
		out.ReclaimedBytes += f.size
	}
	return out, errors.Join(errs...)
}

// Prune applies p.Limits to every kept service's archives under root and
// removes the directories of services not in p.KeepServices that weren't
// written to within the last hour. Entries that aren't service
// directories are left alone.
func Prune(root string, p node.LogPrune) (node.PruneResult, error) {
	var out node.PruneResult
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	keep := make(map[string]bool, len(p.KeepServices))
	for _, s := range p.KeepServices {
		keep[s] = true
	}
	mu.Lock()
	defer mu.Unlock()
	now := time.Now()
	var errs []error
	for _, e := range entries {
		if !e.IsDir() || !validID(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if keep[e.Name()] {
			r, err := enforce(dir, p.Limits, now)
			out.Removed += r.Removed
			out.ReclaimedBytes += r.ReclaimedBytes
			if err != nil {
				errs = append(errs, err)
			}
			continue
		}
		// A service created after the caller listed its services may
		// already have archives: a directory written to lately stays
		// until a later pass.
		if info, err := e.Info(); err != nil || now.Sub(info.ModTime()) < tempStale {
			continue
		}
		files, bytes := usage(dir)
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, err)
			continue
		}
		out.Removed += files
		out.ReclaimedBytes += bytes
	}
	r, err := enforceTotal(root, p.Limits.MaxTotalBytes)
	out.Removed += r.Removed
	out.ReclaimedBytes += r.ReclaimedBytes
	if err != nil {
		errs = append(errs, err)
	}
	return out, errors.Join(errs...)
}

// enforceTotal removes the oldest archives under root, of any service,
// while all of them together are over max (0: no cap; mu held). It reads
// every service directory: a few hundred entries on a busy server, once
// per archive.
func enforceTotal(root string, max int64) (node.PruneResult, error) {
	var out node.PruneResult
	if max <= 0 {
		return out, nil
	}
	dirs, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return out, err
	}
	var files []archiveFile
	for _, d := range dirs {
		if !d.IsDir() || !validID(d.Name()) {
			continue
		}
		entries, _ := os.ReadDir(filepath.Join(root, d.Name()))
		for _, e := range entries {
			info, err := e.Info()
			if err != nil || !info.Mode().IsRegular() || !strings.HasSuffix(e.Name(), ext) || !validID(strings.TrimSuffix(e.Name(), ext)) {
				continue
			}
			files = append(files, archiveFile{name: filepath.Join(d.Name(), e.Name()), size: info.Size(), mtime: info.ModTime()})
		}
	}
	slices.SortFunc(files, func(a, b archiveFile) int { return b.mtime.Compare(a.mtime) })
	var total int64
	var errs []error
	for _, f := range files {
		total += f.size
		if total <= max {
			continue
		}
		total -= f.size
		if err := os.Remove(filepath.Join(root, f.name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		out.Removed++
		out.ReclaimedBytes += f.size
	}
	return out, errors.Join(errs...)
}

// usage counts the archives in a service directory and their bytes.
func usage(dir string) (files int, bytes int64) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && strings.HasSuffix(e.Name(), ext) {
			files++
			bytes += info.Size()
		}
	}
	return files, bytes
}

// lineBuffer is how many decoded lines may wait for a reader's consumer.
const lineBuffer = 256

// Read streams the lines of a release's archive under root, oldest first:
// those at or after q.Since, and of them the last q.Tail (all for 0). A
// tail keeps only that many lines in memory while it reads. The channel
// closes after the last line, on a damaged end of the file (the lines
// before it are sent), or when ctx ends. No archive is node.ErrNotFound.
func Read(ctx context.Context, root string, q node.LogArchiveQuery) (<-chan node.LogLine, error) {
	p, err := path(root, q.Service, q.Release)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("logs of release %s: %w", q.Release, node.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	ch := make(chan node.LogLine, lineBuffer)
	go func() {
		defer close(ch)
		defer func() { _ = f.Close() }()
		emit := func(l node.LogLine) bool {
			select {
			case ch <- l:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if q.Tail > maxRing {
			// A long tail is a count, then a second read that skips the
			// lines before it: memory stays a line, not the archive.
			all := node.LogArchiveQuery{Since: q.Since}
			skip := -q.Tail
			_ = scan(f, all, func(node.LogLine) bool { skip++; return ctx.Err() == nil })
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return
			}
			_ = scan(f, all, func(l node.LogLine) bool {
				if skip > 0 {
					skip--
					return true
				}
				return emit(l)
			})
			return
		}
		_ = scan(f, q, emit)
	}()
	return ch, nil
}

// maxRing is the longest tail Read keeps in memory while it reads; a
// longer one reads the archive twice instead.
const maxRing = 4096

// scan reads an archive from r and emits its lines as Read describes. It
// returns once emit refuses a line, at the end, or at the first damage.
func scan(r io.Reader, q node.LogArchiveQuery, emit func(node.LogLine) bool) error {
	gz, err := gzip.NewReader(bufio.NewReaderSize(r, 64<<10))
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil // an empty file: no members yet
		}
		return err
	}
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	sc.Split(wholeLines)
	var tail ring
	if q.Tail > 0 {
		tail.slots = make([][]byte, 0, min(q.Tail, maxRing))
		tail.n = q.Tail
	}
	for sc.Scan() {
		raw := sc.Bytes()
		if !q.Since.IsZero() {
			if t, ok := lineTime(raw); ok && t.Before(q.Since) {
				continue
			}
		}
		if q.Tail > 0 {
			tail.add(raw)
			continue
		}
		if l, ok := decode(raw); ok && !emit(l) {
			return nil
		}
	}
	for raw := range tail.all() {
		if l, ok := decode(raw); ok && !emit(l) {
			return nil
		}
	}
	return sc.Err()
}

// wholeLines splits like bufio.ScanLines but drops a last line without
// its newline: every archived line has one, so that is the start of a
// member cut short.
func wholeLines(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), nil, nil
	}
	return 0, nil, nil
}

// ring keeps the last n raw lines, reusing their buffers.
type ring struct {
	slots [][]byte
	n     int
	next  int // the oldest slot once full
}

func (r *ring) add(line []byte) {
	if len(r.slots) < r.n {
		r.slots = append(r.slots, append([]byte(nil), line...))
		return
	}
	r.slots[r.next] = append(r.slots[r.next][:0], line...)
	r.next = (r.next + 1) % r.n
}

// all yields the kept lines, oldest first.
func (r *ring) all() func(yield func([]byte) bool) {
	return func(yield func([]byte) bool) {
		for i := range r.slots {
			if !yield(r.slots[(r.next+i)%len(r.slots)]) {
				return
			}
		}
	}
}

// timePrefix starts every line Write encodes, before the time.
var timePrefix = []byte(`{"t":"`)

// lineTime reads a line's time without decoding the rest of it.
func lineTime(raw []byte) (time.Time, bool) {
	rest, ok := bytes.CutPrefix(raw, timePrefix)
	if !ok {
		return time.Time{}, false
	}
	end := bytes.IndexByte(rest, '"')
	if end < 0 {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, string(rest[:end]))
	return t, err == nil
}

// decode parses one archive line; a damaged one is skipped.
func decode(raw []byte) (node.LogLine, bool) {
	var r record
	if err := json.Unmarshal(raw, &r); err != nil {
		return node.LogLine{}, false
	}
	return node.LogLine{Time: r.T, Stream: r.S, Text: r.X}, true
}

// truncate cuts s to at most n bytes, on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
