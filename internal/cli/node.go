//go:build !dockcli

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/moby/moby/client"
	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/httpkit"
	"github.com/NordTempo/tempoDock/internal/node/caddy"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/node/stats"
	"github.com/NordTempo/tempoDock/internal/update"
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
	cmd.AddCommand(newNodeJoinCmd(&configPath), newNodeSetControlPlaneCmd(&configPath))
	return cmd
}

// newNodeJoinCmd is `dock node join <token>`: install.sh --node runs it
// once, as root, before starting dock-node.service, with the token on
// stdin (--token-file -) so it never shows in the process list.
func newNodeJoinCmd(configPath *string) *cobra.Command {
	var tokenFile string
	var check bool
	cmd := &cobra.Command{
		Use:   "join [<token>]",
		Short: "Join this server to a control plane with a token from `dock server add`",
		Long: `Join this server to the control plane named in the token. It creates this
server's key in the data dir and pins the control plane's key (node.key
and node.toml), then prints the server name the control plane assigned.
A new join replaces an earlier one. Tokens work once, for an hour.

Pass the token with --token-file (a path, or - for stdin) to keep it off
the command line, where other users of this machine could see it.

--check only tries to reach the control plane's addresses in the token
and leaves the token unused: install.sh runs it before installing
anything.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := joinToken(cmd.InOrStdin(), args, tokenFile)
			if err != nil {
				return err
			}
			if check {
				addr, err := remote.CheckReach(cmd.Context(), token)
				if err != nil {
					return err
				}
				return joinOutput(cmd, struct {
					Addr string `json:"addr"`
				}{addr}, "The control plane answers at "+addr)
			}
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			link, err := remote.Join(cmd.Context(), token, cfg.DataDir, nodeFacts(cmd.Context(), cfg))
			if err != nil {
				return err
			}
			return joinOutput(cmd, struct {
				ServerID int64  `json:"server_id"`
				Server   string `json:"server"`
			}{link.ServerID, link.Server}, "Joined the control plane as server "+link.Server)
		},
	}
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "read the join token from this file (- for stdin)")
	cmd.Flags().BoolVar(&check, "check", false, "only check that the control plane is reachable; the token stays unused")
	return cmd
}

// joinOutput prints a `dock node join` result: v as JSON under the root's
// --json, else text; --json-file gets the JSON either way. Server commands
// don't get the client's output settings, so it reads the flags.
func joinOutput(cmd *cobra.Command, v any, text string) error {
	if f := cmd.Flag("json-file"); f != nil && f.Value.String() != "" {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if err := os.WriteFile(f.Value.String(), append(b, '\n'), 0o600); err != nil {
			return err
		}
	}
	if f := cmd.Flag("json"); f != nil && f.Value.String() == "true" {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(v)
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), text)
	return err
}

// newNodeSetControlPlaneCmd is `dock node set-control-plane host:port`:
// point a joined node at a control plane that moved (ADR 0021).
func newNodeSetControlPlaneCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "set-control-plane <host:port>",
		Short: "Point this node at the control plane's new address, after it moved",
		Long: `Point this node at the control plane's new node address ([node] listen
there), after the control plane moved to another server with dock admin
restore. The pinned key stays: the node only connects if the server at the
new address is the same control plane, so no new join is needed. The
running node daemon picks the address up at its next connection attempt,
within 30 seconds. Run it as root (or the dock user).`,
		Example: "  sudo dock node set-control-plane 100.64.0.20:7443",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			if err := config.CheckHostPort("the control plane address", args[0]); err != nil || args[0] == "" {
				return usageError(fmt.Sprintf("%q is not a host:port address", args[0]), "Give the control plane's [node] listen address, e.g. 100.64.0.20:7443.")
			}
			if cfg.Node.ControlPlane != "" {
				return usageError(fmt.Sprintf("%s sets [node] control_plane = %q, which overrides this", *configPath, cfg.Node.ControlPlane),
					"Change it there instead, then: sudo systemctl restart dock-node")
			}
			link, err := remote.SetControlPlane(cfg.DataDir, args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Server %s now connects to the control plane at %s (key %s); dock-node picks it up within 30 seconds.\n",
				link.Server, args[0], link.ControlPlaneKey)
			return err
		},
	}
}

// joinToken is the token given as the argument or in --token-file (path
// or - for stdin), exactly one of them.
func joinToken(stdin io.Reader, args []string, file string) (string, error) {
	switch {
	case len(args) == 1 && file != "":
		return "", usageError("give the token either as an argument or with --token-file, not both", "Prefer --token-file - with the token on stdin.")
	case len(args) == 1:
		return args[0], nil
	case file == "":
		return "", usageError("no join token given", "Pass it on stdin with --token-file -, or get one with `dock server add <name>` on the control plane.")
	}
	r := stdin
	if file != "-" {
		f, err := os.Open(file)
		if err != nil {
			return "", err
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, 4<<10))
	if err != nil {
		return "", fmt.Errorf("read the join token: %w", err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", usageError("the join token is empty", "Get one with `dock server add <name>` on the control plane.")
	}
	return token, nil
}

// runNode runs the node daemon until ctx ends: the link to the control
// plane, the registry proxy and, on the internal listener, Caddy's `ask`
// answered from the last applied routes, the access check and the wake
// check of asleep environments.
func runNode(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	// The node's updater checks what its control plane sends; logged here
	// too so the keys show where the node's logs are read.
	if msg := trustUpdateKeys(cfg, true); msg != "" {
		log.Warn(msg)
	}
	if err := checkLoopback("internal_listen", cfg.InternalListen); err != nil {
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
	rt, proxy, in, err := newRuntime(ctx, cfg, remote.ServerMarker(key.Fingerprint()), log)
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
		// The firewall is the exception, read at every hello: it can be
		// applied or removed while the node runs.
		Facts: func(context.Context) (remote.Facts, error) {
			f := facts
			if check := firewallCheck(); check != nil {
				if ok, err := check(); err == nil {
					f.FirewallApplied = &ok
				}
			}
			return f, nil
		},
		UpdaterUnit: update.PathUnit,
		Logger:      log,
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
		mux.Handle("GET "+accessPath, caddy.AccessHandler())
		// Asleep environments here wake through the link (ADR 0070).
		mux.Handle("GET "+wakePath, caddy.WakeHandler(&caddy.NodeWaker{Proxy: proxy, Request: n.RequestWake}))
	}
	internal := &http.Server{Addr: cfg.InternalListen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: httpkit.IdleTimeout}
	go func() { errc <- internal.ListenAndServe() }()
	// Analytics events (ADR 0037) and spans. Apps are served either way.
	ingest := ingestServers(cfg, in)
	for _, srv := range ingest {
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("an ingest can't listen; this server's analytics or spans go uncollected", "addr", srv.Addr, "err", err)
			}
		}()
	}

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
	for _, srv := range ingest {
		_ = srv.Shutdown(shutdownCtx)
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
		Ingress:    remote.Ingress(cfg.Ingress.Mode),
		PublicIPs:  publicIPs(cfg),
		Builder:    cfg.BuildKit.Enabled,
		PlainHTTP:  cfg.Caddy.DisableTLS && cfg.Ingress.Mode == config.IngressDirect,
		BaseDomain: strings.TrimPrefix(cfg.BaseDomain, "*."),
		HTTPPort:   cfg.Caddy.HTTPPort,
		TunnelPort: cfg.CloudflaredPort(),
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
