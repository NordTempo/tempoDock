package remote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Image scans over the link (ADR 0074): the SBOM streams down, the SBOM
// goes back up for the match and its findings come down one by one, and
// a node without a database says so in a way errors.Is reads.
func TestScanOverTheLink(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.rt.SetScanFindings("web", []node.Finding{
		{ID: "CVE-2026-1", Severity: node.SeverityCritical, Package: "openssl", Version: "3.3.1-r0", Type: "apk", FixedIn: "3.3.2-r0", KEV: true, EPSS: 0.5},
		{ID: "CVE-2026-2", Severity: node.SeverityLow, Package: "musl", Version: "1.2.5-r0", Type: "apk", EPSS: -1},
	})
	if _, err := h.client.Pull(ctx, "web:1", io.Discard); err != nil {
		t.Fatal(err)
	}
	pinned, err := h.client.ImageDigest(ctx, "web:1")
	if err != nil || pinned == "" {
		t.Fatalf("digest = %q, %v", pinned, err)
	}
	var sbom bytes.Buffer
	res, err := h.client.SBOM(ctx, node.SBOMRequest{Image: "web:1"}, &sbom)
	if err != nil || res.Packages != 3 || res.Bytes != int64(sbom.Len()) || res.Distro == "" {
		t.Fatalf("sbom = %+v (%d bytes), %v", res, sbom.Len(), err)
	}

	match := func() ([]node.Finding, error) {
		var got []node.Finding
		_, err := h.client.Match(ctx, node.MatchRequest{}, bytes.NewReader(sbom.Bytes()), func(f node.Finding) error {
			got = append(got, f)
			return nil
		})
		return got, err
	}
	if _, err := match(); !errors.Is(err, node.ErrNoScanDatabase) {
		t.Fatalf("match without a database: %v", err)
	}
	if db, err := h.client.ScanDatabase(ctx, node.ScanDatabaseRequest{Update: true}); err != nil || db.Built.IsZero() {
		t.Fatalf("database = %+v, %v", db, err)
	}
	got, err := match()
	if err != nil || len(got) != 2 || got[0] != (node.Finding{ID: "CVE-2026-1", Severity: node.SeverityCritical, Package: "openssl", Version: "3.3.1-r0", Type: "apk", FixedIn: "3.3.2-r0", KEV: true, EPSS: 0.5}) || got[1].EPSS != -1 {
		t.Fatalf("findings = %+v, %v", got, err)
	}
}

// Protocol 27: a registry's digest for a tag, and a database archive
// imported from the control plane.
func TestBaseAndImportOverTheLink(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.rt.SetRemoteDigest("node:22-alpine", "sha256:new")
	if d, err := h.client.RemoteDigest(ctx, "node:22-alpine", node.PullOptions{Username: "u", Password: "p"}); err != nil || d != "sha256:new" {
		t.Fatalf("remote digest = %q, %v", d, err)
	}
	if _, err := h.client.RemoteDigest(ctx, "gone:1", node.PullOptions{}); !errors.Is(err, node.ErrNotFound) {
		t.Fatalf("an unknown tag: %v", err)
	}
	archive := bytes.Repeat([]byte("db"), 1<<20)
	db, err := h.client.ImportScanDatabase(ctx, node.ScanDatabaseRequest{}, bytes.NewReader(archive), int64(len(archive)))
	if err != nil || db.Built.IsZero() {
		t.Fatalf("import = %+v, %v", db, err)
	}
}
