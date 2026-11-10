package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// defaultPreDeployTimeout bounds a pre-deploy command whose service (and
// dock.toml) sets no timeout; maxPreDeployTimeout is the most either may
// set.
const (
	defaultPreDeployTimeout = 10 * time.Minute
	maxPreDeployTimeout     = time.Hour
)

// preDeployTailLines is how many of a failed command's last output lines
// go into the release's events; the build log has all of it.
const preDeployTailLines = 10

// runsPreDeploy reports whether a release from src runs the service's
// pre-deploy command (ADR 0045). Rollbacks and restarts don't: they run an
// image that already served traffic, and an older release's migration
// tool may refuse a schema a newer one migrated.
func runsPreDeploy(src apitypes.ReleaseSource) bool {
	return src != apitypes.SourceRollback && src != apitypes.SourceRestart
}

// preDeploy runs the release's pre-deploy command as a one-off job on the
// release's server: the new image, its resolved variables and resources,
// the environment's private network (so it reaches the database), no
// volumes. It runs before anything is started or stopped, so a failure
// leaves the previous release serving. With pre_deploy_snapshot on, the
// environment's stateful services are snapshotted first. The output goes
// to the build log; a failed snapshot, a non-zero exit, a timeout or a
// job that can't run fails the release.
func (d *deployment) preDeploy(ctx context.Context) error {
	cmd := d.cfg.PreDeploy
	if cmd == "" || !runsPreDeploy(apitypes.ReleaseSource(d.rel.Source)) {
		return nil
	}
	if d.cfg.PreDeploySnapshot {
		if err := d.snapshotBeforePreDeploy(ctx); err != nil {
			return err
		}
	}
	timeout := defaultPreDeployTimeout
	if d.cfg.PreDeployTimeout > 0 {
		timeout = time.Duration(d.cfg.PreDeployTimeout) * time.Second
	}
	d.event(ctx, "Running pre-deploy command: "+cmd)
	_, _ = fmt.Fprintf(d.log, "\n$ %s\n", cmd)
	jctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	res, err := d.rt.RunJob(jctx, d.c.releaseJob(d.ss, d.rel, d.cfg, containerName(d.ss, d.rel.Number)+"_predeploy", cmd))
	if res.Output != "" {
		_, _ = d.log.Write([]byte(res.Output))
		if !strings.HasSuffix(res.Output, "\n") {
			_, _ = d.log.Write([]byte("\n"))
		}
	}
	took := time.Since(start).Round(time.Second)
	// How it ran, for a diagnosis (ADR 0070).
	d.pre = &apitypes.DiagnosisPreDeploy{Command: cmd, DurationMS: time.Since(start).Milliseconds(), Tail: diagLines(tailLines(res.Output, diagPreDeployLines))}
	if err == nil {
		d.pre.ExitCode = ptr(res.ExitCode)
	}
	switch {
	case err != nil && ctx.Err() == nil && errors.Is(jctx.Err(), context.DeadlineExceeded):
		d.pre.TimedOut = true
		return step(apitypes.CodeDeployFailed, "pre-deploy command took longer than %s and was stopped; raise the service's pre-deploy timeout if it needs longer", timeout)
	case err != nil:
		return step(apitypes.CodeDeployFailed, "pre-deploy command could not run: %w", d.c.nodeErr(d.ss.env.ServerID, err))
	case res.ExitCode != 0:
		if tail := lastLines(res.Output, preDeployTailLines); tail != "" {
			d.event(ctx, "Pre-deploy command output (last lines):\n"+tail)
		}
		return step(apitypes.CodeDeployFailed, "pre-deploy command exited with code %d after %s; the previous release keeps serving (output in the build log)", res.ExitCode, took)
	}
	d.event(ctx, fmt.Sprintf("Pre-deploy command finished in %s", took))
	return nil
}

// snapshotBeforePreDeploy snapshots every deployed stateful service in
// the release's environment (ADR 0047), since the command may change any
// of them. The release's own service is one too: its previous release
// still runs (a stateful service stops it only after the command). A
// service never deployed has no data yet and is skipped. Each snapshot
// is a release event and a build log line. The first that fails fails
// the release before the command runs, so nothing has changed yet.
func (d *deployment) snapshotBeforePreDeploy(ctx context.Context) error {
	c := d.c
	vols, err := c.store.ListEnvironmentVolumes(ctx, d.ss.env.ID)
	if err != nil {
		return err
	}
	stateful := map[int64]bool{}
	for _, v := range vols {
		stateful[v.ServiceID] = stateful[v.ServiceID] || !v.Pending
	}
	if len(stateful) == 0 {
		return nil
	}
	actives, err := c.store.ListEnvironmentActiveReleases(ctx, d.ss.env.ID)
	if err != nil {
		return err
	}
	deployed := map[int64]bool{}
	for _, r := range actives {
		deployed[r.ServiceID] = true
	}
	svcs, err := c.store.ListServices(ctx, d.ss.env.ID)
	if err != nil {
		return err
	}
	for _, s := range svcs {
		if !stateful[s.ID] || !deployed[s.ID] || s.ArchivedAt != nil {
			continue
		}
		note := fmt.Sprintf("before %s release %d's pre-deploy command", d.ss.svc.Name, d.rel.Number)
		snap, err := c.SnapshotBefore(ctx, System, svcScope{d.ss.envScope, s}, apitypes.SnapshotBeforeDeploy, note)
		if err != nil {
			return step(apitypes.CodeDeployFailed, "snapshot of %s before the pre-deploy command failed, so the command didn't run and the previous release keeps serving: %w; "+
				"fix that and deploy again, or turn pre_deploy_snapshot off (`dock service update %s --pre-deploy-snapshot=false`) to deploy without snapshots", s.Name, err, d.ss.svc.Name)
		}
		if snap == nil {
			continue
		}
		msg := fmt.Sprintf("Snapshot %d of %s taken before the pre-deploy command", snap.ID, s.Name)
		d.event(ctx, msg)
		_, _ = fmt.Fprintf(d.log, "%s\n", msg)
	}
	return nil
}

// releaseJob is a one-off job in a release's image (by ID), with its
// resolved variables and resource limits, on the environment's private
// network and without volumes: the pre-deploy command (ADR 0045) and the
// commands of `dock run` (ADR 0048) and scheduled jobs (ADR 0049). It carries the
// release's labels; as a job it is never taken for one of its containers.
func (c *Core) releaseJob(ss svcScope, rel store.Release, cfg releaseConfig, name, cmd string) node.JobSpec {
	image := rel.ImageRef
	if rel.ImageID != "" {
		image = rel.ImageID
	}
	return node.JobSpec{
		Name:    name,
		Image:   image,
		Labels:  c.labels(ss, rel.ID),
		Env:     cfg.Env,
		Script:  cmd,
		Network: networkName(ss.envScope),
		Resources: node.Resources{
			MemoryBytes: int64(cfg.MemoryMB) << 20,
			NanoCPUs:    int64(cfg.CPUs * 1e9),
		},
	}
}

// lastLines returns the last n lines of s, without its trailing newlines.
func lastLines(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	i := len(s)
	for range n {
		j := strings.LastIndexByte(s[:i], '\n')
		if j < 0 {
			return s
		}
		i = j
	}
	return s[i+1:]
}
