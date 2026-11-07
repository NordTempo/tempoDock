package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

// A query is prepared once, on first use, also when several goroutines
// run it at once, and a transaction runs the same statement.
func TestStatementsPreparedOnFirstUse(t *testing.T) {
	f := newDataFixture(t)
	ctx := context.Background()
	if n := len(f.s.stmts.m); n == 0 {
		t.Fatal("the fixture ran no query")
	}
	before := len(f.s.stmts.m)
	if _, ok := f.s.stmts.m[listOrgMembers]; ok {
		t.Fatal("ListOrgMembers prepared before its first use")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := f.s.ListOrgMembers(ctx, ListOrgMembersParams{OrgID: f.project.OrgID}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	first := f.s.stmts.m[listOrgMembers]
	if first == nil || len(f.s.stmts.m) != before+1 {
		t.Fatalf("statements = %d (was %d), ListOrgMembers %v", len(f.s.stmts.m), before, first)
	}
	if err := f.s.Tx(ctx, func(q *Queries) error {
		_, err := q.ListOrgMembers(ctx, ListOrgMembersParams{OrgID: f.project.OrgID})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if f.s.stmts.m[listOrgMembers] != first || len(f.s.stmts.m) != before+1 {
		t.Error("the transaction prepared the query again")
	}
}

// Transactions waiting for the write lock hold every connection; one that
// runs a query for the first time mustn't need another to prepare it, or
// it waits until busy_timeout (10 s) fails the others.
func TestFirstUseInATransactionNeedsNoSecondConnection(t *testing.T) {
	f := newDataFixture(t)
	ctx := context.Background()
	locked := make(chan struct{})
	var wg sync.WaitGroup
	start := time.Now()
	wg.Go(func() {
		if err := f.s.Tx(ctx, func(q *Queries) error {
			close(locked)
			// Let the others take every other connection, waiting in BEGIN.
			time.Sleep(300 * time.Millisecond)
			_, err := q.CountOtherOrgAdmins(ctx, CountOtherOrgAdminsParams{OrgID: f.project.OrgID, UserID: 1})
			return err
		}); err != nil {
			t.Error(err)
		}
	})
	<-locked
	for range 7 {
		wg.Go(func() {
			if err := f.s.Tx(ctx, func(*Queries) error { return nil }); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("the transactions took %s", took)
	}
}
