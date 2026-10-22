package core

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"strings"
)

// rerootTar copies a GitHub tarball, whose entries all sit in one
// top-level directory ("owner-repo-sha/"), to w as a plain tar rooted at
// dir inside that directory ("" for the repository root): the build
// context `dock deploy` would upload from there, dock.toml included.
// Entries outside dir are skipped. It streams entry by entry.
func rerootTar(tr *tar.Reader, w io.Writer, dir string) error {
	tw := tar.NewWriter(w)
	top, found := "", false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read GitHub tarball: %w", err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue // GitHub's comment with the commit SHA
		}
		first, rest, _ := strings.Cut(h.Name, "/")
		if top == "" {
			top = first
		} else if first != top {
			return fmt.Errorf("GitHub tarball has %q outside its top directory %s/", h.Name, top)
		}
		name, ok := within(rest, dir)
		if !ok || name == "" {
			continue
		}
		if h.Typeflag == tar.TypeLink {
			// Hard link targets are archive paths too.
			_, target, _ := strings.Cut(h.Linkname, "/")
			if h.Linkname, ok = within(target, dir); !ok || h.Linkname == "" {
				continue
			}
		}
		h.Name = name
		h.Format = tar.FormatUnknown // let the writer pick a format for the new name
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return fmt.Errorf("read GitHub tarball: %w", err)
		}
		found = true
	}
	switch {
	case found:
		return tw.Close()
	case dir != "":
		return invalid("directory %s/ is not in the commit; fix the connection's directory with `dock git connect ... --dir`", dir)
	}
	return invalid("the commit has no files")
}

// within returns p relative to dir when p is dir or inside it.
func within(p, dir string) (string, bool) {
	if dir == "" {
		return p, true
	}
	if p == dir || p == dir+"/" {
		return "", true
	}
	return strings.CutPrefix(p, dir+"/")
}
