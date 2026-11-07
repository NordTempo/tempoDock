//go:build linux

package volsync

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
)

// Mode is how a send pass treats its source.
type Mode int

const (
	// Final: nothing writes to the source, so the copy is exact.
	Final Mode = iota
	// Live: the source may change while it is read. Files that could
	// have changed unseen are marked, and the next pass sends them again.
	Live
	// Check compares only: nothing is sent, the differences are
	// reported (and make dock-sync exit non-zero).
	Check
)

// racyWindow is how recent a file's modification may be for its time to
// stay unchanged by a write right after it was read: a clock tick of the
// file system (jiffies, well under a second), with room to spare.
const racyWindow = 2 * time.Second

// maxDifferences caps Report.Differences.
const maxDifferences = 20

// Send compares root with the target's manifest, read from manifest up to
// its end marker, and writes the operations that make the target equal to
// out, ending with opEnd. The manifest is spooled to a temporary file
// first, so the target is done listing before it gets an operation.
func Send(root string, manifest io.Reader, out io.Writer, mode Mode) (Report, error) {
	spool, err := spoolManifest(manifest)
	if err != nil {
		return Report{}, fmt.Errorf("read the target's list: %w", err)
	}
	defer func() { _ = spool.Close(); _ = os.Remove(spool.Name()) }()
	s := &sender{root: root, mode: mode, w: newWriter(out), t: newReader(spool), links: map[uint64]linked{}}
	s.next()
	err = walk(root, func(e entry) error {
		s.rep.Entries++
		for s.cur != nil && key(s.cur.path) < key(e.path) {
			s.remove(*s.cur)
		}
		var te *entry
		if s.cur != nil && s.cur.path == e.path {
			te = s.cur
		}
		if err := s.entry(e, te); err != nil {
			return err
		}
		if te != nil {
			if te.typ == typeDir && e.typ != typeDir {
				s.skip = te.path // replaced, with everything under it
			}
			s.next()
		}
		return s.w.err
	})
	if err == nil {
		err = s.t.err
	}
	for err == nil && s.cur != nil {
		s.remove(*s.cur)
		err = s.t.err
	}
	if err != nil {
		return s.rep, err
	}
	s.w.byte(opEnd)
	s.w.uint(uint64(s.rep.Entries))
	if mode == Check {
		s.w.uint(0) // nothing was sent
		s.w.uint(0)
	} else {
		s.w.uint(uint64(s.rep.Changed))
		s.w.uint(uint64(s.rep.Deleted))
	}
	if err := s.w.flush(); err != nil {
		return s.rep, fmt.Errorf("send: %w", err)
	}
	return s.rep, nil
}

// spoolManifest copies a manifest into a temporary file, up to and
// including its end marker, and returns it rewound.
func spoolManifest(in io.Reader) (*os.File, error) {
	f, err := os.CreateTemp("", "dock-sync-manifest-*")
	if err != nil {
		return nil, err
	}
	r, w := newReader(in), newWriter(f)
	for {
		tag := r.byte()
		if r.err != nil {
			break
		}
		if tag == tagEnd {
			w.byte(tagEnd)
			w.uint(r.uint())
			break
		}
		if tag != tagEntry {
			r.err = fmt.Errorf("unexpected tag %q in the list", tag)
			break
		}
		w.entry(r.entry())
	}
	if err := errors.Join(r.err, w.flush()); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	return f, nil
}

// sender is one send pass: the source walk merged with the target's
// manifest.
type sender struct {
	root string
	mode Mode
	w    *writer
	t    *reader
	// cur is the target's next entry, nil after the last.
	cur *entry
	// skip is a target directory being replaced or removed: its
	// entries are skipped.
	skip string
	// links are the source's hard-linked files by inode: the first path
	// seen, and the inode it has on the target if it was left as it was.
	links map[uint64]linked
	rep   Report
}

type linked struct {
	path string
	tino uint64
}

// next moves to the target's next entry, past those under skip.
func (s *sender) next() {
	for {
		tag := s.t.byte()
		if s.t.err != nil || tag == tagEnd {
			s.cur = nil
			return
		}
		e := s.t.entry()
		if s.t.err != nil {
			s.cur = nil
			return
		}
		if s.skip != "" && strings.HasPrefix(e.path, s.skip+"/") {
			continue
		}
		s.skip = ""
		s.cur = &e
		return
	}
}

// differ records a difference: in Check mode instead of an operation.
func (s *sender) differ(path string) bool {
	if s.mode != Check {
		return false
	}
	s.rep.Changed++
	if len(s.rep.Differences) < maxDifferences {
		s.rep.Differences = append(s.rep.Differences, "/"+path)
	}
	return true
}

// remove deletes a target entry the source doesn't have.
func (s *sender) remove(t entry) {
	if t.typ == typeDir {
		s.skip = t.path
	}
	if !s.differ(t.path) {
		s.w.byte(opDelete)
		s.w.string(t.path)
		s.rep.Deleted++
	}
	s.next()
}

// sameMeta reports whether mode, owner and time match.
func sameMeta(a, b entry) bool {
	return a.mode == b.mode && a.uid == b.uid && a.gid == b.gid && a.mtime == b.mtime
}

// entry sends what the target needs for the source entry e; te is the
// target's entry at the same path, if any.
func (s *sender) entry(e entry, te *entry) error {
	switch e.typ {
	case typeDir:
		switch {
		case te == nil || te.typ != typeDir:
			if !s.differ(e.path) {
				s.meta(opDir, e)
			}
		case !sameMeta(e, *te):
			if !s.differ(e.path) {
				s.meta(opMeta, e)
			}
		}
		return nil
	case typeSymlink:
		if te != nil && te.typ == typeSymlink && te.link == e.link && te.uid == e.uid && te.gid == e.gid {
			return nil
		}
		if !s.differ(e.path) {
			s.w.byte(opSymlink)
			s.w.string(e.path)
			s.w.string(e.link)
			s.w.uint(uint64(e.uid))
			s.w.uint(uint64(e.gid))
			s.rep.Changed++
		}
		return nil
	case typeNode:
		if te != nil && te.typ == typeNode && te.fmt == e.fmt && te.rdev == e.rdev && sameMeta(e, *te) {
			return nil
		}
		if !s.differ(e.path) {
			s.w.byte(opNode)
			s.w.string(e.path)
			s.w.uint(uint64(e.fmt | e.mode))
			s.w.uint(e.rdev)
			s.w.uint(uint64(e.uid))
			s.w.uint(uint64(e.gid))
			s.w.int(e.mtime)
			s.rep.Changed++
		}
		return nil
	}
	// A regular file.
	if e.nlink > 1 {
		if first, ok := s.links[e.ino]; ok {
			// Another name of a file already seen: the same inode on
			// the target, or a link made now.
			if te != nil && te.typ == typeFile && first.tino != 0 && te.ino == first.tino {
				return nil
			}
			if !s.differ(e.path) {
				s.w.byte(opHardlink)
				s.w.string(e.path)
				s.w.string(first.path)
				s.rep.Changed++
			}
			return nil
		}
		l := linked{path: e.path}
		if te != nil && te.typ == typeFile {
			l.tino = te.ino
		}
		s.links[e.ino] = l
	}
	// A file that is linked on the target but not on the source is sent
	// again, which gives it its own inode there.
	sameFile := te != nil && te.typ == typeFile && te.size == e.size && te.mtime == e.mtime && (e.nlink > 1 || te.nlink <= 1)
	switch {
	case sameFile && sameMeta(e, *te):
		return nil
	case sameFile:
		if !s.differ(e.path) {
			s.meta(opMeta, e)
		}
		return nil
	case s.differ(e.path):
		return nil
	}
	if l, ok := s.links[e.ino]; ok && e.nlink > 1 {
		l.tino = 0 // the target gets a new inode
		s.links[e.ino] = l
	}
	return s.file(e, te)
}

// meta writes an operation carrying e's mode, owner and time.
func (s *sender) meta(op byte, e entry) {
	s.w.byte(op)
	s.w.string(e.path)
	s.w.uint(uint64(e.mode))
	s.w.uint(uint64(e.uid))
	s.w.uint(uint64(e.gid))
	s.w.int(e.mtime)
	s.rep.Changed++
}

// file sends a file's content, then its metadata as it was after the
// read. In Live mode a file that changed while it was read, or so
// recently that a write right after the read could keep its time, is
// racy: the target gets time 0 for it, so the next pass sends it again.
func (s *sender) file(e entry, te *entry) error {
	path, err := safePath(s.root, e.path)
	if err != nil {
		return err
	}
	start := time.Now()
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ELOOP) {
			if s.mode == Live {
				// Gone or replaced since the walk: the next pass sees it.
				if te != nil {
					s.w.byte(opDelete)
					s.w.string(e.path)
					s.rep.Deleted++
				}
				return nil
			}
		}
		return err
	}
	defer func() { _ = f.Close() }()
	var pre, post syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &pre); err != nil {
		return &os.PathError{Op: "fstat", Path: path, Err: err}
	}
	s.w.byte(opFile)
	s.w.string(e.path)
	h := sha256.New()
	buf := make([]byte, chunkSize)
	var n int64
	for {
		k, rerr := f.Read(buf)
		if k > 0 {
			h.Write(buf[:k])
			s.w.bytes(buf[:k])
			n += int64(k)
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return fmt.Errorf("read %s: %w", path, rerr)
		}
		if s.w.err != nil {
			return s.w.err
		}
	}
	s.w.uint(0)
	if s.w.err == nil {
		_, s.w.err = s.w.w.Write(h.Sum(nil))
	}
	if err := syscall.Fstat(int(f.Fd()), &post); err != nil {
		return &os.PathError{Op: "fstat", Path: path, Err: err}
	}
	racy := s.mode == Live && (pre.Size != post.Size || pre.Mtim != post.Mtim || pre.Ctim != post.Ctim || post.Size != n ||
		post.Mtim.Nano() >= start.Add(-racyWindow).UnixNano())
	s.w.uint(uint64(post.Mode & 0o7777))
	s.w.uint(uint64(post.Uid))
	s.w.uint(uint64(post.Gid))
	s.w.int(post.Mtim.Nano())
	if racy {
		s.w.byte(1)
		s.rep.Racy++
	} else {
		s.w.byte(0)
	}
	s.rep.Changed++
	s.rep.Bytes += n
	return s.w.err
}
