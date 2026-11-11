//go:build integration

package mysql

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/mysql/wire"
)

// monitorTarget is tg with the monitor's login.
func monitorTarget(tg data.Target) data.Target {
	tg.Monitor = data.Credentials{User: string(data.RoleMonitor), Password: "monitor-pw"}
	return tg
}

// rawConn opens a plain root connection to tg's database.
func rawConn(t *testing.T, tg data.Target) *wire.Conn {
	t.Helper()
	nc, err := tg.Dial(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c, err := wire.Connect(nc, wire.Config{User: owner.User, Password: owner.Password, Database: tg.Database})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestInsights(t *testing.T) {
	each(t, func(t *testing.T, e *Engine, tg data.Target) {
		ctx := t.Context()
		tg = monitorTarget(tg)
		all := apitypes.DataInsightsRequest{Views: apitypes.InsightViewsOf(apitypes.EngineMySQL)}

		// No monitor yet: not set up, which the panel answers with
		// EnsureMonitor.
		if _, err := e.Insights(ctx, tg, data.Call{}, all); !errors.Is(err, data.ErrNotSetUp) {
			t.Fatalf("before the monitor: %v", err)
		}
		if err := e.EnsureMonitor(ctx, tg, owner); err != nil {
			t.Fatal(err)
		}
		ownerExec(t, e, tg, "CREATE INDEX items_name ON items (name)")
		ownerExec(t, e, tg, "CREATE INDEX items_name2 ON items (name)")
		defer ownerExec(t, e, tg, "DROP INDEX items_name2 ON items")
		defer ownerExec(t, e, tg, "DROP INDEX items_name ON items")
		for range 3 {
			query(t, e, tg, data.Call{}, "SELECT name FROM items WHERE price > 2")
		}

		// A lock chain: holder updates a row and stays in its
		// transaction; waiter wants the same row.
		holder, waiter := rawConn(t, tg), rawConn(t, tg)
		for _, sql := range []string{"SET innodb_lock_wait_timeout = 30", "START TRANSACTION", "UPDATE logs SET msg = 'held' WHERE msg = 'a'"} {
			if _, err := holder.Exec(sql); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := waiter.Exec("SET innodb_lock_wait_timeout = 30"); err != nil {
			t.Fatal(err)
		}
		waited := make(chan error, 1)
		go func() { _, err := waiter.Exec("UPDATE logs SET msg = 'wait' WHERE msg = 'a'"); waited <- err }()
		defer func() { _, _ = holder.Exec("ROLLBACK") }()

		var ins apitypes.DataInsights
		var w, h apitypes.DataSession
		deadline := time.Now().Add(5 * time.Second)
		for {
			var err error
			if ins, err = e.Insights(ctx, tg, data.Call{}, all); err != nil {
				t.Fatal(err)
			}
			for _, s := range ins.Sessions {
				switch int64(s.PID) {
				case int64(waiter.ID):
					w = s
				case int64(holder.ID):
					h = s
				}
			}
			if len(w.BlockedBy) > 0 || time.Now().After(deadline) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if ins.Engine != apitypes.EngineMySQL || ins.Since == nil || ins.Overview == nil || ins.Overview.Connections.Max == 0 ||
			ins.Overview.Connections.Used < 3 || ins.Overview.Bytes == 0 || ins.Overview.QueryStats != apitypes.QueryStatsOn {
			t.Fatalf("overview: %+v", ins.Overview)
		}
		if w.State != "active" || w.Wait != "Lock: row" || !slices.Equal(w.BlockedBy, []string{h.ID}) || h.State != "idle_in_transaction" || h.Kind != "client" {
			t.Fatalf("lock chain: waiter %+v holder %+v (all %+v)", w, h, ins.Sessions)
		}
		if ins.Overview.Connections.Waiting < 1 || ins.Overview.Connections.IdleInTransaction < 1 || ins.Overview.Connections.OldestTransactionAt == nil {
			t.Errorf("pressure: %+v", ins.Overview.Connections)
		}
		found := slices.ContainsFunc(ins.Queries, func(s apitypes.DataStatement) bool {
			return strings.Contains(s.Text, "`price` > ?") && s.Calls >= 3
		})
		if !found || ins.QueriesTracked == 0 {
			t.Errorf("queries: no digest with the literal replaced: %+v", ins.Queries)
		}
		var items *apitypes.DataTableStats
		for i := range ins.Tables {
			if ins.Tables[i].Name == "items" {
				items = &ins.Tables[i]
			}
		}
		if items == nil || items.TotalBytes == nil || *items.TotalBytes == 0 {
			t.Errorf("tables: %+v", ins.Tables)
		}
		dup := slices.ContainsFunc(ins.Hints, func(h apitypes.DataHint) bool {
			return h.Kind == apitypes.HintDuplicateIndex && h.Subject == "app.items_name2" && strings.Contains(h.Copy, "DROP INDEX `items_name2`")
		})
		if !dup || !slices.ContainsFunc(ins.Indexes, func(x apitypes.DataIndexStats) bool { return x.Primary && x.Table == "items" && x.Bytes != nil }) {
			t.Errorf("indexes: %+v hints %+v", ins.Indexes, ins.Hints)
		}
		if p, err := e.Pressure(ctx, tg); err != nil || p.Waiting < 1 {
			t.Errorf("pressure: %+v %v", p, err)
		}

		// Cancel the waiter's statement (only the one seen), then end the
		// holder's connection.
		stale := w.QueryAt.Add(-time.Hour)
		if res, err := e.Cancel(ctx, tg, owner, w.ID, &stale, false); err != nil || res.Found {
			t.Fatalf("cancel of another statement: %+v %v", res, err)
		}
		res, err := e.Cancel(ctx, tg, owner, w.ID, w.QueryAt, false)
		if err != nil || !res.Found || !strings.Contains(res.Text, "UPDATE logs") {
			t.Fatalf("cancel: %+v %v", res, err)
		}
		select {
		case err := <-waited:
			if wire.ErrorCode(err) != errInterrupted {
				t.Errorf("waiter ended with %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the cancelled statement still runs")
		}
		if res, err := e.Cancel(ctx, tg, owner, h.ID, nil, true); err != nil || !res.Found {
			t.Fatalf("terminate: %+v %v", res, err)
		}
		if err := holder.Ping(); err == nil {
			t.Error("the ended connection still answers")
		}
		if _, err := e.Cancel(ctx, tg, owner, "1.1", nil, true); err != nil {
			t.Errorf("a session of another run: %v", err)
		}
		if err := e.ResetQueryStats(ctx, tg, owner); err != nil {
			t.Fatal(err)
		}
		if state, _, err := e.EnableQueryStats(ctx, tg, owner); err != nil || state != apitypes.QueryStatsOn {
			t.Errorf("enable: %s %v", state, err)
		}
	})
}
