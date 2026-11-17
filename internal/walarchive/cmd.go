package walarchive

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

// Exit statuses of wal fetch: restore_command's "not archived" ends
// recovery's search for a file; above 125, Postgres aborts the recovery
// instead of taking a download error for the end of the archive.
const (
	exitNotArchived = 1
	exitAbort       = 126
)

// Command is the hidden first argument that makes the dock binary run
// one of the commands below (ADR 0093). Dock bind-mounts its own binary
// read-only into the archiver and into base backup and restore jobs
// (node.DockPath), which run it there:
//
//	dock __wal serve PGDATA              the archiver
//	dock __wal base PGDATA               a base backup's tar to stdout
//	dock __wal unpack DIR                a base backup's tar from stdin
//	dock __wal index START SEGMENT_BYTES TARGET_MS FILE
//	dock __wal fetch FILE DEST           restore_command
//
// The archive's place and key come in DOCK_WAL_* variables (Config).
// dock has the TLS stack the storage needs already; dock-sync, which is
// embedded, would grow by it.
const Command = "__wal"

// Main runs one command (its arguments after Command) and returns the
// exit status.
func Main(args []string) int {
	if len(args) == 0 {
		return usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if args[0] == "unpack" && len(args) == 2 {
		return report(Unpack(os.Stdin, args[1]))
	}
	if args[0] == "base" && len(args) == 2 {
		b, err := RunBase(ctx, args[1], os.Stdout)
		if err == nil {
			fmt.Fprintln(os.Stderr, b)
		}
		return report(err)
	}
	cfg, err := ConfigFromEnv(os.Getenv)
	if err != nil {
		return report(err)
	}
	switch {
	case args[0] == "serve" && len(args) == 2:
		return report(serveMain(ctx, cfg, args[1]))
	case args[0] == "index" && len(args) == 5:
		return report(indexMain(ctx, cfg, args[1], args[2], args[3], args[4]))
	case args[0] == "fetch" && len(args) == 3:
		err := fetchMain(ctx, cfg, args[1], args[2])
		switch {
		case errors.Is(err, ErrNotArchived):
			return exitNotArchived
		case err != nil:
			fmt.Fprintln(os.Stderr, "dock: "+err.Error())
			return exitAbort
		}
		return 0
	}
	return usage()
}

func usage() int {
	fmt.Fprintln(os.Stderr, "usage: dock "+Command+" serve|base|unpack|index|fetch ...")
	return 2
}

// report prints err and returns the exit status.
func report(err error) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, "dock: "+err.Error())
		return 1
	}
	return 0
}

// serve runs the archiver until it is stopped.
func serveMain(ctx context.Context, cfg Config, pgdata string) error {
	a := NewArchiver(cfg, pgdata)
	go func() {
		if err := a.Load(ctx); err != nil {
			slog.Warn("read the archive", "err", err)
		}
	}()
	srv := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(Port))
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	slog.Info("archiving", "prefix", cfg.Prefix, "port", Port)
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// index lists the archive into file and checks it holds every commit
// from the base backup's start segment up to the target time.
func indexMain(ctx context.Context, cfg Config, start, segBytes, targetMS, file string) error {
	seg, err := strconv.ParseInt(segBytes, 10, 64)
	if err != nil {
		return fmt.Errorf("segment size %q: %w", segBytes, err)
	}
	ms, err := strconv.ParseInt(targetMS, 10, 64)
	if err != nil {
		return fmt.Errorf("target %q: %w", targetMS, err)
	}
	ix, err := List(ctx, cfg)
	if err != nil {
		return fmt.Errorf("list the WAL archive: %w", err)
	}
	if err := ix.Reaches(start, seg, time.UnixMilli(ms)); err != nil {
		return err
	}
	return WriteIndex(file, ix)
}

// fetch is restore_command: the file from the archive, with a few tries
// for a store that hiccups.
func fetchMain(ctx context.Context, cfg Config, file, dest string) error {
	index := os.Getenv("DOCK_WAL_INDEX")
	var err error
	for try := range 4 {
		if try > 0 {
			time.Sleep(time.Duration(try) * 2 * time.Second)
		}
		if err = Fetch(ctx, cfg, index, file, dest); err == nil || errors.Is(err, ErrNotArchived) {
			return err
		}
	}
	return err
}
