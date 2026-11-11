package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Importing a vulnerability database (ADR 0074, phase 3), for installs
// that can't download one: an operator uploads Grype's database archive
// (`dock scanning import FILE`), the control plane keeps it in its data
// dir, and each builder imports it as a scan step, so a build or deploy
// still preempts it. A builder records the archive it imported (its
// sha256), so one that joins later, or was offline, imports it too.
// With `[scan] offline`, builders never try to download one.

// scanImportPath is where the imported archive is kept.
func (c *Core) scanImportPath() string {
	return filepath.Join(c.cfg.DataDir, "scan", "database.tar.zst")
}

// ImportScanDatabase keeps archive (a Grype database archive) for every
// builder to import at the next quiet minute, replacing one imported
// before. Operators only.
func (c *Core) ImportScanDatabase(ctx context.Context, a Actor, archive io.Reader) (apitypes.ScanningStatus, error) {
	if err := a.requireOperator("importing a vulnerability database"); err != nil {
		return apitypes.ScanningStatus{}, err
	}
	path := c.scanImportPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return apitypes.ScanningStatus{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".import-*")
	if err != nil {
		return apitypes.ScanningStatus{}, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, sum), io.LimitReader(archive, node.MaxScanDatabaseArchive+1))
	if err = errors.Join(err, tmp.Close()); err != nil {
		return apitypes.ScanningStatus{}, fmt.Errorf("receive the database archive: %w", err)
	}
	switch {
	case n == 0:
		return apitypes.ScanningStatus{}, invalid("the database archive is empty; download one from Grype's listing (`grype db list`) on a machine with internet access")
	case n > node.MaxScanDatabaseArchive:
		return apitypes.ScanningStatus{}, invalid("the database archive is over %d MB; Grype's is about 200 MB", node.MaxScanDatabaseArchive>>20)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return apitypes.ScanningStatus{}, err
	}
	digest := "sha256:" + hex.EncodeToString(sum.Sum(nil))
	if err := c.store.SetScanDatabaseImport(ctx, store.SetScanDatabaseImportParams{ScanDbImport: digest, UpdatedBy: a.String(), UpdatedAt: now()}); err != nil {
		return apitypes.ScanningStatus{}, err
	}
	c.scans.mu.Lock()
	c.scans.imported = digest
	c.scans.mu.Unlock()
	c.audit(ctx, a.platform(), "scanning.import", nil, nil, "scanning", fmt.Sprintf("imported a vulnerability database archive (%d MB, %s)", n>>20, digest[:19]))
	c.kickScans()
	return c.ScanningStatus(ctx, a)
}

// scanImportDue picks a builder that hasn't imported the archive an
// operator imported last.
func (c *Core) scanImportDue(builders []store.Server) (store.Server, bool) {
	c.scans.mu.Lock()
	want := c.scans.imported
	c.scans.mu.Unlock()
	if want == "" {
		return store.Server{}, false
	}
	for _, s := range builders {
		if s.ScanDbImport != want {
			return s, true
		}
	}
	return store.Server{}, false
}

// importScanDatabase has srv import the kept archive, then queues the
// live images matched against an older database to match again. A
// failure is recorded on the builder and tried again at the next pass
// only once the archive changes, so a bad archive doesn't loop.
func (c *Core) importScanDatabase(ctx context.Context, srv store.Server) {
	rt := c.connected(srv.ID)
	if rt == nil {
		return
	}
	c.scans.mu.Lock()
	want := c.scans.imported
	c.scans.mu.Unlock()
	f, err := os.Open(c.scanImportPath())
	if err != nil {
		c.log.Error("open the imported database archive", "err", err)
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	db, err := rt.ImportScanDatabase(ctx, node.ScanDatabaseRequest{Images: c.scanImages(), Labels: node.Labels{ControlPlane: c.install}}, f, fi.Size())
	if c.scanPreempted(ctx) || c.ctx.Err() != nil {
		return
	}
	wctx := context.WithoutCancel(ctx)
	if err := c.store.SetServerScanImport(wctx, store.SetServerScanImportParams{ScanDbImport: want, ID: srv.ID}); err != nil {
		c.log.Error("record the builder's import", "server", srv.Name, "err", err)
	}
	if err != nil {
		c.log.Warn("import the vulnerability database", "server", srv.Name, "err", err)
		c.setServerScanError(srv.ID, srv.ScanDbBuiltAt, "importing the vulnerability database: "+c.nodeErr(srv.ID, err).Error())
		return
	}
	c.scans.mu.Lock()
	c.scans.db[srv.ID] = db.Built
	c.scans.mu.Unlock()
	c.setServerScanError(srv.ID, ptr(db.Built.Unix()), "")
	c.setScanChecked(srv.ID)
	n, err := c.store.RequeueStaleLiveScans(wctx, ptr(db.Built.Unix()))
	if err != nil {
		c.log.Error("queue images to match again", "err", err)
		return
	}
	c.log.Info("vulnerability database imported", "server", srv.Name, "built", db.Built, "rematch", n)
}
