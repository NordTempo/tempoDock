package core

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/repoconfig"
	"github.com/NordTempo/tempoDock/internal/store"
)

// defaultPort is the container port (and PORT) for services without one.
const defaultPort = 8080

// releaseConfig is a release's fully resolved container config. It is
// stored encrypted (it holds variable values) so a release can be started
// again exactly as it was.
type releaseConfig struct {
	Env map[string]string `json:"env"`
	Cmd string            `json:"cmd,omitempty"`
	// Port is the container port; Published means it is bound to a fixed
	// loopback host port (the release row's host_port) for routing.
	Port               int    `json:"port"`
	Published          bool   `json:"published"`
	HealthCheckPath    string `json:"health_check_path,omitempty"`
	HealthCheckTimeout int    `json:"health_check_timeout_seconds"`
	MemoryMB           int    `json:"memory_mb"`
	// HealthPort is the container port a template database is probed on
	// over its environment network (it publishes none).
	HealthPort int           `json:"health_port,omitempty"`
	CPUs       float64       `json:"cpus"`
	Mounts     []configMount `json:"mounts"`
	Aliases    []string      `json:"aliases"`
	// Repo holds the deploy settings that came from dock.toml, so config
	// releases built on this one keep them.
	Repo *repoDeploy `json:"repo,omitempty"`
}

// repoDeploy is the deploy section of dock.toml (ADR 0007). Set fields
// win over the service's DB settings.
type repoDeploy struct {
	StartCommand              string `json:"start_command,omitempty"`
	Port                      int    `json:"port,omitempty"`
	HealthCheckPath           string `json:"health_check_path,omitempty"`
	HealthCheckTimeoutSeconds int    `json:"health_check_timeout_seconds,omitempty"`
}

func repoDeployOf(rc *repoconfig.Config) *repoDeploy {
	if rc == nil || rc.Deploy == (repoconfig.Deploy{}) {
		return nil
	}
	return &repoDeploy{
		StartCommand: rc.Deploy.StartCommand, Port: rc.Deploy.Port, HealthCheckPath: rc.Deploy.HealthCheckPath,
		HealthCheckTimeoutSeconds: int(rc.Deploy.HealthCheckTimeout / time.Second),
	}
}

// apply overrides s with the file's set fields.
func (r *repoDeploy) apply(s *store.Service) {
	if r == nil {
		return
	}
	if r.StartCommand != "" {
		s.StartCommand = r.StartCommand
	}
	if r.Port != 0 {
		s.Port = int64(r.Port)
	}
	if r.HealthCheckPath != "" {
		s.HealthCheckPath = r.HealthCheckPath
	}
	if r.HealthCheckTimeoutSeconds != 0 {
		s.HealthCheckTimeoutSeconds = int64(r.HealthCheckTimeoutSeconds)
	}
}

// fields lists the settings the file set, by API field name.
func (r *repoDeploy) fields() []string {
	if r == nil {
		return []string{}
	}
	var out []string
	for name, set := range map[string]bool{
		"start_command": r.StartCommand != "", "port": r.Port != 0,
		"health_check_path": r.HealthCheckPath != "", "health_check_timeout_seconds": r.HealthCheckTimeoutSeconds != 0,
	} {
		if set {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

type configMount struct {
	Volume     string `json:"volume"`
	DockerName string `json:"docker_name"`
	Path       string `json:"path"`
}

func (c *Core) sealConfig(cfg releaseConfig) ([]byte, error) {
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return c.box.Seal(b)
}

func (c *Core) openConfig(sealed []byte) (releaseConfig, error) {
	var cfg releaseConfig
	b, err := c.box.Open(sealed)
	if err != nil {
		return cfg, err
	}
	return cfg, json.Unmarshal(b, &cfg)
}

// Docker resource names are built from DB IDs, never from user-chosen names:
// "a"/"b-c" and "a-b"/"c" would otherwise share a network. Underscores keep
// them apart from pre-ID names (project, environment and service names never
// contain one). Labels carry the same IDs for tracing a resource back.

// networkName is the environment's private network. Environments that had
// containers before ID-based naming keep their old network (migration 0002).
func networkName(es envScope) string {
	if es.env.Network != "" {
		return es.env.Network
	}
	return fmt.Sprintf("dock_p%d_e%d", es.project.ID, es.env.ID)
}

func containerName(ss svcScope, number int64) string {
	return fmt.Sprintf("dock_p%d_e%d_s%d_r%d", ss.project.ID, ss.env.ID, ss.svc.ID, number)
}

func buildTag(ss svcScope, number int64) string {
	return fmt.Sprintf("dock/p%d_e%d_s%d:%d", ss.project.ID, ss.env.ID, ss.svc.ID, number)
}

// volumeName is the Docker name of a new volume. It is stored with the
// volume, so volumes created under older naming keep their name and data.
func volumeName(ss svcScope, volume string) string {
	return fmt.Sprintf("dock_p%d_e%d_s%d_%s", ss.project.ID, ss.env.ID, ss.svc.ID, volume)
}

// resolveConfig builds the container config for a service from its live
// settings (overridden by dock.toml values in repo), volumes and
// variables, expanding references.
func (c *Core) resolveConfig(ctx context.Context, q *store.Queries, ss svcScope, repo *repoDeploy) (releaseConfig, error) {
	svcs, err := q.ListServices(ctx, ss.env.ID)
	if err != nil {
		return releaseConfig{}, err
	}
	var target store.Service
	for i := range svcs {
		if svcs[i].ID == ss.svc.ID {
			repo.apply(&svcs[i])
			target = svcs[i]
		}
	}
	ev, err := loadEnvVars(ctx, q, ss.envScope, svcs, nil)
	if err != nil {
		return releaseConfig{}, err
	}
	r, err := c.envResolver(ev, target.ID, false)
	if err != nil {
		return releaseConfig{}, err
	}
	targetDomains, err := q.ListDomainsByService(ctx, target.ID)
	if err != nil {
		return releaseConfig{}, err
	}

	cfg := releaseConfig{
		Env:                map[string]string{},
		Cmd:                target.StartCommand,
		Port:               servicePort(target),
		Published:          target.Port != 0 || len(targetDomains) > 0 || target.HealthCheckPath != "",
		Repo:               repo,
		HealthCheckPath:    target.HealthCheckPath,
		HealthCheckTimeout: int(target.HealthCheckTimeoutSeconds),
		MemoryMB:           int(target.MemoryMb),
		CPUs:               target.Cpus,
		Mounts:             []configMount{},
		Aliases:            []string{target.Name},
	}
	if cfg.HealthCheckTimeout == 0 {
		cfg.HealthCheckTimeout = c.cfg.Defaults.HealthCheckTimeout
	}
	if t, ok := templateOf(target); ok {
		cfg.HealthPort = t.port
	}
	if cfg.MemoryMB == 0 {
		cfg.MemoryMB = c.cfg.Defaults.MemoryMB
	}
	if cfg.CPUs == 0 {
		cfg.CPUs = c.cfg.Defaults.CPUs
	}
	for _, k := range slices.Sorted(maps.Keys(r.vars[target.Name])) {
		v, err := r.value(target.Name, k)
		if err != nil {
			return releaseConfig{}, err
		}
		cfg.Env[k] = v
	}
	vols, err := q.ListVolumes(ctx, target.ID)
	if err != nil {
		return releaseConfig{}, err
	}
	for _, v := range vols {
		if !v.Pending {
			cfg.Mounts = append(cfg.Mounts, configMount{Volume: v.Name, DockerName: v.DockerName, Path: v.MountPath})
		}
	}
	return cfg, nil
}

// envVars is what resolving an environment's variables reads, loaded once
// in a fixed number of queries and resolvable live or staged.
type envVars struct {
	es   envScope
	svcs []store.Service // by name
	vars []store.Variable
	// public is each service's public domain (DOCK_PUBLIC_DOMAIN): its
	// first domain that isn't a redirect.
	public map[int64]string
}

// loadEnvVars loads an environment's domains, and its services (by name)
// and variables unless they are given (nil: load them).
func loadEnvVars(ctx context.Context, q *store.Queries, es envScope, svcs []store.Service, vars []store.Variable) (envVars, error) {
	ev := envVars{es: es, svcs: svcs, vars: vars, public: map[int64]string{}}
	var err error
	if ev.svcs == nil {
		if ev.svcs, err = q.ListServices(ctx, es.env.ID); err != nil {
			return ev, err
		}
	}
	if ev.vars == nil {
		if ev.vars, err = q.ListVariables(ctx, es.env.ID); err != nil {
			return ev, err
		}
	}
	domains, err := q.ListEnvironmentDomains(ctx, es.env.ID)
	if err != nil {
		return ev, err
	}
	first := map[int64]int64{} // domain ID of each service's public domain
	for _, d := range domains {
		if id, ok := first[d.ServiceID]; d.RedirectTo == "" && (!ok || d.ID < id) {
			first[d.ServiceID], ev.public[d.ServiceID] = d.ID, d.Hostname
		}
	}
	return ev, nil
}

// envResolver sets up resolving variable references in an environment:
// the variables each service gets (shared, then its own, then injected
// ones). Archived services are left out, except include. With staged, the
// environment's staged variable and port changes count as applied, to
// preview what applying them would do.
func (c *Core) envResolver(ev envVars, include int64, staged bool) (*resolver, error) {
	es := ev.es
	shared := map[string]string{}
	own := map[int64]map[string]string{}
	for _, v := range ev.vars {
		sealed := v.Value
		if staged {
			switch v.Pending {
			case "set":
				sealed = v.PendingValue
			case "unset":
				continue
			}
		}
		val, err := c.openValue(sealed)
		if err != nil {
			return nil, err
		}
		if val == nil {
			continue // staged only
		}
		if v.ServiceID == nil {
			shared[v.Key] = *val
			continue
		}
		if own[*v.ServiceID] == nil {
			own[*v.ServiceID] = map[string]string{}
		}
		own[*v.ServiceID][v.Key] = *val
	}

	r := &resolver{env: es.String(), vars: map[string]map[string]string{sharedScope: shared}, done: map[string]string{}, visiting: map[string]bool{}}
	for _, s := range ev.svcs {
		if s.ArchivedAt != nil && s.ID != include {
			continue
		}
		if next, err := pendingSettings(s); staged && err == nil && next != nil {
			s.Port = int64(next.Port)
		}
		vars := maps.Clone(shared)
		maps.Copy(vars, own[s.ID])
		// Injected variables win over user ones.
		vars["PORT"] = strconv.Itoa(servicePort(s))
		vars["DOCK_PROJECT"] = es.project.Name
		vars["DOCK_ENVIRONMENT"] = es.env.Name
		vars["DOCK_SERVICE"] = s.Name
		vars["DOCK_PRIVATE_DOMAIN"] = s.Name
		if host, ok := ev.public[s.ID]; ok {
			vars["DOCK_PUBLIC_DOMAIN"] = host
		}
		r.vars[s.Name] = vars
	}
	return r, nil
}

// resolveEnvs loads and resolves the variables of every non-archived
// service in an environment (see resolveLoaded).
func (c *Core) resolveEnvs(ctx context.Context, q *store.Queries, es envScope, staged bool) (envs map[int64]map[string]string, broken map[int64]error, err error) {
	ev, err := loadEnvVars(ctx, q, es, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	return c.resolveLoaded(ev, staged)
}

// resolveLoaded resolves the variables of every non-archived service in
// ev, by service ID (see envResolver for staged). A service whose
// references don't resolve is in broken instead.
func (c *Core) resolveLoaded(ev envVars, staged bool) (envs map[int64]map[string]string, broken map[int64]error, err error) {
	r, err := c.envResolver(ev, 0, staged)
	if err != nil {
		return nil, nil, err
	}
	envs, broken = map[int64]map[string]string{}, map[int64]error{}
	for _, s := range ev.svcs {
		if s.ArchivedAt != nil {
			continue
		}
		env := map[string]string{}
		for _, k := range slices.Sorted(maps.Keys(r.vars[s.Name])) {
			v, err := r.value(s.Name, k)
			if err != nil {
				broken[s.ID] = err
				env = nil
				break
			}
			env[k] = v
		}
		envs[s.ID] = env
	}
	return envs, broken, nil
}

// servicePort is the container port a service listens on (and PORT): its
// port setting, else its template's, else defaultPort.
func servicePort(s store.Service) int {
	if s.Port != 0 {
		return int(s.Port)
	}
	if t, ok := templateOf(s); ok {
		return t.port
	}
	return defaultPort
}

// allocateHostPort picks the lowest free port in the configured range among
// releases that are deploying or serving on the server.
func (c *Core) allocateHostPort(ctx context.Context, q *store.Queries, serverID int64) (int64, error) {
	used, err := q.ListUsedHostPorts(ctx, serverID)
	if err != nil {
		return 0, err
	}
	taken := map[int64]bool{}
	for _, p := range used {
		taken[p] = true
	}
	for p := int64(c.cfg.HostPorts.Min); p <= int64(c.cfg.HostPorts.Max); p++ {
		if !taken[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free host port in %d-%d", c.cfg.HostPorts.Min, c.cfg.HostPorts.Max)
}

// releaseSpec says how to create a release.
type releaseSpec struct {
	source apitypes.ReleaseSource
	// from is copied (image and resolved config) for rollback and restart.
	from *store.Release
	// imageRef is the image for image deploys (empty: the service setting).
	imageRef string
	// upload is the source tarball path for upload deploys, and repo the
	// dock.toml found in it (nil if none).
	upload string
	repo   *repoconfig.Config
	// carried are staged changes applied for this release (by applyEnv);
	// upload and image deploys add the service's own when they apply them.
	carried carried
	// promote is set for promotions (Promote).
	promote *promoteStep
	// commit is the pushed commit an upload was fetched from (ADR 0018).
	// Releases that copy an image copy its commit instead.
	commit *apitypes.Commit
	// done, if set, is called when the release finished (ok: it went
	// active), from the deploy goroutine.
	done func(ok bool)
}

var errNoActiveRelease = errors.New("service has no active release")

// startRelease creates a queued release and starts deploying it in the
// background. The one-deploy-per-service rule is enforced here (and by a
// unique index).
func (c *Core) startRelease(ctx context.Context, a Actor, ss svcScope, spec releaseSpec) (store.Release, error) {
	if ss.svc.ArchivedAt != nil {
		return store.Release{}, invalid("service %s is archived", ss)
	}
	if c.ctx.Err() != nil {
		return store.Release{}, errf(apitypes.CodeConflict, "the daemon is shutting down; try again in a moment")
	}
	// Deploys to a server that can't be reached are refused before a
	// release exists; nothing queues for later.
	if _, err := c.runtimeFor(ss.env.ServerID); err != nil {
		return store.Release{}, err
	}
	var rel store.Release
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		if r, err := q.GetInFlightRelease(ctx, ss.svc.ID); err == nil {
			return deployInProgress(ss, r)
		}
		// Re-checked in the transaction: ArchiveService may have won a race.
		svc, err := q.GetService(ctx, ss.svc.ID)
		if err != nil {
			return err
		}
		if svc.ArchivedAt != nil {
			return invalid("service %s is archived", ss)
		}
		ss.svc = svc
		ch := spec.carried
		if spec.source == apitypes.SourceUpload || spec.source == apitypes.SourceImage {
			// A deploy takes the service's own staged changes with it.
			res, err := applyPending(ctx, q, ss.env.ID, &ss.svc.ID)
			if err != nil {
				return err
			}
			if own := res.own[ss.svc.ID]; own != nil {
				ch.Variables = append(slices.Clone(ch.Variables), own.Variables...)
				ch.Volumes = append(slices.Clone(ch.Volumes), own.Volumes...)
				if own.Settings != nil {
					ch.Settings = own.Settings
				}
			}
			if ss.svc, err = q.GetService(ctx, ss.svc.ID); err != nil {
				return err
			}
		}
		carriedJSON, err := ch.encode()
		if err != nil {
			return err
		}
		number, err := q.NextReleaseNumber(ctx, ss.svc.ID)
		if err != nil {
			return err
		}
		params := store.CreateReleaseParams{ServiceID: ss.svc.ID, Number: number, Source: string(spec.source), CreatedBy: a.Name, Carried: carriedJSON}
		var repo *repoDeploy
		commit := spec.commit
		switch spec.source {
		case apitypes.SourceImage:
			params.ImageRef = spec.imageRef
			if params.ImageRef == "" {
				params.ImageRef = ss.svc.Image
			}
			if params.ImageRef == "" {
				return invalid("no image given and service %s has no image setting", ss)
			}
			if err := validateImageRef(params.ImageRef, c.registryHost()); err != nil {
				return err
			}
			// The image setting follows image deploys, so deploying the
			// setting again (the UI's Deploy) redeploys this image.
			if ss.svc, err = setServiceImage(ctx, q, ss.svc, params.ImageRef); err != nil {
				return err
			}
		case apitypes.SourceUpload:
			params.ImageRef = buildTag(ss, number)
			repo = repoDeployOf(spec.repo)
			// A source deploy makes it a source service again.
			if ss.svc, err = setServiceImage(ctx, q, ss.svc, ""); err != nil {
				return err
			}
		case apitypes.SourceConfig:
			active, err := q.GetActiveRelease(ctx, ss.svc.ID)
			if store.IsNotFound(err) {
				return errNoActiveRelease
			}
			if err != nil {
				return err
			}
			params.ImageRef, params.ImageID = active.ImageRef, active.ImageID
			params.ImageDigest, params.BuiltOnServerID = active.ImageDigest, active.BuiltOnServerID
			activeCfg, err := c.openConfig(active.Config)
			if err != nil {
				return err
			}
			repo = activeCfg.Repo // same code, same dock.toml
			commit = commitOf(active)
		case apitypes.SourcePromote:
			p := spec.promote
			params.ImageRef, params.ImageID = p.imageRef, p.imageID
			params.ImageDigest, params.BuiltOnServerID = p.from.ImageDigest, p.from.BuiltOnServerID
			fromCfg, err := c.openConfig(p.from.Config)
			if err != nil {
				return err
			}
			repo = fromCfg.Repo // same code, same dock.toml
			commit = commitOf(p.from)
		}
		var cfg releaseConfig
		if spec.from != nil {
			commit = commitOf(*spec.from)
			params.FromRelease = spec.from.Number
			params.ImageRef, params.ImageID = spec.from.ImageRef, spec.from.ImageID
			params.ImageDigest, params.BuiltOnServerID = spec.from.ImageDigest, spec.from.BuiltOnServerID
			params.Config = spec.from.Config
			if cfg, err = c.openConfig(spec.from.Config); err != nil {
				return err
			}
		} else {
			if cfg, err = c.resolveConfig(ctx, q, ss, repo); err != nil {
				return err
			}
			if params.Config, err = c.sealConfig(cfg); err != nil {
				return err
			}
		}
		if cfg.Published {
			if params.HostPort, err = c.allocateHostPort(ctx, q, ss.env.ServerID); err != nil {
				return err
			}
		}
		if spec.source == apitypes.SourcePromote {
			rel, err = q.CreatePromotedRelease(ctx, store.CreatePromotedReleaseParams{
				ServiceID: params.ServiceID, Number: params.Number, ImageRef: params.ImageRef, ImageID: params.ImageID,
				ImageDigest: params.ImageDigest, BuiltOnServerID: params.BuiltOnServerID, PromotedFromReleaseID: spec.promote.from.ID,
				Config: params.Config, HostPort: params.HostPort, CreatedBy: params.CreatedBy, Carried: params.Carried,
			})
		} else {
			rel, err = q.CreateRelease(ctx, params)
		}
		if err != nil || commit == nil {
			return err
		}
		rel.CommitSha, rel.CommitMessage, rel.CommitAuthor = commit.SHA, commit.Message, commit.Author
		return q.SetReleaseCommit(ctx, store.SetReleaseCommitParams{
			CommitSha: commit.SHA, CommitMessage: commit.Message, CommitAuthor: commit.Author, ID: rel.ID,
		})
	})
	if store.IsUnique(err) {
		return rel, errf(apitypes.CodeDeployInProgress, "another deploy of %s just started; wait for it to finish", ss)
	}
	if err != nil {
		return rel, err
	}
	c.releaseEvent(ctx, ss, rel, fmt.Sprintf("Release %d queued by %s (%s)", rel.Number, a.Name, rel.Source))
	c.publishRelease(ctx, ss, rel)
	c.wg.Add(1)
	go c.deploy(ss, rel, spec)
	return rel, nil
}

// setServiceImage changes a service's live image setting, if it differs.
func setServiceImage(ctx context.Context, q *store.Queries, s store.Service, image string) (store.Service, error) {
	if s.Image == image {
		return s, nil
	}
	return q.UpdateService(ctx, store.UpdateServiceParams{
		Image: image, Port: s.Port, HealthCheckPath: s.HealthCheckPath, HealthCheckTimeoutSeconds: s.HealthCheckTimeoutSeconds,
		StartCommand: s.StartCommand, DockerfilePath: s.DockerfilePath, BuildCommand: s.BuildCommand,
		MemoryMb: s.MemoryMb, Cpus: s.Cpus, ID: s.ID,
	})
}

func deployInProgress(ss svcScope, r store.Release) error {
	return errf(apitypes.CodeDeployInProgress, "release %d of %s is already deploying (%s); wait for it to finish", r.Number, ss, r.Status)
}

// DeployImage deploys a prebuilt image (default: the service's image
// setting), taking the service's staged changes with it.
func (c *Core) DeployImage(ctx context.Context, a Actor, ref ServiceRef, req apitypes.DeployImageRequest) (apitypes.Release, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "deploying")
	if err != nil {
		return apitypes.Release{}, err
	}
	rel, err := c.startRelease(ctx, a, ss, releaseSpec{source: apitypes.SourceImage, imageRef: req.Image})
	if err != nil {
		return apitypes.Release{}, err
	}
	c.audit(ctx, a, "release.deploy", &ss.project, &ss.env, ss.String(), fmt.Sprintf("release %d from image %s", rel.Number, rel.ImageRef))
	return c.toRelease(ctx, ss, rel, false)
}

// DeploySource streams a source tarball (tar or tar.gz) to disk, then builds
// and deploys it. Uploads larger than maxBytes fail with CodeTooLarge.
func (c *Core) DeploySource(ctx context.Context, a Actor, ref ServiceRef, body io.Reader, maxBytes int64) (apitypes.Release, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "deploying")
	if err != nil {
		return apitypes.Release{}, err
	}
	rel, err := c.deploySource(ctx, a, ss, func() (io.ReadCloser, error) { return io.NopCloser(body), nil }, maxBytes, nil)
	if err != nil {
		return apitypes.Release{}, err
	}
	c.audit(ctx, a, "release.deploy", &ss.project, &ss.env, ss.String(), fmt.Sprintf("release %d from source upload", rel.Number))
	return c.toRelease(ctx, ss, rel, false)
}

// deploySource saves the source that open returns and starts its release.
// open runs once the checks pass and the service's upload slot is held,
// so nothing is received (or fetched) for a deploy that would be refused.
// commit is the pushed commit the source is (nil for uploads).
func (c *Core) deploySource(ctx context.Context, a Actor, ss svcScope, open func() (io.ReadCloser, error), maxBytes int64, commit *apitypes.Commit) (store.Release, error) {
	// Fail fast, before receiving a large upload.
	if ss.svc.ArchivedAt != nil {
		return store.Release{}, invalid("service %s is archived", ss)
	}
	srv, err := c.store.GetServer(ctx, ss.env.ServerID)
	if err != nil {
		return store.Release{}, err
	}
	if _, err := c.runtimeFor(srv.ID); err != nil {
		return store.Release{}, err
	}
	if _, _, err := c.builderFor(ctx, srv, apitypes.EnvironmentKind(ss.env.Kind)); err != nil {
		return store.Release{}, err
	}
	done, err := c.beginUpload(ss)
	if err != nil {
		return store.Release{}, err
	}
	// Held until the release exists, which then blocks further deploys.
	defer done()
	if r, err := c.store.GetInFlightRelease(ctx, ss.svc.ID); err == nil {
		return store.Release{}, deployInProgress(ss, r)
	}
	body, err := open()
	if err != nil {
		return store.Release{}, err
	}
	path, repo, err := c.saveUpload(body, maxBytes)
	_ = body.Close()
	if err != nil {
		return store.Release{}, err
	}
	rel, err := c.startRelease(ctx, a, ss, releaseSpec{source: apitypes.SourceUpload, upload: path, repo: repo, commit: commit})
	if err != nil {
		_ = os.Remove(path)
		return store.Release{}, err
	}
	return rel, nil
}

// maxConcurrentUploads caps source uploads streaming to disk at once on
// this server, whatever the service.
const maxConcurrentUploads = 2

// beginUpload claims the right to stream a source upload for a service:
// one per service, and at most maxConcurrentUploads per server, so parallel
// uploads can't fill the disk before the in-flight check sees them. Call
// done when the upload is saved and its release created (or it failed).
func (c *Core) beginUpload(ss svcScope) (done func(), err error) {
	c.uploadsMu.Lock()
	defer c.uploadsMu.Unlock()
	switch {
	case c.uploads[ss.svc.ID]:
		return nil, errf(apitypes.CodeDeployInProgress, "a source upload for %s is already in progress; wait for it to finish", ss)
	case len(c.uploads) >= maxConcurrentUploads:
		return nil, errf(apitypes.CodeConflict, "%d source uploads are already in progress on this server; try again in a moment", len(c.uploads))
	}
	c.uploads[ss.svc.ID] = true
	return func() {
		c.uploadsMu.Lock()
		delete(c.uploads, ss.svc.ID)
		c.uploadsMu.Unlock()
	}, nil
}

// saveUpload streams a source upload to disk and returns its path and
// dock.toml (nil if it has none). dock.toml is read from the bytes on their
// way to the file, so the archive is never read (and decompressed) again.
func (c *Core) saveUpload(body io.Reader, maxBytes int64) (string, *repoconfig.Config, error) {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	path := filepath.Join(c.cfg.DataDir, "uploads", hex.EncodeToString(b)+".tar")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", nil, err
	}
	in := &uploadReader{r: io.LimitReader(body, maxBytes+1)}
	out := &uploadWriter{w: f}
	rc, found, rcErr := repoconfig.FromTar(io.TeeReader(in, out))
	if in.err == nil && out.err == nil {
		// The rest of the archive, after dock.toml.
		_, err = io.Copy(out, in)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = cmp.Or(in.err, out.err)
	}
	fail := func(err error) (string, *repoconfig.Config, error) {
		_ = os.Remove(path)
		return "", nil, err
	}
	switch {
	case err != nil:
		return fail(fmt.Errorf("receive upload: %w", err))
	case in.n > maxBytes:
		return fail(errf(apitypes.CodeTooLarge, "upload is larger than the server's limit of %d MB; add large files to .dockerignore or deploy a prebuilt image", maxBytes>>20))
	case in.n == 0:
		return fail(invalid("upload is empty"))
	case errors.Is(rcErr, repoconfig.ErrNotArchive):
		return fail(invalid("upload is not a tar or tar.gz archive"))
	case rcErr != nil:
		return fail(invalid("%s: %v", repoconfig.FileName, rcErr))
	case !found:
		return path, nil, nil
	}
	return path, &rc, nil
}

// uploadReader counts what it reads and keeps the first read error
// (io.EOF is not one), so a broken upload isn't reported as a bad archive.
type uploadReader struct {
	r   io.Reader
	n   int64
	err error
}

func (u *uploadReader) Read(p []byte) (int, error) {
	n, err := u.r.Read(p)
	u.n += int64(n)
	if err != nil && !errors.Is(err, io.EOF) && u.err == nil {
		u.err = err
	}
	return n, err
}

// uploadWriter keeps the first write error, for the same reason.
type uploadWriter struct {
	w   io.Writer
	err error
}

func (u *uploadWriter) Write(p []byte) (int, error) {
	n, err := u.w.Write(p)
	if err != nil && u.err == nil {
		u.err = err
	}
	return n, err
}

// Rollback creates a new release from an earlier release's image and
// resolved config, through the same health-checked path.
func (c *Core) Rollback(ctx context.Context, a Actor, ref ServiceRef, number int64) (apitypes.Release, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "rolling back")
	if err != nil {
		return apitypes.Release{}, err
	}
	from, err := c.releaseByNumber(ctx, ss, number)
	if err != nil {
		return apitypes.Release{}, err
	}
	if from.Status != string(apitypes.ReleaseSuperseded) && from.Status != string(apitypes.ReleaseActive) {
		return apitypes.Release{}, invalid("release %d never went live (status %s); roll back to a release that served traffic", number, from.Status)
	}
	rel, err := c.startRelease(ctx, a, ss, releaseSpec{source: apitypes.SourceRollback, from: &from})
	if err != nil {
		return apitypes.Release{}, err
	}
	c.audit(ctx, a, "release.rollback", &ss.project, &ss.env, ss.String(), fmt.Sprintf("release %d rolls back to release %d", rel.Number, number))
	return c.toRelease(ctx, ss, rel, false)
}

// Restart creates a new release identical to the active one.
func (c *Core) Restart(ctx context.Context, a Actor, ref ServiceRef) (apitypes.Release, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "restarting")
	if err != nil {
		return apitypes.Release{}, err
	}
	active, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
	if store.IsNotFound(err) {
		return apitypes.Release{}, invalid("service %s has no active release to restart; deploy it first", ss)
	}
	if err != nil {
		return apitypes.Release{}, err
	}
	rel, err := c.startRelease(ctx, a, ss, releaseSpec{source: apitypes.SourceRestart, from: &active})
	if err != nil {
		return apitypes.Release{}, err
	}
	c.audit(ctx, a, "release.restart", &ss.project, &ss.env, ss.String(), fmt.Sprintf("release %d restarts release %d", rel.Number, active.Number))
	return c.toRelease(ctx, ss, rel, false)
}

// ListReleases returns a service's releases, newest first.
func (c *Core) ListReleases(ctx context.Context, a Actor, ref ServiceRef, limit int) ([]apitypes.Release, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "listing releases")
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := c.store.ListReleases(ctx, store.ListReleasesParams{ServiceID: ss.svc.ID, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.Release, 0, len(rows))
	for _, r := range rows {
		rel, err := c.toRelease(ctx, ss, r, false)
		if err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	return out, nil
}

// GetRelease returns one release with its progress events.
func (c *Core) GetRelease(ctx context.Context, a Actor, ref ServiceRef, number int64) (apitypes.Release, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading releases")
	if err != nil {
		return apitypes.Release{}, err
	}
	r, err := c.releaseByNumber(ctx, ss, number)
	if err != nil {
		return apitypes.Release{}, err
	}
	return c.toRelease(ctx, ss, r, true)
}

func (c *Core) releaseByNumber(ctx context.Context, ss svcScope, number int64) (store.Release, error) {
	r, err := c.store.GetReleaseByNumber(ctx, store.GetReleaseByNumberParams{ServiceID: ss.svc.ID, Number: number})
	if store.IsNotFound(err) {
		return r, notFound("release %d of %s not found", number, ss)
	}
	return r, err
}

func (c *Core) toRelease(ctx context.Context, ss svcScope, r store.Release, withEvents bool) (apitypes.Release, error) {
	out := apitypes.Release{
		ID: r.ID, Number: r.Number, Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name,
		Status: apitypes.ReleaseStatus(r.Status), Source: apitypes.ReleaseSource(r.Source), FromRelease: r.FromRelease,
		ImageRef: r.ImageRef, ImageID: r.ImageID, ImageDigest: r.ImageDigest, Error: r.Error, ErrorCode: apitypes.ErrorCode(r.ErrorCode),
		CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt, Commit: commitOf(r),
	}
	cfg, err := c.openConfig(r.Config)
	if err != nil {
		return out, fmt.Errorf("release %d config: %w", r.ID, err)
	}
	out.Config = apitypes.ReleaseConfig{
		Port: cfg.Port, Published: cfg.Published, StartCommand: cfg.Cmd, HealthCheckPath: cfg.HealthCheckPath,
		MemoryMB: cfg.MemoryMB, CPUs: cfg.CPUs, VariableKeys: slices.Sorted(maps.Keys(cfg.Env)),
		Mounts: make([]apitypes.VolumeMount, 0, len(cfg.Mounts)), FromRepoFile: cfg.Repo.fields(),
	}
	for _, m := range cfg.Mounts {
		out.Config.Mounts = append(out.Config.Mounts, apitypes.VolumeMount{Volume: m.Volume, Path: m.Path})
	}
	if withEvents {
		evs, err := c.store.ListReleaseEvents(ctx, r.ID)
		if err != nil {
			return out, err
		}
		out.Events = make([]apitypes.ReleaseEvent, 0, len(evs))
		for _, e := range evs {
			out.Events = append(out.Events, apitypes.ReleaseEvent{Seq: e.ID, ReleaseNumber: r.Number, Message: e.Message, CreatedAt: e.CreatedAt})
		}
	}
	return out, nil
}

func summarize(r store.Release) *apitypes.ReleaseSummary {
	return &apitypes.ReleaseSummary{
		Number: r.Number, Status: apitypes.ReleaseStatus(r.Status), Source: apitypes.ReleaseSource(r.Source),
		ImageRef: r.ImageRef, ImageID: r.ImageID, Error: r.Error, ErrorCode: apitypes.ErrorCode(r.ErrorCode),
		CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, Commit: commitOf(r),
	}
}

// releaseEvent records a progress line and publishes it.
func (c *Core) releaseEvent(ctx context.Context, ss svcScope, r store.Release, msg string) {
	row, err := c.store.AddReleaseEvent(ctx, store.AddReleaseEventParams{ReleaseID: r.ID, Message: msg, CreatedAt: now()})
	if err != nil {
		c.log.Error("write release event", "release", r.ID, "err", err)
		return
	}
	c.log.Info(msg, "service", ss.String())
	c.publish(ss, apitypes.Event{
		Type:         apitypes.EventReleaseEvent,
		ReleaseEvent: &apitypes.ReleaseEvent{Seq: row.ID, ReleaseNumber: r.Number, Message: msg, CreatedAt: row.CreatedAt},
	})
}

func (c *Core) publishRelease(ctx context.Context, ss svcScope, r store.Release) {
	rel, err := c.toRelease(ctx, ss, r, false)
	if err != nil {
		c.log.Error("publish release", "release", r.ID, "err", err)
		return
	}
	c.publish(ss, apitypes.Event{Type: apitypes.EventRelease, Release: &rel})
}

// publish sends a service-scoped event.
func (c *Core) publish(ss svcScope, ev apitypes.Event) {
	ev.Project, ev.Environment, ev.Service = ss.project.Name, ss.env.Name, ss.svc.Name
	c.bus.publish(busEvent{projectID: ss.project.ID, envID: ss.env.ID, serviceID: ss.svc.ID, ev: ev})
}
