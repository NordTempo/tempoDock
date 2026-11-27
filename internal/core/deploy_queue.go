package core

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// The deploy queue (ADR 0086, amended 2026-11-27): one place to see what
// deploys wait for and run. Each builder builds one release at a time
// (buildSlots); the view lists, per builder, the release in its slot and
// the ones waiting in order, then the releases in flight that aren't at
// a builder. It reads the in-flight releases in one query and the
// servers in another, however many there are, and the builders' lines
// from memory. Pages keep it live from deploy_queue and release events.

// DeployQueue is the queue as the actor's org sees it: its releases the
// actor can read, and in their place at a builder another org's (or one
// the actor can't read) as hidden. Builders are labelled, not named:
// servers are the platform's (seesServers). Scope read.
func (c *Core) DeployQueue(ctx context.Context, a Actor) (apitypes.DeployQueue, error) {
	if err := a.require(apitypes.ScopeRead, "reading the deploy queue"); err != nil {
		return apitypes.DeployQueue{}, err
	}
	return c.deployQueue(ctx, a, false)
}

// OperatorDeployQueue is every org's queue with the builders' names, for
// the console. Platform viewers and operators.
func (c *Core) OperatorDeployQueue(ctx context.Context, a Actor) (apitypes.DeployQueue, error) {
	if err := a.requirePlatform(PlatformView, "reading the deploy queue"); err != nil {
		return apitypes.DeployQueue{}, err
	}
	return c.deployQueue(ctx, a, true)
}

// deployQueue builds the view: the console's (every release, servers
// named, commits by SHA only: an org's content isn't the console's, ADR
// 0062) or a's org's.
func (c *Core) deployQueue(ctx context.Context, a Actor, console bool) (apitypes.DeployQueue, error) {
	rows, err := c.store.ListDeployQueue(ctx)
	if err != nil {
		return apitypes.DeployQueue{}, err
	}
	slices.SortFunc(rows, func(x, y store.ListDeployQueueRow) int { return cmp.Compare(x.ID, y.ID) })
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return apitypes.DeployQueue{}, err
	}
	places := c.builds.places()

	item := func(r store.ListDeployQueueRow, since bool) apitypes.QueueItem {
		at := r.CreatedAt
		if p, ok := places[r.ID]; ok && since {
			at = p.since
		}
		if !console && (r.OrgID != a.OrgID || !a.canSee(r.OrgID, r.ProjectID, r.EnvironmentID)) {
			return apitypes.QueueItem{Hidden: true, QueuedAt: r.CreatedAt, Since: at}
		}
		it := apitypes.QueueItem{
			Project: r.Project, Environment: r.Environment, Service: r.Service, Release: r.Number,
			Status: apitypes.ReleaseStatus(r.Status), Source: apitypes.ReleaseSource(r.Source), FromRelease: r.FromRelease,
			CreatedBy: r.CreatedBy, QueuedAt: r.CreatedAt, Since: at,
			Commit: queueCommit(r.CommitSha, r.CommitMessage, r.CommitAuthor, console),
			Next:   queueCommit(r.PendingSha, r.PendingMessage, r.PendingAuthor, console),
		}
		if console {
			it.Org, it.ID = r.Org, r.ID
		}
		return it
	}

	// Builders: the servers that build, and any a release still waits at
	// (one that stopped building since), in the order they joined.
	used := map[int64]bool{}
	for _, p := range places {
		used[p.server] = true
	}
	byID := map[int64]*apitypes.QueueBuilder{}
	var order []store.Server
	for _, s := range servers {
		if c.canBuild(s) || used[s.ID] {
			order = append(order, s)
		}
	}
	slices.SortFunc(order, func(x, y store.Server) int { return cmp.Compare(x.ID, y.ID) })
	out := apitypes.DeployQueue{Builders: []apitypes.QueueBuilder{}, Deploying: []apitypes.QueueItem{}}
	for i, s := range order {
		b := apitypes.QueueBuilder{Label: "Builder " + strconv.Itoa(i+1), Builds: buildKinds(s), Waiting: []apitypes.QueueItem{}}
		if console {
			b.Name = s.Name
		}
		out.Builders = append(out.Builders, b)
	}
	for i, s := range order {
		byID[s.ID] = &out.Builders[i]
	}

	type waiter struct {
		pos int
		it  apitypes.QueueItem
	}
	lines := map[int64][]waiter{}
	for _, r := range rows {
		p, at := places[r.ID]
		b := byID[p.server]
		switch {
		case at && b != nil && p.holding:
			it := item(r, true)
			b.Building = &it
		case at && b != nil:
			lines[p.server] = append(lines[p.server], waiter{p.position, item(r, true)})
		case console || r.OrgID == a.OrgID && a.canSee(r.OrgID, r.ProjectID, r.EnvironmentID):
			out.Deploying = append(out.Deploying, item(r, false))
		}
	}
	for id, line := range lines {
		slices.SortFunc(line, func(x, y waiter) int { return cmp.Compare(x.pos, y.pos) })
		for _, w := range line {
			byID[id].Waiting = append(byID[id].Waiting, w.it)
		}
	}
	if !console {
		// An org sees a builder only while one of its releases is there or
		// someone else's is: what its next build would wait for.
		out.Builders = slices.DeleteFunc(out.Builders, func(b apitypes.QueueBuilder) bool { return b.Building == nil && len(b.Waiting) == 0 })
	}
	return out, nil
}

// queueCommit is a release's or a waiting push's commit for the queue:
// nil without one, its SHA alone for the console.
func queueCommit(sha, message, author string, console bool) *apitypes.Commit {
	switch {
	case sha == "":
		return nil
	case console:
		return &apitypes.Commit{SHA: sha}
	}
	return &apitypes.Commit{SHA: sha, Message: message, Author: author}
}

// buildKinds is which environments' images s builds.
func buildKinds(s store.Server) apitypes.BuildKinds {
	switch {
	case s.BuildsProduction && s.BuildsNonProduction:
		return apitypes.BuildsAll
	case s.BuildsProduction:
		return apitypes.BuildsProduction
	case s.BuildsNonProduction:
		return apitypes.BuildsNonProduction
	}
	return apitypes.BuildsNone
}

// queueChanged tells open queue pages that a builder's line moved: each
// org with a release in it, and the console. buildSlots calls it.
func (c *Core) queueChanged(orgs []int64) {
	ev := apitypes.Event{Type: apitypes.EventDeployQueue}
	for _, o := range orgs {
		c.bus.publish(busEvent{orgID: o, ev: ev})
	}
	c.bus.publish(busEvent{ev: ev})
}

// OperatorCancelRelease cancels a deploy from the console's queue, by
// release ID, as CancelRelease does: an operator frees a builder a stuck
// or unwanted build holds without joining the org (ADR 0062). Recorded
// in the org's log and the platform's.
func (c *Core) OperatorCancelRelease(ctx context.Context, a Actor, releaseID int64) (apitypes.Release, error) {
	if err := a.requirePlatform(OrgsAdminister, "canceling a deploy"); err != nil {
		return apitypes.Release{}, err
	}
	rel, err := c.store.GetRelease(ctx, releaseID)
	if store.IsNotFound(err) {
		return apitypes.Release{}, notFound("release %d not found; it may have been removed with its service", releaseID)
	}
	if err != nil {
		return apitypes.Release{}, err
	}
	ss, err := c.scopeOf(ctx, rel.ServiceID)
	if err != nil {
		return apitypes.Release{}, err
	}
	o, err := c.store.GetOrg(ctx, ss.project.OrgID)
	if err != nil {
		return apitypes.Release{}, err
	}
	got, err := c.cancelRelease(ctx, a, ss, rel, func(summary string) {
		c.auditBoth(ctx, a, o, "release.cancel", fmt.Sprintf("%s: %s", ss, summary))
	})
	if err != nil {
		return apitypes.Release{}, err
	}
	// The release as the console's queue shows it: its place and outcome,
	// not its config's variable names or its commit's words (ADR 0062).
	return apitypes.Release{
		ID: got.ID, Number: got.Number, Project: got.Project, Environment: got.Environment, Service: got.Service,
		Status: got.Status, Source: got.Source, FromRelease: got.FromRelease, Error: got.Error, ErrorCode: got.ErrorCode,
		CreatedBy: got.CreatedBy, CreatedAt: got.CreatedAt, FinishedAt: got.FinishedAt,
		Commit: queueCommit(commitSHA(got.Commit), "", "", true),
	}, nil
}

// commitSHA is a commit's SHA, "" without one.
func commitSHA(c *apitypes.Commit) string {
	if c == nil {
		return ""
	}
	return c.SHA
}
