package core

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node/remote"
)

// A control plane that starts without its firewall's marker raises the
// platform's alert, naming the server and what to run; the start after
// the firewall is applied resolves it.
func TestFirewallAlertOnTheControlPlane(t *testing.T) {
	h := newHarness(t)
	key := firewallAlertKey(h.c.localID)
	if h.alertOpen(key) {
		t.Fatal("an alert without a firewall check: none is expected on the fake runtime")
	}

	applied := false
	h.opts.FirewallApplied = func() (bool, error) { return applied, nil }
	h.restart()
	open, err := h.c.ListAlerts(h.ctx, testSystem, true, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].Kind != apitypes.AlertConnection || open[0].Server != h.c.localName() ||
		!strings.Contains(open[0].Message, "sudo systemctl restart dock-firewall") {
		t.Fatalf("open alerts = %+v", open)
	}

	applied = true
	h.restart()
	if h.alertOpen(key) {
		t.Fatal("the alert is still open after a start with the firewall applied")
	}
}

// Another server reports its firewall in hello: not applied raises its
// alert, applied resolves it, and one that doesn't say raises nothing.
func TestFirewallAlertOnAnotherServer(t *testing.T) {
	p := newPool(t, poolServer{"cp", true, false, 8}, poolServer{"homelab", false, true, 32})
	key := firewallAlertKey(p.ids["homelab"])
	hello := func(applied *bool) {
		p.c.NodeStatus(remote.State{ServerID: p.ids["homelab"], Server: "homelab", Status: remote.StatusOnline,
			Hello: remote.Hello{Protocol: remote.Protocol, Facts: remote.Facts{Ingress: remote.IngressDirect, FirewallApplied: applied}}})
	}
	yes, no := true, false

	hello(nil)
	if p.alertOpen(key) {
		t.Fatal("an alert for a server that doesn't report its firewall")
	}
	hello(&no)
	if !p.alertOpen(key) {
		t.Fatal("no alert for a server whose firewall isn't applied")
	}
	hello(&yes)
	if p.alertOpen(key) {
		t.Fatal("the alert is still open after the server reported its firewall applied")
	}
}
