package core

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/sbom"
	"github.com/NordTempo/tempoDock/internal/store"
)

// scanHarness is a harness whose scans start after 20 ms of quiet.
func scanHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.opts.ScanQuiet = 20 * time.Millisecond
	h.restart()
	return h
}

// waitScan waits until the image of ref's live release is in state want
// and returns it. Every scan state change publishes a scan event for the
// service, so it reads again after each.
func (h *harness) waitScan(ref ServiceRef, want apitypes.ScanState) apitypes.ImageScan {
	h.t.Helper()
	sub := h.c.bus.subscribe(func(e busEvent) bool { return e.ev.Type == apitypes.EventScan && e.ev.Service == ref.Service })
	defer sub.close()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		var got apitypes.ImageScan
		if d, err := h.c.ServiceVulnerabilities(h.ctx, testSystem, ref, 0); err == nil {
			got = d.Scan
			if got.State == want {
				return got
			}
		}
		select {
		case <-sub.ch:
		case <-timeout.C:
			h.t.Fatalf("%s's image scan is %q, want %q", ref.Service, got.State, want)
		}
	}
}

var webFindings = []node.Finding{
	{ID: "CVE-2026-1", Severity: node.SeverityCritical, Package: "openssl", Version: "3.3.1-r0", Type: "apk", FixedIn: "3.3.2-r0", KEV: true, EPSS: 0.9},
	{ID: "CVE-2026-2", Severity: node.SeverityHigh, Package: "express", Version: "4.19.2", Type: "npm", FixedIn: "4.21.2", EPSS: -1},
	{ID: "CVE-2026-3", Severity: node.SeverityMedium, Package: "sharp", Version: "0.33.1", Type: "npm", EPSS: -1},
}

func TestScanAfterDeploy(t *testing.T) {
	h := scanHarness(t)
	h.rt.SetScanFindings("web", webFindings)
	ref := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(ref, "web:1")

	scan := h.waitScan(ref, apitypes.ScanScanned)
	if scan.Critical != 1 || scan.High != 1 || scan.Medium != 1 || scan.Fixable != 2 || scan.KEV != 1 || scan.WorstFix != apitypes.FixRebuild {
		t.Fatalf("scan = %+v", scan)
	}
	if scan.Distro != "alpine 3.20.3" || scan.Image != "web:1" || !strings.HasPrefix(scan.Digest, "sha256:") {
		t.Fatalf("scan = %+v", scan)
	}
	// The CI image was pinned to its repo digest and scanned by it, then
	// removed again: the scan pulled it.
	rel, err := h.c.GetRelease(h.ctx, testSystem, ref, 1)
	if err != nil || rel.PinnedRef != "web@"+scan.Digest || rel.Scan == nil || rel.Scan.ID != scan.ID {
		t.Fatalf("release = %+v, %v", rel, err)
	}
	if ops := h.rt.Ops(); !slices.Contains(ops, "scan-sbom web@"+scan.Digest) || !slices.Contains(ops, "rmi web@"+scan.Digest) {
		t.Fatalf("ops = %v", ops)
	}

	d, err := h.c.GetImage(h.ctx, testSystem, scan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Findings) != 3 || d.Findings[0].ID != "CVE-2026-1" || !d.Findings[0].System || d.Findings[0].EPSS == nil {
		t.Fatalf("findings = %+v", d.Findings)
	}
	if len(d.Fixes) != 3 || d.Fixes[0].Kind != apitypes.FixRebuild || d.Fixes[1].Kind != apitypes.FixUpgrade || d.Fixes[2].Kind != apitypes.FixNone {
		t.Fatalf("fixes = %+v", d.Fixes)
	}
	if len(d.UsedBy) != 1 || d.UsedBy[0].Service != "web" || !d.UsedBy[0].Live || !d.UsedBy[0].Production {
		t.Fatalf("used by = %+v", d.UsedBy)
	}

	ov, err := h.c.Overview(h.ctx, testSystem, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if s := ov.Environments[0].Services[0].Scan; s == nil || s.Critical != 1 {
		t.Fatalf("overview scan = %+v", s)
	}
	org, err := h.c.OrgFindings(h.ctx, testSystem, true, apitypes.VulnHigh)
	if err != nil || len(org.Findings) != 2 || org.Findings[0].Service != "web" {
		t.Fatalf("org findings = %+v, %v", org, err)
	}
}

func TestScanOncePerImage(t *testing.T) {
	h := scanHarness(t)
	h.rt.SetScanFindings("shared", webFindings[:1])
	a := h.service("a", apitypes.CreateServiceRequest{})
	b := h.service("b", apitypes.CreateServiceRequest{})
	h.deploy(a, "shared:1")
	h.deploy(b, "shared:1")
	h.waitScan(a, apitypes.ScanScanned)
	h.waitScan(b, apitypes.ScanScanned)

	list, err := h.c.ListImages(h.ctx, testSystem, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Images) != 1 || len(list.Images[0].UsedBy) != 2 {
		t.Fatalf("images = %+v", list.Images)
	}
	n := 0
	for _, op := range h.rt.Ops() {
		if strings.HasPrefix(op, "scan-sbom ") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d SBOMs made, want 1: %v", n, h.rt.Ops())
	}
}

// A deploy that arrives while a scan runs kills it and waits for
// nothing; the scan runs again once it is quiet, without counting as a
// failed attempt.
func TestDeployPreemptsScan(t *testing.T) {
	h := scanHarness(t)
	h.rt.SetScanFindings("web", webFindings)
	release, started := h.rt.HoldScans()
	ref := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(ref, "web:1")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("no scan started")
	}

	// The held scan ends only when its ctx does: this deploy must finish
	// with the scan still held, so it waited for nothing the scan holds.
	other := h.service("api", apitypes.CreateServiceRequest{})
	if rel := h.deploy(other, "api:1"); rel.Status != apitypes.ReleaseActive {
		t.Fatalf("deploy during a scan: %s", rel.Status)
	}
	// After the quiet period the scanner starts over; the second start
	// proves the first was cancelled and requeued.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the preempted scan never ran again")
	}
	release()
	scan := h.waitScan(ref, apitypes.ScanScanned)
	row, err := h.store.GetImageScan(h.ctx, scan.ID)
	if err != nil || row.Attempts != 0 {
		t.Fatalf("attempts = %d, %v", row.Attempts, err)
	}
}

func TestScanFailureRetriesLater(t *testing.T) {
	h := scanHarness(t)
	h.rt.FailScans(errors.New("syft: boom"))
	ref := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(ref, "web:1")
	sub := h.c.bus.subscribe(func(e busEvent) bool {
		return e.ev.Type == apitypes.EventScan && e.ev.Scan.State == apitypes.ScanQueued && e.ev.Scan.Error != ""
	})
	defer sub.close()
	select {
	case ev := <-sub.ch:
		if !strings.Contains(ev.Scan.Error, "boom") {
			t.Fatalf("error = %q", ev.Scan.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the failure was never recorded")
	}
	d, err := h.c.ServiceVulnerabilities(h.ctx, testSystem, ref, 0)
	if err != nil {
		t.Fatal(err)
	}
	row, err := h.store.GetImageScan(h.ctx, d.Scan.ID)
	if err != nil || row.Attempts != 1 || row.NextAttemptAt == nil || *row.NextAttemptAt < now().Add(9*time.Minute).Unix() {
		t.Fatalf("row = %+v, %v", row, err)
	}
}

func TestScanningOffAndPermissions(t *testing.T) {
	h := scanHarness(t)
	reader := Actor{Kind: "token", OrgID: 1, Name: "ro", TokenID: 1, Scope: apitypes.ScopeRead}
	admin := Actor{Kind: "token", OrgID: 1, Name: "admin", TokenID: 2, Scope: apitypes.ScopeAdmin}
	if _, err := h.c.SetScanning(h.ctx, admin, apitypes.ScanningSettings{}); err == nil {
		t.Fatal("an org admin turned scanning off; operators only")
	}
	if _, err := h.c.SetScanning(h.ctx, testSystem, apitypes.ScanningSettings{}); err != nil {
		t.Fatal(err)
	}
	ref := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(ref, "web:1")
	if _, err := h.c.ScanService(h.ctx, reader, ref, 0); err == nil {
		t.Fatal("a read token queued a scan; it takes deploy")
	}
	time.Sleep(100 * time.Millisecond) // five quiet periods: nothing may scan
	for _, op := range h.rt.Ops() {
		if strings.HasPrefix(op, "scan-") {
			t.Fatalf("scanned with scanning off: %v", h.rt.Ops())
		}
	}
	list, err := h.c.ListImages(h.ctx, reader, false)
	if err != nil || len(list.Images) != 1 || list.Images[0].Scan != nil {
		t.Fatalf("images = %+v, %v", list, err)
	}

	if _, err := h.c.SetScanning(h.ctx, testSystem, apitypes.ScanningSettings{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	h.waitScan(ref, apitypes.ScanScanned)
	// Another org sees nothing of it.
	other := Actor{Kind: "token", OrgID: 2, Name: "x", TokenID: 3, Scope: apitypes.ScopeRead}
	d, _ := h.c.ServiceVulnerabilities(h.ctx, reader, ref, 0)
	if _, err := h.c.GetImage(h.ctx, other, d.Scan.ID); err == nil {
		t.Fatal("another org read the image")
	}
}

func TestTrimScans(t *testing.T) {
	h := scanHarness(t)
	ref := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(ref, "web:1")
	scan := h.waitScan(ref, apitypes.ScanScanned)
	if _, err := h.c.store.ListImageFindingsSeen(h.ctx, scan.ID); err != nil {
		t.Fatal(err)
	}
	// Kept while a release refers to it.
	if n, err := h.c.trimScans(h.ctx); err != nil || n != 0 {
		t.Fatalf("trimmed %d, %v", n, err)
	}
	rel, _ := h.c.GetRelease(h.ctx, testSystem, ref, 1)
	if err := h.store.ClearTrimmedReleaseScan(h.ctx, rel.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := h.c.trimScans(h.ctx); err != nil || n != 1 {
		t.Fatalf("trimmed %d, %v", n, err)
	}
	if _, err := h.store.GetImageScan(h.ctx, scan.ID); err == nil {
		t.Fatal("the scan is still there")
	}
}

func TestFixGroupsAndVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		less bool
	}{
		{"1.36.1-r29", "1.36.1-r31", true},
		{"0.9", "0.12", true},
		{"4.21.2", "4.19.2", false},
		{"1.0", "1.0.1", true},
	} {
		if got := versionLess(c.a, c.b); got != c.less {
			t.Errorf("versionLess(%q, %q) = %v", c.a, c.b, got)
		}
	}
	fs := []apitypes.Finding{
		{ID: "A", Severity: apitypes.VulnHigh, Package: "openssl", Version: "1", System: true, FixedIn: "1.2"},
		{ID: "B", Severity: apitypes.VulnCritical, Package: "openssl", Version: "1", System: true, FixedIn: "1.10", KEV: true},
		{ID: "C", Severity: apitypes.VulnCritical, Package: "left-pad", Version: "1"},
	}
	g := fixGroups(fs)
	if len(g) != 2 || g[0].Kind != apitypes.FixRebuild || g[1].Kind != apitypes.FixNone {
		t.Fatalf("groups = %+v", g)
	}
	p := g[0].Packages[0]
	if p.FixedIn != "1.10" || p.Worst != apitypes.VulnCritical || !p.KEV || len(p.Findings) != 2 {
		t.Fatalf("package = %+v", p)
	}
	if n := countFindings([]node.Finding{{Severity: node.SeverityHigh, Type: "npm", FixedIn: "2"}}); n.worstFix != apitypes.FixUpgrade || n.high != 1 {
		t.Fatalf("counts = %+v", n)
	}
}

// The SBOM exports as stored or converted, gzipped, to readers only of
// the service's org.
func TestExportSBOM(t *testing.T) {
	h := scanHarness(t)
	ref := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(ref, "web:1")
	h.waitScan(ref, apitypes.ScanScanned)
	for _, f := range []sbom.Format{sbom.SyftJSON, sbom.CycloneDXJSON, sbom.SPDXJSON} {
		exp, err := h.c.ExportSBOM(h.ctx, testSystem, ref, 0, f)
		if err != nil {
			t.Fatal(err)
		}
		var gz bytes.Buffer
		if err := exp.Write(&gz); err != nil {
			t.Fatal(err)
		}
		zr, err := gzip.NewReader(&gz)
		if err != nil {
			t.Fatalf("%s isn't gzipped: %v", f, err)
		}
		var doc map[string]any
		if err := json.NewDecoder(zr).Decode(&doc); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if f == sbom.CycloneDXJSON && len(doc["components"].([]any)) == 0 || !strings.HasPrefix(exp.Name, "web-r1.") {
			t.Fatalf("%s: %s, %v", f, exp.Name, doc)
		}
	}
	if _, err := h.c.ExportSBOM(h.ctx, testSystem, ref, 0, "xml"); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("an unknown format: %v", err)
	}
	other := Actor{Kind: "token", OrgID: 2, Name: "x", TokenID: 3, Scope: apitypes.ScopeAdmin}
	if _, err := h.c.ExportSBOM(h.ctx, other, ref, 0, sbom.SyftJSON); err == nil {
		t.Fatal("another org read the SBOM")
	}
}

// An image only a server that doesn't scan its kind has (no registry)
// waits for an operator without using attempts, and is due again once
// what a server scans changes.
func TestScanParksForItsServer(t *testing.T) {
	h := newHarness(t)
	local, err := h.store.GetLocalServer(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.UpdateServerSettings(h.ctx, store.UpdateServerSettingsParams{BaseDomain: local.BaseDomain, Builder: true, BuildsNonProduction: true, ID: local.ID}); err != nil {
		t.Fatal(err)
	}
	row, err := h.store.AddImageScan(h.ctx, store.AddImageScanParams{ImageKey: "local:sha256:1", LocalRef: "dock/web:1", BuiltOnServerID: &local.ID, OrgID: 1, CreatedAt: now().Unix()})
	if err != nil {
		t.Fatal(err)
	}
	h.c.runScan(h.ctx, row, nil)
	row, err = h.store.GetImageScan(h.ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != string(apitypes.ScanQueued) || row.Attempts != 0 || row.NextAttemptAt == nil || !strings.Contains(row.Error, "--scans all") {
		t.Fatalf("parked scan = %s, %d attempts, next %v, %q", row.State, row.Attempts, row.NextAttemptAt, row.Error)
	}
	h.c.scans.mu.Lock()
	h.c.scans.enabled = false // no pass takes the row before it is read
	h.c.scans.mu.Unlock()
	on := true
	if _, err := h.c.UpdateServer(h.ctx, testSystem, local.Name, apitypes.UpdateServerRequest{ScansNonProduction: &on}); err != nil {
		t.Fatal(err)
	}
	if row, err = h.store.GetImageScan(h.ctx, row.ID); err != nil || row.NextAttemptAt != nil {
		t.Fatalf("after scans changed: next %v (%v), want due now", row.NextAttemptAt, err)
	}
}
