package walarchive

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/s3"
)

// List reads a generation's archive into an Index.
func List(ctx context.Context, cfg Config) (Index, error) {
	return ListFrom(ctx, &s3.Client{Bucket: cfg.Bucket}, cfg.Prefix)
}

// ListFrom reads the archive under prefix through c.
func ListFrom(ctx context.Context, c *s3.Client, prefix string) (Index, error) {
	ix := Index{}
	err := c.List(ctx, prefix, func(objs []s3.Object) error {
		for _, o := range objs {
			if err := ix.Add(prefix, o.Key, o.Size); err != nil {
				return err
			}
		}
		return nil
	})
	return ix, err
}

// WriteIndex writes ix as lines of "<file> <key>", for restore's fetches.
// The job writes it as root and Postgres's restore_command reads it as
// postgres; it names objects, not secrets, so anyone may read it.
func WriteIndex(path string, ix Index) error {
	var b strings.Builder
	for _, file := range slices.Sorted(maps.Keys(ix)) {
		b.WriteString(file + " " + ix[file].Key + "\n")
	}
	f, err := os.Create(path) // the umask's mode: readable by postgres
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, b.String()); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// lookup finds file's key in the index file at path; "" when the archive
// doesn't hold it.
func lookup(path, file string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if name, key, ok := strings.Cut(sc.Text(), " "); ok && name == file {
			return key, nil
		}
	}
	return "", sc.Err()
}

// ErrNotArchived is Fetch's error for a file the archive doesn't hold:
// restore_command then says "not found", which ends recovery's search.
var ErrNotArchived = errors.New("not in the archive")

// Fetch writes file from the archive to dest (restore_command's %f and
// %p), through a temporary file, so recovery never reads half of one.
// The index file (WriteIndex) says which object holds it.
func Fetch(ctx context.Context, cfg Config, index, file, dest string) error {
	key, err := lookup(index, file)
	if err != nil {
		return err
	}
	if key == "" {
		return ErrNotArchived
	}
	body, err := (&s3.Client{Bucket: cfg.Bucket}).Get(ctx, key)
	if err != nil {
		return fmt.Errorf("download %s: %w", file, err)
	}
	defer func() { _ = body.Close() }()
	r, err := Open(body, cfg.Key, file)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".dock-wal-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("read %s: %w", file, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}
