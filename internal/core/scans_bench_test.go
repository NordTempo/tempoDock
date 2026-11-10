package core

import (
	"fmt"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// BenchmarkScanBusy is what image scanning adds to every build, deploy,
// wake and move (ADR 0074): marking it busy and done, with no scan
// running. Budget: under 100 ns, no allocation.
func BenchmarkScanBusy(b *testing.B) {
	c := &Core{scans: newScanState()}
	c.scans.running = true // a kick is a non-blocking send, as while a scanner waits
	b.ReportAllocs()
	for b.Loop() {
		c.scanBusy()
		c.scanDone()
	}
}

// benchFindings are n findings across severities and package kinds.
func benchFindings(n int) []node.Finding {
	out := make([]node.Finding, n)
	for i := range out {
		out[i] = node.Finding{
			ID: fmt.Sprintf("CVE-2026-%05d", i), Severity: node.Severity(i % 6), Package: fmt.Sprintf("pkg-%03d", i%120),
			Version: "1.2.3", Type: []string{"apk", "npm", "go-module"}[i%3], EPSS: -1,
		}
		if i%2 == 0 {
			out[i].FixedIn = "1.2.4"
		}
	}
	return out
}

// BenchmarkScanFindingsWrite is one match's write: 2,000 findings of an
// image replaced in one transaction.
func BenchmarkScanFindingsWrite(b *testing.B) {
	bc := newBenchCore(b)
	if err := bc.c.syncScans(bc.ctx); err != nil {
		b.Fatal(err)
	}
	list, err := bc.c.ListImages(bc.ctx, testSystem, false)
	if err != nil || len(list.Images) == 0 || list.Images[0].Scan == nil {
		b.Fatalf("images = %+v, %v", list, err)
	}
	id, found := list.Images[0].Scan.ID, benchFindings(2000)
	b.ReportAllocs()
	for b.Loop() {
		if err := bc.c.writeFindings(bc.ctx, id, found, node.MatchResult{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkListImages is the Images page of the seeded org: 180 live
// releases (60 services in three environments) and their images, with
// 300 findings on each image.
func BenchmarkListImages(b *testing.B) {
	bc := newBenchCore(b)
	if err := bc.c.syncScans(bc.ctx); err != nil {
		b.Fatal(err)
	}
	list, err := bc.c.ListImages(bc.ctx, testSystem, false)
	if err != nil {
		b.Fatal(err)
	}
	for _, img := range list.Images {
		if err := bc.c.writeFindings(bc.ctx, img.Scan.ID, benchFindings(300), node.MatchResult{}); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		l, err := bc.c.ListImages(bc.ctx, testSystem, false)
		if err != nil || len(l.Images) == 0 || l.Images[0].Scan.State != apitypes.ScanScanned {
			b.Fatalf("images = %+v, %v", l, err)
		}
	}
}
