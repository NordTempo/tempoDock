// Package spool is a server's buffer of analytics events (ADR 0037): an
// append-only log of JSON lines in segment files under the data dir, which
// the control plane reads from (Read, Wait) and acknowledges (Ack) once
// the store has the events. Every server spools, the analytics server too,
// so the store has one writer and production keeps collecting while the
// store is unreachable.
//
// A position names a point in the log: the segment's sequence number in
// the high 32 bits and the byte offset after a line in the low 32.
// Positions only grow, so the store skips a batch at or before the last
// one it wrote.
//
// The spool is capped: past MaxBytes the oldest segments are deleted,
// unread or not, and their events counted as dropped. Appends never
// block on a reader.
package spool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Defaults: segments of 16 MB, at most 256 MB in all.
const (
	DefaultSegmentBytes = 16 << 20
	DefaultMaxBytes     = 256 << 20
)

// MaxLine caps one event's line by default (Options.MaxLine), and
// MaxBatchBytes what one Read returns, or one line where lines may be
// longer (it always returns at least one line when there is one): a
// batch fits one message on the node link with room to spare.
const (
	MaxLine       = 64 << 10
	MaxBatchBytes = 512 << 10
)

// segment is one file of the log.
type segment struct {
	seq  uint32
	size int64
	// lines counts the events in it; -1 for segments from before a
	// restart, which are estimated (estLine) rather than read.
	lines int64
}

// estLine is the typical size of an event's line, for estimating counts
// of segments that weren't written by this process.
const estLine = 400

// estLines is about how many events sg holds after byte offset from.
func (sg segment) estLines(from int64) int64 {
	if sg.lines >= 0 && sg.size > 0 {
		return sg.lines * (sg.size - from) / sg.size
	}
	return (sg.size - from) / estLine
}

// Spool is the log. It is safe for concurrent use.
type Spool struct {
	dir          string
	maxBytes     int64
	segmentBytes int64
	maxLine      int

	mu    sync.Mutex
	segs  []segment // oldest first; the last is appended to
	cur   *os.File
	total int64
	// acked is the position the control plane acknowledged last.
	acked uint64
	// pending counts the events after acked (an estimate across drops).
	pending int64
	// dropped counts the events deleted at the cap since the spool was
	// opened, and capDropped is the last segment deleted at the cap.
	dropped    int64
	capDropped uint32
	// appended is closed by the next append, waking Wait; nil while no
	// one waits, so appends allocate nothing for it.
	appended chan struct{}
	closed   bool
	// buf is reused for each appended line and its newline.
	buf []byte
}

// Options size the spool; zero values take the defaults.
type Options struct {
	MaxBytes     int64
	SegmentBytes int64
	// MaxLine caps a line: MaxLine by default, more for replay chunks.
	MaxLine int
}

// Open opens (creating) the spool in dir. A line cut short by a crash at
// the end of the last segment is removed.
func Open(dir string, opts Options) (*Spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Spool{
		dir:          dir,
		maxBytes:     cmpOr(opts.MaxBytes, DefaultMaxBytes),
		segmentBytes: cmpOr(opts.SegmentBytes, DefaultSegmentBytes),
		maxLine:      int(cmpOr(int64(opts.MaxLine), MaxLine)),
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".seg")
		if !ok {
			continue
		}
		seq, err := strconv.ParseUint(name, 10, 32)
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		s.segs = append(s.segs, segment{seq: uint32(seq), size: info.Size(), lines: -1})
	}
	slices.SortFunc(s.segs, func(a, b segment) int { return int(a.seq) - int(b.seq) })
	if b, err := os.ReadFile(filepath.Join(dir, "acked")); err == nil {
		s.acked, _ = strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	}
	if len(s.segs) == 0 {
		s.segs = []segment{{seq: 1, lines: 0}}
	}
	last := &s.segs[len(s.segs)-1]
	f, err := os.OpenFile(s.path(last.seq), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if last.size, err = trimTorn(f, last.size, s.maxLine); err != nil {
		_ = f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		_ = f.Close()
		return nil, err
	}
	s.cur = f
	for _, sg := range s.segs {
		s.total += sg.size
	}
	s.dropAcked()
	s.pending = s.estimateAfter(s.acked)
	return s, nil
}

// cmpOr is v, or def when v is zero.
func cmpOr(v, def int64) int64 {
	if v <= 0 {
		return def
	}
	return v
}

// trimTorn truncates f, of size n, after its last newline; a line is at
// most maxLine bytes.
func trimTorn(f *os.File, n int64, maxLine int) (int64, error) {
	if n == 0 {
		return 0, nil
	}
	buf := make([]byte, min(n, int64(maxLine)+1))
	if _, err := f.ReadAt(buf, n-int64(len(buf))); err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	i := bytes.LastIndexByte(buf, '\n')
	keep := n - int64(len(buf)) + int64(i) + 1
	if keep == n {
		return n, nil
	}
	return keep, f.Truncate(keep)
}

// path is a segment's file.
func (s *Spool) path(seq uint32) string {
	return filepath.Join(s.dir, fmt.Sprintf("%010d.seg", seq))
}

// Pos makes a position from a segment and an offset in it.
func Pos(seq uint32, offset int64) uint64 { return uint64(seq)<<32 | uint64(offset) }

// split takes a position apart.
func split(p uint64) (uint32, int64) { return uint32(p >> 32), int64(p & 0xffffffff) }

// Append adds one event's JSON line (without its newline). At the cap the
// oldest segments go, read or not.
func (s *Spool) Append(line []byte) error {
	if len(line) == 0 || len(line) > s.maxLine || bytes.IndexByte(line, '\n') >= 0 {
		return fmt.Errorf("a spooled line must be 1 to %d bytes without newlines", s.maxLine)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("the spool is closed")
	}
	last := &s.segs[len(s.segs)-1]
	if last.size > 0 && last.size+int64(len(line))+1 > s.segmentBytes {
		if err := s.rotate(); err != nil {
			return err
		}
		last = &s.segs[len(s.segs)-1]
	}
	// One write with the newline, so a reader never sees half a line
	// that is followed by more.
	s.buf = append(append(s.buf[:0], line...), '\n')
	if _, err := s.cur.Write(s.buf); err != nil {
		return err
	}
	last.size += int64(len(s.buf))
	if last.lines >= 0 {
		last.lines++
	}
	s.total += int64(len(s.buf))
	s.pending++
	for s.total > s.maxBytes && len(s.segs) > 1 {
		s.dropOldest()
	}
	if s.appended != nil {
		close(s.appended)
		s.appended = nil
	}
	return nil
}

// rotate starts a new segment.
func (s *Spool) rotate() error {
	seq := s.segs[len(s.segs)-1].seq + 1
	f, err := os.OpenFile(s.path(seq), os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_ = s.cur.Close()
	s.cur = f
	s.segs = append(s.segs, segment{seq: seq})
	return nil
}

// dropOldest deletes the oldest segment at the cap, counting its unread
// events (an estimate, without reading the file under the lock) as
// dropped.
func (s *Spool) dropOldest() {
	sg := s.segs[0]
	var unread int64
	switch aseq, aoff := split(s.acked); {
	case aseq < sg.seq:
		unread = sg.estLines(0)
	case aseq == sg.seq:
		unread = sg.estLines(min(aoff, sg.size))
	}
	s.dropped += unread
	s.pending -= unread
	s.capDropped = sg.seq
	_ = os.Remove(s.path(sg.seq))
	s.total -= sg.size
	s.segs = s.segs[1:]
}

// estimateAfter is about how many events there are after position p.
func (s *Spool) estimateAfter(p uint64) int64 {
	seq, off := split(p)
	var n int64
	for _, sg := range s.segs {
		switch {
		case sg.seq > seq:
			n += sg.estLines(0)
		case sg.seq == seq:
			n += sg.estLines(min(off, sg.size))
		}
	}
	return n
}

// Resume makes sure positions continue after p: when p is beyond the end
// of the log (the store has more than the spool, after a power loss took
// the end of a segment or the spool was reset), appends go to a new
// segment numbered past p, so the store doesn't take them for events it
// has. Readers call it with the store's cursor before reading.
func (s *Spool) Resume(p uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, off := split(p)
	last := s.segs[len(s.segs)-1]
	if seq < last.seq || (seq == last.seq && off <= last.size) {
		return nil
	}
	next := max(seq, last.seq) + 1
	f, err := os.OpenFile(s.path(next), os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_ = s.cur.Close()
	s.cur = f
	s.segs = append(s.segs, segment{seq: next})
	return nil
}

// Batch is what Read returns: complete lines and the position after the
// last.
type Batch struct {
	Lines [][]byte
	// Position is after the last line; read from it next time.
	Position uint64
	// Skipped is set when the position read from had been dropped at
	// the cap: the batch starts at the oldest event left.
	Skipped bool
}

// Read returns up to maxLines events after position p, in order. An empty
// batch at p means nothing is there yet (Wait for more). A position past
// the end of a segment continues in the next.
func (s *Spool) Read(p uint64, maxLines int) (Batch, error) {
	s.mu.Lock()
	seq, off := split(p)
	b := Batch{Position: p}
	i := slices.IndexFunc(s.segs, func(sg segment) bool { return sg.seq >= seq })
	switch {
	case i < 0:
		s.mu.Unlock()
		return b, nil
	case s.segs[i].seq > seq || (i == 0 && p == 0):
		// The segment is gone (dropped at the cap, or acknowledged and
		// deleted) or never was: start at the oldest one left. Only a
		// drop at the cap lost events.
		b.Skipped = p != 0 && seq <= s.capDropped
		seq, off = s.segs[i].seq, 0
	}
	// Past the end of a finished segment: the next one.
	for i < len(s.segs)-1 && off >= s.segs[i].size {
		i++
		seq, off = s.segs[i].seq, 0
	}
	size := s.segs[i].size
	s.mu.Unlock()
	b.Position = Pos(seq, off)
	if off >= size {
		return b, nil
	}
	f, err := os.Open(s.path(seq))
	if err != nil {
		return b, err
	}
	defer func() { _ = f.Close() }()
	chunk := make([]byte, min(size-off, int64(maxLines)*1024+int64(s.maxLine), max(MaxBatchBytes, int64(s.maxLine))))
	n, err := f.ReadAt(chunk, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return b, err
	}
	chunk = chunk[:n]
	if off > 0 && !lineStart(f, off) {
		// Not where a line starts (a position from before a power loss
		// cut the segment): skip to the next one.
		j := bytes.IndexByte(chunk, '\n')
		if j < 0 {
			return b, nil
		}
		off += int64(j) + 1
		chunk = chunk[j+1:]
		b.Position = Pos(seq, off)
	}
	for len(b.Lines) < maxLines {
		j := bytes.IndexByte(chunk, '\n')
		if j < 0 {
			break
		}
		b.Lines = append(b.Lines, chunk[:j])
		off += int64(j) + 1
		chunk = chunk[j+1:]
	}
	b.Position = Pos(seq, off)
	return b, nil
}

// lineStart reports whether offset off of f is the start of a line.
func lineStart(f *os.File, off int64) bool {
	var c [1]byte
	_, err := f.ReadAt(c[:], off-1)
	return err == nil && c[0] == '\n'
}

// Wait blocks until there is an event after p, or ctx ends. It wakes on
// appends; nothing polls.
func (s *Spool) Wait(ctx context.Context, p uint64) error {
	for {
		s.mu.Lock()
		if s.hasAfter(p) {
			s.mu.Unlock()
			return nil
		}
		if s.appended == nil {
			s.appended = make(chan struct{})
		}
		ch := s.appended
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// hasAfter reports whether there is an event after p. Called with mu.
func (s *Spool) hasAfter(p uint64) bool {
	seq, off := split(p)
	last := s.segs[len(s.segs)-1]
	if seq < last.seq {
		// An older segment: anything left in it or after it?
		for _, sg := range s.segs {
			if (sg.seq == seq && off < sg.size) || (sg.seq > seq && sg.size > 0) {
				return true
			}
		}
		return false
	}
	return seq == last.seq && off < last.size
}

// Ack records that everything up to p is stored, with events the number
// of events it covers, and deletes the segments wholly before it.
func (s *Spool) Ack(p uint64, events int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p <= s.acked {
		return nil
	}
	s.acked = p
	s.pending = max(0, s.pending-int64(events))
	s.dropAcked()
	tmp := filepath.Join(s.dir, "acked.tmp")
	if err := os.WriteFile(tmp, []byte(strconv.FormatUint(p, 10)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, "acked"))
}

// dropAcked deletes the finished segments before the acknowledged
// position. Called with mu.
func (s *Spool) dropAcked() {
	seq, off := split(s.acked)
	for len(s.segs) > 1 && (s.segs[0].seq < seq || (s.segs[0].seq == seq && off >= s.segs[0].size)) {
		_ = os.Remove(s.path(s.segs[0].seq))
		s.total -= s.segs[0].size
		s.segs = s.segs[1:]
	}
}

// Stats is what the spool holds.
type Stats struct {
	Bytes int64
	// Pending counts the events not acknowledged yet.
	Pending int64
	// Dropped counts the events deleted at the cap since the spool was
	// opened.
	Dropped int64
}

// Stats reports the spool's size.
func (s *Spool) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{Bytes: s.total, Pending: max(0, s.pending), Dropped: s.dropped}
}

// Close closes the spool; Append fails after it.
func (s *Spool) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.cur.Close()
}
