package core

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Uptime checks (ADR 0055). A project admin adds a URL on one of a
// service's domains; the scheduler (uptime_sched.go) has another server
// probe it every interval. Two failed probes in a row open an
// uptime_down alert, the next success resolves it. Each probe adds to its
// minute's row in uptime_stats; the statistics tick rolls finished hours
// into hour rows and trims both. A check's state and last probe live in
// memory only: after a restart they are unknown until it is probed again.

const (
	uptimeMaxChecks = 10
	// uptimeOrgMaxChecks caps an organization's checks, and
	// uptimeOrgProbes and uptimeMaxProbes the probes running at once per
	// organization and in all (uptimeState.inFlight).
	uptimeOrgMaxChecks    = 50
	uptimeOrgProbes       = 2
	uptimeMaxProbes       = 8
	uptimeMinInterval     = 30 * time.Second
	uptimeMaxInterval     = time.Hour
	uptimeDefaultInterval = time.Minute
	uptimeDefaultTimeout  = 10 * time.Second
	uptimeMaxTimeout      = 30 * time.Second
	// uptimeFailures failed probes in a row make a check down.
	uptimeFailures = 2
	// uptimeIncidents is how many of a check's alerts its results show.
	uptimeIncidents = 5
	// Results: minute rows are kept a day and summed into 30-minute
	// buckets for the strip, hour rows 30 days.
	uptimeMinute     = 60
	uptimeHour       = 3600
	uptimeBucket     = 1800
	uptimeKeepMinute = 24 * 3600
	uptimeKeepHour   = 30 * 24 * 3600
)

// uptimeState is the scheduler's kick and the checks' state in memory.
type uptimeState struct {
	// kick asks the scheduler to reload its checks.
	kick chan struct{}
	// inFlight and orgInFlight count the probes running, in all and per
	// organization (entries go at zero). A probe that would pass
	// uptimeMaxProbes or uptimeOrgProbes is skipped that round, never
	// queued: one organization's checks against a site that hangs can't
	// hold up anyone else's.
	inFlight    int
	orgInFlight map[int64]int
	// second is how long a second of a check's interval lasts: a second
	// but in tests (Options.UptimeSecond).
	second time.Duration

	mu sync.Mutex
	// checks holds each scheduled check's state, by check ID; entries
	// go when the check does (the scheduler's reload).
	checks map[int64]*checkState
	// rolledTo is the end of the last hour rolled into hour rows (unix
	// seconds), 0 before the first statistics tick.
	rolledTo int64
}

// checkState is what the probes of one check said.
type checkState struct {
	// running: a probe of the check hasn't finished.
	running  bool
	failures int
	// since is when the current run of failures began.
	since time.Time
	state apitypes.UptimeState
	last  *apitypes.UptimeProbe
}

// newUptimeState is the state of a Core without checks loaded yet.
func newUptimeState() uptimeState {
	return uptimeState{kick: make(chan struct{}, 1), orgInFlight: map[int64]int{}, second: time.Second, checks: map[int64]*checkState{}}
}

// kickUptime has the scheduler reload its checks: one was added or
// removed, or a service archived.
func (c *Core) kickUptime() {
	select {
	case c.uptime.kick <- struct{}{}:
	default:
	}
}

// uptimeKey is the alert key of a check.
func uptimeKey(checkID int64) string { return "uptime:" + strconv.FormatInt(checkID, 10) }

// AddUptimeCheck adds an uptime check to a service. Project admins only.
func (c *Core) AddUptimeCheck(ctx context.Context, a Actor, ref ServiceRef, req apitypes.AddUptimeCheckRequest) (apitypes.UptimeCheck, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeAdmin, "adding an uptime check")
	if err != nil {
		return apitypes.UptimeCheck{}, err
	}
	if ss.svc.ArchivedAt != nil {
		return apitypes.UptimeCheck{}, invalid("service %s is archived; uptime checks are for live services", ss)
	}
	params := store.CreateUptimeCheckParams{ServiceID: ss.svc.ID, CreatedAt: now(), CreatedBy: a.Name}
	if params.ExpectStatus, err = parseExpectStatus(req.ExpectStatus); err != nil {
		return apitypes.UptimeCheck{}, err
	}
	interval, timeout := uptimeDefaultInterval, uptimeDefaultTimeout
	if req.IntervalSeconds != 0 {
		interval = time.Duration(req.IntervalSeconds) * time.Second
	}
	if req.TimeoutSeconds != 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}
	switch {
	case interval < uptimeMinInterval || interval > uptimeMaxInterval:
		return apitypes.UptimeCheck{}, invalid("the interval must be 30 seconds to 1 hour, not %s", interval)
	case timeout < time.Second || timeout > uptimeMaxTimeout:
		return apitypes.UptimeCheck{}, invalid("the timeout must be 1 to 30 seconds, not %s", timeout)
	case timeout >= interval:
		return apitypes.UptimeCheck{}, invalid("the timeout (%s) must be shorter than the interval (%s)", timeout, interval)
	}
	params.IntervalS, params.TimeoutMs = int64(interval/time.Second), timeout.Milliseconds()
	domains, err := c.store.ListDomainsByService(ctx, ss.svc.ID)
	if err != nil {
		return apitypes.UptimeCheck{}, err
	}
	if params.Url, err = checkURL(req.URL, ss, domains); err != nil {
		return apitypes.UptimeCheck{}, err
	}
	var row store.UptimeCheck
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		existing, err := q.ListServiceUptimeChecks(ctx, ss.svc.ID)
		if err != nil {
			return err
		}
		if n, err := q.CountOrgUptimeChecks(ctx, a.OrgID); err != nil {
			return err
		} else if n >= uptimeOrgMaxChecks {
			return conflict("this organization has %d uptime checks already, the most it may have; remove one with `dock uptime remove`", uptimeOrgMaxChecks)
		}
		if len(existing) >= uptimeMaxChecks {
			return conflict("service %s has %d uptime checks already, the most it may have; remove one with `dock uptime remove`", ss, uptimeMaxChecks)
		}
		row, err = q.CreateUptimeCheck(ctx, params)
		return err
	})
	if err != nil {
		return apitypes.UptimeCheck{}, err
	}
	c.audit(ctx, a, "uptime.add", &ss.project, &ss.env, ss.String(), "added uptime check "+strconv.FormatInt(row.ID, 10)+" of "+row.Url)
	c.kickUptime()
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return apitypes.UptimeCheck{}, err
	}
	out := c.toUptimeCheck(row, ss.env.ServerID, servers, a.Operator)
	out.Series = uptimeSeries(nil, time.Now())
	return out, nil
}

// parseExpectStatus turns "200", "2xx" or "3xx" ("" is 200) into
// node.ProbeRequest's ExpectStatus.
func parseExpectStatus(s string) (int64, error) {
	switch s {
	case "":
		return 200, nil
	case "2xx":
		return node.ExpectClass2xx, nil
	case "3xx":
		return node.ExpectClass3xx, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 100 || n > 599 {
		return 0, invalid("expected status %q must be a status code (100 to 599), 2xx or 3xx", s)
	}
	return int64(n), nil
}

// expectStatusString is parseExpectStatus's inverse.
func expectStatusString(v int64) string {
	if v == node.ExpectClass2xx || v == node.ExpectClass3xx {
		return strconv.FormatInt(v, 10) + "xx"
	}
	return strconv.FormatInt(v, 10)
}

// checkURL validates a check's URL and returns it normalized: http or
// https, no credentials or port other than the scheme's, and a host
// that is one of the service's domains.
func checkURL(raw string, ss svcScope, domains []store.Domain) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", invalid("%q is not an http:// or https:// URL", raw)
	}
	if u.User != nil {
		return "", invalid("uptime check URLs can't hold credentials")
	}
	standard := "80"
	if u.Scheme == "https" {
		standard = "443"
	}
	if p := u.Port(); p != "" && p != standard {
		return "", invalid("uptime checks probe a domain as visitors reach it; drop the port %s", p)
	}
	host := strings.ToLower(u.Hostname())
	if i := slices.IndexFunc(domains, func(d store.Domain) bool { return d.Hostname == host }); i >= 0 && domains[i].VerifiedAt == nil {
		// Its DNS may point anywhere until it is proven the service's.
		return "", invalid("%s isn't verified yet; add its TXT record (`dock domain show %s`), then add the check", host, host)
	}
	if !slices.ContainsFunc(domains, func(d store.Domain) bool { return d.Hostname == host }) {
		if len(domains) == 0 {
			return "", invalid("service %s has no domains; add one with `dock domain add` first", ss)
		}
		names := make([]string, len(domains))
		for i, d := range domains {
			names[i] = d.Hostname
		}
		return "", invalid("%s is not a domain of service %s; use one of %s", host, ss, strings.Join(names, ", "))
	}
	u.Host, u.Fragment, u.RawFragment = host, "", ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

// RemoveUptimeCheck removes a service's uptime check, its history and
// open alert. Project admins only.
func (c *Core) RemoveUptimeCheck(ctx context.Context, a Actor, ref ServiceRef, id int64) error {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeAdmin, "removing an uptime check")
	if err != nil {
		return err
	}
	row, err := c.store.GetUptimeCheck(ctx, id)
	if store.IsNotFound(err) || (err == nil && row.ServiceID != ss.svc.ID) {
		return notFound("uptime check %d not found on service %s; `dock uptime list` lists them", id, ss)
	}
	if err != nil {
		return err
	}
	if err := c.store.DeleteUptimeCheck(ctx, id); err != nil {
		return err
	}
	c.signalAlert(ctx, alertSignal{Key: uptimeKey(id), Resolve: true})
	c.audit(ctx, a, "uptime.remove", &ss.project, &ss.env, ss.String(), "removed uptime check "+strconv.FormatInt(id, 10)+" of "+row.Url)
	c.kickUptime()
	return nil
}

// ListUptimeChecks returns a service's uptime checks with their results:
// state, last probe, percentages, the last day's strip and incidents.
// Six queries however many checks.
func (c *Core) ListUptimeChecks(ctx context.Context, a Actor, ref ServiceRef) ([]apitypes.UptimeCheck, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "listing uptime checks")
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListServiceUptimeChecks(ctx, ss.svc.ID)
	if err != nil || len(rows) == 0 {
		return []apitypes.UptimeCheck{}, err
	}
	n := time.Now()
	day, week, month := n.Unix()-uptimeKeepMinute, n.Unix()-7*24*3600, n.Unix()-uptimeKeepHour
	totals, err := c.store.UptimeTotals(ctx, store.UptimeTotalsParams{
		ServiceID: ss.svc.ID, SinceDay: day, SinceWeek: week, SinceMonth: month, MinuteFrom: c.uptimeMinuteFrom(n),
	})
	if err != nil {
		return nil, err
	}
	series, err := c.store.UptimeSeries(ctx, store.UptimeSeriesParams{ServiceID: ss.svc.ID, Since: seriesStart(n)})
	if err != nil {
		return nil, err
	}
	incidents, err := c.store.ListUptimeIncidents(ctx, store.ListUptimeIncidentsParams{ServiceID: ss.svc.ID, PerCheck: uptimeIncidents})
	if err != nil {
		return nil, err
	}
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(incidents, func(x, y store.ListUptimeIncidentsRow) int { return int(y.ID - x.ID) })
	out := make([]apitypes.UptimeCheck, 0, len(rows))
	for _, r := range rows {
		uc := c.toUptimeCheck(r, ss.env.ServerID, servers, a.Operator)
		if i := slices.IndexFunc(totals, func(t store.UptimeTotalsRow) bool { return t.CheckID == r.ID }); i >= 0 {
			t := totals[i]
			uc.Uptime24h, uc.Uptime7d, uc.Uptime30d = percent(t.UpDay, t.TotalDay), percent(t.UpWeek, t.TotalWeek), percent(t.UpMonth, t.TotalMonth)
		}
		uc.Series = uptimeSeries(func(yield func(store.UptimeSeriesRow) bool) {
			for _, s := range series {
				if s.CheckID == r.ID && !yield(s) {
					return
				}
			}
		}, n)
		key := uptimeKey(r.ID)
		for _, in := range incidents {
			if in.Key == key {
				uc.Incidents = append(uc.Incidents, apitypes.UptimeIncident{AlertID: in.ID, Message: in.Message, OpenedAt: in.OpenedAt, ResolvedAt: in.ResolvedAt})
			}
		}
		out = append(out, uc)
	}
	return out, nil
}

// toUptimeCheck is a check without its history: settings, state, last
// probe and the server that probes it.
// Servers are named for operators only (serverLabel).
func (c *Core) toUptimeCheck(r store.UptimeCheck, envServer int64, servers []store.Server, operator bool) apitypes.UptimeCheck {
	out := apitypes.UptimeCheck{
		ID: r.ID, URL: r.Url, ExpectStatus: expectStatusString(r.ExpectStatus),
		IntervalSeconds: int(r.IntervalS), TimeoutSeconds: int(r.TimeoutMs / 1000),
		CreatedAt: r.CreatedAt, CreatedBy: r.CreatedBy, State: apitypes.UptimeUnknown,
		Incidents: []apitypes.UptimeIncident{},
	}
	out.Server = c.serverLabel(c.uptimeProber(servers, r.ID, envServer).Name, operator)
	out.State, out.LastProbe = c.checkStatus(r.ID)
	if out.LastProbe != nil {
		p := *out.LastProbe
		p.Server = c.serverLabel(p.Server, operator)
		out.LastProbe = &p
	}
	return out
}

// checkStatus is a check's state and last probe from memory. A check
// not probed since the daemon started is down while its alert is open.
func (c *Core) checkStatus(id int64) (apitypes.UptimeState, *apitypes.UptimeProbe) {
	c.uptime.mu.Lock()
	st := c.uptime.checks[id]
	var state apitypes.UptimeState
	var last *apitypes.UptimeProbe
	if st != nil {
		state, last = st.state, st.last
	}
	c.uptime.mu.Unlock()
	if state == "" || state == apitypes.UptimeUnknown {
		state = apitypes.UptimeUnknown
		c.alerts.mu.Lock()
		if _, open := c.alerts.open[uptimeKey(id)]; open {
			state = apitypes.UptimeDown
		}
		c.alerts.mu.Unlock()
	}
	return state, last
}

// percent is up of total as a percentage, nil without probes.
func percent(up, total int64) *float64 {
	if total <= 0 {
		return nil
	}
	return ptr(100 * float64(up) / float64(total))
}

// seriesStart is the first 30-minute bucket of the strip ending at now.
func seriesStart(now time.Time) int64 {
	cur := now.Unix() - now.Unix()%uptimeBucket
	return cur - (uptimeKeepMinute/uptimeBucket-1)*uptimeBucket
}

// uptimeSeries lays a check's bucket rows out on the 48 buckets of the
// last day, oldest first.
func uptimeSeries(rows func(yield func(store.UptimeSeriesRow) bool), now time.Time) []apitypes.UptimeBucket {
	start := seriesStart(now)
	out := make([]apitypes.UptimeBucket, uptimeKeepMinute/uptimeBucket)
	for i := range out {
		out[i].At = time.Unix(start+int64(i)*uptimeBucket, 0).UTC()
	}
	if rows == nil {
		return out
	}
	for r := range rows {
		if i := (r.Bucket - start) / uptimeBucket; i >= 0 && int(i) < len(out) {
			out[i].Up, out[i].Total = int(r.Up), int(r.Total)
		}
	}
	return out
}

// uptimeMinuteFrom is where minute rows take over from hour rows in
// percentages: the end of the last hour rolled up, or before the first
// rollup the oldest whole hour minute rows still cover.
func (c *Core) uptimeMinuteFrom(now time.Time) int64 {
	c.uptime.mu.Lock()
	defer c.uptime.mu.Unlock()
	if c.uptime.rolledTo > 0 {
		return c.uptime.rolledTo
	}
	from := now.Unix() - uptimeKeepMinute
	return from - from%uptimeHour + uptimeHour
}

// serviceUptimes sums up a project's checks per service for the
// overview: one query.
func (c *Core) serviceUptimes(ctx context.Context, projectID int64) (map[int64]*apitypes.ServiceUptime, error) {
	rows, err := c.store.UptimeProjectSummary(ctx, store.UptimeProjectSummaryParams{ProjectID: projectID, Since: time.Now().Unix() - uptimeKeepMinute})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	type sums struct{ up, total int64 }
	out := map[int64]*apitypes.ServiceUptime{}
	totals := map[int64]sums{}
	for _, r := range rows {
		su := out[r.ServiceID]
		if su == nil {
			su = &apitypes.ServiceUptime{State: apitypes.UptimeUnknown}
			out[r.ServiceID] = su
		}
		su.Checks++
		t := totals[r.ServiceID]
		totals[r.ServiceID] = sums{t.up + r.Up, t.total + r.Total}
		state, _ := c.checkStatus(r.ID)
		switch {
		case state == apitypes.UptimeDown:
			su.State = apitypes.UptimeDown
		case state == apitypes.UptimeUp && su.State != apitypes.UptimeDown:
			su.State = apitypes.UptimeUp
		}
	}
	for id, t := range totals {
		out[id].Uptime24h = percent(t.up, t.total)
	}
	return out, nil
}

// recordProbe stores one probe's result, updates the check's state and
// raises or resolves its alert. A state change (up or down) is published
// as an uptime event.
func (c *Core) recordProbe(ctx context.Context, chk store.ListUptimeScheduleRow, server string, res node.ProbeResult, at time.Time) {
	var up int64
	if res.Up {
		up = 1
	}
	minute := at.Unix() - at.Unix()%uptimeMinute
	if err := c.store.AddUptimeProbe(ctx, store.AddUptimeProbeParams{CheckID: chk.ID, At: minute, Up: up, LatencyMsSum: res.Latency.Milliseconds()}); err != nil {
		c.log.Warn("uptime: record probe", "check", chk.ID, "err", err)
	}
	res.Error = sanitizeProbeError(res.Error)
	probe := &apitypes.UptimeProbe{At: at.UTC(), Up: res.Up, Status: res.Status, LatencyMS: res.Latency.Milliseconds(), Error: res.Error, Server: server}
	u := &c.uptime
	u.mu.Lock()
	st := u.checks[chk.ID]
	if st == nil {
		// Removed while it was probed.
		u.mu.Unlock()
		return
	}
	before := st.state
	st.last = probe
	if res.Up {
		st.failures, st.state = 0, apitypes.UptimeUp
	} else {
		if st.failures == 0 {
			st.since = at
		}
		if st.failures++; st.failures >= uptimeFailures {
			st.state = apitypes.UptimeDown
		}
	}
	failures, state, since := st.failures, st.state, st.since
	u.mu.Unlock()

	// Through the alert engine, like every alert: its channels get the
	// opening and the resolution.
	if res.Up {
		c.signalAlert(ctx, alertSignal{Key: uptimeKey(chk.ID), Resolve: true})
	} else if failures >= uptimeFailures {
		c.signalAlert(ctx, uptimeDown(chk, c.serverLabel(server, false), res.Error, since))
	}
	if state != before {
		ss := svcScope{envScope{
			store.Project{ID: chk.ProjectID, OrgID: chk.OrgID, Name: chk.Project},
			store.Environment{ID: chk.EnvironmentID, Name: chk.Environment},
		}, store.Service{ID: chk.ServiceID, Name: chk.Service}}
		// Everyone watching gets it, so the server goes unnamed.
		shown := *probe
		shown.Server = c.serverLabel(shown.Server, false)
		c.publish(ss, apitypes.Event{Type: apitypes.EventUptime, Uptime: &apitypes.UptimeUpdate{ID: chk.ID, State: state, LastProbe: &shown}})
	}
}

// uptimeDown is the alert for a check that failed uptimeFailures times
// in a row.
func uptimeDown(chk store.ListUptimeScheduleRow, server, reason string, since time.Time) alertSignal {
	where := chk.Url
	if u, err := url.Parse(chk.Url); err == nil {
		where = u.Host
		if p := u.EscapedPath(); p != "/" {
			where += p
		}
		if u.RawQuery != "" {
			where += "?" + u.RawQuery
		}
	}
	msg := "Probes from " + server + " keep failing"
	if reason != "" {
		msg += " (" + reason + ")"
	}
	msg += " since " + since.UTC().Format("15:04 MST") + ". Check the service's logs and status, and its domain's DNS."
	return alertSignal{
		Key: uptimeKey(chk.ID), Kind: apitypes.AlertUptimeDown, Severity: apitypes.SeverityCritical,
		Title: where + " is down", Message: msg,
		Project: chk.Project, Environment: chk.Environment, Service: chk.Service,
		projectID: chk.ProjectID, serviceID: chk.ServiceID,
	}
}

// sanitizeProbeError makes a node's probe error safe to store, show and
// send in alerts: at most maxProbeError bytes, without control
// characters. Nodes run code the control plane doesn't, so what they
// report is never trusted as is.
func sanitizeProbeError(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if len(s) > maxProbeError {
		s = s[:maxProbeError]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// maxProbeError caps a probe's error text.
const maxProbeError = 200

// serverLabel is how a probing server is named to actor: by name for
// operators, else "the control plane" or "another server", as an
// organization doesn't see the platform's servers (ADR 0027).
func (c *Core) serverLabel(name string, operator bool) string {
	switch {
	case operator:
		return name
	case name == c.local.Name:
		return "the control plane"
	}
	return "another server"
}

// dropUptimeChecks removes a service's checks on host, a domain it no
// longer has (RemoveDomain): the hostname could become anyone's, and its
// DNS point anywhere. Their open alerts resolve. Best effort.
func (c *Core) dropUptimeChecks(ctx context.Context, ss svcScope, host string) {
	rows, err := c.store.ListServiceUptimeChecks(ctx, ss.svc.ID)
	if err != nil {
		c.log.Warn("uptime: drop checks of a removed domain", "domain", host, "err", err)
		return
	}
	dropped := false
	for _, r := range rows {
		if u, err := url.Parse(r.Url); err != nil || u.Hostname() != host {
			continue
		}
		if err := c.store.DeleteUptimeCheck(ctx, r.ID); err != nil {
			c.log.Warn("uptime: drop checks of a removed domain", "check", r.ID, "err", err)
			continue
		}
		c.signalAlert(ctx, alertSignal{Key: uptimeKey(r.ID), Resolve: true})
		dropped = true
	}
	if dropped {
		c.kickUptime()
	}
}
