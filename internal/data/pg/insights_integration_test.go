//go:build integration

package pg

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// TestInsights reads every view against a real server: a lock chain
// shows who blocks whom, a table under an ACCESS EXCLUSIVE lock is listed
// without its exact size instead of waiting, a duplicate index is
// flagged, and pg_stat_statements shows the owner's statements to the
// monitor, which reads no table data itself.
func TestInsights(t *testing.T) {
	ctx := ctxT(t)
	e := newEngine(t)
	tg := target(70)
	ownerExec(t, ctx, `CREATE TABLE ins_orders (id int PRIMARY KEY, status text);
INSERT INTO ins_orders SELECT g, 'paid' FROM generate_series(1, 1000) g;
CREATE INDEX ins_status ON ins_orders (status);
CREATE INDEX ins_status2 ON ins_orders (status);
CREATE TABLE ins_locked (id int);
SELECT count(*) FROM ins_orders WHERE status = 'paid'`)

	// A lock chain: holder updates row 1 and stays in its transaction,
	// waiter updates the same row.
	holder := ownerConnT(t, ctx)
	if _, err := holder.Exec(ctx, "BEGIN; UPDATE ins_orders SET status = 'held' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	waiter := ownerConnT(t, ctx)
	waited := make(chan error, 1)
	go func() {
		_, err := waiter.Exec(context.Background(), "UPDATE ins_orders SET status = 'waited' WHERE id = 1")
		waited <- err
	}()
	// And a table someone holds an exclusive lock on.
	locker := ownerConnT(t, ctx)
	if _, err := locker.Exec(ctx, "BEGIN; LOCK TABLE ins_locked IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	watch := ownerConnT(t, ctx)
	waitFor(t, ctx, "the waiter to block", func() bool {
		var n int
		_ = watch.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock'").Scan(&n)
		return n > 0
	})

	started := time.Now()
	ins, err := e.Insights(ctx, tg, data.Call{Actor: "test"}, apitypes.DataInsightsRequest{Views: apitypes.InsightViewsOf(apitypes.EnginePostgres), Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(started); d > time.Second {
		t.Errorf("insights took %v with a locked table; size functions must not wait in its lock queue", d)
	}
	if len(ins.Unavailable) > 0 {
		t.Errorf("unavailable: %+v", ins.Unavailable)
	}
	ov := ins.Overview
	if ov == nil || ov.Connections.Max == 0 || ov.Connections.Waiting < 1 || ov.Connections.IdleInTransaction < 1 || ov.QueryStats != apitypes.QueryStatsOn || ov.Bytes == 0 {
		t.Errorf("overview: %+v", ov)
	}

	var holderS, waiterS *apitypes.DataSession
	holderPID, waiterPID := int64(holder.PgConn().PID()), int64(waiter.PgConn().PID())
	for i, s := range ins.Sessions {
		switch s.PID {
		case holderPID:
			holderS = &ins.Sessions[i]
		case waiterPID:
			waiterS = &ins.Sessions[i]
		}
	}
	if holderS == nil || waiterS == nil {
		t.Fatalf("sessions %+v lack the holder %d or the waiter %d", ins.Sessions, holderPID, waiterPID)
	}
	if holderS.State != "idle_in_transaction" || holderS.Kind != "client" || !strings.Contains(holderS.Text, "UPDATE ins_orders") {
		t.Errorf("holder: %+v", holderS)
	}
	if !slices.Contains(waiterS.BlockedBy, holderS.ID) || !strings.HasPrefix(waiterS.Wait, "Lock") {
		t.Errorf("waiter %+v isn't blocked by %s", waiterS, holderS.ID)
	}

	table := func(name string) apitypes.DataTableStats {
		for _, x := range ins.Tables {
			if x.Name == name {
				return x
			}
		}
		t.Fatalf("no table %s in %+v", name, ins.Tables)
		return apitypes.DataTableStats{}
	}
	if x := table("ins_locked"); !x.Locked || x.TotalBytes != nil {
		t.Errorf("locked table: %+v", x)
	}
	if x := table("ins_orders"); x.Locked || x.TotalBytes == nil || *x.TotalBytes == 0 {
		t.Errorf("orders: %+v", x)
	}

	var dup []string
	for _, h := range ins.Hints {
		if h.Kind == apitypes.HintDuplicateIndex {
			dup = append(dup, h.Subject)
		}
	}
	if len(dup) != 1 || dup[0] != "public.ins_status2" {
		t.Errorf("duplicate index hints: %v (all: %+v)", dup, ins.Hints)
	}

	found := false
	for _, q := range ins.Queries {
		if strings.Contains(q.Text, "FROM ins_orders WHERE status = $1") && q.Calls >= 1 && q.User == "postgres" {
			found = true
		}
		if q.User == string(data.RoleMonitor) {
			t.Errorf("the monitor's own statement is listed: %+v", q)
		}
	}
	if !found {
		t.Errorf("queries lack the owner's count: %+v", ins.Queries)
	}

	if _, err := locker.Exec(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err := <-waited; err != nil {
		t.Fatal(err)
	}

	// The monitor reads statistics, never table data.
	mon, err := connect(ctx, tg, tg.Monitor)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mon.Close(ctx) }()
	if _, err := mon.Exec(ctx, "SELECT * FROM ins_orders"); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("the monitor selected from a table: %v", err)
	}
}

// TestInsightsCreatesMonitor: a monitor whose login fails (a database set
// up before it existed, or with an older password) is ErrNotSetUp, and
// EnsureMonitor fixes it. Pressure counts connections the same way.
func TestInsightsCreatesMonitor(t *testing.T) {
	ctx := ctxT(t)
	e := newEngine(t)
	tg := target(71)
	tg.Monitor.Password = "a-new-monitor-pw"
	req := apitypes.DataInsightsRequest{Views: []apitypes.DataInsightView{apitypes.InsightOverview}}
	if _, err := e.Insights(ctx, tg, data.Call{}, req); !errors.Is(err, data.ErrNotSetUp) {
		t.Fatalf("insights with a stale monitor password: %v", err)
	}
	if err := e.EnsureMonitor(ctx, tg, owner); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.EnsureMonitor(context.Background(), target(0), owner); err != nil {
			t.Error(err)
		}
	})
	if _, err := e.Insights(ctx, tg, data.Call{}, req); err != nil {
		t.Fatal(err)
	}
	p, err := e.Pressure(ctx, tg)
	if err != nil || p.Used < 1 || p.Max == 0 || p.Full {
		t.Fatalf("pressure: %+v %v", p, err)
	}
}

// ownerConnT opens an uncached owner connection closed with the test.
func ownerConnT(t *testing.T, ctx context.Context) *pgx.Conn {
	t.Helper()
	pg, err := connect(ctx, target(0), owner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close(context.Background()) })
	return pg
}

// waitFor polls cond until it holds or ctx ends.
func waitFor(t *testing.T, ctx context.Context, what string, cond func() bool) {
	t.Helper()
	for !cond() {
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v", what, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestInsightActions: a cancel only hits the query the caller saw (by
// session and query start), ending a session rolls back its transaction,
// query stats reset, and creating the extension again is a no-op.
func TestInsightActions(t *testing.T) {
	ctx := ctxT(t)
	e := newEngine(t)
	tg := target(72)

	sleeper := ownerConnT(t, ctx)
	slept := make(chan error, 1)
	go func() {
		_, err := sleeper.Exec(context.Background(), "SELECT pg_sleep(30)")
		slept <- err
	}()
	idle := ownerConnT(t, ctx)
	if _, err := idle.Exec(ctx, "BEGIN; CREATE TABLE ins_tx (id int)"); err != nil {
		t.Fatal(err)
	}
	var ins apitypes.DataInsights
	waitFor(t, ctx, "the sleeper to run", func() bool {
		var err error
		ins, err = e.Insights(ctx, tg, data.Call{}, apitypes.DataInsightsRequest{Views: []apitypes.DataInsightView{apitypes.InsightSessions}})
		return err == nil && session(ins, int64(sleeper.PgConn().PID())) != nil
	})
	sleep := session(ins, int64(sleeper.PgConn().PID()))
	tx := session(ins, int64(idle.PgConn().PID()))
	if tx == nil || tx.State != "idle_in_transaction" {
		t.Fatalf("idle transaction: %+v", tx)
	}

	earlier := sleep.QueryAt.Add(-time.Second)
	if res, err := e.Cancel(ctx, tg, owner, sleep.ID, &earlier, false); err != nil || res.Found {
		t.Fatalf("cancel with another query's start: %+v %v", res, err)
	}
	res, err := e.Cancel(ctx, tg, owner, sleep.ID, sleep.QueryAt, false)
	if err != nil || !res.Found || res.User != "postgres" || !strings.Contains(res.Text, "pg_sleep") {
		t.Fatalf("cancel: %+v %v", res, err)
	}
	if err := <-slept; err == nil || !strings.Contains(err.Error(), "canceling statement") {
		t.Fatalf("the sleeping query: %v", err)
	}

	if res, err := e.Cancel(ctx, tg, owner, tx.ID, nil, true); err != nil || !res.Found {
		t.Fatalf("terminate: %+v %v", res, err)
	}
	if !idle.IsClosed() {
		if _, err := idle.Exec(ctx, "SELECT 1"); err == nil {
			t.Fatal("the ended session still answers")
		}
	}
	var n int
	if err := sleeper.QueryRow(ctx, "SELECT count(*) FROM pg_class WHERE relname = 'ins_tx'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("the ended session's transaction wasn't rolled back: %d %v", n, err)
	}
	if _, err := e.Cancel(ctx, tg, owner, "not-a-session", nil, false); !errors.Is(err, data.ErrInvalid) {
		t.Fatalf("bad session ID: %v", err)
	}

	if state, created, err := e.EnableQueryStats(ctx, tg, owner); err != nil || state != apitypes.QueryStatsOn || created {
		t.Fatalf("enable with the extension there: %s %v %v", state, created, err)
	}
	if err := e.ResetQueryStats(ctx, tg, owner); err != nil {
		t.Fatal(err)
	}
	ins, err = e.Insights(ctx, tg, data.Call{}, apitypes.DataInsightsRequest{Views: []apitypes.DataInsightView{apitypes.InsightQueries}, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range ins.Queries {
		if strings.Contains(q.Text, "pg_sleep") {
			t.Errorf("a statement from before the reset: %+v", q)
		}
	}
}

// session finds a session by process ID.
func session(ins apitypes.DataInsights, pid int64) *apitypes.DataSession {
	for i, s := range ins.Sessions {
		if s.PID == pid {
			return &ins.Sessions[i]
		}
	}
	return nil
}
