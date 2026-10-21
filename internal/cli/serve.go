package cli

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/platform/internal/api"
	"github.com/NordTempo/platform/internal/config"
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

func serve(ctx context.Context, cfg config.Config) error {
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.New(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("platform daemon listening", "addr", cfg.Listen)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
