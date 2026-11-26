package updater

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/cpbackup"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/update"
)

// Host is the production System: systemctl, the release's install.sh,
// and health checks against this server's own daemon.
type Host struct {
	Config     config.Config
	ConfigPath string
	Node       bool
	// HealthTimeout bounds each health check.
	HealthTimeout time.Duration
}

// versionPath is the API's anonymous version endpoint (api.Prefix +
// "/version"); the updater doesn't link the API package.
const versionPath = "/api/v1/version"

// systemPath is install.sh's PATH: nothing from the unit's environment
// reaches it.
const systemPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

func systemctl(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
}

func (h Host) Stop(ctx context.Context, unit string) error  { return systemctl(ctx, "stop", unit) }
func (h Host) Start(ctx context.Context, unit string) error { return systemctl(ctx, "start", unit) }
func (h Host) DaemonReload(ctx context.Context) error       { return systemctl(ctx, "daemon-reload") }

// BinaryVersion runs `bin --version`, which prints "dock version vX.Y.Z".
func (h Host) BinaryVersion(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.Env = []string{"PATH=" + systemPath}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return "", errors.New("it printed no version")
	}
	return f[len(f)-1], nil
}

// Install runs install.sh as an upgrade: DOCK_BINARY and nothing else,
// so it keeps config.toml and the server's role as they are.
func (h Host) Install(ctx context.Context, script, bin, caddy string, log io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/bash", script)
	cmd.Dir = filepath.Dir(script)
	cmd.Env = []string{"PATH=" + systemPath, "HOME=/root", "LANG=C.UTF-8", "DOCK_BINARY=" + bin}
	if caddy != "" {
		cmd.Env = append(cmd.Env, "DOCK_CADDY="+caddy)
	}
	cmd.Stdout, cmd.Stderr = log, log
	return cmd.Run()
}

// Healthy polls once a second until the daemon runs version or the
// timeout passes: the API's version endpoint on a control plane, the
// node's connected file on a node. It only runs during an update.
func (h Host) Healthy(ctx context.Context, version string, since time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, h.HealthTimeout)
	defer cancel()
	check := h.apiVersion
	if h.Node {
		check = func(context.Context) (string, error) { return h.connected(since) }
	}
	var last error
	for {
		got, err := check(ctx)
		switch {
		case err == nil && got == version:
			return nil
		case err == nil:
			last = fmt.Errorf("it runs %s", got)
		default:
			last = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("not within %s: %w", h.HealthTimeout, last)
		case <-time.After(time.Second):
		}
	}
}

// apiVersion asks the API for its version, on this machine.
func (h Host) apiVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+localAddr(h.Config.Listen)+versionPath, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the API answered %s", resp.Status)
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&v); err != nil {
		return "", err
	}
	return v.Version, nil
}

// connected reads the version the node wrote once it reached its control
// plane after since.
func (h Host) connected(since time.Time) (string, error) {
	var c update.Connected
	if err := update.ReadJSON(update.Dir(h.Config.DataDir), update.ConnectedFile, &c); err != nil {
		return "", errors.New("the node hasn't reached its control plane")
	}
	if c.At.Before(since) {
		return "", errors.New("the node hasn't reached its control plane since it restarted")
	}
	return c.Version, nil
}

// localAddr turns a listen address into one to dial on this machine.
func localAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// Backup writes the control plane backup (the `dock admin backup`
// archive, without the registry), 0600, through a .partial file.
func (h Host) Backup(ctx context.Context, path string) error {
	db := h.Config.DBPath()
	defer fixOwner(h.Config.DataDir, db+"-wal", db+"-shm")
	partial := path + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(partial) }()
	gz := gzip.NewWriter(f)
	_, err = cpbackup.Write(ctx, gz, cpbackup.Source{
		DataDir: h.Config.DataDir, ConfigPath: h.ConfigPath,
		Snapshot: func(ctx context.Context, dst string) (store.SnapshotInfo, error) {
			return store.SnapshotFile(ctx, db, dst)
		},
	})
	if err == nil {
		err = gz.Close()
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(partial, path)
}

// RestoreDB puts the backup's database back and gives it to the data
// dir's owner.
func (h Host) RestoreDB(ctx context.Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := cpbackup.RestoreDB(ctx, f, h.Config.DataDir); err != nil {
		return err
	}
	fixOwner(h.Config.DataDir, h.Config.DBPath())
	return nil
}

// fixOwner gives files root created in the data dir to the dir's owner.
func fixOwner(dataDir string, files ...string) {
	o, err := ownerOf(dataDir)
	if err != nil || os.Geteuid() != 0 {
		return
	}
	for _, f := range files {
		_ = os.Lchown(f, o.uid, o.gid)
	}
}

// Notify tells the control plane on its internal listener that the
// result is written. If it isn't up, it reads the result when it starts.
func (h Host) Notify(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+localAddr(h.Config.InternalListen)+update.NotifyPath, nil)
	if err != nil {
		return
	}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
	}
}

var _ System = Host{}

// IsNode reports whether this server runs dock-node.service: a node.
func IsNode(unitDir string) bool {
	_, err := os.Stat(filepath.Join(unitDir, nodeUnit))
	return err == nil
}
