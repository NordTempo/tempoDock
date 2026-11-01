package store

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// A store moves to another server as a tar stream (ADR 0037): its
// database file as "analytics.duckdb", then its replay files under
// "replay/". The old store streams a Snapshot, the new one Receives it
// next to its own database and the next Open puts it in place.

// snapshotDB is the database's name in a snapshot.
const snapshotDB = "analytics.duckdb"

// snapshotFile is a file a snapshot carries: its name in the stream and
// its path and size on disk.
type snapshotFile struct {
	name, path string
	size       int64
}

// Snapshot streams the database file, as it is after a checkpoint, and
// the replay files to w as a tar stream. Writes, rollups and prunes wait
// meanwhile, so nothing changes, and a trim does nothing (it would
// checkpoint); queries go on. start is called with the stream's size and
// the events before the first byte.
func (s *Store) Snapshot(ctx context.Context, start func(size, events int64), w io.Writer) error {
	if s.path == "" {
		return errors.New("an in-memory store has no file to copy")
	}
	// The gate before writeMu, as Write takes them; the gate only for the
	// checkpoint, so a trim waiting for it doesn't hold queries up for
	// the whole copy.
	s.gate.RLock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.copying.Store(true)
	defer s.copying.Store(false)
	var events int64
	_, err := s.db.ExecContext(ctx, "CHECKPOINT")
	if err == nil {
		err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM events").Scan(&events)
	}
	s.gate.RUnlock()
	if err != nil {
		return fmt.Errorf("checkpoint before the copy: %w", err)
	}
	files, err := s.snapshotFiles()
	if err != nil {
		return err
	}
	// The stream's size up front: each file a 512-byte header and its
	// data padded to 512, then two empty blocks.
	size := int64(1024)
	for _, f := range files {
		size += 512 + (f.size+511)/512*512
	}
	start(size, events)
	tw := tar.NewWriter(w)
	for _, f := range files {
		if err := copyInto(tw, f); err != nil {
			return err
		}
	}
	return tw.Close()
}

// snapshotFiles lists what a snapshot carries: the database, then every
// replay file.
func (s *Store) snapshotFiles() ([]snapshotFile, error) {
	st, err := os.Stat(s.path)
	if err != nil {
		return nil, err
	}
	files := []snapshotFile{{name: snapshotDB, path: s.path, size: st.Size()}}
	dir := s.replayDir()
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && p == dir {
			return filepath.SkipDir
		}
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		files = append(files, snapshotFile{name: path.Join("replay", filepath.ToSlash(rel)), path: p, size: info.Size()})
		return nil
	})
	return files, err
}

// copyInto writes one file into a snapshot.
func copyInto(tw *tar.Writer, f snapshotFile) error {
	if len(f.name) > 100 {
		return fmt.Errorf("%s: a snapshot's names are at most 100 bytes", f.name)
	}
	if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o600, Size: f.size, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
		return err
	}
	in, err := os.Open(f.path)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	_, err = io.CopyN(tw, in, f.size)
	return err
}

// Received is what Receive wrote.
type Received struct {
	Bytes  int64
	SHA256 string
}

// pending is where a received database waits for the next Open, and
// pendingReplay its replay files.
func pending(path string) string       { return path + ".restore" }
func pendingReplay(path string) string { return filepath.Join(filepath.Dir(path), "replay.restore") }

// Receive writes another store's Snapshot next to the database at path,
// for the next Open to put in place of it. It hashes what it reads, so
// the sender can check the copy.
func Receive(path string, r io.Reader) (Received, error) {
	tmp, tmpReplay := pending(path)+".partial", pendingReplay(path)+".partial"
	_ = os.RemoveAll(tmpReplay)
	defer func() {
		_ = os.Remove(tmp)
		_ = os.RemoveAll(tmpReplay)
	}()
	h := sha256.New()
	counted := &countingReader{r: io.TeeReader(r, h)}
	tr := tar.NewReader(counted)
	gotDB := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Received{}, fmt.Errorf("read the snapshot: %w", err)
		}
		var dst string
		switch rest, ok := strings.CutPrefix(hdr.Name, "replay/"); {
		case hdr.Name == snapshotDB:
			dst, gotDB = tmp, true
		case ok && hdr.Typeflag == tar.TypeReg && filepath.IsLocal(rest):
			dst = filepath.Join(tmpReplay, filepath.FromSlash(rest))
		default:
			return Received{}, fmt.Errorf("the snapshot has %q, which isn't a store's file", hdr.Name)
		}
		if err := writeFile(dst, tr); err != nil {
			return Received{}, fmt.Errorf("write the received %s: %w", hdr.Name, err)
		}
	}
	// The tar ends before its padding blocks; hash them too.
	if _, err := io.Copy(io.Discard, counted); err != nil {
		return Received{}, err
	}
	if !gotDB {
		return Received{}, errors.New("the snapshot has no database")
	}
	if _, err := os.Stat(tmpReplay); err == nil {
		_ = os.RemoveAll(pendingReplay(path))
		if err := os.Rename(tmpReplay, pendingReplay(path)); err != nil {
			return Received{}, err
		}
	}
	if err := os.Rename(tmp, pending(path)); err != nil {
		return Received{}, err
	}
	return Received{Bytes: counted.n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// writeFile writes r to dst, creating its directory, and syncs it.
func writeFile(dst string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// countingReader counts what it reads.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// applyReceived puts a received database in place of path's, with its
// write-ahead log gone (the snapshot was checkpointed), and its replay
// files in place of the replay directory. Open calls it before DuckDB
// opens the file.
func applyReceived(path string) error {
	if _, err := os.Stat(pending(path)); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.Remove(path + ".wal"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	replay := filepath.Join(filepath.Dir(path), "replay")
	if err := os.RemoveAll(replay); err != nil {
		return err
	}
	if err := os.Rename(pendingReplay(path), replay); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("put the received replays in place: %w", err)
	}
	if err := os.Rename(pending(path), path); err != nil {
		return fmt.Errorf("put the received database in place: %w", err)
	}
	// The spill directory belongs to the old file's queries.
	return os.RemoveAll(filepath.Clean(path + ".tmp"))
}
