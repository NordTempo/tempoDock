package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Every push-to-deploy query reads through an index.
func TestGitQueryPlans(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Now()
	checkPlans(t, s, []planCase{
		{"GetGitConnectionByService", getGitConnectionByService, []any{1}},
		{"GetGitConnectionByHook", getGitConnectionByHook, []any{"h"}},
		{"UpdateGitConnection", updateGitConnection, []any{"r", "b", "", nil, false, "h", []byte{1}, 0, 0, 1}},
		{"GetAppGitConnections", getAppGitConnections, []any{42, 7}},
		{"DeleteGitConnection", deleteGitConnection, []any{1}},
		{"SetGitPending", setGitPending, []any{"s", "m", "a", "b", false, 0, 1}},
		{"ClearGitPending", clearGitPending, []any{1, "s"}},
		{"SetGitLastEvent", setGitLastEvent, []any{"e", now, 1}},
		{"ListGitPendingServices", listGitPendingServices, nil},
		{"PruneGitDeliveries", pruneGitDeliveries, []any{now}},
		{"SetReleaseCommit", setReleaseCommit, []any{"s", "m", "a", 1}},
	})
}

// Every GitHub App query reads through an index.
func TestGitHubQueryPlans(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	checkPlans(t, s, []planCase{
		{"GetGitHubApp", getGitHubApp, []any{1}},
		{"GetGitHubAppBySlug", getGitHubAppBySlug, []any{"s"}},
		{"GetGitHubAppByHook", getGitHubAppByHook, []any{"h"}},
		{"ListOrgGitHubApps", listOrgGitHubApps, []any{1}},
		{"DeleteGitHubApp", deleteGitHubApp, []any{1}},
		{"DeleteAppGitHubInstallation", deleteAppGitHubInstallation, []any{7, 1}},
		{"ListOrgGitHubInstallations", listOrgGitHubInstallations, []any{1}},
		{"GetGitHubInstallation", getGitHubInstallation, []any{7}},
		{"DeleteGitHubInstallation", deleteGitHubInstallation, []any{7}},
		{"GetOrgBySlug", getOrgBySlug, []any{"x"}},
		{"CountInstallationConnections", countInstallationConnections, nil},
	})
}
