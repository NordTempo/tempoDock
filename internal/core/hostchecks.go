package core

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Host checks (ADR 0109): each server reads its own host for security
// problems about once a day, and when an operator asks (Check now).
// Results are the platform's: only operators and platform viewers read
// them, and their alert is a platform alert, never an org's.
//
// There is no timer. The statistics tick (hostCheckTick) starts a
// server's check when its last one is a day old: in memory, one map
// lookup per online server and a time comparison. A check is one call to
// the server, in the background; the results are one row per server.

const (
	// hostCheckEvery is how often a server checks its host by itself.
	hostCheckEvery = 24 * time.Hour
	// hostCheckSlack lets a tick that runs a little early start a check
	// that is due.
	hostCheckSlack = time.Minute
	// hostCheckTimeout bounds one check, the link included.
	hostCheckTimeout = 3 * time.Minute
)

// hostCheckState is what the tick needs in memory: when each server last
// checked (or tried), and which are checking now. Loaded on start.
type hostCheckState struct {
	mu      sync.Mutex
	last    map[int64]time.Time
	running map[int64]bool
	// done is closed when a server's running check ends, for Check now
	// to wait on one already under way.
	done map[int64]chan struct{}
}

// hostAlertKey names a server's host_security alert.
func hostAlertKey(serverID int64) string { return "hostchecks:" + strconv.FormatInt(serverID, 10) }

// loadHostChecks reads when each server last checked its host.
func (c *Core) loadHostChecks(ctx context.Context) error {
	rows, err := c.store.ListHostChecks(ctx)
	if err != nil {
		return fmt.Errorf("read host checks: %w", err)
	}
	h := &c.hostChecks
	h.mu.Lock()
	defer h.mu.Unlock()
	h.last, h.running, h.done = map[int64]time.Time{}, map[int64]bool{}, map[int64]chan struct{}{}
	for _, r := range rows {
		h.last[r.ServerID] = r.CheckedAt
	}
	return nil
}

// hostCheckTick starts the checks that are due, each in the background:
// a server online whose last check is a day old.
func (c *Core) hostCheckTick(now time.Time, servers []store.Server) {
	for _, srv := range servers {
		if c.connected(srv.ID) == nil {
			continue
		}
		done, ok := c.claimHostCheck(srv.ID, now, true)
		if !ok {
			continue
		}
		c.goCounted("checking a server's host", func() {
			defer c.releaseHostCheck(srv.ID, done)
			if _, err := c.runHostCheck(c.ctx, srv); err != nil {
				c.log.Warn("host check", "server", srv.Name, "err", err)
			}
		})
	}
}

// claimHostCheck marks a server's check as running. With due, only when
// one is due; ok false when it isn't, or one is running already.
func (c *Core) claimHostCheck(serverID int64, now time.Time, due bool) (chan struct{}, bool) {
	h := &c.hostChecks
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running == nil || h.running[serverID] || due && now.Sub(h.last[serverID]) < hostCheckEvery-hostCheckSlack {
		return nil, false
	}
	done := make(chan struct{})
	h.running[serverID], h.done[serverID], h.last[serverID] = true, done, now
	return done, true
}

// releaseHostCheck ends a server's running check.
func (c *Core) releaseHostCheck(serverID int64, done chan struct{}) {
	h := &c.hostChecks
	h.mu.Lock()
	delete(h.running, serverID)
	delete(h.done, serverID)
	h.mu.Unlock()
	close(done)
}

// runHostCheck asks a server to check its host, stores the results and
// raises or resolves its alert. A check that fails keeps the results
// before it, with the error.
func (c *Core) runHostCheck(ctx context.Context, srv store.Server) (apitypes.HostChecks, error) {
	prev, err := c.store.GetHostChecks(ctx, srv.ID)
	if err != nil && !store.IsNotFound(err) {
		return apitypes.HostChecks{}, err
	}
	before := decodeHostChecks(prev.Results)
	at := now().UTC()
	var checks []apitypes.HostCheck
	var runErr error
	if rt := c.connected(srv.ID); rt == nil {
		runErr = fmt.Errorf("server %s is offline", srv.Name)
	} else {
		cctx, cancel := context.WithTimeout(ctx, hostCheckTimeout)
		var res []node.HostCheck
		res, runErr = rt.CheckHost(cctx)
		cancel()
		checks = toHostChecks(res, before, at)
	}
	row := store.SetHostChecksParams{ServerID: srv.ID, CheckedAt: at, Results: prev.Results}
	if row.Results == "" {
		row.Results = "[]"
	}
	if runErr != nil {
		row.Error = clip(runErr.Error(), 500)
		checks = before
	} else {
		b, err := json.Marshal(checks)
		if err != nil {
			return apitypes.HostChecks{}, err
		}
		row.Results = string(b)
	}
	if err := c.store.SetHostChecks(ctx, row); err != nil {
		return apitypes.HostChecks{}, err
	}
	dismissed, err := c.store.ListHostCheckDismissals(ctx, srv.ID)
	if err != nil {
		return apitypes.HostChecks{}, err
	}
	out := hostChecksView(srv.Name, &at, row.Error, checks, dismissed)
	if runErr == nil {
		c.hostAlert(ctx, srv, out)
	}
	return out, nil
}

// toHostChecks turns a server's results into the API's, sorted findings
// first and worst first, each finding with when it was first seen (from
// the check before, by key).
func toHostChecks(res []node.HostCheck, before []apitypes.HostCheck, at time.Time) []apitypes.HostCheck {
	seen := map[string]*time.Time{}
	for _, b := range before {
		if b.Status == apitypes.HostCheckFinding {
			seen[b.Key] = b.FirstSeenAt
		}
	}
	out := make([]apitypes.HostCheck, 0, len(res))
	for _, r := range res {
		h := apitypes.HostCheck{
			Key: r.Key(), Check: r.Check, Subject: r.Subject, Status: apitypes.HostCheckStatus(r.Status),
			Severity: apitypes.HostCheckSeverity(r.Severity), Title: r.Title, Detail: r.Detail, Fix: r.Fix,
		}
		if h.Status == apitypes.HostCheckFinding {
			h.FirstSeenAt = cmp.Or(seen[h.Key], &at)
		} else {
			h.Severity = ""
		}
		out = append(out, h)
	}
	slices.SortStableFunc(out, func(a, b apitypes.HostCheck) int {
		return cmp.Compare(hostCheckRank(b), hostCheckRank(a))
	})
	return out
}

// hostCheckRank orders results: critical, warning and info findings,
// then what couldn't tell, then the rest.
func hostCheckRank(h apitypes.HostCheck) int {
	switch {
	case h.Status == apitypes.HostCheckFinding && h.Severity == apitypes.HostCheckCritical:
		return 5
	case h.Status == apitypes.HostCheckFinding && h.Severity == apitypes.HostCheckWarning:
		return 4
	case h.Status == apitypes.HostCheckFinding:
		return 3
	case h.Status == apitypes.HostCheckUnknown:
		return 2
	}
	return 1
}

// decodeHostChecks reads stored results; a row that doesn't decode
// reads as none.
func decodeHostChecks(s string) []apitypes.HostCheck {
	var out []apitypes.HostCheck
	if s != "" {
		_ = json.Unmarshal([]byte(s), &out)
	}
	return out
}

// hostChecksView puts a server's results together with its dismissals
// and counts what is open.
func hostChecksView(server string, at *time.Time, errText string, checks []apitypes.HostCheck, dismissed []store.HostCheckDismissal) apitypes.HostChecks {
	out := apitypes.HostChecks{Server: server, CheckedAt: at, Error: errText, Checks: slices.Clone(checks)}
	if out.Checks == nil {
		out.Checks = []apitypes.HostCheck{}
	}
	for i := range out.Checks {
		h := &out.Checks[i]
		if h.Status != apitypes.HostCheckFinding {
			continue
		}
		if j := slices.IndexFunc(dismissed, func(d store.HostCheckDismissal) bool { return d.Finding == h.Key }); j >= 0 {
			d := dismissed[j]
			h.Dismissed = &apitypes.HostCheckDismissal{Reason: d.Reason, By: d.DismissedBy, At: d.DismissedAt.UTC()}
			continue
		}
		switch h.Severity {
		case apitypes.HostCheckCritical:
			out.Counts.Critical++
		case apitypes.HostCheckWarning:
			out.Counts.Warning++
		default:
			out.Counts.Info++
		}
	}
	return out
}

// alerting reports whether a finding raises the host alert: open,
// warning or critical, and not the app firewall, which has an alert of
// its own (firewall_alert.go).
func alerting(h apitypes.HostCheck) bool {
	return h.Status == apitypes.HostCheckFinding && h.Dismissed == nil && h.Check != "app_firewall" &&
		(h.Severity == apitypes.HostCheckWarning || h.Severity == apitypes.HostCheckCritical)
}

// hostAlert raises, updates or resolves a server's host_security alert
// from its results. Its target lists every finding raised while it is
// open, so a new one counts as a repeat and is sent, while one fixed
// only changes the text quietly.
func (c *Core) hostAlert(ctx context.Context, srv store.Server, v apitypes.HostChecks) {
	key := hostAlertKey(srv.ID)
	var open []apitypes.HostCheck
	for _, h := range v.Checks {
		if alerting(h) {
			open = append(open, h)
		}
	}
	if len(open) == 0 {
		c.signalAlert(ctx, alertSignal{Key: key, Resolve: true})
		return
	}
	var findings []string
	c.alerts.mu.Lock()
	if cur, ok := c.alerts.open[key]; ok && cur.Target != "" {
		var t apitypes.AlertTarget
		if json.Unmarshal([]byte(cur.Target), &t) == nil {
			findings = t.Findings
		}
	}
	c.alerts.mu.Unlock()
	severity := apitypes.SeverityWarning
	titles := make([]string, len(open))
	for i, h := range open {
		if h.Severity == apitypes.HostCheckCritical {
			severity = apitypes.SeverityCritical
		}
		titles[i] = h.Title
		if !slices.Contains(findings, h.Key) {
			findings = append(findings, h.Key)
		}
	}
	slices.Sort(findings)
	title := fmt.Sprintf("%d security findings on server %s", len(open), srv.Name)
	if len(open) == 1 {
		title = fmt.Sprintf("Security finding on server %s: %s", srv.Name, open[0].Title)
	}
	msg := strings.Join(titles, ". ") + ". The server's Security section in the console says what to do about each, or dismiss one with a reason there."
	c.signalAlert(ctx, alertSignal{
		Key: key, Kind: apitypes.AlertHostSecurity, Severity: severity, Title: title, Message: msg,
		Command: "dock server checks " + srv.Name, Server: srv.Name,
		Target: &apitypes.AlertTarget{Findings: findings},
	})
}

// HostChecks returns a server's last host check. Operators and platform
// viewers only.
func (c *Core) HostChecks(ctx context.Context, a Actor, server string) (apitypes.HostChecks, error) {
	if err := a.requirePlatform(PlatformView, "reading host checks"); err != nil {
		return apitypes.HostChecks{}, err
	}
	srv, err := c.loadServer(ctx, server)
	if err != nil {
		return apitypes.HostChecks{}, err
	}
	return c.hostChecksOf(ctx, srv)
}

// ListHostChecks returns every server's last host check, by server name,
// for the console's Security page: three queries whatever the number of
// servers. Operators and platform viewers only.
func (c *Core) ListHostChecks(ctx context.Context, a Actor) ([]apitypes.HostChecks, error) {
	if err := a.requirePlatform(PlatformView, "reading host checks"); err != nil {
		return nil, err
	}
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListHostChecks(ctx)
	if err != nil {
		return nil, err
	}
	dismissed, err := c.store.ListAllHostCheckDismissals(ctx)
	if err != nil {
		return nil, err
	}
	byServer := map[int64]store.HostCheck{}
	for _, r := range rows {
		byServer[r.ServerID] = r
	}
	slices.SortFunc(servers, func(x, y store.Server) int { return cmp.Compare(x.Name, y.Name) })
	out := make([]apitypes.HostChecks, 0, len(servers))
	for _, srv := range servers {
		r, ok := byServer[srv.ID]
		if !ok {
			out = append(out, apitypes.HostChecks{Server: srv.Name, Checks: []apitypes.HostCheck{}})
			continue
		}
		own := slices.DeleteFunc(slices.Clone(dismissed), func(d store.HostCheckDismissal) bool { return d.ServerID != srv.ID })
		out = append(out, hostChecksView(srv.Name, ptr(r.CheckedAt.UTC()), r.Error, decodeHostChecks(r.Results), own))
	}
	return out, nil
}

// hostChecksOf reads a server's stored results and dismissals.
func (c *Core) hostChecksOf(ctx context.Context, srv store.Server) (apitypes.HostChecks, error) {
	row, err := c.store.GetHostChecks(ctx, srv.ID)
	if store.IsNotFound(err) {
		return apitypes.HostChecks{Server: srv.Name, Checks: []apitypes.HostCheck{}}, nil
	}
	if err != nil {
		return apitypes.HostChecks{}, err
	}
	dismissed, err := c.store.ListHostCheckDismissals(ctx, srv.ID)
	if err != nil {
		return apitypes.HostChecks{}, err
	}
	return hostChecksView(srv.Name, ptr(row.CheckedAt.UTC()), row.Error, decodeHostChecks(row.Results), dismissed), nil
}

// CheckHostNow checks a server's host now and returns the results; with
// a check under way, it waits for that one. Operators with
// servers.manage.
func (c *Core) CheckHostNow(ctx context.Context, a Actor, server string) (apitypes.HostChecks, error) {
	if err := a.requirePlatform(ServersManage, "checking a server's host"); err != nil {
		return apitypes.HostChecks{}, err
	}
	srv, err := c.loadServer(ctx, server)
	if err != nil {
		return apitypes.HostChecks{}, err
	}
	if c.connected(srv.ID) == nil {
		return apitypes.HostChecks{}, conflict("server %s is offline; its host is checked once it is back", srv.Name)
	}
	done, ok := c.claimHostCheck(srv.ID, now(), false)
	if !ok {
		c.hostChecks.mu.Lock()
		running := c.hostChecks.done[srv.ID]
		c.hostChecks.mu.Unlock()
		if running != nil {
			select {
			case <-running:
			case <-ctx.Done():
				return apitypes.HostChecks{}, ctx.Err()
			}
		}
		return c.hostChecksOf(ctx, srv)
	}
	defer c.releaseHostCheck(srv.ID, done)
	out, err := c.runHostCheck(ctx, srv)
	if err != nil {
		return out, err
	}
	c.audit(ctx, a.platform(), "server.host_check", nil, nil, srv.Name, "checked the host of server "+srv.Name)
	return out, nil
}

// DismissHostFinding dismisses one of a server's findings with a reason:
// it stops counting and alerting until restored. Operators with
// servers.manage.
func (c *Core) DismissHostFinding(ctx context.Context, a Actor, server string, req apitypes.DismissHostFindingRequest) (apitypes.HostChecks, error) {
	if err := a.requirePlatform(ServersManage, "dismissing a host check finding"); err != nil {
		return apitypes.HostChecks{}, err
	}
	srv, err := c.loadServer(ctx, server)
	if err != nil {
		return apitypes.HostChecks{}, err
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || len(reason) > 500 {
		return apitypes.HostChecks{}, invalid("say why the finding doesn't need fixing, in at most 500 characters")
	}
	cur, err := c.hostChecksOf(ctx, srv)
	if err != nil {
		return apitypes.HostChecks{}, err
	}
	if !slices.ContainsFunc(cur.Checks, func(h apitypes.HostCheck) bool {
		return h.Key == req.Finding && h.Status == apitypes.HostCheckFinding
	}) {
		return apitypes.HostChecks{}, notFound("server %s has no finding %q; `dock server checks %s` lists them", srv.Name, req.Finding, srv.Name)
	}
	err = c.store.DismissHostFinding(ctx, store.DismissHostFindingParams{
		ServerID: srv.ID, Finding: req.Finding, Reason: reason, DismissedBy: a.Name, DismissedAt: now().UTC(),
	})
	if err != nil {
		return apitypes.HostChecks{}, err
	}
	c.audit(ctx, a.platform(), "server.host_finding_dismiss", nil, nil, srv.Name,
		fmt.Sprintf("dismissed host check finding %s on server %s: %s", req.Finding, srv.Name, reason))
	return c.afterDismissal(ctx, srv)
}

// RestoreHostFinding undoes a dismissal: the finding counts and alerts
// again. Operators with servers.manage.
func (c *Core) RestoreHostFinding(ctx context.Context, a Actor, server string, req apitypes.RestoreHostFindingRequest) (apitypes.HostChecks, error) {
	if err := a.requirePlatform(ServersManage, "restoring a host check finding"); err != nil {
		return apitypes.HostChecks{}, err
	}
	srv, err := c.loadServer(ctx, server)
	if err != nil {
		return apitypes.HostChecks{}, err
	}
	n, err := c.store.RestoreHostFinding(ctx, store.RestoreHostFindingParams{ServerID: srv.ID, Finding: req.Finding})
	if err != nil {
		return apitypes.HostChecks{}, err
	}
	if n == 0 {
		return apitypes.HostChecks{}, notFound("finding %q on server %s isn't dismissed", req.Finding, srv.Name)
	}
	c.audit(ctx, a.platform(), "server.host_finding_restore", nil, nil, srv.Name,
		fmt.Sprintf("restored host check finding %s on server %s", req.Finding, srv.Name))
	return c.afterDismissal(ctx, srv)
}

// afterDismissal reads a server's results again and brings its alert in
// line with what is open now.
func (c *Core) afterDismissal(ctx context.Context, srv store.Server) (apitypes.HostChecks, error) {
	out, err := c.hostChecksOf(ctx, srv)
	if err != nil {
		return out, err
	}
	if out.CheckedAt != nil {
		c.hostAlert(ctx, srv, out)
	}
	return out, nil
}
