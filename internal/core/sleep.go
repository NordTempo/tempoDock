package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/traffic"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Sleeping environments (ADR 0070). An environment without
// requests for sleep_after_minutes stops its services' containers (kept,
// not removed: same release, port and volumes) and wakes on the next
// request to its domains, a deploy, a run or the Data panel. Sleeping is
// not a release: it changes whether a release runs, not what runs.
//
// Idle means idle: there is no timer. The minute statistics tick already
// drains every server's traffic counts; sleepTick notes which sleepable
// environments had requests and puts the ones idle for long enough to
// sleep, after asking Caddy whether requests (websockets, streams) are
// still open. environments.asleep_at is the target state: set before
// anything stops and cleared once every service is back, so recovery
// converges to it after a crash (settleSleep).
//
// While an environment is asleep its routes carry a wake check
// (node.Route.Wake): Caddy asks the internal listener's wake handler
// (WakeForRequest) after the route's access check, which wakes the
// environment and holds the request until it is awake.
//
// The control plane's own server only, for now: the node link doesn't
// carry the wake check or the calls sleeping needs.

// Waiting a request to an asleep environment may do: a page load gets the
// waking page after wakeHoldPage, anything else a 503 after wakeHoldAPI,
// and at most wakeMaxWaiting wait at once per environment (the rest get
// their answer at once).
const (
	wakeHoldPage   = 3 * time.Second
	wakeHoldAPI    = 30 * time.Second
	wakeMaxWaiting = 256
)

// sleepState is the sleep manager's memory: the environments that may
// sleep on their own or are asleep, loaded at start and when a setting
// changes (bounded by environments, not requests).
type sleepState struct {
	mu   sync.Mutex
	envs map[int64]*sleepEnv
}

// sleepEnv is one sleepable or asleep environment.
type sleepEnv struct {
	serverID int64
	after    time.Duration // 0: sleeps only when asked
	asleep   bool
	// lastActive is its last request, deploy, wake or run end; it sleeps
	// after `after` without one. lastRequest is the last minute with
	// requests, for the API.
	lastActive, lastRequest time.Time
	// busy counts what keeps it awake while it lasts: command runs.
	busy int
	// waiting counts requests held for its wake (wakeMaxWaiting).
	waiting int
	// op is the sleep or wake in flight, nil when none.
	op *sleepOp
}

// sleepOp is a sleep or wake in flight; done closes when it ends.
type sleepOp struct {
	waking bool
	done   chan struct{}
	err    error
}

// loadSleep reads the sleepable environments, keeping what is known of
// those already loaded. An environment new to the map gets a full
// timeout from now, so a restart or a new setting never sleeps one at
// once.
func (c *Core) loadSleep(ctx context.Context) error {
	rows, err := c.store.ListSleepEnvironments(ctx)
	if err != nil {
		return err
	}
	s := &c.sleep
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.envs
	s.envs = make(map[int64]*sleepEnv, len(rows))
	t := time.Now()
	for _, r := range rows {
		e := old[r.ID]
		if e == nil {
			e = &sleepEnv{lastActive: t}
		}
		e.serverID, e.after, e.asleep = r.ServerID, time.Duration(r.SleepAfterMinutes)*time.Minute, r.AsleepAt != nil
		s.envs[r.ID] = e
	}
	// One whose sleep or wake is in flight stays until it ends (a setting
	// turned off meanwhile): requests must still find it asleep.
	for id, e := range old {
		if _, ok := s.envs[id]; !ok && e.op != nil {
			s.envs[id] = e
		}
	}
	return nil
}

// reloadSleep is loadSleep for callers that changed a setting.
func (c *Core) reloadSleep(ctx context.Context) {
	if err := c.loadSleep(ctx); err != nil {
		c.log.Error("sleep: load environments", "err", err)
	}
}

// touchSleep counts activity in an environment: its idle time starts over.
func (c *Core) touchSleep(envID int64) {
	s := &c.sleep
	s.mu.Lock()
	if e := s.envs[envID]; e != nil {
		e.lastActive = time.Now()
	}
	s.mu.Unlock()
}

// holdAwake keeps an environment from sleeping until the returned func
// is called (a command run); its idle time starts over then.
func (c *Core) holdAwake(envID int64) (release func()) {
	s := &c.sleep
	s.mu.Lock()
	e := s.envs[envID]
	if e != nil {
		e.busy++
	}
	s.mu.Unlock()
	return sync.OnceFunc(func() {
		s.mu.Lock()
		if e != nil {
			e.busy--
			e.lastActive = time.Now()
		}
		s.mu.Unlock()
	})
}

// isAsleep reports whether an environment is asleep or going to sleep,
// from memory: for paths that must not wait for the database (probes).
func (c *Core) isAsleep(envID int64) bool {
	s := &c.sleep
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.envs[envID]
	return e != nil && (e.asleep || e.op != nil && !e.op.waking)
}

// sleepTick runs on the statistics tick, after its traffic step: the
// environments of services with requests this minute (traffic) start
// their idle time over, and those idle for long enough go to sleep in
// the background. Without sleepable environments it does nothing; with
// them, a map walk and no query unless one is due.
func (c *Core) sleepTick(now time.Time, active map[int64]*serviceTraffic) {
	s := &c.sleep
	s.mu.Lock()
	if len(s.envs) == 0 {
		s.mu.Unlock()
		return
	}
	for _, st := range active {
		if e := s.envs[st.host.EnvironmentID]; e != nil {
			e.lastActive, e.lastRequest = now, now
		}
	}
	var due []int64
	for id, e := range s.envs {
		if e.after > 0 && !e.asleep && e.op == nil && e.busy == 0 && e.serverID == c.localID && now.Sub(e.lastActive) >= e.after {
			due = append(due, id)
		}
	}
	s.mu.Unlock()
	for _, id := range due {
		c.wg.Go(func() { c.sleepIdle(c.ctx, id) })
	}
}

// sleepIdle puts an idle environment to sleep, unless something keeps it
// awake: a deploy in flight, a move, or requests Caddy still proxies to
// it (a websocket logs only when it ends). Each of those starts its idle
// time over.
func (c *Core) sleepIdle(ctx context.Context, envID int64) {
	env, err := c.store.GetEnvironment(ctx, envID)
	if err != nil || env.ArchivedAt != nil || env.AsleepAt != nil {
		return
	}
	p, err := c.store.GetProject(ctx, env.ProjectID)
	if err != nil {
		return
	}
	es := envScope{p, env}
	if why := c.keepsAwake(ctx, es); why != "" {
		c.log.Debug("sleep: stays awake", "environment", es.String(), "why", why)
		c.touchSleep(envID)
		return
	}
	if err := c.putToSleep(ctx, es, ""); err != nil && !errors.Is(err, errAwakeAgain) {
		c.log.Warn("sleep: put to sleep", "environment", es.String(), "err", err)
	}
}

// keepsAwake says what keeps an idle environment awake, "" if nothing.
func (c *Core) keepsAwake(ctx context.Context, es envScope) string {
	if n, err := c.store.CountEnvironmentInFlight(ctx, es.env.ID); err != nil || n > 0 {
		return "a deploy is in flight"
	}
	if _, err := c.store.GetOpenEnvironmentMove(ctx, es.env.ID); !store.IsNotFound(err) {
		return "it is moving"
	}
	ups, err := c.envUpstreams(ctx, es.env.ID)
	if err != nil {
		return "its routes could not be read"
	}
	if len(ups) > 0 {
		n, err := c.runtime(es.env.ServerID).OpenRequests(ctx, ups)
		if err != nil || n > 0 {
			return "requests are open"
		}
	}
	return ""
}

// envUpstreams are the host:port upstreams of an environment's routes.
func (c *Core) envUpstreams(ctx context.Context, envID int64) ([]string, error) {
	rows, err := c.store.ListEnvironmentServiceReleases(ctx, envID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		rel, err := c.store.GetRelease(ctx, r.ReleaseID)
		if err != nil {
			return nil, err
		}
		if rel.HostPort != 0 {
			out = append(out, "127.0.0.1:"+strconv.FormatInt(rel.HostPort, 10))
		}
	}
	return out, nil
}

// errAwakeAgain: a sleep found the environment awake on purpose (it was
// asleep already, or something woke it first).
var errAwakeAgain = errors.New("already asleep")

// beginOp starts a sleep or wake of an environment, after the one in
// flight ends. It returns nil when there is nothing to do: the
// environment is in the state asked for already. Only the control
// plane's own server's environments sleep (sleepable).
func (c *Core) beginOp(envID, serverID int64, waking bool) *sleepOp {
	s := &c.sleep
	for {
		s.mu.Lock()
		e := s.envs[envID]
		if e == nil {
			// Asleep without being loaded can't happen (asleep_at is in
			// the load); a manual sleep of an environment that doesn't
			// sleep on its own adds it.
			e = &sleepEnv{serverID: serverID, lastActive: time.Now()}
			s.envs[envID] = e
		}
		if op := e.op; op != nil {
			s.mu.Unlock()
			<-op.done
			continue
		}
		if e.asleep != waking {
			s.mu.Unlock()
			return nil
		}
		op := &sleepOp{waking: waking, done: make(chan struct{})}
		e.op = op
		s.mu.Unlock()
		return op
	}
}

// endOp ends an op: the environment's state is what it left.
func (c *Core) endOp(envID int64, op *sleepOp, asleep bool, err error) {
	s := &c.sleep
	s.mu.Lock()
	if e := s.envs[envID]; e != nil && e.op == op {
		e.op = nil
		e.asleep = asleep
		if !asleep {
			e.lastActive = time.Now()
		}
	}
	s.mu.Unlock()
	op.err = err
	close(op.done)
}

// sleepable says why an environment can't sleep, nil if it can.
func (c *Core) sleepable(es envScope) error {
	switch {
	case es.env.Kind == string(apitypes.KindProduction):
		return invalid("%s is a production environment, which never sleeps", es)
	case es.env.ArchivedAt != nil:
		return invalid("%s is archived", es)
	case es.env.ServerID != c.localID:
		return invalid("%s runs on another server, where environments can't sleep yet; only environments on the control plane's own server sleep", es)
	}
	return nil
}

// putToSleep puts an environment to sleep: marked asleep (the target),
// its routes switched to the wake check, then every running service's
// container stopped, kept for its wake. by is who asked, "" when idle.
// Services stopped on purpose (ADR 0050) are left as they are.
func (c *Core) putToSleep(ctx context.Context, es envScope, by string) error {
	if err := c.sleepable(es); err != nil {
		return err
	}
	op := c.beginOp(es.env.ID, es.env.ServerID, false)
	if op == nil {
		return errAwakeAgain
	}
	asleep, err := c.sleepNow(ctx, es, by)
	c.endOp(es.env.ID, op, asleep, err)
	return err
}

// sleepNow is putToSleep's work; asleep says whether the environment is
// asleep now.
func (c *Core) sleepNow(ctx context.Context, es envScope, by string) (asleep bool, err error) {
	rows, err := c.store.ListEnvironmentServiceReleases(ctx, es.env.ID)
	if err != nil {
		return false, err
	}
	var freed int64
	for _, r := range rows {
		if u, ok := c.stats.serviceUsage(r.ServiceID); ok {
			freed += u.MemoryBytes
		}
	}
	at := now()
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		// One transaction with the in-flight check, which startRelease
		// makes in its own: a deploy and a sleep never both go ahead.
		n, err := q.CountEnvironmentInFlight(ctx, es.env.ID)
		if err != nil {
			return err
		}
		if n > 0 {
			return conflict("%s has a deploy in flight; it can sleep once the deploy ends", es)
		}
		if m, err := q.SetEnvironmentAsleep(ctx, store.SetEnvironmentAsleepParams{AsleepAt: &at, SleptBy: by, FreedBytes: freed, ID: es.env.ID}); err != nil || m == 0 {
			return cmp.Or(err, errAwakeAgain)
		}
		es.env, err = q.GetEnvironment(ctx, es.env.ID)
		return err
	})
	if err != nil {
		return errors.Is(err, errAwakeAgain), err
	}
	c.publishSleep(es, apitypes.SleepGoing)
	// Routes first, so no request reaches a container that is stopping.
	if err := c.applyRoutes(ctx, es.env.ServerID); err != nil {
		c.log.Error("apply routes before sleep", "environment", es.String(), "err", err)
	}
	rt := c.runtime(es.env.ServerID)
	errs := make([]error, len(rows))
	var wg sync.WaitGroup
	for i, r := range rows {
		wg.Go(func() {
			if err := rt.Stop(ctx, r.ContainerID, stopTimeout); err != nil && !errors.Is(err, node.ErrNotFound) {
				errs[i] = err
			}
		})
	}
	wg.Wait()
	for i, r := range rows {
		ss, err := c.scopeOf(ctx, r.ServiceID)
		if err != nil {
			continue
		}
		if errs[i] != nil {
			c.log.Warn("sleep: stop container", "service", ss.String(), "err", errs[i])
			continue // recovery stops it (settleSleep)
		}
		c.setStatus(ss, apitypes.ServiceStatus{State: apitypes.ServiceAsleep})
		c.resolveUptimeAlerts(ctx, ss.svc.ID)
	}
	c.publishSleep(es, apitypes.SleepAsleep)
	return true, errors.Join(errs...)
}

// resolveUptimeAlerts resolves a sleeping service's uptime alerts: its
// checks are paused while it sleeps, and asleep is not down.
func (c *Core) resolveUptimeAlerts(ctx context.Context, serviceID int64) {
	checks, err := c.store.ListServiceUptimeChecks(ctx, serviceID)
	if err != nil {
		return
	}
	t := time.Now()
	for _, chk := range checks {
		c.resolveAlert(ctx, uptimeKey(chk.ID), t)
	}
}

// wake wakes an asleep environment and returns once it is awake: every
// service it stopped started again and past its release's health check,
// services others depend on (those with volumes: databases) first. by is
// who or what woke it. An environment that isn't asleep returns at once;
// a wake in flight is joined. A service that doesn't come back is left
// as an ordinary service that should run and doesn't (its status says
// so); the environment is awake either way. The wake runs on the daemon's
// context: a caller that gives up (a request's client) doesn't cut it
// short for the others waiting on it.
func (c *Core) wake(_ context.Context, es envScope, by string) error {
	if es.env.AsleepAt == nil && !c.isAsleep(es.env.ID) && !c.isWaking(es.env.ID) {
		return nil
	}
	op := c.beginOp(es.env.ID, es.env.ServerID, true)
	if op == nil {
		return nil
	}
	err := c.wakeNow(c.ctx, es, by)
	c.endOp(es.env.ID, op, err != nil, err)
	return err
}

// wakeFor is wake for a caller that goes on either way (a deploy, a run):
// a failed wake is logged and the caller carries on.
func (c *Core) wakeFor(ctx context.Context, es envScope, by string) {
	if err := c.wake(ctx, es, by); err != nil {
		c.log.Warn("wake", "environment", es.String(), "by", by, "err", err)
	}
}

// wakeNow is wake's work.
func (c *Core) wakeNow(ctx context.Context, es envScope, by string) error {
	env, err := c.store.GetEnvironment(ctx, es.env.ID)
	if err != nil {
		return err
	}
	if es.env = env; env.AsleepAt == nil {
		return nil
	}
	c.publishSleep(es, apitypes.SleepWaking)
	rows, err := c.store.ListEnvironmentServiceReleases(ctx, env.ID)
	if err != nil {
		return err
	}
	// Two levels: services with volumes (databases, which others connect
	// to on start) first, then the rest, each level side by side.
	type waking struct {
		ss  svcScope
		rel store.Release
		cfg releaseConfig
	}
	var levels [2][]waking
	for _, r := range rows {
		ss, err := c.scopeOf(ctx, r.ServiceID)
		if err != nil {
			return err
		}
		rel, err := c.store.GetRelease(ctx, r.ReleaseID)
		if err != nil {
			return err
		}
		cfg, err := c.openConfig(rel)
		if err != nil {
			return err
		}
		lvl := 1
		if len(cfg.Mounts) > 0 {
			lvl = 0
		}
		levels[lvl] = append(levels[lvl], waking{ss, rel, cfg})
	}
	rt := c.runtime(env.ServerID)
	for _, lvl := range levels {
		var wg sync.WaitGroup
		for _, w := range lvl {
			wg.Go(func() {
				err := c.resumeService(ctx, rt, w.ss, w.rel, w.cfg)
				if err != nil {
					c.log.Warn("wake: service didn't come back", "service", w.ss.String(), "err", err)
					c.releaseEvent(ctx, w.ss, w.rel, fmt.Sprintf("Waking %s: release %d didn't come back: %v", es.env.Name, w.rel.Number, err))
				}
			})
		}
		wg.Wait()
	}
	if _, err := c.store.SetEnvironmentAwake(ctx, store.SetEnvironmentAwakeParams{WokeAt: ptr(now()), WokeBy: by, ID: env.ID}); err != nil {
		return err
	}
	if es.env, err = c.store.GetEnvironment(ctx, env.ID); err != nil {
		return err
	}
	if err := c.applyRoutes(ctx, env.ServerID); err != nil {
		c.log.Error("apply routes after wake", "environment", es.String(), "err", err)
	}
	c.endOpState(es.env.ID)
	c.publishSleep(es, apitypes.SleepAwake)
	return nil
}

// endOpState marks an environment awake in memory before its wake's op
// ends, so the awake event shows it awake.
func (c *Core) endOpState(envID int64) {
	s := &c.sleep
	s.mu.Lock()
	if e := s.envs[envID]; e != nil {
		e.asleep = false
	}
	s.mu.Unlock()
}

// resumeService starts one service's stopped container again and waits
// for its release's health check, as a deploy's.
func (c *Core) resumeService(ctx context.Context, rt node.Runtime, ss svcScope, rel store.Release, cfg releaseConfig) error {
	if err := rt.Resume(ctx, rel.ContainerID); err != nil {
		c.setStatus(ss, apitypes.ServiceStatus{State: apitypes.ServiceNone})
		return err
	}
	ct, err := rt.Inspect(ctx, rel.ContainerID)
	if err == nil && rel.HostPort != 0 && ct.HostPort != int(rel.HostPort) {
		// Core pins every release's port; one that came back elsewhere
		// wouldn't get its route's requests.
		err = fmt.Errorf("its container came back on port %d instead of %d; redeploy it", ct.HostPort, rel.HostPort)
	}
	if err == nil {
		err = rt.HealthCheck(ctx, ct, healthCheckOf(cfg))
	}
	if err != nil {
		c.setStatus(ss, apitypes.ServiceStatus{State: apitypes.ServiceNone})
		return err
	}
	c.setStatus(ss, apitypes.ServiceStatus{State: apitypes.ServiceRunning})
	c.adoptHealth(ctx, ss, rel.ContainerID)
	return nil
}

// settleSleep converges a server's asleep environments to their target
// after a restart: a container of theirs that runs (a crash in the middle
// of a sleep or wake, a host reboot) is stopped again, and their services
// read asleep. The next request wakes them properly.
func (c *Core) settleSleep(ctx context.Context, serverID int64) {
	s := &c.sleep
	s.mu.Lock()
	var ids []int64
	for id, e := range s.envs {
		if e.asleep && e.serverID == serverID {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	rt := c.runtime(serverID)
	for _, id := range ids {
		rows, err := c.store.ListEnvironmentServiceReleases(ctx, id)
		if err != nil {
			c.log.Warn("sleep: settle", "environment", id, "err", err)
			continue
		}
		for _, r := range rows {
			ss, err := c.scopeOf(ctx, r.ServiceID)
			if err != nil {
				continue
			}
			if ct, err := rt.Inspect(ctx, r.ContainerID); err == nil && ct.State == node.StateRunning {
				if err := rt.Stop(ctx, r.ContainerID, stopTimeout); err != nil {
					c.log.Warn("sleep: stop a container of an asleep environment", "service", ss.String(), "err", err)
					continue
				}
			}
			c.setStatus(ss, apitypes.ServiceStatus{State: apitypes.ServiceAsleep})
		}
	}
}

// startSleep loads the sleep manager and settles the control plane's
// own server; called once at start, after reconcile.
func (c *Core) startSleep(ctx context.Context) error {
	if err := c.loadSleep(ctx); err != nil {
		return err
	}
	c.settleSleep(ctx, c.localID)
	return nil
}

// publishSleep tells open pages an environment's sleep state changed.
func (c *Core) publishSleep(es envScope, state apitypes.SleepState) {
	sl := c.envSleep(es.env)
	if sl == nil {
		sl = &apitypes.EnvironmentSleep{}
	}
	sl.State = state
	c.bus.publish(busEvent{orgID: es.project.OrgID, projectID: es.project.ID, envID: es.env.ID, ev: apitypes.Event{
		Type: apitypes.EventSleep, Time: time.Now().UTC(), Project: es.project.Name, Environment: es.env.Name, Sleep: sl,
	}})
}

// envSleep is an environment's sleep as the API shows it: nil for one
// that never sleeps on its own and isn't asleep.
func (c *Core) envSleep(e store.Environment) *apitypes.EnvironmentSleep {
	if e.SleepAfterMinutes == 0 && e.AsleepAt == nil {
		return nil
	}
	sl := &apitypes.EnvironmentSleep{AfterMinutes: int(e.SleepAfterMinutes), State: apitypes.SleepAwake, WokeBy: e.WokeBy}
	if e.AsleepAt != nil {
		sl.State, sl.Since, sl.SleptBy, sl.FreedBytes = apitypes.SleepAsleep, e.AsleepAt, e.SleptBy, e.FreedBytes
	} else {
		sl.Since = e.WokeAt
	}
	s := &c.sleep
	s.mu.Lock()
	if m := s.envs[e.ID]; m != nil {
		if op := m.op; op != nil {
			sl.State = apitypes.SleepGoing
			if op.waking {
				sl.State = apitypes.SleepWaking
			}
		}
		if !m.lastRequest.IsZero() {
			sl.LastRequestAt = ptr(m.lastRequest.UTC())
		}
		if sl.State == apitypes.SleepAwake && m.after > 0 && m.busy == 0 {
			sl.SleepsAt = ptr(m.lastActive.Add(m.after).Truncate(time.Minute).Add(time.Minute).UTC())
		}
	}
	s.mu.Unlock()
	switch {
	case e.Kind == string(apitypes.KindProduction):
		sl.Blocked = "production environments never sleep"
	case e.ServerID != c.localID:
		sl.Blocked = "it runs on another server, where environments can't sleep yet"
	case c.cfg.Caddy.TrafficListen == "":
		sl.Blocked = "traffic counting is off on its server ([caddy] traffic_listen), so it can't tell when it is idle"
	}
	return sl
}

// WakeAnswer is what the wake check tells Caddy about a request to an
// asleep environment.
type WakeAnswer int

// Wake answers.
const (
	// WakeGo: the environment is awake; the request goes on to the app.
	WakeGo WakeAnswer = iota
	// WakeWaking: it is still waking (or can't wake); a page load gets
	// the waking page, anything else a 503 to retry.
	WakeWaking
	// WakeAsleep: it stays asleep (an uptime probe); a 503.
	WakeAsleep
)

// WakeForRequest is the wake check (Route.Wake) for a request Caddy
// holds: key is the route's environment key, method and uri the
// request's. It wakes the environment (in the background, so a client
// that gives up doesn't stop it) and waits for it: up to wakeHoldPage for
// a page load, wakeHoldAPI for the rest. Dock's uptime probes never wake
// anything. A key that names no asleep environment lets the request go
// on: its route was re-rendered meanwhile.
func (c *Core) WakeForRequest(ctx context.Context, key, method, uri, userAgent string, pageLoad bool) WakeAnswer {
	envID, err := strconv.ParseInt(key, 10, 64)
	if err != nil || !c.isAsleep(envID) && !c.isWaking(envID) {
		return WakeGo
	}
	if strings.HasPrefix(userAgent, "dock-uptime/") {
		return WakeAsleep
	}
	s := &c.sleep
	s.mu.Lock()
	e := s.envs[envID]
	if e == nil || e.serverID != c.localID {
		s.mu.Unlock()
		return WakeGo
	}
	if e.waiting >= wakeMaxWaiting {
		s.mu.Unlock()
		return WakeWaking
	}
	e.waiting++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		e.waiting--
		s.mu.Unlock()
	}()
	done := make(chan error, 1)
	c.wg.Go(func() {
		env, err := c.store.GetEnvironment(c.ctx, envID)
		if err == nil {
			var p store.Project
			if p, err = c.store.GetProject(c.ctx, env.ProjectID); err == nil {
				err = c.wake(c.ctx, envScope{p, env}, traffic.Endpoint(method, uri))
			}
		}
		done <- err
	})
	hold := wakeHoldAPI
	if pageLoad {
		hold = wakeHoldPage
	}
	t := time.NewTimer(hold)
	defer t.Stop()
	select {
	case err := <-done:
		if err != nil {
			c.log.Warn("wake for a request", "environment", envID, "err", err)
			return WakeWaking
		}
		return WakeGo
	case <-t.C:
		return WakeWaking
	case <-ctx.Done():
		return WakeWaking
	}
}

// isWaking reports whether an environment's wake is in flight.
func (c *Core) isWaking(envID int64) bool {
	s := &c.sleep
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.envs[envID]
	return e != nil && e.op != nil && e.op.waking
}

// SleepEnvironment puts an environment to sleep now (deploy scope: it
// takes the environment away from whoever uses it). Production
// environments never sleep.
func (c *Core) SleepEnvironment(ctx context.Context, a Actor, ref EnvRef) (apitypes.Environment, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeDeploy, "putting an environment to sleep")
	if err != nil {
		return apitypes.Environment{}, err
	}
	err = c.putToSleep(ctx, es, a.Name)
	switch {
	case errors.Is(err, errAwakeAgain):
		// Asleep already: nothing changed.
	case err != nil && CodeOf(err) != "":
		return apitypes.Environment{}, err
	case err != nil:
		return apitypes.Environment{}, errf(apitypes.CodeConflict, "%s is asleep, but a container didn't stop: %v; it is stopped again when the daemon restarts, or put it to sleep again", es, err)
	default:
		c.audit(ctx, a, "environment.sleep", &es.project, &es.env, es.String(), "put to sleep")
	}
	if es.env, err = c.store.GetEnvironment(ctx, es.env.ID); err != nil {
		return apitypes.Environment{}, err
	}
	return c.toEnv(ctx, a, es)
}

// WakeEnvironment wakes an asleep environment and returns once it is
// awake. Read scope: anyone who can see it can wake it by opening its
// address anyway.
func (c *Core) WakeEnvironment(ctx context.Context, a Actor, ref EnvRef) (apitypes.Environment, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeRead, "waking an environment")
	if err != nil {
		return apitypes.Environment{}, err
	}
	was := es.env.AsleepAt != nil
	if err := c.wake(ctx, es, a.Name); err != nil {
		return apitypes.Environment{}, err
	}
	if was {
		c.audit(ctx, a, "environment.wake", &es.project, &es.env, es.String(), "woke it")
	}
	if es.env, err = c.store.GetEnvironment(ctx, es.env.ID); err != nil {
		return apitypes.Environment{}, err
	}
	return c.toEnv(ctx, a, es)
}

// SetEnvironmentSleep changes how long a non-production environment may
// go without requests before it sleeps (admin of the project); 0 turns
// sleeping on its own off. A preview's comes from its project's preview
// settings, and a change there overrides it.
func (c *Core) SetEnvironmentSleep(ctx context.Context, a Actor, ref EnvRef, req apitypes.SetSleepRequest) (apitypes.Environment, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeAdmin, "changing when an environment sleeps")
	if err != nil {
		return apitypes.Environment{}, err
	}
	if err := validSleepAfter(req.AfterMinutes); err != nil {
		return apitypes.Environment{}, err
	}
	if req.AfterMinutes > 0 {
		if err := c.sleepable(es); err != nil {
			return apitypes.Environment{}, err
		}
	}
	e, err := c.store.SetEnvironmentSleepAfter(ctx, store.SetEnvironmentSleepAfterParams{SleepAfterMinutes: int64(req.AfterMinutes), ID: es.env.ID})
	if err != nil {
		return apitypes.Environment{}, err
	}
	es.env = e
	c.reloadSleep(ctx)
	summary := fmt.Sprintf("sleeps after %d minutes without requests", req.AfterMinutes)
	if req.AfterMinutes == 0 {
		summary = "doesn't sleep on its own"
	}
	c.audit(ctx, a, "environment.sleep_settings", &es.project, &es.env, es.String(), summary)
	c.publishSleep(es, c.envSleepState(e))
	return c.toEnv(ctx, a, es)
}

// envSleepState is an environment's state for an event after a setting
// changed.
func (c *Core) envSleepState(e store.Environment) apitypes.SleepState {
	if sl := c.envSleep(e); sl != nil {
		return sl.State
	}
	return apitypes.SleepAwake
}

// validSleepAfter checks a sleep setting in minutes: 0, or within bounds.
func validSleepAfter(m int) error {
	if m != 0 && (m < apitypes.SleepAfterMin || m > apitypes.SleepAfterMax) {
		return invalid("an environment sleeps after %d to %d minutes without requests, or 0 for never; not %d", apitypes.SleepAfterMin, apitypes.SleepAfterMax, m)
	}
	return nil
}
