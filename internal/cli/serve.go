//go:build !dockcli

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
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/cpbackup"
	"github.com/NordTempo/tempoDock/internal/node/caddy"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/registry"
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
			return serve(cmd.Context(), cfg, configPath)
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

// serve runs the control plane until ctx ends: the API and UI (on
// [listen], and on [public_listen] for Caddy), the loopback-only internal
// endpoints and, with [node] listen set, the port nodes connect to. It holds the data dir's lock while it runs, and
// refuses to start a control plane that moved to another server (ADR
// 0021). configPath is archived by the control plane's own backups.
func serve(ctx context.Context, cfg config.Config, configPath string) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := checkLoopback("internal_listen", cfg.InternalListen); err != nil {
		return err
	}
	if err := checkLoopback("public_listen", cfg.PublicListen); err != nil {
		return err
	}
	if err := checkNodeListen(cfg.Node.Listen); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	if err := cpbackup.CheckMoved(cfg.DataDir); err != nil {
		return &output.Error{Exit: output.ExitMoved, Err: err}
	}
	unlock, err := cpbackup.Lock(cfg.DataDir)
	if errors.Is(err, cpbackup.ErrLocked) {
		return fmt.Errorf("another dock serve is running with data dir %s", cfg.DataDir)
	}
	if err != nil {
		return err
	}
	defer unlock()
	st, box, err := openState(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	// The link key identifies the control plane to nodes, and gives this
	// server its Caddy marker.
	linkID, err := remote.LoadOrCreateIdentity(cfg.LinkKeyPath())
	if err != nil {
		return err
	}
	rt, _, err := newRuntime(ctx, cfg, remote.ServerMarker(linkID.Fingerprint()), log)
	if err != nil {
		return err
	}
	opts := core.Options{Config: cfg, Store: st, Secrets: box, Runtime: rt, Logger: log, ConfigPath: configPath}
	var signer *registry.Signer
	if cfg.Registry.Enabled {
		if signer, err = registrySigner(cfg); err != nil {
			return err
		}
		if opts.Registry, err = platformRegistry(cfg, signer); err != nil {
			return err
		}
	}
	c, hub, err := startCore(ctx, opts, linkID)
	if err != nil {
		return err
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var registryToken http.Handler
	if signer != nil {
		registryToken = signer.Handler()
	}
	handler := api.New(c, api.Options{MaxUploadBytes: int64(cfg.MaxUploadMB) << 20, Logger: log})
	hooks := api.Hooks(c, log)
	servers := []*http.Server{
		{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second},
		// Dock's public address, through Caddy (ADR 0029), with GitHub's
		// webhooks (ADR 0030). Served whether or not one is set, so
		// setting one needs no restart.
		{Addr: cfg.PublicListen, Handler: api.Public(handler, hooks), ReadHeaderTimeout: 10 * time.Second},
		{
			Addr:              cfg.InternalListen,
			Handler:           api.Internal(c, log, registryToken, caddy.AccessHandler()),
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
	if cfg.Hooks.PublicHost != "" {
		// GitHub webhooks, the only thing Caddy exposes publicly (ADR 0018).
		servers = append(servers, &http.Server{
			Addr: cfg.Hooks.Listen, Handler: hooks,
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second,
		})
	}
	errc := make(chan error, len(servers)+1)
	for _, srv := range servers {
		go func() { errc <- srv.ListenAndServe() }()
	}
	if hub != nil {
		ln, err := net.Listen("tcp", cfg.Node.Listen)
		if err != nil {
			return fmt.Errorf("[node] listen: %w", err)
		}
		go func() { errc <- hub.Serve(ctx, ln) }()
	}
	log.Info("dock daemon listening", "version", version.Version, "api", cfg.Listen, "internal", cfg.InternalListen, "public", cfg.PublicListen,
		"nodes", cfg.Node.Listen, "link_key", linkID.Fingerprint(), "data", cfg.DataDir)

	select {
	case err = <-errc:
	case <-ctx.Done():
		log.Info("shutting down")
	}
	cancel() // closes the node port and every node session
	shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
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

// startCore creates and starts core and, with [node] listen set, the node
// hub (not yet serving): the hub takes core's store and status callback,
// and core the hub, plugged in after New and before Start.
func startCore(ctx context.Context, opts core.Options, linkID remote.Identity) (*core.Core, *remote.Hub, error) {
	c, err := core.New(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	var hub *remote.Hub
	if cfg := opts.Config; cfg.Node.Listen != "" {
		var reg string
		if cfg.Registry.Enabled {
			reg = cfg.Registry.Listen
		}
		hub = remote.NewHub(remote.HubOptions{
			Identity: linkID,
			Store:    c.NodeStore(),
			Addrs:    []string{cfg.Node.Listen},
			Registry: reg,
			OnStatus: c.NodeStatus,
			Logger:   opts.Logger.With("component", "hub"),
		})
		c.SetNodes(core.NodesOf(hub))
	}
	if err := c.Start(ctx); err != nil {
		return nil, nil, err
	}
	return c, hub, nil
}

// checkLoopback refuses to serve internal endpoints, or the listener
// only Caddy may reach, anywhere but loopback. key names the setting.
func checkLoopback(key, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s %q: %w", key, addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%s %q must be a loopback address (127.0.0.1 or ::1)", key, addr)
	}
	return nil
}

// checkNodeListen requires a specific address for the node port: join
// tokens carry it as the address nodes dial, and "0.0.0.0" or ":7443"
// would send them nowhere.
func checkNodeListen(addr string) error {
	if addr == "" {
		return nil
	}
	host, _, _ := net.SplitHostPort(addr) // config.Load checked the form
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		return fmt.Errorf("[node] listen %q must name the address nodes dial, e.g. the Tailscale IP: \"100.64.0.10:7443\"", addr)
	}
	return nil
}
