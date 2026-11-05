package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Moving an environment to another server (ADR 0053). A move is a state
// machine in environment_moves: prepare, stop, copy, start, switch,
// cleanup. Until the switch the environment is on its source in every
// sense, and a failure (or a restart) rolls back; the switch is one
// transaction, and after it the clean-up only goes forward. A move that
// needs a server that is offline waits for it: the server reconnecting,
// or the daemon starting, picks it up again (resumeMoves).

// moveState is the in-memory side of moves.
type moveState struct {
	mu sync.Mutex
	// driving holds the moves a goroutine works on now.
	driving map[int64]bool
	// hook, when set (tests), runs before each step of a move.
	hook func(id int64, step apitypes.MoveStep)
	// recovered is set once Start has dealt with the moves a restart cut
	// short; until then a server reconnecting doesn't resume any, or it
	// could drive one forward that must roll back.
	recovered bool
}

func newMoveState() moveState { return moveState{driving: map[int64]bool{}} }

// errMoveWaits means a move needs a server that is offline; it goes on
// when the server reconnects.
var errMoveWaits = errors.New("waiting for a server to be back")

// moveExportScript archives /v to standard output, saying on standard
// error how many entries it holds.
const moveExportScript = `set -eu
cd /v
echo "dock-move: entries $(find . | wc -l)" >&2
tar -cf - .
`

// moveImportScript empties /v and unpacks standard input into it, saying
// how many entries it holds after.
const moveImportScript = `set -eu
rm -rf /v/* /v/.[!.]* /v/..?*
tar -xf - -C /v
echo "dock-move: entries $(cd /v && find . | wc -l)"
`

// moveSizeScript prints the size of each volume mounted under /volumes,
// in KiB, and their total.
const moveSizeScript = `set -eu
echo "dock-move: kib $(du -sk /volumes | cut -f1)"
`

var (
	moveEntries = regexp.MustCompile(`dock-move: entries (\d+)`)
	moveKiB     = regexp.MustCompile(`dock-move: kib (\d+)`)
)

// movingService is a service of a move: its scope (on the source), its
// active release (nil: nothing to start), its applied volumes and its row.
type movingService struct {
	ss   svcScope
	rel  *store.Release
	vols []store.Volume
	row  store.EnvironmentMoveService
}

func (m movingService) stateful() bool { return len(m.vols) > 0 }

// runs reports whether the service has a release running to stop on the
// source and start on the target: not one stopped on purpose (ADR
// 0050), which moves stopped, its data with it.
func (m movingService) runs() bool { return m.rel != nil && m.ss.svc.StoppedAt == nil }

// MoveEnvironment moves an environment to another server (ADR 0053). It
// checks everything first and refuses with the reason; then it answers
// with the move, running, and works in the background: progress is
// published as environment_move events, the end audited
// environment.moved or environment.move_failed. Operators only, and a
// locked environment's lock applies.
func (c *Core) MoveEnvironment(ctx context.Context, a Actor, ref EnvRef, req apitypes.MoveEnvironmentRequest) (apitypes.EnvironmentMove, error) {
	const action = "moving an environment"
	if err := a.requireOperator(action); err != nil {
		return apitypes.EnvironmentMove{}, err
	}
	es, err := c.loadEnv(ctx, a.OrgID, ref)
	if err != nil {
		return apitypes.EnvironmentMove{}, err
	}
	if err := a.checkLock(es, action); err != nil {
		return apitypes.EnvironmentMove{}, err
	}
	if err := notArchived(es.project); err != nil {
		return apitypes.EnvironmentMove{}, err
	}
	to, err := c.loadServer(ctx, strings.TrimSpace(req.To))
	if err != nil {
		return apitypes.EnvironmentMove{}, err
	}
	from, err := c.store.GetServer(ctx, es.env.ServerID)
	if err != nil {
		return apitypes.EnvironmentMove{}, err
	}
	kind := apitypes.EnvironmentKind(es.env.Kind)
	switch {
	case to.ID == from.ID:
		return apitypes.EnvironmentMove{}, invalid("%s is on server %s already", es, to.Name)
	case !acceptsKind(to, kind):
		return apitypes.EnvironmentMove{}, invalid("server %s doesn't accept %s environments; allow them with `dock server update %s --%s`", to.Name, kindWords(kind), to.Name, kindWords(kind))
	}
	src, dst := c.connected(from.ID), c.connected(to.ID)
	for _, s := range []struct {
		srv store.Server
		rt  node.Runtime
	}{{from, src}, {to, dst}} {
		if s.rt == nil {
			return apitypes.EnvironmentMove{}, errf(apitypes.CodeServerOffline, "server %s is offline; a move needs both servers online from start to end", s.srv.Name)
		}
	}
	if _, err := c.store.GetOpenEnvironmentMove(ctx, es.env.ID); err == nil {
		return apitypes.EnvironmentMove{}, conflict("%s is moving already; follow it with `dock env move %s --status`", es, es.env.Name)
	} else if !store.IsNotFound(err) {
		return apitypes.EnvironmentMove{}, err
	}
	svcs, err := c.moveServices(ctx, es)
	if err != nil {
		return apitypes.EnvironmentMove{}, err
	}
	if err := c.checkMove(ctx, es, from, to, src, dst, svcs); err != nil {
		return apitypes.EnvironmentMove{}, err
	}

	var row store.EnvironmentMove
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		row, err = q.CreateEnvironmentMove(ctx, store.CreateEnvironmentMoveParams{
			EnvironmentID: es.env.ID, FromServerID: from.ID, ToServerID: to.ID, CreatedBy: a.Name, StartedAt: now(),
		})
		if store.IsUnique(err) {
			return conflict("%s is moving already", es)
		}
		if err != nil {
			return err
		}
		for _, s := range svcs {
			var relID *int64
			var port int64
			if s.rel != nil {
				relID = &s.rel.ID
				if s.rel.HostPort > 0 {
					// Reserved on the target from here on (ListUsedHostPorts).
					if port, err = c.allocateHostPort(ctx, q, to.ID); err != nil {
						return err
					}
				}
			}
			if err := q.AddEnvironmentMoveService(ctx, store.AddEnvironmentMoveServiceParams{
				MoveID: row.ID, ServiceID: s.ss.svc.ID, ReleaseID: relID, Stateful: s.stateful(), HostPort: port,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return apitypes.EnvironmentMove{}, err
	}
	c.audit(ctx, a, "environment.move", &es.project, &es.env, es.String(), fmt.Sprintf("moving to server %s from %s", to.Name, from.Name))
	out, err := c.toMove(ctx, es, row)
	if err != nil {
		return out, err
	}
	c.publishMove(es, out)
	c.startMove(row.ID)
	return out, nil
}

// EnvironmentMoveStatus returns an environment's running or latest move;
// not found if it never moved. Read scope.
func (c *Core) EnvironmentMoveStatus(ctx context.Context, a Actor, ref EnvRef) (apitypes.EnvironmentMove, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeRead, "reading a move")
	if err != nil {
		return apitypes.EnvironmentMove{}, err
	}
	row, err := c.store.GetLatestEnvironmentMove(ctx, es.env.ID)
	if store.IsNotFound(err) {
		return apitypes.EnvironmentMove{}, notFound("%s never moved; move it with `dock env move %s --to <server>`", es, es.env.Name)
	}
	if err != nil {
		return apitypes.EnvironmentMove{}, err
	}
	return c.toMove(ctx, es, row)
}

// moveServices lists the environment's live services with their active
// release and applied volumes, refusing while one deploys or has a
// backup, snapshot or restore running.
func (c *Core) moveServices(ctx context.Context, es envScope) ([]movingService, error) {
	svcs, err := c.store.ListServices(ctx, es.env.ID)
	if err != nil {
		return nil, err
	}
	vols, err := c.store.ListEnvironmentVolumes(ctx, es.env.ID)
	if err != nil {
		return nil, err
	}
	var out []movingService
	for _, s := range svcs {
		if s.ArchivedAt != nil {
			continue
		}
		ss := svcScope{es, s}
		if r, err := c.store.GetInFlightRelease(ctx, s.ID); err == nil {
			return nil, deployInProgress(ss, r)
		}
		if c.serviceBusy(s.ID) {
			return nil, conflict("a backup, snapshot or restore of %s is running, or the backups are moving; move the environment once that finishes", ss)
		}
		m := movingService{ss: ss}
		if rel, err := c.store.GetActiveRelease(ctx, s.ID); err == nil {
			m.rel = &rel
		} else if !store.IsNotFound(err) {
			return nil, err
		}
		for _, v := range vols {
			if v.ServiceID == s.ID && !v.Pending {
				m.vols = append(m.vols, v)
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// notMoving refuses an action on an environment that is moving: deploys,
// backups, snapshots and restores wait for the move to end.
func (c *Core) notMoving(ctx context.Context, es envScope, what string) error {
	_, err := c.store.GetOpenEnvironmentMove(ctx, es.env.ID)
	switch {
	case err == nil:
		return conflict("%s is moving to another server; %s once the move ends (`dock env move %s --status`)", es, what, es.env.Name)
	case store.IsNotFound(err):
		return nil
	default:
		return err
	}
}

// checkMove is the pre-flight: images the target can get, memory and
// disk on it, and a tunnel there for domains Dock routes through
// Cloudflare.
func (c *Core) checkMove(ctx context.Context, es envScope, from, to store.Server, src, dst node.Runtime, svcs []movingService) error {
	var needMB int64
	var vols []node.Mount
	for _, s := range svcs {
		needMB += cmpOr(s.ss.svc.MemoryMb, int64(c.cfg.Defaults.MemoryMB))
		for _, v := range s.vols {
			vols = append(vols, node.Mount{Volume: v.DockerName, Path: "/volumes/" + v.DockerName, ReadOnly: true})
		}
		if s.rel == nil {
			continue
		}
		if ok, err := c.imageReaches(ctx, s.rel, dst); err != nil {
			return c.nodeErr(to.ID, err)
		} else if !ok {
			return invalid("%s's image %s was built on a server without the platform registry, so server %s can't get it; deploy %s with the registry set up, then move", s.ss, s.rel.ImageRef, to.Name, s.ss.svc.Name)
		}
	}
	loads, err := c.store.ListServerLoads(ctx, int64(c.cfg.Defaults.MemoryMB))
	if err != nil {
		return err
	}
	for _, l := range loads {
		if l.Server.ID != to.ID || l.Server.MemoryBytes == 0 {
			continue
		}
		if free := l.Server.MemoryBytes>>20 - l.ReservedMb; needMB > free {
			return conflict("server %s has %d MB of memory free and %s's services reserve %d MB; free memory there, or pick another server", to.Name, max(free, 0), es, needMB)
		}
	}
	if n, err := c.store.CountEnvironmentCloudflareRecords(ctx, es.env.ID); err != nil {
		return err
	} else if n > 0 {
		t, err := c.store.GetCloudflareTunnelByServer(ctx, &to.ID)
		if store.IsNotFound(err) || (err == nil && (t.Status != "ok" || t.TunnelID == "")) {
			return invalid("%s has domains Dock routes through Cloudflare, and server %s has no tunnel Dock runs; create one with `dock server tunnel create %s` first", es, to.Name, to.Name)
		}
		if err != nil {
			return err
		}
	}
	if len(vols) == 0 {
		return nil
	}
	out, err := c.runToolJob(ctx, from.ID, src, node.JobSpec{
		Name:   fmt.Sprintf("dock_p%d_e%d_move_size_%d", es.project.ID, es.env.ID, now().UnixNano()),
		Labels: node.Labels{ControlPlane: c.install}, Script: moveSizeScript, Mounts: vols,
	})
	if err != nil {
		return fmt.Errorf("measure the volumes on %s: %w", from.Name, err)
	}
	m := moveKiB.FindStringSubmatch(out)
	if m == nil {
		return fmt.Errorf("measure the volumes on %s: no size%s", from.Name, withOutput(out))
	}
	need, _ := strconv.ParseInt(m[1], 10, 64)
	need <<= 10
	host, err := dst.HostStats(ctx)
	if err != nil {
		return fmt.Errorf("read server %s's disk: %w", to.Name, c.nodeErr(to.ID, err))
	}
	if len(host.Disks) > 0 {
		d := host.Disks[0]
		if free := int64(d.Total) - int64(d.Used); need > free-int64(d.Total)/10 {
			return conflict("server %s has %s of disk free and %s's volumes take %s, of which a tenth of the disk stays free; free disk there first", to.Name, humanBytes(max(free, 0)), es, humanBytes(need))
		}
	}
	return nil
}

// cmpOr is v unless it is zero or less, else def.
func cmpOr(v, def int64) int64 {
	if v > 0 {
		return v
	}
	return def
}

// imageReaches reports whether a release's image can get to rt: rt has
// it, the platform registry has it (a digest), or it is a public image
// rather than a build that never left the server it was built on.
func (c *Core) imageReaches(ctx context.Context, rel *store.Release, rt node.Runtime) (bool, error) {
	if rel.ImageDigest != "" && c.reg != nil {
		return true, nil
	}
	if !strings.HasPrefix(rel.ImageRef, buildTagPrefix) {
		return true, nil
	}
	return rt.HasImage(ctx, rel.ImageRef)
}

// startMove drives a move in the background unless a goroutine does
// already.
func (c *Core) startMove(id int64) {
	c.moves.mu.Lock()
	if c.moves.driving[id] {
		c.moves.mu.Unlock()
		return
	}
	c.moves.driving[id] = true
	c.moves.mu.Unlock()
	c.wg.Go(func() {
		defer func() {
			c.moves.mu.Lock()
			delete(c.moves.driving, id)
			c.moves.mu.Unlock()
		}()
		// A rollback or clean-up that fails is tried again, backing off
		// from 5 s to 5 min, until it succeeds or waits for a server: a
		// timer that exists only while a move is failing to finish.
		for wait := 5 * time.Second; ; wait = min(wait*2, 5*time.Minute) {
			err := c.driveMove(c.ctx, id)
			if err == nil || errors.Is(err, errMoveWaits) || c.ctx.Err() != nil {
				return
			}
			c.log.Error("environment move; trying again", "move", id, "in", wait, "err", err)
			select {
			case <-time.After(wait):
			case <-c.ctx.Done():
				return
			}
		}
	})
}

// resumeMoves picks up every move that hasn't ended: at start (a move a
// restart cut short) and when a server reconnects (one that waited for
// it). One cut short before its switch rolls back.
func (c *Core) resumeMoves(ctx context.Context, restarted bool) {
	c.moves.mu.Lock()
	ready := c.moves.recovered
	c.moves.mu.Unlock()
	if !restarted && !ready {
		return // Start resumes them, after marking those to roll back
	}
	rows, err := c.store.ListOpenEnvironmentMoves(ctx)
	if err != nil {
		c.log.Error("list moves to resume", "err", err)
		return
	}
	for _, m := range rows {
		if restarted && m.Status == string(apitypes.MoveRunning) && moveBeforeSwitch(apitypes.MoveStep(m.Step)) {
			msg := fmt.Sprintf("the daemon restarted during the move (%s); it rolled back", m.Step)
			if err := c.store.SetEnvironmentMoveStatus(ctx, store.SetEnvironmentMoveStatusParams{Status: string(apitypes.MoveRollingBack), Error: msg, ID: m.ID}); err != nil {
				c.log.Error("roll back a move", "move", m.ID, "err", err)
				continue
			}
		}
		c.startMove(m.ID)
	}
	if restarted {
		c.moves.mu.Lock()
		c.moves.recovered = true
		c.moves.mu.Unlock()
	}
}

// moveBeforeSwitch reports whether a move on step s hasn't switched yet.
func moveBeforeSwitch(s apitypes.MoveStep) bool {
	return s != apitypes.MoveCleanup && s != apitypes.MoveDone
}

// driveMove takes a move as far as it can: forward, or back once it
// failed before its switch. errMoveWaits means a server it needs is
// offline.
func (c *Core) driveMove(ctx context.Context, id int64) error {
	m, err := c.store.GetEnvironmentMove(ctx, id)
	if err != nil {
		return err
	}
	es, err := c.envByID(ctx, m.EnvironmentID)
	if err != nil {
		return err
	}
	switch {
	case m.Status == string(apitypes.MoveRollingBack):
		return c.rollbackMove(ctx, es, m)
	case !moveBeforeSwitch(apitypes.MoveStep(m.Step)):
		return c.cleanupMove(ctx, es, m)
	}
	err = c.forwardMove(ctx, es, m)
	if err == nil {
		// Switched: the environment is on the target now, and only the
		// clean-up is left, which never rolls back.
		if es, err = c.envByID(ctx, es.env.ID); err != nil {
			return err
		}
		if m, err = c.store.GetEnvironmentMove(ctx, m.ID); err != nil {
			return err
		}
		return c.cleanupMove(ctx, es, m)
	}
	if ctx.Err() != nil {
		return err
	}
	msg := err.Error()
	if errors.Is(err, errMoveWaits) {
		msg = "a server went offline during the move: " + msg
	}
	if err := c.store.SetEnvironmentMoveStatus(ctx, store.SetEnvironmentMoveStatusParams{Status: string(apitypes.MoveRollingBack), Error: msg, ID: m.ID}); err != nil {
		return err
	}
	m.Status, m.Error = string(apitypes.MoveRollingBack), msg
	c.publishMoveRow(ctx, es, m)
	return c.rollbackMove(ctx, es, m)
}

// envByID loads an environment and its project.
func (c *Core) envByID(ctx context.Context, id int64) (envScope, error) {
	e, err := c.store.GetEnvironment(ctx, id)
	if err != nil {
		return envScope{}, err
	}
	p, err := c.store.GetProject(ctx, e.ProjectID)
	return envScope{p, e}, err
}

// loadMoving loads a move's services, scoped in es (the environment as
// it is now).
func (c *Core) loadMoving(ctx context.Context, es envScope, moveID int64) ([]movingService, error) {
	rows, err := c.store.ListEnvironmentMoveServices(ctx, moveID)
	if err != nil {
		return nil, err
	}
	vols, err := c.store.ListEnvironmentVolumes(ctx, es.env.ID)
	if err != nil {
		return nil, err
	}
	out := make([]movingService, 0, len(rows))
	for _, r := range rows {
		s, err := c.store.GetService(ctx, r.ServiceID)
		if err != nil {
			return nil, err
		}
		m := movingService{ss: svcScope{es, s}, row: r}
		if r.ReleaseID != nil {
			rel, err := c.store.GetRelease(ctx, *r.ReleaseID)
			if err != nil {
				return nil, err
			}
			m.rel = &rel
		}
		if r.Stateful {
			for _, v := range vols {
				if v.ServiceID == s.ID && !v.Pending {
					m.vols = append(m.vols, v)
				}
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// moveRuntimes returns the source's and target's runtimes, or
// errMoveWaits when one is offline.
func (c *Core) moveRuntimes(m store.EnvironmentMove) (node.Runtime, node.Runtime, error) {
	src, dst := c.connected(m.FromServerID), c.connected(m.ToServerID)
	if src == nil || dst == nil {
		return nil, nil, errMoveWaits
	}
	return src, dst, nil
}

// forwardMove runs a move's steps from the one it is on through the
// switch.
func (c *Core) forwardMove(ctx context.Context, es envScope, m store.EnvironmentMove) error {
	svcs, err := c.loadMoving(ctx, es, m.ID)
	if err != nil {
		return err
	}
	steps := []struct {
		step apitypes.MoveStep
		run  func(context.Context, envScope, store.EnvironmentMove, []movingService) error
	}{
		{apitypes.MovePrepare, c.movePrepare},
		{apitypes.MoveStop, c.moveStop},
		{apitypes.MoveCopy, c.moveCopy},
		{apitypes.MoveStart, c.moveStart},
		{apitypes.MoveSwitch, c.moveSwitch},
	}
	i := slices.IndexFunc(steps, func(s struct {
		step apitypes.MoveStep
		run  func(context.Context, envScope, store.EnvironmentMove, []movingService) error
	}) bool {
		return s.step == apitypes.MoveStep(m.Step)
	})
	for _, s := range steps[max(i, 0):] {
		if err := c.moveStep(ctx, es, &m, s.step); err != nil {
			return err
		}
		if err := s.run(ctx, es, m, svcs); err != nil {
			return err
		}
	}
	return nil
}

// moveStep records that a move is on step and tells open pages.
func (c *Core) moveStep(ctx context.Context, es envScope, m *store.EnvironmentMove, step apitypes.MoveStep) error {
	if hook := c.moves.hook; hook != nil {
		hook(m.ID, step)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.store.SetEnvironmentMoveStep(ctx, store.SetEnvironmentMoveStepParams{Step: string(step), ID: m.ID}); err != nil {
		return err
	}
	m.Step = string(step)
	c.publishMoveRow(ctx, es, *m)
	return nil
}

// setMoveService records how far a service of a move got.
func (c *Core) setMoveService(ctx context.Context, es envScope, m store.EnvironmentMove, s *movingService, status, containerID string, copied int64, errText string) {
	s.row.Status, s.row.ContainerID, s.row.CopiedBytes, s.row.Error = status, containerID, copied, errText
	if err := c.store.SetEnvironmentMoveService(ctx, store.SetEnvironmentMoveServiceParams{
		Status: status, ContainerID: containerID, CopiedBytes: copied, Error: errText, MoveID: m.ID, ServiceID: s.ss.svc.ID,
	}); err != nil {
		c.log.Error("record a move's service", "move", m.ID, "service", s.ss.String(), "err", err)
	}
	c.publishMoveRow(ctx, es, m)
}

// movePrepare gets the target ready while the source serves: the images,
// the environment's network, the volumes and the restic image for the
// copy.
func (c *Core) movePrepare(ctx context.Context, es envScope, m store.EnvironmentMove, svcs []movingService) error {
	src, dst, err := c.moveRuntimes(m)
	if err != nil {
		return err
	}
	to, err := c.store.GetServer(ctx, m.ToServerID)
	if err != nil {
		return err
	}
	network := networkName(es)
	if err := dst.EnsureNetwork(ctx, network, node.Labels{ControlPlane: c.install, Project: strconv.FormatInt(es.project.ID, 10), Environment: strconv.FormatInt(es.env.ID, 10)}); err != nil {
		return fmt.Errorf("create network %s on %s: %w", network, to.Name, c.nodeErr(to.ID, err))
	}
	stateful := false
	for _, s := range svcs {
		if s.rel != nil {
			if err := c.pullForMove(ctx, s.ss, s.rel, dst, to); err != nil {
				return err
			}
		}
		for _, v := range s.vols {
			stateful = true
			if err := dst.EnsureVolume(ctx, v.DockerName, c.labels(s.ss, 0)); err != nil {
				return fmt.Errorf("create volume %s on %s: %w", v.DockerName, to.Name, c.nodeErr(to.ID, err))
			}
		}
	}
	if !stateful {
		return nil
	}
	if err := c.ensureImage(ctx, m.FromServerID, src, c.cfg.Backup.ResticImage); err != nil {
		return err
	}
	return c.ensureImage(ctx, to.ID, dst, c.cfg.Backup.ResticImage)
}

// pullForMove makes a release's image present on the target: by digest
// from the platform registry when it is there, else by its reference.
func (c *Core) pullForMove(ctx context.Context, ss svcScope, rel *store.Release, rt node.Runtime, srv store.Server) error {
	ref := rel.ImageRef
	if ok, err := rt.HasImage(ctx, ref); err != nil || ok {
		return c.nodeErr(srv.ID, err)
	}
	from := ref
	if rel.ImageDigest != "" && c.reg != nil {
		from = c.registryRef(ref, rel.ImageDigest)
		if ok, err := rt.HasImage(ctx, from); err != nil || ok {
			return c.nodeErr(srv.ID, err)
		}
	}
	var opts []node.PullOptions
	if repo, ok := c.inRegistry(from); ok {
		if !pullAllowed(ss, repo) {
			return fmt.Errorf("image %s of %s belongs to another project", from, ss)
		}
		defer c.useRegistry()()
		opts = append(opts, node.PullOptions{Token: c.reg.Token("server "+srv.Name, repo, false)})
	}
	if _, err := rt.Pull(ctx, from, io.Discard, opts...); err != nil {
		return fmt.Errorf("pull %s on %s for %s: %w", from, srv.Name, ss.svc.Name, c.nodeErr(srv.ID, err))
	}
	return nil
}

// moveStop stops the stateful services on the source: their data stops
// changing, and the downtime starts.
func (c *Core) moveStop(ctx context.Context, es envScope, m store.EnvironmentMove, svcs []movingService) error {
	if _, _, err := c.moveRuntimes(m); err != nil {
		return err
	}
	stopped := false
	for i := range svcs {
		s := &svcs[i]
		if !s.stateful() || !s.runs() {
			continue
		}
		if !stopped {
			stopped = true
			if err := c.store.SetEnvironmentMoveDowntime(ctx, store.SetEnvironmentMoveDowntimeParams{DowntimeFrom: ptr(now()), ID: m.ID}); err != nil {
				return err
			}
		}
		if err := c.removeReleaseContainers(ctx, s.ss, s.rel.ID, keepData); err != nil {
			return fmt.Errorf("stop %s: %w", s.ss.svc.Name, c.nodeErr(m.FromServerID, err))
		}
		c.setStatus(s.ss, apitypes.ServiceStatus{State: apitypes.ServiceStopped})
		c.setMoveService(ctx, es, m, s, "stopped", "", 0, "")
	}
	return nil
}

// moveCopy streams each stateful service's volumes from the source to the
// target.
func (c *Core) moveCopy(ctx context.Context, es envScope, m store.EnvironmentMove, svcs []movingService) error {
	src, dst, err := c.moveRuntimes(m)
	if err != nil {
		return err
	}
	for i := range svcs {
		s := &svcs[i]
		if !s.stateful() {
			continue
		}
		var copied int64
		for _, v := range s.vols {
			n, err := c.copyVolume(ctx, s.ss, m, src, dst, v)
			copied += n
			if err != nil {
				c.setMoveService(ctx, es, m, s, "failed", "", copied, err.Error())
				return fmt.Errorf("copy %s's volume %s: %w", s.ss.svc.Name, v.Name, err)
			}
		}
		c.setMoveService(ctx, es, m, s, "copied", "", copied, "")
	}
	return nil
}

// countingWriter counts what passes through it.
type countingWriter struct {
	w io.Writer
	n int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.n += int64(n)
	return n, err
}

// copyVolume streams one volume: a job on the source archives it to its
// standard output, a job on the target empties the volume and unpacks
// its standard input, joined by a pipe here (JobSpec.Stream, which the
// node link carries since protocol 16). Both report how many entries the
// volume holds, which must match. It returns the archive's size.
func (c *Core) copyVolume(ctx context.Context, ss svcScope, m store.EnvironmentMove, src, dst node.Runtime, v store.Volume) (int64, error) {
	pr, pw := io.Pipe()
	out := &countingWriter{w: pw}
	type result struct {
		output string
		err    error
	}
	exported := make(chan result, 1)
	name := fmt.Sprintf("dock_p%d_e%d_s%d_move%d", ss.project.ID, ss.env.ID, ss.svc.ID, m.ID)
	go func() {
		var stderr tail
		_, err := c.runToolJob(ctx, m.FromServerID, src, node.JobSpec{
			Name: name + "_out", Labels: c.labels(ss, 0), Script: moveExportScript,
			Mounts: []node.Mount{{Volume: v.DockerName, Path: "/v", ReadOnly: true}},
			Stream: &node.JobStream{Stdout: out, Stderr: &stderr},
		})
		_ = pw.CloseWithError(err) // nil: the import reads the end
		exported <- result{stderr.String(), err}
	}()
	var stdout tail
	_, err := c.runToolJob(ctx, m.ToServerID, dst, node.JobSpec{
		Name: name + "_in", Labels: c.labels(ss, 0), Script: moveImportScript,
		Mounts: []node.Mount{{Volume: v.DockerName, Path: "/v"}},
		Stream: &node.JobStream{Stdin: pr, Stdout: &stdout},
	})
	_ = pr.CloseWithError(errors.New("the target stopped reading")) // unblocks the export if the import died
	ex := <-exported
	switch {
	case ex.err != nil:
		return out.n, fmt.Errorf("archive it on the source: %w%s", ex.err, withOutput(ex.output))
	case err != nil:
		return out.n, fmt.Errorf("unpack it on the target: %w%s", err, withOutput(stdout.String()))
	}
	// Both scripts count; a runtime that doesn't print them (tests) isn't
	// checked.
	a, b := moveEntries.FindStringSubmatch(ex.output), moveEntries.FindStringSubmatch(stdout.String())
	if a != nil && b != nil && a[1] != b[1] {
		return out.n, fmt.Errorf("the copy has %s entries and the volume %s", b[1], a[1])
	}
	return out.n, nil
}

// tail keeps the last 4 KB written to it: a job's report and errors, safe
// for the one goroutine a Stream writes from and a reader after the job.
type tail struct {
	mu sync.Mutex
	b  []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - 4096; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

// moveStart starts every service's active release on the target, the
// stateful ones first, each through its release's health check. The
// downtime ends once the stateful ones pass.
func (c *Core) moveStart(ctx context.Context, es envScope, m store.EnvironmentMove, svcs []movingService) error {
	_, dst, err := c.moveRuntimes(m)
	if err != nil {
		return err
	}
	order := slices.Clone(svcs)
	slices.SortStableFunc(order, func(a, b movingService) int {
		switch {
		case a.stateful() == b.stateful():
			return 0
		case a.stateful():
			return -1
		}
		return 1
	})
	downtime := slices.ContainsFunc(svcs, func(s movingService) bool { return s.stateful() && s.runs() })
	for _, s := range order {
		if !s.stateful() && downtime {
			downtime = false
			if err := c.store.SetEnvironmentMoveDowntime(ctx, store.SetEnvironmentMoveDowntimeParams{DowntimeTo: ptr(now()), ID: m.ID}); err != nil {
				return err
			}
		}
		if !s.runs() {
			continue
		}
		i := slices.IndexFunc(svcs, func(x movingService) bool { return x.ss.svc.ID == s.ss.svc.ID })
		if err := c.startOnTarget(ctx, es, m, dst, &svcs[i]); err != nil {
			return err
		}
	}
	if downtime {
		return c.store.SetEnvironmentMoveDowntime(ctx, store.SetEnvironmentMoveDowntimeParams{DowntimeTo: ptr(now()), ID: m.ID})
	}
	return nil
}

// startOnTarget starts a service's active release on the target with the
// host port the move reserved and waits for its health check. A
// container of it left there by an earlier attempt goes first.
func (c *Core) startOnTarget(ctx context.Context, es envScope, m store.EnvironmentMove, dst node.Runtime, s *movingService) error {
	cfg, err := c.openConfig(*s.rel)
	if err != nil {
		return err
	}
	if err := c.removeOn(ctx, m.ToServerID, dst, c.labels(s.ss, s.rel.ID), node.RemoveOptions{}); err != nil {
		return fmt.Errorf("remove an earlier container of %s on the target: %w", s.ss.svc.Name, err)
	}
	rel := *s.rel
	rel.HostPort = s.row.HostPort
	ctr, err := dst.Start(ctx, c.containerSpec(s.ss, rel, cfg))
	if err != nil {
		c.setMoveService(ctx, es, m, s, "failed", "", s.row.CopiedBytes, err.Error())
		return fmt.Errorf("start %s on the target: %w", s.ss.svc.Name, c.nodeErr(m.ToServerID, err))
	}
	c.setMoveService(ctx, es, m, s, "started", ctr.ID, s.row.CopiedBytes, "")
	hc := healthCheckOf(cfg)
	if err := dst.HealthCheck(ctx, ctr, hc); err != nil {
		err = fmt.Errorf("%s failed its health check on the target (%s, timeout %s): %w", s.ss.svc.Name, healthWhat(cfg, hc), hc.Timeout, err)
		c.setMoveService(ctx, es, m, s, "failed", ctr.ID, s.row.CopiedBytes, err.Error())
		return err
	}
	c.setMoveService(ctx, es, m, s, "healthy", ctr.ID, s.row.CopiedBytes, "")
	return nil
}

// removeOn stops and removes a server's containers that match want
// (node.Labels.Matches): one release's, or a whole environment's, with
// opts (removeContainer).
func (c *Core) removeOn(ctx context.Context, serverID int64, rt node.Runtime, want node.Labels, opts node.RemoveOptions) error {
	ctrs, err := rt.List(ctx)
	if err != nil {
		return c.nodeErr(serverID, err)
	}
	var errs []error
	for _, ct := range ctrs {
		if want.ControlPlane != "" && ct.Labels.ControlPlane == "" {
			continue // older than the label: never someone else's to remove here
		}
		if ct.Labels.Matches(want) {
			errs = append(errs, c.removeContainer(ctx, serverID, ct.ID, opts))
		}
	}
	return errors.Join(errs...)
}

// moveSwitch points the environment at the target in one transaction:
// the environment's server, each active release's container and host
// port there, the project's placement for the kind and Dock's Cloudflare
// records; the move goes on to its clean-up. Then both servers' routes
// are applied and generated domains follow the target's base domain.
func (c *Core) moveSwitch(ctx context.Context, es envScope, m store.EnvironmentMove, svcs []movingService) error {
	if _, _, err := c.moveRuntimes(m); err != nil {
		return err
	}
	var retargeted []string
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.SetEnvironmentServer(ctx, store.SetEnvironmentServerParams{ServerID: m.ToServerID, ID: es.env.ID}); err != nil {
			return err
		}
		for _, s := range svcs {
			if s.rel == nil {
				continue
			}
			if err := q.MoveReleaseServing(ctx, store.MoveReleaseServingParams{HostPort: s.row.HostPort, ContainerID: s.row.ContainerID, ID: s.rel.ID}); err != nil {
				return err
			}
		}
		pinned := false
		if pl, err := q.GetPlacement(ctx, store.GetPlacementParams{ProjectID: es.project.ID, Kind: es.env.Kind}); err == nil {
			pinned = pl.Pinned
		} else if !store.IsNotFound(err) {
			return err
		}
		if err := q.UpsertPlacement(ctx, store.UpsertPlacementParams{ProjectID: es.project.ID, Kind: es.env.Kind, ServerID: m.ToServerID, Pinned: pinned}); err != nil {
			return err
		}
		var err error
		if retargeted, err = q.RetargetCloudflareRecords(ctx, store.RetargetCloudflareRecordsParams{ServerID: &m.ToServerID, UpdatedAt: now(), EnvironmentID: es.env.ID}); err != nil {
			return err
		}
		return q.SetEnvironmentMoveStep(ctx, store.SetEnvironmentMoveStepParams{Step: string(apitypes.MoveCleanup), ID: m.ID})
	})
	if err != nil {
		return err
	}
	moved := es
	moved.env.ServerID = m.ToServerID
	for _, s := range svcs {
		if s.runs() {
			ss := svcScope{moved, s.ss.svc}
			c.setStatus(ss, apitypes.ServiceStatus{State: apitypes.ServiceRunning})
		}
	}
	for _, id := range []int64{m.ToServerID, m.FromServerID} {
		if err := c.applyRoutes(ctx, id); err != nil {
			c.log.Warn("apply routes after a move", "server", id, "err", err)
		}
	}
	if len(retargeted) > 0 {
		c.kickCloudflare()
	}
	c.kickDomains()
	return nil
}

// cleanupMove removes what is left of the environment on the source (its
// containers and network), follows generated domains to the target's base
// domain, and ends the move. It waits while the source is offline.
func (c *Core) cleanupMove(ctx context.Context, es envScope, m store.EnvironmentMove) error {
	if err := c.moveStep(ctx, es, &m, apitypes.MoveCleanup); err != nil {
		return err
	}
	src := c.connected(m.FromServerID)
	if src == nil {
		return errMoveWaits
	}
	from, err := c.store.GetServer(ctx, m.FromServerID)
	if err != nil {
		return err
	}
	to, err := c.store.GetServer(ctx, m.ToServerID)
	if err != nil {
		return err
	}
	var notes []string
	envLabels := node.Labels{ControlPlane: c.install, Project: strconv.FormatInt(es.project.ID, 10), Environment: strconv.FormatInt(es.env.ID, 10)}
	if err := c.removeOn(ctx, from.ID, src, envLabels, keepData); err != nil {
		notes = append(notes, fmt.Sprintf("some of the environment's old containers on %s couldn't be removed (%v); remove them by hand", from.Name, err))
	}
	if err := src.RemoveNetwork(ctx, networkName(es), envLabels); err != nil && !errors.Is(err, node.ErrNotFound) {
		notes = append(notes, fmt.Sprintf("network %s on %s couldn't be removed: %v", networkName(es), from.Name, c.nodeErr(from.ID, err)))
	}
	more, err := c.moveDomains(ctx, es, from, to)
	if err != nil {
		return err
	}
	notes = append(notes, more...)
	svcs, err := c.loadMoving(ctx, es, m.ID)
	if err != nil {
		return err
	}
	var old []string
	for _, s := range svcs {
		for _, v := range s.vols {
			old = append(old, v.DockerName)
		}
	}
	if len(old) > 0 {
		notes = append(notes, fmt.Sprintf("the old volumes stay on %s as a last copy; once the moved environment checks out, remove them there with `docker volume rm %s`", from.Name, strings.Join(old, " ")))
	}
	if n, err := c.store.CountEnvironmentSnapshotsOn(ctx, store.CountEnvironmentSnapshotsOnParams{EnvironmentID: es.env.ID, ServerID: from.ID}); err != nil {
		return err
	} else if n > 0 {
		notes = append(notes, fmt.Sprintf("%d volume snapshots of its services stay on %s, in its snapshot volume; they can't be restored on %s, and retention removes them (or `dock volume snapshot delete`)", n, from.Name, to.Name))
	}
	if err := c.store.FinishEnvironmentMove(ctx, store.FinishEnvironmentMoveParams{
		Status: string(apitypes.MoveSucceeded), Step: string(apitypes.MoveDone), Notes: strings.Join(notes, "\n"), FinishedAt: ptr(now()), ID: m.ID,
	}); err != nil {
		return err
	}
	m, err = c.store.GetEnvironmentMove(ctx, m.ID)
	if err != nil {
		return err
	}
	summary := fmt.Sprintf("moved to server %s from %s", to.Name, from.Name)
	if m.DowntimeFrom != nil && m.DowntimeTo != nil {
		summary += fmt.Sprintf("; stateful services were down %s", m.DowntimeTo.Sub(*m.DowntimeFrom).Round(time.Second))
	}
	if len(notes) > 0 {
		summary += "; " + strings.Join(notes, "; ")
	}
	c.audit(ctx, System, "environment.moved", &es.project, &es.env, es.String(), summary)
	c.publishMoveRow(ctx, es, m)
	return nil
}

// moveDomains follows a moved environment's domains: generated ones under
// the source's base domain are replaced by ones under the target's, and
// the others that Dock doesn't route through Cloudflare are listed with
// the DNS records they need now.
func (c *Core) moveDomains(ctx context.Context, es envScope, from, to store.Server) ([]string, error) {
	doms, err := c.store.ListEnvironmentDomains(ctx, es.env.ID)
	if err != nil {
		return nil, err
	}
	svcs, err := c.store.ListServices(ctx, es.env.ID)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, s := range svcs {
		names[s.ID] = s.Name
	}
	fromBase, toIng := c.ingressOf(from).baseDomain, c.ingressOf(to)
	a := System.InOrg(es.project.OrgID)
	var notes, dns []string
	for _, d := range doms {
		ref := ServiceRef{es.project.Name, es.env.Name, names[d.ServiceID]}
		if d.Generated && fromBase != "" && strings.HasSuffix(d.Hostname, "."+fromBase) {
			if toIng.baseDomain == fromBase {
				continue
			}
			if err := c.RemoveDomain(ctx, a, ref, d.Hostname); err != nil {
				notes = append(notes, fmt.Sprintf("generated domain %s couldn't be removed: %v", d.Hostname, err))
				continue
			}
			if toIng.baseDomain == "" {
				notes = append(notes, fmt.Sprintf("removed generated domain %s: server %s has no base domain", d.Hostname, to.Name))
				continue
			}
			if ch, err := c.GenerateDomain(ctx, a, ref); err != nil {
				notes = append(notes, fmt.Sprintf("generated domain %s is gone, and a new one couldn't be made: %v", d.Hostname, err))
			} else {
				notes = append(notes, fmt.Sprintf("generated domain %s is now %s", d.Hostname, ch.Domain.Hostname))
			}
			continue
		}
		if _, err := c.store.GetCloudflareRecord(ctx, d.Hostname); err == nil {
			continue // Dock points it at the target's tunnel
		} else if !store.IsNotFound(err) {
			return nil, err
		}
		dns = append(dns, d.Hostname)
	}
	if len(dns) > 0 {
		var records []string
		if toIng.mode == apitypes.IngressCloudflareTunnel {
			if toIng.tunnel != "" {
				records = append(records, "CNAME "+toIng.tunnel)
			}
		} else {
			for _, ip := range toIng.publicIPs {
				records = append(records, ipRecordType(ip)+" "+ip)
			}
		}
		where := "server " + to.Name
		if len(records) > 0 {
			where = strings.Join(records, " or ")
		}
		notes = append(notes, fmt.Sprintf("point the DNS of %s at %s: until then they reach the old server, which no longer serves them", strings.Join(dns, ", "), where))
	}
	return notes, nil
}

// rollbackMove undoes a move that failed before its switch: what it made
// on the target goes, and the stateful services start again on the
// source, whose data was never written. It waits for a server that is
// offline.
func (c *Core) rollbackMove(ctx context.Context, es envScope, m store.EnvironmentMove) error {
	if c.connected(m.FromServerID) == nil {
		return errMoveWaits // its services start again there
	}
	svcs, err := c.loadMoving(ctx, es, m.ID)
	if err != nil {
		return err
	}
	// The source's services start again at once, even with the target
	// offline: their data never left the source's volumes, and the
	// target's copy is another volume on another server, so no volume is
	// ever mounted twice. The target is cleaned up once it is back.
	var errs []error
	for _, s := range svcs {
		if !s.runs() {
			continue
		}
		if err := c.ensureRunning(ctx, s.ss, *s.rel); err != nil {
			errs = append(errs, fmt.Errorf("start %s again on the source: %w", s.ss.svc.Name, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	dst := c.connected(m.ToServerID)
	if dst == nil {
		return errMoveWaits // the target's containers go when it reconnects
	}
	envLabels := node.Labels{ControlPlane: c.install, Project: strconv.FormatInt(es.project.ID, 10), Environment: strconv.FormatInt(es.env.ID, 10)}
	if err := c.removeOn(ctx, m.ToServerID, dst, envLabels, keepData); err != nil {
		return fmt.Errorf("remove the move's containers on the target: %w", err)
	}
	if err := dst.RemoveNetwork(ctx, networkName(es), envLabels); err != nil && !errors.Is(err, node.ErrNotFound) {
		c.log.Warn("remove a move's network on the target", "move", m.ID, "err", err)
	}
	msg := m.Error
	var notes string
	if m.Step != string(apitypes.MovePrepare) {
		var vols []string
		for _, s := range svcs {
			for _, v := range s.vols {
				vols = append(vols, v.DockerName)
			}
		}
		if len(vols) > 0 {
			to, _ := c.store.GetServer(ctx, m.ToServerID)
			notes = fmt.Sprintf("the volumes it made on %s stay there, possibly with a partial copy (a new move empties them first); remove them there with `docker volume rm %s` if you won't move again", to.Name, strings.Join(vols, " "))
		}
	}
	if err := c.store.FinishEnvironmentMove(ctx, store.FinishEnvironmentMoveParams{
		Status: string(apitypes.MoveFailed), Step: m.Step, Error: msg, Notes: notes, FinishedAt: ptr(now()), ID: m.ID,
	}); err != nil {
		return err
	}
	if m, err = c.store.GetEnvironmentMove(ctx, m.ID); err != nil {
		return err
	}
	to, _ := c.store.GetServer(ctx, m.ToServerID)
	c.audit(ctx, System, "environment.move_failed", &es.project, &es.env, es.String(),
		fmt.Sprintf("moving to server %s failed during %s and was rolled back; the environment stays where it was: %s", to.Name, m.Step, msg))
	c.publishMoveRow(ctx, es, m)
	return nil
}

// toMove is a move as the API shows it.
func (c *Core) toMove(ctx context.Context, es envScope, m store.EnvironmentMove) (apitypes.EnvironmentMove, error) {
	names, err := c.serverNames(ctx)
	if err != nil {
		return apitypes.EnvironmentMove{}, err
	}
	rows, err := c.store.ListEnvironmentMoveServices(ctx, m.ID)
	if err != nil {
		return apitypes.EnvironmentMove{}, err
	}
	svcs, err := c.store.ListServices(ctx, es.env.ID)
	if err != nil {
		return apitypes.EnvironmentMove{}, err
	}
	svcNames := map[int64]string{}
	for _, s := range svcs {
		svcNames[s.ID] = s.Name
	}
	out := apitypes.EnvironmentMove{
		ID: m.ID, Project: es.project.Name, Environment: es.env.Name, From: names[m.FromServerID], To: names[m.ToServerID],
		Status: apitypes.MoveStatus(m.Status), Step: apitypes.MoveStep(m.Step), Services: []apitypes.MoveService{},
		DowntimeFrom: m.DowntimeFrom, DowntimeTo: m.DowntimeTo, Error: m.Error, Notes: []string{},
		CreatedBy: m.CreatedBy, StartedAt: m.StartedAt, FinishedAt: m.FinishedAt,
	}
	if m.Notes != "" {
		out.Notes = strings.Split(m.Notes, "\n")
	}
	for _, r := range rows {
		out.Services = append(out.Services, apitypes.MoveService{
			Service: svcNames[r.ServiceID], Stateful: r.Stateful, Status: r.Status, CopiedBytes: r.CopiedBytes, Error: r.Error,
		})
	}
	return out, nil
}

// publishMoveRow publishes a move's state to open pages.
func (c *Core) publishMoveRow(ctx context.Context, es envScope, m store.EnvironmentMove) {
	out, err := c.toMove(ctx, es, m)
	if err != nil {
		c.log.Warn("publish a move", "move", m.ID, "err", err)
		return
	}
	c.publishMove(es, out)
}

// publishMove sends an environment_move event.
func (c *Core) publishMove(es envScope, m apitypes.EnvironmentMove) {
	c.bus.publish(busEvent{orgID: es.project.OrgID, projectID: es.project.ID, envID: es.env.ID, ev: apitypes.Event{
		Type: apitypes.EventEnvironmentMove, Project: es.project.Name, Environment: es.env.Name, Move: &m,
	}})
}
