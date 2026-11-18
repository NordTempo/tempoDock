package core

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Analytics alerts (ADR 0037, phase A2), raised through the alert engine
// (ADR 0020) and so through its notification channels:
//
//   - a new error: the store says which error groups a forwarded batch
//     saw for the first time; each opens an alert, which resolves itself
//     after a day (the group is no longer new). At most a few are open
//     per site, so a broken deploy's flood of new errors is one spike
//     alert and a handful of new ones, not dozens;
//   - an error spike: the forwarder counts each site's errors per minute
//     of their time as it forwards them; a site with many times its usual
//     rate in the last five minutes alerts at once;
//   - a traffic drop: once an hour, on the statistics tick, one query asks
//     the store for every site's page views in the last hour and the same
//     hour of the week before.
//
// Nothing here has a timer: errors are counted and judged as they are
// forwarded, and the statistics tick (which runs anyway) resolves spikes,
// expires new-error alerts and checks traffic once an hour.

// Alert keys and limits.
const (
	newErrorKey      = "analytics:error:"
	errorSpikeKey    = "analytics:spike:"
	trafficDropKey   = "analytics:traffic:"
	maxNewErrorAlert = 5
	newErrorFor      = 24 * time.Hour

	// spikeMinutes is the window a spike is judged on, against the rest
	// of errorRateMinutes before it; a spike is at least spikeMin errors
	// and spikeFactor times the usual rate.
	errorRateMinutes = 60
	spikeMinutes     = 5
	spikeMin         = 20
	spikeFactor      = 5

	// A drop is an hour with under trafficDropShare of the usual page
	// views (the median of the same hour over trafficDays days), for a
	// site that usually has at least trafficMinUsual; it is over once an
	// hour has trafficRecoverShare again.
	trafficDays         = 7
	trafficMinUsual     = 50
	trafficDropShare    = 0.3
	trafficRecoverShare = 0.6
	// trafficCheckAt is how far into an hour the last one is checked,
	// and trafficQuiet how long every server must have kept its state:
	// events of a server that was offline may still be on their way.
	trafficCheckAt = 10 * time.Minute
	trafficQuiet   = 70 * time.Minute
)

// analyticsAlerts is the alerts' state.
type analyticsAlerts struct {
	mu sync.Mutex
	// rates are each site's errors per minute of the last hour, by the
	// errors' own time; a site goes once it has none in the hour.
	rates map[int64]*errorRate
	// checkedHour is the last hour (Unix ms) traffic was checked for.
	checkedHour int64
	// changedAt is when a server last came online or went away, or the
	// daemon started.
	changedAt time.Time
}

// errorRate is a ring of per-minute error counts.
type errorRate struct {
	counts  [errorRateMinutes]uint32
	minutes [errorRateMinutes]int64
	spiking bool
}

// add counts n errors in minute m (Unix minutes) of now's hour; older
// ones (a backlog) are left out.
func (r *errorRate) add(m, now int64, n uint32) {
	if m <= now-errorRateMinutes || m > now+1 {
		return
	}
	i := m % errorRateMinutes
	if r.minutes[i] != m {
		r.minutes[i], r.counts[i] = m, 0
	}
	r.counts[i] += n
}

// window sums the errors of the minutes in (now-to, now-from].
func (r *errorRate) window(now, from, to int64) int64 {
	var n int64
	for i := range r.counts {
		if m := r.minutes[i]; m > now-to && m <= now-from {
			n += int64(r.counts[i])
		}
	}
	return n
}

// recent and usual are the errors of the last spikeMinutes, and the usual
// count per spikeMinutes over the rest of the hour.
func (r *errorRate) recent(now int64) (recent, usual int64) {
	recent = r.window(now, 0, spikeMinutes)
	usual = r.window(now, spikeMinutes, errorRateMinutes) * spikeMinutes / (errorRateMinutes - spikeMinutes)
	return recent, usual
}

// spike reports whether recent errors are a spike against usual.
func spike(recent, usual int64) bool {
	return recent >= spikeMin && recent >= spikeFactor*max(usual, 1)
}

// spooledError is what the forwarder reads of a spooled error.
type spooledError struct {
	Time int64          `json:"t"`
	Site int64          `json:"s"`
	Kind analytics.Kind `json:"k"`
}

// errorKind is how a spooled error's kind reads.
var errorKind = []byte(`"k":4`)

// analyticsWritten handles what a forwarded batch brought: an alert for
// each error group the store saw for the first time, and its errors
// counted for spikes. A batch without errors costs one scan for `"k":4`.
func (c *Core) analyticsWritten(ctx context.Context, res analytics.WriteResult, lines []byte) {
	t := now()
	for _, ne := range res.NewErrors {
		c.newErrorAlert(ctx, ne, t)
	}
	if !bytes.Contains(lines, errorKind) {
		return
	}
	minute := t.Unix() / 60
	counts := map[[2]int64]uint32{}
	var sites []int64
	for line := range bytes.SplitSeq(lines, []byte{'\n'}) {
		if !bytes.Contains(line, errorKind) {
			continue
		}
		var e spooledError
		if json.Unmarshal(line, &e) != nil || e.Kind != analytics.KindError {
			continue
		}
		counts[[2]int64{e.Site, e.Time / 60_000}]++
		if !slices.Contains(sites, e.Site) {
			sites = append(sites, e.Site)
		}
	}
	aa := &c.analytics.alerts
	var spiking []int64
	aa.mu.Lock()
	for k, n := range counts {
		r := aa.rates[k[0]]
		if r == nil {
			if aa.rates == nil {
				aa.rates = map[int64]*errorRate{}
			}
			r = &errorRate{}
			aa.rates[k[0]] = r
		}
		r.add(k[1], minute, n)
	}
	for _, site := range sites {
		r := aa.rates[site]
		if recent, usual := r.recent(minute); !r.spiking && spike(recent, usual) {
			r.spiking = true
			spiking = append(spiking, site)
		}
	}
	snapshot := map[int64][2]int64{}
	for _, site := range spiking {
		recent, usual := aa.rates[site].recent(minute)
		snapshot[site] = [2]int64{recent, usual}
	}
	aa.mu.Unlock()
	for _, site := range spiking {
		n := snapshot[site]
		c.siteAlert(ctx, site, alertSignal{Key: errorSpikeKey + strconv.FormatInt(site, 10), Kind: apitypes.AlertErrorSpike,
			Severity: apitypes.SeverityWarning, Title: "Errors spiking on %s",
			Message: fmt.Sprintf("%d errors in the last %d minutes, against about %d usually.", n[0], spikeMinutes, n[1]),
			Command: "dock analytics errors %s", Target: &apitypes.AlertTarget{}}, t)
	}
}

// newErrorAlert raises an alert for an error group's first error, unless
// its site has maxNewErrorAlert of them open already.
func (c *Core) newErrorAlert(ctx context.Context, ne analytics.NewError, t time.Time) {
	prefix := newErrorKey + strconv.FormatInt(ne.Site, 10) + ":"
	e := c.alerts
	e.mu.Lock()
	open := 0
	for k := range e.open {
		if strings.HasPrefix(k, prefix) {
			open++
		}
	}
	e.mu.Unlock()
	if open >= maxNewErrorAlert {
		c.log.Info("analytics: a new error group without an alert; the site has enough open", "site", ne.Site, "group", ne.Group)
		return
	}
	where := ne.Path
	if labels := c.releaseLabels(ctx, []int64{ne.Release}); labels[ne.Release] != "" {
		where += " (" + labels[ne.Release] + ")"
	}
	c.siteAlert(ctx, ne.Site, alertSignal{Key: prefix + ne.Group, Kind: apitypes.AlertNewError, Severity: apitypes.SeverityWarning,
		Title:   "New error on %s: " + cutText(ne.Type+": "+ne.Message, 120),
		Message: "First seen on " + where + ".",
		Command: "dock analytics errors %s " + ne.Group, Target: &apitypes.AlertTarget{ErrorGroup: ne.Group}}, t)
}

// cutText cuts s to n runes on one line.
func cutText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// siteAlert raises s for a site: the project and environment are the
// site's, and %s in the title and message is its name.
func (c *Core) siteAlert(ctx context.Context, siteID int64, s alertSignal, t time.Time) {
	site, err := c.store.GetSite(ctx, siteID)
	if err != nil {
		if !store.IsNotFound(err) {
			c.log.Error("analytics alert: read the site", "site", siteID, "err", err)
		}
		return
	}
	env, err := c.store.GetEnvironment(ctx, site.EnvironmentID)
	if err != nil {
		c.log.Error("analytics alert: read the site's environment", "site", siteID, "err", err)
		return
	}
	p, err := c.store.GetProject(ctx, env.ProjectID)
	if err != nil {
		c.log.Error("analytics alert: read the site's project", "site", siteID, "err", err)
		return
	}
	s.Title, s.Command = strings.Replace(s.Title, "%s", site.Name, 1), strings.Replace(s.Command, "%s", site.Name, 1)
	if s.Target != nil {
		s.Target.Site = site.Name
	}
	s.Project, s.Environment, s.projectID = p.Name, env.Name, p.ID
	c.raiseAlert(ctx, s, t)
}

// analyticsServersChanged notes that a server came online or went away:
// the traffic check waits for things to settle.
func (c *Core) analyticsServersChanged() {
	aa := &c.analytics.alerts
	aa.mu.Lock()
	aa.changedAt = now()
	aa.mu.Unlock()
}

// analyticsAlertTick runs on the statistics tick: spikes that are over
// resolve, new-error alerts a day old resolve, sites without errors in
// the hour are forgotten, and once an hour traffic is checked.
func (c *Core) analyticsAlertTick(ctx context.Context, t time.Time) {
	minute := t.Unix() / 60
	aa := &c.analytics.alerts
	var calm []int64
	aa.mu.Lock()
	for site, r := range aa.rates {
		recent, usual := r.recent(minute)
		if r.spiking && recent < max(spikeMin/2, 2*usual) {
			r.spiking = false
			calm = append(calm, site)
		}
		if !r.spiking && r.window(minute, 0, errorRateMinutes) == 0 {
			delete(aa.rates, site)
		}
	}
	aa.mu.Unlock()
	for _, site := range calm {
		c.resolveAlert(ctx, errorSpikeKey+strconv.FormatInt(site, 10), t)
	}
	e := c.alerts
	var old []string
	e.mu.Lock()
	for k, a := range e.open {
		if strings.HasPrefix(k, newErrorKey) && t.Sub(a.OpenedAt) >= newErrorFor {
			old = append(old, k)
		}
	}
	e.mu.Unlock()
	for _, k := range old {
		c.resolveAlert(ctx, k, t)
	}
	c.checkTraffic(ctx, t)
}

// checkTraffic compares every site's page views in the last whole hour
// with the same hour of the week before, once an hour, trafficCheckAt
// into the next. It skips an hour while servers came and went (their
// events may still be on the way) and when the store doesn't answer.
func (c *Core) checkTraffic(ctx context.Context, t time.Time) {
	hour := t.Truncate(time.Hour).Add(-time.Hour)
	if t.Sub(hour) < time.Hour+trafficCheckAt {
		return
	}
	aa := &c.analytics.alerts
	aa.mu.Lock()
	if aa.checkedHour >= hour.UnixMilli() {
		aa.mu.Unlock()
		return
	}
	aa.checkedHour = hour.UnixMilli()
	settled := t.Sub(aa.changedAt) >= trafficQuiet
	aa.mu.Unlock()
	cl, _ := c.analyticsClient()
	if cl == nil || !settled {
		return
	}
	c.goCounted("checking sites' traffic", func() {
		qctx, cancel := context.WithTimeout(c.ctx, time.Minute)
		defer cancel()
		rows, err := cl.Traffic(qctx, analytics.TrafficQuery{Hour: hour.UnixMilli(), Days: trafficDays})
		if err != nil {
			c.log.Warn("analytics: the traffic check couldn't ask the store; it tries the next hour", "err", err)
			return
		}
		c.judgeTraffic(qctx, rows, hour, t)
	})
}

// judgeTraffic raises or resolves each site's traffic drop from its page
// views in an hour (DaysAgo 0) and the days before.
func (c *Core) judgeTraffic(ctx context.Context, rows []analytics.TrafficRow, hour, t time.Time) {
	bySite := map[int64]*[trafficDays + 1]int64{}
	for _, r := range rows {
		if r.DaysAgo < 0 || r.DaysAgo > trafficDays {
			continue
		}
		if bySite[r.Site] == nil {
			bySite[r.Site] = &[trafficDays + 1]int64{}
		}
		bySite[r.Site][r.DaysAgo] = r.Pageviews
	}
	// Sites with an open drop and no page views at all have no row.
	c.alerts.mu.Lock()
	for k := range c.alerts.open {
		if id, ok := strings.CutPrefix(k, trafficDropKey); ok {
			if site, err := strconv.ParseInt(id, 10, 64); err == nil && bySite[site] == nil {
				bySite[site] = &[trafficDays + 1]int64{}
			}
		}
	}
	c.alerts.mu.Unlock()
	for site, days := range bySite {
		usual := slices.Clone(days[1:])
		slices.Sort(usual)
		median := usual[len(usual)/2]
		key := trafficDropKey + strconv.FormatInt(site, 10)
		now := days[0]
		dropped := median >= trafficMinUsual && float64(now) < trafficDropShare*float64(median)
		switch {
		case dropped && !c.siteServing(ctx, site):
			// Every service of the site is stopped (ADR 0050): no traffic
			// is what was asked for.
			c.resolveAlert(ctx, key, t)
		case dropped:
			c.siteAlert(ctx, site, alertSignal{Key: key, Kind: apitypes.AlertTrafficDrop, Severity: apitypes.SeverityWarning,
				Title: "Traffic dropped on %s",
				Message: fmt.Sprintf("%d page views %s to %s UTC, against usually %d that hour; check that the site and its script load.",
					now, hour.UTC().Format("15:04"), hour.Add(time.Hour).UTC().Format("15:04"), median),
				Target: &apitypes.AlertTarget{}}, t)
		case median < trafficMinUsual || float64(now) >= trafficRecoverShare*float64(median):
			c.resolveAlert(ctx, key, t)
		}
	}
}

// siteServing reports whether a site has a hostname on a service that
// isn't stopped. Read only for a drop, so on errors it says yes and the
// drop is raised as before.
func (c *Core) siteServing(ctx context.Context, siteID int64) bool {
	serving, err := c.store.SiteServing(ctx, siteID)
	if err != nil {
		c.log.Error("analytics alert: read whether the site's services run", "site", siteID, "err", err)
		return true
	}
	return serving
}
