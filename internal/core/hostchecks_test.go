package core

import (
	"errors"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// hostFinding is a finding as a server's checker reports it.
func hostFinding(check, subject, severity, title string) node.HostCheck {
	return node.HostCheck{Check: check, Subject: subject, Status: node.HostFinding, Severity: severity, Title: title}
}

// platformAlerts returns the open platform alerts of a kind.
func (h *harness) platformAlerts(kind apitypes.AlertKind) []apitypes.Alert {
	h.t.Helper()
	open, err := h.c.ListAlerts(h.ctx, testSystem, true, false, 0, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []apitypes.Alert
	for _, a := range open {
		if a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

// localServer is the control plane's own server row, once the harness's
// first statistics tick and the host check it may start have ended: that
// check would otherwise overwrite what a test set up.
func (h *harness) localServer() store.Server {
	h.t.Helper()
	srv, err := h.c.store.GetServer(h.ctx, h.c.localID)
	if err != nil {
		h.t.Fatal(err)
	}
	<-h.c.stats.firstTick
	eventually(h.t, "the first tick's host check to end", func() bool {
		h.c.hostChecks.mu.Lock()
		defer h.c.hostChecks.mu.Unlock()
		return !h.c.hostChecks.running[srv.ID]
	})
	return srv
}

// A server checks its host once a day by itself, riding the statistics
// tick; a tick in between asks nothing.
func TestHostChecksDaily(t *testing.T) {
	h := newHarness(t)
	srv := h.localServer()
	start := time.Now().Add(48 * time.Hour)
	before := h.rt.HostChecksAsked()
	h.c.hostCheckTick(start, []store.Server{srv})
	eventually(t, "the due check", func() bool { return h.rt.HostChecksAsked() == before+1 })
	eventually(t, "the check to end", func() bool {
		h.c.hostChecks.mu.Lock()
		defer h.c.hostChecks.mu.Unlock()
		return !h.c.hostChecks.running[srv.ID]
	})
	h.c.hostCheckTick(start.Add(23*time.Hour), []store.Server{srv})
	h.c.hostCheckTick(start.Add(24*time.Hour-30*time.Second), []store.Server{srv})
	eventually(t, "the next day's check", func() bool { return h.rt.HostChecksAsked() == before+2 })
	v, err := h.c.HostChecks(h.ctx, testSystem, srv.Name)
	if err != nil || v.CheckedAt == nil || len(v.Checks) == 0 {
		t.Fatalf("stored checks = %+v, %v", v, err)
	}
}

// Warning and critical findings open one platform alert per server: a
// new one is sent as a repeat, one fixed only changes the text, a
// dismissed one stops counting, and when none is open the alert
// resolves. Info findings and the app firewall (it has its own alert)
// raise nothing. Org members never see it.
func TestHostChecksAlert(t *testing.T) {
	h := newHarness(t)
	srv := h.localServer()
	ssh := hostFinding("ssh_password", "", node.HostWarning, "SSH takes passwords")
	port := hostFinding("open_port", "5432", node.HostWarning, "Port 5432 is open on every address")
	root := hostFinding("ssh_root", "", node.HostCritical, "Root can sign in over SSH with a password")
	info := hostFinding("host_firewall", "", node.HostInfo, "No host firewall found")
	appFW := hostFinding("app_firewall", "", node.HostWarning, "Dock's app firewall is not installed")

	h.rt.SetHostChecks([]node.HostCheck{info, appFW}, nil)
	v, err := h.c.CheckHostNow(h.ctx, testSystem, srv.Name)
	if err != nil {
		t.Fatal(err)
	}
	if v.Counts != (apitypes.HostCheckCounts{Warning: 1, Info: 1}) {
		t.Fatalf("counts = %+v", v.Counts)
	}
	if got := h.platformAlerts(apitypes.AlertHostSecurity); len(got) != 0 {
		t.Fatalf("info and the app firewall alerted: %+v", got)
	}

	h.rt.SetHostChecks([]node.HostCheck{ssh, info}, nil)
	first, err := h.c.CheckHostNow(h.ctx, testSystem, srv.Name)
	if err != nil {
		t.Fatal(err)
	}
	alerts := h.platformAlerts(apitypes.AlertHostSecurity)
	if len(alerts) != 1 || alerts[0].Severity != apitypes.SeverityWarning || alerts[0].Server != srv.Name || alerts[0].Count != 1 {
		t.Fatalf("alerts = %+v", alerts)
	}
	if open, _ := h.c.ListAlerts(h.ctx, Actor{Kind: "user", OrgID: 1, Name: "admin", Scope: apitypes.ScopeAdmin}, false, false, 0, 0); len(open) != 0 {
		t.Fatalf("the org sees %+v", open)
	}

	// Something new: a repeat, critical now.
	h.rt.SetHostChecks([]node.HostCheck{ssh, port, root, info}, nil)
	v, err = h.c.CheckHostNow(h.ctx, testSystem, srv.Name)
	if err != nil {
		t.Fatal(err)
	}
	if v.Checks[0].Key != "ssh_root" || v.Checks[1].Status != apitypes.HostCheckFinding {
		t.Fatalf("not worst first: %+v", v.Checks)
	}
	alerts = h.platformAlerts(apitypes.AlertHostSecurity)
	if len(alerts) != 1 || alerts[0].Severity != apitypes.SeverityCritical || alerts[0].Count != 2 || alerts[0].Title != "3 security findings on server "+srv.Name {
		t.Fatalf("alerts after new findings = %+v", alerts)
	}
	// A finding seen before keeps when it was first seen.
	for _, c := range v.Checks {
		if c.Key == "ssh_password" && !c.FirstSeenAt.Equal(*first.Checks[0].FirstSeenAt) {
			t.Fatalf("ssh_password first seen %v, want %v", c.FirstSeenAt, first.Checks[0].FirstSeenAt)
		}
	}

	// One fixed: the same alert, not counted again.
	h.rt.SetHostChecks([]node.HostCheck{ssh, port, info}, nil)
	if _, err := h.c.CheckHostNow(h.ctx, testSystem, srv.Name); err != nil {
		t.Fatal(err)
	}
	if alerts = h.platformAlerts(apitypes.AlertHostSecurity); len(alerts) != 1 || alerts[0].Count != 2 {
		t.Fatalf("alerts after a fix = %+v", alerts)
	}

	// Dismissed with reasons: the alert resolves, and stays resolved at
	// the next check.
	for _, f := range []string{"ssh_password", "open_port:5432"} {
		if _, err := h.c.DismissHostFinding(h.ctx, testSystem, srv.Name, apitypes.DismissHostFindingRequest{Finding: f, Reason: "lab box"}); err != nil {
			t.Fatal(err)
		}
	}
	v, err = h.c.CheckHostNow(h.ctx, testSystem, srv.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.platformAlerts(apitypes.AlertHostSecurity); len(got) != 0 {
		t.Fatalf("dismissed findings alert: %+v", got)
	}
	if v.Counts != (apitypes.HostCheckCounts{Info: 1}) || v.Checks[0].Dismissed == nil || v.Checks[0].Dismissed.Reason != "lab box" {
		t.Fatalf("after dismissing = %+v", v)
	}
	if all, err := h.c.ListHostChecks(h.ctx, testSystem); err != nil || len(all) != 1 || all[0].Counts != v.Counts || all[0].Checks[0].Dismissed == nil {
		t.Fatalf("listed = %+v, %v", all, err)
	}
	if _, err := h.c.DismissHostFinding(h.ctx, testSystem, srv.Name, apitypes.DismissHostFindingRequest{Finding: "docker_tcp", Reason: "x"}); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("dismissing a finding that isn't there: %v", err)
	}

	// Restored: it alerts again.
	if _, err := h.c.RestoreHostFinding(h.ctx, testSystem, srv.Name, apitypes.RestoreHostFindingRequest{Finding: "ssh_password"}); err != nil {
		t.Fatal(err)
	}
	if got := h.platformAlerts(apitypes.AlertHostSecurity); len(got) != 1 {
		t.Fatalf("restored finding: %+v", got)
	}
}

// A check that fails keeps the results before it, says why, and leaves
// the alert as it was.
func TestHostChecksFailure(t *testing.T) {
	h := newHarness(t)
	srv := h.localServer()
	h.rt.SetHostChecks([]node.HostCheck{hostFinding("ssh_password", "", node.HostWarning, "SSH takes passwords")}, nil)
	if _, err := h.c.CheckHostNow(h.ctx, testSystem, srv.Name); err != nil {
		t.Fatal(err)
	}
	h.rt.SetHostChecks(nil, errors.New("server homelab runs dock 0.70.0, which can't check its host; upgrade dock on it"))
	v, err := h.c.CheckHostNow(h.ctx, testSystem, srv.Name)
	if err != nil {
		t.Fatal(err)
	}
	if v.Error == "" || len(v.Checks) != 1 || v.Counts.Warning != 1 {
		t.Fatalf("after a failed check = %+v", v)
	}
	if got := h.platformAlerts(apitypes.AlertHostSecurity); len(got) != 1 {
		t.Fatalf("alerts = %+v", got)
	}
}

// Host checks are the platform's: an org admin can't read them, a
// platform viewer reads but can't run or dismiss.
func TestHostChecksPermissions(t *testing.T) {
	h := newHarness(t)
	srv := h.localServer()
	admin := Actor{Kind: "user", OrgID: 1, Name: "admin", Scope: apitypes.ScopeAdmin}
	viewer := Actor{Kind: "user", OrgID: 1, Name: "viewer", Scope: apitypes.ScopeAdmin, Platform: apitypes.PlatformViewer}
	if _, err := h.c.HostChecks(h.ctx, admin, srv.Name); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org admin read host checks: %v", err)
	}
	if _, err := h.c.ListHostChecks(h.ctx, admin); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org admin listed host checks: %v", err)
	}
	if _, err := h.c.HostChecks(h.ctx, viewer, srv.Name); err != nil {
		t.Fatalf("a platform viewer can't read host checks: %v", err)
	}
	if _, err := h.c.ListHostChecks(h.ctx, viewer); err != nil {
		t.Fatalf("a platform viewer can't list host checks: %v", err)
	}
	req := apitypes.DismissHostFindingRequest{Finding: "ssh_password", Reason: "x"}
	for _, a := range []Actor{admin, viewer} {
		_, now := h.c.CheckHostNow(h.ctx, a, srv.Name)
		_, dismiss := h.c.DismissHostFinding(h.ctx, a, srv.Name, req)
		_, restore := h.c.RestoreHostFinding(h.ctx, a, srv.Name, apitypes.RestoreHostFindingRequest{Finding: "ssh_password"})
		for _, err := range []error{now, dismiss, restore} {
			if CodeOf(err) != apitypes.CodeForbidden {
				t.Fatalf("%s changed host checks: %v", a.Name, err)
			}
		}
	}
}
