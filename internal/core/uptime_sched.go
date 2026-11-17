package core

import (
	"cmp"
	"container/heap"
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/access"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

// uptimeEntry is a check in the scheduler's heap.
type uptimeEntry struct {
	chk store.ListUptimeScheduleRow
	// accessKey is the sealed access key of the check's service when the
	// check's host is one of its domains behind a Dock login: its probes
	// run on the control plane and carry a session (probeCookie).
	accessKey []byte
	interval  time.Duration
	due       time.Time
}

// uptimeHeap orders checks by when they are due (container/heap).
type uptimeHeap []*uptimeEntry

func (h uptimeHeap) Len() int           { return len(h) }
func (h uptimeHeap) Less(i, j int) bool { return h[i].due.Before(h[j].due) }
func (h uptimeHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *uptimeHeap) Push(x any)        { *h = append(*h, x.(*uptimeEntry)) }
func (h *uptimeHeap) Pop() any {
	old := *h
	e := old[len(old)-1]
	*h = old[:len(old)-1]
	return e
}

// scheduleUptime is the uptime scheduler (ADR 0055): one goroutine and
// one timer, armed for the next due check only while checks exist. With
// none it waits on its kick channel, without a timer or a bus
// subscription, so an install without checks has no wake-ups. A kick (a check added or removed, a service archived) or a
// server event (one joined, connected, left) reloads the checks and
// servers. A due check is probed in its own goroutine (at most 8 at
// once and 2 per organization, uptimeState.inFlight; past that it is
// skipped that round) and is next due an interval after it was due,
// so a slow probe doesn't drift its schedule; a check whose probe still
// runs when it is due again is skipped that once.
func (c *Core) scheduleUptime() {
	defer c.wg.Done()
	// Server events matter only while there are checks to place, so the
	// subscription exists only then (subscribeServers).
	var sub *subscription
	defer func() {
		if sub != nil {
			sub.close()
		}
	}()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	var h uptimeHeap
	var servers []store.Server
	reload := true
	for {
		if reload {
			reload = false
			if nh, ns, err := c.loadUptime(c.ctx, h, time.Now()); err != nil {
				c.log.Error("uptime: load checks", "err", err)
			} else {
				h, servers = nh, ns
			}
		}
		now := time.Now()
		for len(h) > 0 && !h[0].due.After(now) {
			e := h[0]
			c.startProbe(e, servers)
			e.due = e.due.Add(e.interval)
			if !e.due.After(now) {
				// Probes were missed (the machine slept): keep the phase.
				e.due = e.due.Add((now.Sub(e.due)/e.interval + 1) * e.interval)
			}
			heap.Fix(&h, 0)
		}
		switch {
		case len(h) > 0 && sub == nil:
			sub = c.bus.subscribe(func(e busEvent) bool { return e.ev.Type == apitypes.EventServer })
		case len(h) == 0 && sub != nil:
			sub.close()
			sub = nil
		}
		var wake <-chan time.Time
		var serverEvents <-chan LiveEvent
		if len(h) > 0 {
			timer.Reset(time.Until(h[0].due))
			wake = timer.C
			serverEvents = sub.ch
		}
		select {
		case <-c.ctx.Done():
			timer.Stop()
			return
		case <-c.uptime.kick:
			timer.Stop()
			reload = true
		case <-serverEvents:
			sub.takeLagged()
			timer.Stop()
			reload = true
		case <-wake:
		}
	}
}

// loadUptime reads the checks to probe and the servers, keeping the due
// times of checks in old whose interval didn't change. A new check is
// first due within its first interval, spread by its ID, so checks added
// together (or every check, after a restart) don't fire together. The
// state of checks that went is dropped and their open alerts resolved.
// Three queries however many checks.
func (c *Core) loadUptime(ctx context.Context, old uptimeHeap, now time.Time) (uptimeHeap, []store.Server, error) {
	rows, err := c.store.ListUptimeSchedule(ctx)
	if err != nil {
		return nil, nil, err
	}
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return nil, nil, err
	}
	slices.SortFunc(servers, func(a, b store.Server) int { return cmp.Compare(a.ID, b.ID) })
	var login map[string]store.ListLoginDomainsRow
	if len(rows) > 0 {
		domains, err := c.store.ListLoginDomains(ctx)
		if err != nil {
			return nil, nil, err
		}
		login = make(map[string]store.ListLoginDomainsRow, len(domains))
		for _, d := range domains {
			login[d.Hostname] = d
		}
	}
	prev := make(map[int64]*uptimeEntry, len(old))
	for _, e := range old {
		prev[e.chk.ID] = e
	}
	h := make(uptimeHeap, 0, len(rows))
	live := make(map[int64]bool, len(rows))
	for _, r := range rows {
		e := &uptimeEntry{chk: r, interval: time.Duration(r.IntervalS) * c.uptime.second}
		// Only for a domain of the check's own service, never by hostname
		// alone: a session must not go to anyone else's site.
		if u, err := url.Parse(r.Url); err == nil {
			if d, ok := login[u.Hostname()]; ok && d.ServiceID == r.ServiceID {
				e.accessKey = d.Key
			}
		}
		if p, ok := prev[r.ID]; ok && p.interval == e.interval {
			e.due = p.due
		} else {
			e.due = now.Add(uptimeSpread(r.ID, e.interval))
		}
		h = append(h, e)
		live[r.ID] = true
	}
	heap.Init(&h)

	u := &c.uptime
	u.mu.Lock()
	for id := range u.checks {
		if !live[id] {
			delete(u.checks, id)
		}
	}
	for _, e := range h {
		st := u.checks[e.chk.ID]
		if st == nil {
			st = &checkState{state: apitypes.UptimeUnknown}
			u.checks[e.chk.ID] = st
		}
		st.login = e.accessKey != nil
	}
	u.mu.Unlock()

	var stale []string
	c.alerts.mu.Lock()
	for key, a := range c.alerts.open {
		id, err := strconv.ParseInt(strings.TrimPrefix(key, "uptime:"), 10, 64)
		if a.Kind == string(apitypes.AlertUptimeDown) && (err != nil || !live[id]) {
			stale = append(stale, key)
		}
	}
	c.alerts.mu.Unlock()
	for _, key := range stale {
		c.signalAlert(ctx, alertSignal{Key: key, Resolve: true})
	}
	return h, servers, nil
}

// uptimeSpread is how long after a reload a new check is first probed:
// a hash of its ID within its interval.
func uptimeSpread(id int64, interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	return time.Duration(uint64(id) * 0x9E3779B97F4A7C15 % uint64(interval))
}

// startProbe probes a due check in the background, unless its previous
// probe still runs.
func (c *Core) startProbe(e *uptimeEntry, servers []store.Server) {
	chk := e.chk
	u := &c.uptime
	u.mu.Lock()
	st := u.checks[chk.ID]
	if st == nil || st.running || u.inFlight >= uptimeMaxProbes || u.orgInFlight[chk.OrgID] >= uptimeOrgProbes || c.isAsleep(chk.EnvironmentID) {
		// Still running, or no slot: skipped this round, never queued.
		// A check of an asleep environment is paused, not down (sleep.go).
		u.mu.Unlock()
		return
	}
	st.running = true
	u.inFlight++
	u.orgInFlight[chk.OrgID]++
	u.mu.Unlock()
	srv := c.uptimeProber(servers, chk.ID, chk.ServerID, e.accessKey != nil)
	c.wg.Add(1)
	go c.runProbe(chk, srv, e.accessKey)
}

// probeCookie is the session a probe of chk carries through its
// service's Dock login (ADR 0030, "Uptime probes"), made anew for each
// probe: bound to the URL's host, a domain of that service (loadUptime),
// and valid for a minute. Opening the key and signing take microseconds.
func (c *Core) probeCookie(chk store.ListUptimeScheduleRow, sealed []byte) (string, error) {
	u, err := url.Parse(chk.Url)
	if err != nil {
		return "", err
	}
	key, err := c.box.Open(sealed, accessKeyAAD(chk.ServiceID))
	if err != nil {
		return "", fmt.Errorf("open access key of service %d: %w", chk.ServiceID, err)
	}
	return access.ProbeCookie(key, u.Hostname(), time.Now()), nil
}

// runProbe probes a check on srv and records the result. When srv can't
// probe (it went offline since it was chosen), the control plane does.
// With accessKey (uptimeEntry.accessKey) srv is the control plane's own
// server and the probe carries a session: it never crosses the node link.
func (c *Core) runProbe(chk store.ListUptimeScheduleRow, srv store.Server, accessKey []byte) {
	defer c.wg.Done()
	defer func() {
		u := &c.uptime
		u.mu.Lock()
		if st := u.checks[chk.ID]; st != nil {
			st.running = false
		}
		u.inFlight--
		if u.orgInFlight[chk.OrgID]--; u.orgInFlight[chk.OrgID] <= 0 {
			delete(u.orgInFlight, chk.OrgID)
		}
		u.mu.Unlock()
	}()
	timeout := time.Duration(chk.TimeoutMs) * time.Millisecond
	req := node.ProbeRequest{URL: chk.Url, ExpectStatus: int(chk.ExpectStatus), Timeout: timeout}
	if accessKey != nil {
		cookie, err := c.probeCookie(chk, accessKey)
		if err != nil {
			// Probed without: the login's 401 then shows on the check.
			c.log.Warn("uptime: sign a probe's session", "check", chk.ID, "err", err)
		}
		req.Cookie = cookie
	}
	ctx, cancel := context.WithTimeout(c.ctx, timeout+30*time.Second)
	defer cancel()
	at := time.Now()
	rt := c.connected(srv.ID)
	if rt == nil {
		rt = offlineRuntime{errf(apitypes.CodeServerOffline, "the server is offline")}
	}
	res, err := rt.Probe(ctx, req)
	prober := srv.Name
	if err != nil && !srv.Local {
		c.log.Warn("uptime: probe on another server failed; the control plane probes", "check", chk.ID, "server", srv.Name, "err", err)
		prober = c.localName()
		res, err = c.rt.Probe(ctx, req)
	}
	if c.ctx.Err() != nil {
		return // closing: a cut-short probe says nothing about the site
	}
	if err != nil {
		c.log.Warn("uptime: probe", "check", chk.ID, "err", err)
		return
	}
	c.recordProbe(c.ctx, chk, prober, res, at)
}

// uptimeProber picks the server that probes a check: among the online
// servers other than the environment's (envServer), remote ones only on
// a protocol with the probe op, the check ID modulo their number, so
// checks spread over them. The control plane's own server is always a
// candidate (it probes in process) and the fallback when there is no
// other. servers is sorted by ID and holds the local server. A check
// behind a Dock login (login) is always the control plane's: the session
// its probe carries is made there and stays there.
func (c *Core) uptimeProber(servers []store.Server, checkID, envServer int64, login bool) store.Server {
	var local store.Server
	cands := make([]store.Server, 0, len(servers))
	for _, s := range servers {
		if s.Local {
			local = s
		}
		switch {
		case login:
		case s.ID == envServer || s.RevokedAt != nil:
		case s.Local:
			cands = append(cands, s)
		case c.connected(s.ID) != nil && c.nodes.State(s.ID).Hello.Protocol >= remote.ProbeProtocol:
			cands = append(cands, s)
		}
	}
	if len(cands) == 0 {
		return local
	}
	return cands[checkID%int64(len(cands))]
}

// uptimeTick runs on the statistics tick while checks exist and rolls
// up what is due (rollUptime).
func (c *Core) uptimeTick(ctx context.Context, now time.Time) {
	u := &c.uptime
	u.mu.Lock()
	active, from := len(u.checks) > 0, u.rolledTo
	u.mu.Unlock()
	if !active {
		return
	}
	end, err := c.rollUptime(ctx, now, from)
	if err != nil {
		c.log.Warn("uptime: roll up", "err", err)
		return
	}
	if end == 0 {
		return
	}
	u.mu.Lock()
	u.rolledTo = end
	dayFrom := u.dayRolledTo
	u.mu.Unlock()
	days, err := c.rollUptimeDays(ctx, now, end, dayFrom)
	if err != nil {
		c.log.Warn("uptime: roll up days", "err", err)
		return
	}
	u.mu.Lock()
	u.dayRolledTo = days
	u.mu.Unlock()
}

// rollUptimeDays rolls every UTC day from rolledTo (the first day not
// rolled yet; 0 after a start, when the newest day row says) that ended
// by hourEnd, the end of the hours rolled so far, into day rows, then
// trims day rows past apitypes.StatusDays, in one transaction. Days
// whose hour rows are partly trimmed are never rolled, so a restart
// can't replace a whole day with part of one; after an upgrade the
// first roll fills in the 30 days hour rows hold. It returns the first
// day not rolled.
func (c *Core) rollUptimeDays(ctx context.Context, now time.Time, hourEnd, rolledTo int64) (int64, error) {
	if rolledTo == 0 {
		last, err := c.store.LastUptimeDay(ctx)
		if err != nil {
			return 0, err
		}
		if last > 0 {
			rolledTo = last + uptimeDay
		}
	}
	from := max(rolledTo, firstWholeDay(now))
	end := hourEnd - hourEnd%uptimeDay
	if end <= from {
		return from, nil
	}
	return end, c.store.Tx(ctx, func(q *store.Queries) error {
		for d := from; d < end; d += uptimeDay {
			if err := q.RollupUptimeDay(ctx, store.RollupUptimeDayParams{FromAt: d, ToAt: d + uptimeDay}); err != nil {
				return err
			}
		}
		return q.PruneUptimeStats(ctx, store.PruneUptimeStatsParams{Res: uptimeDay, At: now.Unix() - uptimeKeepDay})
	})
}

// rollUptime rolls every hour after rolledTo that ended at least a
// minute before now (so probes timed in it have been recorded) into hour
// rows, then trims minute rows past a day and hour rows past 30 days, in
// one transaction. It returns the end of the last hour rolled, 0 when
// none was due: then it does nothing, so it works at most once an hour.
// With rolledTo 0 (the first tick after a start) it rolls the hours
// minute rows still cover, which gives the same rows again.
func (c *Core) rollUptime(ctx context.Context, now time.Time, rolledTo int64) (int64, error) {
	cut := now.Unix() - uptimeMinute
	end := cut - cut%uptimeHour
	oldest := now.Unix() - uptimeKeepMinute
	from := max(rolledTo, oldest-oldest%uptimeHour+uptimeHour)
	if end <= from {
		return 0, nil
	}
	return end, c.store.Tx(ctx, func(q *store.Queries) error {
		for h := from; h < end; h += uptimeHour {
			if err := q.RollupUptimeHour(ctx, store.RollupUptimeHourParams{FromAt: h, ToAt: h + uptimeHour}); err != nil {
				return err
			}
		}
		if err := q.PruneUptimeStats(ctx, store.PruneUptimeStatsParams{Res: uptimeMinute, At: now.Unix() - uptimeKeepMinute}); err != nil {
			return err
		}
		return q.PruneUptimeStats(ctx, store.PruneUptimeStatsParams{Res: uptimeHour, At: now.Unix() - uptimeKeepHour})
	})
}
