package walarchive

import (
	"archive/tar"
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A base backup (ADR 0093) is the data directory as a tar, copied while
// Postgres runs between pg_backup_start and pg_backup_stop, with the
// backup_label pg_backup_stop returns as its last entry. Recovery from it
// replays the archive from the start segment, which makes the copy
// consistent at the backup's end.

// BaseLine starts the line Base prints on standard error about the
// backup (ParseBase).
const BaseLine = "dock-wal-base:"

// StopTimeout bounds pg_backup_stop, which waits until the backup's last
// segment is archived: a stalled archive fails the backup instead of
// hanging it.
const StopTimeout = 15 * time.Minute

// Base is what a base backup says about itself.
type Base struct {
	// Start is the first segment recovery needs; SegmentBytes the WAL
	// segment size.
	Start        string
	SegmentBytes int64
	// Consistent is when the backup ended: the earliest time it restores to.
	Consistent time.Time
}

// String is the line Base prints (BaseLine ...).
func (b Base) String() string {
	return fmt.Sprintf("%s start=%s segment_bytes=%d consistent_ms=%d", BaseLine, b.Start, b.SegmentBytes, b.Consistent.UnixMilli())
}

// ParseBase finds the base backup's line in a job's output. restic
// passes on the command's standard error after a prefix of its own
// ("subprocess /.dock: "), so the line is found anywhere.
func ParseBase(output string) (Base, error) {
	for line := range strings.Lines(output) {
		_, rest, ok := strings.Cut(strings.TrimSpace(line), BaseLine)
		if !ok {
			continue
		}
		var b Base
		for f := range strings.FieldsSeq(rest) {
			k, v, _ := strings.Cut(f, "=")
			switch k {
			case "start":
				b.Start = v
			case "segment_bytes":
				b.SegmentBytes, _ = strconv.ParseInt(v, 10, 64)
			case "consistent_ms":
				ms, _ := strconv.ParseInt(v, 10, 64)
				b.Consistent = time.UnixMilli(ms)
			}
		}
		if _, err := ParseSegment(b.Start, b.SegmentBytes); err != nil || b.Consistent.IsZero() {
			return Base{}, fmt.Errorf("the base backup's line %q doesn't parse", strings.TrimSpace(line))
		}
		return b, nil
	}
	return Base{}, errors.New("the base backup didn't say where it starts; its log shows why")
}

// RunBase writes a base backup of the data directory pgdata (mounted
// read-only) as a tar to w. It talks to the server with psql (the libpq
// PG* variables say where), whose one session holds the backup open.
func RunBase(ctx context.Context, pgdata string, w io.Writer) (Base, error) {
	if ents, err := os.ReadDir(filepath.Join(pgdata, "pg_tblspc")); err == nil && len(ents) > 0 {
		return Base{}, errors.New("the database has tablespaces, which point-in-time backups don't copy")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s, err := startSession(ctx)
	if err != nil {
		return Base{}, err
	}
	defer s.close()
	out, err := s.query(`SELECT pg_walfile_name(b) || ' ' || (SELECT setting FROM pg_settings WHERE name = 'wal_segment_size') FROM pg_backup_start('dock', true) AS b;`)
	if err != nil {
		return Base{}, fmt.Errorf("pg_backup_start: %w", err)
	}
	var b Base
	start, size, _ := strings.Cut(out, " ")
	b.Start = start
	if b.SegmentBytes, err = strconv.ParseInt(size, 10, 64); err != nil {
		return Base{}, fmt.Errorf("pg_backup_start answered %q", out)
	}
	owner, err := os.Stat(pgdata)
	if err != nil {
		return Base{}, err
	}
	own, err := tar.FileInfoHeader(owner, "")
	if err != nil {
		return Base{}, err
	}
	tw := tar.NewWriter(w)
	if err := writeTree(tw, pgdata); err != nil {
		return Base{}, err
	}
	stop := time.AfterFunc(StopTimeout, cancel)
	out, err = s.query(`SELECT translate(encode(convert_to(labelfile, 'UTF8'), 'base64'), E'\n', '') || ' ' || translate(encode(convert_to(coalesce(spcmapfile, ''), 'UTF8'), 'base64'), E'\n', '') || ' ' || (extract(epoch FROM clock_timestamp()) * 1000)::bigint FROM pg_backup_stop(true);`)
	if !stop.Stop() {
		return Base{}, fmt.Errorf("the WAL this backup needs wasn't archived within %s; the archiver's status says why", StopTimeout)
	}
	if err != nil {
		return Base{}, fmt.Errorf("pg_backup_stop: %w", err)
	}
	parts := strings.Split(out, " ")
	if len(parts) != 3 {
		return Base{}, fmt.Errorf("pg_backup_stop answered %q", out)
	}
	label, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		return Base{}, fmt.Errorf("pg_backup_stop's label: %w", err)
	}
	if parts[1] != "" {
		return Base{}, errors.New("the database has tablespaces, which point-in-time backups don't copy")
	}
	ms, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return Base{}, fmt.Errorf("pg_backup_stop answered %q", out)
	}
	b.Consistent = time.UnixMilli(ms)
	if err := tw.WriteHeader(&tar.Header{Name: "backup_label", Mode: 0o600, Size: int64(len(label)), ModTime: b.Consistent, Typeflag: tar.TypeReg, Uid: own.Uid, Gid: own.Gid}); err != nil {
		return Base{}, err
	}
	if _, err := tw.Write(label); err != nil {
		return Base{}, err
	}
	return b, tw.Close()
}

// session is one psql session, fed statements on standard input.
type session struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	stderr *strings.Builder
}

// endMark ends each answer.
const endMark = "DOCK-END"

func startSession(ctx context.Context) (*session, error) {
	cmd := exec.CommandContext(ctx, "psql", "-X", "-q", "-A", "-t", "-v", "ON_ERROR_STOP=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start psql: %w", err)
	}
	return &session{cmd: cmd, in: in, out: bufio.NewReader(out), stderr: &stderr}, nil
}

// query runs one statement and returns its one line of output.
func (s *session) query(sql string) (string, error) {
	if _, err := io.WriteString(s.in, sql+"\n\\echo "+endMark+"\n"); err != nil {
		return "", s.failed(err)
	}
	var lines []string
	for {
		line, err := s.out.ReadString('\n')
		if err != nil {
			return "", s.failed(err)
		}
		line = strings.TrimRight(line, "\n")
		if line == endMark {
			break
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		return "", fmt.Errorf("expected one line, got %q", lines)
	}
	return lines[0], nil
}

// failed is the error of a session that ended: what psql printed.
func (s *session) failed(err error) error {
	_ = s.in.Close()
	_ = s.cmd.Wait()
	if msg := strings.TrimSpace(s.stderr.String()); msg != "" {
		return errors.New(msg)
	}
	return err
}

func (s *session) close() {
	_ = s.in.Close()
	_ = s.cmd.Wait()
}

// skipContents are directories copied empty: Postgres rebuilds what they
// hold, and WAL comes from the archive.
var skipContents = map[string]bool{
	"pg_wal": true, "pg_replslot": true, "pg_dynshmem": true, "pg_notify": true, "pg_serial": true,
	"pg_snapshots": true, "pg_stat_tmp": true, "pg_subtrans": true,
}

// skipFiles are files a base backup leaves out (the PostgreSQL manual's
// list for low-level backups).
var skipFiles = map[string]bool{
	"postmaster.pid": true, "postmaster.opts": true, "backup_label": true, "tablespace_map": true,
	"backup_manifest": true, "recovery.signal": true, "standby.signal": true,
}

// writeTree writes the data directory under root to tw. Files may
// change while they are read: each gets the size it had when opened,
// cut or padded with zeros to it (WAL replay repairs the pages), and one
// that disappeared is left out.
func writeTree(tw *tar.Writer, root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		dir, name := path.Split(rel)
		// Timeline history files stay with the backup: recovery reads its
		// timeline's.
		history := dir == "pg_wal/" && strings.HasSuffix(name, ".history")
		if skipContents[strings.TrimSuffix(dir, "/")] && rel != "pg_wal/archive_status" && !history {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if (dir == "" && skipFiles[name]) || name == "pg_internal.init" || strings.HasPrefix(name, "pgsql_tmp") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.IsDir():
			info, err := d.Info()
			if err != nil {
				return skipGone(err)
			}
			if err := writeHeader(tw, info, rel+"/", ""); err != nil {
				return err
			}
			if strings.HasPrefix(rel, "pg_wal/") {
				return fs.SkipDir
			}
			return nil
		case d.Type()&fs.ModeSymlink != 0:
			info, err := d.Info()
			if err != nil {
				return skipGone(err)
			}
			link, err := os.Readlink(p)
			if err != nil {
				return skipGone(err)
			}
			return writeHeader(tw, info, rel, link)
		case d.Type().IsRegular():
			return writeFile(tw, p, rel)
		}
		return nil
	})
}

// skipGone ignores a file that disappeared while the tree was walked.
func skipGone(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func writeHeader(tw *tar.Writer, info fs.FileInfo, name, link string) error {
	h, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return err
	}
	h.Name, h.Uname, h.Gname = name, "", ""
	return tw.WriteHeader(h)
}

// writeFile copies one file at the size it had when opened.
func writeFile(tw *tar.Writer, p, rel string) error {
	f, err := os.Open(p)
	if err != nil {
		return skipGone(err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := writeHeader(tw, info, rel, ""); err != nil {
		return err
	}
	n, err := io.CopyN(tw, f, info.Size())
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if n < info.Size() {
		_, err = io.CopyN(tw, zeros{}, info.Size()-n)
	}
	return err
}

// zeros reads zero bytes forever.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// Unpack writes a base backup's tar from r into dir, with the owners and
// modes it holds. Names leaving dir are refused.
func Unpack(r io.Reader, dir string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read the base backup: %w", err)
		}
		name := path.Clean(strings.TrimSuffix(h.Name, "/"))
		if name == "." || name == ".." || strings.HasPrefix(name, "../") || path.IsAbs(name) {
			return fmt.Errorf("the base backup holds %q, outside the data directory", h.Name)
		}
		dst := filepath.Join(dir, filepath.FromSlash(name))
		mode := fs.FileMode(h.Mode).Perm()
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, mode); err != nil {
				return err
			}
		case tar.TypeReg:
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.Symlink(h.Linkname, dst); err != nil {
				return err
			}
		default:
			continue
		}
		if os.Geteuid() == 0 {
			if err := os.Lchown(dst, h.Uid, h.Gid); err != nil {
				return err
			}
		}
	}
}
