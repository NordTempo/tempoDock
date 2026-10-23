// Package updater applies a release the daemon staged: the root side of
// updates (ADR 0028). `dock admin update` runs it, started by
// dock-update.service whenever the daemon writes update.RequestFile.
//
// It trusts nothing the daemon wrote except the requested version: it
// copies the staged files out of the daemon's reach and checks them
// against the release's SHA256SUMS there, checks the binary reports that
// version, saves what it replaces, backs up the control plane with the
// daemon stopped, and runs the release's own install.sh. If install.sh
// fails or the new version isn't healthy in time, it puts the old binary,
// units and database back.
package updater

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/NordTempo/tempoDock/internal/update"
)

// System is what the updater does to the machine. Production is
// systemd and install.sh (system.go); tests fake it.
type System interface {
	// Stop and Start a systemd unit.
	Stop(ctx context.Context, unit string) error
	Start(ctx context.Context, unit string) error
	// DaemonReload makes systemd read changed unit files.
	DaemonReload(ctx context.Context) error
	// BinaryVersion runs bin --version and returns the version it says.
	BinaryVersion(ctx context.Context, bin string) (string, error)
	// Install runs a release's install.sh with DOCK_BINARY=bin and a
	// clean environment, its output going to log.
	Install(ctx context.Context, script, bin string, log io.Writer) error
	// Healthy waits until the daemon runs version, started after since,
	// and works: the API answers on a control plane, the node reached its
	// control plane on a node. It gives up after the timeout.
	Healthy(ctx context.Context, version string, since time.Time) error
	// Backup writes the control plane backup to path, with the daemon
	// stopped. Not called on nodes.
	Backup(ctx context.Context, path string) error
	// RestoreDB puts the database in the backup at path back.
	RestoreDB(ctx context.Context, path string) error
	// Notify tells the daemon the result is written; best effort.
	Notify(ctx context.Context)
}

// Options configure a run. `dock admin update` fills them from the config;
// tests point the paths at a temporary directory.
type Options struct {
	// DataDir is the daemon's data dir; the request, the staged release and
	// the result are in update.Dir(DataDir).
	DataDir string
	// StateDir is the updater's own directory, root only: the verified
	// release, what it replaced, and the backup.
	StateDir string
	// BinPath is the installed dock binary.
	BinPath string
	// UnitDir holds the systemd units install.sh installs.
	UnitDir string
	// CaddyJSON is Caddy's bootstrap config, which install.sh rewrites.
	CaddyJSON string
	// Node: this server runs dock-node.service: no database to back up.
	Node bool
	// Arch is this machine's GOARCH; Current the running dock's version.
	Arch, Current string
	System        System
	// Log gets the run's progress; the result keeps its end.
	Log io.Writer
	// MaxRequestAge refuses older requests (a stale one found at boot).
	MaxRequestAge time.Duration
	// Now defaults to time.Now.
	Now func() time.Time
}

// unitPatterns match the units install.sh installs (dock.service,
// dock-node.service, dock-update.path, ...), and not Docker's, whose
// names start with "dock" too.
var unitPatterns = []string{"dock.service", "dock-*.service", "dock-*.path"}

// Daemon units.
const (
	controlPlaneUnit = "dock.service"
	nodeUnit         = "dock-node.service"
)

// logTail is how much of the run's output the result keeps.
const logTail = 16 << 10

// errNoRequest: the unit started but there is nothing to do.
var errNoRequest = errors.New("no update request")

// Run applies the pending request, if any, and writes its result. It
// returns the result; ok is false when there was no request.
func Run(ctx context.Context, o Options) (res update.Result, ok bool, err error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	dir := update.Dir(o.DataDir)
	owner, err := ownerOf(dir)
	if err != nil {
		return res, false, err
	}
	req, err := claim(dir)
	switch {
	case errors.Is(err, errNoRequest):
		return res, false, nil
	case err != nil:
		now := o.Now().UTC()
		res = update.Result{From: o.Current, Status: update.Failed, Error: fmt.Sprintf("read the request: %v", err), StartedAt: now, FinishedAt: now}
	default:
		tail := &tailWriter{max: logTail}
		r := &run{o: o, req: req, log: io.MultiWriter(o.Log, tail), tail: tail}
		res = r.apply(ctx)
		res.Log = tail.String()
	}
	werr := update.WriteJSON(dir, update.ResultFile, res, func(f *os.File) error { return f.Chown(owner.uid, owner.gid) })
	_ = update.RemoveIfExists(dir, update.ApplyingFile)
	if werr == nil && !o.Node {
		o.System.Notify(ctx)
	}
	return res, true, werr
}

// claim takes the request: renamed to ApplyingFile first, so the path
// unit doesn't start the updater again for it.
func claim(dir string) (update.Request, error) {
	var req update.Request
	if !update.Exists(dir, update.RequestFile) {
		return req, errNoRequest
	}
	if err := os.Rename(filepath.Join(dir, update.RequestFile), filepath.Join(dir, update.ApplyingFile)); err != nil {
		return req, err
	}
	return req, update.ReadJSON(dir, update.ApplyingFile, &req)
}

// owner is a uid and gid.
type owner struct{ uid, gid int }

// ownerOf checks that dir is a real directory (not a symlink to
// somewhere else) and returns its owner, who gets the result.
func ownerOf(dir string) (owner, error) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return owner{}, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok {
		return owner{}, fmt.Errorf("%s is not a directory", dir)
	}
	return owner{int(st.Uid), int(st.Gid)}, nil
}

// run is one update.
type run struct {
	o   Options
	req update.Request
	log io.Writer
	// tail is the end of log, for install.sh's last words.
	tail *tailWriter
	// release is the verified copy of the release; prev what it replaces.
	release, prev string
	backup        string
}

func (r *run) say(format string, args ...any) { _, _ = fmt.Fprintf(r.log, "==> "+format+"\n", args...) }

// apply runs the update and returns its result (without the log).
func (r *run) apply(ctx context.Context) update.Result {
	res := update.Result{Request: r.req, From: r.o.Current, StartedAt: r.o.Now().UTC()}
	finish := func(s update.Status, err error) update.Result {
		res.Status, res.FinishedAt, res.Backup = s, r.o.Now().UTC(), r.backup
		if err != nil {
			res.Error = err.Error()
			r.say("%s: %v", s, err)
		} else {
			r.say("%s: dock %s", s, r.req.Version)
		}
		return res
	}
	if err := r.check(); err != nil {
		return finish(update.Failed, err)
	}
	r.say("update dock %s to %s", r.o.Current, r.req.Version)
	if err := r.verify(ctx); err != nil {
		return finish(update.Failed, fmt.Errorf("%w; nothing was changed", err))
	}
	if err := r.save(); err != nil {
		return finish(update.Failed, fmt.Errorf("save the current version: %w; nothing was changed", err))
	}
	unit := controlPlaneUnit
	if r.o.Node {
		unit = nodeUnit
	} else {
		if err := r.backUp(ctx); err != nil {
			_ = r.o.System.Start(ctx, unit)
			return finish(update.Failed, fmt.Errorf("back up the control plane: %w; nothing was changed", err))
		}
	}
	_ = update.RemoveIfExists(update.Dir(r.o.DataDir), update.ConnectedFile)
	since := r.o.Now()
	r.say("install.sh from %s", r.req.Version)
	err := r.o.System.Install(ctx, filepath.Join(r.release, "deploy", "install.sh"), filepath.Join(r.release, update.BinaryAsset(r.o.Arch)), r.log)
	if err != nil {
		err = fmt.Errorf("install.sh failed (%w): %s", err, r.tail.lastLine())
	} else {
		r.say("health check")
		if err = r.o.System.Healthy(ctx, r.req.Version, since); err != nil {
			err = fmt.Errorf("dock %s isn't healthy: %w", r.req.Version, err)
		}
	}
	if err == nil {
		_ = os.RemoveAll(r.release)
		if r.o.Node {
			// A control plane keeps its staged release for nodes that
			// were offline; a node has no use for its copy.
			_ = os.RemoveAll(filepath.Dir(update.ReleaseDir(r.o.DataDir, r.req.Version)))
		}
		return finish(update.Succeeded, nil)
	}
	r.say("%v; rolling back to %s", err, r.o.Current)
	if rerr := r.rollback(ctx, unit); rerr != nil {
		return finish(update.Failed, fmt.Errorf("%w; the rollback failed too: %w. Restore by hand: the previous binary and units are in %s, the backup is %s", err, rerr, r.prev, r.backup))
	}
	return finish(update.RolledBack, err)
}

// check refuses requests that make no sense before anything is touched.
func (r *run) check() error {
	v := r.req.Version
	switch {
	case !update.IsRelease(v):
		return fmt.Errorf("%q is not a release version", v)
	case r.o.MaxRequestAge > 0 && r.o.Now().Sub(r.req.RequestedAt) > r.o.MaxRequestAge:
		return fmt.Errorf("the request to update to %s from %s is too old; ask again", v, r.req.RequestedAt.Format(time.RFC3339))
	case update.IsRelease(r.o.Current) && !update.Newer(v, r.o.Current):
		return fmt.Errorf("this server runs %s already; updates only go to newer versions", r.o.Current)
	}
	return nil
}

// verify copies the staged release into StateDir and checks the copies:
// against SHA256SUMS, and the binary against the version it says. Then it
// unpacks the install tarball's deploy/ next to them.
func (r *run) verify(ctx context.Context) error {
	v := r.req.Version
	staged := update.ReleaseDir(r.o.DataDir, v)
	r.release = filepath.Join(r.o.StateDir, v)
	if err := os.RemoveAll(r.release); err != nil {
		return err
	}
	if err := os.MkdirAll(r.release, 0o700); err != nil {
		return err
	}
	r.say("verify the release")
	for _, name := range update.Assets(v, r.o.Arch) {
		if _, err := update.CopyFile(filepath.Join(staged, name), filepath.Join(r.release, name), 0o700); err != nil {
			return fmt.Errorf("copy the staged %s: %w", name, err)
		}
	}
	sums, err := update.ReadSums(r.release)
	if err != nil {
		return err
	}
	for _, name := range update.Assets(v, r.o.Arch)[1:] {
		if err := sums.Check(r.release, name); err != nil {
			return err
		}
	}
	bin := filepath.Join(r.release, update.BinaryAsset(r.o.Arch))
	got, err := r.o.System.BinaryVersion(ctx, bin)
	if err != nil {
		return fmt.Errorf("run the new binary: %w", err)
	}
	if got != v {
		return fmt.Errorf("the binary says it is %q, not %s", got, v)
	}
	return untarDeploy(filepath.Join(r.release, update.InstallAsset(v)), r.release)
}

// save copies what install.sh replaces into StateDir/previous: the
// binary, dock's units (unitPatterns) and Caddy's bootstrap config.
func (r *run) save() error {
	r.prev = filepath.Join(r.o.StateDir, "previous")
	if err := os.RemoveAll(r.prev); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(r.prev, "units"), 0o700); err != nil {
		return err
	}
	if _, err := update.CopyFile(r.o.BinPath, filepath.Join(r.prev, "dock"), 0o755); err != nil {
		return err
	}
	var units []string
	for _, pattern := range unitPatterns {
		m, err := filepath.Glob(filepath.Join(r.o.UnitDir, pattern))
		if err != nil {
			return err
		}
		units = append(units, m...)
	}
	for _, u := range units {
		if _, err := update.CopyFile(u, filepath.Join(r.prev, "units", filepath.Base(u)), 0o644); err != nil {
			return err
		}
	}
	if _, err := update.CopyFile(r.o.CaddyJSON, filepath.Join(r.prev, "caddy.json"), 0o644); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// backUp stops the daemon and writes the control plane backup, the
// rollback point. Older backups go once it is written.
func (r *run) backUp(ctx context.Context) error {
	r.say("stop dock and back up the control plane")
	if err := r.o.System.Stop(ctx, controlPlaneUnit); err != nil {
		return err
	}
	name := fmt.Sprintf("backup-%s-%s.tar.gz", r.o.Current, r.o.Now().UTC().Format("20060102-150405"))
	path := filepath.Join(r.o.StateDir, name)
	if err := r.o.System.Backup(ctx, path); err != nil {
		return err
	}
	r.backup = path
	old, _ := filepath.Glob(filepath.Join(r.o.StateDir, "backup-*.tar.gz"))
	for _, b := range old {
		if b != path {
			_ = os.Remove(b)
		}
	}
	r.say("backup %s", path)
	return nil
}

// rollback puts the saved binary, units, Caddy config and (on a control
// plane) the database back, starts the daemon and checks it.
func (r *run) rollback(ctx context.Context, unit string) error {
	_ = r.o.System.Stop(ctx, unit)
	if err := replace(filepath.Join(r.prev, "dock"), r.o.BinPath, 0o755); err != nil {
		return err
	}
	units, err := filepath.Glob(filepath.Join(r.prev, "units", "*"))
	if err != nil {
		return err
	}
	for _, u := range units {
		if err := replace(u, filepath.Join(r.o.UnitDir, filepath.Base(u)), 0o644); err != nil {
			return err
		}
	}
	if err := replace(filepath.Join(r.prev, "caddy.json"), r.o.CaddyJSON, 0o644); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := r.o.System.DaemonReload(ctx); err != nil {
		return err
	}
	if !r.o.Node {
		r.say("restore the database from %s", r.backup)
		if err := r.o.System.RestoreDB(ctx, r.backup); err != nil {
			return err
		}
	}
	since := r.o.Now()
	if err := r.o.System.Start(ctx, unit); err != nil {
		return err
	}
	return r.o.System.Healthy(ctx, r.o.Current, since)
}

// replace copies src over dst through a temporary file and a rename.
func replace(src, dst string, perm os.FileMode) error {
	tmp := dst + ".rollback"
	_ = os.Remove(tmp)
	if _, err := update.CopyFile(src, tmp, perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// untarDeploy unpacks deploy/ from an install tarball into dir: regular
// files and directories only, never a path leaving it.
func untarDeploy(tarball, dir string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	found := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", filepath.Base(tarball), err)
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if !filepath.IsLocal(name) || (name != "deploy" && !strings.HasPrefix(name, "deploy/")) {
			return fmt.Errorf("refusing %q in %s", hdr.Name, filepath.Base(tarball))
		}
		dst := filepath.Join(dir, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return err
			}
			out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(hdr.Mode)&0o755)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, io.LimitReader(tr, update.MaxAsset))
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			found = found || name == "deploy/install.sh"
		default:
			return fmt.Errorf("refusing %q in %s: only files and directories", hdr.Name, filepath.Base(tarball))
		}
	}
	if !found {
		return fmt.Errorf("%s has no deploy/install.sh", filepath.Base(tarball))
	}
	return nil
}

// tailWriter keeps the last max bytes written to it.
type tailWriter struct {
	max int
	buf []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailWriter) String() string { return string(t.buf) }

// lastLine is the last line written that isn't empty.
func (t *tailWriter) lastLine() string {
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
