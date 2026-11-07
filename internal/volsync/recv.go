//go:build linux

package volsync

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Recv lists root as a manifest on manifest, then applies the operations
// it reads from ops until their end, and flushes the file system. The
// counts at the end must match what it applied: a stream cut short, or
// a file whose bytes don't match their hash, is an error.
func Recv(root string, ops io.Reader, manifest io.Writer) (Report, error) {
	w := newWriter(manifest)
	var n uint64
	err := walk(root, func(e entry) error {
		n++
		w.entry(e)
		return w.err
	})
	if err != nil {
		return Report{}, fmt.Errorf("list %s: %w", root, err)
	}
	w.byte(tagEnd)
	w.uint(n)
	if err := w.flush(); err != nil {
		return Report{}, fmt.Errorf("send the list: %w", err)
	}
	a := &applier{root: root, r: newReader(ops), dirTimes: map[string]int64{}}
	if err := a.run(); err != nil {
		return a.rep, err
	}
	return a.rep, nil
}

// applier applies one pass's operations under root.
type applier struct {
	root string
	r    *reader
	// dirTimes are the times directories get once the pass is done,
	// since creating and removing entries in them changes theirs: the
	// source's for directories the pass sent, their own from before for
	// the parents of what it changed.
	dirTimes map[string]int64
	tmp      int
	rep      Report
}

func (a *applier) run() error {
	for {
		op := a.r.byte()
		if a.r.err != nil {
			return cut(a.r.err)
		}
		var err error
		switch op {
		case opDelete:
			err = a.delete(a.r.string())
		case opDir, opMeta:
			p := a.r.string()
			mode, uid, gid, mtime := uint32(a.r.uint()), int(a.r.uint()), int(a.r.uint()), a.r.int()
			if a.r.err == nil {
				err = a.dirOrMeta(op, p, mode, uid, gid, mtime)
			}
		case opFile:
			err = a.file(a.r.string())
		case opSymlink:
			p, target := a.r.string(), a.r.string()
			uid, gid := int(a.r.uint()), int(a.r.uint())
			if a.r.err == nil {
				err = a.symlink(p, target, uid, gid)
			}
		case opHardlink:
			p, to := a.r.string(), a.r.string()
			if a.r.err == nil {
				err = a.hardlink(p, to)
			}
		case opNode:
			p := a.r.string()
			mode, rdev, uid, gid, mtime := uint32(a.r.uint()), a.r.uint(), int(a.r.uint()), int(a.r.uint()), a.r.int()
			if a.r.err == nil {
				err = a.node(p, mode, rdev, uid, gid, mtime)
			}
		case opEnd:
			entries, changed, deleted := int64(a.r.uint()), int64(a.r.uint()), int64(a.r.uint())
			if a.r.err != nil {
				return a.r.err
			}
			a.rep.Entries = entries
			if changed != a.rep.Changed || deleted != a.rep.Deleted {
				return fmt.Errorf("the source sent %d changes and %d deletions, %d and %d arrived", changed, deleted, a.rep.Changed, a.rep.Deleted)
			}
			return a.finish()
		default:
			return fmt.Errorf("unexpected operation %q in the stream: it is damaged", op)
		}
		if err == nil {
			err = a.r.err
		}
		if err != nil {
			return cut(err)
		}
	}
}

// cut explains a stream that ended in the middle of a pass.
func cut(err error) error {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return errors.New("the stream ended before the pass did: the source stopped sending")
	}
	return err
}

// finish gives directories their times, deepest first, and flushes the
// file system so the copy is on disk before the pass reports success.
func (a *applier) finish() error {
	dirs := make([]string, 0, len(a.dirTimes))
	for d := range a.dirTimes {
		dirs = append(dirs, d)
	}
	slices.SortFunc(dirs, func(x, y string) int {
		return strings.Count(y, "/") - strings.Count(x, "/") + boolInt(x == "") - boolInt(y == "")
	})
	for _, d := range dirs {
		p, err := safePath(a.root, d)
		if err != nil {
			return err
		}
		if err := setTime(p, a.dirTimes[d]); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	f, err := os.Open(a.root)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := unix.Syncfs(int(f.Fd())); err != nil {
		return fmt.Errorf("flush %s to disk: %w", a.root, err)
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// touch notes the time of rel's parent before the pass changes what is
// in it, unless it is noted already.
func (a *applier) touch(rel string) {
	if rel == "" {
		return
	}
	parent := path.Dir(rel)
	if parent == "." {
		parent = ""
	}
	if _, ok := a.dirTimes[parent]; ok {
		return
	}
	p, err := safePath(a.root, parent)
	if err != nil {
		return
	}
	if st, err := lstat(p); err == nil {
		a.dirTimes[parent] = st.Mtim.Nano()
	}
}

// prepare returns rel's absolute path with whatever was there removed,
// ready to be created anew.
func (a *applier) prepare(rel string) (string, error) {
	if rel == "" {
		return "", errors.New("the stream replaces the volume's root: it is damaged")
	}
	p, err := safePath(a.root, rel)
	if err != nil {
		return "", err
	}
	a.touch(rel)
	if err := os.RemoveAll(p); err != nil {
		return "", err
	}
	return p, nil
}

func (a *applier) delete(rel string) error {
	if _, err := a.prepare(rel); err != nil {
		return err
	}
	a.rep.Deleted++
	return nil
}

// dirOrMeta creates a directory (opDir) or updates an entry that stays
// (opMeta), with mode, owner and time.
func (a *applier) dirOrMeta(op byte, rel string, mode uint32, uid, gid int, mtime int64) error {
	p, err := safePath(a.root, rel)
	if err != nil {
		return err
	}
	st, err := lstat(p)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	isDir := exists && st.Mode&syscall.S_IFMT == syscall.S_IFDIR
	if op == opDir && !isDir {
		if p, err = a.prepare(rel); err != nil {
			return err
		}
		if err := os.Mkdir(p, 0o700); err != nil {
			return err
		}
		isDir = true
	} else if op == opMeta && !exists {
		return fmt.Errorf("%s is gone from the target during the pass", p)
	}
	if err := os.Lchown(p, uid, gid); err != nil {
		return err
	}
	a.rep.Changed++
	if exists && st.Mode&syscall.S_IFMT == syscall.S_IFLNK {
		return nil
	}
	if err := syscall.Chmod(p, mode); err != nil {
		return &os.PathError{Op: "chmod", Path: p, Err: err}
	}
	if isDir {
		a.dirTimes[rel] = mtime
		return nil
	}
	return setTime(p, mtime)
}

// file writes a file's content into a temporary file next to it, checks
// its hash, sets its metadata and renames it into place.
func (a *applier) file(rel string) error {
	p, err := safePath(a.root, rel)
	if err != nil || rel == "" {
		return errors.Join(err, errors.New("a file in place of the volume's root: the stream is damaged"))
	}
	a.touch(rel)
	f, tmp, err := a.createTemp(filepath.Dir(p))
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	h := sha256.New()
	var n int64
	var buf []byte
	for {
		size := a.r.uint()
		if a.r.err != nil {
			return a.r.err
		}
		if size == 0 {
			break
		}
		if size > chunkSize {
			return fmt.Errorf("a chunk of %d bytes in the stream: it is damaged", size)
		}
		if cap(buf) < int(size) {
			buf = make([]byte, chunkSize)
		}
		b := buf[:size]
		if _, err := io.ReadFull(a.r.r, b); err != nil {
			a.r.fail(err)
			return a.r.err
		}
		h.Write(b)
		if _, err := f.Write(b); err != nil {
			return err
		}
		n += int64(size)
	}
	sum := make([]byte, sha256.Size)
	if _, err := io.ReadFull(a.r.r, sum); err != nil {
		a.r.fail(err)
		return a.r.err
	}
	if !bytes.Equal(sum, h.Sum(nil)) {
		return fmt.Errorf("%s arrived damaged: its SHA-256 doesn't match the source's", p)
	}
	mode, uid, gid, mtime := uint32(a.r.uint()), int(a.r.uint()), int(a.r.uint()), a.r.int()
	racy := a.r.byte()
	if a.r.err != nil {
		return a.r.err
	}
	if err := f.Chown(uid, gid); err != nil {
		return err
	}
	if err := syscall.Fchmod(int(f.Fd()), mode); err != nil {
		return &os.PathError{Op: "chmod", Path: tmp, Err: err}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if st, err := lstat(p); err == nil && st.Mode&syscall.S_IFMT == syscall.S_IFDIR {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	done = true
	if racy != 0 {
		mtime = 0 // sent again by the next pass
		a.rep.Racy++
	}
	if err := setTime(p, mtime); err != nil {
		return err
	}
	a.rep.Changed++
	a.rep.Bytes += n
	return nil
}

// createTemp creates a new file in dir that no entry of the pass uses.
func (a *applier) createTemp(dir string) (*os.File, string, error) {
	for {
		a.tmp++
		p := filepath.Join(dir, ".dock-sync-tmp-"+strconv.Itoa(a.tmp))
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return f, p, err
	}
}

func (a *applier) symlink(rel, target string, uid, gid int) error {
	p, err := a.prepare(rel)
	if err != nil {
		return err
	}
	if err := os.Symlink(target, p); err != nil {
		return err
	}
	if err := os.Lchown(p, uid, gid); err != nil {
		return err
	}
	a.rep.Changed++
	return nil
}

func (a *applier) hardlink(rel, to string) error {
	src, err := safePath(a.root, to)
	if err != nil {
		return err
	}
	p, err := a.prepare(rel)
	if err != nil {
		return err
	}
	if err := os.Link(src, p); err != nil {
		return err
	}
	a.rep.Changed++
	return nil
}

func (a *applier) node(rel string, mode uint32, rdev uint64, uid, gid int, mtime int64) error {
	p, err := a.prepare(rel)
	if err != nil {
		return err
	}
	if err := syscall.Mknod(p, mode, int(rdev)); err != nil {
		return &os.PathError{Op: "mknod", Path: p, Err: err}
	}
	if err := os.Lchown(p, uid, gid); err != nil {
		return err
	}
	if err := syscall.Chmod(p, mode&0o7777); err != nil {
		return &os.PathError{Op: "chmod", Path: p, Err: err}
	}
	if err := setTime(p, mtime); err != nil {
		return err
	}
	a.rep.Changed++
	return nil
}

// setTime sets a file's access and modification time to mtime.
func setTime(p string, mtime int64) error {
	ts := syscall.NsecToTimespec(mtime)
	if err := syscall.UtimesNano(p, []syscall.Timespec{ts, ts}); err != nil {
		return &os.PathError{Op: "utimes", Path: p, Err: err}
	}
	return nil
}
