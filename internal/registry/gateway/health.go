package gateway

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/s3"
)

// Store states (Status.State).
const (
	Healthy  = "healthy"
	Degraded = "degraded"
	Failing  = "failing"
)

// windowSize is how many recent bucket calls health looks at (within
// Timing.Window). degradedAt of them failing or slow make a healthy store
// degraded; the newest failingAt all bad make it failing. An unhealthy
// store stays degraded until the newest recoverAt calls were all good,
// and the window starts over then.
const (
	windowSize = 64
	degradedAt = 4
	failingAt  = 8
	recoverAt  = 8
)

// outcome is one bucket call's result.
type outcome struct {
	at  time.Time
	dur time.Duration
	bad bool
}

// health is the window of recent outcomes, guarded by Gateway.mu.
type health struct {
	ring    [windowSize]outcome
	n, next int
	lastErr string
	since   time.Time
	state   string
	// failed: calls made the store unhealthy, until recoverAt good ones.
	failed bool

	probe      *time.Timer
	probeDelay time.Duration
}

// record adds a bucket call's outcome: an error other than a missing key
// or range is bad, and so is a call slower than Timing.Slow.
func (g *Gateway) record(start time.Time, err error, stalled bool) {
	now := g.now()
	dur := now.Sub(start)
	bad := stalled || dur > g.t.Slow
	if err != nil && !s3.IsNotFound(err) && !isRangeError(err) {
		bad = true
	}
	g.mu.Lock()
	h := &g.health
	h.ring[h.next] = outcome{at: now, dur: dur, bad: bad}
	h.next = (h.next + 1) % windowSize
	h.n = min(h.n+1, windowSize)
	switch {
	case stalled:
		h.lastErr = fmt.Sprintf("the bucket didn't answer within %s", dur.Round(time.Second))
	case err != nil && bad:
		h.lastErr = err.Error()
	}
	if !bad {
		g.retryNowLocked() // the bucket answers again: don't wait out the backoffs
	}
	g.mu.Unlock()
	g.notify()
}

func isRangeError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "InvalidRange")
}

// StuckBlob is a blob the bucket keeps timing out on.
type StuckBlob struct {
	Key   string
	Since time.Time
}

// Status is the gateway's view of the bucket and its spool.
type Status struct {
	// State is Healthy, Degraded or Failing, since Since.
	State string
	Since time.Time
	// Calls and Bad are the recent bucket calls and how many failed or
	// were slow; Slowest the slowest; LastError the newest failure.
	Calls, Bad int
	Slowest    time.Duration
	LastError  string
	// Waiting objects (WaitingBytes) are only on this server's disk until
	// they upload; OldestWaiting is the earliest's time. Uploading are
	// being sent now.
	Waiting       int
	WaitingBytes  int64
	OldestWaiting time.Time
	Uploading     int
	// SpoolBytes is what the spool holds (with unfinished uploads), of
	// SpoolMax; its disk has DiskFree of DiskTotal free, and writes stop
	// before less than MinFree of it would be.
	SpoolBytes, SpoolMax int64
	DiskFree, DiskTotal  int64
	MinFree              float64
	// Refusing is why writes are refused: "spool" (at SpoolMax) or "disk"
	// (the disk is nearly full); "" when they aren't.
	Refusing      string
	RefusingSince time.Time
	Stuck         []StuckBlob
	// Reported: OnChange was told a state (Status's own may be newer).
	Reported bool
}

// Status reports the bucket's health and the spool.
func (g *Gateway) Status() Status {
	free, total, _ := diskStat(g.sp.dir)
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.statusLocked(g.now())
	st.DiskFree, st.DiskTotal, st.MinFree = free, total, g.sp.minFree
	st.Reported = g.reported != stateKey{}
	return st
}

func (g *Gateway) statusLocked(now time.Time) Status {
	st := Status{SpoolBytes: g.sp.used, SpoolMax: g.sp.max, Refusing: g.sp.refusing, RefusingSince: g.sp.refusingSince}
	for _, e := range g.sp.idx {
		if e.local {
			continue
		}
		st.Waiting++
		st.WaitingBytes += e.size
		if st.OldestWaiting.IsZero() || e.queued.Before(st.OldestWaiting) {
			st.OldestWaiting = e.queued
		}
		if e.busy != nil {
			st.Uploading++
		}
	}
	h := &g.health
	// The newest calls in a row that were bad, or good.
	badRun, goodRun, inRun := 0, 0, true
	for i := range h.n {
		o := h.ring[(h.next-1-i+windowSize)%windowSize]
		if now.Sub(o.at) > g.t.Window {
			break
		}
		st.Calls++
		st.Slowest = max(st.Slowest, o.dur)
		if o.bad {
			st.Bad++
		}
		switch {
		case !inRun:
		case o.bad && goodRun == 0:
			badRun++
		case !o.bad && badRun == 0:
			goodRun++
		default:
			inRun = false
		}
	}
	if st.Bad > 0 {
		st.LastError = h.lastErr
	}
	// Failed calls stick until recoverAt good ones; a long wait to upload
	// counts only while it lasts.
	switch {
	case badRun >= failingAt:
		st.State = Failing
	case h.failed && goodRun >= recoverAt:
		st.State = Healthy
		h.n, h.next = 0, 0 // the failures before don't count any more
	case h.failed, st.Bad >= degradedAt:
		st.State = Degraded
	default:
		st.State = Healthy
	}
	h.failed = st.State != Healthy
	if st.State == Healthy && st.Waiting > 0 && now.Sub(st.OldestWaiting) > g.t.Waiting {
		st.State = Degraded
	}
	for k, t := range g.stuck {
		if now.Sub(t) <= g.t.StuckFor {
			st.Stuck = append(st.Stuck, StuckBlob{Key: k, Since: t})
		}
	}
	slices.SortFunc(st.Stuck, func(a, b StuckBlob) int { return a.Since.Compare(b.Since) })
	if st.State != h.state {
		h.state, h.since = st.State, now
	}
	st.Since = h.since
	return st
}

// stateKey is what OnChange reports changes of.
type stateKey struct{ state, refusing string }

// notify tells OnChange when the state or refusal changed, and keeps the
// probe going while the store is unhealthy and nothing else talks to it.
func (g *Gateway) notify() {
	g.mu.Lock()
	st := g.statusLocked(g.now())
	k := stateKey{st.State, st.Refusing}
	// Healthy is reported first only on evidence: recoverAt calls, none
	// bad. Until then a new daemon has nothing to say (an alert the last
	// one opened stays open until it does).
	evidence := k != stateKey{state: Healthy} || st.Calls >= recoverAt && st.Bad == 0
	changed := k != g.reported && (g.reported != stateKey{} || evidence)
	if changed {
		g.reported = k
	}
	st.Reported = g.reported != stateKey{}
	if changed {
		g.log.Warn("registry gateway: storage state changed", "state", st.State, "refusing", st.Refusing, "bad", st.Bad, "calls", st.Calls, "waiting", st.Waiting, "last_error", st.LastError)
	}
	g.scheduleProbeLocked(st)
	g.mu.Unlock()
	if changed && g.opt.OnChange != nil {
		go g.opt.OnChange(st)
	}
}

// scheduleProbeLocked starts the probe's timer when the store is unhealthy
// and idle (no upload runs, queues or waits a retry; those tell anyway).
// It backs off from ProbeMin to ProbeMax and stops once healthy.
func (g *Gateway) scheduleProbeLocked(st Status) {
	h := &g.health
	idle := g.up.running == 0 && len(g.up.queue) == 0 && len(g.up.timers) == 0
	if st.State == Healthy {
		h.probeDelay = 0
		return
	}
	if !idle || h.probe != nil || g.bucket == nil || g.up.closed {
		return
	}
	h.probeDelay = min(max(h.probeDelay*2, g.t.ProbeMin), g.t.ProbeMax)
	c := g.bucket
	h.probe = time.AfterFunc(h.probeDelay, func() {
		ctx, cancel := context.WithTimeout(context.Background(), g.t.MetaTimeout)
		start := g.now()
		_, err := c.ListPage(ctx, s3.ListInput{Max: 1})
		g.record(start, err, ctx.Err() != nil)
		cancel()
		g.mu.Lock()
		h.probe = nil
		g.mu.Unlock()
		g.notify()
	})
}

// stopProbeLocked stops the probe (Close).
func (h *health) stopProbeLocked() {
	if h.probe != nil {
		h.probe.Stop()
		h.probe = nil
	}
}
