package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/api"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/version"
)

func newServeCmd() *cobra.Command {
	configPath := config.DefaultPath
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the control plane daemon (API, UI and local node runtime)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			return serve(cmd.Context(), cfg)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", configPath, "path to the daemon config file")
	return cmd
}

// openState opens the database (running migrations) and the secrets key,
// creating the data directory on first start.
func openState(ctx context.Context, cfg config.Config) (*store.Store, *secrets.Box, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, nil, err
	}
	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return nil, nil, err
	}
	box, err := secrets.LoadOrCreate(cfg.KeyPath())
	if err != nil {
		_ = st.Close()
		return nil, nil, err
	}
	return st, box, nil
}

func serve(ctx context.Context, cfg config.Config) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := checkLoopback(cfg.InternalListen); err != nil {
		return err
	}
	st, box, err := openState(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	rt, err := newRuntime(cfg)
	if err != nil {
		return err
	}
	c, err := core.New(ctx, core.Options{Config: cfg, Store: st, Secrets: box, Runtime: rt, Logger: log})
	if err != nil {
		return err
	}
	if err := c.Start(ctx); err != nil {
		return err
	}
	defer c.Close()

	servers := []*http.Server{
		{
			Addr:              cfg.Listen,
			Handler:           api.New(c, api.Options{MaxUploadBytes: int64(cfg.MaxUploadMB) << 20, Logger: log}),
			ReadHeaderTimeout: 10 * time.Second,
		},
		{
			Addr:              cfg.InternalListen,
			Handler:           api.Internal(c, log),
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
	errc := make(chan error, len(servers))
	for _, srv := range servers {
		go func() { errc <- srv.ListenAndServe() }()
	}
	log.Info("dock daemon listening", "version", version.Version, "api", cfg.Listen, "internal", cfg.InternalListen, "data", cfg.DataDir)

	select {
	case err = <-errc:
	case <-ctx.Done():
		log.Info("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range servers {
		if serr := srv.Shutdown(shutdownCtx); serr != nil && !errors.Is(serr, http.ErrServerClosed) && err == nil {
			err = serr
		}
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// checkLoopback refuses to serve internal endpoints anywhere but loopback.
func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("internal_listen %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("internal_listen %q must be a loopback address (127.0.0.1 or ::1)", addr)
	}
	return nil
}
