package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// EnvRef names an environment.
type EnvRef struct{ Project, Environment string }

// ServiceRef names a service.
type ServiceRef struct{ Project, Environment, Service string }

// Env returns the service's environment reference.
func (r ServiceRef) Env() EnvRef { return EnvRef{r.Project, r.Environment} }

func (r ServiceRef) String() string { return r.Project + "/" + r.Environment + "/" + r.Service }

type envScope struct {
	project store.Project
	env     store.Environment
}

type svcScope struct {
	envScope
	svc store.Service
}

func (s envScope) String() string { return s.project.Name + "/" + s.env.Name }

func (s svcScope) String() string { return s.envScope.String() + "/" + s.svc.Name }

// nameRe keeps project, environment, service and volume names DNS-safe:
// service names are private hostnames. Docker resources are named by ID
// (see networkName), and these names never contain the "_" those use.
var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

func validName(kind, name string) error {
	if !nameRe.MatchString(name) {
		return invalid("%s name %q must be 1-32 lowercase letters, digits or dashes, not starting or ending with a dash", kind, name)
	}
	return nil
}

// reservedProjectNames are the web UI's top-level paths (web/src/routes,
// public/ and assets/): a project's page is /<name>, so a project with one
// of these names could not be opened.
var reservedProjectNames = []string{
	"account", "alerts", "api", "assets", "audit", "backups", "cli-login",
	"data-policy", "fonts", "login", "servers", "tokens", "users",
}

func (c *Core) loadProject(ctx context.Context, name string) (store.Project, error) {
	p, err := c.store.GetProjectByName(ctx, name)
	if store.IsNotFound(err) {
		return p, notFound("project %q not found", name)
	}
	return p, err
}

func (c *Core) loadEnv(ctx context.Context, ref EnvRef) (envScope, error) {
	p, err := c.loadProject(ctx, ref.Project)
	if err != nil {
		return envScope{}, err
	}
	e, err := c.store.GetEnvironmentByName(ctx, store.GetEnvironmentByNameParams{ProjectID: p.ID, Name: ref.Environment})
	if store.IsNotFound(err) {
		return envScope{}, notFound("environment %q not found in project %s", ref.Environment, p.Name)
	}
	return envScope{p, e}, err
}

func (c *Core) loadService(ctx context.Context, ref ServiceRef) (svcScope, error) {
	es, err := c.loadEnv(ctx, ref.Env())
	if err != nil {
		return svcScope{}, err
	}
	s, err := c.store.GetServiceByName(ctx, store.GetServiceByNameParams{EnvironmentID: es.env.ID, Name: ref.Service})
	if store.IsNotFound(err) {
		return svcScope{}, notFound("service %q not found in %s", ref.Service, es)
	}
	return svcScope{es, s}, err
}

// projectFor loads a project and checks the actor may do action on it.
func (c *Core) projectFor(ctx context.Context, a Actor, name string, need apitypes.Scope, action string) (store.Project, error) {
	if err := a.require(need, action); err != nil {
		return store.Project{}, err
	}
	p, err := c.loadProject(ctx, name)
	if err != nil {
		return p, err
	}
	return p, a.checkProject(need, action, p)
}

func (c *Core) envFor(ctx context.Context, a Actor, ref EnvRef, need apitypes.Scope, action string) (envScope, error) {
	if err := a.require(need, action); err != nil {
		return envScope{}, err
	}
	es, err := c.loadEnv(ctx, ref)
	if err != nil {
		return es, err
	}
	return es, a.checkEnv(need, action, es.project, es.env)
}

func (c *Core) serviceFor(ctx context.Context, a Actor, ref ServiceRef, need apitypes.Scope, action string) (svcScope, error) {
	if err := a.require(need, action); err != nil {
		return svcScope{}, err
	}
	ss, err := c.loadService(ctx, ref)
	if err != nil {
		return ss, err
	}
	return ss, a.checkEnv(need, action, ss.project, ss.env)
}

// ListProjects returns the projects the actor can see; archived ones only
// with archived.
func (c *Core) ListProjects(ctx context.Context, a Actor, archived bool) ([]apitypes.Project, error) {
	if err := a.require(apitypes.ScopeRead, "listing projects"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := []apitypes.Project{}
	for _, p := range rows {
		if (a.ProjectID == 0 || a.ProjectID == p.ID) && (archived || p.ArchivedAt == nil) {
			out = append(out, toProject(p))
		}
	}
	return out, nil
}

// ArchiveProject archives a project and every service in it, the same
// non-destructive way as ArchiveService: routes removed, containers stopped
// in the background, volumes and history kept. Its environments' networks
// go once their containers are stopped (removeNetworks). The project is
// hidden from lists and gets no new environments or services. Refused
// while any of its services is deploying.
func (c *Core) ArchiveProject(ctx context.Context, a Actor, name string) (apitypes.Project, error) {
	p, err := c.projectFor(ctx, a, name, apitypes.ScopeAdmin, "archiving a project")
	if err != nil {
		return apitypes.Project{}, err
	}
	var done []archivedService
	var envs []store.Environment
	already := false
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if p, err = q.GetProject(ctx, p.ID); err != nil {
			return err
		}
		if already = p.ArchivedAt != nil; already {
			return nil
		}
		if envs, err = q.ListEnvironments(ctx, p.ID); err != nil {
			return err
		}
		svcs, err := q.ListProjectServices(ctx, p.ID)
		if err != nil {
			return err
		}
		for _, s := range svcs {
			i := slices.IndexFunc(envs, func(e store.Environment) bool { return e.ID == s.EnvironmentID })
			as, ok, err := archiveTx(ctx, q, svcScope{envScope{p, envs[i]}, s})
			if err != nil {
				return err
			}
			if ok {
				done = append(done, as)
			}
		}
		n := now()
		p.ArchivedAt = &n
		return q.ArchiveProject(ctx, store.ArchiveProjectParams{ArchivedAt: &n, ID: p.ID})
	})
	if err != nil || already {
		return toProject(p), err
	}
	notes, stopped := c.finishArchive(ctx, done)
	notes = append(notes, c.removeNetworks(ctx, p, envs, done, stopped)...)
	c.maybePruneRegistry()
	c.audit(ctx, a, "project.archive", &p, nil, p.Name, archiveSummary(fmt.Sprintf("archived project and its %d services; volumes kept", len(done)), notes))
	out := toProject(p)
	out.ArchiveNotes = notes
	return out, nil
}

// removeNetworks removes an archived project's environment networks in
// the background, once stopped is done (finishArchive's container stops),
// so their subnets go back to [network] pool: each holds a /24, and the
// pool runs out with 256 left behind. A network is only removed while no
// container uses it (Docker refuses), and only one labelled for this
// control plane and environment.
//
// Servers that can't be reached keep theirs: the notes it returns say so,
// one per server, naming the networks of environments that had a running
// release (the ones sure to exist), for the archive's answer.
func (c *Core) removeNetworks(ctx context.Context, p store.Project, envs []store.Environment, done []archivedService, stopped *sync.WaitGroup) []string {
	deployed := map[int64]bool{}
	for _, as := range done {
		deployed[as.env.ID] = deployed[as.env.ID] || as.active != nil
	}
	links := map[int64]serverLink{}
	left := map[int64][]string{} // networks on unreachable servers, by server
	var online []envScope
	for _, e := range envs {
		if _, ok := links[e.ServerID]; !ok {
			links[e.ServerID] = c.linkOf(ctx, e.ServerID)
		}
		es := envScope{p, e}
		switch {
		case links[e.ServerID].status == apitypes.ServerOnline:
			online = append(online, es)
		case deployed[e.ID]:
			left[e.ServerID] = append(left[e.ServerID], networkName(es))
		}
	}
	var notes []string
	for _, id := range slices.Sorted(maps.Keys(left)) {
		link, nets := links[id], left[id]
		what, it := "network", "it"
		if len(nets) > 1 {
			what, it = "networks", "them"
		}
		notes = append(notes, fmt.Sprintf("server %s is %s: its %s %s stayed; remove %s on that machine with `docker network rm %s`",
			link.name, link.status, what, strings.Join(nets, ", "), it, strings.Join(nets, " ")))
	}
	if len(online) == 0 {
		return notes
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		stopped.Wait()
		for _, es := range online {
			rt := c.connected(es.env.ServerID)
			if rt == nil {
				continue // went offline meanwhile
			}
			name := networkName(es)
			labels := node.Labels{ControlPlane: c.install, Project: strconv.FormatInt(p.ID, 10), Environment: strconv.FormatInt(es.env.ID, 10)}
			if err := rt.RemoveNetwork(c.ctx, name, labels); err != nil && !errors.Is(err, node.ErrNotFound) {
				c.log.Warn("remove the network of an archived environment", "network", name, "server", es.env.ServerID, "err", err)
			}
		}
	}()
	return notes
}

// notArchived refuses adding to an archived project.
func notArchived(p store.Project) error {
	if p.ArchivedAt != nil {
		return invalid("project %s is archived", p.Name)
	}
	return nil
}

// GetProject returns one project.
func (c *Core) GetProject(ctx context.Context, a Actor, name string) (apitypes.Project, error) {
	p, err := c.projectFor(ctx, a, name, apitypes.ScopeRead, "reading a project")
	return toProject(p), err
}

// CreateProject creates a project with a `production` environment.
func (c *Core) CreateProject(ctx context.Context, a Actor, req apitypes.CreateProjectRequest) (apitypes.Project, error) {
	if err := a.requireUnlimited(apitypes.ScopeDeploy, "creating a project"); err != nil {
		return apitypes.Project{}, err
	}
	if err := validName("project", req.Name); err != nil {
		return apitypes.Project{}, err
	}
	if slices.Contains(reservedProjectNames, req.Name) {
		return apitypes.Project{}, invalid("%q is the address of a page in the web UI; pick another project name", req.Name)
	}
	var p store.Project
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		if p, err = q.CreateProject(ctx, req.Name); err != nil {
			return err
		}
		_, err = c.createEnv(ctx, q, p, "production", apitypes.KindProduction)
		return err
	})
	if store.IsUnique(err) {
		return apitypes.Project{}, conflict("project %q already exists", req.Name)
	}
	if err != nil {
		return apitypes.Project{}, err
	}
	c.audit(ctx, a, "project.create", &p, nil, p.Name, "created project "+p.Name+" with environment production")
	return toProject(p), nil
}

// createEnv places and creates an environment; call it in a transaction
// so the placement and the environment are recorded together.
func (c *Core) createEnv(ctx context.Context, q *store.Queries, p store.Project, name string, kind apitypes.EnvironmentKind) (store.Environment, error) {
	serverID, err := c.placeEnvironment(ctx, q, p, kind)
	if err != nil {
		return store.Environment{}, err
	}
	return q.CreateEnvironment(ctx, store.CreateEnvironmentParams{ProjectID: p.ID, Name: name, Kind: string(kind), ServerID: serverID})
}

// ListEnvironments returns a project's environments the actor can see.
func (c *Core) ListEnvironments(ctx context.Context, a Actor, project string) ([]apitypes.Environment, error) {
	p, err := c.projectFor(ctx, a, project, apitypes.ScopeRead, "listing environments")
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListEnvironments(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	servers, err := c.serverNames(ctx)
	if err != nil {
		return nil, err
	}
	out := []apitypes.Environment{}
	for _, e := range rows {
		if a.canSee(p.ID, e.ID) {
			out = append(out, toEnvironment(p, e, servers[e.ServerID]))
		}
	}
	return out, nil
}

// GetEnvironment returns one environment.
func (c *Core) GetEnvironment(ctx context.Context, a Actor, ref EnvRef) (apitypes.Environment, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeRead, "reading an environment")
	if err != nil {
		return apitypes.Environment{}, err
	}
	srv, err := c.store.GetServer(ctx, es.env.ServerID)
	if err != nil {
		return apitypes.Environment{}, err
	}
	return toEnvironment(es.project, es.env, srv.Name), nil
}

// CreateEnvironment creates an environment and places it on a server.
func (c *Core) CreateEnvironment(ctx context.Context, a Actor, project string, req apitypes.CreateEnvironmentRequest) (apitypes.Environment, error) {
	p, err := c.projectFor(ctx, a, project, apitypes.ScopeDeploy, "creating an environment")
	if err != nil {
		return apitypes.Environment{}, err
	}
	if err := notArchived(p); err != nil {
		return apitypes.Environment{}, err
	}
	if err := validName("environment", req.Name); err != nil {
		return apitypes.Environment{}, err
	}
	kind := req.Kind
	if kind == "" {
		kind = defaultKind(req.Name)
	}
	if !validKind(kind) {
		return apitypes.Environment{}, invalid("kind must be production or non_production")
	}
	var e store.Environment
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		e, err = c.createEnv(ctx, q, p, req.Name, kind)
		return err
	})
	if store.IsUnique(err) {
		return apitypes.Environment{}, conflict("environment %q already exists in project %s", req.Name, p.Name)
	}
	if err != nil {
		return apitypes.Environment{}, err
	}
	srv, err := c.store.GetServer(ctx, e.ServerID)
	if err != nil {
		return apitypes.Environment{}, err
	}
	c.audit(ctx, a, "environment.create", &p, &e, p.Name+"/"+e.Name, fmt.Sprintf("created %s environment on server %s", kindWords(kind), srv.Name))
	return toEnvironment(p, e, srv.Name), nil
}

func (c *Core) serverNames(ctx context.Context) (map[int64]string, error) {
	rows, err := c.serverRows(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[int64]string, len(rows))
	for id, s := range rows {
		m[id] = s.Name
	}
	return m, nil
}

// serverRows loads every server by ID, in one query.
func (c *Core) serverRows(ctx context.Context) (map[int64]store.Server, error) {
	rows, err := c.store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[int64]store.Server, len(rows))
	for _, s := range rows {
		m[s.ID] = s
	}
	return m, nil
}

func toProject(p store.Project) apitypes.Project {
	return apitypes.Project{ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt, ArchivedAt: p.ArchivedAt}
}

func toEnvironment(p store.Project, e store.Environment, server string) apitypes.Environment {
	return apitypes.Environment{ID: e.ID, Project: p.Name, Name: e.Name, Kind: apitypes.EnvironmentKind(e.Kind), Server: server, CreatedAt: e.CreatedAt}
}
