//go:build !dockcli

package cli

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"runtime"
	"slices"
	"strings"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/docker"
	"github.com/NordTempo/tempoDock/internal/node/firewall"
)

// newFirewallCmd manages the rules that keep app containers and builds off
// the server's private networks. dock-firewall.service runs `apply` as root
// whenever Docker starts.
func newFirewallCmd() *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "firewall",
		Short: "Keep apps and builds off the LAN, the tailnet and the server (run as root)",
	})

	configPath := config.DefaultPath
	var dryRun bool
	apply := &cobra.Command{
		Use:   "apply",
		Short: "Install or refresh the firewall rules",
		Long: `Installs iptables rules that stop app containers and builds from reaching
private addresses (RFC 1918, the Tailscale range, link-local, IPv6 ULA) and
the server itself, except Caddy's ports, DNS and [network] allow_private.
The public internet stays reachable.

Safe to run again: it replaces its own rules and touches nothing else.
Rerun it after changing [network] in the config, or restart dock-firewall.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			spec, err := firewallSpec(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			if dryRun {
				_, err := fmt.Fprint(cmd.OutOrStdout(), firewall.Rules(spec))
				return err
			}
			if os.Geteuid() != 0 {
				return errors.New("firewall rules need root: run with sudo")
			}
			skipped, err := firewall.Apply(cmd.Context(), spec)
			for _, s := range skipped {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "skipped %s\n", s)
			}
			if err != nil {
				// What is installed now is unknown: the daemon alerts.
				_ = firewall.ClearApplied()
				return err
			}
			// The daemon can't read iptables; this tells it the rules are in.
			if err := firewall.MarkApplied(); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "firewall applied: bridges %s; allowed private %v; DNS %v\n",
				strings.Join(spec.Bridges, " "), spec.Allow, spec.Resolvers)
			return err
		},
	}
	apply.Flags().StringVar(&configPath, "config", configPath, "path to the daemon config file")
	apply.Flags().BoolVar(&dryRun, "dry-run", false, "print the iptables-restore rules instead of installing them")

	remove := &cobra.Command{
		Use:   "remove",
		Short: "Remove the firewall rules (apps can reach private networks again)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Geteuid() != 0 {
				return errors.New("firewall rules need root: run with sudo")
			}
			if err := firewall.Remove(cmd.Context()); err != nil {
				return err
			}
			return firewall.ClearApplied()
		},
	}
	cmd.AddCommand(apply, remove)
	return cmd
}

// firewallSpec collects what the rules cover: every bridge dock containers
// use, dock's subnets, and the host's DNS servers.
func firewallSpec(ctx context.Context, cfg config.Config) (firewall.Spec, error) {
	spec := firewall.Spec{
		// New networks: any dk- bridge, addresses from the pool.
		Bridges:  []string{docker.BridgePrefix + "+"},
		Internal: []netip.Prefix{cfg.Network.Pool},
		Allow:    cfg.Network.AllowPrivate,
		HostTCP:  []int{cfg.Caddy.HTTPPort, cfg.Caddy.HTTPSPort},
		HostUDP:  []int{cfg.Caddy.HTTPSPort},
	}
	resolvers, err := firewall.Resolvers()
	if err != nil {
		return spec, err
	}
	spec.Resolvers = resolvers

	cli, err := client.New(client.FromEnv)
	if err != nil {
		return spec, fmt.Errorf("docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()
	info, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return spec, fmt.Errorf("docker info: %w", err)
	}
	if fw := info.Info.FirewallBackend; fw != nil && !strings.HasPrefix(fw.Driver, "iptables") {
		return spec, fmt.Errorf("docker uses the %q firewall backend; only iptables is supported (remove \"firewall-backend\" from /etc/docker/daemon.json)", fw.Driver)
	}

	// Networks created before the pool existed have Docker's addresses and
	// br-<id> bridges; cover them too.
	nets, err := cli.NetworkList(ctx, client.NetworkListOptions{
		Filters: client.Filters{}.Add("label", node.LabelManaged+"=true"),
	})
	if err != nil {
		return spec, fmt.Errorf("list networks: %w", err)
	}
	add := func(bridge string, subnets ...netip.Prefix) {
		if !strings.HasPrefix(bridge, docker.BridgePrefix) {
			spec.Bridges = append(spec.Bridges, bridge)
		}
		spec.Internal = append(spec.Internal, subnets...)
	}
	slices.SortFunc(nets.Items, func(a, b network.Summary) int { return strings.Compare(a.Name, b.Name) })
	for _, n := range nets.Items {
		bridge := n.Options[docker.BridgeNameOption]
		if bridge == "" {
			bridge = "br-" + n.ID[:12]
		}
		add(bridge, subnets(n.IPAM.Config)...)
	}

	return spec, nil
}

func subnets(cfgs []network.IPAMConfig) []netip.Prefix {
	var out []netip.Prefix
	for _, c := range cfgs {
		if c.Subnet.IsValid() {
			out = append(out, c.Subnet)
		}
	}
	return out
}

// firewallCheck is how a daemon learns whether its server's firewall is
// in (core.Options.FirewallApplied, remote.Facts.FirewallApplied), or nil
// where none is expected: off Linux, on the fake runtime and outside
// systemd (INVOCATION_ID), so a daemon started by hand or by `make dev`
// raises no alert. The config has no switch for the firewall; an
// installed server always gets dock-firewall.service.
func firewallCheck() func() (bool, error) {
	if runtime.GOOS != "linux" || os.Getenv("DOCK_FAKE_RUNTIME") == "1" || os.Getenv("INVOCATION_ID") == "" {
		return nil
	}
	return firewall.Applied
}
