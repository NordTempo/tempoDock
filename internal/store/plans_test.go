package store

import (
	"strings"
	"testing"
	"time"
)

// What sign-in, the members page and the retention prunes run reads
// through an index.
func TestQueryPlansUseIndexes(t *testing.T) {
	f := newDataFixture(t)
	now := time.Now()
	checkPlans(t, f.s, []planCase{
		{"DeleteUserSessions", deleteUserSessions, []any{1}},
		{"DeleteExpiredSessions", deleteExpiredSessions, []any{now}},
		{"ListOrgMembers", listOrgMembers, []any{now, 1}},
		{"PruneResolvedAlerts", pruneResolvedAlerts, []any{now}},
		{"PruneGitDeliveries", pruneGitDeliveries, []any{now}},
	})
}

// A site dashboard's deploy markers seek each service's releases by finish
// time. With several services behind a site the rows in range are still
// sorted, which is a few hundred at most (LIMIT 200 per range).
func TestSiteDeploysPlan(t *testing.T) {
	f := newDataFixture(t)
	rows, err := f.s.DB.Query("EXPLAIN QUERY PLAN "+listSiteDeploys, 1, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if got := strings.Join(plan, "; "); !strings.Contains(got, "SEARCH releases USING INDEX releases_finished (service_id=? AND finished_at>? AND finished_at<?)") {
		t.Errorf("ListSiteDeploys: %s", got)
	}
}
