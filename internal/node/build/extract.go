package build

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
)

// extract unpacks a tar stream (plain or gzip) into dir, which must exist.
// Entries and links that would land outside dir are rejected, and all writes
// go through os.Root as a second line of defence. Devices, FIFOs and other
// special files are skipped.
func extract(r io.Reader, dir string) error {
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

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		name := path.Clean(hdr.Name)
		if name == "." {
			continue
		}
		if !fs.ValidPath(name) {
			return fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		if err := extractEntry(root, tr, hdr, name); err != nil {
			return fmt.Errorf("extract %q: %w", hdr.Name, err)
		}
	}
}

func extractEntry(root *os.Root, tr *tar.Reader, hdr *tar.Header, name string) error {
	mode := fs.FileMode(hdr.Mode).Perm()
	if dir := path.Dir(name); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	switch hdr.Typeflag {
	case tar.TypeDir:
		return root.MkdirAll(name, mode|0o700)
	case tar.TypeReg:
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode|0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	case tar.TypeSymlink:
		// Railpack reads the source on the host, so a link out of the
		// tree could leak host files into the plan or its logs.
		if !fs.ValidPath(path.Join(path.Dir(name), hdr.Linkname)) || path.IsAbs(hdr.Linkname) {
			return fmt.Errorf("symlink points outside the source: %q", hdr.Linkname)
		}
		return root.Symlink(hdr.Linkname, name)
	case tar.TypeLink:
		target := path.Clean(hdr.Linkname)
		if !fs.ValidPath(target) {
			return fmt.Errorf("unsafe link target %q", hdr.Linkname)
		}
		return root.Link(target, name)
	default:
		return nil
	}
}
