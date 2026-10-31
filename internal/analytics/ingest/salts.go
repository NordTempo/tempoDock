package ingest

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json/v2"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Salts are the daily salts visitor IDs are hashed with (ADR 0037). The
// control plane makes a random one per UTC day and hands each server
// today's and tomorrow's; a day's salt is deleted once the day is over,
// so nobody, Dock included, can recompute yesterday's visitor IDs. They
// are kept in a file (0600) so a server restarted while the control plane
// is unreachable still counts visitors.
type Salts struct {
	path string
	mu   sync.RWMutex
	days map[int64][]byte
}

// SaltBytes is the length of a salt.
const SaltBytes = 32

// Day is the UTC day number of t: days since 1970-01-01.
func Day(t time.Time) int64 { return t.Unix() / 86400 }

// OpenSalts reads the salts kept at path (none if it doesn't exist).
func OpenSalts(path string) (*Salts, error) {
	s := &Salts{path: path, days: map[int64][]byte{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var stored map[string][]byte
	if err := json.Unmarshal(b, &stored); err != nil {
		return nil, err
	}
	for k, v := range stored {
		if d, err := strconv.ParseInt(k, 10, 64); err == nil && len(v) == SaltBytes {
			s.days[d] = v
		}
	}
	return s, nil
}

// Set replaces the salts with days (UTC day number to salt), keeping only
// today's and later ones, and saves them.
func (s *Salts) Set(days map[int64][]byte, now time.Time) error {
	today := Day(now)
	keep := map[int64][]byte{}
	stored := map[string][]byte{}
	for d, v := range days {
		if d >= today && len(v) == SaltBytes {
			keep[d] = v
			stored[strconv.FormatInt(d, 10)] = v
		}
	}
	b, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	if s.path != "" {
		if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
			return err
		}
		tmp := s.path + ".tmp"
		if err := os.WriteFile(tmp, b, 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, s.path); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.days = keep
	s.mu.Unlock()
	return nil
}

// get returns day's salt.
func (s *Salts) get(day int64) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.days[day]
	return v, ok
}

// maxHashedAgent caps the user agent bytes that go into a visitor ID.
const maxHashedAgent = 400

// visitorID is hash(salt, site, IP, user agent): the same person on the
// same day and site, and nothing that names them.
func visitorID(salt []byte, site int64, ip, ua string) uint64 {
	var buf [SaltBytes + 8 + 46 + 1 + maxHashedAgent]byte
	n := copy(buf[:], salt)
	binary.BigEndian.PutUint64(buf[n:], uint64(site))
	n += 8
	n += copy(buf[n:n+46], ip)
	buf[n] = 0
	n++
	n += copy(buf[n:], ua[:min(len(ua), maxHashedAgent)])
	sum := sha256.Sum256(buf[:n])
	return binary.BigEndian.Uint64(sum[:8])
}
