// Command dock-demo runs Dock's public demo (ADR 0100), the entrypoint of
// its image (deploy/demo): it empties the data dir, creates the first
// user, starts `dock serve` on the fake runtime, seeds it through the API
// with a small company's projects, and creates the demo's shared user
// last, which makes the demo ready (GET /api/v1/auth/demo). Every -reset
// it stops the daemon and does it all again, so whatever visitors
// changed is gone; sooner when the data dir grows past -max-disk-mb,
// since it lives on the disk the server's real apps use. It passes SIGTERM and SIGINT on, and exits when the
// daemon exits on its own (Dock then starts the container again).
//
//	dock-demo -config /demo/config.toml -dock /usr/local/bin/dock
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/NordTempo/tempoDock/internal/config"
)

func main() {
	configPath := flag.String("config", "/demo/config.toml", "the demo daemon's config file; it must have [demo] user")
	dock := flag.String("dock", "dock", "the dock binary")
	content := flag.String("content", "/demo", "directory with the demo's logs.txt and findings.jsonl")
	reset := flag.Duration("reset", time.Hour, "how often the demo starts again from its seed")
	maxDisk := flag.Int64("max-disk-mb", 1024, "start again early when the data dir grows past this")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	d := demo{log: log, configPath: *configPath, dock: *dock, content: *content, maxDisk: *maxDisk << 20}
	if err := d.loop(ctx, *reset); err != nil {
		log.Error("demo", "err", err)
		os.Exit(1)
	}
}

// demo runs one demo daemon after another.
type demo struct {
	log                       *slog.Logger
	configPath, dock, content string
	// maxDisk is the data dir's size, in bytes, past which the demo
	// starts again before its next reset.
	maxDisk int64
}

// errReset ends one demo for the next.
var errReset = errors.New("reset")

// loop runs fresh demos until ctx ends or a daemon exits on its own.
// The reset timer and run's disk check are the demo's only periodic
// work: about 2 seconds of downtime an hour, when the daemon restarts
// and is seeded again.
func (d demo) loop(ctx context.Context, every time.Duration) error {
	for {
		runCtx, cancel := context.WithTimeoutCause(ctx, every, errReset)
		err := d.run(runCtx)
		cancel()
		if err != nil || ctx.Err() != nil {
			return err
		}
		d.log.Info("resetting the demo")
	}
}

// run starts a fresh demo and waits for its daemon to exit, or stops it
// when ctx ends or its data dir grows past maxDisk. It returns nil when
// it stopped the daemon, and the loop starts the next demo.
func (d demo) run(ctx context.Context) error {
	cfg, err := config.Load(d.configPath)
	if err != nil {
		return err
	}
	if !cfg.Demo.Enabled() {
		return fmt.Errorf("%s has no [demo] user: dock-demo empties the data dir, so it only runs a demo's", d.configPath)
	}
	if err := os.RemoveAll(cfg.DataDir); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	password, err := bootstrap(ctx, d.dock, d.configPath)
	if err != nil {
		return err
	}
	spool := filepath.Join(cfg.DataDir, "demo-spool.jsonl")
	serve := exec.Command(d.dock, "serve", "--config", d.configPath)
	serve.Stdout, serve.Stderr = os.Stdout, os.Stderr
	serve.Env = append(os.Environ(),
		"DOCK_FAKE_RUNTIME=1",
		"DOCK_FAKE_LOGS="+filepath.Join(d.content, "logs.txt"),
		"DOCK_FAKE_FINDINGS="+filepath.Join(d.content, "findings.jsonl"),
		"DOCK_FAKE_SPOOL="+spool,
		"DOCK_FAKE_PENDING_DOMAINS="+pendingDomain,
	)
	if err := serve.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- serve.Wait() }()

	seeded := make(chan error, 1)
	go func() {
		seeded <- seed(ctx, d.log, seedConfig{
			URL:      "http://" + dialable(cfg.Listen),
			Password: password,
			DemoUser: cfg.Demo.User,
			Spool:    spool,
		})
	}()
	// stop ends the daemon and returns how it exited, which callers
	// ignore: they stopped it.
	stop := func() error {
		_ = serve.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-exited:
			return err
		case <-time.After(20 * time.Second):
			_ = serve.Process.Kill()
			return <-exited
		}
	}
	// Once a minute: the data dir is small (a database and some logs), so
	// walking it is cheap, and a visitor filling the disk takes minutes.
	disk := time.NewTicker(time.Minute)
	defer disk.Stop()
	for {
		select {
		case <-disk.C:
			size, err := dirSize(cfg.DataDir)
			if err != nil {
				d.log.Warn("measure the data dir", "err", err)
			} else if size > d.maxDisk {
				d.log.Warn("the data dir is too large; starting the demo again", "bytes", size)
				_ = stop()
				return nil
			}
		case err := <-exited:
			return fmt.Errorf("dock serve exited: %w", err)
		case err := <-seeded:
			seeded = nil
			if err != nil && ctx.Err() == nil {
				_ = stop()
				return fmt.Errorf("seed the demo: %w", err)
			}
			if err == nil {
				d.log.Info("the demo is ready", "user", cfg.Demo.User)
			}
		case <-ctx.Done():
			_ = stop()
			return nil
		}
	}
}

// bootstrapPassword finds the first user's password in `dock admin
// bootstrap`'s output.
var bootstrapPassword = regexp.MustCompile(`with password (\S+) \(shown once\)`)

// bootstrap creates the first user, who seeds the demo, and returns its
// password.
func bootstrap(ctx context.Context, dock, configPath string) (string, error) {
	cmd := exec.CommandContext(ctx, dock, "admin", "bootstrap", "--config", configPath, "--email", seedUser)
	cmd.Env = append(os.Environ(), "DOCK_FAKE_RUNTIME=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("dock admin bootstrap: %w: %s", err, stderr.String())
	}
	m := bootstrapPassword.FindStringSubmatch(stderr.String())
	if m == nil {
		return "", errors.New("dock admin bootstrap printed no password: " + stderr.String())
	}
	return m[1], nil
}

// dirSize adds up the sizes of the files under dir. Files that go away
// while it walks are skipped.
func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil || e.IsDir() {
			return err
		}
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// dialable is a listen address to connect to: 0.0.0.0 and [::] become
// loopback.
func dialable(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
