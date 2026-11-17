package store

import (
	"strings"
	"testing"
)

// What uptime checks read and write goes through an index: the check
// leads uptime_stats' key, rollups and trims go by time, incidents by
// alert key. Grouping sorts a bounded set (a service's checks, an hour's
// minute rows), so only scans of growing tables and sorts for ORDER BY
// fail. The scheduler's list (ListUptimeSchedule) reads every check on
// purpose, and its protected domains (ListProtectedDomains) every protected
// service, each one's domains through the index.
func TestUptimeQueryPlans(t *testing.T) {
	f := newDataFixture(t)
	for _, tc := range []struct {
		name, sql string
		args      []any
		want      string
	}{
		{"GetUptimeCheck", getUptimeCheck, []any{1}, "uptime_checks USING INTEGER PRIMARY KEY"},
		{"ListServiceUptimeChecks", listServiceUptimeChecks, []any{1}, "uptime_checks_service"},
		{"RollupUptimeHour", rollupUptimeHour, []any{3600, 7200}, "uptime_stats_time (res=? AND at>? AND at<?)"},
		{"PruneUptimeStats", pruneUptimeStats, []any{60, 3600}, "uptime_stats_time (res=? AND at<?)"},
		{"UptimeTotals", uptimeTotals, []any{1, 2, 3, 1, 0}, "s USING PRIMARY KEY (check_id=?)"},
		{"UptimeSeries", uptimeSeries, []any{1, 0}, "s USING PRIMARY KEY (check_id=? AND res=? AND at>?)"},
		{"UptimeProjectSummary", uptimeProjectSummary, []any{0, 1}, "s USING PRIMARY KEY (check_id=? AND res=? AND at>?)"},
		{"ListProtectedDomains", listProtectedDomains, nil, "domains USING INDEX domains_service (service_id=?)"},
		{"ListUptimeIncidents", listUptimeIncidents, []any{1, 5}, "alerts USING INDEX alerts_key (key=?)"},
	} {
		rows, err := f.s.DB.Query("EXPLAIN QUERY PLAN "+tc.sql, tc.args...)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		_ = rows.Close()
		all := strings.Join(plan, "; ")
		t.Logf("%s: %s", tc.name, all)
		if !strings.Contains(all, tc.want) || strings.Contains(all, "SCAN uptime_stats") || strings.Contains(all, "SCAN m") ||
			strings.Contains(all+";", "SCAN s;") || strings.Contains(all, "SCAN s ") || strings.Contains(all, "SCAN alerts") || strings.Contains(all, "FOR ORDER BY") {
			t.Errorf("%s: %s, want %s", tc.name, all, tc.want)
		}
	}
}
