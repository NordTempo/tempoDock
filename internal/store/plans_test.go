package store

import (
	"strings"
	"testing"
	"time"
)

// What sign-in, the members page, the project overview and the retention
// prunes run reads through an index.
func TestQueryPlansUseIndexes(t *testing.T) {
	f := newDataFixture(t)
	now := time.Now()
	checkPlans(t, f.s, []planCase{
		{"DeleteUserSessions", deleteUserSessions, []any{1}},
		{"DeleteExpiredSessions", deleteExpiredSessions, []any{now}},
		{"ListOrgMembers", listOrgMembers, []any{now, 1}},
		{"PruneResolvedAlerts", pruneResolvedAlerts, []any{now}},
		{"PruneGitDeliveries", pruneGitDeliveries, []any{now}},
		{"ListProjectReleaseHeads", listProjectReleaseHeads, []any{1}},
		{"DeleteRemovedVolume", deleteRemovedVolume, []any{1}},
		{"GetLatestReadySnapshot", getLatestReadySnapshot, []any{1, 1}},
		{"ListServerReleaseImages", listServerReleaseImages, []any{1}},
		{"ListUnroutedGeneratedDomains", listUnroutedGeneratedDomains, []any{1}},
		{"ListOrgCustomCertificates", listOrgCustomCertificates, []any{1}},
		{"ListOrgVerifiedDomains", listOrgVerifiedDomains, []any{1}},
	})
}

// Access requests' inbox, filing and expiry read through an index: the
// lists join their names instead of a query per project.
func TestAccessRequestPlansUseIndexes(t *testing.T) {
	f := newDataFixture(t)
	now := time.Now()
	checkPlans(t, f.s, []planCase{
		{"ListOrgAccessRequests", listOrgAccessRequests, []any{1, 50}},
		{"ListTokenAccessRequests", listTokenAccessRequests, []any{1, 50}},
		{"GetPendingAccessRequest", getPendingAccessRequest, []any{"x"}},
		{"ExpireAccessRequests", expireAccessRequests, []any{now}},
		{"EndUnlockedAccessRequests", endUnlockedAccessRequests, []any{now, "x", now, 1}},
		{"PruneAccessRequests", pruneAccessRequests, []any{now}},
		{"CountEnvironmentAuditSince", countEnvironmentAuditSince, []any{1, 1}},
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

// The trace forwarder's releases are read by ID, and the release that
// replaced each superseded one by a seek of its service's releases by
// finish time: nothing is scanned, however many releases a service has.
func TestReleaseServicesPlan(t *testing.T) {
	f := newDataFixture(t)
	rows, err := f.s.DB.Query("EXPLAIN QUERY PLAN "+listReleaseServices, 1)
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
	got := strings.Join(plan, "; ")
	if !strings.Contains(got, "SEARCH x USING INDEX releases_finished (service_id=? AND finished_at>?)") || strings.Contains(got, "SCAN") || strings.Contains(got, "TEMP B-TREE") {
		t.Errorf("ListReleaseServices: %s", got)
	}
}

// The deploy queue reads the releases in flight from their partial index,
// which holds only them, and each one's service, environment, project,
// org and git connection by key: nothing grows with the releases kept.
func TestDeployQueuePlan(t *testing.T) {
	f := newDataFixture(t)
	rows, err := f.s.DB.Query("EXPLAIN QUERY PLAN " + listDeployQueue)
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
	got := strings.Join(plan, "; ")
	if !strings.HasPrefix(got, "SCAN r USING INDEX releases_one_in_flight;") || strings.Count(got, "SCAN") != 1 || strings.Contains(got, "TEMP B-TREE") {
		t.Errorf("ListDeployQueue: %s", got)
	}
}
