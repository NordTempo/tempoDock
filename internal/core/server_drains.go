package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Draining a server (ADR 0063): every environment on it, or every one of
// a kind, moves to other servers without downtime, one at a time, so the
// server can take a kind off, go into maintenance or be removed. A drain
// is a list in server_drain_items that a goroutine works through: each
// item's move is an ordinary environment move (env_move.go) with the
// drain's ID. A failed move leaves its environment where it was and
// pauses the drain until an operator retries or skips it; a restart, or
// a server coming back, picks a running drain up again.

// serverDrainState is the in-memory side of drains.
type serverDrainState struct {
	mu      sync.Mutex
	driving map[int64]bool
}

func newServerDrainState() serverDrainState { return serverDrainState{driving: map[int64]bool{}} }

// drainBusyWait is how long a drain waits before trying again a move a
// deploy, backup or snapshot of the environment keeps from starting: a
// timer that exists only while a drain waits for one.
const drainBusyWait = 15 * time.Second

// DrainServer drains a server, or with req.DryRun answers with the plan:
// each environment, the server it goes to and its data, and the
// problems that keep it from starting. Nothing starts while the plan has
// a problem. Starting stops the server taking new environments (of the
// kind, for a kind drain). Operators only.
func (c *Core) DrainServer(ctx context.Context, a Actor, name string, req apitypes.DrainServerRequest) (apitypes.ServerDrain, error) {
	if err := a.requireOperator("draining a server"); err != nil {
		return apitypes.ServerDrain{}, err
	}
	srv, err := c.loadServer(ctx, name)
	if err != nil {
		return apitypes.ServerDrain{}, err
	}
	switch req.Kind {
	case "", apitypes.KindProduction, apitypes.KindNonProduction:
	default:
		return apitypes.ServerDrain{}, invalid("kind must be production or non_production, or empty for every environment")
	}
	hold, err := maxHold(req.MaxHold)
	if err != nil {
		return apitypes.ServerDrain{}, err
	}
	if d, err := c.store.GetOpenServerDrain(ctx, srv.ID); err == nil {
		return apitypes.ServerDrain{}, conflict("server %s is draining already (drain %d); follow it with `dock server drain %s --status`", srv.Name, d.ID, srv.Name)
	} else if !store.IsNotFound(err) {
		return apitypes.ServerDrain{}, err
	}
	plan, err := c.planDrain(ctx, srv, req)
	if err != nil {
		return apitypes.ServerDrain{}, err
	}
	plan.MaxHold = hold
	if req.DryRun {
		return plan.ServerDrain, nil
	}
	if len(plan.Problems) > 0 {
		return plan.ServerDrain, invalid("server %s can't drain as planned: %s", srv.Name, strings.Join(plan.Problems, "; "))
	}
	var row store.ServerDrain
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		row, err = q.CreateServerDrain(ctx, store.CreateServerDrainParams{
			ServerID: srv.ID, Kind: string(req.Kind), MaxHoldS: int64(hold), CreatedBy: a.Name, StartedAt: now(),
		})
		if store.IsUnique(err) {
			return conflict("server %s is draining already", srv.Name)
		}
		if err != nil {
			return err
		}
		for i, it := range plan.Items {
			if err := q.AddServerDrainItem(ctx, store.AddServerDrainItemParams{
				DrainID: row.ID, EnvironmentID: it.envID, ToServerID: it.toID, Position: int64(i),
			}); err != nil {
				return err
			}
		}
		// Nothing new lands on the server while it drains.
		p := store.UpdateServerPlacementParams{
			AcceptsProduction: srv.AcceptsProduction, AcceptsNonProduction: srv.AcceptsNonProduction,
			DisabledForPlacement: srv.DisabledForPlacement, ID: srv.ID,
		}
		switch req.Kind {
		case apitypes.KindProduction:
			p.AcceptsProduction = false
		case apitypes.KindNonProduction:
			p.AcceptsNonProduction = false
		default:
			p.DisabledForPlacement = true
		}
		_, err = q.UpdateServerPlacement(ctx, p)
		return err
	})
	if err != nil {
		return apitypes.ServerDrain{}, err
	}
	what := "every environment"
	if req.Kind != "" {
		what = "its " + kindWords(req.Kind) + " environments"
	}
	c.audit(ctx, a.platform(), "server.drain", nil, nil, "server "+srv.Name,
		fmt.Sprintf("draining server %s: %s (%d) move to other servers", srv.Name, what, len(plan.Items)))
	c.publishServer(ctx, srv.ID)
	c.startServerDrain(row.ID)
	return c.serverDrain(ctx, row)
}

// plannedItem is a DrainItem with the IDs a drain stores.
type plannedItem struct {
	apitypes.DrainItem
	envID, toID int64
}

// drainPlan is a plan of a drain.
type drainPlan struct {
	apitypes.ServerDrain
	Items []plannedItem
}

// planDrain decides where each environment of the drain goes and runs
// each move's pre-flight. Placement picks a project's target for a kind
// once (most free memory among the online servers that accept the kind,
// counting what the plan already put there), unless req.Targets names
// one, so a project stays together.
func (c *Core) planDrain(ctx context.Context, srv store.Server, req apitypes.DrainServerRequest) (drainPlan, error) {
	plan := drainPlan{ServerDrain: apitypes.ServerDrain{Server: srv.Name, Kind: req.Kind, Items: []apitypes.DrainItem{}, Problems: []string{}, Forwards: []apitypes.ServerForward{}}}
	envs, err := c.store.ListDrainableEnvironments(ctx, srv.ID)
	if err != nil {
		return plan, err
	}
	loads, err := c.serverLoads(ctx, c.store.Queries)
	if err != nil {
		return plan, err
	}
	for i := range loads {
		if loads[i].server.ID == srv.ID {
			loads[i].server.DisabledForPlacement = true // never back onto itself
		} else if _, err := c.store.GetOpenServerDrain(ctx, loads[i].server.ID); err == nil {
			loads[i].server.DisabledForPlacement = true // draining too
		}
	}
	chosen := map[string]store.Server{} // "<project id>/<kind>" → target
	for _, e := range envs {
		kind := apitypes.EnvironmentKind(e.Kind)
		if req.Kind != "" && kind != req.Kind {
			continue
		}
		es, err := c.envByID(ctx, e.ID)
		if err != nil {
			return plan, err
		}
		it := plannedItem{envID: e.ID, DrainItem: apitypes.DrainItem{
			Project: e.ProjectName, Org: e.OrgSlug, Environment: e.Name, Kind: kind, Status: apitypes.DrainWaiting,
		}}
		mem, stateful, err := c.envReserve(ctx, e.ID)
		if err != nil {
			return plan, err
		}
		it.Stateful = stateful
		key := fmt.Sprintf("%d/%s", e.ProjectID, e.Kind)
		to, ok := chosen[key]
		if !ok {
			to, err = c.drainTarget(ctx, req, e.ProjectName, e.OrgSlug, kind, mem, loads)
			if err != nil {
				it.Problem = err.Error()
			} else {
				chosen[key] = to
			}
		}
		if it.Problem == "" {
			it.To, it.toID = to.Name, to.ID
			for i := range loads {
				if loads[i].server.ID == to.ID {
					loads[i].reserved += mem << 20
				}
			}
			it.DataBytes, err = c.checkDrainItem(ctx, es, srv, to)
			if err != nil {
				it.Problem = err.Error()
			}
		}
		if it.Problem != "" {
			plan.Problems = append(plan.Problems, fmt.Sprintf("%s/%s: %s", e.ProjectName, e.Name, it.Problem))
		}
		plan.Items = append(plan.Items, it)
		plan.ServerDrain.Items = append(plan.ServerDrain.Items, it.DrainItem)
	}
	return plan, nil
}

// envReserve is the memory an environment's live services reserve, in
// MB, and whether any of them has volumes.
func (c *Core) envReserve(ctx context.Context, envID int64) (int64, bool, error) {
	svcs, err := c.store.ListServices(ctx, envID)
	if err != nil {
		return 0, false, err
	}
	vols, err := c.store.ListEnvironmentVolumes(ctx, envID)
	if err != nil {
		return 0, false, err
	}
	var mb int64
	for _, s := range svcs {
		if s.ArchivedAt == nil {
			mb += cmpOr(s.MemoryMb, int64(c.cfg.Defaults.MemoryMB))
		}
	}
	return mb, len(vols) > 0, nil
}

// drainTarget is the server a project's environments of kind go to: the
// one req.Targets names for the project ("project" or "org/project"),
// else placement's pick with mem MB free.
func (c *Core) drainTarget(ctx context.Context, req apitypes.DrainServerRequest, project, org string, kind apitypes.EnvironmentKind, mem int64, loads []serverLoad) (store.Server, error) {
	for _, t := range req.Targets {
		if p := strings.TrimSpace(t.Project); p == project || p == org+"/"+project {
			return c.loadServer(ctx, strings.TrimSpace(t.To))
		}
	}
	eligible := make([]serverLoad, 0, len(loads))
	for _, l := range loads {
		if l.server.MemoryBytes == 0 || l.free() >= mem<<20 {
			eligible = append(eligible, l)
		}
	}
	l, err := pickServer(kind, eligible)
	if err != nil {
		return store.Server{}, conflict("no other online server takes %s environments with %d MB free; let one take them (`dock server update <name> --%s`) or add a server",
			kindWords(kind), mem, kindWords(kind))
	}
	return l.server, nil
}

// checkDrainItem runs a move's pre-flight for a planned item and returns
// the size of its data. A deploy in flight isn't a problem: the drain
// waits for it.
func (c *Core) checkDrainItem(ctx context.Context, es envScope, from, to store.Server) (int64, error) {
	svcs, src, dst, err := c.preflightMove(ctx, es, from, to)
	if CodeOf(err) == apitypes.CodeDeployInProgress || isBusy(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return c.checkMove(ctx, es, from, to, src, dst, svcs)
}

// isBusy reports whether err is a move refused for a backup, snapshot or
// restore that runs, which a drain waits out.
func isBusy(err error) bool {
	return err != nil && CodeOf(err) == apitypes.CodeConflict && strings.Contains(err.Error(), "move the environment once that finishes")
}

// ServerDrainStatus returns a server's running or latest drain, with the
// ports it forwards; not found when it never drained and forwards
// nothing. Operators only.
func (c *Core) ServerDrainStatus(ctx context.Context, a Actor, name string) (apitypes.ServerDrain, error) {
	if err := a.requireOperator("reading a server's drain"); err != nil {
		return apitypes.ServerDrain{}, err
	}
	srv, err := c.loadServer(ctx, name)
	if err != nil {
		return apitypes.ServerDrain{}, err
	}
	d, err := c.store.GetLatestServerDrain(ctx, srv.ID)
	if store.IsNotFound(err) {
		out := apitypes.ServerDrain{Server: srv.Name, Items: []apitypes.DrainItem{}, Problems: []string{}}
		if out.Forwards, err = c.serverForwards(ctx, srv.ID); err != nil {
			return out, err
		}
		if len(out.Forwards) == 0 {
			return out, notFound("server %s never drained; drain it with `dock server drain %s`", srv.Name, srv.Name)
		}
		return out, nil
	}
	if err != nil {
		return apitypes.ServerDrain{}, err
	}
	return c.serverDrain(ctx, d)
}

// ResumeServerDrain goes on with a paused drain: the environment that
// failed is tried again, or with req.Skip left where it is. Operators
// only.
func (c *Core) ResumeServerDrain(ctx context.Context, a Actor, name string, req apitypes.ResumeDrainRequest) (apitypes.ServerDrain, error) {
	if err := a.requireOperator("draining a server"); err != nil {
		return apitypes.ServerDrain{}, err
	}
	srv, err := c.loadServer(ctx, name)
	if err != nil {
		return apitypes.ServerDrain{}, err
	}
	d, err := c.store.GetOpenServerDrain(ctx, srv.ID)
	if store.IsNotFound(err) {
		return apitypes.ServerDrain{}, notFound("server %s isn't draining", srv.Name)
	}
	if err != nil {
		return apitypes.ServerDrain{}, err
	}
	if d.Status != string(apitypes.DrainPaused) {
		return apitypes.ServerDrain{}, conflict("server %s's drain runs already", srv.Name)
	}
	items, err := c.store.ListServerDrainItems(ctx, d.ID)
	if err != nil {
		return apitypes.ServerDrain{}, err
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		for _, it := range items {
			if it.ServerDrainItem.Status != string(apitypes.DrainFailed) {
				continue
			}
			st, why := apitypes.DrainWaiting, ""
			if req.Skip {
				st, why = apitypes.DrainSkipped, it.ServerDrainItem.Error
			}
			if err := q.SetServerDrainItem(ctx, store.SetServerDrainItemParams{
				Status: string(st), MoveID: it.ServerDrainItem.MoveID, Error: why, DrainID: d.ID, EnvironmentID: it.ServerDrainItem.EnvironmentID,
			}); err != nil {
				return err
			}
		}
		return q.SetServerDrainStatus(ctx, store.SetServerDrainStatusParams{Status: string(apitypes.DrainRunning), ID: d.ID})
	})
	if err != nil {
		return apitypes.ServerDrain{}, err
	}
	verb := "resumed"
	if req.Skip {
		verb = "resumed, skipping the environment that failed,"
	}
	c.audit(ctx, a.platform(), "server.drain", nil, nil, "server "+srv.Name, fmt.Sprintf("draining server %s %s", srv.Name, verb))
	c.startServerDrain(d.ID)
	if d, err = c.store.GetServerDrain(ctx, d.ID); err != nil {
		return apitypes.ServerDrain{}, err
	}
	c.publishServerDrain(ctx, d)
	c.publishServer(ctx, srv.ID)
	return c.serverDrain(ctx, d)
}

// CancelServerDrain stops a drain after the move that runs, which isn't
// cut short: environments not moved yet stay where they are. The server
// keeps not taking new environments until an operator says otherwise.
// Operators only.
func (c *Core) CancelServerDrain(ctx context.Context, a Actor, name string) (apitypes.ServerDrain, error) {
	if err := a.requireOperator("draining a server"); err != nil {
		return apitypes.ServerDrain{}, err
	}
	srv, err := c.loadServer(ctx, name)
	if err != nil {
		return apitypes.ServerDrain{}, err
	}
	d, err := c.store.GetOpenServerDrain(ctx, srv.ID)
	if store.IsNotFound(err) {
		return apitypes.ServerDrain{}, notFound("server %s isn't draining", srv.Name)
	}
	if err != nil {
		return apitypes.ServerDrain{}, err
	}
	if err := c.store.SetServerDrainStatus(ctx, store.SetServerDrainStatusParams{
		Status: string(apitypes.DrainCancelled), Error: d.Error, FinishedAt: ptr(now()), ID: d.ID,
	}); err != nil {
		return apitypes.ServerDrain{}, err
	}
	c.audit(ctx, a.platform(), "server.drain_cancelled", nil, nil, "server "+srv.Name,
		fmt.Sprintf("cancelled the drain of server %s; a move that runs finishes, the rest stay", srv.Name))
	if d, err = c.store.GetServerDrain(ctx, d.ID); err != nil {
		return apitypes.ServerDrain{}, err
	}
	c.publishServerDrain(ctx, d)
	c.publishServer(ctx, srv.ID)
	return c.serverDrain(ctx, d)
}

// resumeServerDrains picks up every running drain: at start and when a
// server reconnects (one may have waited for it).
func (c *Core) resumeServerDrains(ctx context.Context) {
	rows, err := c.store.ListRunningServerDrains(ctx)
	if err != nil {
		c.log.Error("list drains to resume", "err", err)
		return
	}
	for _, d := range rows {
		c.startServerDrain(d.ID)
	}
}

// startServerDrain drives a drain in the background unless a goroutine
// does already.
func (c *Core) startServerDrain(id int64) {
	c.drains.mu.Lock()
	if c.drains.driving[id] {
		c.drains.mu.Unlock()
		return
	}
	c.drains.driving[id] = true
	c.drains.mu.Unlock()
	c.wg.Go(func() {
		defer func() {
			c.drains.mu.Lock()
			delete(c.drains.driving, id)
			c.drains.mu.Unlock()
		}()
		if err := c.driveServerDrain(c.ctx, id); err != nil && c.ctx.Err() == nil {
			c.log.Error("server drain", "drain", id, "err", err)
		}
	})
}

// errDrainWaits ends a drain's goroutine while a server it needs is
// offline: the server reconnecting resumes it.
var errDrainWaits = errors.New("waiting for a server to be back")

// driveServerDrain works through a drain's environments until it is
// done, paused, cancelled or waits for a server.
func (c *Core) driveServerDrain(ctx context.Context, id int64) error {
	for {
		ended := c.moveEnded()
		d, err := c.store.GetServerDrain(ctx, id)
		if err != nil {
			return err
		}
		if d.Status != string(apitypes.DrainRunning) {
			return nil
		}
		items, err := c.store.ListServerDrainItems(ctx, id)
		if err != nil {
			return err
		}
		var next *store.ListServerDrainItemsRow
		for i := range items {
			st := apitypes.DrainItemStatus(items[i].ServerDrainItem.Status)
			if st == apitypes.DrainWaiting || st == apitypes.DrainMoving {
				next = &items[i]
				break
			}
		}
		if next == nil {
			if added, err := c.addDrainLeftovers(ctx, d, items); err != nil || added {
				if err != nil {
					return err
				}
				continue
			}
			return c.finishServerDrain(ctx, d, items)
		}
		it := next.ServerDrainItem
		if it.Status == string(apitypes.DrainMoving) && it.MoveID != nil {
			m, err := c.store.GetEnvironmentMove(ctx, *it.MoveID)
			if err != nil {
				return err
			}
			switch apitypes.MoveStatus(m.Status) {
			case apitypes.MoveSucceeded:
				err = c.setDrainItem(ctx, d, it, apitypes.DrainMoved, it.MoveID, "")
			case apitypes.MoveFailed:
				err = c.pauseServerDrain(ctx, d, it, it.MoveID, fmt.Sprintf("%s/%s didn't move and stays on its server: %s", next.ProjectName, next.EnvironmentName, m.Error))
			default:
				select { // the move goes on in its own goroutine
				case <-ended:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if err != nil {
				return err
			}
			continue
		}
		if err := c.startDrainMove(ctx, d, *next); err != nil {
			if errors.Is(err, errDrainWaits) {
				return nil
			}
			return err
		}
	}
}

// addDrainLeftovers adds the environments that came to the server while
// it drained (a preview of a project placed there, say) to the end of the
// drain, each to where its project's environments of the kind went, or
// where placement puts it. It reports whether it added any.
func (c *Core) addDrainLeftovers(ctx context.Context, d store.ServerDrain, items []store.ListServerDrainItemsRow) (bool, error) {
	envs, err := c.store.ListDrainableEnvironments(ctx, d.ServerID)
	if err != nil {
		return false, err
	}
	had := map[int64]bool{}
	went := map[string]int64{} // "<project id>/<kind>" → target
	for _, it := range items {
		had[it.ServerDrainItem.EnvironmentID] = true
		if e, err := c.store.GetEnvironment(ctx, it.ServerDrainItem.EnvironmentID); err == nil {
			went[fmt.Sprintf("%d/%s", e.ProjectID, e.Kind)] = it.ServerDrainItem.ToServerID
		}
	}
	var loads []serverLoad
	added := false
	for _, e := range envs {
		if had[e.ID] || (d.Kind != "" && e.Kind != d.Kind) {
			continue
		}
		to, ok := went[fmt.Sprintf("%d/%s", e.ProjectID, e.Kind)]
		if !ok {
			if loads == nil {
				if loads, err = c.serverLoads(ctx, c.store.Queries); err != nil {
					return added, err
				}
				for i := range loads {
					if loads[i].server.ID == d.ServerID {
						loads[i].server.DisabledForPlacement = true
					}
				}
			}
			l, err := pickServer(apitypes.EnvironmentKind(e.Kind), loads)
			if err != nil {
				// Paused: the loop sees it and stops.
				return true, c.pauseServerDrain(ctx, d, store.ServerDrainItem{EnvironmentID: e.ID}, nil,
					fmt.Sprintf("%s/%s came to the server while it drained, and no other server can take it: %v", e.ProjectName, e.Name, err))
			}
			to = l.server.ID
		}
		if err := c.store.AddServerDrainItem(ctx, store.AddServerDrainItemParams{
			DrainID: d.ID, EnvironmentID: e.ID, ToServerID: to, Position: int64(len(items)),
		}); err != nil {
			return added, err
		}
		items = append(items, store.ListServerDrainItemsRow{ServerDrainItem: store.ServerDrainItem{EnvironmentID: e.ID}})
		added = true
	}
	return added, nil
}

// startDrainMove starts the move of a drain's next environment, waiting
// out a deploy, backup or snapshot that keeps it from starting, and
// pauses the drain when it can't start.
func (c *Core) startDrainMove(ctx context.Context, d store.ServerDrain, row store.ListServerDrainItemsRow) error {
	it := row.ServerDrainItem
	es, err := c.envByID(ctx, it.EnvironmentID)
	if err != nil {
		return err
	}
	if es.env.ServerID != d.ServerID || es.env.ArchivedAt != nil || es.project.ArchivedAt != nil {
		// Moved by hand, or archived, since the plan: nothing to do.
		return c.setDrainItem(ctx, d, it, apitypes.DrainSkipped, nil, "it isn't on the server any more")
	}
	to, err := c.store.GetServer(ctx, it.ToServerID)
	if err != nil {
		return err
	}
	m, err := c.beginMove(ctx, es, to, moveOpts{maxHold: int(d.MaxHoldS), drainID: &d.ID, by: d.CreatedBy})
	switch {
	case err == nil:
		return c.setDrainItem(ctx, d, it, apitypes.DrainMoving, &m.ID, "")
	case CodeOf(err) == apitypes.CodeServerOffline:
		return errDrainWaits
	case CodeOf(err) == apitypes.CodeDeployInProgress || isBusy(err):
		select {
		case <-time.After(drainBusyWait):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return c.pauseServerDrain(ctx, d, it, nil, fmt.Sprintf("%s/%s can't move: %v", row.ProjectName, row.EnvironmentName, err))
}

// setDrainItem records how far a drain's environment got and tells open
// pages.
func (c *Core) setDrainItem(ctx context.Context, d store.ServerDrain, it store.ServerDrainItem, st apitypes.DrainItemStatus, moveID *int64, why string) error {
	if err := c.store.SetServerDrainItem(ctx, store.SetServerDrainItemParams{
		Status: string(st), MoveID: moveID, Error: why, DrainID: d.ID, EnvironmentID: it.EnvironmentID,
	}); err != nil {
		return err
	}
	c.publishServerDrain(ctx, d)
	c.publishServer(ctx, d.ServerID) // its drain's progress
	return nil
}

// pauseServerDrain marks an environment failed and pauses the drain for
// an operator to retry or skip it.
func (c *Core) pauseServerDrain(ctx context.Context, d store.ServerDrain, it store.ServerDrainItem, moveID *int64, why string) error {
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.SetServerDrainItem(ctx, store.SetServerDrainItemParams{
			Status: string(apitypes.DrainFailed), MoveID: moveID, Error: why, DrainID: d.ID, EnvironmentID: it.EnvironmentID,
		}); err != nil {
			return err
		}
		return q.SetServerDrainStatus(ctx, store.SetServerDrainStatusParams{Status: string(apitypes.DrainPaused), Error: why, ID: d.ID})
	})
	if err != nil {
		return err
	}
	name := c.serverName(ctx, d.ServerID)
	c.audit(ctx, System.platform(), "server.drain_paused", nil, nil, "server "+name,
		fmt.Sprintf("the drain of server %s paused: %s; retry with `dock server drain %s --resume`, or skip it with --resume --skip", name, why, name))
	d.Status, d.Error = string(apitypes.DrainPaused), why
	c.publishServerDrain(ctx, d)
	c.publishServer(ctx, d.ServerID)
	return nil
}

// finishServerDrain ends a drain whose environments all moved or were
// skipped.
func (c *Core) finishServerDrain(ctx context.Context, d store.ServerDrain, items []store.ListServerDrainItemsRow) error {
	if err := c.store.SetServerDrainStatus(ctx, store.SetServerDrainStatusParams{
		Status: string(apitypes.DrainSucceeded), FinishedAt: ptr(now()), ID: d.ID,
	}); err != nil {
		return err
	}
	moved, skipped := 0, 0
	for _, it := range items {
		switch apitypes.DrainItemStatus(it.ServerDrainItem.Status) {
		case apitypes.DrainMoved:
			moved++
		case apitypes.DrainSkipped:
			skipped++
		}
	}
	name := c.serverName(ctx, d.ServerID)
	summary := fmt.Sprintf("server %s drained: %d environments moved", name, moved)
	if skipped > 0 {
		summary += fmt.Sprintf(", %d skipped", skipped)
	}
	c.audit(ctx, System.platform(), "server.drained", nil, nil, "server "+name, summary)
	if d, err := c.store.GetServerDrain(ctx, d.ID); err == nil {
		c.publishServerDrain(ctx, d)
	}
	c.publishServer(ctx, d.ServerID)
	return nil
}

// serverName is a server's name for messages, its ID if it can't be read.
func (c *Core) serverName(ctx context.Context, id int64) string {
	if s, err := c.store.GetServer(ctx, id); err == nil {
		return s.Name
	}
	return fmt.Sprintf("#%d", id)
}

// serverDrain is a drain as the API shows it: its environments, with the
// move of the one moving (or failed), and the server's forwards.
func (c *Core) serverDrain(ctx context.Context, d store.ServerDrain) (apitypes.ServerDrain, error) {
	out := apitypes.ServerDrain{
		ID: d.ID, Server: c.serverName(ctx, d.ServerID), Kind: apitypes.EnvironmentKind(d.Kind), Status: apitypes.DrainStatus(d.Status),
		Error: d.Error, Items: []apitypes.DrainItem{}, Problems: []string{}, MaxHold: int(d.MaxHoldS),
		CreatedBy: d.CreatedBy, StartedAt: &d.StartedAt, FinishedAt: d.FinishedAt,
	}
	items, err := c.store.ListServerDrainItems(ctx, d.ID)
	if err != nil {
		return out, err
	}
	for _, r := range items {
		it := apitypes.DrainItem{
			Project: r.ProjectName, Environment: r.EnvironmentName, To: r.ToServerName,
			Status: apitypes.DrainItemStatus(r.ServerDrainItem.Status), Error: r.ServerDrainItem.Error,
		}
		if org, err := c.store.GetOrg(ctx, r.OrgID); err == nil {
			it.Org = org.Slug
		}
		if e, err := c.store.GetEnvironment(ctx, r.ServerDrainItem.EnvironmentID); err == nil {
			it.Kind = apitypes.EnvironmentKind(e.Kind)
		}
		// Only the one moving or failed carries its move, so a drain
		// of many environments stays a few queries.
		if id := r.ServerDrainItem.MoveID; id != nil && (it.Status == apitypes.DrainMoving || it.Status == apitypes.DrainFailed) {
			if m, err := c.store.GetEnvironmentMove(ctx, *id); err == nil {
				if es, err := c.envByID(ctx, m.EnvironmentID); err == nil {
					if mv, err := c.toMove(ctx, es, m); err == nil {
						it.Move = &mv
					}
				}
			}
		}
		out.Items = append(out.Items, it)
	}
	out.Forwards, err = c.serverForwards(ctx, d.ServerID)
	return out, err
}

// serverForwards lists the ports a server forwards.
func (c *Core) serverForwards(ctx context.Context, serverID int64) ([]apitypes.ServerForward, error) {
	rows, err := c.store.ListServerForwardDetails(ctx, serverID)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.ServerForward, len(rows))
	for i, r := range rows {
		out[i] = apitypes.ServerForward{
			Project: r.ProjectName, Environment: r.EnvironmentName, Service: r.ServiceName, Port: r.HostPort, NowOn: r.NowOn, Since: r.CreatedAt,
		}
	}
	return out, nil
}

// publishServerDrain sends a drain's state to operators' open pages.
func (c *Core) publishServerDrain(ctx context.Context, d store.ServerDrain) {
	out, err := c.serverDrain(ctx, d)
	if err != nil {
		c.log.Warn("publish a drain", "drain", d.ID, "err", err)
		return
	}
	c.bus.publish(busEvent{serverStats: true, ev: apitypes.Event{Type: apitypes.EventServerDrain, Drain: &out}})
}

// publishMoveDrain publishes the drain a move belongs to, so its page
// follows the move's steps.
func (c *Core) publishMoveDrain(ctx context.Context, drainID int64) {
	if d, err := c.store.GetServerDrain(ctx, drainID); err == nil {
		c.publishServerDrain(ctx, d)
	}
}

// drainProgress is every running or paused drain's progress by server,
// in one query.
func (c *Core) drainProgress(ctx context.Context) (map[int64]*apitypes.DrainProgress, error) {
	rows, err := c.store.ListOpenServerDrainProgress(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*apitypes.DrainProgress, len(rows))
	for _, r := range rows {
		out[r.ServerID] = &apitypes.DrainProgress{
			ID: r.ID, Status: apitypes.DrainStatus(r.Status), Done: int(r.Done), Total: int(r.Total), Error: r.Error,
		}
	}
	return out, nil
}
