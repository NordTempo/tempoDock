//go:build integration

package docker

import (
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// A stopped container's whole output is archived by its labels' service
// and release and read back with both streams (ADR 0046). Tail, missing
// releases and pruning are internal/node/logarchive's tests.
func TestArchiveLogs(t *testing.T) {
	e := newEnv(t)
	e.DataDir = t.TempDir()
	ctx := ctxTimeout(t, time.Minute)
	c := e.start(node.ContainerSpec{
		Name:   e.name("archive"),
		Image:  shellImage,
		Cmd:    "for i in 1 2 3 4 5; do echo out $i; done; echo oops >&2; sleep 60",
		Labels: node.Labels{ControlPlane: e.prefix, Service: "41", Release: "7"},
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		lines, err := e.Logs(ctx, c.ID, node.LogOptions{})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for range lines {
			n++
		}
		if n == 6 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("container wrote %d lines", n)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := e.Stop(ctx, c.ID, time.Second); err != nil {
		t.Fatal(err)
	}
	a, err := e.ArchiveLogs(ctx, c.ID, node.LogLimits{MaxBytes: 1 << 20, MaxAge: time.Hour})
	if err != nil || a.Service != "41" || a.Release != "7" || a.Bytes <= 0 {
		t.Fatalf("archive = %+v, %v", a, err)
	}
	if err := e.Remove(ctx, c.ID, node.RemoveOptions{}); err != nil {
		t.Fatal(err)
	}

	ch, err := e.ArchivedLogs(ctx, node.LogArchiveQuery{Service: "41", Release: "7"})
	if err != nil {
		t.Fatal(err)
	}
	var all []node.LogLine
	for l := range ch {
		all = append(all, l)
	}
	if len(all) != 6 || !containsLine(all, node.LogLine{Stream: "stderr", Text: "oops"}) || all[0].Time.IsZero() {
		t.Fatalf("archived = %+v", all)
	}
}
