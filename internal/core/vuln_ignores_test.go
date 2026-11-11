package core

import (
	"slices"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// vulnAlert is the open vulnerability alert of ref, if any.
func (h *harness) vulnAlert(ref ServiceRef) (store.Alert, bool) {
	h.c.alerts.mu.Lock()
	defer h.c.alerts.mu.Unlock()
	a, ok := h.c.alerts.open[vulnAlertKey+alertServiceKey(h.serviceID(ref), "", "", "")]
	return a, ok
}

// overviewScan is ref's live image as its overview counts it.
func (h *harness) overviewScan(ref ServiceRef) apitypes.ImageScan {
	h.t.Helper()
	ov, err := h.c.Overview(h.ctx, testSystem, ref.Project)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, e := range ov.Environments {
		for _, s := range e.Services {
			if s.Service.Name == ref.Service && e.Environment.Name == ref.Environment && s.Scan != nil {
				return *s.Scan
			}
		}
	}
	h.t.Fatalf("%s has no scan on the overview", ref)
	return apitypes.ImageScan{}
}

// The daily refresh gets a newer database and matches live images again
// from their SBOMs; a critical that appears alerts (the first scan
// didn't), and ignoring it closes the alert and leaves it out of counts.
func TestScanRefreshAlertsAndIgnores(t *testing.T) {
	h := scanHarness(t)
	h.rt.SetScanFindings("web", webFindings[1:2]) // one high
	ref := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(ref, "web:1")
	scan := h.waitScan(ref, apitypes.ScanScanned)
	if _, ok := h.vulnAlert(ref); ok {
		t.Fatal("the service's first scan alerted")
	}

	// A day later the database knows a critical in openssl.
	h.rt.SetScanFindings("web", webFindings)
	servers, err := h.c.store.ListServers(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range servers {
		day := now().Add(-25 * time.Hour).Unix()
		if err := h.c.store.SetServerScanChecked(h.ctx, store.SetServerScanCheckedParams{ScanDbCheckedAt: &day, ID: s.ID}); err != nil {
			t.Fatal(err)
		}
	}
	// The scanner's next pass sees the builder due; the hourly pass kicks it.
	h.c.scans.mu.Lock()
	h.c.scans.refreshAt = now().Add(-time.Minute)
	h.c.scans.mu.Unlock()
	h.c.kickScanRefresh()
	eventually(t, "the vulnerability alert", func() bool { _, ok := h.vulnAlert(ref); return ok })
	al, _ := h.vulnAlert(ref)
	got := toAlert(al)
	if got.Kind != apitypes.AlertNewVulnerability || got.Severity != apitypes.SeverityCritical || got.Target == nil ||
		got.Target.Image != scan.ID || !slices.Equal(got.Target.Vulnerabilities, []string{"CVE-2026-1"}) {
		t.Fatalf("alert = %+v", got)
	}
	if got.Title != "New critical vulnerability in shop/production/web" || got.Message != "CVE-2026-1 in openssl 3.3.1-r0, known exploited. Fixed by rebuilding." {
		t.Fatalf("alert reads %q: %q", got.Title, got.Message)
	}
	var updates, sboms int
	for _, op := range h.rt.Ops() {
		switch {
		case op == "scan-database update":
			updates++
		case len(op) > 10 && op[:10] == "scan-sbom ":
			sboms++
		}
	}
	if updates != 2 || sboms != 1 {
		t.Fatalf("%d database updates, %d SBOMs; want 2 and 1 (matched again from the stored SBOM): %v", updates, sboms, h.rt.Ops())
	}
	if s := h.overviewScan(ref); s.Critical != 1 || s.WorstFix != apitypes.FixRebuild {
		t.Fatalf("overview = %+v", s)
	}

	ig, err := h.c.IgnoreVulnerability(h.ctx, testSystem, apitypes.IgnoreVulnRequest{
		Vulnerability: "CVE-2026-1", Package: "openssl", Project: "shop", Environment: "production", Service: "web", Reason: "not reachable",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ig.Scope != apitypes.IgnoreService || ig.Service != "web" || ig.Expires == nil || ig.Expires.Sub(now()) < 89*24*time.Hour {
		t.Fatalf("ignore = %+v", ig)
	}
	if _, ok := h.vulnAlert(ref); ok {
		t.Fatal("the alert stayed open with its vulnerability ignored")
	}
	if s := h.overviewScan(ref); s.Critical != 0 || s.High != 1 || s.KEV != 0 || s.WorstFix != apitypes.FixUpgrade {
		t.Fatalf("overview with the ignore = %+v", s)
	}
	list, err := h.c.ListImages(h.ctx, testSystem, false)
	if err != nil || list.Images[0].Scan.Critical != 0 {
		t.Fatalf("images = %+v, %v", list, err)
	}
	d, err := h.c.GetImage(h.ctx, testSystem, scan.ID)
	if err != nil || len(d.Findings) != 3 || !d.Findings[0].Ignored || d.Scan.Critical != 0 || len(d.Ignores) != 1 || d.Fixes[0].Kind != apitypes.FixUpgrade {
		t.Fatalf("image = %+v, %v", d, err)
	}
	org, err := h.c.OrgFindings(h.ctx, testSystem, false, apitypes.VulnHigh)
	if err != nil || len(org.Findings) != 1 || org.Findings[0].ID != "CVE-2026-2" {
		t.Fatalf("org findings = %+v, %v", org, err)
	}

	if err := h.c.RemoveVulnIgnore(h.ctx, testSystem, ig.ID); err != nil {
		t.Fatal(err)
	}
	if s := h.overviewScan(ref); s.Critical != 1 {
		t.Fatalf("overview after removing the ignore = %+v", s)
	}
}

// An image new to a service alerts with what its previous image didn't
// have; deploying a clean one resolves it; off resolves and stops them.
func TestVulnAlertNewImage(t *testing.T) {
	h := scanHarness(t)
	h.rt.SetScanFindings("web", webFindings[1:2])
	h.rt.SetScanFindings("next", webFindings)
	ref := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(ref, "web:1")
	h.waitScan(ref, apitypes.ScanScanned)

	h.deploy(ref, "next:1")
	eventually(t, "the vulnerability alert", func() bool { _, ok := h.vulnAlert(ref); return ok })
	if al, _ := h.vulnAlert(ref); !slices.Equal(toAlert(al).Target.Vulnerabilities, []string{"CVE-2026-1"}) {
		t.Fatalf("alert = %+v (the high was there before)", toAlert(al))
	}

	// Back to the clean image, already scanned: resolved once it is live.
	h.deploy(ref, "web:1")
	eventually(t, "the alert to resolve", func() bool { _, ok := h.vulnAlert(ref); return !ok })

	reader := Actor{Kind: "token", OrgID: 1, Name: "ro", TokenID: 1, Scope: apitypes.ScopeRead}
	if _, err := h.c.SetVulnAlerts(h.ctx, reader, apitypes.VulnAlertSettings{Alert: apitypes.VulnAlertsOff}); err == nil {
		t.Fatal("a read token changed the alert setting")
	}
	if _, err := h.c.SetVulnAlerts(h.ctx, testSystem, apitypes.VulnAlertSettings{Alert: apitypes.VulnAlertsOff}); err != nil {
		t.Fatal(err)
	}
	h.deploy(ref, "next:1")
	h.waitScan(ref, apitypes.ScanScanned)
	time.Sleep(50 * time.Millisecond)
	if _, ok := h.vulnAlert(ref); ok {
		t.Fatal("alerted with vulnerability alerts off")
	}
	if s, err := h.c.VulnAlertSetting(h.ctx, reader); err != nil || s.Alert != apitypes.VulnAlertsOff {
		t.Fatalf("setting = %+v, %v", s, err)
	}
}

// Who may ignore where, and an ignore in one service leaves the image's
// other services as they were.
func TestVulnIgnoreScopes(t *testing.T) {
	h := scanHarness(t)
	h.rt.SetScanFindings("shared", webFindings)
	a := h.service("a", apitypes.CreateServiceRequest{})
	b := h.service("b", apitypes.CreateServiceRequest{})
	h.deploy(a, "shared:1")
	h.deploy(b, "shared:1")
	scan := h.waitScan(a, apitypes.ScanScanned)
	h.waitScan(b, apitypes.ScanScanned)

	reader := Actor{Kind: "token", OrgID: 1, Name: "ro", TokenID: 1, Scope: apitypes.ScopeRead}
	deployer := Actor{Kind: "token", OrgID: 1, Name: "ci", TokenID: 2, Scope: apitypes.ScopeDeploy}
	other := Actor{Kind: "token", OrgID: 2, Name: "x", TokenID: 3, Scope: apitypes.ScopeAdmin}
	req := apitypes.IgnoreVulnRequest{Vulnerability: "CVE-2026-1", Project: "shop", Environment: "production", Service: "a", Reason: "not reachable"}
	if _, err := h.c.IgnoreVulnerability(h.ctx, reader, req); err == nil {
		t.Fatal("a read token ignored a vulnerability")
	}
	forGood := req
	forGood.Always = true
	if _, err := h.c.IgnoreVulnerability(h.ctx, deployer, forGood); err == nil {
		t.Fatal("a deploy token ignored a vulnerability for good; that takes admin")
	}
	orgWide := req
	orgWide.Scope = apitypes.IgnoreOrg
	if _, err := h.c.IgnoreVulnerability(h.ctx, deployer, orgWide); err == nil {
		t.Fatal("a deploy token ignored a vulnerability in the whole org")
	}
	noReason := req
	noReason.Reason = " "
	if _, err := h.c.IgnoreVulnerability(h.ctx, deployer, noReason); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("an ignore without a reason: %v", err)
	}
	ig, err := h.c.IgnoreVulnerability(h.ctx, deployer, req)
	if err != nil {
		t.Fatal(err)
	}

	if s := h.overviewScan(a); s.Critical != 0 {
		t.Fatalf("a = %+v", s)
	}
	if s := h.overviewScan(b); s.Critical != 1 {
		t.Fatalf("b = %+v: a's ignore hid it", s)
	}
	// The image runs in b too: its page and the Images page still count it.
	d, err := h.c.GetImage(h.ctx, testSystem, scan.ID)
	if err != nil || d.Scan.Critical != 1 || d.Findings[0].Ignored || len(d.Ignores) != 1 {
		t.Fatalf("image = %+v, %v", d, err)
	}
	if v, err := h.c.ServiceVulnerabilities(h.ctx, testSystem, a, 0); err != nil || v.Scan.Critical != 0 || !v.Findings[0].Ignored {
		t.Fatalf("a's vulnerabilities = %+v, %v", v, err)
	}

	if err := h.c.RemoveVulnIgnore(h.ctx, other, ig.ID); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("another org removed the ignore: %v", err)
	}
	if err := h.c.RemoveVulnIgnore(h.ctx, reader, ig.ID); err == nil {
		t.Fatal("a read token removed an ignore")
	}
	if l, err := h.c.ListVulnIgnores(h.ctx, reader); err != nil || len(l.Ignores) != 1 || l.Ignores[0].By != "ci" {
		t.Fatalf("ignores = %+v, %v", l, err)
	}
	// Ignoring it again replaces it rather than adding one.
	req.Reason = "still not reachable"
	if again, err := h.c.IgnoreVulnerability(h.ctx, deployer, req); err != nil || again.ID != ig.ID || again.Reason != req.Reason {
		t.Fatalf("ignored again = %+v, %v", again, err)
	}
	if err := h.c.RemoveVulnIgnore(h.ctx, deployer, ig.ID); err != nil {
		t.Fatal(err)
	}

	// Expired ignores apply no longer and go after 30 days.
	old := now().Add(-31 * 24 * time.Hour).Unix()
	if _, err := h.c.store.UpsertVulnIgnore(h.ctx, store.UpsertVulnIgnoreParams{OrgID: 1, VulnID: "CVE-2026-1", Reason: "x", ExpiresAt: &old, CreatedBy: "x", CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	if s := h.overviewScan(a); s.Critical != 1 {
		t.Fatalf("an expired ignore applied: %+v", s)
	}
	if n, err := h.c.pruneVulnIgnores(h.ctx); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
}

func TestIgnoreSetHidden(t *testing.T) {
	ig := ignoreSet{
		{VulnID: "CVE-1", ServiceID: 7, ProjectID: 1},
		{VulnID: "CVE-2", Package: "zlib"},
	}
	a, b := vulnUse{1, 7}, vulnUse{1, 8}
	for _, c := range []struct {
		vuln, pkg string
		uses      []vulnUse
		want      bool
	}{
		{"CVE-1", "x", []vulnUse{a}, true},
		{"CVE-1", "x", []vulnUse{a, b}, false},
		{"CVE-2", "zlib", []vulnUse{a, b}, true},
		{"CVE-2", "openssl", []vulnUse{a}, false},
		{"CVE-3", "x", []vulnUse{a}, false},
		{"CVE-1", "x", nil, false},
	} {
		if got := ig.hidden(c.vuln, c.pkg, c.uses); got != c.want {
			t.Errorf("hidden(%s, %s, %v) = %v", c.vuln, c.pkg, c.uses, got)
		}
	}
}
