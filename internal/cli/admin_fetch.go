//go:build !dockcli

package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/cpbackup"
)

// The environment `dock admin fetch-backup` reads the repository from.
const (
	envRestoreRepository = "DOCK_RESTORE_REPOSITORY"
	envRestoreEndpoint   = "DOCK_RESTORE_S3_ENDPOINT"
	envRestoreBucket     = "DOCK_RESTORE_S3_BUCKET"
	envRestorePath       = "DOCK_RESTORE_S3_PATH"
	envRestoreAccessKey  = "DOCK_RESTORE_S3_ACCESS_KEY_ID"
	envRestoreSecretKey  = "DOCK_RESTORE_S3_SECRET_ACCESS_KEY"
	envRestoreRegion     = "DOCK_RESTORE_S3_REGION"
	envRestorePathStyle  = "DOCK_RESTORE_S3_PATH_STYLE"
	envRestorePassword   = "DOCK_RESTORE_PASSWORD"
)

// fetchReachTimeout is how long the first read of the repository may take,
// pulling the restic image included. restic itself retries an endpoint
// that doesn't answer for many minutes.
const fetchReachTimeout = 3 * time.Minute

// resticFunc runs restic with args in image, with env as its environment
// and its standard output streamed to stdout. It returns what restic wrote
// to standard error (its last part) and how it ended.
type resticFunc func(ctx context.Context, image string, env map[string]string, args []string, stdout io.Writer) (stderr string, err error)

// asker asks for a value on the terminal; secret ones are not echoed.
type asker func(label string, secret bool) (string, error)

// fetchBackupOptions is what `dock admin fetch-backup` was asked.
type fetchBackupOptions struct {
	out   string
	image string
	// getenv reads the environment, ask asks on the terminal (nil without
	// one) and restic runs restic; tests replace all three.
	getenv func(string) (string, bool)
	ask    asker
	restic resticFunc
}

// newAdminFetchBackupCmd is `dock admin fetch-backup`: the newest control
// plane backup out of the backup repository in a bucket, as the archive
// `dock admin restore` takes. install.sh --restore-from-bucket runs it on a
// fresh server after the old one is lost (ADR 0098).
func newAdminFetchBackupCmd() *cobra.Command {
	o := fetchBackupOptions{image: config.Default().Backup.ResticImage, getenv: os.LookupEnv, restic: dockerRestic}
	cmd := &cobra.Command{
		Use:   "fetch-backup",
		Short: "Fetch the newest control plane backup from the backup bucket (root; needs only Docker)",
		Long: `Fetch the newest control plane backup out of the backup repository in an S3
bucket and write it as an archive for install.sh --restore (dock admin
restore). It is for a server that is gone: it runs before Dock is installed,
needs only Docker, and reads nothing from this machine.

The repository comes from the environment; what is missing is asked on the
terminal (the secret key and the password without echo):

  DOCK_RESTORE_S3_ENDPOINT           the store's URL, e.g. https://fsn1.your-objectstorage.com
  DOCK_RESTORE_S3_BUCKET             the bucket
  DOCK_RESTORE_S3_PATH               the folder in the bucket (may be empty)
  DOCK_RESTORE_S3_ACCESS_KEY_ID      the bucket's keys
  DOCK_RESTORE_S3_SECRET_ACCESS_KEY
  DOCK_RESTORE_S3_REGION             optional
  DOCK_RESTORE_S3_PATH_STYLE         optional: 0 puts the bucket in the host name
                                     (default 1: in the path, as Dock's storage connections)
  DOCK_RESTORE_PASSWORD              the backup repository's password
  DOCK_RESTORE_REPOSITORY            instead of endpoint, bucket and folder: the whole
                                     restic URL, s3:https://<endpoint>/<bucket>/<folder>

The archive is streamed to FILE.partial, checked from start to end, then
renamed: a fetch that fails leaves no file. It holds every secret of the
platform, is created 0600 and never overwrites a file.`,
		Example: `  sudo dock admin fetch-backup -o /root/dock.tar
  curl -fsSL https://dock.nordtempo.com/install.sh | sudo bash -s -- --restore-from-bucket`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
				o.ask = terminalAsker(f, cmd.ErrOrStderr())
			}
			return fetchBackup(cmd.Context(), cmd.ErrOrStderr(), o)
		},
	}
	cmd.Flags().StringVarP(&o.out, "output", "o", "", "archive to write; default ./dock-recovered-<time>.tar")
	cmd.Flags().StringVar(&o.image, "restic-image", o.image, "image with the restic binary")
	return cmd
}

// terminalAsker asks on the terminal in: the question goes to out, a
// secret answer is read without echo.
func terminalAsker(in *os.File, out io.Writer) asker {
	lines := bufio.NewReader(in)
	return func(label string, secret bool) (string, error) {
		_, _ = fmt.Fprintf(out, "%s: ", label)
		if secret {
			b, err := term.ReadPassword(int(in.Fd()))
			_, _ = fmt.Fprintln(out)
			return strings.TrimSpace(string(b)), err
		}
		s, err := lines.ReadString('\n')
		if err != nil && (!errors.Is(err, io.EOF) || s == "") {
			return "", err
		}
		return strings.TrimSpace(s), nil
	}
}

// restoreRepo is the repository the DOCK_RESTORE_* environment names. What
// is missing is asked with ask; without one (no terminal) it is a usage
// error naming the variables.
func restoreRepo(getenv func(string) (string, bool), ask asker) (backup.Repo, error) {
	var missing []string
	// value is the variable's value, asked for when it is empty (or, for
	// one that may be empty, not set at all).
	value := func(name, label string, secret, mayBeEmpty bool) (string, error) {
		v, set := getenv(name)
		if v = strings.TrimSpace(v); v != "" || (set && mayBeEmpty) {
			return v, nil
		}
		if ask == nil {
			if !mayBeEmpty {
				missing = append(missing, name)
			}
			return "", nil
		}
		for {
			v, err := ask(label, secret)
			if err != nil {
				return "", fmt.Errorf("read %s from the terminal: %w", name, err)
			}
			if v != "" || mayBeEmpty {
				return v, nil
			}
		}
	}

	var repo backup.Repo
	var err error
	if repo.URL, _ = getenv(envRestoreRepository); strings.TrimSpace(repo.URL) != "" {
		repo.URL = strings.TrimSpace(repo.URL)
		if _, _, _, err := backup.ParseS3URL(repo.URL); err != nil {
			return repo, usageError(envRestoreRepository+": "+err.Error(), "It is the restic URL of the backup repository: s3:https://<endpoint>/<bucket>/<folder>.")
		}
	} else {
		var endpoint, bucket, folder string
		if endpoint, err = value(envRestoreEndpoint, "S3 endpoint (like https://fsn1.your-objectstorage.com)", false, false); err != nil {
			return repo, err
		}
		if bucket, err = value(envRestoreBucket, "Bucket", false, false); err != nil {
			return repo, err
		}
		if folder, err = value(envRestorePath, "Folder in the bucket (Enter for none)", false, true); err != nil {
			return repo, err
		}
		if folder = strings.Trim(folder, "/"); folder != "" {
			if err := backup.ValidPath(folder); err != nil {
				return repo, usageError(envRestorePath+": "+err.Error(), "It is the folder the backup repository is in, as the storage connection had it.")
			}
		}
		repo.URL = backup.S3URL(endpoint, bucket, folder)
	}
	if repo.AccessKeyID, err = value(envRestoreAccessKey, "Access key ID", false, false); err != nil {
		return repo, err
	}
	if repo.SecretAccessKey, err = value(envRestoreSecretKey, "Secret access key (not shown)", true, false); err != nil {
		return repo, err
	}
	if repo.Password, err = value(envRestorePassword, "Backup repository password (not shown)", true, false); err != nil {
		return repo, err
	}
	if len(missing) > 0 {
		return repo, usageError("missing "+strings.Join(missing, ", "),
			"Set them in the environment, or run it on a terminal to be asked. "+envRestoreRepository+" (s3:https://<endpoint>/<bucket>/<folder>) can stand in for endpoint, bucket and folder.")
	}
	if err := backup.ValidateURL(repo.URL); err != nil {
		return repo, usageError(err.Error(), "Check "+envRestoreEndpoint+" and "+envRestoreBucket+".")
	}
	region, _ := getenv(envRestoreRegion)
	repo.Region = strings.TrimSpace(region)
	// Path style unless told otherwise, as Dock's storage connections.
	style, _ := getenv(envRestorePathStyle)
	switch strings.ToLower(strings.TrimSpace(style)) {
	case "", "1", "true":
		repo.PathStyle = true
	case "0", "false":
	default:
		return repo, usageError(envRestorePathStyle+" must be 1 or 0", "1 addresses the bucket in the URL's path, 0 in the host name.")
	}
	return repo, nil
}

// resticSnapshot is one entry of `restic snapshots --json`.
type resticSnapshot struct {
	ID      string    `json:"id"`
	ShortID string    `json:"short_id"`
	Time    time.Time `json:"time"`
}

// fetchBackup finds the newest control plane snapshot in the repository
// the environment names and writes its archive to o.out: streamed to
// o.out.partial (0600), verified, then renamed. An existing file is never
// overwritten and a failed fetch leaves none.
func fetchBackup(ctx context.Context, stderr io.Writer, o fetchBackupOptions) error {
	path := o.out
	if path == "" {
		path = fmt.Sprintf("dock-recovered-%s.tar", time.Now().Format("20060102-150405"))
	}
	if _, err := os.Lstat(path); err == nil {
		return usageError(path+" already exists", "Pick another name with -o.")
	}
	repo, err := restoreRepo(o.getenv, o.ask)
	if err != nil {
		return err
	}
	env := repo.Env()
	// The bucket addressing travels as restic options, which the backup
	// jobs' shell adds to every command (backup's prelude); here they go
	// on the command line.
	opts := strings.Fields(env["DOCK_RESTIC_OPTS"])
	delete(env, "DOCK_RESTIC_OPTS")
	// The server that took the locks is gone: a lock it left must not
	// stop a read.
	opts = append(opts, "--no-lock")
	where := strings.TrimPrefix(repo.URL, "s3:")

	_, _ = fmt.Fprintf(stderr, "Opening the backup repository at %s...\n", where)
	reach, cancel := context.WithTimeout(ctx, fetchReachTimeout)
	var list bytes.Buffer
	errText, err := o.restic(reach, o.image, env, slices.Concat(opts, []string{"snapshots", "--tag", backup.ControlPlaneTag, "--latest", "1", "--json"}), &list)
	timedOut := reach.Err() != nil && ctx.Err() == nil
	cancel()
	if err != nil {
		return resticError(where, errText, err, timedOut)
	}
	var snaps []resticSnapshot
	if err := json.Unmarshal(bytes.TrimSpace(list.Bytes()), &snaps); err != nil {
		return fmt.Errorf("read restic's list of snapshots: %w", err)
	}
	if len(snaps) == 0 {
		return &output.Error{
			Exit:    output.ExitNotFound,
			Message: "the repository at " + where + " opened, but it holds no control plane backup",
			Hint:    "Check the folder (" + envRestorePath + "): it must be the one the control plane backed up into, as its storage connection had it. A repository that only ever held one server's local backups has none.",
		}
	}
	snap := slices.MaxFunc(snaps, func(a, b resticSnapshot) int { return a.Time.Compare(b.Time) })
	if snap.ShortID == "" {
		snap.ShortID = shortID(snap.ID)
	}

	_, _ = fmt.Fprintf(stderr, "Fetching snapshot %s, taken %s...\n", snap.ShortID, snap.Time.Local().Format("2006-01-02 15:04 MST"))
	partial := path + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(partial)
		return err
	}
	cw := &countWriter{w: f}
	if errText, err := o.restic(ctx, o.image, env, slices.Concat(opts, []string{"dump", snap.ID, "/" + backup.ControlPlaneFile}), cw); err != nil {
		return fail(resticError(where, errText, err, false))
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	m, err := cpbackup.Verify(ctx, f)
	if err != nil {
		return fail(&output.Error{
			Exit:    output.ExitError,
			Message: fmt.Sprintf("snapshot %s came out of the repository, but its archive doesn't check out: %v", snap.ShortID, err),
			Hint:    "Nothing was written. Run it again; if it fails the same way, that backup is damaged: `restic snapshots --tag " + backup.ControlPlaneTag + "` lists older ones to dump by hand.",
			Err:     err,
		})
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(partial, path); err != nil {
		_ = os.Remove(partial)
		return err
	}
	_, _ = fmt.Fprintf(stderr, "Wrote %s (%s): snapshot %s, taken %s (%s) on %s, dock %s, schema %d.\n",
		path, bytesize(cw.n), snap.ShortID, snap.Time.Local().Format("2006-01-02 15:04 MST"), ago(snap.Time), m.Hostname, m.Version, m.Schema)
	_, _ = fmt.Fprintln(stderr, secretsWarning)
	return nil
}

// resticError turns a failed restic run into an error that says what was
// wrong (Docker, the keys, the password, the folder or the network) and
// what to do next, keeping restic's last lines. where is the repository,
// timedOut that it didn't answer in fetchReachTimeout.
func resticError(where, stderr string, err error, timedOut bool) error {
	tail := lastLines(stderr, 5)
	with := func(msg string) string {
		if tail == "" {
			return msg
		}
		return msg + ":\n" + tail
	}
	low := strings.ToLower(stderr)
	has := func(subs ...string) bool {
		return slices.ContainsFunc(subs, func(s string) bool { return strings.Contains(low, s) })
	}
	exit, exited := errors.AsType[*osexec.ExitError](err)
	switch {
	case errors.Is(err, osexec.ErrNotFound):
		return &output.Error{Exit: output.ExitNotFound, Err: err, Message: "docker is not installed; restic runs in a container",
			Hint: "Install Docker Engine (https://docs.docker.com/engine/install/), or use install.sh --restore-from-bucket, which installs it first."}
	case timedOut:
		return &output.Error{Exit: output.ExitUnreachable, Err: err, Message: with(fmt.Sprintf("the bucket at %s didn't answer in %s", where, fetchReachTimeout)),
			Hint: "Check " + envRestoreEndpoint + " and that this server can reach it (DNS, firewall), then run it again."}
	case has("wrong password", "no key found"):
		return &output.Error{Exit: output.ExitAuth, Err: err, Message: with("the bucket opened, but the repository password is wrong"),
			Hint: "Set " + envRestorePassword + " to the backup repository's password, the one kept outside the platform when backups were set up."}
	case has("access denied", "accessdenied", "invalidaccesskeyid", "signaturedoesnotmatch", "forbidden", "access key"):
		return &output.Error{Exit: output.ExitAuth, Err: err, Message: with("the bucket at " + where + " refused the keys"),
			Hint: "Check " + envRestoreAccessKey + " and " + envRestoreSecretKey + ", that the keys may read the bucket, and " + envRestoreRegion + " if the store wants one."}
	case has("dial tcp", "no such host", "connection refused", "i/o timeout", "tls:", "x509"):
		return &output.Error{Exit: output.ExitUnreachable, Err: err, Message: with("can't reach the bucket at " + where),
			Hint: "Check " + envRestoreEndpoint + " and that this server can reach it (DNS, firewall), then run it again."}
	case has("repository does not exist", "is there a repository at", "nosuchbucket", "bucket does not exist"):
		return &output.Error{Exit: output.ExitNotFound, Err: err, Message: with("there is no backup repository at " + where),
			Hint: "Check the bucket and the folder (" + envRestoreBucket + ", " + envRestorePath + "): they must be the ones the storage connection had. If the bucket's name is right, try " + envRestorePathStyle + "=0."}
	case exited && exit.ExitCode() == 125:
		return &output.Error{Exit: output.ExitError, Err: err, Message: with("docker couldn't run the restic image"),
			Hint: "Check that Docker runs (docker info) and can pull images, then run it again. --restic-image names another image."}
	}
	return &output.Error{Exit: output.ExitError, Err: err, Message: with("restic failed on the repository at " + where),
		Hint: "Fix what restic says above and run it again."}
}

// lastLines is the last n lines of s that say something.
func lastLines(s string, n int) string {
	var lines []string
	for l := range strings.SplitSeq(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, "  "+l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// dockerArgs is the docker command line that runs restic with args in
// image. The environment is passed by name only (-e NAME): the values are
// in the docker client's own environment (dockerEnv), never in argv, which
// every user of the machine can read.
func dockerArgs(image string, env map[string]string, args []string) []string {
	argv := []string{"run", "--rm"}
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		argv = append(argv, "-e", name)
	}
	return append(append(argv, image), args...)
}

// dockerEnv is the docker client's environment for dockerArgs: this
// process's, with env on top.
func dockerEnv(env map[string]string) []string {
	out := os.Environ()
	for name, v := range env {
		out = append(out, name+"="+v)
	}
	return out
}

// dockerRestic is the resticFunc that runs restic with `docker run --rm`.
// Canceling ctx sends docker SIGTERM, which it passes on to restic, so the
// container ends with the command.
func dockerRestic(ctx context.Context, image string, env map[string]string, args []string, stdout io.Writer) (string, error) {
	cmd := osexec.CommandContext(ctx, "docker", dockerArgs(image, env, args)...)
	cmd.Env = dockerEnv(env)
	cmd.Stdout = stdout
	errTail := &tailWriter{max: 8 << 10}
	cmd.Stderr = errTail
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 15 * time.Second
	err := cmd.Run()
	return errTail.String(), err
}

// tailWriter keeps the last max bytes written to it.
type tailWriter struct {
	max int
	buf []byte
}

// Write appends p, dropping the oldest bytes past max.
func (t *tailWriter) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

// String is what was kept.
func (t *tailWriter) String() string { return string(t.buf) }
