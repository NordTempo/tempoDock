//go:build !dockcli

package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/cpbackup"
)

// restoreEnv is a whole DOCK_RESTORE_* environment for a bucket.
var restoreEnv = map[string]string{
	envRestoreEndpoint:  "https://s3.example.com/",
	envRestoreBucket:    "backups",
	envRestorePath:      "/dock/prod/",
	envRestoreAccessKey: "AKIDEXAMPLE",
	envRestoreSecretKey: "s3-secret-value",
	envRestoreRegion:    "eu-central-1",
	envRestorePassword:  "repo-password-value",
}

// envFrom is a getenv over m, without the names in drop.
func envFrom(m map[string]string, drop ...string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if slices.Contains(drop, name) {
			return "", false
		}
		v, ok := m[name]
		return v, ok
	}
}

// controlPlaneTar is a minimal control plane archive, as restic dumps it:
// the manifest, the database and the secrets key.
func controlPlaneTar(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range [][2]string{
		{cpbackup.ManifestName, `{"format":1,"version":"v1.2.3","schema":1,"hostname":"old-server"}`},
		{"data/" + cpbackup.DBFile, "database"},
		{"data/" + cpbackup.SecretKeyFile, "0123456789abcdef"},
	} {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: f[0], Size: int64(len(f[1])), Mode: 0o600}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The environment becomes the repository: the URL from endpoint, bucket
// and folder (or given whole), path style unless turned off, and a usage
// error naming what is missing when there is no terminal to ask on.
func TestFetchBackupRepo(t *testing.T) {
	repo, err := restoreRepo(envFrom(restoreEnv), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := backup.Repo{
		URL: "s3:https://s3.example.com/backups/dock/prod", AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "s3-secret-value",
		Region: "eu-central-1", PathStyle: true, Password: "repo-password-value",
	}
	if repo != want {
		t.Errorf("repo = %+v, want %+v", repo, want)
	}

	whole := map[string]string{
		envRestoreRepository: "s3:https://s3.example.com/backups", envRestorePathStyle: "0",
		envRestoreAccessKey: "a", envRestoreSecretKey: "b", envRestorePassword: "c",
	}
	repo, err = restoreRepo(envFrom(whole), nil)
	if err != nil || repo.URL != "s3:https://s3.example.com/backups" || repo.PathStyle {
		t.Errorf("a whole repository URL: %+v, %v", repo, err)
	}

	// The folder may be left out; the password and the bucket may not.
	_, err = restoreRepo(envFrom(restoreEnv, envRestorePath, envRestorePassword, envRestoreBucket), nil)
	oe, ok := errors.AsType[*output.Error](err)
	if !ok || oe.Exit != output.ExitUsage || !strings.Contains(oe.Message, envRestorePassword) ||
		!strings.Contains(oe.Message, envRestoreBucket) || strings.Contains(oe.Message, envRestorePath) {
		t.Errorf("missing variables: %v", err)
	}

	// On a terminal, what is missing is asked, secrets without echo.
	asked := map[string]bool{}
	repo, err = restoreRepo(envFrom(restoreEnv, envRestorePath, envRestorePassword), func(label string, secret bool) (string, error) {
		asked[label] = secret
		if secret {
			return "typed-password", nil
		}
		return "typed/folder", nil
	})
	if err != nil || repo.Password != "typed-password" || repo.URL != "s3:https://s3.example.com/backups/typed/folder" || len(asked) != 2 {
		t.Errorf("asked %v: %+v, %v", asked, repo, err)
	}
}

// Secrets reach restic through the docker client's environment, never its
// command line; the bucket addressing is on restic's.
func TestFetchBackupNoSecretInArgv(t *testing.T) {
	var argv, env []string
	o := fetchBackupOptions{
		out: filepath.Join(t.TempDir(), "dock.tar"), image: "restic:test", getenv: envFrom(restoreEnv),
		restic: func(_ context.Context, image string, e map[string]string, args []string, stdout io.Writer) (string, error) {
			argv = append(argv, dockerArgs(image, e, args)...)
			env = dockerEnv(e)
			if slices.Contains(args, "snapshots") {
				_, err := io.WriteString(stdout, `[{"id":"4cf53263aaaa","short_id":"4cf53263","time":"2026-11-18T03:00:00Z","hostname":"dock"}]`)
				return "", err
			}
			_, err := stdout.Write(controlPlaneTar(t))
			return "", err
		},
	}
	var stderr bytes.Buffer
	if err := fetchBackup(context.Background(), &stderr, o); err != nil {
		t.Fatal(err)
	}
	line := strings.Join(argv, " ")
	for _, secret := range []string{"s3-secret-value", "repo-password-value", "AKIDEXAMPLE"} {
		if strings.Contains(line, secret) {
			t.Errorf("%s is on the command line: %s", secret, line)
		}
	}
	for _, want := range []string{"-e RESTIC_PASSWORD ", "-e AWS_SECRET_ACCESS_KEY ", "restic:test -o s3.bucket-lookup=path --no-lock snapshots --tag dock-control-plane", "dump 4cf53263aaaa /dock-control-plane.tar"} {
		if !strings.Contains(line, want) {
			t.Errorf("no %q on the command line: %s", want, line)
		}
	}
	if !slices.Contains(env, "RESTIC_PASSWORD=repo-password-value") || !slices.Contains(env, "AWS_SECRET_ACCESS_KEY=s3-secret-value") {
		t.Error("the secrets are not in docker's environment")
	}
	fi, err := os.Stat(o.out)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the archive: %v, %v", fi, err)
	}
	for _, want := range []string{"snapshot 4cf53263", "old-server", "dock v1.2.3, schema 1", "every secret"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("no %q in: %s", want, stderr.String())
		}
	}
	// A second fetch never overwrites it.
	if err := fetchBackup(context.Background(), io.Discard, o); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("fetching over a file: %v", err)
	}
}

// A repository that opens but holds no control plane snapshot says so, and
// a dump that doesn't verify (cut short) leaves no file behind.
func TestFetchBackupFailures(t *testing.T) {
	out := filepath.Join(t.TempDir(), "dock.tar")
	dump := controlPlaneTar(t)
	snapshots := "[]"
	o := fetchBackupOptions{
		out: out, getenv: envFrom(restoreEnv),
		restic: func(_ context.Context, _ string, _ map[string]string, args []string, stdout io.Writer) (string, error) {
			if slices.Contains(args, "snapshots") {
				_, err := io.WriteString(stdout, snapshots)
				return "", err
			}
			_, err := stdout.Write(dump[:len(dump)/3])
			return "", err
		},
	}
	err := fetchBackup(context.Background(), io.Discard, o)
	if oe, ok := errors.AsType[*output.Error](err); !ok || oe.Exit != output.ExitNotFound ||
		!strings.Contains(oe.Message, "holds no control plane backup") || !strings.Contains(oe.Hint, envRestorePath) {
		t.Errorf("no snapshot: %v", err)
	}

	snapshots = `[{"id":"4cf53263aaaa","short_id":"4cf53263","time":"2026-11-18T03:00:00Z"}]`
	if err := fetchBackup(context.Background(), io.Discard, o); err == nil || !strings.Contains(err.Error(), "doesn't check out") {
		t.Errorf("a dump cut short: %v", err)
	}
	if left, _ := filepath.Glob(out + "*"); len(left) != 0 {
		t.Errorf("a failed fetch left %v", left)
	}

	// restic's own words decide what the error says to do.
	err = resticError("s3.example.com/backups", "Fatal: wrong password or no key found\n", errors.New("exit status 12"), false)
	if oe, ok := errors.AsType[*output.Error](err); !ok || oe.Exit != output.ExitAuth || !strings.Contains(oe.Hint, envRestorePassword) || !strings.Contains(oe.Message, "wrong password or no key found") {
		t.Errorf("wrong password: %v", err)
	}
	err = resticError("s3.example.com/backups", "Fatal: unable to open config file: Stat: dial tcp: lookup s3.example.com: no such host\nIs there a repository at the following location?\n", errors.New("exit status 1"), false)
	if oe, ok := errors.AsType[*output.Error](err); !ok || oe.Exit != output.ExitUnreachable || !strings.Contains(oe.Hint, envRestoreEndpoint) {
		t.Errorf("unreachable: %v", err)
	}
}
