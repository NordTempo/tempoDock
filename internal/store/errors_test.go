package store

import (
	"testing"
	"time"
)

// The errors page's queries, the tick's writes and the retention pass's
// read error issues through their indexes, without sorting.
func TestErrorIssueQueryPlans(t *testing.T) {
	f := newDataFixture(t)
	now := time.Now().UTC()
	checkPlans(t, f.s, []planCase{
		{"GetErrorIssueByFingerprint", getErrorIssueByFingerprint, []any{1, "abc"}},
		{"CountServiceErrorIssues", countServiceErrorIssues, []any{1}},
		{"GetErrorTarget", getErrorTarget, []any{1}},
		{"ListOrgErrorIssues", listOrgErrorIssues, []any{1, "production", 1, 201}},
		{"ListProjectErrorIssues", listProjectErrorIssues, []any{1, "", 0, 201}},
		{"ListServiceErrorIssues", listServiceErrorIssues, []any{1, 2, 201}},
		{"ListOrgErrorCounts", listOrgErrorCounts, []any{0, 1, now}},
		{"ListErrorFacets", listErrorFacets, []any{now, 0, 1, ""}},
		{"GetErrorIssue", getErrorIssue, []any{1}},
		{"ListErrorIssueCounts", listErrorIssueCounts, []any{1, 0}},
		{"ResolveErrorIssue", resolveErrorIssue, []any{now, "a", 1}},
		{"AutoResolveErrorIssues", autoResolveErrorIssues, []any{now, now}},
		{"PruneErrorIssues", pruneErrorIssues, []any{now}},
		{"PruneErrorCounts", pruneErrorCounts, []any{0}},
		{"CountReleaseNewErrors", countReleaseNewErrors, []any{1, 3}},
		{"ListFollowContainers", listFollowContainers, []any{1}},
	})
}
