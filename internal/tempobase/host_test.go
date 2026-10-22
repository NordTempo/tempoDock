package tempobase

import (
	"errors"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// The Host keeps the panel's records per connection with the contract's
// errors: ErrDuplicate, ErrNotFound, and a decision only on a pending,
// unexpired changeset.
func TestHostStore(t *testing.T) {
	h := newHarness(t)
	h.admin()
	viewer := h.user("viewer@example.com", tbtypes.RoleViewer)
	h.connection("orders", false)
	c, err := h.store.GetConnectionByName(h.ctx, "orders")
	if err != nil {
		t.Fatal(err)
	}
	host, id := h.Host(), c.ID
	pa := viewer.panelActor()

	if acc, err := host.Access(h.ctx, id); acc != nil || err != nil {
		t.Fatalf("Access before setup = %v, %v", acc, err)
	}
	if err := host.PutAccess(h.ctx, id, datapanel.Access{Engine: apitypes.EnginePostgres, ServerVersion: "17.2", ReaderSecret: []byte{1}, WriterSecret: []byte{2}, SetUpAt: h.clock, SetUpBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	if err := host.SetGrantsHash(h.ctx, id, "abc"); err != nil {
		t.Fatal(err)
	}
	if acc, err := host.Access(h.ctx, id); err != nil || acc.GrantsSyncedHash != "abc" || acc.ServerVersion != "17.2" {
		t.Fatalf("Access = %+v, %v", acc, err)
	}

	cols := []apitypes.DataColumnRef{{Schema: "public", Table: "users", Column: "email"}, {Schema: "public", Table: "users", Column: "email"}}
	if err := host.ReplaceSensitiveColumns(h.ctx, id, cols); err != nil {
		t.Fatal(err)
	}
	if got, _ := host.SensitiveColumns(h.ctx, id); len(got) != 1 {
		t.Fatalf("sensitive columns %v", got)
	}

	owner, _ := host.SavedQueryOwner(h.ctx, pa)
	q, err := host.SaveQuery(h.ctx, id, owner, 0, "recent", "select 1", h.clock)
	if err != nil {
		t.Fatal(err)
	}
	other, err := host.SaveQuery(h.ctx, id, owner, 0, "other", "select 2", h.clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.SaveQuery(h.ctx, id, owner, other.ID, "recent", "select 3", h.clock); !errors.Is(err, datapanel.ErrDuplicate) {
		t.Fatalf("rename onto another: %v", err)
	}
	if _, err := host.SaveQuery(h.ctx, id, owner, 999, "x", "select 4", h.clock); !errors.Is(err, datapanel.ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	if ok, _ := host.DeleteSavedQuery(h.ctx, id, owner, q.ID); !ok {
		t.Fatal("delete found nothing")
	}
	agent := h.token(viewer, tbtypes.CreateTokenRequest{Name: "agent"})
	if n, _ := host.SavedQueryOwner(h.ctx, agent.panelActor()); n != 0 {
		t.Fatalf("token's saved query owner %d", n)
	}

	newCS := func() datapanel.Changeset {
		cs, err := host.CreateChangeset(h.ctx, datapanel.Changeset{
			Database: id, Author: pa.Name, AuthorKind: pa.Kind, UserID: pa.UserID, Status: apitypes.ChangesetPending,
			Changes: []apitypes.DataChange{{Table: "public.users"}}, SQL: "UPDATE ...", CreatedAt: h.clock, ExpiresAt: h.clock.Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		return cs
	}
	cs := newCS()
	decided, err := host.DecideChangeset(h.ctx, datapanel.Decision{Database: id, ID: cs.ID, Status: apitypes.ChangesetCommitted, DecidedAt: h.clock, DecidedBy: "admin", RowsAffected: []int64{1}})
	if err != nil || decided.Status != apitypes.ChangesetCommitted || len(decided.RowsAffected) != 1 || len(decided.Changes) != 1 {
		t.Fatalf("decided %+v, %v", decided, err)
	}
	if _, err := host.DecideChangeset(h.ctx, datapanel.Decision{Database: id, ID: cs.ID, Status: apitypes.ChangesetRejected, DecidedAt: h.clock}); !errors.Is(err, datapanel.ErrNotFound) {
		t.Fatalf("second decision: %v", err)
	}
	late := newCS()
	h.clock = h.clock.Add(2 * time.Hour)
	if err := host.ExpireChangesets(h.ctx, h.clock); err != nil {
		t.Fatal(err)
	}
	if got, _ := host.Changeset(h.ctx, id, late.ID); got.Status != apitypes.ChangesetExpired {
		t.Fatalf("late changeset %s", got.Status)
	}
	if pending, _ := host.Changesets(h.ctx, id, true, 50); len(pending) != 0 {
		t.Fatalf("pending %v", pending)
	}
	if _, err := host.Changeset(h.ctx, id+1, late.ID); !errors.Is(err, datapanel.ErrNotFound) {
		t.Fatalf("another connection's changeset: %v", err)
	}

	// History is the actor's own data.query events.
	host.Audit(h.ctx, pa, &c, datapanel.AuditEntry{Action: apitypes.AuditDataQuery, Summary: "select ?", Detail: &datapanel.AuditDetail{SQL: "select 1", Rows: 1, Outcome: apitypes.OutcomeOK}})
	host.Audit(h.ctx, agent.panelActor(), &c, datapanel.AuditEntry{Action: apitypes.AuditDataQuery, Summary: "select ?", Detail: &datapanel.AuditDetail{SQL: "select 2", Outcome: apitypes.OutcomeOK}})
	hist, err := host.QueryHistory(h.ctx, pa, &c, 0, 10)
	if err != nil || len(hist) != 1 || hist[0].SQL != "select 1" {
		t.Fatalf("history %+v, %v", hist, err)
	}
}
