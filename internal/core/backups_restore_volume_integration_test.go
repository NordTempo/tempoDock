//go:build integration

package core

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/docker"
)

// The volume backup and restore scripts on real Docker and restic (ADR
// 0095): a restore brings back the files, owners and modes of the volumes
// the backup holds and removes what was added since, leaves a volume the
// backup doesn't hold alone, and changes nothing when the service lost a
// volume the backup holds. Manual backups keep their own last
// backup.KeepManual, next to the scheduled ones.
func TestIntegrationVolumeRestoreScript(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rt, err := docker.New(networkPool(harnesses.Add(1)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	image := config.Default().Backup.ResticImage
	if ok, _ := rt.HasImage(ctx, image); !ok {
		if _, err := rt.Pull(ctx, image, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	prefix := fmt.Sprintf("dock-volrestore-%d", time.Now().UnixNano())
	labels := node.Labels{ControlPlane: prefix}
	var made []string
	volume := func(name string) string {
		t.Helper()
		name = prefix + "-" + name
		if _, err := cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Labels: labels.Map()}); err != nil {
			t.Fatal(err)
		}
		made = append(made, name)
		return name
	}
	t.Cleanup(func() {
		for _, v := range made {
			_, _ = cli.VolumeRemove(context.Background(), v, client.VolumeRemoveOptions{Force: true})
		}
	})
	bin, repoVol := volume("bin"), volume("repo")
	jobs := 0
	run := func(script string, env map[string]string, mounts ...node.Mount) node.JobResult {
		t.Helper()
		jobs++
		mounts = append(mounts, node.Mount{Volume: bin, Path: backup.BinDir}, node.Mount{Volume: repoVol, Path: backup.LocalPath})
		res, err := rt.RunJob(ctx, node.JobSpec{Name: fmt.Sprintf("%s-%d", prefix, jobs), Image: image, Labels: labels, Script: script, Env: env, Mounts: mounts})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := run(backup.InstallScript, nil); res.ExitCode != 0 {
		t.Fatalf("install restic: %+v", res)
	}
	at := func(vol, name string) node.Mount {
		return node.Mount{Volume: vol, Path: backup.VolumesPath + "/" + name}
	}
	files, data, extra := volume("files"), volume("data"), volume("extra")
	const list = `cd /volumes && find files data | sort | while read f; do stat -c '%n %u:%g %a %s' "$f"; done`

	if res := run("mkdir -p /volumes/files/up && echo hello > /volumes/files/up/a && chown -R 999:999 /volumes/files && chmod 700 /volumes/files && echo two > /volumes/data/b", nil, at(files, "files"), at(data, "data")); res.ExitCode != 0 {
		t.Fatalf("fill: %+v", res)
	}
	want := run(list, nil, at(files, "files"), at(data, "data")).Output

	repo := backup.Repo{Password: "pw"}
	src := backup.Source{Kind: apitypes.BackupVolume, ServiceID: 5}
	script, err := backup.BackupScript(apitypes.BackupVolume)
	if err != nil {
		t.Fatal(err)
	}
	take := func(manual bool) backup.Result {
		t.Helper()
		res := run(script, backup.BackupEnv(src, repo, backup.Retention{Daily: 7, Weekly: 4, Monthly: 6}, manual), at(files, "files"), at(data, "data"))
		got, err := backup.ParseBackup(res.Output)
		if res.ExitCode != 0 || err != nil {
			t.Fatalf("backup: %v %+v", err, res)
		}
		return got
	}
	first := take(false)

	if res := run("rm -rf /volumes/files/up && echo junk > /volumes/files/junk && chmod 755 /volumes/files && echo changed > /volumes/data/b && echo keep > /volumes/extra/k", nil,
		at(files, "files"), at(data, "data"), at(extra, "extra")); res.ExitCode != 0 {
		t.Fatalf("change: %+v", res)
	}
	env := backup.RestoreEnv(first.SnapshotID, backup.Source{}, repo)

	// The service lost the volume data: nothing is touched.
	res := run(backup.VolumeRestoreScript, env, at(files, "files"))
	if res.ExitCode == 0 || !strings.Contains(res.Output, "the volume data, which the service no longer has") {
		t.Fatalf("restore without a volume the backup holds: %+v", res)
	}
	if out := run("cat /volumes/files/junk", nil, at(files, "files")).Output; strings.TrimSpace(out) != "junk" {
		t.Fatalf("a refused restore changed the volume: %q", out)
	}

	res = run(backup.VolumeRestoreScript, env, at(files, "files"), at(data, "data"), at(extra, "extra"))
	if res.ExitCode != 0 || !strings.Contains(res.Output, "the volume extra isn't in the backup") {
		t.Fatalf("restore: %+v", res)
	}
	if got := run(list, nil, at(files, "files"), at(data, "data")).Output; got != want {
		t.Fatalf("restored:\n%s\nwant:\n%s", got, want)
	}
	if out := run("cat /volumes/extra/k; cat /volumes/data/b", nil, at(data, "data"), at(extra, "extra")).Output; out != "keep\ntwo\n" {
		t.Fatalf("after the restore: %q", out)
	}

	// Manual backups keep their own last KeepManual; the scheduled one of
	// the same day stays next to them.
	var last backup.Result
	for range backup.KeepManual + 1 {
		last = take(true)
	}
	if len(last.Kept) != backup.KeepManual+1 {
		t.Fatalf("kept %d snapshots, want the scheduled one and %d manual: %v", len(last.Kept), backup.KeepManual, last.Kept)
	}
	found := false
	for _, id := range last.Kept {
		found = found || id == first.SnapshotID
	}
	if !found {
		t.Errorf("the scheduled backup %s was removed by manual retention: %v", first.SnapshotID, last.Kept)
	}
}
