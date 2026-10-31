package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// A store moves to another server as its database file (ADR 0037): the
// old store streams a Snapshot, the new one Receives it next to its own
// database and the next Open puts it in place.

// Snapshot streams the database file to w as it is after a checkpoint.
// Writes, rollups and prunes wait meanwhile, so the file doesn't change,
// and a trim does nothing (it would checkpoint); queries go on. start is
// called with the file's size and its events before the first byte.
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
	f, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	start(st.Size(), events)
	_, err = io.CopyN(w, f, st.Size())
	return err
}

// Received is what Receive wrote.
type Received struct {
	Bytes  int64
	SHA256 string
}

// pending is where a received database waits for the next Open.
func pending(path string) string { return path + ".restore" }

// Receive writes another store's Snapshot next to the database at path,
// for the next Open to put in place of it. It hashes what it writes, so
// the sender can check the copy.
func Receive(path string, r io.Reader) (Received, error) {
	tmp := pending(path) + ".partial"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return Received{}, err
	}
	defer func() { _ = os.Remove(tmp) }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), r)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Received{}, fmt.Errorf("write the received database: %w", err)
	}
	if err := os.Rename(tmp, pending(path)); err != nil {
		return Received{}, err
	}
	return Received{Bytes: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// applyReceived puts a received database in place of path's, with its
// write-ahead log gone (the snapshot was checkpointed). Open calls it
// before DuckDB opens the file.
func applyReceived(path string) error {
	if _, err := os.Stat(pending(path)); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.Remove(path + ".wal"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(pending(path), path); err != nil {
		return fmt.Errorf("put the received database in place: %w", err)
	}
	// The spill directory belongs to the old file's queries.
	return os.RemoveAll(filepath.Clean(path + ".tmp"))
}
