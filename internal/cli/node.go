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

	"github.com/moby/moby/client"
	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/node/stats"
	"github.com/NordTempo/tempoDock/internal/version"
)

// newNodeCmd is `dock node`, the daemon of a server that joined a control
// plane (dock-node.service), and `dock node join`.
func newNodeCmd() *cobra.Command {
	configPath := config.DefaultPath
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Run this server as a node of a control plane (the daemon of dock-node.service)",
		Long: `Run this server as a node: it dials the control plane, keeps the link up
and runs what the control plane asks for on Docker, BuildKit and Caddy. No
API, UI or database runs here. Join first with ` + "`dock node join`" + ` (install.sh
--node does both).

Stopping the node never stops apps: they are containers with a restart
policy and keep serving.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			return runNode(cmd.Context(), cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
		},
	}
	cmd.PersistentFlags().StringVar(&configPath, "config", configPath, "path to the node config file")
	cmd.AddCommand(newNodeJoinCmd(&configPath))
	return cmd
}

// newNodeJoinCmd is `dock node join <token>`: install.sh --node runs it
// once, as root, before starting dock-node.service.
func newNodeJoinCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "join <token>",
		Short: "Join this server to a control plane with a token from `dock server add`",
		Long: `Join this server to the control plane named in the token. It creates this
server's key in the data dir and pins the control plane's key (node.key
and node.toml), then prints the server name the control plane assigned.
A new join replaces an earlier one. Tokens work once, for an hour.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			link, err := remote.Join(cmd.Context(), args[0], cfg.DataDir, nodeFacts(cmd.Context(), cfg))
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Joined the control plane at %s as server %s\n", link.Addrs[0], link.Server)
			return err
		},
	}
}

// runNode runs the node daemon until ctx ends: the link to the control
// plane, the registry proxy and, on the internal listener, Caddy's `ask`
// answered from the last applied routes.
func runNode(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	if err := checkLoopback(cfg.InternalListen); err != nil {
		return err
	}
	link, err := remote.LoadLink(cfg.DataDir)
	if err != nil {
		return err
	}
	// The key exists once the server joined; its fingerprint gives the
	// Caddy marker, as on the control plane.
	key, err := remote.LoadOrCreateIdentity(cfg.NodeKeyPath())
	if err != nil {
		return err
	}
	rt, proxy, err := newRuntime(ctx, cfg, remote.ServerMarker(key.Fingerprint()), log)
	if err != nil {
		return err
	}
	facts := nodeFacts(ctx, cfg)
	n := &remote.Node{
		DataDir: cfg.DataDir,
		Addr:    cfg.Node.ControlPlane,
		Runtime: rt,
		// Facts don't change while the node runs: Docker restarting
		// restarts this unit too (Requires=docker.service).
		Facts:  func(context.Context) (remote.Facts, error) { return facts, nil },
		Logger: log,
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 3)
	go func() { errc <- n.Run(ctx) }()

	// Images reach this server through the link (ADR 0014 amendment).
	// Without the proxy, apps already here keep running; only pulls and
	// builder pushes fail, so it is logged rather than fatal.
	if ln, err := net.Listen("tcp", cfg.Registry.Listen); err != nil {
		log.Error("registry proxy: can't listen; images from other servers can't be pulled here", "addr", cfg.Registry.Listen, "err", err)
	} else {
		go func() { errc <- n.ServeRegistry(ctx, ln) }()
	}

	mux := http.NewServeMux()
	if proxy != nil {
		mux.Handle("GET "+askPath, proxy.AskHandler())
	}
	internal := &http.Server{Addr: cfg.InternalListen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { errc <- internal.ListenAndServe() }()

	log.Info("dock node running", "version", version.Version, "server", link.Server,
		"control_plane", link.Addrs, "internal", cfg.InternalListen, "registry", cfg.Registry.Listen)
	select {
	case err = <-errc:
	case <-ctx.Done():
		log.Info("shutting down; apps keep running")
	}
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if serr := internal.Shutdown(shutdownCtx); serr != nil && err == nil {
		err = serr
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// nodeFacts gathers what a node reports in hello (ADR 0014 section 6).
// Docker and /proc are asked once; what can't be read is left empty.
func nodeFacts(ctx context.Context, cfg config.Config) remote.Facts {
	f := remote.Facts{
		Ingress:   remote.Ingress(cfg.Ingress.Mode),
		PublicIPs: publicIPs(cfg),
		Builder:   cfg.BuildKit.Enabled,
		PlainHTTP: cfg.Caddy.DisableTLS && cfg.Ingress.Mode == config.IngressDirect,
	}
	if cfg.Ingress.Mode == config.IngressCloudflareTunnel {
		f.TunnelHostname = cfg.Ingress.TunnelHostname
	}
	if hs, err := stats.New(stats.Options{}).HostStats(ctx); err == nil {
		f.MemoryTotal = hs.MemoryTotal
	}
	if cli, err := client.New(client.FromEnv); err == nil {
		vctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if v, err := cli.ServerVersion(vctx, client.ServerVersionOptions{}); err == nil {
			f.DockerVersion = v.Version
		}
		cancel()
		_ = cli.Close()
	}
	return f
}
