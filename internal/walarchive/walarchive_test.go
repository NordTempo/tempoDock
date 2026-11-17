package walarchive

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSegments(t *testing.T) {
	s, err := ParseSegment("0000000100000002000000FF", DefaultSegmentBytes)
	if err != nil {
		t.Fatal(err)
	}
	s.Number++
	if got := s.Name(DefaultSegmentBytes); got != "000000010000000300000000" {
		t.Fatalf("next segment = %s", got)
	}
	if _, err := ParseSegment("000000010000000200000100", DefaultSegmentBytes); err == nil {
		t.Fatal("a segment number past the log ID's last parsed")
	}
	for name, ok := range map[string]bool{
		"000000010000000000000003": true, "00000002.history": true, "000000010000000000000003.00000028.backup": true,
		"000000010000000000000003.partial": true, "../etc/passwd": false, "000000010000000000000003x": false, "": false,
	} {
		if ValidName(name) != ok {
			t.Errorf("ValidName(%q) = %v", name, !ok)
		}
	}
}

// index builds an archive of the given segments, each written a minute
// after the one before from t0.
func index(t *testing.T, t0 time.Time, names ...string) Index {
	t.Helper()
	ix := Index{}
	for i, n := range names {
		if err := ix.Add("p/", Key("p/", n, t0.Add(time.Duration(i)*time.Minute)), 1); err != nil {
			t.Fatal(err)
		}
	}
	return ix
}

func TestReaches(t *testing.T) {
	t0 := time.Date(2026, 11, 17, 14, 0, 0, 0, time.UTC)
	ix := index(t, t0, "000000010000000000000003", "000000010000000000000004", "000000010000000000000005")
	if err := ix.Reaches("000000010000000000000003", DefaultSegmentBytes, t0.Add(90*time.Second)); err != nil {
		t.Fatalf("a time the archive holds: %v", err)
	}
	err := ix.Reaches("000000010000000000000003", DefaultSegmentBytes, t0.Add(3*time.Minute))
	if err == nil || !strings.Contains(err.Error(), "only up to 2026-11-17T14:02:00Z") {
		t.Fatalf("past the archive's end: %v", err)
	}

	gap := index(t, t0, "000000010000000000000003", "000000010000000000000004", "000000010000000000000006")
	err = gap.Reaches("000000010000000000000003", DefaultSegmentBytes, t0.Add(2*time.Minute))
	if err == nil || !strings.Contains(err.Error(), "000000010000000000000005 is missing") || !strings.Contains(err.Error(), "up to 2026-11-17T14:01:00Z") {
		t.Fatalf("a missing segment: %v", err)
	}
	if err := gap.Reaches("000000010000000000000009", DefaultSegmentBytes, t0); err == nil || !strings.Contains(err.Error(), "where the base backup starts") {
		t.Fatalf("a missing start: %v", err)
	}

	if err := ix.Add("p/", Key("p/", "000000010000000000000004", t0.Add(time.Hour)), 1); err == nil {
		t.Fatal("a segment archived twice with different contents was taken")
	}
	if err := ix.Add("p/", "p/garbage", 1); err != nil {
		t.Fatalf("an object that isn't the archive's: %v", err)
	}
}

// TestParseBase reads the base backup's line as restic passes it on.
func TestParseBase(t *testing.T) {
	b, err := ParseBase(`{"message_type":"summary"}` + "\nsubprocess /.dock: dock-wal-base: start=000000010000000000000002 segment_bytes=16777216 consistent_ms=1794925349891\n")
	if err != nil || b.Start != "000000010000000000000002" || b.SegmentBytes != DefaultSegmentBytes || b.Consistent.UnixMilli() != 1794925349891 {
		t.Fatalf("ParseBase = %+v, %v", b, err)
	}
	if _, err := ParseBase("nothing\n"); err == nil {
		t.Fatal("parsed a line that isn't there")
	}
}

// dbKey is service id's key under the archive key of password.
func dbKey(t *testing.T, password string, id int64) []byte {
	t.Helper()
	archive, err := DeriveKey(password)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ServiceKey(archive, id)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// openAll opens and reads an object, sealed for file under key.
func openAll(b, key []byte, file string) ([]byte, error) {
	r, err := Open(bytes.NewReader(b), key, file)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// TestSealOpen: an object opens with its database's key and file name
// only, and not once cut short, its chunks or header changed.
func TestSealOpen(t *testing.T) {
	const file = "000000010000000000000003"
	key := dbKey(t, "repository password", 7)
	// Random, so it stays past three chunks after compression.
	plain := make([]byte, 3*chunkSize+17)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	var obj bytes.Buffer
	if err := Seal(&obj, bytes.NewReader(plain), key, file); err != nil {
		t.Fatal(err)
	}
	if got, err := openAll(obj.Bytes(), key, file); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip: %v, %d bytes", err, len(got))
	}
	tampered := func(i int) []byte {
		b := bytes.Clone(obj.Bytes())
		b[i] ^= 1
		return b
	}
	for what, c := range map[string]struct {
		obj  []byte
		key  []byte
		file string
	}{
		"another file's name":     {obj.Bytes(), key, "000000010000000000000004"},
		"another database's key":  {obj.Bytes(), dbKey(t, "repository password", 8), file},
		"another install's key":   {obj.Bytes(), dbKey(t, "another password", 7), file},
		"cut short":               {obj.Bytes()[:obj.Len()-200], key, file},
		"a changed magic":         {tampered(0), key, file},
		"a changed salt":          {tampered(len(magic) + 3), key, file},
		"a changed chunk":         {tampered(headerSize + 100), key, file},
		"the last chunk left out": {obj.Bytes()[:headerSize+chunkSize+16], key, file},
	} {
		if _, err := openAll(c.obj, c.key, c.file); err == nil {
			t.Errorf("an object opened with %s", what)
		}
	}
}

// TestKeysAreNeverShared: every object has a key of its own (its salt),
// and no two databases' or files' keys are the same, so counter nonces
// never repeat under one key.
func TestKeysAreNeverShared(t *testing.T) {
	archive, err := DeriveKey("repository password")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	add := func(what string, key []byte) {
		t.Helper()
		if prev, ok := seen[string(key)]; ok {
			t.Fatalf("%s has the key of %s", what, prev)
		}
		seen[string(key)] = what
	}
	add("the archive", archive)
	for _, id := range []int64{1, 2, 10} {
		db, err := ServiceKey(archive, id)
		if err != nil {
			t.Fatal(err)
		}
		add(fmt.Sprintf("database %d", id), db)
		for _, file := range []string{"000000010000000000000003", "000000010000000000000004"} {
			for n := range 3 { // the same file sealed again gets a new salt
				var obj bytes.Buffer
				if err := Seal(&obj, strings.NewReader("wal"), db, file); err != nil {
					t.Fatal(err)
				}
				k, err := objectKey(db, obj.Bytes()[len(magic):headerSize], file)
				if err != nil {
					t.Fatal(err)
				}
				add(fmt.Sprintf("database %d's %s, sealing %d", id, file, n), k)
			}
		}
	}
}

// TestStalledArchiveIsUnhealthy: Postgres keeps every segment it couldn't
// archive, so the archiver turns unhealthy on waiting WAL long before it
// fills the disk, by age or by size, and says how much and how long.
func TestStalledArchiveIsUnhealthy(t *testing.T) {
	now := time.Date(2026, 11, 17, 14, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	for _, c := range []struct {
		name string
		st   Status
		ok   bool
		says string
	}{
		{"nothing waits", Status{}, true, ""},
		{"a segment waits a minute", Status{Waiting: 1, WaitingBytes: 16 << 20, OldestWaiting: at(time.Minute), DiskBytes: 20 << 30}, true, ""},
		{"a segment waits too long", Status{Waiting: 4, WaitingBytes: 64 << 20, OldestWaiting: at(12 * time.Minute), DiskBytes: 20 << 30},
			false, "4 WAL files wait to be archived, the oldest for 12m"},
		{"a burst passes the size", Status{Waiting: 70, WaitingBytes: 70 << 24, OldestWaiting: at(time.Minute), DiskBytes: 100 << 30, DiskFree: 80 << 30},
			false, "unarchived WAL takes 1.1 GB of the database's disk (80.0 GB free)"},
		{"a small disk's share", Status{Waiting: 7, WaitingBytes: 7 << 24, OldestWaiting: at(time.Minute), DiskBytes: 1 << 30, DiskFree: 600 << 20, LastError: "s3: connection refused"},
			false, "takes 112 MB of the database's disk (600 MB free); the last upload failed: s3: connection refused"},
	} {
		ok, says := judge(c.st, now)
		if ok != c.ok || !strings.Contains(says, c.says) {
			t.Errorf("%s: healthy %v, %q", c.name, ok, says)
		}
	}
}

func TestTreeRoundTrip(t *testing.T) {
	src := t.TempDir()
	for name, body := range map[string]string{
		"PG_VERSION": "17\n", "base/1/1259": "relation", "global/pg_control": "control",
		"pg_wal/000000010000000000000003": "wal", "pg_wal/archive_status/000000010000000000000003.ready": "",
		"postmaster.pid": "1", "pg_stat_tmp/x": "x", "base/1/pg_internal.init": "cache",
	} {
		p := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := writeTree(tw, src); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := Unpack(&buf, dst); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"PG_VERSION": true, "base/1/1259": true, "global/pg_control": true, "pg_wal/archive_status": true, "pg_stat_tmp": true,
		"pg_wal/000000010000000000000003": false, "pg_wal/archive_status/000000010000000000000003.ready": false,
		"postmaster.pid": false, "pg_stat_tmp/x": false, "base/1/pg_internal.init": false,
	} {
		_, err := os.Stat(filepath.Join(dst, name))
		if (err == nil) != want {
			t.Errorf("%s: copied %v, want %v", name, err == nil, want)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "base/1/1259")); string(b) != "relation" {
		t.Errorf("base/1/1259 = %q", b)
	}

	var evil bytes.Buffer
	ew := tar.NewWriter(&evil)
	_ = ew.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o600, Typeflag: tar.TypeReg})
	_ = ew.Close()
	if err := Unpack(&evil, t.TempDir()); err == nil {
		t.Fatal("a name outside the data directory was unpacked")
	}
}
