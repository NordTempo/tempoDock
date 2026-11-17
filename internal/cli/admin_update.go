//go:build !dockcli

package cli

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/update"
	"github.com/NordTempo/tempoDock/internal/updater"
	"github.com/NordTempo/tempoDock/internal/version"
)

// Where install.sh puts things; `dock admin update` replaces them.
const (
	installedBinary = "/usr/local/bin/dock"
	unitDir         = "/etc/systemd/system"
	caddyBootstrap  = "/etc/dock/caddy.json"
	// updaterStateDir is dock-update.service's StateDirectory.
	updaterStateDir = "/var/lib/dock-update"
)

// newAdminUpdateCmd is `dock admin update`, the updater (ADR 0028):
// dock-update.service runs it as root when the daemon asks for an update.
func newAdminUpdateCmd() *cobra.Command {
	configPath := config.DefaultPath
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Apply the release the daemon staged (run by dock-update.service, as root)",
		Long: `Apply the update the daemon asked for, as root. dock-update.service runs it
when the daemon writes <data_dir>/update/request.json, which ` + "`dock update apply`" + `
and the console's Update do. Nothing happens without a request.

It copies the staged release out of the daemon's reach, checks it against
the release's signed SHA256SUMS and the binary's own version, saves the current
binary, units and caddy.json, and on the control plane stops dock and
writes a control plane backup to /var/lib/dock-update. Then it runs the
release's install.sh and checks the new version comes up. If not, it puts
everything back, the database from the backup included. The result goes
to <data_dir>/update/result.json, and the control plane audits it.

Logs: journalctl -u dock-update`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Geteuid() != 0 {
				return usageError("dock admin update runs as root", "It is started by dock-update.service; use `dock update apply` (or the console) to update.")
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			node := updater.IsNode(unitDir)
			if msg := trustUpdateKeys(cfg, node); msg != "" {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), msg)
			}
			res, ok, err := updater.Run(cmd.Context(), updater.Options{
				DataDir: cfg.DataDir, StateDir: updaterStateDir, BinPath: installedBinary,
				UnitDir: unitDir, CaddyJSON: caddyBootstrap, Node: node,
				Arch: runtime.GOARCH, Current: version.Version,
				System: updater.Host{Config: cfg, ConfigPath: configPath, Node: node, HealthTimeout: 90 * time.Second},
				Log:    cmd.OutOrStdout(), MaxRequestAge: 10 * time.Minute,
			})
			switch {
			case err != nil:
				return err
			case !ok:
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "No update was requested; nothing to do.")
				return nil
			case res.Status != update.Succeeded:
				return fmt.Errorf("update to %s %s: %s", res.Version, res.Status, res.Error)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", configPath, "path to the daemon config file")
	return cmd
}

// trustUpdateKeys hands [updates] trusted_keys to update.TrustExtra when
// they apply (config.Updates.Extra) and returns the line to log about it,
// empty without any: `dock serve`, `dock node` and `dock admin update`
// call it at start-up.
func trustUpdateKeys(cfg config.Config, node bool) string {
	keys, ignored := cfg.Updates.Extra(node)
	update.TrustExtra(keys)
	switch {
	case ignored:
		return "ignoring [updates] trusted_keys: they apply only with a directory [updates] source"
	case len(keys) == 0:
		return ""
	}
	ids := make([]string, len(keys))
	for i, k := range keys {
		ids[i] = update.KeyID(k)
	}
	noun := "key"
	if len(keys) > 1 {
		noun = "keys"
	}
	return fmt.Sprintf("trusting %d extra update %s: %s", len(keys), noun, strings.Join(ids, ", "))
}
