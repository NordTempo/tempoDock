package core

import (
	"slices"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// Analytics turned off stops its store and keeps its data, withholds the
// salts so the ingests keep nothing, keeps a site's first domain from
// turning it on again and stays off across a restart. On runs the store
// again; off with RemoveData removes the store with its data. Only an
// operator switches it.
func TestAnalyticsOffAndOn(t *testing.T) {
	h := newHarness(t)
	if err := h.c.EnableAnalytics(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	waitStoreStarted(t, h.c, h.c.localName())
	admin := Actor{Name: "org-admin", Kind: "token", Scope: apitypes.ScopeAdmin, OrgID: 1}
	if _, err := h.c.SetAnalytics(h.ctx, admin, apitypes.AnalyticsSwitch{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org admin turned analytics off: %v", err)
	}

	st, err := h.c.SetAnalytics(h.ctx, testSystem, apitypes.AnalyticsSwitch{})
	if err != nil || st.Enabled || st.StoreRunning {
		t.Fatalf("off = %+v, %v", st, err)
	}
	if ops := h.rt.Ops(); !slices.Contains(ops, "stop-analytics-store") || slices.Contains(ops, "remove-analytics-store") {
		t.Errorf("off kept the data? ops %v", ops)
	}
	eventually(t, "the servers' salts withheld", func() bool { return len(h.rt.AnalyticsSalts()) == 0 })
	web := h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	h.deploy(web, "nginx:1")
	h.domain(web, "shop.example.com", "")
	if _, err := h.c.EnableHostnameAnalytics(h.ctx, testSystem, "shop.example.com", apitypes.EnableAnalyticsRequest{}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("a domain turned analytics on again: %v", err)
	}

	h.restart()
	if st, _ := h.c.AnalyticsStatus(h.ctx, testSystem); st.Enabled || !h.c.analyticsOff() {
		t.Fatalf("after a restart = %+v", st)
	}

	if _, err := h.c.SetAnalytics(h.ctx, testSystem, apitypes.AnalyticsSwitch{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	waitStoreStarted(t, h.c, h.c.localName())
	eventually(t, "the salts back", func() bool { return len(h.rt.AnalyticsSalts()) == 2 })
	if _, err := h.c.EnableHostnameAnalytics(h.ctx, testSystem, "shop.example.com", apitypes.EnableAnalyticsRequest{}); err != nil {
		t.Fatalf("a domain once analytics is on: %v", err)
	}

	if _, err := h.c.SetAnalytics(h.ctx, testSystem, apitypes.AnalyticsSwitch{RemoveData: true}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.rt.Ops(), "remove-analytics-store") {
		t.Errorf("off with its data didn't remove the store: %v", h.rt.Ops())
	}
	events, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Platform: true, Action: "analytics.off"})
	if err != nil || len(events) != 2 {
		t.Errorf("audit = %+v, %v", events, err)
	}
}

// Scanning turned off with RemoveDatabase removes each builder's
// vulnerability database; without it the database stays.
func TestScanningOffRemovesTheDatabase(t *testing.T) {
	h := newHarness(t)
	if _, err := h.c.SetScanning(h.ctx, testSystem, apitypes.ScanningSettings{}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(h.rt.Ops(), "scan-database prune all") {
		t.Fatal("off alone removed the database")
	}
	st, err := h.c.SetScanning(h.ctx, testSystem, apitypes.ScanningSettings{RemoveDatabase: true})
	if err != nil || st.Enabled {
		t.Fatalf("off = %+v, %v", st, err)
	}
	if !slices.Contains(h.rt.Ops(), "scan-database prune all") {
		t.Errorf("the database stayed: %v", h.rt.Ops())
	}
}

// Features lists scanning, analytics and tracing with where each runs and
// what it takes: disk from the server's last reading, the store's memory
// limit from the config.
func TestFeatures(t *testing.T) {
	h := newHarness(t)
	h.rt.SetDiskUsage(node.DiskUsage{ScanLeftovers: 1 << 20, Volumes: []node.VolumeUsage{
		{Name: "dock_scan_db_x", Role: node.VolumeScanDB, Bytes: 5 << 20},
		{Name: "dock-analytics-data", Role: node.VolumeAnalytics, Bytes: 3 << 20},
	}})
	if _, err := h.c.ServerDisk(h.ctx, testSystem, h.c.localName()); err != nil {
		t.Fatal(err)
	}
	admin := Actor{Name: "org-admin", Kind: "token", Scope: apitypes.ScopeAdmin, OrgID: 1}
	if _, err := h.c.Features(h.ctx, admin); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org admin read the features: %v", err)
	}
	fs, err := h.c.Features(h.ctx, testSystem)
	if err != nil {
		t.Fatal(err)
	}
	got := map[apitypes.FeatureKey]apitypes.Feature{}
	for _, f := range fs.Items {
		got[f.Key] = f
	}
	scan, an, tr := got[apitypes.FeatureScanning], got[apitypes.FeatureAnalytics], got[apitypes.FeatureTracing]
	if !scan.Enabled || scan.DiskBytes == nil || *scan.DiskBytes != 5<<20 || scan.MemoryLimitBytes != node.ScanDatabaseMemory || !slices.Equal(scan.Servers, []string{h.c.localName()}) {
		t.Errorf("scanning = %+v", scan)
	}
	if an.Enabled || an.DiskBytes == nil || *an.DiskBytes != 3<<20 || an.MemoryLimitBytes != 512<<20 {
		t.Errorf("analytics = %+v", an)
	}
	if tr.Enabled || tr.MemoryLimitBytes != 512<<20 || len(fs.Items) != 3 {
		t.Errorf("tracing = %+v, %d features", tr, len(fs.Items))
	}
}
