//go:build integration

package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
)

// scanImages are the pinned scanners the daemon uses by default.
func scanImages() node.ScanImages {
	d := config.Default().Scan
	return node.ScanImages{Syft: d.SyftImage, Grype: d.GrypeImage}
}

// Syft runs inside the scanned image (here one without the scanner, as
// every app image is) and finds its operating system and packages; the
// output is syft-json Grype reads.
func TestSBOM(t *testing.T) {
	e := newEnv(t)
	e.DataDir = t.TempDir()
	ctx := ctxTimeout(t, 5*time.Minute)
	var out bytes.Buffer
	res, err := e.SBOM(ctx, node.SBOMRequest{
		Images: scanImages(), Image: shellImage, Labels: node.Labels{ControlPlane: e.prefix},
		Limits: node.ScanLimits{MemoryBytes: 512 << 20, Timeout: 5 * time.Minute},
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if res.Packages < 10 || res.Distro == "" || res.Pulled || res.Bytes != int64(out.Len()) {
		t.Fatalf("result = %+v (%d bytes)", res, out.Len())
	}
	var doc struct {
		Artifacts []struct {
			Type string `json:"type"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || len(doc.Artifacts) != res.Packages || doc.Artifacts[0].Type != "apk" {
		t.Fatalf("SBOM: %d artifacts, %v", len(doc.Artifacts), err)
	}
	e.noScanJobs(ctx)
}

// A scan's ctx ending (a build or deploy starting) kills its job at once
// and leaves no container behind.
func TestSBOMPreempted(t *testing.T) {
	e := newEnv(t)
	e.DataDir = t.TempDir()
	ctx := ctxTimeout(t, 5*time.Minute)
	// The binary is copied out once; time only the scan itself.
	if _, err := e.syftBinary(ctx, scanImages().Syft); err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithCancel(ctx)
	go func() {
		e.waitScanJob(ctx)
		cancel()
	}()
	start := time.Now()
	_, err := e.SBOM(sctx, node.SBOMRequest{Images: scanImages(), Image: httpImage, Labels: node.Labels{ControlPlane: e.prefix}}, new(bytes.Buffer))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
	if took := time.Since(start); took > 30*time.Second {
		t.Fatalf("a cancelled scan took %s to end", took)
	}
	e.noScanJobs(ctx)
}

// The database downloads once (about 200 MB, 3.4 GB on disk) and a match
// reads it without network. Opt in with DOCK_SCAN_DB=1: CI shouldn't
// download it on every run.
func TestScanDatabaseAndMatch(t *testing.T) {
	if os.Getenv("DOCK_SCAN_DB") != "1" {
		t.Skip("set DOCK_SCAN_DB=1 to download the vulnerability database")
	}
	e := newEnv(t)
	e.DataDir = t.TempDir()
	ctx := ctxTimeout(t, 20*time.Minute)
	e.ScanNetwork = "bridge"
	t.Cleanup(func() {
		_, _ = e.cli.VolumeRemove(context.Background(), e.scanDBVolume(), client.VolumeRemoveOptions{})
	})
	db, err := e.ScanDatabase(ctx, node.ScanDatabaseRequest{Images: scanImages(), Update: true, Labels: node.Labels{ControlPlane: e.prefix}})
	if err != nil || db.Built.IsZero() {
		t.Fatalf("database = %+v, %v", db, err)
	}
	var sbom bytes.Buffer
	if _, err := e.SBOM(ctx, node.SBOMRequest{Images: scanImages(), Image: shellImage, Labels: node.Labels{ControlPlane: e.prefix}}, &sbom); err != nil {
		t.Fatal(err)
	}
	n := 0
	res, err := e.Match(ctx, node.MatchRequest{Images: scanImages(), Labels: node.Labels{ControlPlane: e.prefix}}, &sbom, func(f node.Finding) error {
		if f.ID == "" || f.Package == "" {
			t.Errorf("finding = %+v", f)
		}
		n++
		return nil
	})
	if err != nil || res.DatabaseBuilt.IsZero() {
		t.Fatalf("match = %+v, %v (%d findings)", res, err, n)
	}
}

// waitScanJob waits until one of the env's scan jobs is running.
func (e *testEnv) waitScanJob(ctx context.Context) {
	for ctx.Err() == nil {
		list, _ := e.cli.ContainerList(ctx, client.ContainerListOptions{Filters: e.scanFilter()})
		if len(list.Items) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// noScanJobs fails when one of the env's scan jobs is left, a second
// after its call returned (removal is part of returning).
func (e *testEnv) noScanJobs(ctx context.Context) {
	e.t.Helper()
	list, err := e.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: e.scanFilter()})
	if err != nil || len(list.Items) != 0 {
		e.t.Fatalf("scan jobs left: %d, %v", len(list.Items), err)
	}
}

func (e *testEnv) scanFilter() client.Filters {
	return client.Filters{}.Add("label", node.LabelControlPlane+"="+e.prefix).Add("label", node.LabelRole+"="+node.RoleJob)
}

// A registry tells which digest a tag points at, without a pull; and an
// import job reads the archive it is given (here not an archive, which
// Grype refuses) and leaves nothing behind.
func TestRemoteDigestAndImport(t *testing.T) {
	e := newEnv(t)
	e.DataDir = t.TempDir()
	ctx := ctxTimeout(t, 5*time.Minute)
	d, err := e.RemoteDigest(ctx, shellImage, node.PullOptions{})
	if err != nil || !strings.HasPrefix(d, "sha256:") {
		t.Fatalf("remote digest of %s = %q, %v", shellImage, d, err)
	}
	junk := []byte("not a database archive")
	_, err = e.ImportScanDatabase(ctx, node.ScanDatabaseRequest{Images: scanImages(), Labels: node.Labels{ControlPlane: e.prefix}},
		bytes.NewReader(junk), int64(len(junk)))
	if err == nil || !strings.Contains(err.Error(), "db-import") {
		t.Fatalf("importing junk: %v; want Grype's refusal from the import job", err)
	}
	t.Log(err)
	if left, _ := os.ReadDir(e.DataDir + "/scan"); len(left) != 0 {
		t.Fatalf("left in the data dir: %v", left)
	}
	e.noScanJobs(ctx)
}
