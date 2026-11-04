package core

import (
	"cmp"
	"context"
	"slices"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// ExportProject returns a project's configuration as a manifest (ADR
// 0022): the environments the actor can see, their live services and
// settings, variables, volumes, domains and git connections. Staged
// changes (services staged for creation too), archived services,
// releases, placement and preview environments are left out. Values are
// exported only when they are made of references and nothing else (ADR
// 0049): they wire services together
// and hold no secret. Every other variable is exported by name.
// Everything is sorted by name so the export diffs well. A fixed number of
// queries, however big the project. Needs read.
func (c *Core) ExportProject(ctx context.Context, a Actor, project string) (apitypes.ProjectExport, error) {
	out := apitypes.ProjectExport{Version: apitypes.ExportVersion, Project: project, Environments: []apitypes.EnvironmentExport{}}
	p, err := c.projectFor(ctx, a, project, apitypes.ScopeRead, "exporting a project")
	if err != nil {
		return out, err
	}
	envs, err := c.store.ListEnvironments(ctx, p.ID)
	if err != nil {
		return out, err
	}
	svcs, err := c.store.ListProjectServices(ctx, p.ID)
	if err != nil {
		return out, err
	}
	vars, err := c.store.ListProjectLiveVariables(ctx, p.ID)
	if err != nil {
		return out, err
	}
	vols, err := c.store.ListProjectVolumes(ctx, p.ID)
	if err != nil {
		return out, err
	}
	domains, err := c.store.ListProjectDomains(ctx, p.ID)
	if err != nil {
		return out, err
	}
	gits, err := c.store.ListProjectGitConnections(ctx, p.ID)
	if err != nil {
		return out, err
	}
	// Preview environments come and go with pull requests (ADR 0056): a
	// project file that named them would make them ordinary ones.
	previews, err := c.previewEnvironments(ctx, p.ID)
	if err != nil {
		return out, err
	}

	// Services by ID; their environments' exports collect them.
	bySvc := map[int64]*apitypes.ServiceExport{}
	byEnv := map[int64][]*apitypes.ServiceExport{}
	for _, s := range svcs {
		if s.ArchivedAt != nil || s.Staged == "create" {
			continue
		}
		se := serviceExport(s)
		bySvc[s.ID] = se
		byEnv[s.EnvironmentID] = append(byEnv[s.EnvironmentID], se)
	}
	shared := map[int64][]string{}
	sharedValues := map[int64]map[string]string{}
	for _, v := range vars {
		var se *apitypes.ServiceExport
		if v.ServiceID != nil {
			if se = bySvc[*v.ServiceID]; se == nil {
				continue
			}
		}
		value, err := c.openValue(v.Value, variableAAD(v.EnvironmentID, v.ServiceID, v.Key), v.Key)
		if err != nil {
			return out, err
		}
		ref := value != nil && onlyReferences(*value)
		switch {
		case se == nil && ref:
			if sharedValues[v.EnvironmentID] == nil {
				sharedValues[v.EnvironmentID] = map[string]string{}
			}
			sharedValues[v.EnvironmentID][v.Key] = *value
		case se == nil:
			shared[v.EnvironmentID] = append(shared[v.EnvironmentID], v.Key)
		case ref:
			if se.Values == nil {
				se.Values = map[string]string{}
			}
			se.Values[v.Key] = *value
		default:
			se.Variables = append(se.Variables, v.Key)
		}
	}
	for _, v := range vols {
		if se := bySvc[v.ServiceID]; se != nil && !v.Pending {
			se.Volumes = append(se.Volumes, apitypes.VolumeExport{Name: v.Name, MountPath: v.MountPath})
		}
	}
	for _, d := range domains {
		if se := bySvc[d.ServiceID]; se != nil {
			se.Domains = append(se.Domains, apitypes.DomainExport{Hostname: d.Hostname, RedirectTo: d.RedirectTo, Generated: d.Generated})
		}
	}
	for _, g := range gits {
		if se := bySvc[g.ServiceID]; se != nil {
			se.Git = &apitypes.GitExport{Repository: g.Repo, Branch: g.Branch, Directory: g.Dir, Statuses: g.PostStatus}
		}
	}

	for _, e := range envs {
		if !a.canSee(p.OrgID, p.ID, e.ID) || previews[e.ID] != nil {
			continue
		}
		ee := apitypes.EnvironmentExport{Name: e.Name, Kind: apitypes.EnvironmentKind(e.Kind), SharedVariables: shared[e.ID], SharedValues: sharedValues[e.ID]}
		drains, err := c.store.ListLogDrains(ctx, e.ID) // by name
		if err != nil {
			return out, err
		}
		for _, d := range drains {
			ee.LogDrains = append(ee.LogDrains, apitypes.LogDrainExport{Name: d.Name, Kind: apitypes.LogDrainKind(d.Kind), Target: d.Target})
		}
		for _, se := range byEnv[e.ID] {
			// The queries sort by name or key already, except domains
			// (by ID); sort everything here so the order never depends
			// on a query plan.
			slices.Sort(se.Variables)
			slices.SortFunc(se.Volumes, func(x, y apitypes.VolumeExport) int { return cmp.Compare(x.Name, y.Name) })
			slices.SortFunc(se.Domains, func(x, y apitypes.DomainExport) int { return cmp.Compare(x.Hostname, y.Hostname) })
			ee.Services = append(ee.Services, *se)
		}
		slices.Sort(ee.SharedVariables)
		slices.SortFunc(ee.Services, func(x, y apitypes.ServiceExport) int { return cmp.Compare(x.Name, y.Name) })
		out.Environments = append(out.Environments, ee)
	}
	slices.SortFunc(out.Environments, func(x, y apitypes.EnvironmentExport) int { return cmp.Compare(x.Name, y.Name) })
	// The registry hosts its images pull from with an org credential, by
	// host: a file names what it needs, never the credential.
	creds, err := c.store.ListRegistryCredentials(ctx, p.OrgID) // by host
	if err != nil {
		return out, err
	}
	for _, cr := range creds {
		if slices.ContainsFunc(out.Environments, func(e apitypes.EnvironmentExport) bool {
			return slices.ContainsFunc(e.Services, func(s apitypes.ServiceExport) bool { return s.Image != "" && imageRegistry(s.Image) == cr.Host })
		}) {
			out.RegistryCredentials = append(out.RegistryCredentials, cr.Host)
		}
	}
	return out, nil
}

// serviceExport is a service's live settings in an export.
func serviceExport(s store.Service) *apitypes.ServiceExport {
	return &apitypes.ServiceExport{
		Name: s.Name, Template: apitypes.Template(s.Template), Stopped: s.StoppedAt != nil, Image: s.Image, Port: int(s.Port),
		HealthCheckPath: s.HealthCheckPath, HealthCheckTimeoutSeconds: int(s.HealthCheckTimeoutSeconds),
		StartCommand: s.StartCommand, DockerfilePath: s.DockerfilePath, BuildCommand: s.BuildCommand,
		MemoryMB: int(s.MemoryMb), CPUs: s.Cpus,
		HealthCheckIntervalSeconds:     int(s.HealthCheckIntervalSeconds),
		HealthCheckProbeTimeoutSeconds: int(s.HealthCheckProbeTimeoutSeconds),
		HealthCheckRetries:             int(s.HealthCheckRetries),
		HealthCheckStartPeriodSeconds:  int(s.HealthCheckStartPeriodSeconds),
		Database:                       apitypes.DatabaseSetting(s.Database), DatabaseURLVariable: s.DatabaseUrlVariable,
		PreDeployCommand: s.PreDeployCommand, PreDeployTimeoutSeconds: int(s.PreDeployTimeoutSeconds),
		PreDeploySnapshot: s.PreDeploySnapshot,
	}
}
