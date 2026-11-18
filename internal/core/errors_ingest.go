package core

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/errparse"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Error issues (ADR 0062, "One place for errors"): errors from three
// sources, grouped by fingerprint per service.
//
//   - logs: log followers find errors in containers' output as it is
//     written (errors_logs.go);
//   - frontend: browser errors Dock Analytics collected, read from each
//     batch the forwarder sends the store (frontendErrors), grouped by the
//     store's own error group ("fe:<group>"), so counts are exact;
//   - traffic: a minute in which a service answered at least
//     trafficErrorMin requests with 5xx, and at least 1 in
//     trafficErrorShare of its requests (trafficErrors).
//
// A release that fails before it goes live is a source too, but for the
// noise of the failed deploy itself: what the app logged about Dock's
// health probe getting a 404, and what it wrote while Dock stopped it.
// Its log errors wait in the sink while it deploys (logFate).
//
// Each source adds to the error sink in memory without blocking. On the
// statistics tick (once a minute, nothing of its own) the sink's issues
// are written in one transaction: a new fingerprint opens an issue, a
// known one counts again and a resolved one regresses. New and regressed
// issues raise alerts. The sink is bounded per service and in all, and a
// batch opens at most maxNewIssues issues per service, so a flood of
// unique errors can't blow up memory or the table.

// Bounds on the sink and the table.
const (
	// maxPendingPerService and maxPending bound the sink's fingerprints
	// between two ticks; errors of fingerprints past them are dropped.
	maxPendingPerService = 50
	maxPending           = 2000
	// maxNewIssues is how many issues one tick opens per service, and
	// maxServiceIssues how many a service may have in all.
	maxNewIssues     = 10
	maxServiceIssues = 1000

	// trafficErrorMin and trafficErrorShare: a minute with at least this
	// many 5xx answers, and at least this share of all its requests, is
	// a traffic error.
	trafficErrorMin   = 5
	trafficErrorShare = 0.05

	// errorNewFor is how long an issue is new after it was first seen,
	// and how long its new or regressed alert stays open.
	errorNewFor = 24 * time.Hour
	// errorAlertsPerService caps a service's open error alerts, so a
	// broken deploy's many new errors are a few alerts, not dozens.
	errorAlertsPerService = 3
)

// Fingerprints of the sources other than logs (errparse's are 16 hex
// digits).
const (
	trafficFingerprint = "traffic:5xx"
	frontendPrefix     = "fe:"
	// errorAlertKey prefixes error issues' alert keys:
	// errors:<service ID>:new:<issue ID> and errors:<service>:regressed:<issue>.
	errorAlertKey = "errors:"
)

// errorSink holds the errors seen since the last tick, by service and
// fingerprint. Sources add to it from any goroutine.
type errorSink struct {
	mu      sync.Mutex
	pending map[errorKey]*pendingError
	// perService counts each service's fingerprints in pending.
	perService map[int64]int
	// dropped counts errors past the bounds since the last tick.
	dropped int
}

// errorKey is one issue: a service's fingerprint.
type errorKey struct {
	service     int64
	fingerprint string
}

// pendingError is an issue's errors since the last tick: how many, when,
// and the latest one's title, culprit and sample.
type pendingError struct {
	source                 apitypes.ErrorSource
	title, culprit, sample string
	count                  int64
	first, last            time.Time
	// release is the service's release number they were seen in, or 0;
	// releaseID the release's ID when the number isn't known (frontend
	// errors), and server the server whose spool they came from, which
	// must be the service's.
	release, releaseID, server int64
}

// add counts errors of a fingerprint in a service, never blocking: past
// the sink's bounds, a new fingerprint's errors are dropped.
func (s *errorSink) add(service int64, fingerprint string, e pendingError) {
	k := errorKey{service, fingerprint}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.pending[k]; p != nil {
		p.count += e.count
		if e.first.Before(p.first) {
			p.first = e.first
		}
		if !e.last.Before(p.last) {
			p.last, p.title, p.culprit, p.sample = e.last, e.title, e.culprit, e.sample
			p.release, p.releaseID, p.server = e.release, e.releaseID, e.server
		}
		return
	}
	if s.perService[service] >= maxPendingPerService || len(s.pending) >= maxPending {
		s.dropped++
		return
	}
	if s.pending == nil {
		s.pending, s.perService = map[errorKey]*pendingError{}, map[int64]int{}
	}
	s.pending[k] = &e
	s.perService[service]++
}

// take returns what the sink holds and empties it.
func (s *errorSink) take() (map[errorKey]*pendingError, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out, dropped := s.pending, s.dropped
	s.pending, s.perService, s.dropped = nil, nil, 0
	return out, dropped
}

// errorsTick runs on the statistics tick: errors still waiting for a
// line end, the sink's issues are written, new and regressed ones alert,
// and error alerts a day old resolve. With nothing in the sink it writes
// nothing.
func (c *Core) errorsTick(ctx context.Context, now time.Time) {
	c.flushQuietErrors(now)
	batch, dropped := c.issues.take()
	if dropped > 0 {
		c.log.Warn("errors: dropped errors past the sink's bounds", "errors", dropped)
	}
	if len(batch) > 0 {
		alerts, err := c.writeErrors(ctx, batch, now)
		if err != nil {
			c.log.Warn("errors: store issues", "err", err)
		}
		for _, a := range alerts {
			c.issueAlert(ctx, a, now)
		}
	}
	c.stops.prune(now)
	c.expireErrorAlerts(ctx, now)
}

// issueEvent is an issue a tick opened or reopened, for its alert.
type issueEvent struct {
	issue     store.ErrorIssue
	target    store.GetErrorTargetRow
	regressed bool
}

// writeErrors writes a tick's issues in one transaction and returns the
// issues that are new (not frontend ones: Dock Analytics alerts on those
// itself) or regressed. Log errors of a release still deploying are put
// back into the sink, and a failed deploy's noise is dropped (logFate).
func (c *Core) writeErrors(ctx context.Context, batch map[errorKey]*pendingError, now time.Time) ([]issueEvent, error) {
	var events []issueEvent
	var held []errorKey
	dropped := 0
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		events, held, dropped = events[:0], held[:0], 0
		targets := map[int64]*store.GetErrorTargetRow{}
		fates := map[releaseKey]logFate{}
		opened := map[int64]int{}
		for k, p := range batch {
			t, ok := targets[k.service]
			if !ok {
				row, err := q.GetErrorTarget(ctx, k.service)
				if err != nil && !store.IsNotFound(err) {
					return err
				}
				if err == nil && row.Archived == 0 {
					t = &row
				}
				targets[k.service] = t
			}
			if t == nil || p.server != 0 && p.server != t.ServerID {
				continue // gone, archived, or another server's
			}
			release, err := errorRelease(ctx, q, k.service, p, t)
			if err != nil {
				return err
			}
			if p.source == apitypes.ErrorFromLogs && release != 0 && release != t.ActiveRelease {
				rk := releaseKey{k.service, release}
				fate, ok := fates[rk]
				if !ok {
					if fate, err = c.logErrorFate(ctx, q, rk); err != nil {
						return err
					}
					fates[rk] = fate
				}
				if fate.held {
					held = append(held, k)
					continue
				}
				if fate.noise(p) {
					continue
				}
			}
			cur, err := q.GetErrorIssueByFingerprint(ctx, store.GetErrorIssueByFingerprintParams{ServiceID: k.service, Fingerprint: k.fingerprint})
			switch {
			case store.IsNotFound(err):
				if opened[k.service] >= maxNewIssues {
					dropped++
					continue
				}
				if opened[k.service] == 0 {
					n, err := q.CountServiceErrorIssues(ctx, k.service)
					if err != nil {
						return err
					}
					if n >= maxServiceIssues {
						opened[k.service] = maxNewIssues
						dropped++
						continue
					}
				}
				opened[k.service]++
				cur, err = q.CreateErrorIssue(ctx, store.CreateErrorIssueParams{
					OrgID: t.OrgID, ProjectID: t.ProjectID, EnvironmentID: t.EnvironmentID, ServiceID: k.service,
					Fingerprint: k.fingerprint, Source: string(p.source), Title: p.title, Culprit: p.culprit, Sample: p.sample,
					Count: p.count, FirstSeen: p.first.UTC(), LastSeen: p.last.UTC(), FirstRelease: release, LastRelease: release,
				})
				if err != nil {
					return err
				}
				if p.source != apitypes.ErrorFromFrontend {
					events = append(events, issueEvent{issue: cur, target: *t})
				}
			case err != nil:
				return err
			default:
				upd := store.UpdateErrorIssueSeenParams{
					ID: cur.ID, Count: cur.Count + p.count, Title: cur.Title, Culprit: cur.Culprit, Sample: cur.Sample,
					LastSeen: cur.LastSeen, LastRelease: cur.LastRelease, RegressedAt: cur.RegressedAt,
				}
				if !p.last.Before(cur.LastSeen) {
					upd.Title, upd.Culprit, upd.Sample, upd.LastSeen = p.title, p.culprit, p.sample, p.last.UTC()
					if release != 0 {
						upd.LastRelease = release
					}
				}
				regressed := cur.ResolvedAt != nil
				if regressed {
					at := now.UTC()
					upd.RegressedAt = &at
				}
				if err := q.UpdateErrorIssueSeen(ctx, upd); err != nil {
					return err
				}
				if regressed {
					// Its alert names the occurrence that came back.
					cur.ResolvedAt, cur.ResolvedBy, cur.RegressedAt, cur.LastRelease = nil, "", upd.RegressedAt, upd.LastRelease
					cur.Title, cur.Culprit, cur.Sample, cur.LastSeen = upd.Title, upd.Culprit, upd.Sample, upd.LastSeen
					events = append(events, issueEvent{issue: cur, target: *t, regressed: true})
				}
			}
			hour := p.last.Unix() - p.last.Unix()%3600
			if err := q.AddErrorCount(ctx, store.AddErrorCountParams{IssueID: cur.ID, Hour: hour, Count: p.count}); err != nil {
				return err
			}
		}
		return nil
	})
	if dropped > 0 {
		c.log.Warn("errors: services had more new issues than a minute opens; their errors were dropped", "issues", dropped)
	}
	if err == nil {
		for _, k := range held {
			c.issues.add(k.service, k.fingerprint, *batch[k])
		}
	}
	return events, err
}

// releaseKey names a service's release by its number.
type releaseKey struct{ service, number int64 }

// logFate is what a tick does with the log errors of a release other
// than the active one.
type logFate struct {
	// held: the release is still deploying. Its errors go back into the
	// sink until a tick finds it live or failed: only then is it known
	// which of them are noise.
	held bool
	// failed: the release failed or was canceled before it went live. Its
	// errors are issues like any other, but for the noise of the failed
	// deploy itself (noise).
	failed bool
	// healthPath is the failed release's health check path, or "".
	healthPath string
	// stopAt is when Dock began to stop the failed release's container,
	// or zero when that isn't known (the daemon restarted since).
	stopAt time.Time
}

// noise reports whether p is noise of the failed deploy and no error of
// the app: what it logged about Dock's own health probe getting a 404
// (the deploy's failure says that already, with a diagnosis), and what it
// first wrote while Dock was stopping it.
func (f logFate) noise(p *pendingError) bool {
	if !f.failed {
		return false
	}
	if !f.stopAt.IsZero() && !p.first.Before(f.stopAt) {
		return true
	}
	return probe404(p.title, f.healthPath) || probe404(p.sample, f.healthPath)
}

// probe404 reports whether text is a line about a request to path
// answered with 404: path as a whole (not a longer path's start) and the
// status as a word.
func probe404(text, path string) bool {
	if path == "" || !hasWord(text, "404", isDigit) {
		return false
	}
	return hasWord(text, path, func(b byte) bool {
		return b == '/' || b == '-' || b == '_' || b == '.' || isDigit(b) || b|0x20 >= 'a' && b|0x20 <= 'z'
	})
}

// isDigit reports whether b is an ASCII digit.
func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// hasWord reports whether text holds word with no byte that part accepts
// right after it, nor (unless word starts with '/') right before it.
func hasWord(text, word string, part func(byte) bool) bool {
	for from := 0; ; {
		i := strings.Index(text[from:], word)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(word)
		before := start == 0 || word[0] == '/' || !part(text[start-1])
		if before && (end == len(text) || !part(text[end])) {
			return true
		}
		from = start + 1
	}
}

// logErrorFate decides what happens to the log errors of a release other
// than the active one. A release that is gone counts as one that was
// live, as it did before releases were looked at.
func (c *Core) logErrorFate(ctx context.Context, q *store.Queries, k releaseKey) (logFate, error) {
	r, err := q.GetReleaseByNumber(ctx, store.GetReleaseByNumberParams{ServiceID: k.service, Number: k.number})
	if store.IsNotFound(err) {
		return logFate{}, nil
	}
	if err != nil {
		return logFate{}, err
	}
	switch status := apitypes.ReleaseStatus(r.Status); {
	case status == apitypes.ReleaseFailed || status == apitypes.ReleaseCanceled:
		f := logFate{failed: true, stopAt: c.stops.at(k)}
		// Without its settings only the probe's lines can't be told.
		if cfg, err := c.openConfig(r); err == nil {
			f.healthPath = cfg.HealthCheckPath
		}
		return f, nil
	case !status.Done():
		return logFate{held: true}, nil
	}
	return logFate{}, nil
}

// releaseStops remembers when Dock began to stop the container of a
// release that failed, for the ticks that judge its last log errors
// (logFate.noise). In memory only and pruned by the statistics tick: a
// daemon restart forgets them, and those errors then count as the app's.
type releaseStops struct {
	mu sync.Mutex
	m  map[releaseKey]time.Time
}

// stopsKept is how long a stop is remembered: longer than a container
// takes to stop plus the ticks its last lines wait in the sink.
const stopsKept = 10 * time.Minute

// mark records that k's container is being stopped from t on.
func (s *releaseStops) mark(k releaseKey, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[releaseKey]time.Time{}
	}
	s.m[k] = t
}

// at is when k's container began to stop, or zero.
func (s *releaseStops) at(k releaseKey) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[k]
}

// prune forgets the stops from before now - stopsKept.
func (s *releaseStops) prune(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, t := range s.m {
		if now.Sub(t) > stopsKept {
			delete(s.m, k)
		}
	}
}

// errorRelease is the release number errors were seen in: the one they
// came with, the release ID's (of the service only), or for traffic and
// unknown releases the service's active one.
func errorRelease(ctx context.Context, q *store.Queries, service int64, p *pendingError, t *store.GetErrorTargetRow) (int64, error) {
	if p.release != 0 {
		return p.release, nil
	}
	if p.releaseID != 0 {
		r, err := q.GetRelease(ctx, p.releaseID)
		switch {
		case err == nil && r.ServiceID == service:
			return r.Number, nil
		case err != nil && !store.IsNotFound(err):
			return 0, err
		}
	}
	return t.ActiveRelease, nil
}

// issueAlert raises a new or regressed issue's alert, unless its service
// has errorAlertsPerService open already.
func (c *Core) issueAlert(ctx context.Context, ev issueEvent, now time.Time) {
	t, i := ev.target, ev.issue
	prefix := errorAlertKey + strconv.FormatInt(i.ServiceID, 10) + ":"
	e := c.alerts
	e.mu.Lock()
	open := 0
	for k := range e.open {
		if strings.HasPrefix(k, prefix) {
			open++
		}
	}
	e.mu.Unlock()
	if open >= errorAlertsPerService {
		c.log.Info("errors: an issue without an alert; its service has enough open", "issue", i.ID, "service", i.ServiceID)
		return
	}
	where := serviceKey(t.Project, t.Environment, t.Service)
	s := alertSignal{
		Kind: apitypes.AlertNewError, Severity: apitypes.SeverityWarning,
		Key:     prefix + "new:" + strconv.FormatInt(i.ID, 10),
		Title:   "New error in " + where + ": " + cutText(i.Title, 120),
		Message: "First seen " + inRelease(i.FirstRelease) + ".",
		Command: "dock errors show " + strconv.FormatInt(i.ID, 10),
		Target:  &apitypes.AlertTarget{ErrorIssue: i.ID, Release: i.FirstRelease},
		Project: t.Project, Environment: t.Environment, Service: t.Service, projectID: t.ProjectID, serviceID: i.ServiceID,
	}
	if ev.regressed {
		s.Kind, s.Key = apitypes.AlertErrorRegressed, prefix+"regressed:"+strconv.FormatInt(i.ID, 10)
		s.Title = "Error back in " + where + ": " + cutText(i.Title, 120)
		s.Message = "It was resolved, and was seen again " + inRelease(i.LastRelease) + "."
		s.Target.Release = i.LastRelease
	}
	c.raiseAlert(ctx, s, now)
}

// inRelease says which release something was seen in.
func inRelease(n int64) string {
	if n == 0 {
		return "with no release running"
	}
	return "in release " + strconv.FormatInt(n, 10)
}

// expireErrorAlerts resolves error issues' alerts open for errorNewFor.
func (c *Core) expireErrorAlerts(ctx context.Context, now time.Time) {
	e := c.alerts
	var old []string
	e.mu.Lock()
	for k, a := range e.open {
		if strings.HasPrefix(k, errorAlertKey) && now.Sub(a.OpenedAt) >= errorNewFor {
			old = append(old, k)
		}
	}
	e.mu.Unlock()
	for _, k := range old {
		c.resolveAlert(ctx, k, now)
	}
}

// resolveIssueAlerts resolves an issue's alerts, as it is resolved: its
// own, and for a frontend issue the "new error" alert Dock Analytics
// raised for its group on its environment's site (newErrorAlert), whose
// regression would otherwise alert beside it.
func (c *Core) resolveIssueAlerts(ctx context.Context, i store.ErrorIssue, env string, now time.Time) {
	prefix := errorAlertKey + strconv.FormatInt(i.ServiceID, 10) + ":"
	id := ":" + strconv.FormatInt(i.ID, 10)
	for _, kind := range []string{"new", "regressed"} {
		c.resolveAlert(ctx, prefix+kind+id, now)
	}
	group, ok := strings.CutPrefix(i.Fingerprint, frontendPrefix)
	if !ok {
		return
	}
	var site []string
	e := c.alerts
	e.mu.Lock()
	for k, a := range e.open {
		if strings.HasPrefix(k, newErrorKey) && strings.HasSuffix(k, ":"+group) && a.ProjectID != nil && *a.ProjectID == i.ProjectID && a.Environment == env {
			site = append(site, k)
		}
	}
	e.mu.Unlock()
	for _, k := range site {
		c.resolveAlert(ctx, k, now)
	}
}

// trafficErrors adds a traffic error for each service whose minute of
// traffic at (Unix seconds) had a spike of 5xx answers. trafficTick
// calls it with the counts it just stored.
func (c *Core) trafficErrors(services map[int64]*serviceTraffic, at int64) {
	minute := time.Unix(at, 0).UTC()
	for id, st := range services {
		n, req := st.counts.Status[3], st.counts.Requests
		if n < trafficErrorMin || float64(n) < trafficErrorShare*float64(req) {
			continue
		}
		sample := fmt.Sprintf("%d of %d requests answered with a 5xx status in the minute from %s UTC.",
			n, req, minute.Format("2006-01-02 15:04"))
		c.issues.add(id, trafficFingerprint, pendingError{
			source: apitypes.ErrorFromTraffic, title: "5xx answers", sample: sample,
			count: toInt64(n), first: minute, last: minute,
		})
	}
}

// spooledBrowserError is what frontendErrors reads of a spooled error
// event (analytics.Event).
type spooledBrowserError struct {
	Time    int64                `json:"t"`
	Service int64                `json:"sv"`
	Release int64                `json:"rl"`
	Kind    analytics.Kind       `json:"k"`
	Message string               `json:"n"`
	Path    string               `json:"p"`
	Error   *analytics.ErrorInfo `json:"er"`
}

// frontendErrors adds the browser errors of a batch forwarded from a
// server's spool to their services' issues, grouped as the store groups
// them (the ingest's fingerprint, "fe:<group>"). A batch without errors
// costs one scan for `"k":4`.
func (c *Core) frontendErrors(serverID int64, lines []byte) {
	if !bytes.Contains(lines, errorKind) {
		return
	}
	for line := range bytes.SplitSeq(lines, []byte{'\n'}) {
		if !bytes.Contains(line, errorKind) {
			continue
		}
		var e spooledBrowserError
		if json.Unmarshal(line, &e) != nil || e.Kind != analytics.KindError || e.Error == nil || e.Service == 0 {
			continue
		}
		title := e.Message
		if e.Error.Type != "" {
			title = e.Error.Type + ": " + e.Message
		}
		sample := e.Error.Stack
		if e.Path != "" {
			sample = "Page: " + e.Path + "\n\n" + sample
		}
		at := time.UnixMilli(e.Time).UTC()
		c.issues.add(e.Service, frontendPrefix+fmt.Sprintf("%016x", e.Error.Group), pendingError{
			source: apitypes.ErrorFromFrontend, title: cutText(title, 200), culprit: e.Error.Frame,
			sample: cutBytes(sample, errparse.MaxSample), count: 1, first: at, last: at,
			releaseID: e.Release, server: serverID,
		})
	}
}

// cutBytes cuts s to at most n bytes on a rune boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
