package core

import (
	"cmp"
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/httpkit"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Outside uptime checks (ADR 0055, 2026-11-28 amendment): an
// organization's checks of sites outside Dock, for status page
// components. They have no service and no domain to verify, so the
// guards are the prober's (public addresses on ports 80 and 443 only),
// an interval of at least a minute, the organization's cap of 50 checks
// and organization admins. They share the scheduler, the history and
// uptime_down alerts (to the organization's channels) with service
// checks.

// uptimeOutsideMinInterval is the shortest interval of an outside check:
// Dock probes sites it doesn't serve no more than once a minute.
const uptimeOutsideMinInterval = time.Minute

// outsideTarget is what outside checks' audit events are about: Activity
// reads "added an uptime check to outside checks".
const outsideTarget = "outside checks"

// outsideNotFound is the error for an ID that isn't one of the org's
// outside checks.
func outsideNotFound(id int64) error {
	return notFound("outside uptime check %d not found; `dock uptime list --outside` lists the organization's", id)
}

// outsideHost refuses hosts a probe could never reach as a public site:
// an IP address that isn't public, or a name without a dot (a container,
// LAN or localhost name). The prober refuses the rest after DNS.
func outsideHost(host string) error {
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		if !httpkit.IsPublicAddr(ip) {
			return invalid("%s is not a public address; outside uptime checks probe public sites only", host)
		}
		return nil
	}
	if !strings.Contains(strings.TrimSuffix(host, "."), ".") {
		return invalid("%s is not a public hostname; outside uptime checks probe public sites only", host)
	}
	return nil
}

// AddOutsideUptimeCheck adds an outside check to the actor's org.
// Organization admins only.
func (c *Core) AddOutsideUptimeCheck(ctx context.Context, a Actor, req apitypes.AddUptimeCheckRequest) (apitypes.UptimeCheck, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "adding an outside uptime check"); err != nil {
		return apitypes.UptimeCheck{}, err
	}
	u, err := parseCheckURL(req.URL)
	if err != nil {
		return apitypes.UptimeCheck{}, err
	}
	if err := outsideHost(u.Hostname()); err != nil {
		return apitypes.UptimeCheck{}, err
	}
	org := a.OrgID
	params := store.CreateUptimeCheckParams{OrgID: &org, Url: u.String(), CreatedAt: now(), CreatedBy: a.Name}
	if params.ExpectStatus, err = parseExpectStatus(req.ExpectStatus); err != nil {
		return apitypes.UptimeCheck{}, err
	}
	if params.IntervalS, params.TimeoutMs, err = checkTiming(req, uptimeOutsideMinInterval); err != nil {
		return apitypes.UptimeCheck{}, err
	}
	var row store.UptimeCheck
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		n, err := q.CountOrgUptimeChecks(ctx, org)
		if err != nil {
			return err
		}
		if n >= uptimeOrgMaxChecks {
			return conflict("this organization has %d uptime checks already, the most it may have; remove one with `dock uptime remove`", uptimeOrgMaxChecks)
		}
		existing, err := q.ListOutsideUptimeChecks(ctx, org)
		if err != nil {
			return err
		}
		if i := slices.IndexFunc(existing, func(r store.UptimeCheck) bool { return r.Url == params.Url }); i >= 0 {
			return conflict("outside uptime check %d probes %s already; components can share it", existing[i].ID, params.Url)
		}
		row, err = q.CreateUptimeCheck(ctx, params)
		return err
	})
	if err != nil {
		return apitypes.UptimeCheck{}, err
	}
	c.audit(ctx, a, "uptime.add", nil, nil, outsideTarget, "added outside uptime check "+strconv.FormatInt(row.ID, 10)+" of "+row.Url)
	c.kickUptime()
	return c.outsideCheck(ctx, a, row.ID)
}

// ListOutsideUptimeChecks returns the org's outside checks with their
// results and the status page components that show them. Anyone who
// reads status pages.
func (c *Core) ListOutsideUptimeChecks(ctx context.Context, a Actor) ([]apitypes.UptimeCheck, error) {
	if err := a.require(apitypes.ScopeRead, "reading status pages"); err != nil {
		return nil, err
	}
	return c.outsideChecks(ctx, a)
}

// outsideChecks is ListOutsideUptimeChecks after its permission check:
// seven queries however many checks.
func (c *Core) outsideChecks(ctx context.Context, a Actor) ([]apitypes.UptimeCheck, error) {
	org := a.OrgID
	rows, err := c.store.ListOutsideUptimeChecks(ctx, org)
	if err != nil || len(rows) == 0 {
		return []apitypes.UptimeCheck{}, err
	}
	n := time.Now()
	totals, err := c.store.OutsideUptimeTotals(ctx, store.OutsideUptimeTotalsParams{
		OrgID: org, SinceDay: n.Unix() - uptimeKeepMinute, SinceWeek: n.Unix() - 7*24*3600, SinceMonth: n.Unix() - uptimeKeepHour, MinuteFrom: c.uptimeMinuteFrom(n),
	})
	if err != nil {
		return nil, err
	}
	series, err := c.store.OutsideUptimeSeries(ctx, store.OutsideUptimeSeriesParams{OrgID: org, Since: seriesStart(n)})
	if err != nil {
		return nil, err
	}
	incidents, err := c.store.ListOutsideUptimeIncidents(ctx, store.ListOutsideUptimeIncidentsParams{OrgID: org, PerCheck: uptimeIncidents})
	if err != nil {
		return nil, err
	}
	uses, err := c.store.ListOutsideUptimeUses(ctx, org)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(uses, func(x, y store.ListOutsideUptimeUsesRow) int {
		return cmp.Or(cmp.Compare(x.Page, y.Page), cmp.Compare(x.Component, y.Component))
	})
	t := make([]store.UptimeTotalsRow, len(totals))
	for i, r := range totals {
		t[i] = store.UptimeTotalsRow(r)
	}
	s := make([]store.UptimeSeriesRow, len(series))
	for i, r := range series {
		s[i] = store.UptimeSeriesRow(r)
	}
	in := make([]store.ListUptimeIncidentsRow, len(incidents))
	for i, r := range incidents {
		in[i] = store.ListUptimeIncidentsRow(r)
	}
	out, err := c.checkResults(ctx, rows, 0, a.Operator, n, t, s, in)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].UsedBy = []apitypes.UptimeCheckUse{}
		for _, u := range uses {
			if u.CheckID != nil && *u.CheckID == out[i].ID {
				out[i].UsedBy = append(out[i].UsedBy, apitypes.UptimeCheckUse{Page: u.Page, Component: u.Component})
			}
		}
	}
	return out, nil
}

// outsideCheck is one of the org's outside checks with its results.
func (c *Core) outsideCheck(ctx context.Context, a Actor, id int64) (apitypes.UptimeCheck, error) {
	checks, err := c.outsideChecks(ctx, a)
	if err != nil {
		return apitypes.UptimeCheck{}, err
	}
	if i := slices.IndexFunc(checks, func(u apitypes.UptimeCheck) bool { return u.ID == id }); i >= 0 {
		return checks[i], nil
	}
	return apitypes.UptimeCheck{}, outsideNotFound(id)
}

// loadOutsideCheck is the org's outside check id, for an org admin.
func (c *Core) loadOutsideCheck(ctx context.Context, a Actor, id int64, action string) (store.UptimeCheck, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, action); err != nil {
		return store.UptimeCheck{}, err
	}
	row, err := c.store.GetUptimeCheck(ctx, id)
	if store.IsNotFound(err) || (err == nil && (row.OrgID == nil || *row.OrgID != a.OrgID)) {
		return store.UptimeCheck{}, outsideNotFound(id)
	}
	return row, err
}

// UpdateOutsideUptimeCheck changes an outside check's interval or
// expected status in place, keeping its history. Organization admins.
func (c *Core) UpdateOutsideUptimeCheck(ctx context.Context, a Actor, id int64, req apitypes.UpdateUptimeCheckRequest) (apitypes.UptimeCheck, error) {
	row, err := c.loadOutsideCheck(ctx, a, id, "changing an outside uptime check")
	if err != nil {
		return apitypes.UptimeCheck{}, err
	}
	params, err := checkUpdate(row, req, uptimeOutsideMinInterval)
	if err != nil {
		return apitypes.UptimeCheck{}, err
	}
	if params.IntervalS != row.IntervalS || params.ExpectStatus != row.ExpectStatus {
		if _, err := c.store.UpdateUptimeCheck(ctx, params); err != nil {
			return apitypes.UptimeCheck{}, err
		}
		c.audit(ctx, a, "uptime.update", nil, nil, outsideTarget,
			fmt.Sprintf("changed outside uptime check %d of %s to every %ds, expecting %s", id, row.Url, params.IntervalS, expectStatusString(params.ExpectStatus)))
		c.kickUptime()
	}
	return c.outsideCheck(ctx, a, id)
}

// RemoveOutsideUptimeCheck removes an outside check, its history, its
// open alert and the status page components' sources that name it.
// Organization admins.
func (c *Core) RemoveOutsideUptimeCheck(ctx context.Context, a Actor, id int64) error {
	row, err := c.loadOutsideCheck(ctx, a, id, "removing an outside uptime check")
	if err != nil {
		return err
	}
	uses, err := c.store.ListOutsideUptimeUses(ctx, a.OrgID)
	if err != nil {
		return err
	}
	var dropped []string
	for _, u := range uses {
		if u.CheckID != nil && *u.CheckID == id {
			dropped = append(dropped, u.Page+"/"+u.Component)
		}
	}
	// Its sources and statistics go with it (ON DELETE CASCADE).
	if err := c.store.DeleteUptimeCheck(ctx, id); err != nil {
		return err
	}
	c.signalAlert(ctx, alertSignal{Key: uptimeKey(id), Resolve: true, Gone: true})
	summary := "removed outside uptime check " + strconv.FormatInt(id, 10) + " of " + row.Url
	if len(dropped) > 0 {
		summary += "; status page components no longer showing it: " + strings.Join(dropped, ", ")
	}
	c.audit(ctx, a, "uptime.remove", nil, nil, outsideTarget, summary)
	c.kickUptime()
	if len(dropped) > 0 {
		c.statusPagesChanged(ctx)
	}
	return nil
}
