package build

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"slices"
)

// limits caps what one source archive may unpack to. The upload limit only
// bounds the compressed body, so without these a small gzip bomb could fill
// the disk or the inode table.
type limits struct {
	bytes   int64 // total size of regular file contents
	entries int   // number of archive entries
}

var defaultLimits = limits{bytes: 4 << 30, entries: 200_000}

// extract unpacks a tar stream (plain or gzip) into dir, which must exist and
// be empty. The result is safe to read with ordinary file APIs: no entry is
// written outside dir, and every symlink in the tree resolves inside dir (or
// dangles). Entries whose path runs through an earlier symlink are rejected,
// and all writes go through os.Root as a second line of defence. Devices,
// FIFOs and other special files are skipped.
func extract(r io.Reader, dir string, lim limits) error {
	br := bufio.NewReader(r)
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return fmt.Errorf("gzip: %w", err)
		}
		defer func() { _ = zr.Close() }()
		r = zr
	} else {
		r = br
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	x := extractor{root: root, links: map[string]bool{}, budget: lim.bytes}
	tr := tar.NewReader(r)
	for n := 0; ; n++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if n >= lim.entries {
			return fmt.Errorf("source has more than %d files; exclude dependencies and build output from the upload", lim.entries)
		}
		name := path.Clean(hdr.Name)
		if name == "." {
			continue
		}
		if !fs.ValidPath(name) {
			return fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		if err := x.entry(tr, hdr, name); err != nil {
			if errors.Is(err, errTooLarge) {
				return fmt.Errorf("source unpacks to more than %d MB; exclude dependencies and build output from the upload", lim.bytes>>20)
			}
			return fmt.Errorf("extract %q: %w", hdr.Name, err)
		}
	}
	return x.checkLinks()
}

var errTooLarge = errors.New("extraction size limit reached")

type extractor struct {
	root *os.Root
	// links holds every path that is a symlink on disk, including hard
	// links to symlinks.
	links  map[string]bool
	budget int64 // bytes left for file contents
}

func (x *extractor) entry(tr *tar.Reader, hdr *tar.Header, name string) error {
	if p := x.viaLink(name); p != "" {
		return fmt.Errorf("path goes through symlink %q", p)
	}
	mode := fs.FileMode(hdr.Mode).Perm()
	if dir := path.Dir(name); dir != "." {
		if err := x.root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	switch hdr.Typeflag {
	case tar.TypeDir:
		return x.root.MkdirAll(name, mode|0o700)
	case tar.TypeReg:
		if hdr.Size > x.budget {
			return errTooLarge
		}
		f, err := x.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode|0o600)
		if err != nil {
			return err
		}
		n, err := io.CopyN(f, tr, x.budget+1)
		x.budget -= n
		if err == nil { // the file holds more than the budget
			_ = f.Close()
			return errTooLarge
		}
		if !errors.Is(err, io.EOF) {
			_ = f.Close()
			return err
		}
		return f.Close()
	case tar.TypeSymlink:
		// A cheap early check; checkLinks does the real one once the whole
		// tree is on disk.
		if path.IsAbs(hdr.Linkname) || !fs.ValidPath(path.Join(path.Dir(name), hdr.Linkname)) {
			return fmt.Errorf("symlink points outside the source: %q", hdr.Linkname)
		}
		x.links[name] = true
		return x.root.Symlink(hdr.Linkname, name)
	case tar.TypeLink:
		target := path.Clean(hdr.Linkname)
		if !fs.ValidPath(target) {
			return fmt.Errorf("unsafe link target %q", hdr.Linkname)
		}
		if p := x.viaLink(target); p != "" && p != target {
			return fmt.Errorf("link target goes through symlink %q", p)
		}
		// link(2) doesn't follow a symlink target, so this creates a copy
		// of the symlink at a new location.
		if x.links[target] {
			x.links[name] = true
		}
		return x.root.Link(target, name)
	default:
		return nil
	}
}

// viaLink returns name or the first of its parent directories that is a
// symlink extracted earlier, or "" if there is none. Writing through such a
// path would land wherever the link points, not where the lexical path says.
func (x *extractor) viaLink(name string) string {
	for i := range len(name) {
		if name[i] == '/' && x.links[name[:i]] {
			return name[:i]
		}
	}
	if x.links[name] {
		return name
	}
	return ""
}

// checkLinks resolves every symlink against the tree on disk, following
// chains of links, and fails if any leads outside the root. A lexical check
// isn't enough: with `s -> .`, the target `s/s/../..` looks inside but
// resolves to the root's parent. Dangling links are fine, since nothing is
// added to the tree after extraction.
func (x *extractor) checkLinks() error {
	for _, name := range slices.Sorted(maps.Keys(x.links)) {
		if _, err := x.root.Stat(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("symlink %q points outside the source: %w", name, err)
		}
	}
	return nil
}
