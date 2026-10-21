package core

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/NordTempo/platform/internal/apitypes"
	"github.com/NordTempo/platform/internal/store"
)

func settingsOf(s store.Service) apitypes.ServiceSettings {
	return apitypes.ServiceSettings{
		Image: s.Image, Port: int(s.Port), HealthCheckPath: s.HealthCheckPath,
		HealthCheckTimeoutSeconds: int(s.HealthCheckTimeoutSeconds), StartCommand: s.StartCommand,
		DockerfilePath: s.DockerfilePath, BuildCommand: s.BuildCommand, MemoryMB: int(s.MemoryMb), CPUs: s.Cpus,
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

func validateSettings(s apitypes.ServiceSettings) error {
	switch {
	case s.Port < 0 || s.Port > 65535:
		return invalid("port must be between 0 and 65535")
	case s.HealthCheckPath != "" && !strings.HasPrefix(s.HealthCheckPath, "/"):
		return invalid("health check path must start with /")
	case s.HealthCheckTimeoutSeconds < 0 || s.MemoryMB < 0 || s.CPUs < 0:
		return invalid("timeouts and limits can't be negative")
	}
	return nil
}

// ListServices returns an environment's services, including archived ones.
func (c *Core) ListServices(ctx context.Context, a Actor, ref EnvRef) ([]apitypes.Service, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeRead, "listing services")
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListServices(ctx, es.env.ID)
	if err != nil {
		return nil, err
	}
	pending, err := c.pendingChanges(ctx, c.store.Queries, es, false)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.Service, 0, len(rows))
	for _, s := range rows {
		svc, err := c.toService(ctx, svcScope{es, s}, pending)
		if err != nil {
			return nil, err
		}
		out = append(out, svc)
	}
	return out, nil
}

// GetService returns one service.
func (c *Core) GetService(ctx context.Context, a Actor, ref ServiceRef) (apitypes.Service, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading a service")
	if err != nil {
		return apitypes.Service{}, err
	}
	pending, err := c.pendingChanges(ctx, c.store.Queries, ss.envScope, false)
	if err != nil {
		return apitypes.Service{}, err
	}
	return c.toService(ctx, ss, pending)
}

// CreateService creates a service. Its settings are live immediately (there
// is nothing running yet); nothing is deployed.
func (c *Core) CreateService(ctx context.Context, a Actor, ref EnvRef, req apitypes.CreateServiceRequest) (apitypes.Service, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeDeploy, "creating a service")
	if err != nil {
		return apitypes.Service{}, err
	}
	if err := validName("service", req.Name); err != nil {
		return apitypes.Service{}, err
	}
	if req.Name == "shared" {
		return apitypes.Service{}, invalid(`"shared" is reserved for environment variable references`)
	}
	set := apitypes.ServiceSettings{
		Image: req.Image, Port: req.Port, HealthCheckPath: req.HealthCheckPath,
		HealthCheckTimeoutSeconds: req.HealthCheckTimeoutSeconds, StartCommand: req.StartCommand,
		DockerfilePath: req.DockerfilePath, BuildCommand: req.BuildCommand, MemoryMB: req.MemoryMB, CPUs: req.CPUs,
	}
	if err := validateSettings(set); err != nil {
		return apitypes.Service{}, err
	}
	s, err := c.store.CreateService(ctx, store.CreateServiceParams{
		EnvironmentID: es.env.ID, Name: req.Name, Image: set.Image, Port: int64(set.Port),
		HealthCheckPath: set.HealthCheckPath, HealthCheckTimeoutSeconds: int64(set.HealthCheckTimeoutSeconds),
		StartCommand: set.StartCommand, DockerfilePath: set.DockerfilePath, BuildCommand: set.BuildCommand,
		MemoryMb: int64(set.MemoryMB), Cpus: set.CPUs,
	})
	if store.IsUnique(err) {
		return apitypes.Service{}, conflict("service %q already exists in %s", req.Name, es)
	}
	if err != nil {
		return apitypes.Service{}, err
	}
	c.audit(ctx, a, "service.create", &es.project, &es.env, es.String()+"/"+s.Name, "created service")
	return c.toService(ctx, svcScope{es, s}, apitypes.PendingChanges{})
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
	if err := validateSettings(next); err != nil {
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

// ArchiveService stops a service and removes its routes. Volumes and the
// database rows are kept; real deletion is a separate, later operation.
func (c *Core) ArchiveService(ctx context.Context, a Actor, ref ServiceRef) (apitypes.Service, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeAdmin, "archiving a service")
	if err != nil {
		return apitypes.Service{}, err
	}
	if ss.svc.ArchivedAt != nil {
		return c.toService(ctx, ss, apitypes.PendingChanges{})
	}
	if inflight, err := c.store.GetInFlightRelease(ctx, ss.svc.ID); err == nil {
		return apitypes.Service{}, errf(apitypes.CodeDeployInProgress, "release %d of %s is deploying; wait for it to finish before archiving", inflight.Number, ss)
	}
	n := now()
	if err := c.store.ArchiveService(ctx, store.ArchiveServiceParams{ArchivedAt: &n, ID: ss.svc.ID}); err != nil {
		return apitypes.Service{}, err
	}
	ss.svc.ArchivedAt = &n
	// Routes first, so no traffic goes to a container that is stopping.
	if err := c.applyRoutes(ctx, ss.env.ServerID); err != nil {
		c.log.Error("apply routes after archive", "service", ss.String(), "err", err)
	}
	if active, err := c.store.GetActiveRelease(ctx, ss.svc.ID); err == nil {
		if active.ContainerID != "" {
			c.removeContainer(ctx, ss.env.ServerID, active.ContainerID)
		}
		if err := c.store.SupersedeRelease(ctx, active.ID); err != nil {
			return apitypes.Service{}, err
		}
	}
	c.setStatus(ss, apitypes.ServiceStatus{State: apitypes.ServiceNone})
	c.audit(ctx, a, "service.archive", &ss.project, &ss.env, ss.String(), "archived service; volumes kept")
	return c.toService(ctx, ss, apitypes.PendingChanges{})
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
		DockerName: fmt.Sprintf("%s-%s-%s-%s", ss.project.Name, ss.env.Name, ss.svc.Name, req.Name),
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

func (c *Core) toService(ctx context.Context, ss svcScope, pending apitypes.PendingChanges) (apitypes.Service, error) {
	out := apitypes.Service{
		ID: ss.svc.ID, Project: ss.project.Name, Environment: ss.env.Name, Name: ss.svc.Name,
		ServiceSettings: settingsOf(ss.svc), Status: c.getStatus(ss.svc.ID),
		CreatedAt: ss.svc.CreatedAt, ArchivedAt: ss.svc.ArchivedAt,
		PendingChanges: pending.CountFor(ss.svc.Name),
	}
	if active, err := c.store.GetActiveRelease(ctx, ss.svc.ID); err == nil {
		out.ActiveRelease = &active.Number
	} else if !store.IsNotFound(err) {
		return out, err
	}
	vols, err := c.store.ListVolumes(ctx, ss.svc.ID)
	if err != nil {
		return out, err
	}
	for _, v := range vols {
		if !v.Pending {
			out.Stateful = true
		}
	}
	return out, nil
}
