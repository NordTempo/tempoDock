package core

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/fake"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/s3"
	"github.com/NordTempo/tempoDock/internal/store"
)

// fakeRegistry records what core asks of the platform registry. Tokens
// name their access and repository, so tests can see which was used.
type fakeRegistry struct {
	mu      sync.Mutex
	ops     []string
	pruned  int     // what Prune reports deleting
	skipped []error // and skipping
	// migrateErr fails Migrate before it commits; gate, if set, holds
	// Migrate until it is closed.
	migrateErr error
	gate       chan struct{}
	// gcHangs makes GarbageCollect run until its context ends, like a
	// collection over a large bucket.
	gcHangs bool
}

func (r *fakeRegistry) op(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ops = append(r.ops, fmt.Sprintf(format, args...))
}

func (r *fakeRegistry) Ops() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.ops)
}

func (r *fakeRegistry) Host() string { return "127.0.0.1:5000" }

func (r *fakeRegistry) Token(subject, repo string, push bool) string {
	if push {
		return "push:" + repo
	}
	return "pull:" + repo
}

func (r *fakeRegistry) Digest(_ context.Context, repo, tag string) (string, error) {
	r.op("digest %s:%s", repo, tag)
	return digestOf(repo + ":" + tag), nil
}

func (r *fakeRegistry) Prune(_ context.Context, keep []string) (int, []error, error) {
	r.op("prune keep=%s", strings.Join(keep, ","))
	return r.pruned, r.skipped, nil
}

func (r *fakeRegistry) GarbageCollect(ctx context.Context) error {
	r.op("gc")
	if r.gcHangs {
		<-ctx.Done()
		r.op("gc stopped")
		return ctx.Err()
	}
	return nil
}

func (r *fakeRegistry) Stored(context.Context) (int64, error) { return 1 << 30, nil }

func (r *fakeRegistry) UseStorage(_ context.Context, b *s3.Bucket) error {
	r.op("use %s", bucketLabel(b))
	return nil
}

func (r *fakeRegistry) Migrate(_ context.Context, to *s3.Bucket, _ string, commit func() error) (int, int64, error) {
	r.op("migrate to %s", bucketLabel(to))
	if r.gate != nil {
		<-r.gate
	}
	if r.migrateErr != nil {
		return 0, 0, r.migrateErr
	}
	if err := commit(); err != nil {
		return 0, 0, err
	}
	return 3, 300, nil
}

func (r *fakeRegistry) PushStarted() func() { return func() {} }

func (r *fakeRegistry) Bucket() *apitypes.RegistryBucket { return nil }

func (r *fakeRegistry) Drop(_ context.Context, b *s3.Bucket) error {
	r.op("drop %s", bucketLabel(b))
	return nil
}

// bucketLabel names a storage in fakeRegistry's ops: local or the bucket
// with its access key.
func bucketLabel(b *s3.Bucket) string {
	if b == nil {
		return "local"
	}
	return b.Name + ":" + b.AccessKeyID
}

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// labPool is a control plane "cp" taking production and a remote
// "homelab" taking everything else, with project shop: production on cp,
// staging on homelab.
func labPool(t *testing.T) (*pool, *fake.Runtime) {
	t.Helper()
	p := newPool(t, poolServer{"cp", true, false, 8}, poolServer{"homelab", false, true, 32})
	p.project("shop")
	p.mustEnv("shop", "staging", "", "homelab")
	return p, p.rts[p.ids["homelab"]]
}

// withRegistry restarts p with a registry and homelab as the only
// builder.
func (p *pool) withRegistry(reg Registry) {
	p.t.Helper()
	lab := p.ids["homelab"]
	if _, err := p.store.UpdateServerSettings(p.ctx, store.UpdateServerSettingsParams{Builder: true, BuildsNonProduction: true, ID: lab}); err != nil {
		p.t.Fatal(err)
	}
	p.nodes.add(lab, p.rts[lab], remote.Hello{Facts: remote.Facts{Ingress: remote.IngressDirect, Builder: true}})
	p.opts.Registry = reg
	p.opts.Config.BuildKit.Enabled = false // cp doesn't build
	p.restart()
}

func (p *pool) createService(env, name string, req apitypes.CreateServiceRequest) ServiceRef {
	p.t.Helper()
	req.Name = name
	if _, err := p.c.CreateService(p.ctx, testSystem, EnvRef{"shop", env}, req); err != nil {
		p.t.Fatal(err)
	}
	return ServiceRef{"shop", env, name}
}

func (p *pool) deploySource(ref ServiceRef) apitypes.Release {
	p.t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: 11, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("FROM app:1\n"))
	_ = tw.Close()
	rel, err := p.c.DeploySource(p.ctx, testSystem, ref, &buf, 1<<20)
	if err != nil {
		p.t.Fatal(err)
	}
	return p.wait(ref, rel.Number)
}

// waitOps waits until rt has seen an op starting with prefix.
func waitOps(t *testing.T, rt *fake.Runtime, prefix string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.ContainsFunc(rt.Ops(), func(op string) bool { return strings.HasPrefix(op, prefix) }) {
		if time.Now().After(deadline) {
			t.Fatalf("no op %q in %v", prefix, rt.Ops())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func hasOp(ops []string, prefix string) bool {
	return slices.ContainsFunc(ops, func(op string) bool { return strings.HasPrefix(op, prefix) })
}

// An environment on a remote server deploys through that server's
// runtime, and its routes go to that server's proxy only. While the
// server is offline, deploys are refused before a release exists.
func TestRemoteDeployAndOffline(t *testing.T) {
	p, lab := labPool(t)
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{Port: 3000})
	if _, err := p.c.AddDomain(p.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "web.staging.test"}); err != nil {
		t.Fatal(err)
	}
	if r := p.deploy(web, "app:1"); r.Status != apitypes.ReleaseActive {
		t.Fatalf("release 1: %s (%s)", r.Status, r.Error)
	}
	if !hasOp(lab.Ops(), "pull app:1") || hasOp(p.rt.Ops(), "pull app:1") {
		t.Fatalf("pulled on the wrong server: homelab %v, cp %v", lab.Ops(), p.rt.Ops())
	}
	if routes := lab.Routes(); len(routes) != 1 || routes[0].Hostnames[0] != "web.staging.test" {
		t.Fatalf("homelab routes: %+v", routes)
	}
	if routes := p.rt.Routes(); len(routes) != 0 {
		t.Fatalf("cp got another server's routes: %+v", routes)
	}

	p.setOffline("homelab", true)
	_, err := p.c.DeployImage(p.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:2"})
	if CodeOf(err) != apitypes.CodeServerOffline || err.Error() != "the server is offline; try again once it is back" {
		t.Fatalf("deploy to an offline server: %v", err)
	}
	rels, err := p.c.ListReleases(p.ctx, testSystem, web, 10)
	if err != nil || len(rels) != 1 {
		t.Fatalf("releases after a refused deploy: %d, %v", len(rels), err)
	}
	srv, err := p.c.GetServer(p.ctx, testSystem, "homelab")
	if err != nil || srv.Server.Status != apitypes.ServerOffline {
		t.Fatalf("homelab: %+v, %v", srv.Server, err)
	}
}

// While a service's server can't be reached, reads keep its last known
// state and say which server is offline or revoked.
func TestStatusOnOfflineServer(t *testing.T) {
	p, _ := labPool(t)
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	p.deploy(web, "app:1")
	status := func() apitypes.ServiceStatus {
		t.Helper()
		svc, err := p.c.GetService(p.ctx, testSystem, web)
		if err != nil {
			t.Fatal(err)
		}
		o, err := p.c.Overview(p.ctx, testSystem, "shop")
		if err != nil {
			t.Fatal(err)
		}
		// The deploy's last status update (the cutover's, then its start
		// event's) may land between the two reads, so only the times may
		// differ. TestStatusStartedAtIsTheContainers checks the start.
		s := svc.Status
		s.UpdatedAt, s.StartedAt = nil, nil
		for _, e := range o.Environments {
			for _, so := range e.Services {
				os := so.Service.Status
				os.UpdatedAt, os.StartedAt = nil, nil
				if so.Service.ID == svc.ID && os != s {
					t.Fatalf("overview status %+v, service status %+v", so.Service.Status, svc.Status)
				}
			}
		}
		return s
	}
	if s := status(); s != (apitypes.ServiceStatus{State: apitypes.ServiceRunning}) {
		t.Fatalf("online: %+v", s)
	}

	p.setOffline("homelab", true)
	want := apitypes.ServiceStatus{State: apitypes.ServiceRunning, ServerOffline: true, Server: "homelab", ServerStatus: apitypes.ServerOffline}
	if s := status(); s != want {
		t.Fatalf("offline: %+v", s)
	}
	if _, err := p.c.RevokeServer(p.ctx, testSystem, "homelab"); err != nil {
		t.Fatal(err)
	}
	want.ServerStatus = apitypes.ServerRevoked
	if s := status(); s != want {
		t.Fatalf("revoked: %+v", s)
	}
}

// Archiving on a server that can't be reached succeeds and says what was
// left: an offline server's containers stop when it reconnects, a revoked
// one's are left running.
func TestArchiveOnUnreachableServer(t *testing.T) {
	p, lab := labPool(t)
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	api := p.createService("staging", "api", apitypes.CreateServiceRequest{})
	p.deploy(web, "app:1")
	p.deploy(api, "app:1")
	ss, err := p.c.loadService(p.ctx, 1, web)
	if err != nil {
		t.Fatal(err)
	}
	webCtr := containerName(ss, 1)

	p.setOffline("homelab", true)
	svc, err := p.c.ArchiveService(p.ctx, testSystem, web)
	if err != nil || svc.ArchivedAt == nil {
		t.Fatalf("archive: %+v, %v", svc, err)
	}
	if want := []string{"the server is offline: its containers (1) are stopped when it reconnects"}; !slices.Equal(svc.ArchiveNotes, want) {
		t.Fatalf("notes = %q", svc.ArchiveNotes)
	}
	if !slices.Contains(lab.Running(), webCtr) {
		t.Fatalf("running on homelab = %v", lab.Running())
	}
	p.setOffline("homelab", false)
	p.c.NodeStatus(remote.State{ServerID: p.ids["homelab"], Server: "homelab", Status: remote.StatusOnline,
		Hello: remote.Hello{Facts: remote.Facts{Ingress: remote.IngressDirect}}})
	waitOps(t, lab, "stop "+webCtr)

	if _, err := p.c.RevokeServer(p.ctx, testSystem, "homelab"); err != nil {
		t.Fatal(err)
	}
	proj, err := p.c.ArchiveProject(p.ctx, testSystem, "shop")
	if err != nil || proj.ArchivedAt == nil {
		t.Fatalf("archive project: %+v, %v", proj, err)
	}
	stagingNet := networkName(ss.envScope)
	if len(proj.ArchiveNotes) != 2 || !strings.HasPrefix(proj.ArchiveNotes[0], "containers on a revoked server were left running (1)") ||
		proj.ArchiveNotes[1] != "the server is revoked: its network "+stagingNet+" stayed; a platform operator can remove it on that machine with `docker network rm "+stagingNet+"`" {
		t.Fatalf("notes = %q", proj.ArchiveNotes)
	}
	if !slices.Contains(lab.Networks(), stagingNet) {
		t.Fatalf("network removed on a revoked server: %v", lab.Networks())
	}
}

// Archived services keep no release a rollback or restore could need, so
// their images leave the registry's and the servers' keep sets, and the
// archive starts a registry prune and a prune of the server's images
// that names them: a test project can be removed for good.
func TestArchiveReleasesImages(t *testing.T) {
	p, _ := labPool(t)
	reg := &fakeRegistry{}
	p.withRegistry(reg)
	p.buildsProduction("homelab")
	web := p.createService("production", "web", apitypes.CreateServiceRequest{})
	api := p.createService("production", "api", apitypes.CreateServiceRequest{})
	webRel, apiRel := p.deploySource(web), p.deploySource(api)
	server, err := p.c.store.GetServer(p.ctx, p.ids["cp"])
	if err != nil {
		t.Fatal(err)
	}
	kept := func() (digests, ids []string) {
		t.Helper()
		digests, err := p.c.store.ListKeptDigests(p.ctx, 5)
		if err != nil {
			t.Fatal(err)
		}
		req, err := p.c.imagePrune(p.ctx, server)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(digests)
		return digests, req.Keep
	}
	if digests, ids := kept(); len(digests) != 2 || len(ids) != 2 {
		t.Fatalf("before the archive: digests %v, image IDs %v", digests, ids)
	}

	p.c.images.mu.Lock()
	p.c.images.lastPrune = time.Time{} // the deploys' prune was just now
	p.c.images.mu.Unlock()
	if _, err := p.c.ArchiveService(p.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	digests, ids := kept()
	if !slices.Equal(digests, []string{apiRel.ImageDigest}) || len(ids) != 1 {
		t.Fatalf("after archiving web: digests %v (web %s, api %s), image IDs %v", digests, webRel.ImageDigest, apiRel.ImageDigest, ids)
	}
	waitOps(t, p.rt, "prune images keep="+apiRel.ImageID+" repos="+p.c.reg.Host()+"/dock/p unneeded="+webRel.ImageID)
	// The archive prunes the registry, keeping only api's image. A deploy's
	// own background prune can take the hourly slot first; then prune again
	// (anything from now on keeps only api).
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(reg.Ops(), "prune keep="+apiRel.ImageDigest) {
		if time.Now().After(deadline) {
			t.Fatalf("registry ops: %v", reg.Ops())
		}
		time.Sleep(20 * time.Millisecond)
		p.c.images.mu.Lock()
		if !p.c.images.pruning {
			p.c.images.lastPrune = time.Time{}
		}
		p.c.images.mu.Unlock()
		p.c.maybePruneRegistry()
	}
}

// The console's registry view lists each service's kept images from the
// releases (none once archived), and a manual prune runs whenever asked,
// for unlimited admins only, and records how it went.
func TestRegistryInfoAndManualPrune(t *testing.T) {
	p, _ := labPool(t)
	reg := &fakeRegistry{pruned: 3}
	p.withRegistry(reg)
	p.buildsProduction("homelab")
	web := p.createService("production", "web", apitypes.CreateServiceRequest{})
	api := p.createService("production", "api", apitypes.CreateServiceRequest{})
	p.deploySource(web)
	second := p.deploySource(web)
	p.deploySource(api)
	if _, err := p.c.ArchiveService(p.ctx, testSystem, api); err != nil {
		t.Fatal(err)
	}
	idle := func() {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			p.c.images.mu.Lock()
			pruning := p.c.images.pruning
			p.c.images.mu.Unlock()
			if !pruning {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("prune still running")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	idle() // the deploys' and the archive's own prune

	info, err := p.c.GetRegistry(p.ctx, testSystem)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Enabled || info.StoredBytes != 1<<30 || len(info.Services) != 2 {
		t.Fatalf("registry = %+v", info)
	}
	a, w := info.Services[0], info.Services[1] // by name: api, web
	if !a.Archived || a.Kept != 0 || w.Archived || w.Kept != 2 || w.Newest != second.Number || w.Active != second.Number ||
		!strings.HasPrefix(w.Repository, buildTagPrefix) {
		t.Fatalf("services = %+v", info.Services)
	}

	limited := Actor{Kind: "token", Name: "ops", Scope: apitypes.ScopeAdmin, ProjectID: 1, ProjectName: "shop"}
	for _, a := range []Actor{limited, {Kind: "token", Name: "ci", Scope: apitypes.ScopeDeploy}} {
		if _, err := p.c.PruneRegistry(p.ctx, a); CodeOf(err) != apitypes.CodeForbidden {
			t.Fatalf("%s: err = %v", a, err)
		}
	}
	if info, err = p.c.PruneRegistry(p.ctx, testSystem); err != nil || !info.Pruning {
		t.Fatalf("prune: %+v, %v", info, err)
	}
	idle()
	if info, err = p.c.GetRegistry(p.ctx, testSystem); err != nil || info.LastPruneAt == nil || info.LastPruned != 3 || info.CollectPending {
		t.Fatalf("after the prune: %+v, %v", info, err)
	}
	if ops := reg.Ops(); ops[len(ops)-1] != "gc" {
		t.Fatalf("registry ops: %v", ops)
	}

	// A prune that passed over an image it couldn't read still ran
	// through: the console and an alert name what it skipped, until a
	// prune gets through everything.
	reg.skipped = []error{errors.New("read dock/p8_e34_s37:1: timed out")}
	if _, err := p.c.PruneRegistry(p.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	idle()
	info, err = p.c.GetRegistry(p.ctx, testSystem)
	if err != nil || info.LastPruneError != "" || len(info.LastPruneSkipped) != 1 || !strings.Contains(info.LastPruneSkipped[0], "dock/p8_e34_s37:1") {
		t.Fatalf("after a prune that skipped: %+v, %v", info, err)
	}
	if a := p.openAlert("registry:prune", true); !strings.Contains(a.Message, "dock/p8_e34_s37:1") {
		t.Fatalf("alert = %+v", a)
	}
	reg.skipped = nil
	if _, err := p.c.PruneRegistry(p.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	idle()
	p.openAlert("registry:prune", false)
	if info, err = p.c.GetRegistry(p.ctx, testSystem); err != nil || len(info.LastPruneSkipped) != 0 {
		t.Fatalf("after a clean prune: %+v, %v", info, err)
	}
}

// When a server reconnects, its leftover containers of retired releases
// are removed, statuses are read and routes applied again; afterwards its
// container events reach service statuses.
func TestReconnectReconcilesServer(t *testing.T) {
	p, lab := labPool(t)
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{Port: 3000})
	r1 := p.deploy(web, "app:1")
	p.deploy(web, "app:2")
	ss, err := p.c.serviceFor(p.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}

	p.setOffline("homelab", true)
	p.c.NodeStatus(remote.State{ServerID: p.ids["homelab"], Server: "homelab", Status: remote.StatusOffline})
	lab.AddContainer(node.Container{ID: "leftover", Name: "dock_leftover", Labels: p.c.labels(ss, r1.ID), State: node.StateRunning})
	p.setOffline("homelab", false)
	p.c.NodeStatus(remote.State{ServerID: p.ids["homelab"], Server: "homelab", Status: remote.StatusOnline,
		Hello: remote.Hello{Version: "v9", Facts: remote.Facts{Ingress: remote.IngressDirect, DockerVersion: "28.0", MemoryTotal: 32 << 30}}})
	waitOps(t, lab, "stop dock_leftover")
	waitOps(t, lab, "routes ")

	srv, err := p.c.GetServer(p.ctx, testSystem, "homelab")
	if err != nil || srv.Server.Status != apitypes.ServerOnline || srv.Server.Version != "v9" || srv.Server.DockerVersion != "28.0" {
		t.Fatalf("homelab after hello: %+v, %v", srv.Server, err)
	}

	_, id := p.activeContainer(web)
	lab.Emit(node.ContainerEvent{ContainerID: id, Action: "die", ExitCode: 137})
	deadline := time.Now().Add(5 * time.Second)
	for p.c.getStatus(ss.svc.ID).State != apitypes.ServiceCrashed {
		if time.Now().After(deadline) {
			t.Fatalf("status after a remote die event: %+v", p.c.getStatus(ss.svc.ID))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Source builds run on a builder. With the registry, the image is pushed
// with a push token, its digest recorded, and the server that runs it
// pulls it by digest with a pull token. Images out of the rollback window
// are removed afterwards, and the registry pruned.
func TestBuildOnBuilderPushesAndPulls(t *testing.T) {
	p, lab := labPool(t)
	reg := &fakeRegistry{pruned: 1}
	p.withRegistry(reg)
	p.buildsProduction("homelab")
	// A rollback window of one: the second deploy removes release 1's
	// images. Set before deploying, since deploys read it as they finish.
	p.c.cfg.Defaults.KeepReleases = 1
	web := ServiceRef{"shop", "production", "web"}
	p.createService("production", "web", apitypes.CreateServiceRequest{})

	r := p.deploySource(web)
	if r.Status != apitypes.ReleaseActive {
		t.Fatalf("release 1: %s (%s)", r.Status, r.Error)
	}
	ss, err := p.c.serviceFor(p.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	tag := buildTag(ss, 1)
	repo := strings.Split(tag, ":")[0]
	digest := digestOf(tag)
	if !hasOp(lab.Ops(), "build "+tag) || !hasOp(lab.Ops(), "push 127.0.0.1:5000/"+tag+" token=push:"+repo) {
		t.Fatalf("homelab ops: %v", lab.Ops())
	}
	if want := "pull 127.0.0.1:5000/" + repo + "@" + digest + " token=pull:" + repo; !hasOp(p.rt.Ops(), want) || hasOp(p.rt.Ops(), "build") {
		t.Fatalf("cp ops %v, want %q and no build", p.rt.Ops(), want)
	}
	if r.ImageDigest != digest {
		t.Fatalf("release digest %q, want %q", r.ImageDigest, digest)
	}

	// The next deploy removes release 1's images: the pulled one on cp,
	// the built one on homelab.
	p.deploySource(web)
	waitOps(t, p.rt, "rmi 127.0.0.1:5000/"+repo+"@"+digest)
	waitOps(t, lab, "rmi "+tag)

	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(reg.Ops(), "gc") {
		if time.Now().After(deadline) {
			t.Fatalf("registry ops: %v", reg.Ops())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Without an online builder, a source deploy is refused up front.
func TestBuildNeedsOnlineBuilder(t *testing.T) {
	p, _ := labPool(t)
	p.withRegistry(&fakeRegistry{})
	p.buildsProduction("homelab")
	web := p.createService("production", "web", apitypes.CreateServiceRequest{})
	p.setOffline("homelab", true)
	_, err := p.c.DeploySource(p.ctx, testSystem, web, strings.NewReader("x"), 1<<20)
	if CodeOf(err) != apitypes.CodeServerOffline || !strings.Contains(err.Error(), "builders: homelab") {
		t.Fatalf("deploy without a builder: %v", err)
	}
}

// buildsProduction lets a builder build production's images too, though
// it hosts no production environments.
func (p *pool) buildsProduction(name string) {
	p.t.Helper()
	on := true
	if _, err := p.c.UpdateServer(p.ctx, testSystem, name, apitypes.UpdateServerRequest{BuildsProduction: &on}); err != nil {
		p.t.Fatal(err)
	}
}

// A production build never goes to a builder that doesn't build
// production: the build gets the environment's secrets. Staging still
// builds there.
func TestProductionBuildsOnlyWhereAllowed(t *testing.T) {
	p, lab := labPool(t)
	p.withRegistry(&fakeRegistry{})
	web := p.createService("production", "web", apitypes.CreateServiceRequest{})
	_, err := p.c.DeploySource(p.ctx, testSystem, web, strings.NewReader("x"), 1<<20)
	if CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "production builds only run on servers that build production") {
		t.Fatalf("production build on a non-production builder: %v", err)
	}
	if hasOp(lab.Ops(), "build") {
		t.Fatalf("homelab built: %v", lab.Ops())
	}
	if r := p.deploySource(p.createService("staging", "web", apitypes.CreateServiceRequest{})); r.Status != apitypes.ReleaseActive {
		t.Fatalf("staging build: %s (%s)", r.Status, r.Error)
	}
}

// The control plane builds production's images only while no other
// builder for production is online, even when it hosts them.
func TestControlPlaneBuildsLast(t *testing.T) {
	p, lab := labPool(t)
	p.withRegistry(&fakeRegistry{})
	p.opts.Config.BuildKit.Enabled = true // cp builds too
	p.restart()
	p.buildsProduction("homelab")
	web := p.createService("production", "web", apitypes.CreateServiceRequest{})

	if r := p.deploySource(web); r.Status != apitypes.ReleaseActive {
		t.Fatalf("release 1: %s (%s)", r.Status, r.Error)
	}
	if !hasOp(lab.Ops(), "build") || hasOp(p.rt.Ops(), "build") {
		t.Fatalf("homelab ops %v, cp ops %v: want only homelab to build", lab.Ops(), p.rt.Ops())
	}
	p.setOffline("homelab", true)
	if r := p.deploySource(web); r.Status != apitypes.ReleaseActive {
		t.Fatalf("release 2 with homelab offline: %s (%s)", r.Status, r.Error)
	}
	if !hasOp(p.rt.Ops(), "build") {
		t.Fatalf("cp ops %v: want a build while homelab is offline", p.rt.Ops())
	}
}

// A revoked server gets no calls even while its link looks alive (a node
// that finished hello just as it was revoked): deploys are refused.
func TestRevokedServerGetsNoCalls(t *testing.T) {
	p, lab := labPool(t)
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	if _, err := p.c.RevokeServer(p.ctx, testSystem, "homelab"); err != nil {
		t.Fatal(err)
	}
	p.setOffline("homelab", false) // the hub still has a link
	_, err := p.c.DeployImage(p.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:1"})
	if CodeOf(err) != apitypes.CodeServerOffline || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("deploy to a revoked server: %v", err)
	}
	p.restart() // and after a restart
	if _, err := p.c.DeployImage(p.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:1"}); CodeOf(err) != apitypes.CodeServerOffline {
		t.Fatalf("deploy to a revoked server after restart: %v", err)
	}
	if hasOp(lab.Ops(), "pull") || hasOp(lab.Ops(), "start") {
		t.Fatalf("revoked server got calls: %v", lab.Ops())
	}
}

// A node rotating its key can't claim a key another server already has.
func TestRotationRejectsTakenKeys(t *testing.T) {
	h := newHarness(t)
	key := func(b string) string { return "sha256:" + strings.Repeat(b, 32) }
	servers := map[string]int64{}
	for name, k := range map[string]string{"a": key("aa"), "b": key("bb")} {
		srv, err := h.store.RegisterNodeServer(h.ctx, store.RegisterNodeServerParams{Name: name, KeyFingerprint: k})
		if err != nil {
			t.Fatal(err)
		}
		servers[name] = srv.ID
	}
	ns := h.c.NodeStore()
	for _, taken := range []string{key("bb"), key("aa")} { // another server's key, its own
		if err := ns.SetNextKey(h.ctx, servers["a"], taken); err == nil || !strings.Contains(err.Error(), "already belongs to server") {
			t.Fatalf("rotate a to %s: %v", taken, err)
		}
	}
	if err := ns.SetNextKey(h.ctx, servers["a"], key("cc")); err != nil {
		t.Fatal(err)
	}
	if err := ns.SetNextKey(h.ctx, servers["b"], key("cc")); err == nil {
		t.Fatal("b took a's next key")
	}
	if k, ok, err := ns.NodeKey(h.ctx, key("bb")); err != nil || !ok || k.ID != servers["b"] {
		t.Fatalf("b's key = %+v, %v, %v", k, ok, err)
	}
}

// Promote carries staging's images to production by digest (or a CI
// image as it is) with production's own variables, never rebuilding, and
// lists what it skipped.
func TestPromote(t *testing.T) {
	p, lab := labPool(t)
	p.withRegistry(&fakeRegistry{})
	for _, name := range []string{"web", "worker", "jobs", "cron"} {
		p.createService("staging", name, apitypes.CreateServiceRequest{})
	}
	for _, name := range []string{"web", "worker", "jobs"} {
		p.createService("production", name, apitypes.CreateServiceRequest{})
	}
	p.set(EnvRef{"shop", "production"}, "web", map[string]string{"MODE": "prod"}, true)
	staged := p.deploySource(ServiceRef{"shop", "staging", "web"})
	p.deploy(ServiceRef{"shop", "staging", "worker"}, "ghcr.io/acme/worker:1")
	p.deploy(ServiceRef{"shop", "staging", "cron"}, "ghcr.io/acme/cron:1")
	builds := len(slices.DeleteFunc(lab.Ops(), func(op string) bool { return !strings.HasPrefix(op, "build ") }))

	res, err := p.c.Promote(p.ctx, testSystem, "shop", apitypes.PromoteRequest{From: "staging", To: "production"})
	if err != nil {
		t.Fatal(err)
	}
	var promoted, skipped []string
	for _, s := range res.Promoted {
		promoted = append(promoted, s.Service)
	}
	for _, s := range res.Skipped {
		skipped = append(skipped, s.Service+":"+string(s.Reason))
	}
	if !slices.Equal(promoted, []string{"web", "worker"}) || !slices.Equal(skipped, []string{"cron:missing_in_target", "jobs:no_active_release"}) {
		t.Fatalf("promoted %v, skipped %v", promoted, skipped)
	}
	for _, s := range res.Promoted {
		r := p.wait(ServiceRef{"shop", "production", s.Service}, s.Release.Number)
		if r.Status != apitypes.ReleaseActive || r.Source != apitypes.SourcePromote {
			t.Fatalf("%s: %s %s (%s)", s.Service, r.Source, r.Status, r.Error)
		}
	}
	web := res.Promoted[0].Release
	if web.ImageDigest != staged.ImageDigest || !strings.HasSuffix(web.ImageRef, "@"+staged.ImageDigest) {
		t.Fatalf("promoted web image %s (%s), staging's digest %s", web.ImageRef, web.ImageDigest, staged.ImageDigest)
	}
	if !hasOp(p.rt.Ops(), "pull "+web.ImageRef+" token=pull:") || !hasOp(p.rt.Ops(), "pull ghcr.io/acme/worker:1") {
		t.Fatalf("cp ops: %v", p.rt.Ops())
	}
	spec, ok := p.rt.Spec(p.ctr(ServiceRef{"shop", "production", "web"}, web.Number))
	if !ok || spec.Env["MODE"] != "prod" || spec.Env["DOCK_ENVIRONMENT"] != "production" {
		t.Fatalf("promoted web env: %v", spec.Env)
	}
	after := len(slices.DeleteFunc(lab.Ops(), func(op string) bool { return !strings.HasPrefix(op, "build ") }))
	if after != builds || hasOp(p.rt.Ops(), "build ") {
		t.Fatalf("promote rebuilt: %d builds before, %d after", builds, after)
	}

	// Promoting again changes nothing: both services already run
	// staging's images, by registry digest (web) and by image ID (worker).
	again, err := p.c.Promote(p.ctx, testSystem, "shop", apitypes.PromoteRequest{From: "staging", To: "production", Services: []string{"web", "worker"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Promoted) != 0 || len(again.Skipped) != 2 || again.Skipped[0].Reason != apitypes.SkipSameImage || again.Skipped[1].Reason != apitypes.SkipSameImage {
		t.Fatalf("second promote: promoted %+v, skipped %+v", again.Promoted, again.Skipped)
	}
}

// Promoted services deploy one at a time: when one fails, the ones after
// it fail without starting.
func TestPromoteStopsAtFirstFailure(t *testing.T) {
	p, _ := labPool(t)
	for _, env := range []string{"staging", "production"} {
		p.createService(env, "api", apitypes.CreateServiceRequest{})
		p.createService(env, "web", apitypes.CreateServiceRequest{})
	}
	p.deploy(ServiceRef{"shop", "staging", "api"}, "ghcr.io/acme/api:1")
	p.deploy(ServiceRef{"shop", "staging", "web"}, "ghcr.io/acme/web:1")
	p.rt.FailHealthCheck(func(s node.ContainerSpec) error {
		if slices.Contains(s.Aliases, "api") {
			return fmt.Errorf("unhealthy")
		}
		return nil
	})
	res, err := p.c.Promote(p.ctx, testSystem, "shop", apitypes.PromoteRequest{From: "staging", To: "production"})
	if err != nil || len(res.Promoted) != 2 {
		t.Fatalf("promote: %+v, %v", res, err)
	}
	api := p.wait(ServiceRef{"shop", "production", "api"}, res.Promoted[0].Release.Number)
	web := p.wait(ServiceRef{"shop", "production", "web"}, res.Promoted[1].Release.Number)
	if api.Status != apitypes.ReleaseFailed || web.Status != apitypes.ReleaseFailed || web.Error != "not promoted: api failed first" {
		t.Fatalf("api %s (%s), web %s (%s)", api.Status, api.Error, web.Status, web.Error)
	}
	if hasOp(p.rt.Ops(), "pull ghcr.io/acme/web:1") {
		t.Fatalf("web started after api failed: %v", p.rt.Ops())
	}
}

// A service whose release can't even be created after earlier ones
// started stops the promotion there, like a failed release: it and the
// services after it are skipped, and the started ones are audited.
func TestPromoteStopsWhenAReleaseCantStart(t *testing.T) {
	p, _ := labPool(t)
	for _, name := range []string{"api", "web", "worker"} {
		p.createService("staging", name, apitypes.CreateServiceRequest{})
		p.createService("production", name, apitypes.CreateServiceRequest{})
		p.deploy(ServiceRef{"shop", "staging", name}, "ghcr.io/acme/"+name+":1")
	}
	// Production's web references cache, which is archived since: web's
	// next release can't resolve its variables.
	prod := EnvRef{"shop", "production"}
	cache := p.createService("production", "cache", apitypes.CreateServiceRequest{})
	p.set(prod, "cache", map[string]string{"HOST": "cache"}, true)
	p.set(prod, "web", map[string]string{"CACHE": "${{cache.HOST}}"}, true)
	if _, err := p.c.ArchiveService(p.ctx, testSystem, cache); err != nil {
		t.Fatal(err)
	}

	res, err := p.c.Promote(p.ctx, testSystem, "shop", apitypes.PromoteRequest{From: "staging", To: "production", Services: []string{"api", "web", "worker"}})
	if err != nil || len(res.Promoted) != 1 || res.Promoted[0].Service != "api" || len(res.Skipped) != 2 {
		t.Fatalf("promote: %+v, %v", res, err)
	}
	web, worker := res.Skipped[0], res.Skipped[1]
	if web.Service != "web" || web.Reason != apitypes.SkipFailed || !strings.Contains(web.Message, `no service "cache"`) {
		t.Fatalf("web skipped: %+v", web)
	}
	if worker.Service != "worker" || worker.Reason != apitypes.SkipFailed || worker.Message != "not promoted: web failed first" {
		t.Fatalf("worker skipped: %+v", worker)
	}
	if r := p.wait(ServiceRef{"shop", "production", "api"}, res.Promoted[0].Release.Number); r.Status != apitypes.ReleaseActive {
		t.Fatalf("api: %s (%s)", r.Status, r.Error)
	}
	evs, err := p.c.ListAudit(p.ctx, testSystem, AuditFilter{Action: "release.promote"})
	if err != nil || len(evs) != 1 || !strings.Contains(evs[0].Summary, "api (release 1 as 1); skipped web (failed); skipped worker (failed)") {
		t.Fatalf("audit: %+v, %v", evs, err)
	}
}

// A rename changes only the name: environments follow by ID, and names
// in use (by a server or a join token) are refused.
func TestRenameServer(t *testing.T) {
	p, _ := labPool(t)
	rename := func(from, to string) (apitypes.Server, error) {
		return p.c.UpdateServer(p.ctx, testSystem, from, apitypes.UpdateServerRequest{Name: &to})
	}
	if _, err := p.c.AddServer(p.ctx, testSystem, apitypes.AddServerRequest{Name: "lab2"}); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{p.c.localName(), "lab2"} {
		if _, err := rename("homelab", to); CodeOf(err) != apitypes.CodeConflict {
			t.Errorf("rename to %s: %v", to, err)
		}
	}
	if _, err := rename("homelab", "Lab One"); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("invalid name: %v", err)
	}

	s, err := rename("homelab", "lab1")
	if err != nil || s.Name != "lab1" || s.ID != p.ids["homelab"] {
		t.Fatalf("rename = %+v, %v", s, err)
	}
	if got := p.server("shop", "staging"); got != "lab1" {
		t.Fatalf("shop/staging on %s, want lab1", got)
	}
	if _, err := p.c.GetServer(p.ctx, testSystem, "homelab"); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("old name: %v", err)
	}
	audit, _ := p.c.ListAudit(p.ctx, testSystem, AuditFilter{Action: "server.rename", Limit: 1, Platform: true})
	if len(audit) != 1 || audit[0].Summary != "renamed server homelab to lab1" {
		t.Fatalf("audit = %+v", audit)
	}

	// The control plane's own server can be renamed too.
	if _, err := rename(p.c.localName(), "control"); err != nil || p.c.localName() != "control" {
		t.Fatalf("local rename: %v, name %s", err, p.c.localName())
	}
}

// Adding a server mints a join token; the node's join creates it; a
// revoke drops its key and connection but keeps its environments, which
// keep it from being removed.
func TestServerLifecycle(t *testing.T) {
	p, _ := labPool(t)
	added, err := p.c.AddServer(p.ctx, testSystem, apitypes.AddServerRequest{Name: "lab2", AcceptsNonProduction: true, Builder: true})
	if err != nil {
		t.Fatal(err)
	}
	if added.Server.Status != apitypes.ServerJoining || !strings.Contains(added.Command, "NODE_JOIN_TOKEN="+added.Token) {
		t.Fatalf("added: %+v", added)
	}
	// The token is never an argument of sudo, where the process list shows it.
	if _, sudo, _ := strings.Cut(added.Command, "sudo "); strings.Contains(sudo, added.Token) {
		t.Errorf("the join command puts the token on sudo's command line: %s", added.Command)
	}
	if _, err := p.c.AddServer(p.ctx, testSystem, apitypes.AddServerRequest{Name: "homelab"}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("adding an existing name: %v", err)
	}
	tok, err := remote.ParseJoinToken(added.Token)
	if err != nil {
		t.Fatal(err)
	}
	ns := p.c.NodeStore()
	fp := "sha256:" + strings.Repeat("ef", 32)
	srv, err := ns.Join(p.ctx, remote.JoinRequest{SecretHash: tok.SecretHash(), Fingerprint: fp,
		Hello: remote.Hello{Version: "v1", Facts: remote.Facts{Ingress: remote.IngressCloudflareTunnel, TunnelHostname: "abc.cfargotunnel.com", BaseDomain: "apps.example.dev"}}})
	if err != nil || srv.Name != "lab2" {
		t.Fatalf("join: %+v, %v", srv, err)
	}
	if _, err := ns.Join(p.ctx, remote.JoinRequest{SecretHash: tok.SecretHash(), Fingerprint: "sha256:" + strings.Repeat("aa", 32)}); err == nil {
		t.Fatal("a join token worked twice")
	}
	if key, ok, err := ns.NodeKey(p.ctx, fp); !ok || err != nil || key.ID != srv.ID {
		t.Fatalf("node key: %+v %v %v", key, ok, err)
	}
	got, err := p.c.GetServer(p.ctx, testSystem, "lab2")
	if err != nil || got.Server.Ingress != apitypes.IngressCloudflareTunnel || got.Server.TunnelHostname != "abc.cfargotunnel.com" || !got.Server.Builder ||
		got.Server.BaseDomain != "apps.example.dev" {
		t.Fatalf("lab2: %+v, %v", got.Server, err)
	}

	// homelab has shop/staging: revoked, it stays, and can't be removed.
	lab := p.ids["homelab"]
	rev, err := p.c.RevokeServer(p.ctx, testSystem, "homelab")
	if err != nil || rev.Status != apitypes.ServerRevoked || p.nodes.disconnects[lab] != 1 {
		t.Fatalf("revoke: %+v, %v (disconnects %d)", rev, err, p.nodes.disconnects[lab])
	}
	if p.server("shop", "staging") != "homelab" {
		t.Fatal("revoking moved the environment")
	}
	if err := p.c.RemoveServer(p.ctx, testSystem, "homelab"); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("removing a server with environments: %v", err)
	}
	// Once shop is archived, its staging no longer holds homelab.
	if _, err := p.c.ArchiveProject(p.ctx, testSystem, "shop"); err != nil {
		t.Fatal(err)
	}
	if err := p.c.RemoveServer(p.ctx, testSystem, "homelab"); err != nil {
		t.Fatalf("removing a server with only archived environments: %v", err)
	}
	if got := p.server("shop", "staging"); got != p.c.localName() {
		t.Fatalf("archived staging moved to %s, want the local server %s", got, p.c.localName())
	}
	if err := p.c.RemoveServer(p.ctx, testSystem, "lab2"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.c.GetServer(p.ctx, testSystem, "lab2"); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("removed server: %v", err)
	}
}

// Domains of an environment on a tunnel server are https:// and point
// at the tunnel; generated ones use that server's base domain.
func TestDomainsFollowTheirServer(t *testing.T) {
	p, _ := labPool(t)
	lab := p.ids["homelab"]
	p.nodes.add(lab, p.rts[lab], remote.Hello{Facts: remote.Facts{Ingress: remote.IngressCloudflareTunnel, TunnelHostname: "t1.cfargotunnel.com"}})
	p.c.NodeStatus(p.nodes.State(lab))
	base := "up.example.dev"
	if _, err := p.c.UpdateServer(p.ctx, testSystem, "homelab", apitypes.UpdateServerRequest{BaseDomain: &base}); err != nil {
		t.Fatal(err)
	}
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	gen, err := p.c.GenerateDomain(p.ctx, testSystem, web)
	if err != nil || !generatedLike("web-shop-staging", base).MatchString(gen.Domain.Hostname) {
		t.Fatalf("generated: %+v, %v", gen.Domain, err)
	}
	custom, err := p.c.AddDomain(p.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	d := custom.Domain
	if d.URL != "https://shop.example.com" || len(d.DNSRecords) == 0 || d.DNSRecords[0] != (apitypes.DNSRecord{Type: "CNAME", Name: "shop.example.com", Value: "t1.cfargotunnel.com"}) {
		t.Fatalf("tunnel domain: %+v", d)
	}
}

// A node whose Caddy has TLS disabled says so in hello; its domains are
// http:// even though the control plane serves TLS.
func TestPlainHTTPNode(t *testing.T) {
	p, _ := labPool(t)
	lab := p.ids["homelab"]
	p.nodes.add(lab, p.rts[lab], remote.Hello{Facts: remote.Facts{Ingress: remote.IngressDirect, PublicIPs: []string{"192.0.2.7"}, PlainHTTP: true}})
	p.c.NodeStatus(p.nodes.State(lab))
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	d, err := p.c.AddDomain(p.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"})
	if err != nil || d.Domain.URL != "http://shop.example.com" {
		t.Fatalf("domain on a plain HTTP node: %+v, %v", d.Domain, err)
	}
}

// Builders fetch Railpack's images ahead of their first build (ADR 0089
// amendment): the control plane at start, a node once it connects as
// one. A server that doesn't build never does.
func TestWarmBuilders(t *testing.T) {
	p, lab := labPool(t)
	waitOps(t, p.rt, "warm builder")
	if hasOp(lab.Ops(), "warm builder") {
		t.Fatalf("homelab doesn't build but fetched build images: %v", lab.Ops())
	}
	p.withRegistry(&fakeRegistry{}) // homelab becomes the only builder
	waitOps(t, lab, "warm builder")
}

// A node installs the control plane's own release; a dev build pins none.
func TestNodeInstallCommandPinsRelease(t *testing.T) {
	if got := nodeInstallCommand("tok", "", "v1.2.3"); !strings.Contains(got, " DOCK_VERSION=v1.2.3 bash -s -- --yes") {
		t.Errorf("release: %s", got)
	}
	if got := nodeInstallCommand("tok", "INGRESS=cloudflare_tunnel ", "dev"); strings.Contains(got, "DOCK_VERSION") || !strings.Contains(got, "NODE_JOIN_TOKEN INGRESS=cloudflare_tunnel bash") {
		t.Errorf("dev: %s", got)
	}
}

// The control plane scans a kind only while no other server builds it,
// online or not: scans wait for the homelab rather than download a
// vulnerability database to the control plane.
func TestControlPlaneScansLast(t *testing.T) {
	p, _ := labPool(t)
	p.withRegistry(&fakeRegistry{})
	p.opts.Config.BuildKit.Enabled = true
	p.restart()
	names := func() []string {
		out, _, _ := p.c.scanBuilders(p.ctx)
		var names []string
		for _, s := range out {
			names = append(names, s.Name)
		}
		return names
	}
	// homelab builds only non-production: cp still scans production.
	if got, _, _ := p.c.scanBuilders(p.ctx); len(got) != 2 || !got[0].Local || got[0].BuildsNonProduction || !got[0].BuildsProduction {
		t.Fatalf("scanners %v: want cp for production only, then homelab", names())
	}
	p.buildsProduction("homelab")
	if got := names(); !slices.Equal(got, []string{"homelab"}) {
		t.Fatalf("scanners %v, want homelab alone", got)
	}
	p.setOffline("homelab", true)
	if got := names(); len(got) != 0 {
		t.Fatalf("scanners %v with homelab offline, want none", got)
	}
}

// Without the registry images stay where they were built: the control
// plane keeps scanning its own though homelab builds every kind too.
func TestControlPlaneScansItsOwnWithoutRegistry(t *testing.T) {
	p, _ := labPool(t)
	p.opts.Config.BuildKit.Enabled = true
	p.restart()
	on := true
	if _, err := p.c.UpdateServer(p.ctx, testSystem, "homelab", apitypes.UpdateServerRequest{Builder: &on, BuildsProduction: &on}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := p.c.scanBuilders(p.ctx)
	if !slices.ContainsFunc(got, func(s store.Server) bool { return s.Local && s.BuildsProduction && s.BuildsNonProduction }) {
		t.Fatalf("scanners %+v: want the control plane for both kinds", got)
	}
}
