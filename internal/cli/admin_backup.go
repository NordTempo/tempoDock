//go:build !dockcli

package cli

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/cpbackup"
	"github.com/NordTempo/tempoDock/internal/store"
)

// secretsWarning goes with every control plane archive written.
const secretsWarning = "It holds every secret of the platform (secret.key opens all variables and credentials, link.key is what nodes trust): keep it 0600, store it encrypted, and delete copies you no longer need."

// newAdminBackupCmd is `dock admin backup`: the control plane's state in
// one archive file (ADR 0021).
func newAdminBackupCmd() *cobra.Command {
	configPath := config.DefaultPath
	var o adminBackupOptions
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Write the control plane's state to an archive (run on it, as root or the dock user)",
		Long: `Write the control plane's state to one archive: a consistent snapshot of the
database (SQLite's VACUUM INTO; safe while dock runs), the keys (secret.key,
link.key, registry.key), build logs and config.toml. The archive holds every
secret of the platform. It is created 0600 and never overwrites a file.

Registry images are left out: releases can be built or pushed again.
--include-registry adds the registry's storage (root), so rollbacks and
promotions of old releases work right after a restore.

--move is for moving the control plane to another server: it needs the
daemon stopped (sudo systemctl stop dock; apps keep running), writes the
archive, then retires this control plane so dock serve refuses to start
here again. Restore it on the new server with install.sh --restore.

Daily backups into the backup repository happen on their own (dock backup
status); this command is for a file in hand.`,
		Example: `  sudo dock admin backup
  sudo dock admin backup -o /root/dock.tar.gz --include-registry
  sudo systemctl stop dock && sudo dock admin backup --move -o /root/dock-move.tar.gz
  ssh old-server 'sudo dock admin backup -o -' > dock.tar.gz`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			o.configPath = configPath
			return adminBackup(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), cfg, o)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", configPath, "path to the daemon config file")
	cmd.Flags().StringVarP(&o.out, "output", "o", "", "archive to write (- for stdout); default ./dock-backup-<host>-<time>.tar.gz")
	cmd.Flags().BoolVar(&o.registry, "include-registry", false, "include the image registry's storage (root)")
	cmd.Flags().BoolVar(&o.move, "move", false, "the daemon must be stopped; retire this control plane after the backup")
	return cmd
}

type adminBackupOptions struct {
	configPath string
	out        string
	registry   bool
	move       bool
}

// adminBackup writes the archive as o says, gzipped. A file is written
// next to its final name and renamed once complete, so a failed backup
// never leaves a file that looks whole.
func adminBackup(ctx context.Context, stdout, stderr io.Writer, cfg config.Config, o adminBackupOptions) error {
	if _, err := os.Stat(cfg.DBPath()); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return &output.Error{Exit: output.ExitAuth, Err: err, Hint: "Run it as root (sudo) or as the dock user."}
		}
		return &output.Error{Exit: output.ExitNotFound, Err: err, Hint: "Run it on the control plane; --config names its config.toml."}
	}
	if o.move {
		if err := cpbackup.CheckMoved(cfg.DataDir); err != nil {
			return err
		}
		unlock, err := cpbackup.Lock(cfg.DataDir)
		if errors.Is(err, cpbackup.ErrLocked) {
			return &output.Error{Exit: output.ExitConflict, Err: err, Hint: "--move needs the daemon stopped, so nothing changes after the backup. Apps keep running."}
		}
		if err != nil {
			return err
		}
		defer unlock()
	}
	src := cpbackup.Source{
		DataDir: cfg.DataDir, ConfigPath: o.configPath,
		Snapshot: func(ctx context.Context, dst string) (store.SnapshotInfo, error) {
			return store.SnapshotFile(ctx, cfg.DBPath(), dst)
		},
	}
	if o.registry {
		if !cfg.Registry.Enabled {
			return usageError("this control plane runs no registry", "Leave out --include-registry.")
		}
		src.RegistryDir = cfg.Registry.StorageDir
	}
	defer fixDBOwner(cfg.DBPath())

	var m cpbackup.Manifest
	var size int64
	where := o.out
	if o.out == "-" {
		if f, ok := stdout.(*os.File); ok {
			if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
				return usageError("refusing to write an archive to a terminal", "Redirect it to a file: `dock admin backup -o - > dock.tar.gz`.")
			}
		}
		cw := &countWriter{w: stdout}
		var err error
		if m, err = writeArchive(ctx, cw, src); err != nil {
			return err
		}
		size, where = cw.n, "stdout"
	} else {
		if where == "" {
			host, _ := os.Hostname()
			where = fmt.Sprintf("dock-backup-%s-%s.tar.gz", host, time.Now().Format("20060102-150405"))
		}
		var err error
		if m, size, err = writeArchiveFile(ctx, where, src); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintf(stderr, "Wrote %s (%s): dock %s, schema %d, taken on %s.\n", where, bytesize(size), m.Version, m.Schema, m.Hostname)
	if m.LinkKey != "" {
		_, _ = fmt.Fprintf(stderr, "Nodes trust link key %s; a restore keeps it.\n", m.LinkKey)
	}
	_, _ = fmt.Fprintln(stderr, secretsWarning)
	if !o.move {
		return nil
	}
	if err := cpbackup.MarkMoved(cfg.DataDir, cpbackup.Moved{At: time.Now().UTC(), Archive: where}); err != nil {
		return fmt.Errorf("the archive is written, but retiring this control plane failed: %w", err)
	}
	_, _ = fmt.Fprintf(stderr, `
This control plane is retired: dock serve refuses to start here from now on.
  sudo systemctl disable dock
Then on the new server: install.sh --restore <archive>
(docs/guide/install.md#moving-the-control-plane). To undo a move that was
never restored anywhere: delete %s and start dock.
`, filepath.Join(cfg.DataDir, cpbackup.MovedFile))
	return nil
}

// writeArchive writes a gzipped archive of src to w.
func writeArchive(ctx context.Context, w io.Writer, src cpbackup.Source) (cpbackup.Manifest, error) {
	gz := gzip.NewWriter(w)
	m, err := cpbackup.Write(ctx, gz, src)
	if err != nil {
		return m, err
	}
	return m, gz.Close()
}

// writeArchiveFile writes a gzipped archive of src to path, created 0600:
// first as path.partial, renamed when complete. An existing file is never
// overwritten.
func writeArchiveFile(ctx context.Context, path string, src cpbackup.Source) (cpbackup.Manifest, int64, error) {
	if _, err := os.Lstat(path); err == nil {
		return cpbackup.Manifest{}, 0, usageError(path+" already exists", "Pick another name with -o.")
	}
	partial := path + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return cpbackup.Manifest{}, 0, err
	}
	fail := func(err error) (cpbackup.Manifest, int64, error) {
		_ = f.Close()
		_ = os.Remove(partial)
		return cpbackup.Manifest{}, 0, err
	}
	cw := &countWriter{w: f}
	m, err := writeArchive(ctx, cw, src)
	if err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(partial, path); err != nil {
		_ = os.Remove(partial)
		return cpbackup.Manifest{}, 0, err
	}
	return m, cw.n, nil
}

// countWriter counts what goes through it.
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// fixDBOwner gives the database's -wal and -shm files back to the
// database's owner. Root opening a stopped control plane's database
// (`dock admin backup --move`) can create them; SQLite removes them on
// close, but one left behind owned by root would stop dock serve.
func fixDBOwner(db string) {
	fi, err := os.Stat(db)
	if err != nil {
		return
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || os.Geteuid() != 0 {
		return
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Lchown(db+suffix, int(st.Uid), int(st.Gid))
	}
}

// newAdminRestoreCmd is `dock admin restore`, which install.sh --restore
// runs on a fresh server (ADR 0021).
func newAdminRestoreCmd() *cobra.Command {
	configPath := config.DefaultPath
	var printConfig, oldServerNode, recovery bool
	cmd := &cobra.Command{
		Use:   "restore <archive>",
		Short: "Restore the control plane from `dock admin backup` onto this server (root)",
		Long: `Restore a control plane archive onto this server: the database, keys and
build logs into the data dir, and the registry's storage if the archive has
it and this server runs a registry. The control plane comes back with the
same identity: nodes keep trusting its link key and need no re-join, only
its new address (dock node set-control-plane on each node).

Use install.sh --restore, which installs this server, keeps the settings of
the old config.toml that aren't about the machine, and runs this. The data
dir must not hold a database yet and dock must not be running.

By default this server takes the old one's place: environments placed on
the old server are expected here now (redeploy them; restore databases from
their backups). With --old-server-becomes-node the old server keeps its apps
and joins this control plane as a node under its name: run
` + "`dock server add <name>`" + ` and install.sh --node there.

--recovery is for a server that replaces a lost one (install.sh
--restore-from-bucket): the control plane starts in recovery mode, where
scheduled backups wait, so an empty volume is never backed up over a good
one. ` + "`dock admin recover`" + ` shows what will be restored and ends the mode.

--print-config prints the archive's config.toml and nothing else.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, closeIn, err := openArchiveArg(cmd.InOrStdin(), args[0])
			if err != nil {
				return err
			}
			defer closeIn()
			if printConfig {
				_, b, err := cpbackup.ReadConfig(in)
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(b)
				return err
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			if recovery && oldServerNode {
				return usageError("--recovery is for a server that replaces a lost one", "Leave out --old-server-becomes-node, or --recovery when the old server still runs its apps.")
			}
			return adminRestore(cmd.Context(), cmd.ErrOrStderr(), in, cfg, oldServerNode, recovery)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", configPath, "path to this server's daemon config file")
	cmd.Flags().BoolVar(&printConfig, "print-config", false, "print the archive's config.toml and exit")
	cmd.Flags().BoolVar(&oldServerNode, "old-server-becomes-node", false, "the old server keeps its apps and joins as a node")
	cmd.Flags().BoolVar(&recovery, "recovery", false, "the old server is lost: start in recovery mode (dock admin recover)")
	return cmd
}

// openArchiveArg opens the archive named on the command line (- for
// stdin).
func openArchiveArg(stdin io.Reader, name string) (io.Reader, func(), error) {
	if name == "-" {
		return stdin, func() {}, nil
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { _ = f.Close() }, nil
}

// adminRestore unpacks an archive into cfg's data dir (and registry
// storage), prepares the database for this server and, as root, gives the
// files to the users the daemon and the registry run as. With recovery
// the control plane starts in recovery mode (ADR 0098).
func adminRestore(ctx context.Context, stderr io.Writer, in io.Reader, cfg config.Config, oldServerNode, recovery bool) error {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	unlock, err := cpbackup.Lock(cfg.DataDir)
	if errors.Is(err, cpbackup.ErrLocked) {
		return &output.Error{Exit: output.ExitConflict, Err: err}
	}
	if err != nil {
		return err
	}
	defer unlock()
	t := cpbackup.Target{DataDir: cfg.DataDir}
	if cfg.Registry.Enabled {
		t.RegistryDir = cfg.Registry.StorageDir
	}
	m, err := cpbackup.Restore(ctx, in, t)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	from := fmt.Sprintf("a backup of %s taken %s (dock %s)", m.Hostname, m.CreatedAt.Format(time.RFC3339), m.Version)
	old, err := core.AfterRestore(ctx, st, from, oldServerNode)
	if cerr := st.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if recovery {
		if err := cpbackup.MarkRecovering(cfg.DataDir, from); err != nil {
			return fmt.Errorf("the control plane is restored, but entering recovery mode failed: %w", err)
		}
	}
	if os.Geteuid() == 0 {
		if err := chownTree(cfg.DataDir, "dock"); err != nil {
			return err
		}
		if m.Registry && t.RegistryDir != "" {
			if err := chownTree(t.RegistryDir, "dock-registry"); err != nil {
				return err
			}
		}
	}
	_, _ = fmt.Fprintf(stderr, "Restored %s into %s.\n", from, cfg.DataDir)
	if m.LinkKey != "" {
		_, _ = fmt.Fprintf(stderr, "Link key %s: nodes keep trusting this control plane. Point each at its new address: sudo dock node set-control-plane <host:port>\n", m.LinkKey)
	}
	if m.Registry && t.RegistryDir == "" {
		_, _ = fmt.Fprintln(stderr, "The archive has the registry's storage, but this server runs no registry ([registry] enabled); it was left out.")
	}
	if recovery {
		_, _ = fmt.Fprintln(stderr, "Recovery mode: the apps and their volumes are not back yet, and scheduled backups wait. Once dock runs: sudo dock admin recover")
	}
	if old != "" {
		_, _ = fmt.Fprintf(stderr, "Server %s keeps its apps and waits to join as a node: run `dock server add %s --non-production` (it keeps the kinds it had), then install.sh --node with the token on it.\n", old, old)
	}
	return nil
}

// chownTree gives root and everything under it to the named system user
// and its group.
func chownTree(root, name string) error {
	u, err := user.Lookup(name)
	if err != nil {
		return fmt.Errorf("user %s: %w (install.sh creates it)", name, err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, uid, gid)
	})
}
