package core

import (
	"errors"
	"slices"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// Grep and Stderr filter every source of a release's output (the live
// container, a replaced release's archive and a failed release's kept
// lines), after Tail, and send nothing when no line matches.
func TestServiceLogsFilter(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	write := func(ctr string) {
		h.rt.AddLogs(ctr, "GET /health 200")
		h.rt.AddStderr(ctr, "error: db timeout")
		h.rt.AddLogs(ctr, "GET /api error 500")
		h.rt.AddStderr(ctr, "warn: slow query")
	}
	replaced := h.deploy(web, "app:1")
	write(h.ctr(web, replaced.Number))
	live := h.deploy(web, "app:2")
	write(h.ctr(web, live.Number))
	waitOps(t, h.rt, "remove "+h.ctr(web, replaced.Number))

	release := h.rt.HoldHealthChecks()
	defer release()
	failed, err := h.c.DeployImage(h.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:3"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(web, failed.Number, apitypes.ReleaseHealthChecking)
	write(h.ctr(web, failed.Number))
	h.rt.FailHealthCheck(func(node.ContainerSpec) error { return errors.New("connection refused") })
	release()
	if failed = h.wait(web, failed.Number); failed.Status != apitypes.ReleaseFailed {
		t.Fatalf("release %d is %s", failed.Number, failed.Status)
	}

	for _, tc := range []struct {
		name string
		q    LogQuery
		want []string
	}{
		{"grep", LogQuery{Grep: "error"}, []string{"error: db timeout", "GET /api error 500"}},
		{"case-sensitive", LogQuery{Grep: "Error"}, nil},
		{"stderr", LogQuery{Stderr: true}, []string{"error: db timeout", "warn: slow query"}},
		{"both", LogQuery{Grep: "error", Stderr: true}, []string{"error: db timeout"}},
		{"after tail", LogQuery{Tail: 2, Grep: "error"}, []string{"GET /api error 500"}},
	} {
		for _, rel := range []apitypes.Release{replaced, live, failed} {
			q := tc.q
			q.Release = rel.Number
			stream, err := h.c.ServiceLogs(h.ctx, testSystem, web, q)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			sends := 0
			err = stream(h.ctx, func() {}, func(ls ...apitypes.LogLine) error {
				sends++
				for _, l := range ls {
					got = append(got, l.Text)
				}
				return nil
			})
			if err != nil || !slices.Equal(got, tc.want) || (got == nil && sends != 0) {
				t.Errorf("%s, release %d: %d sends of %q, %v; want %q", tc.name, rel.Number, sends, got, err, tc.want)
			}
		}
	}
}

// Grep sees lines as masked for the actor, so it can't probe for a
// secret value the actor may not reveal.
func TestServiceLogsGrepAfterMasking(t *testing.T) {
	h := newHarness(t)
	web := secretService(t, h)
	if got, err := h.readLogs(readerActor, web, LogQuery{Grep: leakedKey}); err != nil || got != nil {
		t.Errorf("reader's grep for the secret = %q, %v", got, err)
	}
	if got, err := h.readLogs(testSystem, web, LogQuery{Grep: leakedKey}); err != nil || len(got) != 1 {
		t.Errorf("admin's grep for the secret = %q, %v", got, err)
	}
}
