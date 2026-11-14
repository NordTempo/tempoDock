package core

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

func settingsOf(s store.Service) apitypes.ServiceSettings {
	return apitypes.ServiceSettings{
		Image: s.Image, Port: int(s.Port), HealthCheckPath: s.HealthCheckPath,
		HealthCheckTimeoutSeconds: int(s.HealthCheckTimeoutSeconds), StartCommand: s.StartCommand,
		DockerfilePath: s.DockerfilePath, BuildCommand: s.BuildCommand, MemoryMB: int(s.MemoryMb), CPUs: s.Cpus,
		HealthCheckIntervalSeconds:     int(s.HealthCheckIntervalSeconds),
		HealthCheckProbeTimeoutSeconds: int(s.HealthCheckProbeTimeoutSeconds),
		HealthCheckRetries:             int(s.HealthCheckRetries),
		HealthCheckStartPeriodSeconds:  int(s.HealthCheckStartPeriodSeconds),
		PreDeployCommand:               s.PreDeployCommand, PreDeployTimeoutSeconds: int(s.PreDeployTimeoutSeconds),
		PreDeploySnapshot: s.PreDeploySnapshot,
	}
}

// pendingSettings returns the staged settings, or nil if none are staged.
func pendingSettings(s store.Service) (*apitypes.ServiceSettings, error) {
	if s.PendingSettings == nil {
		return nil, nil
	}
	var p apitypes.ServiceSettings
	if err := json.Unmarshal([]byte(*s.PendingSettings), &p); err != nil {
		return nil, fmt.Errorf("service %d pending settings: %w", s.ID, err)
	}
	return &p, nil
}

func validateSettings(s apitypes.ServiceSettings, registry string, pulls *egressGuard) error {
	if err := validateImageRef(s.Image, registry, pulls); err != nil {
		return err
	}
	switch {
	case s.Port < 0 || s.Port > 65535:
		return invalid("port must be between 0 and 65535")
	case s.HealthCheckPath != "" && !strings.HasPrefix(s.HealthCheckPath, "/"):
		return invalid("health check path must start with /")
	case s.HealthCheckTimeoutSeconds < 0 || s.MemoryMB < 0 || s.CPUs < 0 || s.PreDeployTimeoutSeconds < 0:
		return invalid("timeouts and limits can't be negative")
	case s.HealthCheckIntervalSeconds < -1 || s.HealthCheckIntervalSeconds > maxLivenessSeconds:
		return invalid("health check interval must be between 0 and %d seconds (0 means %d), or -1 to turn the ongoing check off", maxLivenessSeconds, defaultLivenessInterval)
	case s.HealthCheckProbeTimeoutSeconds < 0 || s.HealthCheckProbeTimeoutSeconds > maxProbeTimeoutSeconds:
		return invalid("health check probe timeout must be between 0 and %d seconds (0 means %d)", maxProbeTimeoutSeconds, defaultProbeTimeout)
	case s.HealthCheckRetries < 0 || s.HealthCheckRetries > maxLivenessRetries:
		return invalid("health check retries must be between 0 and %d (0 means %d)", maxLivenessRetries, defaultLivenessRetries)
	case s.HealthCheckStartPeriodSeconds < 0 || s.HealthCheckStartPeriodSeconds > maxLivenessSeconds:
		return invalid("health check start period must be between 0 and %d seconds (0 means the health check timeout)", maxLivenessSeconds)
	case s.PreDeployTimeoutSeconds > int(maxPreDeployTimeout/time.Second):
		return invalid("pre-deploy timeout can be at most %d seconds (%s)", int(maxPreDeployTimeout/time.Second), maxPreDeployTimeout)
	case s.PreDeployCommand != "" && strings.TrimSpace(s.PreDeployCommand) == "":
		return invalid("pre-deploy command is blank; set it to an empty string to remove it")
	}
	return nil
}

// ListServices returns an environment's services, including archived ones.
func (c *Core) ListServices(ctx context.Context, a Actor, ref EnvRef) ([]apitypes.Service, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeRead, "listing services")
	if err != nil {
		return nil, err
	}
	// A fixed number of queries however many services there are, like
	// Overview: staged changes (with the services), active releases and
	// volumes.
	pending, rows, _, err := c.stagedChanges(ctx, c.store.Queries, es, false)
	if err != nil {
		return nil, err
	}
	actives, err := c.store.ListEnvironmentActiveReleases(ctx, es.env.ID)
	if err != nil {
		return nil, err
	}
	active := make(map[int64]*int64, len(actives))
	for _, r := range actives {
		active[r.ServiceID] = &r.Number
	}
	vols, err := c.store.ListEnvironmentVolumes(ctx, es.env.ID)
	if err != nil {
		return nil, err
	}
	stateful := map[int64]bool{}
	for _, v := range vols {
		stateful[v.ServiceID] = stateful[v.ServiceID] || !v.Pending
	}
	link := c.linkOf(ctx, a, es.env.ServerID)
	out := make([]apitypes.Service, 0, len(rows))
	for _, s := range rows {
		out = append(out, c.serviceOf(svcScope{es, s}, link, pending, active[s.ID], stateful[s.ID]))
	}
	return out, nil
}

// GetService returns one service.
func (c *Core) GetService(ctx context.Context, a Actor, ref ServiceRef) (apitypes.Service, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading a service")
	if err != nil {
		return apitypes.Service{}, err
	}
	pending, _, _, err := c.stagedChanges(ctx, c.store.Queries, ss.envScope, false)
	if err != nil {
		return apitypes.Service{}, err
	}
	return c.toService(ctx, a, ss, pending)
}

// CreateService creates a service. Its settings are live immediately (there
// is nothing running yet); nothing is deployed.
func (c *Core) CreateService(ctx context.Context, a Actor, ref EnvRef, req apitypes.CreateServiceRequest) (apitypes.Service, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeDeploy, "creating a service")
	if err != nil {
		return apitypes.Service{}, err
	}
	if err := notArchived(es.project); err != nil {
		return apitypes.Service{}, err
	}
	if err := validName("service", req.Name); err != nil {
		return apitypes.Service{}, err
	}
	if req.Name == sharedScope || req.Name == orgScope {
		return apitypes.Service{}, invalid("%q is reserved for variable references (${{%s.KEY}})", req.Name, req.Name)
	}
	// A service from a repository: the repository, branch and directory
	// are checked through the App before anything is created.
	var git *appRepoTarget
	var gitReq apitypes.GitConnectRequest
	if req.Git != nil {
		if req.Template != apitypes.TemplateNone || req.Image != "" {
			return apitypes.Service{}, invalid("a service from a repository is built from it; drop the image and the template")
		}
		branch := strings.TrimSpace(req.Git.Branch)
		repo, _, dir, err := validGitTarget(req.Git.Repo, cmp.Or(branch, "main"), req.Git.Dir)
		if err != nil {
			return apitypes.Service{}, err
		}
		if branch != "" {
			if _, branch, _, err = validGitTarget(repo, branch, dir); err != nil {
				return apitypes.Service{}, err
			}
		}
		if git, err = c.resolveAppTarget(ctx, es.project.OrgID, repo, branch, dir); err != nil {
			return apitypes.Service{}, err
		}
		gitReq = apitypes.GitConnectRequest{Mode: apitypes.GitModeApp, Repo: git.repo.FullName, Branch: git.branch, Dir: dir}
	}
	if req.Template != apitypes.TemplateNone {
		t, ok := templates[req.Template]
		if !ok {
			return apitypes.Service{}, invalid("unknown template %q; the templates are postgres and redis", req.Template)
		}
		ss, err := c.createFromTemplate(ctx, a, es, req.Name, t, templateOptions{})
		if ss.svc.ID == 0 {
			return apitypes.Service{}, err
		}
		svc, terr := c.toService(ctx, a, ss, apitypes.PendingChanges{})
		if err != nil {
			return svc, err
		}
		return svc, terr
	}
	set := apitypes.ServiceSettings{
		Image: req.Image, Port: req.Port, HealthCheckPath: req.HealthCheckPath,
		HealthCheckTimeoutSeconds: req.HealthCheckTimeoutSeconds, StartCommand: req.StartCommand,
		DockerfilePath: req.DockerfilePath, BuildCommand: req.BuildCommand, MemoryMB: req.MemoryMB, CPUs: req.CPUs,
		HealthCheckIntervalSeconds:     req.HealthCheckIntervalSeconds,
		HealthCheckProbeTimeoutSeconds: req.HealthCheckProbeTimeoutSeconds,
		HealthCheckRetries:             req.HealthCheckRetries,
		HealthCheckStartPeriodSeconds:  req.HealthCheckStartPeriodSeconds,
		PreDeployCommand:               req.PreDeployCommand, PreDeployTimeoutSeconds: req.PreDeployTimeoutSeconds,
		PreDeploySnapshot: req.PreDeploySnapshot,
	}
	if err := validateSettings(set, c.registryHost(), &c.pulls); err != nil {
		return apitypes.Service{}, err
	}
	s, err := c.store.CreateService(ctx, store.CreateServiceParams{
		EnvironmentID: es.env.ID, Name: req.Name, Image: set.Image, Port: int64(set.Port),
		HealthCheckPath: set.HealthCheckPath, HealthCheckTimeoutSeconds: int64(set.HealthCheckTimeoutSeconds),
		StartCommand: set.StartCommand, DockerfilePath: set.DockerfilePath, BuildCommand: set.BuildCommand,
		MemoryMb: int64(set.MemoryMB), Cpus: set.CPUs,
		HealthCheckIntervalSeconds:     int64(set.HealthCheckIntervalSeconds),
		HealthCheckProbeTimeoutSeconds: int64(set.HealthCheckProbeTimeoutSeconds),
		HealthCheckRetries:             int64(set.HealthCheckRetries),
		HealthCheckStartPeriodSeconds:  int64(set.HealthCheckStartPeriodSeconds),
		PreDeployCommand:               set.PreDeployCommand, PreDeployTimeoutSeconds: int64(set.PreDeployTimeoutSeconds),
		PreDeploySnapshot: set.PreDeploySnapshot,
	})
	if store.IsUnique(err) {
		return apitypes.Service{}, conflict("service %q already exists in %s", req.Name, es)
	}
	if err != nil {
		return apitypes.Service{}, err
	}
	c.audit(ctx, a, "service.create", &es.project, &es.env, es.String()+"/"+s.Name, "created service")
	if git != nil {
		ss := svcScope{es, s}
		where := fmt.Sprintf("-s %s -e %s -p %s", s.Name, es.env.Name, es.project.Name)
		if _, err := c.connectGit(ctx, a, ss, gitReq, git); err != nil {
			return apitypes.Service{}, fmt.Errorf("created service %s, but connecting it to %s failed (connect it with `dock git connect %s %s`): %w", ss, gitReq.Repo, gitReq.Repo, where, err)
		}
		if _, err := c.GitDeploy(ctx, a, ServiceRef{Project: ref.Project, Environment: ref.Environment, Service: s.Name}, apitypes.GitDeployRequest{}); err != nil {
			return apitypes.Service{}, fmt.Errorf("created service %s from %s, but its first deploy didn't start (start it with `dock git deploy %s`): %w", ss, gitReq.Repo, where, err)
		}
	}
	return c.toService(ctx, a, svcScope{es, s}, apitypes.PendingChanges{})
}

// UpdateService stages settings changes, optionally applying the
// environment's staged changes right away.
func (c *Core) UpdateService(ctx context.Context, a Actor, ref ServiceRef, req apitypes.UpdateServiceRequest) (apitypes.ChangeResult, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "changing service settings")
	if err != nil {
		return apitypes.ChangeResult{}, err
	}
	if ss.svc.ArchivedAt != nil {
		return apitypes.ChangeResult{}, invalid("service %s is archived", ss)
	}
	live := settingsOf(ss.svc)
	next := live
	if p, err := pendingSettings(ss.svc); err != nil {
		return apitypes.ChangeResult{}, err
	} else if p != nil {
		next = *p
	}
	patch(&next.Image, req.Image)
	patch(&next.Port, req.Port)
	patch(&next.HealthCheckPath, req.HealthCheckPath)
	patch(&next.HealthCheckTimeoutSeconds, req.HealthCheckTimeoutSeconds)
	patch(&next.StartCommand, req.StartCommand)
	patch(&next.DockerfilePath, req.DockerfilePath)
	patch(&next.BuildCommand, req.BuildCommand)
	patch(&next.MemoryMB, req.MemoryMB)
	patch(&next.CPUs, req.CPUs)
	patch(&next.HealthCheckIntervalSeconds, req.HealthCheckIntervalSeconds)
	patch(&next.HealthCheckProbeTimeoutSeconds, req.HealthCheckProbeTimeoutSeconds)
	patch(&next.HealthCheckRetries, req.HealthCheckRetries)
	patch(&next.HealthCheckStartPeriodSeconds, req.HealthCheckStartPeriodSeconds)
	patch(&next.PreDeployCommand, req.PreDeployCommand)
	patch(&next.PreDeployTimeoutSeconds, req.PreDeployTimeoutSeconds)
	patch(&next.PreDeploySnapshot, req.PreDeploySnapshot)
	if err := validateSettings(next, c.registryHost(), &c.pulls); err != nil {
		return apitypes.ChangeResult{}, err
	}
	var staged *string
	if next != live {
		b, _ := json.Marshal(next)
		s := string(b)
		staged = &s
	}
	if err := c.store.SetPendingSettings(ctx, store.SetPendingSettingsParams{PendingSettings: staged, ID: ss.svc.ID}); err != nil {
		return apitypes.ChangeResult{}, err
	}
	c.audit(ctx, a, "service.update", &ss.project, &ss.env, ss.String(), "staged settings: "+describeSettings(live, next))
	return c.changeResult(ctx, a, ss.envScope, req.Apply)
}

func patch[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}

// ArchiveService archives a service: its routes are removed and its
// container is stopped in the background. Volumes and the database rows
// are kept; real deletion is a separate, later operation.
func (c *Core) ArchiveService(ctx context.Context, a Actor, ref ServiceRef) (apitypes.Service, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeAdmin, "archiving a service")
	if err != nil {
		return apitypes.Service{}, err
	}
	var done []archivedService
	// One transaction, so no deploy can start between the check and the
	// archive (startRelease re-checks archived_at in its own).
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		as, ok, err := archiveTx(ctx, q, ss)
		if ok {
			done = append(done, as)
		}
		ss = as.svcScope
		return err
	})
	if err != nil {
		return apitypes.Service{}, err
	}
	var notes []string
	if len(done) > 0 {
		notes, _ = c.finishArchive(ctx, done)
		c.maybePruneRegistry()
		c.audit(ctx, a, "service.archive", &ss.project, &ss.env, ss.String(), archiveSummary("archived service; volumes kept", notes))
	}
	svc, err := c.toService(ctx, a, ss, apitypes.PendingChanges{})
	svc.ArchiveNotes = notes
	return svc, err
}

// archiveSummary is an archive's audit summary with its notes.
func archiveSummary(summary string, notes []string) string {
	return strings.Join(append([]string{summary}, notes...), "; ")
}

// archivedService is a service archiveTx archived, with the release that
// was active (nil if none).
type archivedService struct {
	svcScope
	active *store.Release
}

// archiveTx archives a service within a transaction, gives up the DNS
// records Dock made for its domains, and supersedes its active release, so from then on no event of that release's container
// counts as the service's status or as drift. ok is false if the service
// was already archived. A service that is deploying is refused.
func archiveTx(ctx context.Context, q *store.Queries, ss svcScope) (archivedService, bool, error) {
	svc, err := q.GetService(ctx, ss.svc.ID)
	if err != nil {
		return archivedService{svcScope: ss}, false, err
	}
	ss.svc = svc
	as := archivedService{svcScope: ss}
	if svc.ArchivedAt != nil {
		return as, false, nil
	}
	if inflight, err := q.GetInFlightRelease(ctx, ss.svc.ID); err == nil {
		return as, false, errf(apitypes.CodeDeployInProgress, "release %d of %s is deploying; wait for it to finish before archiving", inflight.Number, ss)
	} else if !store.IsNotFound(err) {
		return as, false, err
	}
	n := now()
	as.svc.ArchivedAt = &n
	if err := q.ArchiveService(ctx, store.ArchiveServiceParams{ArchivedAt: &n, ID: ss.svc.ID}); err != nil {
		return as, false, err
	}
	// Its domains stay, unrouted; the DNS records Dock made for them go.
	if err := q.SetServiceRecordsAbsent(ctx, store.SetServiceRecordsAbsentParams{UpdatedAt: n, ServiceID: ss.svc.ID}); err != nil {
		return as, false, err
	}
	active, err := q.GetActiveRelease(ctx, ss.svc.ID)
	switch {
	case store.IsNotFound(err):
		return as, true, nil
	case err != nil:
		return as, false, err
	}
	if err := q.SupersedeRelease(ctx, active.ID); err != nil {
		return as, false, err
	}
	active.Status = string(apitypes.ReleaseSuperseded)
	as.active = &active
	return as, true, nil
}

// finishArchive runs after archiveTx committed: routes are removed first,
// so no traffic goes to a container that is stopping, then the containers
// are stopped and removed in the background (up to stopTimeout each),
// reported as release events. A release keeps its host port until its
// container is gone; if the daemon stops first, recovery removes it.
//
// On a server that can't be reached nothing is tried: its releases are
// superseded already, so the reconnect procedure removes their containers
// and applies its routes (sweepRetired, reconnected). A revoked server
// only gets there if it joins again. The notes it returns say so, one per
// such server with containers to stop, for the archive's answer. stopped
// is done when the background stops are.
func (c *Core) finishArchive(ctx context.Context, done []archivedService) (notes []string, stopped *sync.WaitGroup) {
	stopped = &sync.WaitGroup{}
	c.kickCloudflare() // archiveTx gave up their DNS records
	c.kickUptime()     // their checks stop
	links := map[int64]serverLink{}
	stopping := map[int64]int{} // containers to stop, by server
	for _, as := range done {
		id := as.env.ServerID
		if _, ok := links[id]; !ok {
			links[id] = c.linkOf(ctx, System, id)
		}
		if as.active != nil {
			stopping[id]++
		}
	}
	for _, id := range slices.Sorted(maps.Keys(links)) {
		switch link := links[id]; {
		case link.status == apitypes.ServerOnline:
			if err := c.applyRoutes(ctx, id); err != nil {
				c.log.Error("apply routes after archive", "server", id, "err", err)
			}
		case stopping[id] == 0:
		case link.status == apitypes.ServerRevoked:
			notes = append(notes, fmt.Sprintf("containers on a revoked server were left running (%d); a platform operator can remove them on that machine, or join it again and they are removed", stopping[id]))
		default:
			notes = append(notes, fmt.Sprintf("the server is %s: its containers (%d) are stopped when it reconnects", link.status, stopping[id]))
		}
	}
	for _, as := range done {
		c.setStatus(as.svcScope, apitypes.ServiceStatus{State: apitypes.ServiceNone})
		// Its domains stay, unrouted, so their alerts are found by hostname.
		var hostnames []string
		if ds, err := c.store.ListDomainsByService(ctx, as.svc.ID); err != nil {
			c.log.Error("list domains after archive", "service", as.svc.ID, "err", err)
		} else {
			for _, d := range ds {
				hostnames = append(hostnames, d.Hostname)
			}
		}
		c.resolveServiceAlerts(ctx, as.svc.ID, hostnames)
		if as.active == nil {
			continue
		}
		c.publishRelease(ctx, as.svcScope, *as.active)
		if link := links[as.env.ServerID]; link.status != apitypes.ServerOnline {
			msg := fmt.Sprintf("Release %d stops when its server reconnects: %s was archived", as.active.Number, as.svc.Name)
			if link.status == apitypes.ServerRevoked {
				msg = fmt.Sprintf("Release %d was left running on a revoked server: %s was archived; a platform operator can remove its container there", as.active.Number, as.svc.Name)
			}
			c.releaseEvent(ctx, as.svcScope, *as.active, msg)
			continue
		}
		c.wg.Add(1)
		stopped.Add(1)
		go func() {
			defer c.wg.Done()
			defer stopped.Done()
			ctx, ss, rel := c.ctx, as.svcScope, *as.active
			c.releaseEvent(ctx, ss, rel, fmt.Sprintf("Stopping release %d: %s was archived", rel.Number, ss.svc.Name))
			if err := c.retire(ctx, ss, rel.ID); err != nil {
				if ctx.Err() == nil {
					c.releaseEvent(ctx, ss, rel, fmt.Sprintf("Could not stop release %d (it keeps its port until it is removed): %v", rel.Number, err))
				}
				return
			}
			c.releaseEvent(ctx, ss, rel, fmt.Sprintf("Stopped release %d; volumes are kept", rel.Number))
		}()
	}
	return notes, stopped
}

// ListVolumes returns a service's volumes, including staged ones.
func (c *Core) ListVolumes(ctx context.Context, a Actor, ref ServiceRef) ([]apitypes.Volume, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "listing volumes")
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListVolumes(ctx, ss.svc.ID)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.Volume, 0, len(rows))
	for _, v := range rows {
		out = append(out, apitypes.Volume{
			Name: v.Name, Service: ss.svc.Name, MountPath: v.MountPath, DockerName: v.DockerName,
			Pending: v.Pending, CreatedAt: v.CreatedAt,
		})
	}
	return out, nil
}

// AddVolume stages a new volume for a service.
func (c *Core) AddVolume(ctx context.Context, a Actor, ref ServiceRef, req apitypes.AddVolumeRequest) (apitypes.ChangeResult, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "adding a volume")
	if err != nil {
		return apitypes.ChangeResult{}, err
	}
	if err := validName("volume", req.Name); err != nil {
		return apitypes.ChangeResult{}, err
	}
	if !path.IsAbs(req.MountPath) || path.Clean(req.MountPath) == "/" {
		return apitypes.ChangeResult{}, invalid("mount path must be an absolute path other than /")
	}
	_, err = c.store.CreateVolume(ctx, store.CreateVolumeParams{
		ServiceID: ss.svc.ID, Name: req.Name, MountPath: path.Clean(req.MountPath), Pending: true,
		DockerName: volumeName(ss, req.Name),
	})
	if store.IsUnique(err) {
		return apitypes.ChangeResult{}, conflict("service %s already has a volume named %s or mounted at %s", ss, req.Name, req.MountPath)
	}
	if err != nil {
		return apitypes.ChangeResult{}, err
	}
	c.audit(ctx, a, "volume.add", &ss.project, &ss.env, ss.String(), fmt.Sprintf("staged volume %s at %s", req.Name, req.MountPath))
	return c.changeResult(ctx, a, ss.envScope, req.Apply)
}

// toService loads one service as a sees it (serviceOf).
func (c *Core) toService(ctx context.Context, a Actor, ss svcScope, pending apitypes.PendingChanges) (apitypes.Service, error) {
	var active *int64
	if r, err := c.store.GetActiveRelease(ctx, ss.svc.ID); err == nil {
		active = &r.Number
	} else if !store.IsNotFound(err) {
		return apitypes.Service{}, err
	}
	vols, err := c.store.ListVolumes(ctx, ss.svc.ID)
	if err != nil {
		return apitypes.Service{}, err
	}
	stateful := slices.ContainsFunc(vols, func(v store.Volume) bool { return !v.Pending })
	return c.serviceOf(ss, c.linkOf(ctx, a, ss.env.ServerID), pending, active, stateful), nil
}

// serviceOf builds a service from loaded state: its server's link, its
// active release number (nil if none) and whether it has an applied
// volume. toService loads that for one service; Overview loads it for a
// whole project at once.
func (c *Core) serviceOf(ss svcScope, link serverLink, pending apitypes.PendingChanges, active *int64, stateful bool) apitypes.Service {
	s := apitypes.Service{
		ID: ss.svc.ID, Project: ss.project.Name, Environment: ss.env.Name, Name: ss.svc.Name,
		ServiceSettings: settingsOf(ss.svc), Status: link.mark(c.getStatus(ss.svc.ID)),
		ActiveRelease: active, Stateful: stateful,
		Database: apitypes.DatabaseSetting(ss.svc.Database), DatabaseURLVariable: ss.svc.DatabaseUrlVariable,
		CreatedAt: ss.svc.CreatedAt, ArchivedAt: ss.svc.ArchivedAt, Staged: ss.svc.Staged,
		PendingChanges:  pending.CountFor(ss.svc.Name),
		EffectiveLimits: c.limitsOf(ss.svc.MemoryMb, ss.svc.Cpus),
	}
	s.DataEngine, _ = detectDatabase(ss.svc)
	s.Template = apitypes.Template(ss.svc.Template)
	if ss.svc.FromTemplate != "" {
		s.FromTemplate = &apitypes.FromTemplate{Name: ss.svc.FromTemplate, Revision: int(ss.svc.FromTemplateRevision)}
	}
	if ss.svc.StoppedAt != nil {
		// The database's word, also before a server's first reconcile.
		s.StoppedAt, s.StoppedBy = ss.svc.StoppedAt, ss.svc.StoppedBy
		s.Status.State = apitypes.ServiceStopped
		return s
	}
	if ss.env.AsleepAt != nil && active != nil {
		// Asleep (sleep.go), also before the first reconcile; waking
		// services read running as each comes back.
		if st := s.Status.State; st != apitypes.ServiceRunning || !c.isWaking(ss.env.ID) {
			s.Status.State = apitypes.ServiceAsleep
		}
		return s
	}
	if active != nil {
		if u, ok := c.stats.serviceUsage(ss.svc.ID); ok {
			s.Usage = &u
		}
	}
	return s
}

// limitsOf is a service's memory (MB) and CPU limits with the defaults
// releases get (releaseConfig) filled in.
func (c *Core) limitsOf(memoryMB int64, cpus float64) apitypes.EffectiveLimits {
	l := apitypes.EffectiveLimits{EffectiveMemoryMB: int(memoryMB), EffectiveCPUs: cpus}
	if l.EffectiveMemoryMB == 0 {
		l.EffectiveMemoryMB, l.MemoryFromDefault = c.cfg.Defaults.MemoryMB, true
	}
	if l.EffectiveCPUs == 0 {
		l.EffectiveCPUs, l.CPUsFromDefault = c.cfg.Defaults.CPUs, true
	}
	return l
}
