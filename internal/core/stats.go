package core

import (
	"context"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Resource statistics (ADR 0015). Once a minute the collector samples every
// online server and the containers of active releases (node.Stats reads
// /proc and cgroup files, nothing else), stores a 1-minute point per server
// and service in one transaction, and at each 10-minute boundary averages
// the last ten into a 10-minute point and prunes old points. It then
// publishes the samples as usage events, so clients patch what they show
// (current numbers and open charts) instead of polling. That tick is
// the daemon's only periodic work. While someone watches a server or a
// service live, a second sampler runs every liveInterval, and stops as
// soon as the last watcher leaves.

const (
	statsMinute   = 60
	statsTen      = 600
	statsKeep1m   = 24 * 60 * 60
	statsKeep10m  = 7 * 24 * 60 * 60
	liveBufferLen = 1
)

// liveInterval is how often watched servers and services are sampled.
var liveInterval = 3 * time.Second

// statsState is the collector's memory: the latest sample of each server
// and service (served with servers and services), and live watchers.
type statsState struct {
	mu      sync.Mutex
	server  map[int64]apitypes.ServerUsage
	service map[int64]apitypes.ServiceUsage

	// Live watchers, and whether the live sampler runs. closed stops new
	// samplers once Core is closing.
	watchers map[*watcher]struct{}
	stopLive chan struct{}
	closed   bool

	// firstTick closes after the start-up sample, which replaces whatever
	// usage was known before it.
	firstTick chan struct{}
}

func newStatsState() *statsState {
	return &statsState{
		server: map[int64]apitypes.ServerUsage{}, service: map[int64]apitypes.ServiceUsage{},
		watchers: map[*watcher]struct{}{}, firstTick: make(chan struct{}),
	}
}

func (s *statsState) serverUsage(id int64) (apitypes.ServerUsage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.server[id]
	return u, ok
}

func (s *statsState) serviceUsage(id int64) (apitypes.ServiceUsage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.service[id]
	return u, ok
}

// sampler turns cumulative samples into usage by keeping the previous
// sample of each server and container. Each sampler is used by one
// goroutine.
type sampler struct {
	host map[int64]node.HostStats
	// ctr holds this round's container samples, prev the last round's;
	// they swap after each round so containers that went away are dropped
	// without allocating.
	ctr, prev map[string]node.ContainerStats
}

func newSampler() *sampler {
	return &sampler{host: map[int64]node.HostStats{}, ctr: map[string]node.ContainerStats{}, prev: map[string]node.ContainerStats{}}
}

// hostUsage converts a host sample; ok is false for a server's first
// sample, which has no CPU use yet.
func (s *sampler) hostUsage(serverID int64, h node.HostStats, t time.Time) (u apitypes.ServerUsage, ok bool) {
	prev, ok := s.host[serverID]
	s.host[serverID] = h
	u = apitypes.ServerUsage{
		Time: t, CPUs: h.CPUs, MemoryTotalBytes: toInt64(h.MemoryTotal), Load1: h.Load1,
		MemoryUsedBytes: toInt64(h.MemoryTotal - min(h.MemoryAvailable, h.MemoryTotal)),
		Disks:           make([]apitypes.DiskUsage, len(h.Disks)),
	}
	for i, d := range h.Disks {
		u.Disks[i] = apitypes.DiskUsage{Paths: d.Paths, UsedBytes: toInt64(d.Used), TotalBytes: toInt64(d.Total)}
	}
	// The node reports Docker's data root first (node.HostStats.Disks).
	if len(u.Disks) > 0 {
		u.DiskUsedBytes, u.DiskTotalBytes = u.Disks[0].UsedBytes, u.Disks[0].TotalBytes
	}
	if !ok || h.CPUTotal <= prev.CPUTotal || h.CPUBusy < prev.CPUBusy {
		return u, false
	}
	u.CPUPercent = 100 * float64(h.CPUBusy-prev.CPUBusy) / float64(h.CPUTotal-prev.CPUTotal)
	return u, true
}

// containerUsage converts a container sample; ok is false for its first
// sample.
func (s *sampler) containerUsage(cs node.ContainerStats) (u apitypes.ServiceUsage, ok bool) {
	s.ctr[cs.ID] = cs
	u = apitypes.ServiceUsage{Time: cs.Time, MemoryBytes: toInt64(cs.Memory), MemoryLimitBytes: toInt64(cs.MemoryLimit)}
	prev, ok := s.prev[cs.ID]
	elapsed := cs.Time.Sub(prev.Time).Seconds()
	if !ok || elapsed <= 0 || cs.CPUUsec < prev.CPUUsec {
		return u, false
	}
	u.CPUPercent = 100 * float64(cs.CPUUsec-prev.CPUUsec) / (elapsed * 1e6)
	u.IOReadBytesPerSec = int64(float64(cs.IORead-min(prev.IORead, cs.IORead)) / elapsed)
	u.IOWriteBytesPerSec = int64(float64(cs.IOWrite-min(prev.IOWrite, cs.IOWrite)) / elapsed)
	return u, true
}

// endRound forgets containers that weren't sampled this round.
func (s *sampler) endRound() {
	s.ctr, s.prev = s.prev, s.ctr
	clear(s.ctr)
}

func toInt64(v uint64) int64 { return int64(min(v, math.MaxInt64)) }

// collector is the minute tick's state; only collectStats' goroutine uses it.
type collector struct {
	sampler *sampler
	// downsampled is the end of the last 10-minute window written.
	downsampled int64
	// Reused between ticks.
	samples []serverSample
	// own are the indexes of one server's targets; targetOf maps their
	// containers back.
	own      []int
	targetOf map[string]int
	servers  []store.AddServerStatsParams
	services []store.AddServiceStatsParams
	latest   map[int64]apitypes.ServiceUsage
	// events are the tick's usage events, published once its points are
	// stored.
	events []busEvent
	// names are the targets' names by service, for the events; reloaded
	// when the set of targets changes (names never do).
	names map[int64]store.ListStatsNamesRow
}

// serverSample is what one server answered in a tick.
type serverSample struct {
	srv      store.Server
	rt       node.Runtime
	ids      []string // its targets' containers
	host     node.HostStats
	hostErr  error
	stats    []node.ContainerStats
	statsErr error
}

// statsServerTimeout bounds sampling one server, so a server that doesn't
// answer can't delay the others' points (fanOut).
const statsServerTimeout = 15 * time.Second

// liveServerTimeout is statsServerTimeout for live samples, which a
// watcher wants every few seconds.
const liveServerTimeout = 5 * time.Second

// sample asks every server for its numbers at once (fanOut) and keeps
// the answers in col.samples.
func (c *Core) sample(ctx context.Context, col *collector, servers []store.Server, targets []store.ListStatsTargetsRow) {
	n := 0
	for _, srv := range servers {
		rt := c.connected(srv.ID)
		if rt == nil {
			continue
		}
		if n == len(col.samples) {
			col.samples = append(col.samples, serverSample{})
		}
		s := &col.samples[n]
		*s = serverSample{srv: srv, rt: rt, ids: s.ids[:0]}
		for _, t := range targets {
			if t.ServerID == srv.ID {
				s.ids = append(s.ids, t.ContainerID)
			}
		}
		n++
	}
	col.samples = col.samples[:n]
	fanOut(ctx, n, statsServerTimeout, func(ctx context.Context, i int) {
		s := &col.samples[i]
		s.host, s.hostErr = s.rt.HostStats(ctx)
		if len(s.ids) > 0 {
			s.stats, s.statsErr = s.rt.ContainerStats(ctx, s.ids)
		}
	})
}

func newCollector() *collector {
	return &collector{
		sampler: newSampler(), targetOf: map[string]int{}, latest: map[int64]apitypes.ServiceUsage{},
		names: map[int64]store.ListStatsNamesRow{},
	}
}

// loadNames makes sure names covers exactly the targets' services (one
// per target, as a service has one active release).
func (col *collector) loadNames(ctx context.Context, q *store.Queries, targets []store.ListStatsTargetsRow) error {
	known := len(col.names) == len(targets)
	for i := 0; known && i < len(targets); i++ {
		_, known = col.names[targets[i].ServiceID]
	}
	if known {
		return nil
	}
	rows, err := q.ListStatsNames(ctx)
	if err != nil {
		return err
	}
	clear(col.names)
	for _, r := range rows {
		col.names[r.ID] = r
	}
	return nil
}

// collectStats runs the minute tick until Core closes. The first sample is
// taken right away so the first tick already has CPU use.
func (c *Core) collectStats() {
	defer c.wg.Done()
	col := newCollector()
	c.statsTick(c.ctx, col, time.Now())
	close(c.stats.firstTick)
	t := time.NewTimer(untilNextMinute(time.Now()))
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case now := <-t.C:
			c.statsTick(c.ctx, col, now)
			t.Reset(untilNextMinute(time.Now()))
		}
	}
}

// untilNextMinute keeps ticks on minute boundaries without drifting.
func untilNextMinute(now time.Time) time.Duration {
	return now.Truncate(time.Minute).Add(time.Minute).Sub(now)
}

// statsTick samples every online server, stores the minute's points and
// publishes them as usage events: one per server (admins only) and one per
// environment with its services.
func (c *Core) statsTick(ctx context.Context, col *collector, now time.Time) {
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		c.log.Warn("stats: list servers", "err", err)
		return
	}
	targets, err := c.store.ListStatsTargets(ctx)
	if err != nil {
		c.log.Warn("stats: list containers", "err", err)
		return
	}
	if err := col.loadNames(ctx, c.store.Queries, targets); err != nil {
		c.log.Warn("stats: name containers", "err", err)
		return
	}
	// A point covers the minute before the tick and is stamped with its start.
	at := now.Truncate(time.Minute).Unix() - statsMinute
	col.servers, col.services = col.servers[:0], col.services[:0]
	clear(col.latest)
	hosts := map[int64]apitypes.ServerUsage{}
	var memory []store.SetServerMemoryParams
	c.sample(ctx, col, servers, targets)
	for i := range col.samples {
		s := &col.samples[i]
		srv, h := s.srv, s.host
		if s.hostErr != nil {
			c.log.Warn("stats: sample server", "server", srv.Name, "err", s.hostErr)
			continue
		}
		if mem := toInt64(h.MemoryTotal); mem != srv.MemoryBytes {
			memory = append(memory, store.SetServerMemoryParams{MemoryBytes: mem, ID: srv.ID, MemoryBytes_2: mem})
		}
		if u, ok := col.sampler.hostUsage(srv.ID, h, now); ok {
			hosts[srv.ID] = u
			col.servers = append(col.servers, store.AddServerStatsParams{
				Res: statsMinute, At: at, ServerID: srv.ID, Cpu: hundredths(u.CPUPercent),
				Memory: u.MemoryUsedBytes, Disk: u.DiskUsedBytes, Load1: hundredths(u.Load1),
			})
			col.events = append(col.events, busEvent{serverStats: true, ev: apitypes.Event{
				Type: apitypes.EventUsage, Time: now.UTC(), Usage: &apitypes.UsageUpdate{Server: srv.Name, Host: &u},
			}})
		}
		col.own = col.own[:0]
		clear(col.targetOf)
		for i, t := range targets {
			if t.ServerID == srv.ID {
				col.own = append(col.own, i)
				col.targetOf[t.ContainerID] = i
			}
		}
		if len(s.ids) == 0 {
			continue
		}
		if s.statsErr != nil {
			c.log.Warn("stats: sample containers", "server", srv.Name, "err", s.statsErr)
			continue
		}
		for _, cs := range s.stats {
			t, asked := col.targetOf[cs.ID]
			if !asked {
				continue // not one of this server's containers: ignore it
			}
			u, ok := col.sampler.containerUsage(cs)
			if !ok {
				continue
			}
			svc := targets[t].ServiceID
			col.latest[svc] = u
			col.services = append(col.services, store.AddServiceStatsParams{
				Res: statsMinute, At: at, ServiceID: svc, Cpu: hundredths(u.CPUPercent),
				Memory: u.MemoryBytes, IoRead: u.IOReadBytesPerSec, IoWrite: u.IOWriteBytesPerSec,
			})
		}
		col.serviceEvents(srv.Name, targets, now)
	}
	col.sampler.endRound()
	err = c.storeStats(ctx, col, now.Unix(), memory)
	if err != nil {
		c.log.Warn("stats: store", "err", err)
	}
	c.stats.mu.Lock()
	for id, u := range hosts {
		c.stats.server[id] = u
	}
	clear(c.stats.service)
	for id, u := range col.latest {
		c.stats.service[id] = u
	}
	c.stats.mu.Unlock()

	// Stored samples are history points too; open charts append them.
	var point *time.Time
	if err == nil {
		point = ptr(time.Unix(at, 0).UTC())
	}
	for _, e := range col.events {
		e.ev.Usage.Point = point
		c.bus.publish(e)
	}
	clear(col.events)
	col.events = col.events[:0]
}

// serviceEvents adds one usage event per environment of a server with the
// samples of its services. Targets are ordered by server and environment,
// so own (this server's targets) has each environment's services together.
func (col *collector) serviceEvents(server string, targets []store.ListStatsTargetsRow, now time.Time) {
	var up *apitypes.UsageUpdate
	var env int64
	for n, i := range col.own {
		t := targets[i]
		u, ok := col.latest[t.ServiceID]
		if !ok {
			continue
		}
		if up == nil || env != t.EnvironmentID {
			env = t.EnvironmentID
			size := 1
			for _, j := range col.own[n+1:] {
				if targets[j].EnvironmentID != t.EnvironmentID {
					break
				}
				size++
			}
			name := col.names[t.ServiceID]
			up = &apitypes.UsageUpdate{Server: server, Services: make([]apitypes.ServiceUsageSample, 0, size)}
			col.events = append(col.events, busEvent{projectID: t.ProjectID, envID: t.EnvironmentID, ev: apitypes.Event{
				Type: apitypes.EventUsage, Time: now.UTC(), Project: name.ProjectName, Environment: name.EnvironmentName, Usage: up,
			}})
		}
		up.Services = append(up.Services, apitypes.ServiceUsageSample{Service: col.names[t.ServiceID].ServiceName, Usage: u})
	}
}

// storeStats writes a tick's points in one transaction. When a 10-minute
// window has completed it also writes that window's averages and prunes
// points past their retention, all as range queries on the time-first keys.
func (c *Core) storeStats(ctx context.Context, col *collector, now int64, memory []store.SetServerMemoryParams) error {
	window := now - now%statsTen // end of the last complete 10-minute window
	downsample := window > col.downsampled
	if len(col.servers) == 0 && len(col.services) == 0 && len(memory) == 0 && !downsample {
		return nil
	}
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		for _, p := range memory {
			if err := q.SetServerMemory(ctx, p); err != nil {
				return err
			}
		}
		for _, p := range col.servers {
			if err := q.AddServerStats(ctx, p); err != nil {
				return err
			}
		}
		for _, p := range col.services {
			if err := q.AddServiceStats(ctx, p); err != nil {
				return err
			}
		}
		if !downsample {
			return nil
		}
		span := store.DownsampleServerStatsParams{FromAt: window - statsTen, ToAt: window}
		if err := q.DownsampleServerStats(ctx, span); err != nil {
			return err
		}
		if err := q.DownsampleServiceStats(ctx, store.DownsampleServiceStatsParams(span)); err != nil {
			return err
		}
		for _, p := range []store.PruneServerStatsParams{{Res: statsMinute, At: now - statsKeep1m}, {Res: statsTen, At: now - statsKeep10m}} {
			if err := q.PruneServerStats(ctx, p); err != nil {
				return err
			}
			if err := q.PruneServiceStats(ctx, store.PruneServiceStatsParams(p)); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil && downsample {
		col.downsampled = window
	}
	return err
}

func hundredths(v float64) int64 { return int64(math.Round(v * 100)) }

// statsSpan maps a range to its resolution and start.
func statsSpan(r apitypes.StatsRange, now time.Time) (res int64, since int64, err error) {
	switch r {
	case apitypes.Range1h, "":
		return statsMinute, now.Unix() - 3600, nil
	case apitypes.Range24h:
		return statsMinute, now.Unix() - statsKeep1m, nil
	case apitypes.Range7d:
		return statsTen, now.Unix() - statsKeep10m, nil
	}
	return 0, 0, invalid("range must be 1h, 24h or 7d")
}

// ServerStats returns a server's statistics history and latest sample.
func (c *Core) ServerStats(ctx context.Context, a Actor, name string, r apitypes.StatsRange) (apitypes.ServerStats, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "reading server statistics"); err != nil {
		return apitypes.ServerStats{}, err
	}
	srv, err := c.loadServer(ctx, name)
	if err != nil {
		return apitypes.ServerStats{}, err
	}
	res, since, err := statsSpan(r, time.Now())
	if err != nil {
		return apitypes.ServerStats{}, err
	}
	rows, err := c.store.ListServerStats(ctx, store.ListServerStatsParams{Res: res, At: since, ServerID: srv.ID})
	if err != nil {
		return apitypes.ServerStats{}, err
	}
	out := apitypes.ServerStats{Server: srv.Name, Range: rangeOr1h(r), ResolutionSeconds: int(res), Points: make([]apitypes.ServerStatsPoint, len(rows))}
	for i, p := range rows {
		out.Points[i] = apitypes.ServerStatsPoint{
			Time: time.Unix(p.At, 0).UTC(), CPUPercent: float64(p.Cpu) / 100, MemoryUsedBytes: p.Memory,
			DiskUsedBytes: p.Disk, Load1: float64(p.Load1) / 100,
		}
	}
	if u, ok := c.stats.serverUsage(srv.ID); ok {
		out.Current = &u
	}
	return out, nil
}

// ServiceStats returns a service's statistics history (across releases)
// and latest sample.
func (c *Core) ServiceStats(ctx context.Context, a Actor, ref ServiceRef, r apitypes.StatsRange) (apitypes.ServiceStats, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading service statistics")
	if err != nil {
		return apitypes.ServiceStats{}, err
	}
	res, since, err := statsSpan(r, time.Now())
	if err != nil {
		return apitypes.ServiceStats{}, err
	}
	rows, err := c.store.ListServiceStats(ctx, store.ListServiceStatsParams{Res: res, At: since, ServiceID: ss.svc.ID})
	if err != nil {
		return apitypes.ServiceStats{}, err
	}
	out := apitypes.ServiceStats{
		Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name,
		Range: rangeOr1h(r), ResolutionSeconds: int(res), Points: make([]apitypes.ServiceStatsPoint, len(rows)),
	}
	for i, p := range rows {
		out.Points[i] = apitypes.ServiceStatsPoint{
			Time: time.Unix(p.At, 0).UTC(), CPUPercent: float64(p.Cpu) / 100, MemoryBytes: p.Memory,
			IOReadBytesPerSec: p.IoRead, IOWriteBytesPerSec: p.IoWrite,
		}
	}
	if u, ok := c.stats.serviceUsage(ss.svc.ID); ok {
		out.Current = &u
	}
	return out, nil
}

func rangeOr1h(r apitypes.StatsRange) apitypes.StatsRange {
	if r == "" {
		return apitypes.Range1h
	}
	return r
}

// watcher is one live stats stream: a server's host or a service's
// container. Samples replace each other in the buffer, so a slow client
// gets the newest one rather than a backlog.
type watcher struct {
	server  int64 // the server sampled
	service int64 // 0 for a server watcher
	host    chan apitypes.ServerUsage
	ctr     chan apitypes.ServiceUsage
}

// watch registers w and starts the live sampler if it isn't running. The
// returned func unregisters it and stops the sampler when it was the last.
func (c *Core) watch(w *watcher) (unwatch func(), ok bool) {
	s := c.stats
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false
	}
	s.watchers[w] = struct{}{}
	if s.stopLive == nil {
		s.stopLive = make(chan struct{})
		c.wg.Add(1)
		go c.runLive(s.stopLive)
	}
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.watchers, w)
		if len(s.watchers) == 0 && s.stopLive != nil {
			close(s.stopLive)
			s.stopLive = nil
		}
	}, true
}

// closeStats stops new live samplers; Close waits for running ones.
func (c *Core) closeStats() {
	c.stats.mu.Lock()
	c.stats.closed = true
	c.stats.mu.Unlock()
}

// runLive samples watched servers and services every liveInterval until
// stop closes. It has its own sampler, so the minute points keep their
// one-minute deltas.
func (c *Core) runLive(stop <-chan struct{}) {
	defer c.wg.Done()
	smp := newSampler()
	t := time.NewTicker(liveInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-c.ctx.Done():
			return
		default:
		}
		c.liveRound(c.ctx, smp)
		select {
		case <-stop:
			return
		case <-c.ctx.Done():
			return
		case <-t.C:
		}
	}
}

// liveRound takes one sample of everything watched and hands it out.
func (c *Core) liveRound(ctx context.Context, smp *sampler) {
	c.stats.mu.Lock()
	ws := make([]*watcher, 0, len(c.stats.watchers))
	for w := range c.stats.watchers {
		ws = append(ws, w)
	}
	c.stats.mu.Unlock()

	type need struct {
		id   int64
		rt   node.Runtime
		host bool
		ids  []string
		// What the server answered.
		h      node.HostStats
		hostOK bool
		stats  []node.ContainerStats
	}
	servers := map[int64]*need{}
	containerOf := map[int64]string{} // by service
	for _, w := range ws {
		n := servers[w.server]
		if n == nil {
			n = &need{id: w.server}
			servers[w.server] = n
		}
		if w.service == 0 {
			n.host = true
			continue
		}
		if _, done := containerOf[w.service]; done {
			continue
		}
		rel, err := c.store.GetActiveRelease(ctx, w.service)
		if err != nil || rel.ContainerID == "" {
			containerOf[w.service] = ""
			continue
		}
		containerOf[w.service] = rel.ContainerID
		n.ids = append(n.ids, rel.ContainerID)
	}
	online := make([]*need, 0, len(servers))
	for _, n := range servers {
		if n.rt = c.connected(n.id); n.rt != nil {
			online = append(online, n)
		}
	}
	fanOut(ctx, len(online), liveServerTimeout, func(ctx context.Context, i int) {
		n := online[i]
		if n.host {
			var err error
			n.h, err = n.rt.HostStats(ctx)
			n.hostOK = err == nil
		}
		if len(n.ids) > 0 {
			n.stats, _ = n.rt.ContainerStats(ctx, n.ids)
		}
	})
	hosts := map[int64]apitypes.ServerUsage{}
	ctrs := map[string]apitypes.ServiceUsage{}
	now := time.Now()
	for _, n := range online {
		if n.hostOK {
			if u, ok := smp.hostUsage(n.id, n.h, now); ok {
				hosts[n.id] = u
			}
		}
		for _, cs := range n.stats {
			if !slices.Contains(n.ids, cs.ID) {
				continue // a server only answers for its own containers
			}
			if u, ok := smp.containerUsage(cs); ok {
				ctrs[cs.ID] = u
			}
		}
	}
	smp.endRound()
	for _, w := range ws {
		if w.service == 0 {
			if u, ok := hosts[w.server]; ok {
				offer(w.host, u)
			}
		} else if u, ok := ctrs[containerOf[w.service]]; ok {
			offer(w.ctr, u)
		}
	}
}

// offer puts v in a one-slot channel, replacing what the reader hasn't
// taken yet.
func offer[T any](ch chan T, v T) {
	for {
		select {
		case ch <- v:
			return
		default:
		}
		select {
		case <-ch:
		default:
		}
	}
}

// ServerStatsLive streams a server's usage every few seconds while the
// client watches; the first event is the latest minute sample, if any.
func (c *Core) ServerStatsLive(ctx context.Context, a Actor, name string) (Stream[apitypes.ServerUsage], error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "reading server statistics"); err != nil {
		return nil, err
	}
	srv, err := c.loadServer(ctx, name)
	if err != nil {
		return nil, err
	}
	if _, err := c.runtimeFor(srv.ID); err != nil {
		return nil, err
	}
	return func(ctx context.Context, ready func(), send func(...apitypes.ServerUsage) error) error {
		w := &watcher{server: srv.ID, host: make(chan apitypes.ServerUsage, liveBufferLen)}
		unwatch, ok := c.watch(w)
		if !ok {
			return nil
		}
		defer unwatch()
		ready()
		if u, ok := c.stats.serverUsage(srv.ID); ok {
			if err := send(u); err != nil {
				return err
			}
		}
		return relay(ctx, c.ctx, w.host, send)
	}, nil
}

// ServiceStatsLive streams a service's usage every few seconds while the
// client watches; the first event is the latest minute sample, if any.
// Nothing is sent while the service has no running container.
func (c *Core) ServiceStatsLive(ctx context.Context, a Actor, ref ServiceRef) (Stream[apitypes.ServiceUsage], error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading service statistics")
	if err != nil {
		return nil, err
	}
	if _, err := c.runtimeFor(ss.env.ServerID); err != nil {
		return nil, err
	}
	return func(ctx context.Context, ready func(), send func(...apitypes.ServiceUsage) error) error {
		w := &watcher{server: ss.env.ServerID, service: ss.svc.ID, ctr: make(chan apitypes.ServiceUsage, liveBufferLen)}
		unwatch, ok := c.watch(w)
		if !ok {
			return nil
		}
		defer unwatch()
		ready()
		if u, ok := c.stats.serviceUsage(ss.svc.ID); ok {
			if err := send(u); err != nil {
				return err
			}
		}
		return relay(ctx, c.ctx, w.ctr, send)
	}, nil
}

// relay sends samples from ch until the client or Core goes away.
func relay[T any](ctx, core context.Context, ch <-chan T, send func(...T) error) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-core.Done():
			return nil
		case v := <-ch:
			if err := send(v); err != nil {
				return err
			}
		}
	}
}
