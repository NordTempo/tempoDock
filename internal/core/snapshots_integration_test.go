//go:build integration

package core

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/docker"
)

// The snapshot scripts on real Docker and busybox: a copy keeps owners
// and the volume root's mode (Postgres insists on 0700) and restores in
// place over other files; a copy that doesn't fit is refused up front,
// and one that runs out of disk anyway (a sparse file is larger copied
// than measured) leaves nothing behind.
func TestIntegrationSnapshotScripts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	rt, err := docker.New(netip.MustParsePrefix("10.213.0.0/16"))
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
	prefix := fmt.Sprintf("dock-snaptest-%d", time.Now().UnixNano())
	labels := node.Labels{ControlPlane: prefix}
	var made []string
	volume := func(name string, opts map[string]string) string {
		t.Helper()
		name = prefix + "-" + name
		if _, err := cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Labels: labels.Map(), DriverOpts: opts}); err != nil {
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
	run := func(name, script string, env map[string]string, mounts ...node.Mount) node.JobResult {
		t.Helper()
		res, err := rt.RunJob(ctx, node.JobSpec{Name: prefix + "-" + name, Image: image, Labels: labels, Script: script, Env: env, Mounts: mounts})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	data, snaps := volume("data", nil), volume("snaps", nil)
	small := volume("small", map[string]string{"type": "tmpfs", "device": "tmpfs", "o": "size=4m"})
	at := func(vol, path string) node.Mount { return node.Mount{Volume: vol, Path: path} }
	const list = `cd /volumes/data && find . | sort | while read f; do stat -c '%n %u:%g %a %s' "$f"; done`

	if res := run("fill", "mkdir -p /volumes/data/base && echo hello > /volumes/data/base/f && chown -R 999:999 /volumes/data && chmod 700 /volumes/data", nil, at(data, "/volumes/data")); res.ExitCode != 0 {
		t.Fatalf("fill: %+v", res)
	}
	want := run("list", list, nil, at(data, "/volumes/data")).Output

	env := map[string]string{"SNAPSHOT": "7", "VOLUMES": "data"}
	res := run("snap", snapshotScript, env, node.Mount{Volume: data, Path: "/volumes/data", ReadOnly: true}, at(snaps, snapshotsPath))
	if res.ExitCode != 0 || !snapshotSize.MatchString(res.Output) {
		t.Fatalf("snapshot: %+v", res)
	}
	if res := run("change", "rm -rf /volumes/data/base && touch /volumes/data/new && chmod 755 /volumes/data && chown 0:0 /volumes/data", nil, at(data, "/volumes/data")); res.ExitCode != 0 {
		t.Fatalf("change: %+v", res)
	}
	if res := run("restore", snapshotRestoreScript, env, at(data, "/volumes/data"), at(snaps, snapshotsPath)); res.ExitCode != 0 {
		t.Fatalf("restore: %+v", res)
	}
	if got := run("list-again", list, nil, at(data, "/volumes/data")).Output; got != want {
		t.Fatalf("restored:\n%s\nwant:\n%s", got, want)
	}

	// 6 MB measured against a 4 MB disk: refused before copying.
	run("big", "head -c 6000000 /dev/zero > /volumes/data/big", nil, at(data, "/volumes/data"))
	res = run("no-room", snapshotScript, map[string]string{"SNAPSHOT": "8", "VOLUMES": "data"}, node.Mount{Volume: data, Path: "/volumes/data", ReadOnly: true}, at(small, snapshotsPath))
	if res.ExitCode != 3 || !noRoom.MatchString(res.Output) {
		t.Fatalf("no room: %+v", res)
	}
	// Sparse: measured as nothing, 6 MB once copied. The copy fails and
	// leaves the small disk empty.
	run("sparse", "rm /volumes/data/big && truncate -s 6000000 /volumes/data/sparse", nil, at(data, "/volumes/data"))
	res = run("full", snapshotScript, map[string]string{"SNAPSHOT": "9", "VOLUMES": "data"}, node.Mount{Volume: data, Path: "/volumes/data", ReadOnly: true}, at(small, snapshotsPath))
	if res.ExitCode == 0 {
		t.Skipf("busybox cp kept the file sparse, so the disk didn't fill: %+v", res)
	}
	if left := run("left", "ls -A "+snapshotsPath, nil, at(small, snapshotsPath)).Output; strings.TrimSpace(left) != "" {
		t.Fatalf("a failed copy left %q behind", left)
	}
}
