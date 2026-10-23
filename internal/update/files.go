package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"syscall"
)

// OpenNoFollow opens path for reading, failing if its last element is a
// symlink. The root updater reads files the dock user could have
// replaced with links to anything.
func OpenNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}

// HashFile returns the hex SHA-256 of the regular file at path, without
// following a symlink.
func HashFile(path string) (string, error) {
	f, err := OpenNoFollow(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CopyFile copies the regular file src to dst (created, never
// overwritten, with mode perm) and returns its hex SHA-256, read from the
// same bytes it wrote: what dst holds is what was hashed.
func CopyFile(src, dst string, perm os.FileMode) (string, error) {
	in, err := OpenNoFollow(src)
	if err != nil {
		return "", err
	}
	defer func() { _ = in.Close() }()
	st, err := in.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", src)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dst)
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
