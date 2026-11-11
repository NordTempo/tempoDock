package core

import (
	"context"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

// sleepEnv makes shop/staging with a database (a volume) and a web
// service with a domain, both deployed, and returns them.
func (h *harness) sleepEnv() (db, web ServiceRef) {
	h.t.Helper()
	h.service("api", apitypes.CreateServiceRequest{}) // creates shop
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		h.t.Fatal(err)
	}
	staging := EnvRef{"shop", "staging"}
	for _, name := range []string{"db", "web"} {
		if _, err := h.c.CreateService(h.ctx, testSystem, staging, apitypes.CreateServiceRequest{Name: name, Port: 8080}); err != nil {
			h.t.Fatal(err)
		}
	}
	db, web = ServiceRef{"shop", "staging", "db"}, ServiceRef{"shop", "staging", "web"}
	if _, err := h.c.AddVolume(h.ctx, testSystem, db, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.c.GenerateDomain(h.ctx, testSystem, web); err != nil {
		h.t.Fatal(err)
	}
	h.deploy(db, "postgres:17")
	h.deploy(web, "app:1")
	return db, web
}

// state is a service's state as the API shows it.
func (h *harness) state(ref ServiceRef) apitypes.ServiceState {
	h.t.Helper()
	v, err := h.c.GetService(h.ctx, testSystem, ref)
	if err != nil {
		h.t.Fatal(err)
	}
	return v.Status.State
}

// wakeKey is the wake key of web's route, "" when it has none.
func (h *harness) wakeKey(host string) string {
	for _, r := range h.rt.Routes() {
		if slices.Contains(r.Hostnames, host) {
			return r.Wake
		}
	}
	h.t.Fatalf("no route for %s in %+v", host, h.rt.Routes())
	return ""
}

// Sleeping stops an environment's containers without removing them,
// switches its routes to the wake check and leaves its services asleep;
// waking starts the same containers again (no new release) and routes
// them as before.
func TestSleepAndWake(t *testing.T) {
	h := newHarness(t)
	db, web := h.sleepEnv()
	host := "web-shop-staging.apps.test"
	staging := EnvRef{"shop", "staging"}
	running := h.rt.Running()

	e, err := h.c.SleepEnvironment(h.ctx, testSystem, staging)
	if err != nil {
		t.Fatal(err)
	}
	if e.Sleep == nil || e.Sleep.State != apitypes.SleepAsleep || e.Sleep.SleptBy != testSystem.Name {
		t.Fatalf("sleep = %+v", e.Sleep)
	}
	h.waitRunning()
	for _, ref := range []ServiceRef{db, web} {
		if s := h.state(ref); s != apitypes.ServiceAsleep {
			t.Fatalf("%s is %s, want asleep", ref.Service, s)
		}
		if h.status(ref, 1) != apitypes.ReleaseActive {
			t.Fatalf("%s's release isn't active any more", ref.Service)
		}
	}
	if slices.ContainsFunc(h.rt.Ops(), func(op string) bool { return op == "remove "+h.ctr(web, 1) }) {
		t.Fatal("sleep removed the container")
	}
	if h.wakeKey(host) == "" {
		t.Fatal("asleep route has no wake check")
	}

	if _, err := h.c.WakeEnvironment(h.ctx, testSystem, staging); err != nil {
		t.Fatal(err)
	}
	if got := h.rt.Running(); !slices.Equal(got, running) {
		t.Fatalf("running after wake = %v, want %v", got, running)
	}
	ops := h.rt.Ops()
	if d, w := indexOf(ops, "resume "+h.ctr(db, 1)), indexOf(ops, "resume "+h.ctr(web, 1)); d < 0 || w < 0 || d > w {
		t.Fatalf("the database must wake first: %v", ops)
	}
	if s := h.state(web); s != apitypes.ServiceRunning {
		t.Fatalf("web is %s after wake", s)
	}
	if h.wakeKey(host) != "" {
		t.Fatal("awake route still asks the wake check")
	}
	if r, err := h.c.GetRelease(h.ctx, testSystem, web, 2); err == nil {
		t.Fatalf("waking made release %d", r.Number)
	}
}

// An environment idle for its setting goes to sleep on the tick, unless
// Caddy still proxies requests to it; one with requests stays awake, and
// production never sleeps.
func TestSleepWhenIdle(t *testing.T) {
	h := newHarness(t)
	_, web := h.sleepEnv()
	staging := EnvRef{"shop", "staging"}
	if _, err := h.c.SetEnvironmentSleep(h.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.SetSleepRequest{AfterMinutes: 30}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("production sleeps: %v", err)
	}
	if _, err := h.c.SetEnvironmentSleep(h.ctx, testSystem, staging, apitypes.SetSleepRequest{AfterMinutes: 2}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("2 minutes: %v", err)
	}
	if _, err := h.c.SetEnvironmentSleep(h.ctx, testSystem, staging, apitypes.SetSleepRequest{AfterMinutes: 30}); err != nil {
		t.Fatal(err)
	}
	es, err := h.c.loadEnv(h.ctx, 1, staging)
	if err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(31 * time.Minute)

	// Requests this minute: its idle time starts over.
	ss, err := h.c.serviceFor(h.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	h.c.sleepTick(later, map[int64]*serviceTraffic{ss.svc.ID: {host: store.ListTrafficHostsRow{EnvironmentID: ss.env.ID}}})
	h.c.sleepTick(later.Add(time.Minute), nil)
	if h.c.isAsleep(es.env.ID) {
		t.Fatal("asleep a minute after a request")
	}

	// A websocket keeps it awake past the timeout.
	h.rt.SetOpenRequests(1)
	h.c.sleepIdle(h.ctx, es.env.ID)
	if h.c.isAsleep(es.env.ID) {
		t.Fatal("asleep with a request open")
	}
	h.rt.SetOpenRequests(0)
	h.c.sleepTick(time.Now().Add(62*time.Minute), nil)
	eventually(t, "staging to sleep", func() bool { return h.state(web) == apitypes.ServiceAsleep })
	env, err := h.c.store.GetEnvironment(h.ctx, es.env.ID)
	if err != nil || env.AsleepAt == nil || env.SleptBy != "" {
		t.Fatalf("environment after an idle sleep: %+v, %v", env, err)
	}
}

// A request to an asleep environment wakes it, through the control
// plane's Waker, and goes on once it is awake.
func TestWakeForRequest(t *testing.T) {
	h := newHarness(t)
	_, web := h.sleepEnv()
	host := "web-shop-staging.apps.test"
	staging := EnvRef{"shop", "staging"}
	if _, err := h.c.SetEnvironmentSleep(h.ctx, testSystem, staging, apitypes.SetSleepRequest{AfterMinutes: 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SleepEnvironment(h.ctx, testSystem, staging); err != nil {
		t.Fatal(err)
	}
	key := h.wakeKey(host)
	w := h.c.RequestWaker()
	if !w.Asleep(key) || w.Asleep("999") {
		t.Fatalf("asleep: %v for %s, %v for 999", w.Asleep(key), key, w.Asleep("999"))
	}
	if err := w.Wake(h.ctx, key, "GET /orders/:id"); err != nil {
		t.Fatal(err)
	}
	if h.state(web) != apitypes.ServiceRunning || h.wakeKey(host) != "" {
		t.Fatalf("after the request: web %s, wake key %q", h.state(web), h.wakeKey(host))
	}
	v, err := h.c.GetEnvironment(h.ctx, testSystem, staging)
	if err != nil || v.Sleep == nil || v.Sleep.WokeBy != "GET /orders/:id" {
		t.Fatalf("woke by = %+v, %v", v.Sleep, err)
	}

	// Requests held while a wake is in flight wait for it, a goroutine
	// each no longer: a slow wake met a page's reloads by the thousand.
	if _, err := h.c.SleepEnvironment(h.ctx, testSystem, staging); err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(key, 10, 64)
	op := h.c.beginOp(id, h.c.localID, true)
	before := runtime.NumGoroutine()
	for range 200 {
		ctx, cancel := context.WithTimeout(h.ctx, time.Millisecond)
		if err := w.Wake(ctx, key, "GET /"); err == nil {
			t.Fatal("woke while the wake is in flight")
		}
		cancel()
	}
	if n := runtime.NumGoroutine() - before; n > 20 {
		t.Errorf("%d goroutines left by 200 held requests", n)
	}
	h.c.endOp(id, op, true, nil)
}

// Sleeping takes the environment from its users: deploy scope, refused to
// readers. Waking is read scope. Production never sleeps.
func TestSleepPermissions(t *testing.T) {
	h := newHarness(t)
	h.sleepEnv()
	staging := EnvRef{"shop", "staging"}
	reader := Actor{Kind: "token", OrgID: 1, Name: "ro", TokenID: 1, Scope: apitypes.ScopeRead}
	if _, err := h.c.SleepEnvironment(h.ctx, reader, staging); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("read token sleeps: %v", err)
	}
	if _, err := h.c.SleepEnvironment(h.ctx, testSystem, EnvRef{"shop", "production"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("production sleeps: %v", err)
	}
	if _, err := h.c.SleepEnvironment(h.ctx, testSystem, staging); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.WakeEnvironment(h.ctx, reader, staging); err != nil {
		t.Fatalf("read token wakes: %v", err)
	}
}

// A deploy to an asleep environment wakes it first; a scheduled job is
// skipped while it sleeps.
func TestDeployWakes(t *testing.T) {
	h := newHarness(t)
	db, web := h.sleepEnv()
	if _, err := h.c.SleepEnvironment(h.ctx, testSystem, EnvRef{"shop", "staging"}); err != nil {
		t.Fatal(err)
	}
	if r := h.deploy(web, "app:2"); r.Status != apitypes.ReleaseActive {
		t.Fatalf("deploy into an asleep environment: %s", r.Status)
	}
	if s := h.state(db); s != apitypes.ServiceRunning {
		t.Fatalf("db is %s after a deploy of web", s)
	}
}

// A daemon that crashed with an asleep environment's containers running
// again (a host reboot, a crash mid-wake) stops them on start: asleep_at
// is the target.
func TestSleepSettlesAfterCrash(t *testing.T) {
	h := newHarness(t)
	_, web := h.sleepEnv()
	if _, err := h.c.SleepEnvironment(h.ctx, testSystem, EnvRef{"shop", "staging"}); err != nil {
		t.Fatal(err)
	}
	ctr := h.ctrID(web, 1)
	if err := h.rt.Resume(h.ctx, ctr); err != nil {
		t.Fatal(err)
	}
	h.crash()
	if ct, err := h.rt.Inspect(h.ctx, ctr); err != nil || ct.State == node.StateRunning {
		t.Fatalf("web after restart: %+v, %v", ct, err)
	}
	if s := h.state(web); s != apitypes.ServiceAsleep {
		t.Fatalf("web is %s after restart", s)
	}
}

// An environment on another server sleeps too, through the node link:
// its routes there carry the wake check, and the node's wake request (an
// ActionWake event) wakes it. A node too old for it refuses.
func TestSleepOnAnotherServer(t *testing.T) {
	p := newPool(t, poolServer{"cp", true, false, 8}, poolServer{"homelab", false, true, 16})
	p.project("shop")
	p.mustEnv("shop", "staging", apitypes.KindNonProduction, "homelab")
	staging := EnvRef{"shop", "staging"}
	if _, err := p.c.CreateService(p.ctx, testSystem, staging, apitypes.CreateServiceRequest{Name: "web", Port: 8080}); err != nil {
		t.Fatal(err)
	}
	web := ServiceRef{"shop", "staging", "web"}
	if _, err := p.c.UpdateServer(p.ctx, testSystem, "homelab", apitypes.UpdateServerRequest{BaseDomain: ptr("lab.example.dev")}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.c.GenerateDomain(p.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	p.deploy(web, "app:1")
	lab := p.ids["homelab"]
	rt := p.rts[lab]
	wakeKey := func() string {
		for _, r := range rt.Routes() {
			if r.Wake != "" {
				return r.Wake
			}
		}
		return ""
	}

	e, err := p.c.SleepEnvironment(p.ctx, testSystem, staging)
	if err != nil || e.Sleep == nil || e.Sleep.State != apitypes.SleepAsleep {
		t.Fatalf("sleep on homelab: %+v, %v", e.Sleep, err)
	}
	if len(rt.Running()) != 0 || wakeKey() == "" {
		t.Fatalf("asleep on homelab: running %v, wake key %q", rt.Running(), wakeKey())
	}

	// The node's wake check asks over the link.
	rt.Emit(node.ContainerEvent{Action: node.ActionWake, Labels: node.Labels{Environment: wakeKey()}, Request: "GET /orders/:id"})
	eventually(t, "staging to wake from the node's request", func() bool { return p.state(web) == apitypes.ServiceRunning })
	eventually(t, "homelab's routes to drop the wake check", func() bool { return wakeKey() == "" })

	// An older node gets no sleeping environments.
	p.nodes.add(lab, rt, remote.Hello{Protocol: remote.SleepProtocol - 1, Facts: remote.Facts{Ingress: remote.IngressDirect}})
	if _, err := p.c.SleepEnvironment(p.ctx, testSystem, staging); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("sleep on an older node: %v", err)
	}
	if v, err := p.c.GetEnvironment(p.ctx, testSystem, staging); err != nil || v.Sleep != nil && v.Sleep.Blocked == "" {
		t.Fatalf("an older node's environment: %+v, %v", v.Sleep, err)
	}
}

// Moving an asleep environment wakes it first, and sleep follows it to
// its new server: there it sleeps again and its wake check knows it.
func TestSleepFollowsMove(t *testing.T) {
	p, _, _, web, _ := movePool(t)
	staging := EnvRef{"shop", "staging"}
	if _, err := p.c.SetEnvironmentSleep(p.ctx, testSystem, staging, apitypes.SetSleepRequest{AfterMinutes: 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.c.SleepEnvironment(p.ctx, testSystem, staging); err != nil {
		t.Fatal(err)
	}
	if _, err := p.c.MoveEnvironment(p.ctx, testSystem, staging, apitypes.MoveEnvironmentRequest{To: p.ids2name("cp")}); err != nil {
		t.Fatal(err)
	}
	if m := p.waitMove(staging); m.Status != apitypes.MoveSucceeded {
		t.Fatalf("move = %+v", m)
	}
	if e, err := p.c.GetEnvironment(p.ctx, testSystem, staging); err != nil || e.Sleep == nil || e.Sleep.State == apitypes.SleepAsleep || p.state(web) != apitypes.ServiceRunning {
		t.Fatalf("after the move: %+v %v, web %s", e.Sleep, err, p.state(web))
	}
	if _, err := p.c.SleepEnvironment(p.ctx, testSystem, staging); err != nil {
		t.Fatal(err)
	}
	es, err := p.c.loadEnv(p.ctx, 1, staging)
	if err != nil {
		t.Fatal(err)
	}
	if !p.c.RequestWaker().Asleep(strconv.FormatInt(es.env.ID, 10)) {
		t.Error("the control plane's wake check doesn't know the moved environment sleeps")
	}
}
