package core

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// The errors page and a service's Errors tab (ADR 0062): an org's error
// issues, read in a fixed number of queries however many services it
// has: the issues, the org's hourly counts for their trends, and the
// facets. Issues of archived services, environments and projects are
// hidden.

// List sizes.
const (
	defaultErrorList = 200
	maxErrorList     = 500
	// errorTrendHours is how many hours an issue's trend has.
	errorTrendHours = 24
	// errorQuietFor resolves an open issue without an occurrence, and
	// errorKeepFor deletes a resolved one (retainErrors).
	errorQuietFor = 7 * 24 * time.Hour
	errorKeepFor  = 30 * 24 * time.Hour
)

// ErrorFilter picks the issues ListErrors returns. Service needs Project
// and Environment; Environment alone is that environment of every
// project.
type ErrorFilter struct {
	Project, Environment, Service string
	// State is open (the default: not resolved), resolved or all.
	State string
	// Limit is how many issues at most: defaultErrorList by default,
	// maxErrorList at most.
	Limit int
}

// errorStates maps a filter's state to the queries' state parameter.
var errorStates = map[string]int64{"": 1, "open": 1, "resolved": 2, "all": 0}

// issueRow is an issue as the list queries return it.
type issueRow = store.ListOrgErrorIssuesRow

// ListErrors returns the issues of the actor's org that match f, new
// first, then regressed, then ongoing (and resolved), each by last seen;
// and the facets of every live service with open issues in f's
// environment, whatever project and service f names. The list holds the
// most recently seen issues of the filter, at most f.Limit of them
// (Truncated says there were more). A token limited to a project or an
// environment sees only issues there.
func (c *Core) ListErrors(ctx context.Context, a Actor, f ErrorFilter) (apitypes.ErrorList, error) {
	const action = "reading errors"
	if err := a.require(apitypes.ScopeRead, action); err != nil {
		return apitypes.ErrorList{}, err
	}
	state, ok := errorStates[f.State]
	if !ok {
		return apitypes.ErrorList{}, invalid("state %q: pass open, resolved or all", f.State)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultErrorList
	}
	limit = min(limit, maxErrorList)
	rows, err := c.errorIssueRows(ctx, a, f, state, int64(limit+1))
	if err != nil {
		return apitypes.ErrorList{}, err
	}
	// Errors are logs: logs.read where each one is (ADR 0080).
	kinds, err := c.envKinds(ctx, a)
	if err != nil {
		return apitypes.ErrorList{}, err
	}
	reads := func(project, env int64) bool {
		return a.canSee(a.OrgID, project, env) && a.holdsAt(PermLogsRead, project, env, kinds[env])
	}
	rows = slices.DeleteFunc(rows, func(r issueRow) bool { return !reads(r.ProjectID, r.EnvironmentID) })
	out := apitypes.ErrorList{Issues: make([]apitypes.ErrorIssue, 0, min(len(rows), limit)), Facets: []apitypes.ErrorFacet{}}
	if len(rows) > limit {
		rows, out.Truncated = rows[:limit], true
	}

	t := now()
	since := trendStart(t)
	counts, err := c.store.ListOrgErrorCounts(ctx, store.ListOrgErrorCountsParams{SinceHour: since, OrgID: a.OrgID, SeenSince: time.Unix(since, 0).UTC()})
	if err != nil {
		return apitypes.ErrorList{}, err
	}
	trends := make(map[int64][]int64, len(rows))
	for _, r := range rows {
		trends[r.ID] = nil
	}
	for _, n := range counts {
		if tr, ok := trends[n.IssueID]; ok {
			if tr == nil {
				tr = make([]int64, errorTrendHours)
				trends[n.IssueID] = tr
			}
			addTrend(tr, since, n.Hour, n.Count)
		}
	}
	for _, r := range rows {
		out.Issues = append(out.Issues, toErrorIssue(r, trends[r.ID], t))
	}
	slices.SortStableFunc(out.Issues, func(x, y apitypes.ErrorIssue) int {
		return cmp.Or(cmp.Compare(stateRank(x.State), stateRank(y.State)), y.LastSeen.Compare(x.LastSeen))
	})

	env := f.Environment
	if a.EnvironmentID != 0 {
		env = a.EnvironmentName
	}
	facets, err := c.store.ListErrorFacets(ctx, store.ListErrorFacetsParams{
		NewSince: t.Add(-errorNewFor), SinceHour: since, OrgID: a.OrgID, Environment: env,
	})
	if err != nil {
		return apitypes.ErrorList{}, err
	}
	for _, r := range facets {
		if reads(r.ProjectID, r.EnvironmentID) {
			out.Facets = append(out.Facets, apitypes.ErrorFacet{
				Project: r.Project, Environment: r.Environment, Service: r.Service,
				Issues: int(r.Issues), New: int(r.NewIssues), Events: r.Events,
			})
		}
	}
	slices.SortFunc(out.Facets, func(x, y apitypes.ErrorFacet) int {
		return cmp.Or(cmp.Compare(x.Project, y.Project), cmp.Compare(x.Environment, y.Environment), cmp.Compare(x.Service, y.Service))
	})
	return out, nil
}

// errorIssueRows runs the list query f's shape needs: a service's, a
// project's (also for a token limited to one) or the org's.
func (c *Core) errorIssueRows(ctx context.Context, a Actor, f ErrorFilter, state, maxRows int64) ([]issueRow, error) {
	const action = "reading errors"
	switch {
	case f.Service != "":
		if f.Project == "" || f.Environment == "" {
			return nil, invalid("a service's errors need its project and environment too")
		}
		ss, err := c.serviceFor(ctx, a, ServiceRef{f.Project, f.Environment, f.Service}, apitypes.ScopeRead, action)
		if err != nil {
			return nil, err
		}
		rows, err := c.store.ListServiceErrorIssues(ctx, store.ListServiceErrorIssuesParams{ServiceID: ss.svc.ID, State: state, MaxRows: maxRows})
		return convertRows(rows, func(r store.ListServiceErrorIssuesRow) issueRow { return issueRow(r) }), err
	case f.Project != "" || a.ProjectID != 0:
		id := a.ProjectID
		if f.Project != "" {
			p, err := c.projectFor(ctx, a, f.Project, apitypes.ScopeRead, action)
			if err != nil {
				return nil, err
			}
			id = p.ID
		}
		env := f.Environment
		if a.EnvironmentID != 0 {
			env = a.EnvironmentName
		}
		rows, err := c.store.ListProjectErrorIssues(ctx, store.ListProjectErrorIssuesParams{ProjectID: id, Environment: env, State: state, MaxRows: maxRows})
		return convertRows(rows, func(r store.ListProjectErrorIssuesRow) issueRow { return issueRow(r) }), err
	}
	return c.store.ListOrgErrorIssues(ctx, store.ListOrgErrorIssuesParams{OrgID: a.OrgID, Environment: f.Environment, State: state, MaxRows: maxRows})
}

// convertRows converts one query's rows to another's of the same shape.
func convertRows[T, U any](rows []T, conv func(T) U) []U {
	out := make([]U, len(rows))
	for i, r := range rows {
		out[i] = conv(r)
	}
	return out
}

// GetError returns one issue of the actor's org with its latest sample.
func (c *Core) GetError(ctx context.Context, a Actor, id int64) (apitypes.ErrorIssueDetail, error) {
	if err := a.require(apitypes.ScopeRead, "reading errors"); err != nil {
		return apitypes.ErrorIssueDetail{}, err
	}
	r, err := c.errorIssue(ctx, a, id)
	if err != nil {
		return apitypes.ErrorIssueDetail{}, err
	}
	d, err := c.errorDetail(ctx, r)
	if err == nil {
		d.LatestTrace = c.errorTrace(ctx, r.ErrorIssue)
	}
	return d, err
}

// errorTrace is a log issue's newest trace: a span whose exception has
// the issue's fingerprint, from an hour before it was last seen to five
// minutes after (spans start before the line is logged, and SDKs send
// them seconds later). nil when tracing is off, nothing matched or the
// trace store isn't answering: the issue reads without it.
func (c *Core) errorTrace(ctx context.Context, i store.ErrorIssue) *apitypes.TraceRef {
	cl, _ := c.tracesClient()
	if cl == nil || apitypes.ErrorSource(i.Source) != apitypes.ErrorFromLogs {
		return nil
	}
	res, err := cl.ErrorTrace(ctx, analytics.ErrorTraceQuery{
		Org: i.OrgID, Services: []int64{i.ServiceID}, Fingerprint: i.Fingerprint,
		From: i.LastSeen.Add(-time.Hour).UnixMicro(), To: i.LastSeen.Add(5 * time.Minute).UnixMicro(),
	})
	if err != nil || res.Trace.IsZero() {
		return nil
	}
	return &apitypes.TraceRef{ID: res.Trace.String(), At: time.UnixMicro(res.Start).UTC()}
}

// errorDetail is a loaded issue with its trend and sample.
func (c *Core) errorDetail(ctx context.Context, r store.GetErrorIssueRow) (apitypes.ErrorIssueDetail, error) {
	t := now()
	since := trendStart(t)
	counts, err := c.store.ListErrorIssueCounts(ctx, store.ListErrorIssueCountsParams{IssueID: r.ErrorIssue.ID, Hour: since})
	if err != nil {
		return apitypes.ErrorIssueDetail{}, err
	}
	var trend []int64
	if len(counts) > 0 {
		trend = make([]int64, errorTrendHours)
		for _, n := range counts {
			addTrend(trend, since, n.Hour, n.Count)
		}
	}
	i := r.ErrorIssue
	row := issueRow{
		ID: i.ID, Fingerprint: i.Fingerprint, Source: i.Source, Title: i.Title, Culprit: i.Culprit, Count: i.Count,
		FirstSeen: i.FirstSeen, LastSeen: i.LastSeen, FirstRelease: i.FirstRelease, LastRelease: i.LastRelease,
		ResolvedAt: i.ResolvedAt, ResolvedBy: i.ResolvedBy, RegressedAt: i.RegressedAt,
		ProjectID: i.ProjectID, EnvironmentID: i.EnvironmentID, ServiceID: i.ServiceID,
		Project: r.Project, Environment: r.Environment, Service: r.Service, ActiveRelease: r.ActiveRelease,
	}
	return apitypes.ErrorIssueDetail{ErrorIssue: toErrorIssue(row, trend, t), Sample: i.Sample}, nil
}

// errorIssue loads an issue the actor may see: another org's, or one of
// an archived service, is not found.
func (c *Core) errorIssue(ctx context.Context, a Actor, id int64) (store.GetErrorIssueRow, error) {
	r, err := c.store.GetErrorIssue(ctx, id)
	missing := notFound("error %d not found; `dock errors` lists the open ones", id)
	if store.IsNotFound(err) {
		return r, missing
	}
	if err != nil {
		return r, err
	}
	i := r.ErrorIssue
	if r.Archived != 0 || !a.canSee(i.OrgID, i.ProjectID, i.EnvironmentID) || a.OrgID != 0 && a.OrgID != i.OrgID {
		return r, missing
	}
	// Errors are logs: logs.read where it is (ADR 0080).
	kind, err := c.envKindOf(ctx, a, i.EnvironmentID)
	if err != nil {
		return r, err
	}
	if !a.holdsAt(PermLogsRead, i.ProjectID, i.EnvironmentID, kind) {
		return r, missing
	}
	return r, nil
}

// ResolveError marks an issue resolved by the actor (deploy scope in its
// environment, which a lock holds tokens to), and resolves its alerts.
// If it is seen again it regresses. Resolving a resolved issue changes
// nothing.
func (c *Core) ResolveError(ctx context.Context, a Actor, id int64) (apitypes.ErrorIssue, error) {
	const action = "resolving errors"
	if err := a.requireScope(apitypes.ScopeDeploy, action); err != nil {
		return apitypes.ErrorIssue{}, err
	}
	r, err := c.errorIssue(ctx, a, id)
	if err != nil {
		return apitypes.ErrorIssue{}, err
	}
	i := r.ErrorIssue
	p, err := c.store.GetProject(ctx, i.ProjectID)
	if err != nil {
		return apitypes.ErrorIssue{}, err
	}
	e, err := c.store.GetEnvironment(ctx, i.EnvironmentID)
	if err != nil {
		return apitypes.ErrorIssue{}, err
	}
	if err := a.checkEnv(apitypes.ScopeDeploy, action, p, e); err != nil {
		return apitypes.ErrorIssue{}, err
	}
	if i.ResolvedAt == nil {
		t := now()
		n, err := c.store.ResolveErrorIssue(ctx, store.ResolveErrorIssueParams{ResolvedAt: &t, ResolvedBy: a.Name, ID: i.ID})
		if err != nil {
			return apitypes.ErrorIssue{}, err
		}
		if n > 0 {
			c.resolveIssueAlerts(ctx, i.ServiceID, i.ID, t)
			c.audit(ctx, a, "error.resolve", &p, &e, serviceKey(p.Name, e.Name, r.Service)+" error "+strconv.FormatInt(i.ID, 10),
				"resolved error: "+cutText(i.Title, 120))
		}
		if r, err = c.store.GetErrorIssue(ctx, id); err != nil {
			return apitypes.ErrorIssue{}, err
		}
	}
	d, err := c.errorDetail(ctx, r)
	return d.ErrorIssue, err
}

// trendStart is the start (Unix seconds) of the first hour of a trend
// that ends with now's hour.
func trendStart(t time.Time) int64 {
	return t.Truncate(time.Hour).Unix() - (errorTrendHours-1)*3600
}

// addTrend adds an hour's count to a trend starting at since.
func addTrend(trend []int64, since, hour, n int64) {
	if i := (hour - since) / 3600; i >= 0 && i < int64(len(trend)) {
		trend[i] += n
	}
}

// errorState is where an issue stands: resolved, regressed (seen again
// after it was resolved, until it is resolved again), new (first seen in
// the service's active release, or in the last 24 hours) or ongoing.
func errorState(r issueRow, t time.Time) apitypes.ErrorState {
	switch {
	case r.ResolvedAt != nil:
		return apitypes.ErrorResolved
	case r.RegressedAt != nil:
		return apitypes.ErrorRegressed
	case r.FirstRelease != 0 && r.FirstRelease == r.ActiveRelease, t.Sub(r.FirstSeen) < errorNewFor:
		return apitypes.ErrorNew
	}
	return apitypes.ErrorOngoing
}

// stateRank orders states as a list shows them.
func stateRank(s apitypes.ErrorState) int {
	return slices.Index([]apitypes.ErrorState{apitypes.ErrorNew, apitypes.ErrorRegressed, apitypes.ErrorOngoing, apitypes.ErrorResolved}, s)
}

// toErrorIssue converts a row; a nil trend is a day without errors.
func toErrorIssue(r issueRow, trend []int64, t time.Time) apitypes.ErrorIssue {
	if trend == nil {
		trend = make([]int64, errorTrendHours)
	}
	return apitypes.ErrorIssue{
		ID: r.ID, Fingerprint: r.Fingerprint, Source: apitypes.ErrorSource(r.Source), State: errorState(r, t),
		Title: r.Title, Culprit: r.Culprit, Project: r.Project, Environment: r.Environment, Service: r.Service,
		Count: r.Count, Trend: trend, FirstSeen: r.FirstSeen, LastSeen: r.LastSeen,
		FirstRelease: r.FirstRelease, LastRelease: r.LastRelease, ResolvedAt: r.ResolvedAt, ResolvedBy: r.ResolvedBy,
	}
}

// retainErrors runs on the retention pass: open issues without an
// occurrence for errorQuietFor resolve by themselves, resolved ones not
// seen for errorKeepFor go, and hourly counts past errorQuietFor are
// pruned.
func (c *Core) retainErrors(ctx context.Context, t time.Time) (resolved, pruned int64, err error) {
	if resolved, err = c.store.AutoResolveErrorIssues(ctx, store.AutoResolveErrorIssuesParams{Now: ptr(t.UTC()), Cutoff: t.Add(-errorQuietFor).UTC()}); err != nil {
		return 0, 0, err
	}
	if pruned, err = c.store.PruneErrorIssues(ctx, t.Add(-errorKeepFor).UTC()); err != nil {
		return resolved, 0, err
	}
	_, err = c.store.PruneErrorCounts(ctx, t.Add(-errorQuietFor).Unix())
	return resolved, pruned, err
}

// releaseNewErrors counts a service's issues first seen in a release.
func (c *Core) releaseNewErrors(ctx context.Context, serviceID, number int64) (int, error) {
	n, err := c.store.CountReleaseNewErrors(ctx, store.CountReleaseNewErrorsParams{ServiceID: serviceID, FirstRelease: number})
	return int(n), err
}
