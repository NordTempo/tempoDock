package core

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// An alert about a service is its org's: its members see it, its
// channels hear it, and another org's members can't see or resolve it.
// An alert about a server is the platform's: operators and platform
// channels only (ADR 0062).
func TestAlertsBelongToTheirOrg(t *testing.T) {
	h := newHarness(t)
	team, ops := newReceiver(h), newReceiver(h)
	h.channel(apitypes.CreateAlertChannelRequest{Name: "team", Kind: apitypes.ChannelWebhook, URL: team.srv.URL})
	h.platformChannel(apitypes.CreateAlertChannelRequest{Name: "ops", Kind: apitypes.ChannelWebhook, URL: ops.srv.URL})

	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	h.rt.FailPull(errors.New("manifest unknown"))
	h.deploy(web, "app:2")
	deploy := h.openAlert("deploy:service:1", true)
	if deploy.OrgID == nil || *deploy.OrgID != 1 {
		t.Fatalf("deploy alert org = %v, want 1", deploy.OrgID)
	}
	at := time.Now()
	for range 2 {
		at = at.Add(time.Minute)
		h.c.diskAlert(h.ctx, h.c.local, apitypes.ServerUsage{Disks: []apitypes.DiskUsage{{UsedBytes: 99, TotalBytes: 100}}}, at)
	}
	if disk := h.openAlert("disk:"+itoa(h.c.local.ID), true); disk.OrgID != nil {
		t.Fatalf("disk alert org = %v, want the platform", *disk.OrgID)
	}
	h.outboxSent()
	if got := titles(team); got != "Deploy of shop/production/web failed" {
		t.Errorf("the org's channel heard %q", got)
	}
	if got := titles(ops); !strings.HasPrefix(got, "Disk 99% full") || strings.Contains(got, "Deploy") {
		t.Errorf("the platform's channel heard %q", got)
	}

	member := h.in(h.member("dev@x", map[string]apitypes.Scope{"test": apitypes.ScopeRead}), "test")
	open, err := h.c.ListAlerts(h.ctx, member, false, false, 0, 0)
	if err != nil || len(open) != 1 || open[0].Kind != apitypes.AlertDeployFailed {
		t.Fatalf("a member's org alerts = %+v, %v; want the deploy alert only", open, err)
	}
	if _, err := h.c.ListAlerts(h.ctx, member, true, false, 0, 0); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a member read the platform's alerts: %v", err)
	}
	if _, err := h.c.ResolveAlert(h.ctx, member, deploy.ID); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a read member resolved an alert: %v", err)
	}

	stranger := h.in(h.member("bob@x", map[string]apitypes.Scope{"acme": apitypes.ScopeAdmin}), "acme")
	if open, _ := h.c.ListAlerts(h.ctx, stranger, false, true, 0, 0); len(open) != 0 {
		t.Fatalf("another org's admin sees %+v", open)
	}
	if _, err := h.c.ResolveAlert(h.ctx, stranger, deploy.ID); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("another org's admin resolved the alert: %v", err)
	}
	if _, err := h.c.ListAlertChannels(h.ctx, stranger, false); err != nil {
		t.Fatal(err)
	} else if chs, _ := h.c.ListAlertChannels(h.ctx, stranger, false); len(chs) != 0 {
		t.Fatalf("another org's channels = %+v", chs)
	}

	platform, err := h.c.ListAlerts(h.ctx, testSystem, true, false, 0, 0)
	if err != nil || len(platform) != 1 || platform[0].Kind != apitypes.AlertDisk {
		t.Fatalf("platform alerts = %+v, %v; want the disk alert only", platform, err)
	}
}

// titles is what a receiver heard, joined.
func titles(k *receiver) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []string
	for _, n := range k.got {
		out = append(out, n.Alert.Title)
	}
	return strings.Join(out, "; ")
}

// Migration 0067 asks for platform channels to be copied into the orgs
// operators run; the copy is made on start, sealed for its own row, so it
// delivers, and a second start makes no second copy.
func TestPlatformChannelsAreCopiedIntoOperatorOrgs(t *testing.T) {
	h := newHarness(t)
	k := newReceiver(h)
	h.platformChannel(apitypes.CreateAlertChannelRequest{Name: "ops", Kind: apitypes.ChannelWebhook, URL: k.srv.URL})
	h.exec(`INSERT INTO alert_channel_copies (channel_id, org_id) SELECT id, 1 FROM alert_channels`)
	h.restart()
	h.restart()
	h.c.AllowLoopbackAlertChannels(true)

	chs, err := h.c.ListAlertChannels(h.ctx, testSystem, false)
	if err != nil || len(chs) != 1 || chs[0].Name != "ops" {
		t.Fatalf("org channels = %+v, %v; want the copy of ops", chs, err)
	}
	if res, err := h.c.TestAlertChannel(h.ctx, testSystem, false, "ops"); err != nil || !res.OK {
		t.Fatalf("the copy doesn't deliver: %+v, %v", res, err)
	}
	if chs, _ := h.c.ListAlertChannels(h.ctx, testSystem, true); len(chs) != 1 {
		t.Fatalf("platform channels = %+v; the original stays", chs)
	}
}

// Memory and CPU alerts open once a server has been over the line for
// the configured minutes, not on one busy sample, and resolve once it is
// back under, below the hysteresis band.
func TestMemoryAndCPUAlertsNeedSustainedLoad(t *testing.T) {
	h := newHarness(t)
	srv := h.c.local
	at := time.Now()
	step := func(memPct, cpuPct float64) {
		at = at.Add(time.Minute)
		u := apitypes.ServerUsage{CPUPercent: cpuPct, CPUs: 4, MemoryUsedBytes: int64(memPct * (1 << 30)), MemoryTotalBytes: 100 << 30}
		h.c.memoryAlert(h.ctx, srv, u, at)
		h.c.cpuAlert(h.ctx, srv, u, at)
	}
	mem, cpu := "memory:"+itoa(srv.ID), "cpu:"+itoa(srv.ID)
	step(95, 99) // a spike
	step(40, 10)
	step(95, 99)
	h.openAlert(mem, false)
	for range 5 { // memory_minutes 5
		step(92, 99)
	}
	if a := h.openAlert(mem, true); a.Severity != string(apitypes.SeverityWarning) || a.OrgID != nil {
		t.Fatalf("memory alert = %+v", a)
	}
	step(98, 99)
	if a := h.openAlert(mem, true); a.Severity != string(apitypes.SeverityCritical) {
		t.Fatalf("memory alert = %+v, want critical", a)
	}
	h.openAlert(cpu, false) // cpu_minutes 15
	for range 10 {
		step(98, 99)
	}
	h.openAlert(cpu, true)
	step(87, 87) // under the lines, within the hysteresis
	h.openAlert(mem, true)
	h.openAlert(cpu, true)
	step(50, 20)
	h.openAlert(mem, false)
	h.openAlert(cpu, false)
}
