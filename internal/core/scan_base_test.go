package core

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// A built image knows its base image; the daily refresh asks the
// registry whether its tag moved, and the image's scan says so.
func TestBaseImageHint(t *testing.T) {
	h := scanHarness(t)
	h.rt.SetBuildBase("node:22-alpine", "sha256:old")
	ref := h.service("web", apitypes.CreateServiceRequest{})
	if _, err := h.c.DeploySource(h.ctx, testSystem, ref, sourceTar("v1"), 1<<20); err != nil {
		t.Fatal(err)
	}
	h.waitStatus(ref, 1, apitypes.ReleaseActive)
	scan := h.waitScan(ref, apitypes.ScanScanned)
	if scan.Base == nil || scan.Base.Image != "node:22-alpine" || scan.Base.Digest != "sha256:old" || scan.Base.Moved {
		t.Fatalf("base = %+v", scan.Base)
	}

	// A day later the tag points at a newer image.
	h.rt.SetRemoteDigest("node:22-alpine", "sha256:new")
	h.dueRefresh()
	eventually(t, "the base check", func() bool {
		d, err := h.c.ServiceVulnerabilities(h.ctx, testSystem, ref, 0)
		return err == nil && d.Scan.Base != nil && d.Scan.Base.Moved && d.Scan.Base.CheckedAt != nil
	})
	if !slices.Contains(h.rt.Ops(), "remote-digest node:22-alpine") {
		t.Fatalf("ops = %v", h.rt.Ops())
	}
}

// An imported database reaches every builder as a scan step and the
// live images are matched against it; offline builders never download.
func TestImportScanDatabase(t *testing.T) {
	h := newHarness(t)
	h.opts.ScanQuiet = 20 * time.Millisecond
	h.opts.Config.Scan.Offline = true
	h.restart()
	ref := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(ref, "web:1")
	eventually(t, "the scan to fail without a database", func() bool {
		d, err := h.c.ServiceVulnerabilities(h.ctx, testSystem, ref, 0)
		return err == nil && strings.Contains(d.Scan.Error, "dock scanning import")
	})

	admin := Actor{Kind: "token", OrgID: 1, Name: "admin", TokenID: 2, Scope: apitypes.ScopeAdmin}
	if _, err := h.c.ImportScanDatabase(h.ctx, admin, bytes.NewReader([]byte("db"))); err == nil {
		t.Fatal("an org admin imported a database; operators only")
	}
	if _, err := h.c.ImportScanDatabase(h.ctx, testSystem, bytes.NewReader(nil)); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("an empty archive: %v", err)
	}
	st, err := h.c.ImportScanDatabase(h.ctx, testSystem, bytes.NewReader(bytes.Repeat([]byte("db"), 1000)))
	if err != nil || !strings.HasPrefix(st.Imported, "sha256:") || !st.Offline {
		t.Fatalf("status = %+v, %v", st, err)
	}
	// Retried scans wait for their backoff; ask for one now.
	if _, err := h.c.ScanService(h.ctx, testSystem, ref, 0); err != nil {
		t.Fatal(err)
	}
	h.waitScan(ref, apitypes.ScanScanned)
	st, err = h.c.ScanningStatus(h.ctx, testSystem)
	if err != nil || len(st.Builders) == 0 || !st.Builders[0].Imported || st.Builders[0].DatabaseBuilt == nil {
		t.Fatalf("status = %+v, %v", st, err)
	}
	for _, op := range h.rt.Ops() {
		if op == "scan-database update" {
			t.Fatalf("an offline builder downloaded a database: %v", h.rt.Ops())
		}
	}
}

// dueRefresh makes every builder's database a day old and starts the
// scanner, as the hourly pass would.
func (h *harness) dueRefresh() {
	h.t.Helper()
	servers, err := h.c.store.ListServers(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	day := now().Add(-25 * time.Hour).Unix()
	for _, s := range servers {
		if err := h.c.store.SetServerScanChecked(h.ctx, store.SetServerScanCheckedParams{ScanDbCheckedAt: &day, ID: s.ID}); err != nil {
			h.t.Fatal(err)
		}
	}
	h.c.scans.mu.Lock()
	h.c.scans.refreshAt = now().Add(-time.Minute)
	h.c.scans.mu.Unlock()
	h.c.kickScanRefresh()
}
