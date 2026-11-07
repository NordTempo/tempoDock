package core

import (
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

// readLogs reads a service's logs to the end, as texts.
func (h *harness) readLogs(a Actor, ref ServiceRef, q LogQuery) ([]string, error) {
	h.t.Helper()
	stream, err := h.c.ServiceLogs(h.ctx, a, ref, q)
	if err != nil {
		return nil, err
	}
	var got []string
	err = stream(h.ctx, func() {}, func(ls ...apitypes.LogLine) error {
		for _, l := range ls {
			got = append(got, l.Text)
		}
		return nil
	})
	return got, err
}

// A replaced release's container output is archived before the container
// goes, and its logs read from there; following ends after the backlog.
func TestReplacedReleaseLogsAreKept(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	first := h.ctr(web, 1)
	h.rt.AddLogs(first, "listening on :3000", "GET /", "shutting down")
	h.deploy(web, "app:2")
	waitOps(t, h.rt, "remove "+first)

	ops := h.rt.Ops()
	archived := indexOf(ops, "archive logs "+first+" max=67108864 age=720h0m0s")
	if archived < 0 || archived < indexOf(ops, "stop "+first) || archived > indexOf(ops, "remove "+first) {
		t.Fatalf("not archived between stop and remove: %v", ops)
	}
	got, err := h.readLogs(testSystem, web, LogQuery{Release: 1, Follow: true, Tail: 2})
	if err != nil || !slices.Equal(got, []string{"GET /", "shutting down"}) {
		t.Fatalf("logs of release 1 = %v, %v", got, err)
	}
	if r, _ := h.c.GetRelease(h.ctx, testSystem, web, 1); !r.LogsKept {
		t.Fatal("release 1's logs aren't marked kept")
	}
	if r, _ := h.c.GetRelease(h.ctx, testSystem, web, 2); r.LogsKept {
		t.Fatal("the active release's logs are marked kept")
	}

	// Gone from the server (limits): a clear error.
	h.rt.AgeLogArchive(strconv.FormatInt(h.serviceID(web), 10), strconv.FormatInt(h.releaseID(web, 1), 10), 2*time.Hour)
	if _, err := h.rt.PruneLogs(h.ctx, node.LogPrune{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.readLogs(testSystem, web, LogQuery{Release: 1}); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("logs of a pruned archive: %v", err)
	}
}

// Reading an archive is a read like any other: a token for another
// environment can't.
func TestArchivedLogsPermissions(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	h.rt.AddLogs(h.ctr(web, 1), "secret-ish output")
	h.deploy(web, "app:2")
	waitOps(t, h.rt, "remove "+h.ctr(web, 1))
	staging := Actor{Kind: "token", OrgID: 1, Name: "ci", TokenID: 2, Scope: apitypes.ScopeRead, ProjectID: 1, EnvironmentID: 999, ProjectName: "shop", EnvironmentName: "staging"}
	if got, err := h.readLogs(staging, web, LogQuery{Release: 1}); CodeOf(err) != apitypes.CodeForbidden || len(got) != 0 {
		t.Fatalf("token for another environment read %v, %v", got, err)
	}
	reader := Actor{Kind: "token", OrgID: 1, Name: "ro", TokenID: 1, Scope: apitypes.ScopeRead}
	if got, err := h.readLogs(reader, web, LogQuery{Release: 1}); err != nil || len(got) != 1 {
		t.Fatalf("read token: %v, %v", got, err)
	}
}

// An archive that fails says so on the release, and the container is
// removed all the same.
func TestArchiveFailureDoesNotBlockRemoval(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	h.rt.FailArchiveLogs(errors.New("disk full"))
	h.deploy(web, "app:2")
	waitOps(t, h.rt, "remove "+h.ctr(web, 1))
	r, err := h.c.GetRelease(h.ctx, testSystem, web, 1)
	if err != nil || r.LogsKept || !slices.ContainsFunc(r.Events, func(e apitypes.ReleaseEvent) bool {
		return e.Message == "Could not keep the logs of release 1: disk full"
	}) {
		t.Fatalf("release 1 = %+v, %v", r, err)
	}
	if _, err := h.readLogs(testSystem, web, LogQuery{Release: 1}); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("logs of release 1: %v", err)
	}
}

// A server too old to archive keeps a failed release's last lines on the
// control plane as before; one that can archives them where they ran, and
// they are read from there through the node link.
func TestRemoteServerLogHistory(t *testing.T) {
	p, lab := labPool(t)
	p.nodes.add(p.ids["homelab"], lab, remote.Hello{Facts: remote.Facts{Ingress: remote.IngressDirect}}) // an old node
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	set := map[string]string{"API_KEY": leakedKey}
	if _, err := p.c.SetVariables(p.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Service: "web", Set: set}); err != nil {
		t.Fatal(err)
	}
	release := lab.HoldHealthChecks()
	rel, err := p.c.DeployImage(p.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:1"})
	if err != nil {
		t.Fatal(err)
	}
	p.waitStatus(web, rel.Number, apitypes.ReleaseHealthChecking)
	lab.AddLogs(p.ctr(web, 1), "panic: no DATABASE_URL", "key "+leakedKey)
	lab.FailHealthCheck(func(node.ContainerSpec) error { return errors.New("connection refused") })
	release()
	if r := p.wait(web, 1); r.Status != apitypes.ReleaseFailed || !r.LogsKept {
		t.Fatalf("release 1 = %+v", r)
	}
	if hasOp(lab.Ops(), "archive logs") {
		t.Fatalf("an old node was asked to archive: %v", lab.Ops())
	}
	if got, err := p.readLogs(readerActor, web, LogQuery{Release: 1}); err != nil || !slices.Equal(got, []string{"panic: no DATABASE_URL", "key *****"}) {
		t.Fatalf("saved lines = %v, %v", got, err)
	}

	labID := p.ids["homelab"]
	p.nodes.add(labID, lab, remote.Hello{Protocol: remote.LogsProtocol, Facts: remote.Facts{Ingress: remote.IngressDirect}})
	lab.FailHealthCheck(nil)
	p.deploy(web, "app:2")
	lab.AddLogs(p.ctr(web, 2), "ready with "+leakedKey)
	p.deploy(web, "app:3")
	waitOps(t, lab, "remove "+p.ctr(web, 2))
	rel2, err := p.store.GetReleaseByNumber(p.ctx, store.GetReleaseByNumberParams{ServiceID: p.serviceID(web), Number: 2})
	if err != nil || rel2.LogsServerID == nil || *rel2.LogsServerID != labID {
		t.Fatalf("release 2's archive is on %v, %v", rel2.LogsServerID, err)
	}
	if got, err := p.readLogs(readerActor, web, LogQuery{Release: 2}); err != nil || !slices.Equal(got, []string{"ready with *****"}) {
		t.Fatalf("archived lines = %v, %v", got, err)
	}
	p.setOffline("homelab", true)
	if _, err := p.readLogs(testSystem, web, LogQuery{Release: 2}); CodeOf(err) != apitypes.CodeServerOffline {
		t.Fatalf("logs on an offline server: %v", err)
	}
}

// The retention pass holds each server's archives to [logs] limits and
// drops those of services that no longer exist.
func TestPruneLogs(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	h.deploy(web, "app:2")
	waitOps(t, h.rt, "remove "+h.ctr(web, 1))
	svc := strconv.FormatInt(h.serviceID(web), 10)
	h.rt.AddLogArchive("999", "1", "a service that is gone")
	h.rt.AgeLogArchive("999", "1", 2*time.Hour)
	if got := h.rt.LogArchives(); len(got) != 2 {
		t.Fatalf("archives before = %v", got)
	}
	servers, err := h.store.ListServers(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.c.pruneLogs(h.ctx, servers)
	if got := h.rt.LogArchives(); len(got) != 1 || got[0] != svc+"/"+strconv.FormatInt(h.releaseID(web, 1), 10) {
		t.Fatalf("archives after = %v", got)
	}
	ops := h.rt.Ops()
	if want := "prune logs keep=" + svc + " max=67108864 age=720h0m0s"; ops[len(ops)-1] != want {
		t.Fatalf("last op = %q, want %q", ops[len(ops)-1], want)
	}
}

// releaseID is the ID of a service's release.
func (h *harness) releaseID(ref ServiceRef, number int64) int64 {
	h.t.Helper()
	r, err := h.c.GetRelease(h.ctx, testSystem, ref, number)
	if err != nil {
		h.t.Fatal(err)
	}
	return r.ID
}

// A release's history is masked with that release's own secret values,
// even after the variable changed: an actor who can't reveal them never
// reads them back from the archive.
func TestArchivedLogsMaskSecrets(t *testing.T) {
	h := newHarness(t)
	web := secretService(t, h)
	set := map[string]string{"API_KEY": "rotated-key-42"}
	if _, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Service: "web", Set: set}); err != nil {
		t.Fatal(err)
	}
	h.deploy(web, "app:2")
	waitOps(t, h.rt, "remove "+h.ctr(web, 1))
	if got, err := h.readLogs(readerActor, web, LogQuery{Release: 1}); err != nil || !slices.Equal(got, []string{"API_KEY=***** stripe=***** mode=on"}) {
		t.Fatalf("reader sees %q, %v", got, err)
	}
	if got, err := h.readLogs(testSystem, web, LogQuery{Release: 1}); err != nil || !slices.Equal(got, []string{"API_KEY=" + leakedKey + " stripe=sk_live_org123 mode=on"}) {
		t.Fatalf("admin sees %q, %v", got, err)
	}
}
