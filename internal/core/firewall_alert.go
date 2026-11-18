package core

import (
	"context"
	"strconv"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The app firewall's alert. Root installs the firewall's rules
// (dock-firewall.service); a daemon can't read them, only the marker the
// install leaves (firewall.Applied). A server whose daemon runs without
// that marker has apps that reach its private networks, and nothing else
// says so: the platform gets an alert, keyed firewall:<server ID>.
//
// Nothing watches the marker. The control plane reads its own once, at
// start; another server reports its own in every hello. So the alert
// opens and resolves when that server's daemon starts or reconnects.

// firewallAlertKey names a server's firewall alert.
func firewallAlertKey(serverID int64) string {
	return "firewall:" + strconv.FormatInt(serverID, 10)
}

// checkLocalFirewall raises or resolves the firewall alert of the control
// plane's own server, once, at start. Without Options.FirewallApplied no
// firewall is expected here and nothing is raised.
func (c *Core) checkLocalFirewall(ctx context.Context) {
	if c.firewallApplied == nil {
		return
	}
	applied, err := c.firewallApplied()
	if err != nil {
		// Not knowing isn't the firewall missing: say so in the log only.
		c.log.Warn("can't tell whether the app firewall is installed on this server", "err", err)
		return
	}
	c.firewallReported(ctx, c.localID, c.localName(), "dock", &applied)
}

// firewallReported records what a server's daemon said about its
// firewall: applied resolves the server's alert, not applied raises it.
// Nil is a server that doesn't report it (an older dock, or a daemon
// outside systemd, where no firewall is expected): any alert it had is
// resolved rather than left open with nothing to close it. unit is the
// server's daemon, whose restart reports again.
func (c *Core) firewallReported(ctx context.Context, serverID int64, server, unit string, applied *bool) {
	key := firewallAlertKey(serverID)
	if applied == nil || *applied {
		c.signalAlert(ctx, alertSignal{Key: key, Resolve: true})
		return
	}
	c.signalAlert(ctx, alertSignal{
		Key: key, Kind: apitypes.AlertConnection, Severity: apitypes.SeverityWarning,
		Title: "The app firewall is not installed on server " + server,
		Message: "Apps and builds on server " + server + " can reach its private networks and the server itself. " +
			"On that server, run `sudo systemctl restart dock-firewall` and check `systemctl status dock-firewall`. " +
			"Then `sudo systemctl restart " + unit + "` closes this alert; apps keep running through it.",
		Command: "sudo systemctl restart dock-firewall",
		Server:  server,
		Target:  &apitypes.AlertTarget{Connection: "firewall:" + server},
	})
}
