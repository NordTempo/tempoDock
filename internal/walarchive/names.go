// Package walarchive is Dock's Postgres WAL archive (ADR 0093): the
// object layout in the backups' bucket, the encryption of each object,
// the archiver that uploads what Postgres's archive_command hands it,
// the fetch that restore_command runs, base backups, and the checks that
// an archive holds every segment a restore needs. The dock binary runs it
// in the archiver container and in jobs (Command); core uses the names
// and checks.
//
// It imports only the standard library and internal/s3.
package walarchive

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Objects are <prefix>/<file>@<mtime in Unix ms>: the file is what
// Postgres archived (a segment, a timeline history, a backup history),
// and mtime is when Postgres last wrote it, so a listing says up to when
// the archive holds every commit without reading an object.

// DefaultSegmentBytes is Postgres's default WAL segment size.
const DefaultSegmentBytes = 16 << 20

// segment is a WAL segment's file name: timeline, then the segment
// number as two 8-digit halves.
var segment = regexp.MustCompile(`^[0-9A-F]{24}$`)

// archivable are the names archive_command is given.
var archivable = regexp.MustCompile(`^([0-9A-F]{24}(\.partial|\.[0-9A-F]{8}\.backup)?|[0-9A-F]{8}\.history)$`)

// ValidName reports whether name is a file Postgres archives.
func ValidName(name string) bool { return archivable.MatchString(name) }

// IsSegment reports whether name is a WAL segment (not a history file).
func IsSegment(name string) bool { return segment.MatchString(name) }

// Prefix is the folder of one database's archive generation: path is
// the backups' folder in the bucket (no slashes around it).
func Prefix(path string, serviceID int64, generation string) string {
	return ServicePrefix(path, serviceID) + generation + "/"
}

// ServicePrefix is the folder of every generation of a database.
func ServicePrefix(path string, serviceID int64) string {
	p := strings.Trim(path, "/")
	if p != "" {
		p += "/"
	}
	return p + "dock-wal/s" + strconv.FormatInt(serviceID, 10) + "/"
}

// Key is the object key of file, last written at mtime, under prefix.
func Key(prefix, file string, mtime time.Time) string {
	return prefix + file + "@" + strconv.FormatInt(mtime.UnixMilli(), 10)
}

// ParseKey splits an object key under prefix into the file and its
// mtime; ok is false for anything else.
func ParseKey(prefix, key string) (file string, mtime time.Time, ok bool) {
	rest, found := strings.CutPrefix(key, prefix)
	if !found {
		return "", time.Time{}, false
	}
	file, ms, found := strings.Cut(rest, "@")
	n, err := strconv.ParseInt(ms, 10, 64)
	if !found || err != nil || !ValidName(file) {
		return "", time.Time{}, false
	}
	return file, time.UnixMilli(n), true
}

// Segment is a WAL segment's position: its timeline and number.
type Segment struct {
	Timeline uint32
	Number   uint64
}

// ParseSegment reads a segment file name, for a segment size.
func ParseSegment(name string, segBytes int64) (Segment, error) {
	if !IsSegment(name) {
		return Segment{}, fmt.Errorf("%q is not a WAL segment name", name)
	}
	per, err := perID(segBytes)
	if err != nil {
		return Segment{}, err
	}
	tl, _ := strconv.ParseUint(name[:8], 16, 32)
	hi, _ := strconv.ParseUint(name[8:16], 16, 32)
	lo, _ := strconv.ParseUint(name[16:], 16, 32)
	if lo >= per {
		return Segment{}, fmt.Errorf("%q isn't a segment of %d bytes", name, segBytes)
	}
	return Segment{Timeline: uint32(tl), Number: hi*per + lo}, nil
}

// Name is the segment's file name, for a segment size.
func (s Segment) Name(segBytes int64) string {
	per, _ := perID(segBytes)
	return fmt.Sprintf("%08X%08X%08X", s.Timeline, s.Number/per, s.Number%per)
}

// perID is how many segments one 4 GB "log ID" holds.
func perID(segBytes int64) (uint64, error) {
	if segBytes < 1<<20 || segBytes > 1<<30 || segBytes&(segBytes-1) != 0 {
		return 0, fmt.Errorf("%d is not a WAL segment size", segBytes)
	}
	return (1 << 32) / uint64(segBytes), nil
}

// Entry is one archived file.
type Entry struct {
	File  string
	Key   string
	MTime time.Time
	Size  int64
}

// Index is a generation's archive by file name.
type Index map[string]Entry

// ErrTwice is Add's error for a file archived twice with different
// contents (different mtimes): the archive holds two histories.
var ErrTwice = errors.New("archived twice with different contents")

// Add records an object of the listing; keys that aren't archive objects
// are skipped.
func (ix Index) Add(prefix, key string, size int64) error {
	file, mtime, ok := ParseKey(prefix, key)
	if !ok {
		return nil
	}
	if e, ok := ix[file]; ok && !e.MTime.Equal(mtime) {
		return fmt.Errorf("WAL file %s was %w", file, ErrTwice)
	}
	ix[file] = Entry{File: file, Key: key, MTime: mtime, Size: size}
	return nil
}

// Coverage is how far an archive reaches from a base backup's start.
type Coverage struct {
	// Through is when the last segment of the unbroken run from the start
	// was written: every commit until then is in the archive.
	Through time.Time
	// Last is that segment; Missing the first segment after it the
	// archive lacks (the end of the run, or a gap if later ones exist).
	Last, Missing string
	// Gap is set when segments after Missing are archived: one is lost.
	Gap bool
}

// Cover walks the segments from start (a base backup's first segment)
// while they are archived and returns how far they reach. Only start's
// timeline is followed: a generation is one history (ADR 0093).
func (ix Index) Cover(start string, segBytes int64) (Coverage, error) {
	s, err := ParseSegment(start, segBytes)
	if err != nil {
		return Coverage{}, err
	}
	var cov Coverage
	for {
		name := s.Name(segBytes)
		e, ok := ix[name]
		if !ok {
			cov.Missing = name
			break
		}
		cov.Through, cov.Last = e.MTime, name
		s.Number++
	}
	if cov.Last == "" {
		return cov, nil
	}
	for name := range ix {
		if !IsSegment(name) {
			continue
		}
		if o, err := ParseSegment(name, segBytes); err == nil && o.Timeline == s.Timeline && o.Number > s.Number {
			cov.Gap = true
			break
		}
	}
	return cov, nil
}

// Reaches checks that the archive holds every commit up to t from a base
// backup's start, saying what is missing and the latest time it reaches
// when it doesn't.
func (ix Index) Reaches(start string, segBytes int64, t time.Time) error {
	cov, err := ix.Cover(start, segBytes)
	if err != nil {
		return err
	}
	if cov.Last == "" {
		return fmt.Errorf("the archive lacks segment %s, where the base backup starts, so nothing from this backup can be restored", cov.Missing)
	}
	if !cov.Through.Before(t) {
		return nil
	}
	at := cov.Through.UTC().Format(time.RFC3339)
	if cov.Gap {
		return fmt.Errorf("WAL segment %s is missing from the archive, so this database can be restored only up to %s; pick a time before that", cov.Missing, at)
	}
	return fmt.Errorf("the archive holds WAL only up to %s (segment %s is the newest); pick a time before that, or wait for archiving to catch up", at, cov.Last)
}
