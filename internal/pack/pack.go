// Package pack creates the source archive that `dock deploy` uploads: a
// gzip-compressed tar of a directory.
//
// What goes in:
//   - If the root has a .dockerignore, it decides (Docker semantics) and
//     .gitignore files are not consulted.
//   - Otherwise .gitignore files at the root and in subdirectories apply (git
//     semantics). Global excludes and .git/info/exclude are not read.
//   - .git (at any depth) is always left out. The Dockerfile, .dockerignore and
//     dock.toml are always kept, as the build needs them.
//
// Entries are written in lexical order. Regular files keep their permission
// bits (so executables stay executable), symlinks are stored as links and
// never followed, and other special files are skipped. Owner names and IDs
// are not recorded.
package pack

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/NordTempo/tempoDock/internal/repoconfig"
)

// Options tune Write.
type Options struct {
	// Dockerfile is the Dockerfile path relative to the root, always
	// included even when ignored. Defaults to "Dockerfile".
	Dockerfile string
}

// Stats describe a written archive.
type Stats struct {
	// Files is the number of regular files.
	Files int
	// Bytes is the uncompressed size of those files.
	Bytes int64
}

// Write streams a gzip tar of dir to w and reports what it contained. To
// upload while packing, write into an io.Pipe from a goroutine:
//
//	pr, pw := io.Pipe()
//	go func() { _, err := pack.Write(pw, dir, opts); pw.CloseWithError(err) }()
func Write(w io.Writer, dir string, opts Options) (Stats, error) {
	if opts.Dockerfile == "" {
		opts.Dockerfile = "Dockerfile"
	}
	keep := []string{filepath.ToSlash(filepath.Clean(opts.Dockerfile)), ".dockerignore", repoconfig.FileName}

	var f filter = &gitignore{keep: keep}
	di, err := newDockerignore(filepath.Join(dir, ".dockerignore"), keep)
	switch {
	case err == nil:
		f = di
	case !errors.Is(err, fs.ErrNotExist):
		return Stats{}, fmt.Errorf("read .dockerignore: %w", err)
	}

	// gzip writes a few hundred bytes at a time; through an io.Pipe into an
	// HTTP request each of those is a goroutine hand-off and a chunk.
	bw := bufio.NewWriterSize(w, 64<<10)
	zw := gzip.NewWriter(bw)
	tw := tar.NewWriter(zw)
	var st Stats
	if err := f.enter(dir, ""); err != nil {
		return st, err
	}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.Name() == ".git" { // the repo, or a submodule's .git file
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		dec, err := f.decide(rel, d.IsDir())
		if err != nil {
			return err
		}
		switch {
		case dec == skip && d.IsDir():
			return filepath.SkipDir
		case dec == skip:
			return nil
		case dec == descend:
			return nil
		case d.IsDir():
			if err := f.enter(dir, rel); err != nil {
				return err
			}
		}
		return add(tw, p, rel, d, &st)
	})
	if err != nil {
		return st, err
	}
	if err := tw.Close(); err != nil {
		return st, err
	}
	if err := zw.Close(); err != nil {
		return st, err
	}
	return st, bw.Flush()
}

// add writes one entry. Directories and regular files keep their permission
// bits, symlinks keep their target, anything else is skipped.
func add(tw *tar.Writer, p, rel string, d fs.DirEntry, st *Stats) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	h := &tar.Header{
		Name:    rel,
		Mode:    int64(info.Mode().Perm()),
		ModTime: info.ModTime().Truncate(time.Second),
	}
	switch mode := info.Mode(); {
	case mode.IsDir():
		h.Typeflag, h.Name = tar.TypeDir, rel+"/"
		return tw.WriteHeader(h)
	case mode&fs.ModeSymlink != 0:
		target, err := os.Readlink(p)
		if err != nil {
			return err
		}
		h.Typeflag, h.Linkname = tar.TypeSymlink, filepath.ToSlash(target)
		return tw.WriteHeader(h)
	case mode.IsRegular():
		return addFile(tw, p, h, st)
	default:
		return nil // sockets, devices, pipes
	}
}

func addFile(tw *tar.Writer, p string, h *tar.Header, st *Stats) error {
	file, err := os.Open(p)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	// Stat the open file, so the size matches what is read.
	info, err := file.Stat()
	if err != nil {
		return err
	}
	h.Typeflag, h.Size = tar.TypeReg, info.Size()
	if err := tw.WriteHeader(h); err != nil {
		return err
	}
	if _, err := io.CopyN(tw, file, h.Size); err != nil {
		return fmt.Errorf("%s: %w (did it change while packing?)", h.Name, err)
	}
	st.Files++
	st.Bytes += h.Size
	return nil
}
