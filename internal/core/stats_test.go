package core

import (
	"context"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

func TestSamplerRates(t *testing.T) {
	s := newSampler()
	t0 := time.Date(2031, 1, 1, 10, 0, 0, 0, time.UTC)
	h := node.HostStats{CPUBusy: 1000, CPUTotal: 4000, CPUs: 4, MemoryTotal: 8 << 30, MemoryAvailable: 6 << 30,
		Disks: []node.Disk{{Paths: []string{"/data"}, Total: 100, Used: 40}}}
	if u, ok := s.hostUsage(1, h, t0); ok || u.MemoryUsedBytes != 2<<30 || u.Disks[0].UsedBytes != 40 {
		t.Fatalf("first host sample = %+v, %v; want memory and disk but no CPU", u, ok)
	}
	h.CPUBusy, h.CPUTotal = 1300, 5000 // 300 of 1000 ticks busy
	if u, ok := s.hostUsage(1, h, t0.Add(time.Minute)); !ok || u.CPUPercent != 30 {
		t.Fatalf("host cpu = %v, %v", u.CPUPercent, ok)
	}

	c := node.ContainerStats{ID: "c1", Time: t0, CPUUsec: 5e6, Memory: 100 << 20, MemoryLimit: 512 << 20, IORead: 1000, IOWrite: 0}
	if _, ok := s.containerUsage(c); ok {
		t.Fatal("first container sample has a rate")
	}
	s.endRound()
	c.Time, c.CPUUsec, c.IORead, c.IOWrite = t0.Add(time.Minute), 5e6+90e6, 7000, 60000 // 1.5 cores
	u, ok := s.containerUsage(c)
	if !ok || u.CPUPercent != 150 || u.IOReadBytesPerSec != 100 || u.IOWriteBytesPerSec != 1000 || u.MemoryLimitBytes != 512<<20 {
		t.Fatalf("container usage = %+v, %v", u, ok)
	}
	s.endRound()
	// A container missing from a round is forgotten: its next sample
	// starts over instead of spanning the gap.
	s.endRound()
	c.Time = t0.Add(3 * time.Minute)
	if _, ok := s.containerUsage(c); ok {
		t.Fatal("rate across a gap")
	}
	// A restarted container's counters go down: no rate that round.
	s.endRound()
	c.Time, c.CPUUsec = t0.Add(4*time.Minute), 10
	if _, ok := s.containerUsage(c); ok {
		t.Fatal("rate across a counter reset")
	}
}

func (h *harness) serviceID(ref ServiceRef) int64 {
	h.t.Helper()
	ss, err := h.c.serviceFor(h.ctx, System, ref, apitypes.ScopeRead, "testing")
	if err != nil {
		h.t.Fatal(err)
	}
	return ss.svc.ID
}

// activeContainer is the service ID and its active container ID.
func (h *harness) activeContainer(ref ServiceRef) (svcID int64, id string) {
	h.t.Helper()
	svcID = h.serviceID(ref)
	rel, err := h.store.GetActiveRelease(h.ctx, svcID)
	if err != nil {
		h.t.Fatal(err)
	}
	return svcID, rel.ContainerID
}

// The minute tick stores a point per server and service, averages each
// complete 10-minute window into one point, and prunes past retention.
// Times are in the future so the harness's own collector never touches them.
func TestStatsTickStoresDownsamplesAndPrunes(t *testing.T) {
	h := newHarness(t)
	ref := h.service("web", apitypes.CreateServiceRequest{Image: "app:1", MemoryMB: 256})
	h.deploy(ref, "app:1")
	svc, ctr := h.activeContainer(ref)
	local := h.c.local.ID

	base := time.Date(2031, 1, 1, 10, 0, 0, 0, time.UTC)
	col := newCollector()
	tick := func(i int, at time.Time) {
		// Each minute: half of the host's CPU, half a core and 100 B/s of
		// reads in the container, memory growing by 1 MiB.
		n := uint64(i)
		h.rt.SetHostStats(node.HostStats{CPUs: 4, CPUBusy: 50 * n, CPUTotal: 100 * n, MemoryTotal: 8 << 30, MemoryAvailable: 6 << 30})
		h.rt.SetContainerStats(node.ContainerStats{
			ID: ctr, Time: at, CPUUsec: 30e6 * n, Memory: (100 + n) << 20, MemoryLimit: 256 << 20, IORead: 6000 * n,
		})
		h.c.statsTick(h.ctx, col, at)
	}
	for i := 0; i <= 10; i++ {
		tick(i, base.Add(time.Duration(i)*time.Minute))
	}

	points, err := h.store.ListServiceStats(h.ctx, store.ListServiceStatsParams{Res: statsMinute, At: base.Unix(), ServiceID: svc})
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 10 || points[0].At != base.Unix() || points[9].At != base.Add(9*time.Minute).Unix() {
		t.Fatalf("minute points = %+v", points)
	}
	if p := points[0]; p.Cpu != 5000 || p.Memory != 101<<20 || p.IoRead != 100 {
		t.Fatalf("first point = %+v", p)
	}
	ten, _ := h.store.ListServiceStats(h.ctx, store.ListServiceStatsParams{Res: statsTen, At: base.Unix(), ServiceID: svc})
	if len(ten) != 1 || ten[0].At != base.Unix() || ten[0].Cpu != 5000 || ten[0].Memory != (101<<20+110<<20)/2 {
		t.Fatalf("10-minute points = %+v", ten)
	}
	hosts, _ := h.store.ListServerStats(h.ctx, store.ListServerStatsParams{Res: statsTen, At: base.Unix(), ServerID: local})
	if len(hosts) != 1 || hosts[0].Cpu != 5000 || hosts[0].Memory != 2<<30 {
		t.Fatalf("server 10-minute points = %+v", hosts)
	}

	// The tick keeps each service's latest sample, which is served with the
	// service. (Checked on the collector, because the harness's own
	// collector may replace the shared cache at a minute boundary.)
	u, ok := col.latest[svc]
	if !ok || u.MemoryBytes != 110<<20 || u.MemoryLimitBytes != 256<<20 || u.CPUPercent != 50 {
		t.Fatalf("latest usage = %+v, %v", u, ok)
	}
	h.c.stats.mu.Lock()
	h.c.stats.service[svc] = u
	h.c.stats.mu.Unlock()
	if got, err := h.c.GetService(h.ctx, System, ref); err != nil || got.Usage == nil || *got.Usage != u {
		t.Fatalf("service usage = %+v, %v", got.Usage, err)
	}

	// A day later the minute points are gone; the 10-minute one stays a
	// week.
	day := base.Add(24*time.Hour + 10*time.Minute)
	tick(11, day)
	if points, _ := h.store.ListServiceStats(h.ctx, store.ListServiceStatsParams{Res: statsMinute, At: 0, ServiceID: svc}); len(points) != 1 || points[0].At != day.Unix()-60 {
		t.Fatalf("minute points after a day: %+v (only the new one should be left)", points)
	}
	if ten, _ := h.store.ListServiceStats(h.ctx, store.ListServiceStatsParams{Res: statsTen, At: 0, ServiceID: svc}); len(ten) == 0 || ten[0].At != base.Unix() {
		t.Fatalf("10-minute points after a day: %+v", ten)
	}
	tick(12, base.Add(7*24*time.Hour+10*time.Minute))
	if ten, _ := h.store.ListServiceStats(h.ctx, store.ListServiceStatsParams{Res: statsTen, At: 0, ServiceID: svc}); len(ten) == 0 || ten[0].At == base.Unix() {
		t.Fatalf("10-minute points after a week: %+v (the first window should be pruned)", ten)
	}
}

func TestServiceStatsHistory(t *testing.T) {
	h := newHarness(t)
	ref := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	svc := h.serviceID(ref)
	now := time.Now().Truncate(time.Minute)
	for i := range 3 {
		if err := h.store.AddServiceStats(h.ctx, store.AddServiceStatsParams{
			Res: statsMinute, At: now.Add(time.Duration(i-3) * time.Minute).Unix(), ServiceID: svc, Cpu: 1234, Memory: 42,
		}); err != nil {
			t.Fatal(err)
		}
	}
	st, err := h.c.ServiceStats(h.ctx, Actor{Kind: "token", Name: "r", Scope: apitypes.ScopeRead}, ref, "")
	if err != nil || st.Range != apitypes.Range1h || st.ResolutionSeconds != 60 || len(st.Points) != 3 || st.Points[0].CPUPercent != 12.34 {
		t.Fatalf("stats = %+v, %v", st, err)
	}
	if _, err := h.c.ServiceStats(h.ctx, System, ref, "2d"); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("bad range: %v", err)
	}
	other := Actor{Kind: "token", Name: "blog", Scope: apitypes.ScopeAdmin, ProjectID: 99, ProjectName: "blog"}
	if _, err := h.c.ServiceStats(h.ctx, other, ref, ""); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("other project's token: %v", err)
	}
}

// Live sampling runs only while someone watches and stops when the last
// watcher leaves.
func TestLiveStatsStopWhenUnwatched(t *testing.T) {
	old := liveInterval
	liveInterval = 10 * time.Millisecond
	t.Cleanup(func() { liveInterval = old })
	h := newHarness(t)
	ref := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(ref, "app:1")

	stream, err := h.c.ServiceStatsLive(h.ctx, System, ref)
	if err != nil {
		t.Fatal(err)
	}
	host, err := h.c.ServerStatsLive(h.ctx, System, h.c.local.Name)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(h.ctx)
	samples := make(chan apitypes.ServiceUsage, 100)
	hostSamples := make(chan apitypes.ServerUsage, 100)
	done := make(chan struct{}, 2)
	go func() {
		_ = stream(ctx, func() {}, func(us ...apitypes.ServiceUsage) error {
			for _, u := range us {
				samples <- u
			}
			return nil
		})
		done <- struct{}{}
	}()
	go func() {
		_ = host(ctx, func() {}, func(us ...apitypes.ServerUsage) error {
			for _, u := range us {
				hostSamples <- u
			}
			return nil
		})
		done <- struct{}{}
	}()
	for range 2 {
		select {
		case u := <-samples:
			if u.MemoryBytes != 64<<20 {
				t.Fatalf("live sample = %+v", u)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no live service samples")
		}
	}
	select {
	case u := <-hostSamples:
		if u.CPUPercent != 25 || u.CPUs != 4 {
			t.Fatalf("live host sample = %+v", u)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no live host samples")
	}

	cancel()
	<-done
	<-done
	h.c.stats.mu.Lock()
	running := h.c.stats.stopLive != nil || len(h.c.stats.watchers) != 0
	h.c.stats.mu.Unlock()
	if running {
		t.Fatal("live sampler still registered after the last watcher left")
	}
	before := h.rt.StatsCalls()
	time.Sleep(20 * liveInterval)
	// The minute collector may tick meanwhile (two calls); live sampling
	// would have made dozens.
	if n := h.rt.StatsCalls() - before; n > 2 {
		t.Fatalf("%d stats calls after the last watcher left", n)
	}
}
