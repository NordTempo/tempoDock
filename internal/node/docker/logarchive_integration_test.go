//go:build integration

package docker

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// A stopped container's whole output is archived by its labels' service
// and release, read back with tail and both streams, and pruned once its
// service is gone (ADR 0046).
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
	if err := e.Remove(ctx, c.ID); err != nil {
		t.Fatal(err)
	}

	read := func(q node.LogArchiveQuery) []node.LogLine {
		t.Helper()
		ch, err := e.ArchivedLogs(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []node.LogLine
		for l := range ch {
			out = append(out, l)
		}
		return out
	}
	all := read(node.LogArchiveQuery{Service: "41", Release: "7"})
	if len(all) != 6 || !containsLine(all, node.LogLine{Stream: "stderr", Text: "oops"}) || all[0].Time.IsZero() {
		t.Fatalf("archived = %+v", all)
	}
	var texts []string
	for _, l := range read(node.LogArchiveQuery{Service: "41", Release: "7", Tail: 2}) {
		texts = append(texts, l.Text)
	}
	if want := []string{all[4].Text, all[5].Text}; !slices.Equal(texts, want) {
		t.Fatalf("tail 2 = %v, want %v", texts, want)
	}
	if _, err := e.ArchivedLogs(ctx, node.LogArchiveQuery{Service: "41", Release: "8"}); !errors.Is(err, node.ErrNotFound) {
		t.Fatalf("another release: %v", err)
	}
	if r, err := e.PruneLogs(context.Background(), node.LogPrune{KeepServices: []string{"41"}, Limits: node.LogLimits{MaxAge: time.Hour}}); err != nil || r.Removed != 0 {
		t.Fatalf("prune of a kept service = %+v, %v", r, err)
	}
}
