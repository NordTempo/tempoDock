package core

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/NordTempo/tempoDock/internal/store"
)

// fakeRegistry records what core asks of the platform registry. Tokens
// name their access and repository, so tests can see which was used.
type fakeRegistry struct {
	mu     sync.Mutex
	ops    []string
	pruned int // what Prune reports deleting
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

func (r *fakeRegistry) Prune(_ context.Context, keep []string) (int, error) {
	r.op("prune keep=%s", strings.Join(keep, ","))
	return r.pruned, nil
}

func (r *fakeRegistry) GarbageCollect(context.Context) error {
	r.op("gc")
	return nil
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
	if _, err := p.store.UpdateServerSettings(p.ctx, store.UpdateServerSettingsParams{Builder: true, ID: lab}); err != nil {
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
	if _, err := p.c.CreateService(p.ctx, System, EnvRef{"shop", env}, req); err != nil {
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
	rel, err := p.c.DeploySource(p.ctx, System, ref, &buf, 1<<20)
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
	if _, err := p.c.AddDomain(p.ctx, System, web, apitypes.AddDomainRequest{Hostname: "web.staging.test"}); err != nil {
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
	_, err := p.c.DeployImage(p.ctx, System, web, apitypes.DeployImageRequest{Image: "app:2"})
	if CodeOf(err) != apitypes.CodeServerOffline || err.Error() != "server homelab is offline" {
		t.Fatalf("deploy to an offline server: %v", err)
	}
	rels, err := p.c.ListReleases(p.ctx, System, web, 10)
	if err != nil || len(rels) != 1 {
		t.Fatalf("releases after a refused deploy: %d, %v", len(rels), err)
	}
	srv, err := p.c.GetServer(p.ctx, System, "homelab")
	if err != nil || srv.Server.Status != apitypes.ServerOffline {
		t.Fatalf("homelab: %+v, %v", srv.Server, err)
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
	ss, err := p.c.serviceFor(p.ctx, System, web, apitypes.ScopeRead, "testing")
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

	srv, err := p.c.GetServer(p.ctx, System, "homelab")
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
	web := ServiceRef{"shop", "production", "web"}
	p.createService("production", "web", apitypes.CreateServiceRequest{})

	r := p.deploySource(web)
	if r.Status != apitypes.ReleaseActive {
		t.Fatalf("release 1: %s (%s)", r.Status, r.Error)
	}
	ss, err := p.c.serviceFor(p.ctx, System, web, apitypes.ScopeRead, "testing")
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

	// With a rollback window of one, the next deploy removes release 1's
	// images: the pulled one on cp, the built one on homelab.
	p.c.cfg.Defaults.KeepReleases = 1
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
	web := p.createService("production", "web", apitypes.CreateServiceRequest{})
	p.setOffline("homelab", true)
	_, err := p.c.DeploySource(p.ctx, System, web, strings.NewReader("x"), 1<<20)
	if CodeOf(err) != apitypes.CodeServerOffline || !strings.Contains(err.Error(), "builders: homelab") {
		t.Fatalf("deploy without a builder: %v", err)
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

	res, err := p.c.Promote(p.ctx, System, "shop", apitypes.PromoteRequest{From: "staging", To: "production"})
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
	res, err := p.c.Promote(p.ctx, System, "shop", apitypes.PromoteRequest{From: "staging", To: "production"})
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

// Adding a server mints a join token; the node's join creates it; a
// revoke drops its key and connection but keeps its environments, which
// keep it from being removed.
func TestServerLifecycle(t *testing.T) {
	p, _ := labPool(t)
	added, err := p.c.AddServer(p.ctx, System, apitypes.AddServerRequest{Name: "lab2", AcceptsNonProduction: true, Builder: true})
	if err != nil {
		t.Fatal(err)
	}
	if added.Server.Status != apitypes.ServerJoining || !strings.Contains(added.Command, "NODE_JOIN_TOKEN="+added.Token) {
		t.Fatalf("added: %+v", added)
	}
	if _, err := p.c.AddServer(p.ctx, System, apitypes.AddServerRequest{Name: "homelab"}); CodeOf(err) != apitypes.CodeConflict {
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
	got, err := p.c.GetServer(p.ctx, System, "lab2")
	if err != nil || got.Server.Ingress != apitypes.IngressCloudflareTunnel || got.Server.TunnelHostname != "abc.cfargotunnel.com" || !got.Server.Builder ||
		got.Server.BaseDomain != "apps.example.dev" {
		t.Fatalf("lab2: %+v, %v", got.Server, err)
	}

	// homelab has shop/staging: revoked, it stays, and can't be removed.
	lab := p.ids["homelab"]
	rev, err := p.c.RevokeServer(p.ctx, System, "homelab")
	if err != nil || rev.Status != apitypes.ServerRevoked || p.nodes.disconnects[lab] != 1 {
		t.Fatalf("revoke: %+v, %v (disconnects %d)", rev, err, p.nodes.disconnects[lab])
	}
	if p.server("shop", "staging") != "homelab" {
		t.Fatal("revoking moved the environment")
	}
	if err := p.c.RemoveServer(p.ctx, System, "homelab"); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("removing a server with environments: %v", err)
	}
	// Once shop is archived, its staging no longer holds homelab.
	if _, err := p.c.ArchiveProject(p.ctx, System, "shop"); err != nil {
		t.Fatal(err)
	}
	if err := p.c.RemoveServer(p.ctx, System, "homelab"); err != nil {
		t.Fatalf("removing a server with only archived environments: %v", err)
	}
	if got := p.server("shop", "staging"); got != p.c.local.Name {
		t.Fatalf("archived staging moved to %s, want the local server %s", got, p.c.local.Name)
	}
	if err := p.c.RemoveServer(p.ctx, System, "lab2"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.c.GetServer(p.ctx, System, "lab2"); CodeOf(err) != apitypes.CodeNotFound {
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
	if _, err := p.c.UpdateServer(p.ctx, System, "homelab", apitypes.UpdateServerRequest{BaseDomain: &base}); err != nil {
		t.Fatal(err)
	}
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	gen, err := p.c.GenerateDomain(p.ctx, System, web)
	if err != nil || gen.Domain.Hostname != "web-shop-staging.up.example.dev" {
		t.Fatalf("generated: %+v, %v", gen.Domain, err)
	}
	custom, err := p.c.AddDomain(p.ctx, System, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"})
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
	d, err := p.c.AddDomain(p.ctx, System, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"})
	if err != nil || d.Domain.URL != "http://shop.example.com" {
		t.Fatalf("domain on a plain HTTP node: %+v, %v", d.Domain, err)
	}
}
