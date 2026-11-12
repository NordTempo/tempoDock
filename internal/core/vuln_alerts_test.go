package core

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// countingDB counts the reads run through it: writes follow from what
// changed, reads are what a request shape costs.
type countingDB struct {
	db *sql.DB
	n  atomic.Int64
}

func (c *countingDB) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return c.db.ExecContext(ctx, q, args...)
}

func (c *countingDB) PrepareContext(ctx context.Context, q string) (*sql.Stmt, error) {
	return c.db.PrepareContext(ctx, q)
}

func (c *countingDB) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	c.n.Add(1)
	return c.db.QueryContext(ctx, q, args...)
}

func (c *countingDB) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	c.n.Add(1)
	return c.db.QueryRowContext(ctx, q, args...)
}

// Rechecking an org's vulnerability alerts (an ignore or the setting
// changed) reads the same few queries however many alerts are open; it
// used to read each alert's release, scan, uses, setting, findings and
// ignores.
func TestRecheckVulnAlertsFixedQueries(t *testing.T) {
	h := scanHarness(t)
	db := &countingDB{db: h.store.DB}
	counted := *h.store
	counted.Queries = store.New(db)
	h.opts.Store = &counted
	h.restart()
	h.rt.SetScanFindings("web", webFindings)

	made := 0
	recheck := func(services int) int64 {
		t.Helper()
		for ; made < services; made++ {
			ref := h.service(fmt.Sprintf("web%d", made), apitypes.CreateServiceRequest{})
			h.deploy(ref, "web:1")
			scan := h.waitScan(ref, apitypes.ScanScanned)
			uses, err := h.store.ListImageScanLiveUses(h.ctx, &scan.ID)
			if err != nil {
				t.Fatal(err)
			}
			found, err := h.store.ListScanFindingsAtLeast(h.ctx, store.ListScanFindingsAtLeastParams{ScanIds: []int64{scan.ID}, MinSeverity: int64(node.SeverityHigh)})
			if err != nil {
				t.Fatal(err)
			}
			for _, u := range uses {
				if u.ServiceID == h.serviceID(ref) {
					h.c.raiseVulnAlert(h.ctx, vulnAlertKey+alertServiceKey(u.ServiceID, "", "", ""), scan.ID, u, found,
						func(store.ListScanFindingsAtLeastRow) bool { return true })
				}
			}
			if _, ok := h.vulnAlert(ref); !ok {
				t.Fatalf("%s has no alert", ref)
			}
		}
		before := db.n.Load()
		h.c.recheckVulnAlerts(h.ctx, 1)
		return db.n.Load() - before
	}
	one, six := recheck(1), recheck(6)
	if six != one {
		t.Errorf("rechecking 1 alert read %d queries, 6 alerts %d", one, six)
	}
}
