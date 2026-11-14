package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/projectfile"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Project files (`dock apply FILE`, ADR 0049). A plan compares a file with
// each of its environments' live state; applying turns that plan into
// staged changes, so a file goes through the same review, apply, audit and
// permission checks as edits from the CLI or UI. Only a new project and new
// (empty) environments are created at once: they run nothing.
//
// The plan is against live values. Staging replaces any staged change to an
// item the file sets, so after `dock changes apply` the live state matches
// the file; staged changes to items the file doesn't mention stay.

// PlanProject compares a project file with the live state. Needs read (on
// the project, if it exists).
func (c *Core) PlanProject(ctx context.Context, a Actor, project string, req apitypes.ApplyRequest) (apitypes.ApplyPlan, error) {
	f, p, err := c.loadFile(ctx, a, project, req, apitypes.ScopeRead)
	if err != nil {
		return apitypes.ApplyPlan{}, err
	}
	plan := newPlan(project)
	if p == nil {
		plan.Changes = append(plan.Changes, apitypes.PlanChange{Kind: apitypes.PlanProject, Action: apitypes.ChangeAdd, Name: project})
	}
	for _, fe := range f.envs {
		var le *liveEnv
		if p != nil {
			if le, err = c.liveEnvFor(ctx, c.store.Queries, a, *p, fe.Name); err != nil {
				return plan, err
			}
		}
		ep, err := c.planEnv(a, fe, le, f.values, req.Prune)
		if err != nil {
			return plan, err
		}
		addPlan(&plan, ep)
	}
	reg, err := c.planRegistry(ctx, a, f.registry)
	plan.Manual = append(plan.Manual, reg...)
	return plan, err
}

// ApplyProject stages a project file: it creates the project and any new
// environments (empty), then stages every change of the plan, one
// transaction per environment, replanned inside it; jobs are set at once
// in the same transaction (jobs_apply.go). With req.Deploy it then
// applies each touched environment's staged changes like ApplyChanges,
// and sets the jobs of the services that created. Needs deploy; removing
// services (prune) needs admin.
func (c *Core) ApplyProject(ctx context.Context, a Actor, project string, req apitypes.ApplyRequest) (apitypes.ApplyResult, error) {
	out := apitypes.ApplyResult{Plan: newPlan(project), Staged: []apitypes.PendingChanges{}}
	f, p, err := c.loadFile(ctx, a, project, req, apitypes.ScopeDeploy)
	if err != nil {
		return out, err
	}
	if p == nil {
		if _, err := c.CreateProject(ctx, a, apitypes.CreateProjectRequest{Name: project}); err != nil {
			return out, err
		}
		out.Plan.Changes = append(out.Plan.Changes, apitypes.PlanChange{Kind: apitypes.PlanProject, Action: apitypes.ChangeAdd, Name: project})
	}
	var touched []envScope
	for _, fe := range f.envs {
		ref := EnvRef{project, fe.Name}
		es, err := c.envFor(ctx, a, ref, apitypes.ScopeDeploy, "applying a project file")
		created := false
		if CodeOf(err) == apitypes.CodeNotFound {
			kind := fe.Kind
			if kind == "" {
				kind = defaultKind(fe.Name)
			}
			if _, err := c.CreateEnvironment(ctx, a, project, apitypes.CreateEnvironmentRequest{Name: fe.Name, Kind: kind}); err != nil {
				return out, err
			}
			es, err = c.envFor(ctx, a, ref, apitypes.ScopeDeploy, "applying a project file")
			created = true
		}
		if err != nil {
			return out, err
		}
		var ep envPlan
		var jobs fileJobsDone
		err = c.store.Tx(ctx, func(q *store.Queries) error {
			le, err := loadLiveEnv(ctx, q, c, es.env)
			if err != nil {
				return err
			}
			if ep, err = c.planEnv(a, fe, le, f.values, req.Prune); err != nil {
				return err
			}
			if created {
				// Created a moment ago: the plan line says so.
				ep.changes = slices.Insert(ep.changes, 0, apitypes.PlanChange{Environment: fe.Name, Kind: apitypes.PlanEnvironment, Action: apitypes.ChangeAdd, Name: fe.Name, New: es.env.Kind})
			}
			if len(ep.archive) > 0 {
				if err := a.checkEnvAccess(apitypes.ScopeAdmin, "archiving services a project file leaves out (--prune)", es.project, es.env); err != nil {
					return err
				}
			}
			if err := c.stageEnv(ctx, q, es, le, ep); err != nil {
				return err
			}
			jobs, err = c.setFileJobs(ctx, q, a, es, le.svcs, ep.jobs)
			return err
		})
		if err != nil {
			return out, err
		}
		jobs.finish(ctx, c, a, es)
		addPlan(&out.Plan, ep)
		if ep.staged > 0 {
			c.audit(ctx, a, "changes.stage_file", &es.project, &es.env, es.String(),
				fmt.Sprintf("staged %s from a project file%s", count(ep.staged, "change"), map[bool]string{true: " (with --prune)"}[req.Prune]))
		}
		touched = append(touched, es)
	}
	reg, err := c.planRegistry(ctx, a, f.registry)
	if err != nil {
		return out, err
	}
	out.Plan.Manual = append(out.Plan.Manual, reg...)
	for _, es := range touched {
		pc, err := c.pendingChanges(ctx, c.store.Queries, es, false)
		if err != nil {
			return out, err
		}
		out.Staged = append(out.Staged, pc)
	}
	if !req.Deploy {
		return out, nil
	}
	for i, es := range touched {
		if out.Staged[i].Count == 0 {
			continue
		}
		res, err := c.applyEnv(ctx, a, es, f.order[es.env.Name])
		if err != nil {
			return out, fmt.Errorf("staged the file, but applying %s failed: %w", es, err)
		}
		out.Applied = append(out.Applied, apitypes.EnvironmentApplied{Environment: es.env.Name, AppliedChanges: res})
		fe, _ := projectfile.Environment(apitypes.ProjectExport{Environments: f.envs}, es.env.Name)
		chs, err := c.applyCreatedJobs(ctx, a, es, fe, req.Prune)
		if err != nil {
			return out, fmt.Errorf("applied %s, but setting the jobs of its new services failed: %w", es, err)
		}
		out.Plan.Changes = append(out.Plan.Changes, chs...)
	}
	return out, nil
}

// parsedFile is a checked project file with the environments to plan and
// the values sent with it.
type parsedFile struct {
	envs   []apitypes.EnvironmentExport
	values map[varKey]string
	// order is each environment's depends_on, by service, for first deploys.
	order map[string]map[string][]string
	// registry are the registry hosts the file needs credentials for.
	registry []string
}

// varKey names a variable of a file: service "" is a shared one.
type varKey struct{ env, service, key string }

// loadFile parses req.File, picks its environments and checks the actor
// may plan (read) or apply (deploy) each of them. p is nil if the project
// doesn't exist yet.
func (c *Core) loadFile(ctx context.Context, a Actor, project string, req apitypes.ApplyRequest, need apitypes.Scope) (parsedFile, *store.Project, error) {
	var out parsedFile
	f, err := projectfile.Parse([]byte(req.File))
	if err != nil {
		if req.Environment != "" && need != apitypes.ScopeRead {
			// The environment the caller named refuses first (a lock, a
			// token limit): its answer doesn't depend on the file.
			if _, lerr := c.envFor(ctx, a, EnvRef{project, req.Environment}, need, "applying a project file"); lerr != nil && CodeOf(lerr) != apitypes.CodeNotFound {
				return out, nil, lerr
			}
		}
		return out, nil, invalid("%v", err)
	}
	out.envs = f.Environments
	out.registry = f.RegistryCredentials
	if req.Environment != "" {
		fe, ok := projectfile.Environment(f, req.Environment)
		if !ok {
			return out, nil, notFound("environment %s isn't in the project file", req.Environment)
		}
		out.envs = []apitypes.EnvironmentExport{fe}
	}
	if len(out.envs) == 0 {
		return out, nil, invalid("the project file has no environments")
	}
	out.order = map[string]map[string][]string{}
	for _, fe := range out.envs {
		for _, s := range fe.Services {
			if len(s.DependsOn) > 0 {
				if out.order[fe.Name] == nil {
					out.order[fe.Name] = map[string][]string{}
				}
				out.order[fe.Name][s.Name] = s.DependsOn
			}
		}
	}
	out.values = map[varKey]string{}
	for _, v := range req.Values {
		fe, ok := projectfile.Environment(f, v.Environment)
		named := ok && slices.Contains(fe.SharedVariables, v.Key)
		if ok && v.Service != "" {
			i := slices.IndexFunc(fe.Services, func(s apitypes.ServiceExport) bool { return s.Name == v.Service })
			named = i >= 0 && slices.Contains(fe.Services[i].Variables, v.Key)
		}
		if !named {
			return out, nil, invalid("a value for %s isn't named in the file's variables (or shared_variables) of that service and environment", varKey{v.Environment, v.Service, v.Key})
		}
		out.values[varKey{v.Environment, v.Service, v.Key}] = v.Value
	}

	if err := a.require(need, "applying a project file"); err != nil {
		return out, nil, err
	}
	p, err := c.loadProject(ctx, a.OrgID, project)
	if store.IsNotFound(err) || CodeOf(err) == apitypes.CodeNotFound {
		if err := validProjectName(project); err != nil {
			return out, nil, err
		}
		// A new project is created at once; creating one needs what
		// `dock project create` needs.
		if err := a.requireUnlimited(need, "applying a project file to a new project"); err != nil {
			return out, nil, err
		}
		return out, nil, nil
	}
	if err != nil {
		return out, nil, err
	}
	// Each environment is checked below; an environment-limited token
	// may change its own.
	if err := a.checkProject(apitypes.ScopeRead, "applying a project file", p); err != nil {
		return out, nil, err
	}
	if err := notArchived(p); err != nil {
		return out, nil, err
	}
	envs, err := c.store.ListEnvironments(ctx, p.ID)
	if err != nil {
		return out, nil, err
	}
	for _, fe := range out.envs {
		i := slices.IndexFunc(envs, func(e store.Environment) bool { return e.Name == fe.Name })
		if i < 0 {
			// New environments are created at once; that needs a token
			// that isn't limited to one environment.
			if a.limited() && a.EnvironmentID != 0 {
				return out, nil, errf(apitypes.CodeForbidden, "this token is limited to %s and can't create environment %s; apply only its environment with --environment", a.limitName(), fe.Name)
			}
			continue
		}
		if err := a.checkEnv(need, "applying a project file", p, envs[i]); err != nil {
			if _, locked := errors.AsType[*LockedError](err); locked {
				// One refusal for the whole file, naming what it would
				// change there, so an access request shows the plan.
				return out, nil, c.lockedPlan(ctx, a, p, envs[i], fe, out.values, req.Prune)
			}
			return out, nil, errf(apitypes.CodeForbidden, "%v; apply only the environments this token may change with --environment", err)
		}
	}
	return out, &p, nil
}

func (k varKey) String() string {
	if k.service == "" {
		return k.env + "/shared." + k.key
	}
	return k.env + "/" + k.service + "." + k.key
}

// lockedPlan is the lock's refusal of a file for a locked environment,
// with what the file would change there (at most planDigestLines lines).
func (c *Core) lockedPlan(ctx context.Context, a Actor, p store.Project, e store.Environment, fe apitypes.EnvironmentExport, values map[varKey]string, prune bool) error {
	le, err := loadLiveEnv(ctx, c.store.Queries, c, e)
	if err != nil {
		return err
	}
	ep, err := c.planEnv(a, fe, le, values, prune)
	if err != nil {
		return err
	}
	return a.lockedErr(p, e, "applying a project file that "+planDigest(ep.changes))
}

// planDigestLines caps the plan a lock refusal quotes.
const planDigestLines = 12

// planDigest is a plan's changes in one line: "changes: + service api,
// ~ web port 3000 → 8080, and 3 more".
func planDigest(chs []apitypes.PlanChange) string {
	if len(chs) == 0 {
		return "changes nothing"
	}
	glyph := map[apitypes.ChangeAction]string{apitypes.ChangeAdd: "+", apitypes.ChangeUpdate: "~", apitypes.ChangeRemove: "-"}
	var parts []string
	for _, ch := range chs[:min(len(chs), planDigestLines)] {
		what := strings.TrimSpace(ch.Service + " " + string(ch.Kind) + " " + ch.Name)
		if ch.Kind == apitypes.PlanService {
			what = "service " + ch.Name
		}
		if ch.Old != "" && ch.New != "" {
			what += " " + ch.Old + " → " + ch.New
		}
		parts = append(parts, glyph[ch.Action]+" "+what)
	}
	if more := len(chs) - planDigestLines; more > 0 {
		parts = append(parts, fmt.Sprintf("and %d more", more))
	}
	return "changes: " + strings.Join(parts, ", ")
}

// liveEnv is an environment's live state, as a plan compares it.
type liveEnv struct {
	row store.Environment
	// shared and each service's vars hold live values (opened); staged
	// holds the staged action ("set", "unset") by key.
	shared       map[string]string
	sharedStaged map[string]string
	svcs         map[string]*liveSvc
	// archived names archived services, whose names stay taken.
	archived map[string]bool
	// baseDomain is whether the server can generate hostnames.
	baseDomain bool
	// drains are the environment's log drains by name.
	drains map[string]store.LogDrain
}

type liveSvc struct {
	row     store.Service
	vars    map[string]string
	staged  map[string]string
	pending *apitypes.ServiceSettings
	vols    map[string]store.Volume
	domains []store.Domain
	// stagedDomains by hostname ("" for a generated one to add).
	stagedDomains map[string]store.ListEnvironmentStagedDomainsRow
	git           *store.ListEnvironmentGitConnectionsRow
	jobs          []store.ServiceJob
}

// liveEnvFor loads an environment of p for a plan, or nil if it doesn't
// exist yet.
func (c *Core) liveEnvFor(ctx context.Context, q *store.Queries, a Actor, p store.Project, name string) (*liveEnv, error) {
	e, err := q.GetEnvironmentByName(ctx, store.GetEnvironmentByNameParams{ProjectID: p.ID, Name: name})
	if store.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return loadLiveEnv(ctx, q, c, e)
}

// loadLiveEnv loads an environment's live state in a fixed number of queries, however
// many services it has.
func loadLiveEnv(ctx context.Context, q *store.Queries, c *Core, e store.Environment) (*liveEnv, error) {
	le := &liveEnv{row: e, shared: map[string]string{}, sharedStaged: map[string]string{}, svcs: map[string]*liveSvc{}, archived: map[string]bool{}}
	svcs, err := q.ListServices(ctx, e.ID)
	if err != nil {
		return nil, err
	}
	byID := map[int64]*liveSvc{}
	for _, s := range svcs {
		if s.ArchivedAt != nil {
			le.archived[s.Name] = true
			continue
		}
		ls := &liveSvc{row: s, vars: map[string]string{}, staged: map[string]string{}, vols: map[string]store.Volume{}, stagedDomains: map[string]store.ListEnvironmentStagedDomainsRow{}}
		if ls.pending, err = pendingSettings(s); err != nil {
			return nil, err
		}
		le.svcs[s.Name], byID[s.ID] = ls, ls
	}
	vars, err := q.ListVariables(ctx, e.ID)
	if err != nil {
		return nil, err
	}
	for _, v := range vars {
		values, staged := le.shared, le.sharedStaged
		if v.ServiceID != nil {
			ls := byID[*v.ServiceID]
			if ls == nil {
				continue // an archived service's
			}
			values, staged = ls.vars, ls.staged
		}
		if v.Value != nil {
			s, err := c.openValue(v.Value, variableAAD(v.EnvironmentID, v.ServiceID, v.Key), v.Key)
			if err != nil {
				return nil, err
			}
			values[v.Key] = *s
		}
		if v.Pending != "" {
			staged[v.Key] = v.Pending
		}
	}
	vols, err := q.ListEnvironmentVolumes(ctx, e.ID)
	if err != nil {
		return nil, err
	}
	for _, v := range vols {
		if ls := byID[v.ServiceID]; ls != nil {
			ls.vols[v.Name] = v
		}
	}
	domains, err := q.ListEnvironmentDomains(ctx, e.ID)
	if err != nil {
		return nil, err
	}
	for _, d := range domains {
		if ls := byID[d.ServiceID]; ls != nil {
			ls.domains = append(ls.domains, d)
		}
	}
	staged, err := q.ListEnvironmentStagedDomains(ctx, e.ID)
	if err != nil {
		return nil, err
	}
	for _, d := range staged {
		if ls := byID[d.ServiceID]; ls != nil {
			ls.stagedDomains[d.Hostname] = d
		}
	}
	drains, err := q.ListLogDrains(ctx, e.ID)
	if err != nil {
		return nil, err
	}
	le.drains = map[string]store.LogDrain{}
	for _, d := range drains {
		le.drains[d.Name] = d
	}
	gits, err := q.ListEnvironmentGitConnections(ctx, e.ID)
	if err != nil {
		return nil, err
	}
	for _, g := range gits {
		if ls := byID[g.ServiceID]; ls != nil {
			ls.git = &g
		}
	}
	jobs, err := q.ListEnvironmentJobs(ctx, e.ID)
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if ls := byID[j.ServiceID]; ls != nil {
			ls.jobs = append(ls.jobs, j)
		}
	}
	if srv, err := q.GetServer(ctx, e.ServerID); err == nil {
		le.baseDomain = c.ingressOf(srv).baseDomain != ""
	} else if !store.IsNotFound(err) {
		return nil, err
	}
	return le, nil
}

// envPlan is one environment's part of a plan: its lines and what staging
// it does.
type envPlan struct {
	changes, kept, manual []apitypes.PlanChange
	// staged counts the changes staging makes (for the audit entry).
	staged int
	// blind plans a value sent for a live variable as an update without
	// comparing them, so a plan can't confirm a guessed secret for an
	// actor that may not reveal secrets.
	blind bool

	create    []apitypes.ServiceExport
	settings  map[string]*apitypes.ServiceSettings // nil: clear staged settings
	unarchive []string
	archive   []string // a staged creation is discarded instead
	set       []varSet
	volumes   []volAdd
	domains   []domainStage
	// jobs are set or removed at once, not staged (jobs_apply.go).
	jobs []jobOp
}

// varSet is a variable to stage: set to value, unset, or (keep) back to
// its live value, dropping a staged change.
type varSet struct {
	service, key, value string
	unset, keep         bool
}

type volAdd struct{ service, name, mount string }

// domainStage is a domain change to stage, or (cancel) a staged one to drop.
type domainStage struct {
	service, host, redirect string
	generated, remove       bool
	cancel                  bool
}

func newPlan(project string) apitypes.ApplyPlan {
	return apitypes.ApplyPlan{Project: project, Changes: []apitypes.PlanChange{}, Kept: []apitypes.PlanChange{}, Manual: []apitypes.PlanChange{}}
}

// add appends an environment's lines to the plan.
func addPlan(p *apitypes.ApplyPlan, ep envPlan) {
	p.Changes = append(p.Changes, ep.changes...)
	p.Kept = append(p.Kept, ep.kept...)
	p.Manual = append(p.Manual, ep.manual...)
}

// planEnv compares a file environment with its live state (le nil: it
// doesn't exist yet) for a. values are the values sent with the file.
func (c *Core) planEnv(a Actor, fe apitypes.EnvironmentExport, le *liveEnv, values map[varKey]string, prune bool) (envPlan, error) {
	ep := envPlan{settings: map[string]*apitypes.ServiceSettings{}, blind: !a.revealsAt(0, 0, 0)}
	if le != nil {
		ep.blind = !a.revealsAt(le.row.ProjectID, le.row.ID, envKind(le.row))
	}
	env := fe.Name
	if err := validName("environment", env); err != nil {
		return ep, err
	}
	kind := fe.Kind
	if kind == "" {
		kind = defaultKind(env)
	}
	if !validKind(kind) {
		return ep, invalid("environment %s: kind must be production or non_production", env)
	}
	line := func(to *[]apitypes.PlanChange, ch apitypes.PlanChange) {
		ch.Environment = env
		*to = append(*to, ch)
	}
	if le == nil {
		line(&ep.changes, apitypes.PlanChange{Kind: apitypes.PlanEnvironment, Action: apitypes.ChangeAdd, Name: env, New: string(kind)})
		le = &liveEnv{shared: map[string]string{}, sharedStaged: map[string]string{}, svcs: map[string]*liveSvc{}, archived: map[string]bool{}, baseDomain: true}
	} else if apitypes.EnvironmentKind(le.row.Kind) != kind {
		line(&ep.manual, apitypes.PlanChange{Kind: apitypes.PlanEnvironment, Action: apitypes.ChangeUpdate, Name: env, Old: le.row.Kind, New: string(kind),
			Note: "an environment's kind doesn't change; clone it into a new environment of that kind instead"})
	}

	given := func(service string) map[string]string {
		out := map[string]string{}
		for k, v := range values {
			if k.env == env && k.service == service {
				out[k.key] = v
			}
		}
		return out
	}
	if err := ep.planVars(line, "", fe.SharedValues, fe.SharedVariables, given(""), le.shared, le.sharedStaged, nil, prune); err != nil {
		return ep, err
	}
	ep.planDrains(line, env, fe.LogDrains, le.drains)

	inFile := map[string]bool{}
	for _, fs := range slices.SortedFunc(slices.Values(fe.Services), func(x, y apitypes.ServiceExport) int { return cmp.Compare(x.Name, y.Name) }) {
		inFile[fs.Name] = true
		if err := c.planService(&ep, line, fs, le, given(fs.Name), prune); err != nil {
			return ep, err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(le.svcs)) {
		ls := le.svcs[name]
		if inFile[name] {
			continue
		}
		ch := apitypes.PlanChange{Service: name, Kind: apitypes.PlanService, Action: apitypes.ChangeRemove, Name: name}
		switch {
		case prune && ls.row.Staged == "create":
			ch.Note = "discards its staged creation"
			line(&ep.changes, ch)
			ep.archive = append(ep.archive, name)
		case prune && ls.row.Staged == "archive":
			// Staged for archiving already.
		case prune:
			ch.Note = "archived (volumes kept); needs an admin to stage and to apply"
			line(&ep.changes, ch)
			ep.archive = append(ep.archive, name)
		default:
			ch.Note = "not in the file; kept (--prune archives it)"
			line(&ep.kept, ch)
		}
	}
	return ep, nil
}

// planService compares one file service with the live one, if any.
func (c *Core) planService(ep *envPlan, line func(*[]apitypes.PlanChange, apitypes.PlanChange), fs apitypes.ServiceExport, le *liveEnv, given map[string]string, prune bool) error {
	name := fs.Name
	if err := validName("service", name); err != nil {
		return err
	}
	if name == sharedScope || name == orgScope {
		return invalid("service name %q is reserved for variable references (${{%s.KEY}})", name, name)
	}
	var tmpl *dbTemplate
	if fs.Template != apitypes.TemplateNone {
		t, ok := templates[fs.Template]
		if !ok {
			return invalid("service %s: unknown template %q; the templates are %s", name, fs.Template, templateNames())
		}
		tmpl = &t
	}
	want := desiredSettings(fs, tmpl)
	if err := validateSettings(want, c.registryHost(), &c.pulls); err != nil {
		return invalid("service %s: %v", name, err)
	}
	svc := func(ch apitypes.PlanChange) apitypes.PlanChange { ch.Service = name; return ch }

	ls := le.svcs[name]
	if ls == nil && le.archived[name] {
		line(&ep.manual, svc(apitypes.PlanChange{Kind: apitypes.PlanService, Action: apitypes.ChangeAdd, Name: name,
			Note: "an archived service has this name; rename the service in the file"}))
		return nil
	}
	implied := map[string]bool{}
	if tmpl != nil {
		for k := range tmpl.vars(name, "") {
			implied[k] = true
		}
	}

	if ls == nil {
		line(&ep.changes, svc(apitypes.PlanChange{Kind: apitypes.PlanService, Action: apitypes.ChangeAdd, Name: name, New: serviceSummary(fs, want)}))
		if tmpl == nil {
			for _, ch := range settingChanges(name, apitypes.ServiceSettings{}, want) {
				line(&ep.changes, svc(apitypes.PlanChange{Kind: apitypes.PlanSetting, Action: apitypes.ChangeAdd, Name: ch.Field, New: ch.New}))
			}
		}
		ep.create = append(ep.create, fs)
		ep.staged++
		ls = &liveSvc{vars: map[string]string{}, staged: map[string]string{}, vols: map[string]store.Volume{}, stagedDomains: map[string]store.ListEnvironmentStagedDomainsRow{}}
		if tmpl != nil {
			// The template's volume, made with the service.
			ls.vols[templateVolume] = store.Volume{Name: templateVolume, MountPath: tmpl.mountPath}
		}
	} else {
		live := apitypes.Template(ls.row.Template)
		if live != fs.Template {
			line(&ep.manual, svc(apitypes.PlanChange{Kind: apitypes.PlanService, Action: apitypes.ChangeUpdate, Name: "template", Old: string(live), New: string(fs.Template),
				Note: "a service's template doesn't change; create a new service"}))
		}
		if ls.row.Staged == "archive" {
			line(&ep.changes, svc(apitypes.PlanChange{Kind: apitypes.PlanService, Action: apitypes.ChangeAdd, Name: name, Note: "drops its staged archive"}))
			ep.unarchive = append(ep.unarchive, name)
			ep.staged++
		}
		cur := settingsOf(ls.row)
		diff := settingChanges(name, cur, want)
		for _, ch := range diff {
			line(&ep.changes, svc(apitypes.PlanChange{Kind: apitypes.PlanSetting, Action: apitypes.ChangeUpdate, Name: ch.Field, Old: ch.Old, New: ch.New}))
		}
		switch {
		case len(diff) > 0:
			ep.settings[name] = &want
			ep.staged += len(diff)
		case ls.pending != nil:
			line(&ep.changes, svc(apitypes.PlanChange{Kind: apitypes.PlanSetting, Action: apitypes.ChangeUpdate, Name: "settings", Note: "drops its staged settings: the file matches the live ones"}))
			ep.settings[name] = nil
			ep.staged++
		}
		if tmpl == nil && (fs.Database != apitypes.DatabaseSetting(ls.row.Database) || fs.DatabaseURLVariable != ls.row.DatabaseUrlVariable) {
			line(&ep.manual, svc(apitypes.PlanChange{Kind: apitypes.PlanDatabase, Action: apitypes.ChangeUpdate, Name: "database",
				Old: dbSettingText(apitypes.DatabaseSetting(ls.row.Database), ls.row.DatabaseUrlVariable), New: dbSettingText(fs.Database, fs.DatabaseURLVariable),
				Note: fmt.Sprintf("data settings apply at once and aren't staged; change them with `dock db settings -s %s`", name)}))
		}
		if fs.Git != nil && (ls.git == nil || ls.git.Repo != fs.Git.Repository || ls.git.Branch != fs.Git.Branch || ls.git.Dir != fs.Git.Directory || ls.git.PostStatus != fs.Git.Statuses) {
			old := ""
			if ls.git != nil {
				old = gitText(ls.git.Repo, ls.git.Branch, ls.git.Dir)
			}
			line(&ep.manual, svc(apitypes.PlanChange{Kind: apitypes.PlanGit, Action: apitypes.ChangeUpdate, Name: "git", Old: old, New: gitText(fs.Git.Repository, fs.Git.Branch, fs.Git.Directory),
				Note: fmt.Sprintf("connect it with `dock git connect %s --branch %s -s %s`", fs.Git.Repository, fs.Git.Branch, name)}))
		}
	}
	if stopped := ls.row.StoppedAt != nil; fs.Stopped != stopped && (ls.row.ID != 0 || fs.Stopped) {
		ch := svc(apitypes.PlanChange{Kind: apitypes.PlanService, Action: apitypes.ChangeUpdate, Name: "stopped", Old: stoppedText(stopped), New: stoppedText(fs.Stopped)})
		verb := map[bool]string{true: "stop", false: "start"}[fs.Stopped]
		ch.Note = fmt.Sprintf("stopping and starting act at once and aren't staged; %s it with `dock service %s %s -e %s`", verb, verb, name, le.row.Name)
		if ls.row.ID == 0 {
			ch.Note = fmt.Sprintf("after applying, stop it with `dock service stop %s -e <environment>`", name)
		}
		line(&ep.manual, ch)
	}
	if ls.row.ID == 0 && fs.Git != nil {
		line(&ep.manual, svc(apitypes.PlanChange{Kind: apitypes.PlanGit, Action: apitypes.ChangeAdd, Name: "git", New: gitText(fs.Git.Repository, fs.Git.Branch, fs.Git.Directory),
			Note: fmt.Sprintf("after applying, connect it with `dock git connect %s --branch %s -s %s`", fs.Git.Repository, fs.Git.Branch, name)}))
	}

	if err := ep.planVars(line, name, fs.Values, fs.Variables, given, ls.vars, ls.staged, implied, prune); err != nil {
		return err
	}
	ep.planVolumes(line, name, fs, ls, tmpl)
	if err := ep.planDomains(line, name, fs, ls, le.baseDomain, prune); err != nil {
		return err
	}
	return ep.planJobs(line, name, fs, ls, prune, c.jobRunning)
}

// planVars compares one scope's variables (service "" for shared ones):
// values written in the file, names whose values are set separately
// (secrets, given ones sent with the file), and the live values. implied
// are a template's own variables, which the template makes and prune
// never removes.
func (ep *envPlan) planVars(line func(*[]apitypes.PlanChange, apitypes.PlanChange), service string, values map[string]string, names []string, given, live, staged map[string]string, implied map[string]bool, prune bool) error {
	ch := func(action apitypes.ChangeAction, key, note string) apitypes.PlanChange {
		return apitypes.PlanChange{Service: service, Kind: apitypes.PlanVariable, Action: action, Name: key, Note: note}
	}
	stage := func(key, value string, action apitypes.ChangeAction, note string) {
		line(&ep.changes, ch(action, key, note))
		ep.set = append(ep.set, varSet{service: service, key: key, value: value})
		ep.staged++
	}
	for _, k := range slices.Sorted(maps.Keys(values)) {
		if err := validKey(k); err != nil {
			return err
		}
		v, ok := live[k]
		switch {
		case !ok:
			stage(k, values[k], apitypes.ChangeAdd, "")
		case ep.blind || v != values[k]:
			stage(k, values[k], apitypes.ChangeUpdate, "")
		case staged[k] != "":
			stage(k, values[k], apitypes.ChangeUpdate, "drops a staged change: the file matches the live value")
		}
	}
	for _, k := range slices.Sorted(slices.Values(names)) {
		if err := validKey(k); err != nil {
			return err
		}
		v, live := live[k]
		g, sent := given[k]
		switch {
		case sent && !live:
			stage(k, g, apitypes.ChangeAdd, "value sent with the file")
		case sent && (ep.blind || v != g):
			stage(k, g, apitypes.ChangeUpdate, "value sent with the file")
		case live && staged[k] == "unset":
			line(&ep.changes, ch(apitypes.ChangeUpdate, k, "drops its staged removal"))
			ep.set = append(ep.set, varSet{service: service, key: k, keep: true})
			ep.staged++
		case live || staged[k] == "set" || implied[k] || sent:
		default:
			where := "-s " + service
			if service == "" {
				where = "--shared"
			}
			line(&ep.manual, ch(apitypes.ChangeAdd, k, fmt.Sprintf("needs a value: `dock env set %s %s=...` after applying the file", where, k)))
		}
	}
	for _, k := range slices.Sorted(maps.Keys(live)) {
		_, valued := values[k]
		if valued || slices.Contains(names, k) || implied[k] {
			continue
		}
		switch {
		case prune && staged[k] == "unset":
		case prune:
			line(&ep.changes, ch(apitypes.ChangeRemove, k, ""))
			ep.set = append(ep.set, varSet{service: service, key: k, unset: true})
			ep.staged++
		default:
			line(&ep.kept, ch(apitypes.ChangeRemove, k, "not in the file; kept (--prune removes it)"))
		}
	}
	return nil
}

// planVolumes compares a service's volumes. Volumes are never removed by a
// file, and a mount path never changes.
func (ep *envPlan) planVolumes(line func(*[]apitypes.PlanChange, apitypes.PlanChange), service string, fs apitypes.ServiceExport, ls *liveSvc, tmpl *dbTemplate) {
	inFile := map[string]bool{}
	for _, v := range slices.SortedFunc(slices.Values(fs.Volumes), func(x, y apitypes.VolumeExport) int { return cmp.Compare(x.Name, y.Name) }) {
		inFile[v.Name] = true
		ch := apitypes.PlanChange{Service: service, Kind: apitypes.PlanVolume, Name: v.Name, New: v.MountPath}
		if err := validName("volume", v.Name); err != nil {
			ch.Action, ch.Note = apitypes.ChangeAdd, err.Error()
			line(&ep.manual, ch)
			continue
		}
		mount := path.Clean(v.MountPath)
		if !path.IsAbs(mount) || mount == "/" {
			ch.Action, ch.Note = apitypes.ChangeAdd, "mount path must be an absolute path other than /"
			line(&ep.manual, ch)
			continue
		}
		cur, ok := ls.vols[v.Name]
		switch {
		case ok && cur.MountPath != mount:
			ch.Action, ch.Old, ch.Note = apitypes.ChangeUpdate, cur.MountPath, "a volume's mount path doesn't change; add a new volume"
			line(&ep.manual, ch)
		case ok:
		case slices.ContainsFunc(slices.Collect(maps.Values(ls.vols)), func(x store.Volume) bool { return x.MountPath == mount }):
			ch.Action, ch.Note = apitypes.ChangeAdd, "another volume of the service is mounted there"
			line(&ep.manual, ch)
		default:
			ch.Action = apitypes.ChangeAdd
			line(&ep.changes, ch)
			ep.volumes = append(ep.volumes, volAdd{service: service, name: v.Name, mount: mount})
			ep.staged++
		}
	}
	for _, name := range slices.Sorted(maps.Keys(ls.vols)) {
		if inFile[name] || (tmpl != nil && name == templateVolume) {
			continue
		}
		line(&ep.kept, apitypes.PlanChange{Service: service, Kind: apitypes.PlanVolume, Action: apitypes.ChangeRemove, Name: name, Old: ls.vols[name].MountPath,
			Note: "not in the file; kept: a project file never removes volumes"})
	}
}

// planDomains compares a service's domains. A generated domain in the file
// matches any generated domain of the service.
func (ep *envPlan) planDomains(line func(*[]apitypes.PlanChange, apitypes.PlanChange), service string, fs apitypes.ServiceExport, ls *liveSvc, baseDomain, prune bool) error {
	ch := func(action apitypes.ChangeAction, host, note string) apitypes.PlanChange {
		return apitypes.PlanChange{Service: service, Kind: apitypes.PlanDomain, Action: action, Name: host, Note: note}
	}
	stage := func(d domainStage) {
		ep.domains = append(ep.domains, d)
		if !d.cancel {
			ep.staged++
		}
	}
	liveGen := slices.ContainsFunc(ls.domains, func(d store.Domain) bool { return d.Generated })
	inFile := map[string]bool{}
	wantGen := false
	for _, d := range fs.Domains {
		if d.Generated {
			wantGen = true
			if d.Hostname != "" {
				inFile[d.Hostname] = true
			}
			continue
		}
		host, err := normalizeHostname(d.Hostname)
		if err != nil {
			return invalid("service %s: %v", service, err)
		}
		redirect := ""
		if d.RedirectTo != "" {
			if redirect, err = normalizeHostname(d.RedirectTo); err != nil {
				return invalid("service %s: %v", service, err)
			}
		}
		inFile[host] = true
		i := slices.IndexFunc(ls.domains, func(x store.Domain) bool { return x.Hostname == host })
		staged, isStaged := ls.stagedDomains[host]
		switch {
		case i >= 0 && ls.domains[i].RedirectTo != redirect:
			c := ch(apitypes.ChangeUpdate, host, "a domain's redirect doesn't change in place; remove the domain and add it again")
			c.Old, c.New = ls.domains[i].RedirectTo, redirect
			line(&ep.manual, c)
		case i >= 0 && isStaged:
			line(&ep.changes, ch(apitypes.ChangeUpdate, host, "drops its staged removal"))
			stage(domainStage{service: service, host: host, cancel: true})
		case i >= 0:
		default:
			c := ch(apitypes.ChangeAdd, host, "")
			if redirect != "" {
				c.New = "redirects to " + redirect
			}
			line(&ep.changes, c)
			if !isStaged || staged.Action != "add" || staged.RedirectTo != redirect {
				stage(domainStage{service: service, host: host, redirect: redirect})
			} else {
				ep.staged++
			}
		}
	}
	if wantGen && !liveGen {
		switch {
		case !baseDomain:
			line(&ep.manual, ch(apitypes.ChangeAdd, "", "a generated domain needs a base domain on the environment's server: `dock server set <server> --base-domain <domain>`, or name the hostname in the file"))
		default:
			c := ch(apitypes.ChangeAdd, "", "generated")
			line(&ep.changes, c)
			stage(domainStage{service: service, generated: true})
		}
	}
	for _, d := range ls.domains {
		if inFile[d.Hostname] || (wantGen && d.Generated) {
			continue
		}
		switch _, isStaged := ls.stagedDomains[d.Hostname]; {
		case prune && isStaged:
		case prune:
			line(&ep.changes, ch(apitypes.ChangeRemove, d.Hostname, ""))
			stage(domainStage{service: service, host: d.Hostname, remove: true})
		default:
			line(&ep.kept, ch(apitypes.ChangeRemove, d.Hostname, "not in the file; kept (--prune removes it)"))
		}
	}
	return nil
}

// desiredSettings are the settings a file service asks for: what the file
// sets (a left-out field is zero), with the template's values where a
// template service leaves a field out. A new setting goes here, in
// ServiceExport and in serviceExport, and project files cover it.
func desiredSettings(fs apitypes.ServiceExport, tmpl *dbTemplate) apitypes.ServiceSettings {
	s := apitypes.ServiceSettings{
		Image: fs.Image, Port: fs.Port, HealthCheckPath: fs.HealthCheckPath, HealthCheckTimeoutSeconds: fs.HealthCheckTimeoutSeconds,
		StartCommand: fs.StartCommand, DockerfilePath: fs.DockerfilePath, BuildCommand: fs.BuildCommand,
		MemoryMB: fs.MemoryMB, CPUs: fs.CPUs,
		HealthCheckIntervalSeconds: fs.HealthCheckIntervalSeconds, HealthCheckProbeTimeoutSeconds: fs.HealthCheckProbeTimeoutSeconds,
		HealthCheckRetries: fs.HealthCheckRetries, HealthCheckStartPeriodSeconds: fs.HealthCheckStartPeriodSeconds,
		PreDeployCommand: fs.PreDeployCommand, PreDeployTimeoutSeconds: fs.PreDeployTimeoutSeconds,
	}
	if tmpl != nil {
		s.Image = cmp.Or(s.Image, tmpl.image)
		s.StartCommand = cmp.Or(s.StartCommand, tmpl.cmd)
		s.MemoryMB = cmp.Or(s.MemoryMB, tmpl.memoryMB)
	}
	return s
}

// serviceSummary describes a new service in one phrase.
func serviceSummary(fs apitypes.ServiceExport, s apitypes.ServiceSettings) string {
	switch {
	case fs.Template != apitypes.TemplateNone:
		return "template " + string(fs.Template)
	case s.Image != "":
		return "image " + s.Image
	case s.DockerfilePath != "":
		return "built from source with " + s.DockerfilePath
	default:
		return "built from source"
	}
}

func dbSettingText(d apitypes.DatabaseSetting, urlVar string) string {
	parts := []string{}
	if d != "" {
		parts = append(parts, string(d))
	}
	if urlVar != "" {
		parts = append(parts, "url from "+urlVar)
	}
	return strings.Join(parts, ", ")
}

func gitText(repo, branch, dir string) string {
	s := repo + "@" + branch
	if dir != "" {
		s += " in " + dir
	}
	return s
}

// stageEnv stages an environment's plan in the caller's transaction.
func (c *Core) stageEnv(ctx context.Context, q *store.Queries, es envScope, le *liveEnv, ep envPlan) error {
	byName := map[string]store.Service{}
	for name, ls := range le.svcs {
		byName[name] = ls.row
	}
	for _, fs := range ep.create {
		s, err := c.createStagedService(ctx, q, es, fs)
		if err != nil {
			return err
		}
		byName[fs.Name] = s
	}
	for _, name := range ep.unarchive {
		if err := q.SetServiceStaged(ctx, store.SetServiceStagedParams{Staged: "", ID: byName[name].ID}); err != nil {
			return err
		}
	}
	for _, name := range ep.archive {
		s := byName[name]
		if s.Staged == "create" {
			if err := discardStagedService(ctx, q, es.env.ID, s.ID); err != nil {
				return err
			}
			continue
		}
		if err := q.SetServiceStaged(ctx, store.SetServiceStagedParams{Staged: "archive", ID: s.ID}); err != nil {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(ep.settings)) {
		var staged *string
		if next := ep.settings[name]; next != nil {
			b, err := json.Marshal(next)
			if err != nil {
				return err
			}
			v := string(b)
			staged = &v
		}
		if err := q.SetPendingSettings(ctx, store.SetPendingSettingsParams{PendingSettings: staged, ID: byName[name].ID}); err != nil {
			return err
		}
	}
	for _, v := range ep.set {
		var owner *int64
		if v.service != "" {
			id := byName[v.service].ID
			owner = &id
		}
		var err error
		switch {
		case v.unset:
			err = stageUnset(ctx, q, es.env.ID, owner, v.key)
		case v.keep:
			var cur store.Variable
			if cur, err = q.GetVariable(ctx, store.GetVariableParams{EnvironmentID: es.env.ID, ServiceID: owner, Key: v.key}); err == nil {
				err = discardVariable(ctx, q, cur)
			}
		default:
			err = c.stageSet(ctx, q, es.env.ID, owner, v.key, v.value)
		}
		if err != nil {
			return err
		}
	}
	for _, v := range ep.volumes {
		s := byName[v.service]
		if _, err := q.CreateVolume(ctx, store.CreateVolumeParams{
			ServiceID: s.ID, Name: v.name, MountPath: v.mount, Pending: true, DockerName: volumeName(svcScope{es, s}, v.name),
		}); err != nil {
			if store.IsUnique(err) {
				return conflict("service %s already has a volume named %s or mounted at %s", v.service, v.name, v.mount)
			}
			return err
		}
	}
	for _, d := range ep.domains {
		s := byName[d.service]
		if d.cancel {
			if staged, ok := le.svcs[d.service].stagedDomains[d.host]; ok {
				if err := q.DeleteStagedDomain(ctx, staged.ID); err != nil {
					return err
				}
			}
			continue
		}
		action := "add"
		if d.remove {
			action = "remove"
		}
		if err := q.InsertStagedDomain(ctx, store.InsertStagedDomainParams{
			ServiceID: s.ID, Hostname: d.host, Action: action, Generated: d.generated, RedirectTo: d.redirect,
		}); err != nil {
			return err
		}
	}
	return nil
}

// createStagedService creates a file's new service, staged: the row with
// its settings and data settings, flagged staged, and for a template its
// volume and generated variables (staged), as createFromTemplate makes
// them.
func (c *Core) createStagedService(ctx context.Context, q *store.Queries, es envScope, fs apitypes.ServiceExport) (store.Service, error) {
	var tmpl *dbTemplate
	if t, ok := templates[fs.Template]; ok {
		tmpl = &t
	}
	set := desiredSettings(fs, tmpl)
	s, err := q.CreateService(ctx, store.CreateServiceParams{
		EnvironmentID: es.env.ID, Name: fs.Name, Image: set.Image, Port: int64(set.Port),
		HealthCheckPath: set.HealthCheckPath, HealthCheckTimeoutSeconds: int64(set.HealthCheckTimeoutSeconds),
		StartCommand: set.StartCommand, DockerfilePath: set.DockerfilePath, BuildCommand: set.BuildCommand,
		MemoryMb: int64(set.MemoryMB), Cpus: set.CPUs,
		HealthCheckIntervalSeconds: int64(set.HealthCheckIntervalSeconds), HealthCheckProbeTimeoutSeconds: int64(set.HealthCheckProbeTimeoutSeconds),
		HealthCheckRetries: int64(set.HealthCheckRetries), HealthCheckStartPeriodSeconds: int64(set.HealthCheckStartPeriodSeconds),
		PreDeployCommand: set.PreDeployCommand, PreDeployTimeoutSeconds: int64(set.PreDeployTimeoutSeconds),
	})
	if store.IsUnique(err) {
		return s, conflict("service %q already exists in %s", fs.Name, es)
	}
	if err != nil {
		return s, err
	}
	if err := q.SetServiceStaged(ctx, store.SetServiceStagedParams{Staged: "create", ID: s.ID}); err != nil {
		return s, err
	}
	s.Staged = "create"
	database, urlVar := fs.Database, fs.DatabaseURLVariable
	if tmpl != nil {
		if err := q.SetServiceTemplate(ctx, store.SetServiceTemplateParams{Template: string(tmpl.name), ID: s.ID}); err != nil {
			return s, err
		}
		s.Template = string(tmpl.name)
		database, urlVar = tmpl.database, ""
		if _, err := q.CreateVolume(ctx, store.CreateVolumeParams{
			ServiceID: s.ID, Name: templateVolume, MountPath: tmpl.mountPath, Pending: true, DockerName: volumeName(svcScope{es, s}, templateVolume),
		}); err != nil {
			return s, err
		}
		vars := tmpl.vars(fs.Name, newPassword())
		for _, k := range slices.Sorted(maps.Keys(vars)) {
			if err := c.stageSet(ctx, q, es.env.ID, &s.ID, k, vars[k]); err != nil {
				return s, err
			}
		}
	}
	if database != "" || urlVar != "" {
		if s, err = q.SetServiceDatabaseSettings(ctx, store.SetServiceDatabaseSettingsParams{Database: string(database), DatabaseUrlVariable: urlVar, ID: s.ID}); err != nil {
			return s, err
		}
	}
	return s, nil
}

// discardStagedService deletes a service whose creation is staged, with
// everything staged on it. It never ran, so nothing else refers to it.
func discardStagedService(ctx context.Context, q *store.Queries, envID, id int64) error {
	if err := q.DeleteServiceVariables(ctx, store.DeleteServiceVariablesParams{EnvironmentID: envID, ServiceID: &id}); err != nil {
		return err
	}
	if err := q.DeleteServiceStagedVolumes(ctx, id); err != nil {
		return err
	}
	if err := q.DeleteServiceStagedDomains(ctx, id); err != nil {
		return err
	}
	return q.DeleteStagedService(ctx, id)
}

// firstDeployOrder orders the services an apply created that can start
// from an image: templates first, then each after the services it depends
// on (depends_on from the file, when the apply came with one), then by
// name. Dock starts them in this order without waiting for one to be
// healthy before the next.
func firstDeployOrder(svcs []store.Service, deps map[string][]string) []store.Service {
	slices.SortFunc(svcs, func(x, y store.Service) int {
		return cmp.Or(cmp.Compare(boolCount(x.Template == ""), boolCount(y.Template == "")), cmp.Compare(x.Name, y.Name))
	})
	var out []store.Service
	done := map[string]bool{}
	var visit func(s store.Service, depth int)
	byName := map[string]store.Service{}
	for _, s := range svcs {
		byName[s.Name] = s
	}
	visit = func(s store.Service, depth int) {
		if done[s.Name] || depth > len(svcs) {
			return
		}
		done[s.Name] = true
		for _, d := range deps[s.Name] {
			if dep, ok := byName[d]; ok {
				visit(dep, depth+1)
			}
		}
		out = append(out, s)
	}
	for _, s := range svcs {
		visit(s, 0)
	}
	return out
}

// stagedSummary describes a staged new service for the changes view.
func stagedSummary(s store.Service) string {
	return serviceSummary(apitypes.ServiceExport{Template: apitypes.Template(s.Template)}, settingsOf(s))
}

// stoppedText names a service's stopped setting in a plan.
func stoppedText(stopped bool) string {
	if stopped {
		return "stopped"
	}
	return "running"
}

// planDrains compares an environment's log drains by name. A drain's URL
// holds its credentials, so a file names its kind and target only and
// differences are for a person to make (`dock logs drain`).
func (ep *envPlan) planDrains(line func(*[]apitypes.PlanChange, apitypes.PlanChange), env string, want []apitypes.LogDrainExport, live map[string]store.LogDrain) {
	inFile := map[string]bool{}
	for _, d := range want {
		inFile[d.Name] = true
		ch := apitypes.PlanChange{Kind: apitypes.PlanLogDrain, Name: d.Name, New: string(d.Kind) + " " + d.Target}
		cur, ok := live[d.Name]
		switch {
		case !ok:
			ch.Action = apitypes.ChangeAdd
			ch.Note = fmt.Sprintf("its URL holds credentials, so add it yourself: `dock logs drain add %s <url> --name %s -e %s`", d.Kind, d.Name, env)
			line(&ep.manual, ch)
		case string(d.Kind) != cur.Kind || d.Target != cur.Target:
			ch.Action, ch.Old = apitypes.ChangeUpdate, cur.Kind+" "+cur.Target
			ch.Note = fmt.Sprintf("remove it and add it again with the new URL: `dock logs drain remove %s -e %s`, then `dock logs drain add`", d.Name, env)
			line(&ep.manual, ch)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(live)) {
		if !inFile[name] {
			d := live[name]
			line(&ep.kept, apitypes.PlanChange{Kind: apitypes.PlanLogDrain, Action: apitypes.ChangeRemove, Name: name, Old: d.Kind + " " + d.Target,
				Note: "not in the file; kept (a file never removes log drains: `dock logs drain remove`)"})
		}
	}
}

// planRegistry checks the org has a credential for each registry host the
// file names; a missing one is for an admin to add (`dock registry login`).
func (c *Core) planRegistry(ctx context.Context, a Actor, hosts []string) ([]apitypes.PlanChange, error) {
	if len(hosts) == 0 {
		return nil, nil
	}
	creds, err := c.store.ListRegistryCredentials(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	var out []apitypes.PlanChange
	for _, h := range slices.Sorted(slices.Values(hosts)) {
		host := normalizeRegistryHost(strings.TrimSpace(h))
		if slices.ContainsFunc(creds, func(cr store.RegistryCredential) bool { return cr.Host == host }) {
			continue
		}
		out = append(out, apitypes.PlanChange{Kind: apitypes.PlanRegistry, Action: apitypes.ChangeAdd, Name: host,
			Note: fmt.Sprintf("images pull from %s with a credential this organization doesn't have; an admin adds it with `dock registry login %s --username <name> --password-stdin`", host, host)})
	}
	return out, nil
}
