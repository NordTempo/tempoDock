package core

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/httpkit"
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
	// Day rows (UTC days) are status pages' history (ADR 0073), kept as
	// long as a page shows.
	uptimeDay     = 86400
	uptimeKeepDay = apitypes.StatusDays * uptimeDay
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
	// dayRolledTo is the first day not rolled into day rows yet, 0 before
	// the first hour is rolled.
	dayRolledTo int64
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

// kickUptime has the scheduler reload its checks: one was added,
// changed or removed, or a service archived.
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
	rows, err := c.insertUptimeChecks(ctx, a, ss, []store.CreateUptimeCheckParams{params}, false)
	if err != nil {
		return apitypes.UptimeCheck{}, err
	}
	row := rows[0]
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return apitypes.UptimeCheck{}, err
	}
	out := c.toUptimeCheck(row, ss.env.ServerID, servers, a.Operator)
	out.Series = uptimeSeries(nil, time.Now())
	return out, nil
}

// insertUptimeChecks adds checks to a service within its and its
// organization's caps, audits each and kicks the scheduler. With skip a
// check past a cap is left out; without, it is refused.
func (c *Core) insertUptimeChecks(ctx context.Context, a Actor, ss svcScope, params []store.CreateUptimeCheckParams, skip bool) ([]store.UptimeCheck, error) {
	var rows []store.UptimeCheck
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		existing, err := q.ListServiceUptimeChecks(ctx, ss.svc.ID)
		if err != nil {
			return err
		}
		n, err := q.CountOrgUptimeChecks(ctx, ss.project.OrgID)
		if err != nil {
			return err
		}
		have := len(existing)
		for _, p := range params {
			switch {
			case n >= uptimeOrgMaxChecks && skip, have >= uptimeMaxChecks && skip:
				return nil
			case n >= uptimeOrgMaxChecks:
				return conflict("this organization has %d uptime checks already, the most it may have; remove one with `dock uptime remove`", uptimeOrgMaxChecks)
			case have >= uptimeMaxChecks:
				return conflict("service %s has %d uptime checks already, the most it may have; remove one with `dock uptime remove`", ss, uptimeMaxChecks)
			}
			row, err := q.CreateUptimeCheck(ctx, p)
			if err != nil {
				return err
			}
			rows = append(rows, row)
			n, have = n+1, have+1
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		c.audit(ctx, a, "uptime.add", &ss.project, &ss.env, ss.String(), "added uptime check "+strconv.FormatInt(r.ID, 10)+" of "+r.Url)
	}
	if len(rows) > 0 {
		c.kickUptime()
	}
	return rows, nil
}

// domainChecks are the auto checks (ADR 0059) of the service's verified
// domains, those that don't redirect, on hosts it has no check of yet:
// GET https://<host>/ with the default settings.
func domainChecks(ss svcScope, domains []store.Domain, existing []store.UptimeCheck, by string) []store.CreateUptimeCheckParams {
	checked := map[string]bool{}
	for _, r := range existing {
		if u, err := url.Parse(r.Url); err == nil {
			checked[u.Hostname()] = true
		}
	}
	var out []store.CreateUptimeCheckParams
	for _, d := range domains {
		if d.VerifiedAt == nil || d.RedirectTo != "" || checked[d.Hostname] {
			continue
		}
		checked[d.Hostname] = true
		out = append(out, store.CreateUptimeCheckParams{
			ServiceID: ss.svc.ID, Url: "https://" + d.Hostname + "/", ExpectStatus: 200,
			IntervalS: int64(uptimeDefaultInterval / time.Second), TimeoutMs: uptimeDefaultTimeout.Milliseconds(),
			CreatedAt: now(), CreatedBy: by, Auto: true,
		})
	}
	return out
}

// CheckDomains adds an auto check (ADR 0059) for each of a service's
// verified domains without a check, the Uptime tab's "Check all
// domains", and returns the service's checks. Project admins only.
func (c *Core) CheckDomains(ctx context.Context, a Actor, ref ServiceRef) ([]apitypes.UptimeCheck, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeAdmin, "adding uptime checks")
	if err != nil {
		return nil, err
	}
	if ss.svc.ArchivedAt != nil {
		return nil, invalid("service %s is archived; uptime checks are for live services", ss)
	}
	domains, err := c.store.ListDomainsByService(ctx, ss.svc.ID)
	if err != nil {
		return nil, err
	}
	existing, err := c.store.ListServiceUptimeChecks(ctx, ss.svc.ID)
	if err != nil {
		return nil, err
	}
	if _, err := c.insertUptimeChecks(ctx, a, ss, domainChecks(ss, domains, existing, a.Name), false); err != nil {
		return nil, err
	}
	return c.ListUptimeChecks(ctx, a, ref)
}

// autoUptimeChecks adds Dock's own checks (ADR 0059) of a live service's
// verified domains (only host, unless empty) that have none: when a
// domain is verified and when the service's first release goes live.
// A domain whose DNS names no public address is left out, since a probe
// couldn't reach it (a LAN or tailnet install). Best effort: caps skip,
// failures are logged.
func (c *Core) autoUptimeChecks(ctx context.Context, ss svcScope, host string) {
	if ss.svc.ArchivedAt != nil || ss.svc.StoppedAt != nil {
		return
	}
	if _, err := c.store.GetActiveRelease(ctx, ss.svc.ID); err != nil {
		return // not deployed yet: its first release adds them
	}
	domains, err := c.store.ListDomainsByService(ctx, ss.svc.ID)
	if err == nil && host != "" {
		domains = slices.DeleteFunc(domains, func(d store.Domain) bool { return d.Hostname != host })
	}
	var existing []store.UptimeCheck
	if err == nil {
		existing, err = c.store.ListServiceUptimeChecks(ctx, ss.svc.ID)
	}
	if err != nil {
		c.log.Warn("uptime: add checks of domains", "service", ss.String(), "err", err)
		return
	}
	params := slices.DeleteFunc(domainChecks(ss, domains, existing, System.Name), func(p store.CreateUptimeCheckParams) bool {
		u, _ := url.Parse(p.Url)
		lctx, cancel := context.WithTimeout(ctx, verifyLookupTimeout)
		defer cancel()
		addrs, err := c.lookupIP(lctx, u.Hostname())
		return err != nil || !slices.ContainsFunc(addrs, httpkit.IsPublicAddr)
	})
	if len(params) == 0 {
		return
	}
	if _, err := c.insertUptimeChecks(ctx, System.InOrg(ss.project.OrgID), ss, params, true); err != nil {
		c.log.Warn("uptime: add checks of domains", "service", ss.String(), "err", err)
	}
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

// UpdateUptimeCheck changes a check's interval or expected status in
// place, keeping its history, and returns it with its results. The
// scheduler reloads at once: a new interval starts from now. Project
// admins only.
func (c *Core) UpdateUptimeCheck(ctx context.Context, a Actor, ref ServiceRef, id int64, req apitypes.UpdateUptimeCheckRequest) (apitypes.UptimeCheck, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeAdmin, "changing an uptime check")
	if err != nil {
		return apitypes.UptimeCheck{}, err
	}
	if req.IntervalSeconds == nil && req.ExpectStatus == nil {
		return apitypes.UptimeCheck{}, invalid("nothing to change: give interval_seconds or expect_status")
	}
	row, err := c.store.GetUptimeCheck(ctx, id)
	if store.IsNotFound(err) || (err == nil && row.ServiceID != ss.svc.ID) {
		return apitypes.UptimeCheck{}, notFound("uptime check %d not found on service %s; `dock uptime list` lists them", id, ss)
	}
	if err != nil {
		return apitypes.UptimeCheck{}, err
	}
	params := store.UpdateUptimeCheckParams{ID: id, IntervalS: row.IntervalS, ExpectStatus: row.ExpectStatus}
	if req.ExpectStatus != nil {
		if params.ExpectStatus, err = parseExpectStatus(*req.ExpectStatus); err != nil {
			return apitypes.UptimeCheck{}, err
		}
	}
	if req.IntervalSeconds != nil {
		interval, timeout := time.Duration(*req.IntervalSeconds)*time.Second, time.Duration(row.TimeoutMs)*time.Millisecond
		switch {
		case interval < uptimeMinInterval || interval > uptimeMaxInterval:
			return apitypes.UptimeCheck{}, invalid("the interval must be 30 seconds to 1 hour, not %s", interval)
		case timeout >= interval:
			return apitypes.UptimeCheck{}, invalid("the interval must be longer than the check's timeout (%s); remove the check and add it again with a shorter --timeout", timeout)
		}
		params.IntervalS = int64(*req.IntervalSeconds)
	}
	if params.IntervalS == row.IntervalS && params.ExpectStatus == row.ExpectStatus {
		return c.uptimeCheck(ctx, ss, a.Operator, id)
	}
	if _, err := c.store.UpdateUptimeCheck(ctx, params); err != nil {
		return apitypes.UptimeCheck{}, err
	}
	c.audit(ctx, a, "uptime.update", &ss.project, &ss.env, ss.String(), fmt.Sprintf("changed uptime check %d of %s to every %ds, expecting %s", id, row.Url, params.IntervalS, expectStatusString(params.ExpectStatus)))
	c.kickUptime()
	return c.uptimeCheck(ctx, ss, a.Operator, id)
}

// uptimeCheck is one of a service's checks with its results.
func (c *Core) uptimeCheck(ctx context.Context, ss svcScope, operator bool, id int64) (apitypes.UptimeCheck, error) {
	checks, err := c.uptimeChecks(ctx, ss, operator)
	if err != nil {
		return apitypes.UptimeCheck{}, err
	}
	if i := slices.IndexFunc(checks, func(u apitypes.UptimeCheck) bool { return u.ID == id }); i >= 0 {
		return checks[i], nil
	}
	return apitypes.UptimeCheck{}, notFound("uptime check %d not found on service %s; `dock uptime list` lists them", id, ss)
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
	return c.uptimeChecks(ctx, ss, a.Operator)
}

// uptimeChecks is ListUptimeChecks after its permission check.
func (c *Core) uptimeChecks(ctx context.Context, ss svcScope, operator bool) ([]apitypes.UptimeCheck, error) {
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
		uc := c.toUptimeCheck(r, ss.env.ServerID, servers, operator)
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
		CreatedAt: r.CreatedAt, CreatedBy: r.CreatedBy, Auto: r.Auto, State: apitypes.UptimeUnknown,
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

// uptimeDayFrom is where hour rows take over from day rows in status
// pages' history: the first day not rolled into day rows, or before the
// first rollup the first day hour rows still cover whole.
func (c *Core) uptimeDayFrom(now time.Time) int64 {
	c.uptime.mu.Lock()
	defer c.uptime.mu.Unlock()
	if c.uptime.dayRolledTo > 0 {
		return c.uptime.dayRolledTo
	}
	return firstWholeDay(now)
}

// firstWholeDay is the first UTC day whose hour rows are all still kept.
func firstWholeDay(now time.Time) int64 {
	oldest := now.Unix() - uptimeKeepHour
	return oldest - oldest%uptimeDay + uptimeDay
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
	// During a maintenance window over the check (ADR 0073), a probe is
	// shown but neither counted nor a failure.
	muted := c.checkMuted(chk.ID, chk.ServiceID)
	minute := at.Unix() - at.Unix()%uptimeMinute
	if !muted {
		if err := c.store.AddUptimeProbe(ctx, store.AddUptimeProbeParams{CheckID: chk.ID, At: minute, Up: up, LatencyMsSum: res.Latency.Milliseconds()}); err != nil {
			c.log.Warn("uptime: record probe", "check", chk.ID, "err", err)
		}
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
	if muted && !res.Up {
		u.mu.Unlock()
		return
	}
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
	case name == c.localName():
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
