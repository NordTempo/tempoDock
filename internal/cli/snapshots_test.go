package cli

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/node"
)

// `dock volume snapshot` takes a snapshot and waits for it; restore
// --in-place and delete ask first, and an in-place restore reports its
// safety snapshot.
func TestVolumeSnapshotCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	exec(t, "project", "create", "shop")
	ref := core.ServiceRef{Project: "shop", Environment: "production", Service: "db"}
	ctx := context.Background()
	if _, err := srv.core.CreateService(ctx, sys, ref.Env(), apitypes.CreateServiceRequest{Name: "db", Template: apitypes.TemplatePostgres}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		rels, err := srv.core.ListReleases(ctx, sys, ref, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(rels) == 1 && rels[0].Status == apitypes.ReleaseActive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("db's first release: %+v", rels)
		}
	}
	// The snapshot jobs' scripts report as on a real server.
	srv.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if strings.Contains(spec.Script, "dock-snapshot: size") {
			return node.JobResult{Output: "dock-snapshot: size 64\n"}, nil
		}
		return node.JobResult{Output: "dock-snapshot: restored\n"}, nil
	})
	at := []string{"-p", "shop", "-e", "production", "-s", "db"}

	snap := ok[apitypes.Snapshot](t, append([]string{"volume", "snapshot", "--note", "before 2.0"}, at...)...)
	if snap.Status != apitypes.SnapshotReady || snap.Note != "before 2.0" || snap.SizeBytes != 64<<10 || snap.Trigger != apitypes.SnapshotManual {
		t.Fatalf("snapshot = %+v", snap)
	}
	list := ok[apitypes.ServiceSnapshots](t, append([]string{"volume", "snapshot", "ls"}, at...)...)
	if !list.IntoNew || len(list.Snapshots) != 1 || list.Snapshots[0].ID != snap.ID {
		t.Fatalf("list = %+v", list)
	}

	id := []string{strconv.FormatInt(snap.ID, 10)}
	for _, args := range [][]string{
		append(append([]string{"volume", "snapshot", "restore", "--in-place"}, id...), at...),
		append(append([]string{"volume", "snapshot", "delete"}, id...), at...),
	} {
		if code, _, stderr := exec(t, args...); code != int(output.ExitUsage) || !strings.Contains(stderr, "--yes") {
			t.Errorf("%v without --yes: exit %d %q", args, code, stderr)
		}
	}

	res := ok[apitypes.SnapshotRestoreResult](t, append(append([]string{"volume", "snapshot", "restore", "--in-place", "--yes"}, id...), at...)...)
	if r := res.Restore; !r.InPlace || r.To != "db" || r.Status != apitypes.BackupSucceeded || r.Safety == 0 || r.Safety == snap.ID {
		t.Fatalf("in-place restore = %+v", r)
	}
	ok[struct{ Deleted bool }](t, append(append([]string{"volume", "snapshot", "delete", "--yes"}, id...), at...)...)
	list = ok[apitypes.ServiceSnapshots](t, append([]string{"volume", "snapshot", "list"}, at...)...)
	if len(list.Snapshots) != 1 || list.Snapshots[0].ID != res.Restore.Safety || list.Snapshots[0].Trigger != apitypes.SnapshotBeforeRestore {
		t.Fatalf("after delete: %+v", list.Snapshots)
	}
}
