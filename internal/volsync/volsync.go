//go:build linux

// Package volsync copies a directory tree to another server while the
// tree may still change, in passes (ADR 0063). It is what dock-sync
// (cmd/dock-sync) runs inside two job containers, one on each server,
// joined by the control plane:
//
//	target: dock-sync recv /v     lists /v (the manifest) on stdout, then
//	                              applies the operations it reads on stdin
//	source: dock-sync send /v     reads the manifest on stdin, compares it
//	                              with /v and writes the operations that
//	                              make the target equal on stdout
//
// The control plane pipes the target's stdout into the source's stdin and
// the source's stdout into the target's stdin. A pass sends only what
// differs: entries whose type, size, modification time, mode, owner, link
// target or device differ, plus deletions. Every file's bytes carry a
// SHA-256 that the receiver checks.
//
// Passes run while the source changes (Live). A file read while it was
// being written, or modified so recently that a write in the same clock
// tick wouldn't change its time (the "racy" case), gets modification time
// 0 on the target, so the next pass sends it again. The last pass runs
// with the source's writer stopped, so it is exact; a Check pass after it
// compares without changing anything and must find nothing.
//
// Not copied: sockets (ephemeral), extended attributes and ACLs, holes in
// sparse files (they arrive filled), access times. Symlinks keep their
// target and owner, not their own times.
package volsync

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// Entry types in manifests and operations.
const (
	typeDir     = 'd'
	typeFile    = 'f'
	typeSymlink = 'l'
	typeNode    = 'n' // a FIFO or a character or block device
)

// Manifest and operation tags.
const (
	tagEntry    = 'e' // manifest: one entry
	tagEnd      = 'Z' // manifest: the end, with the count
	opDelete    = 'D'
	opDir       = 'd'
	opFile      = 'f'
	opSymlink   = 'l'
	opHardlink  = 'h'
	opNode      = 'n'
	opMeta      = 'm' // mode, owner and time of an entry that stays
	opEnd       = 'E' // the end of a pass, with its counts
	chunkSize   = 128 << 10
	maxPathSize = 64 << 10
)

// entry is one file system entry of a tree, by its path relative to the
// root ("" is the root itself, separators are "/").
type entry struct {
	path  string
	typ   byte
	mode  uint32 // permission bits, setuid/setgid/sticky (st_mode & 07777)
	uid   uint32
	gid   uint32
	size  int64
	mtime int64 // nanoseconds since the epoch
	link  string
	rdev  uint64
	ino   uint64
	nlink uint64
	// fmt is st_mode's type bits, for nodes (FIFO, char, block).
	fmt uint32
}

// key orders paths as a depth-first walk with sorted names visits them:
// a directory, then everything under it, then its next sibling.
func key(p string) string { return strings.ReplaceAll(p, "/", "\x00") }

// statEntry describes path (relative rel) from its lstat; ok is false for
// sockets and anything else that isn't copied.
func statEntry(rel string, st *syscall.Stat_t) (entry, bool) {
	e := entry{
		path: rel, mode: st.Mode & 0o7777, uid: st.Uid, gid: st.Gid,
		mtime: st.Mtim.Nano(), ino: st.Ino, fmt: st.Mode & syscall.S_IFMT,
		nlink: uint64(st.Nlink), //nolint:unconvert // uint32 on arm64
	}
	switch st.Mode & syscall.S_IFMT {
	case syscall.S_IFDIR:
		e.typ = typeDir
	case syscall.S_IFREG:
		e.typ, e.size = typeFile, st.Size
	case syscall.S_IFLNK:
		e.typ = typeSymlink
	case syscall.S_IFIFO, syscall.S_IFCHR, syscall.S_IFBLK:
		e.typ, e.rdev = typeNode, st.Rdev
	default:
		return entry{}, false
	}
	return e, true
}

// lstat is syscall.Lstat with the path in the error.
func lstat(path string) (*syscall.Stat_t, error) {
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		return nil, &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	return &st, nil
}

// walk calls fn for every entry under root in key order, the root first
// (path ""). An entry that disappears while walking (a live tree) is
// skipped.
func walk(root string, fn func(e entry) error) error {
	st, err := lstat(root)
	if err != nil {
		return err
	}
	e, _ := statEntry("", st)
	if e.typ != typeDir {
		return fmt.Errorf("%s is not a directory", root)
	}
	if err := fn(e); err != nil {
		return err
	}
	return walkDir(root, "", fn)
}

func walkDir(root, rel string, fn func(e entry) error) error {
	names, err := readNames(filepath.Join(root, rel))
	if errors.Is(err, os.ErrNotExist) {
		return nil // removed while walking
	}
	if err != nil {
		return err
	}
	for _, name := range names {
		child := name
		if rel != "" {
			child = rel + "/" + name
		}
		st, err := lstat(filepath.Join(root, child))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		e, ok := statEntry(child, st)
		if !ok {
			continue
		}
		if e.typ == typeSymlink {
			if e.link, err = os.Readlink(filepath.Join(root, child)); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return err
			}
		}
		if err := fn(e); err != nil {
			return err
		}
		if e.typ == typeDir {
			if err := walkDir(root, child, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// readNames lists a directory's names, sorted.
func readNames(dir string) ([]string, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	return names, nil
}

// writer encodes manifests and operations: a tag byte, then fields as
// uvarints, varints and length-prefixed strings.
type writer struct {
	w   *bufio.Writer
	buf [binary.MaxVarintLen64]byte
	err error
}

func newWriter(w io.Writer) *writer { return &writer{w: bufio.NewWriterSize(w, 256<<10)} }

func (w *writer) byte(b byte) {
	if w.err == nil {
		w.err = w.w.WriteByte(b)
	}
}

func (w *writer) uint(v uint64) {
	if w.err == nil {
		_, w.err = w.w.Write(w.buf[:binary.PutUvarint(w.buf[:], v)])
	}
}

func (w *writer) int(v int64) {
	if w.err == nil {
		_, w.err = w.w.Write(w.buf[:binary.PutVarint(w.buf[:], v)])
	}
}

func (w *writer) bytes(b []byte) {
	w.uint(uint64(len(b)))
	if w.err == nil {
		_, w.err = w.w.Write(b)
	}
}

func (w *writer) string(s string) { w.bytes([]byte(s)) }

func (w *writer) flush() error {
	if w.err == nil {
		w.err = w.w.Flush()
	}
	return w.err
}

// entry writes e as a manifest entry.
func (w *writer) entry(e entry) {
	w.byte(tagEntry)
	w.string(e.path)
	w.byte(e.typ)
	w.uint(uint64(e.mode))
	w.uint(uint64(e.uid))
	w.uint(uint64(e.gid))
	w.int(e.size)
	w.int(e.mtime)
	w.string(e.link)
	w.uint(e.rdev)
	w.uint(e.ino)
	w.uint(e.nlink)
	w.uint(uint64(e.fmt))
}

// reader decodes what writer encodes. The first error sticks.
type reader struct {
	r   *bufio.Reader
	err error
}

func newReader(r io.Reader) *reader { return &reader{r: bufio.NewReaderSize(r, 256<<10)} }

func (r *reader) byte() byte {
	if r.err != nil {
		return 0
	}
	b, err := r.r.ReadByte()
	r.fail(err)
	return b
}

func (r *reader) uint() uint64 {
	if r.err != nil {
		return 0
	}
	v, err := binary.ReadUvarint(r.r)
	r.fail(err)
	return v
}

func (r *reader) int() int64 {
	if r.err != nil {
		return 0
	}
	v, err := binary.ReadVarint(r.r)
	r.fail(err)
	return v
}

func (r *reader) string() string {
	n := r.uint()
	if r.err != nil {
		return ""
	}
	if n > maxPathSize {
		r.err = fmt.Errorf("a name of %d bytes in the stream: it is damaged", n)
		return ""
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r.r, b)
	r.fail(err)
	return string(b)
}

// fail records err; an end of the stream in the middle is unexpected.
func (r *reader) fail(err error) {
	if err != nil && r.err == nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		r.err = err
	}
}

// entry reads a manifest entry's fields (after its tag).
func (r *reader) entry() entry {
	e := entry{path: r.string(), typ: r.byte()}
	e.mode, e.uid, e.gid = uint32(r.uint()), uint32(r.uint()), uint32(r.uint())
	e.size, e.mtime = r.int(), r.int()
	e.link, e.rdev, e.ino, e.nlink, e.fmt = r.string(), r.uint(), r.uint(), r.uint(), uint32(r.uint())
	return e
}

// safePath joins a relative path from the stream to root, refusing one
// that would leave it.
func safePath(root, rel string) (string, error) {
	if rel == "" {
		return root, nil
	}
	if strings.HasPrefix(rel, "/") || slices.Contains(strings.Split(rel, "/"), "..") || strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("path %q leaves the volume", rel)
	}
	return filepath.Join(root, rel), nil
}

// Report is what a pass did, as each side counts it.
type Report struct {
	// Entries is how many entries the source has (send).
	Entries int64
	// Changed is how many entries were created or updated, Bytes the
	// file content that took, Deleted how many were removed.
	Changed, Bytes, Deleted int64
	// Racy is how many files were sent while they could still change
	// (Live): the next pass sends them again.
	Racy int64
	// Differences lists the first paths a Check pass found different.
	Differences []string
}

// String is the report line dock-sync prints, which the control plane
// parses.
func (r Report) String() string {
	return fmt.Sprintf("dock-sync: entries=%d changed=%d bytes=%d deleted=%d racy=%d", r.Entries, r.Changed, r.Bytes, r.Deleted, r.Racy)
}
