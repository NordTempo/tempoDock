package store

import "testing"

// The log drain and log history queries (ADR 0046) read through indexes.
// ListAllLogDrains and ListServiceIDs read whole tables on purpose (at
// start and in the hourly retention pass), in primary key order.
func TestLogQueryPlans(t *testing.T) {
	f := newDataFixture(t)
	checkPlans(t, f.s, []planCase{
		{"ListLogDrains", listLogDrains, []any{1}},
		{"GetLogDrain", getLogDrain, []any{1, 1}},
		{"DeleteLogDrain", deleteLogDrain, []any{1}},
		{"ListDrainContainers", listDrainContainers, []any{1}},
		{"GetLogSource", getLogSource, []any{1}},
		{"SetReleaseLogsServer", setReleaseLogsServer, []any{1, 1}},
	})
}
