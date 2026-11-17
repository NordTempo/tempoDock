package core

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/apptemplates"
	"github.com/NordTempo/tempoDock/internal/store"
)

// App templates (ADR 0083). A template expands into the services of one
// environment as a project file describes them; using it plans that with
// planEnv, stages it with stageEnv and makes only its services live at
// once, the way a direct `dock service create` is live. It never touches
// other staged changes in the environment. Domains are added before the
// first deploys, so DOCK_PUBLIC_DOMAIN is set in the first release.

// appTemplate returns a template by name: the catalog's, one made from a
// database template, or the organization's own (org is then true).
func (c *Core) appTemplate(ctx context.Context, orgID int64, name string) (t apptemplates.Template, org bool, err error) {
	if t, ok := builtinTemplate(name); ok {
		return t, false, nil
	}
	row, err := c.store.GetOrgTemplate(ctx, store.GetOrgTemplateParams{OrgID: orgID, Name: name})
	if store.IsNotFound(err) {
		return t, false, notFound("there's no template %q; list them with `dock template list`", name)
	}
	if err != nil {
		return t, false, err
	}
	t, err = apptemplates.Parse([]byte(row.Body))
	if err != nil {
		return t, true, invalid("the organization's template %s doesn't parse: %v. Save it again with `dock template save`", name, err)
	}
	return t, true, nil
}

// builtinTemplate is a template that comes with Dock: the catalog's, or
// one made from a database template.
func builtinTemplate(name string) (apptemplates.Template, bool) {
	cat, err := apptemplates.Catalog()
	if err != nil {
		return apptemplates.Template{}, false // the catalog test keeps it parsing
	}
	if t, ok := cat[name]; ok {
		return t, true
	}
	if t, ok := templates[apitypes.Template(name)]; ok {
		return dbAppTemplate(t), true
	}
	return apptemplates.Template{}, false
}

// dbAppTemplate is a database template as a one-service app template, so
// the catalog lists databases too.
func dbAppTemplate(t dbTemplate) apptemplates.Template {
	name := string(t.name)
	return apptemplates.Template{
		Name: name, Title: t.title, Summary: t.description, Category: "databases", License: t.license,
		Revision: 1, Main: name, Mark: apptemplates.Mark(name),
		Services: []apptemplates.Service{{ServiceExport: apitypes.ServiceExport{Name: name, Template: t.name}}},
	}
}

// appTemplateInfo describes a template, with the database templates'
// images and memory filled in.
func appTemplateInfo(t apptemplates.Template) apitypes.AppTemplate {
	info := t.Info()
	for i, s := range info.Services {
		if db, ok := templates[s.Template]; ok {
			info.Services[i].Image = cmp.Or(s.Image, db.image)
			info.Services[i].MemoryMB = cmp.Or(s.MemoryMB, db.memoryMB)
			info.Services[i].Volumes = append(info.Services[i].Volumes, db.mountPath)
		}
		info.MemoryMB += info.Services[i].MemoryMB
	}
	return info
}

// ListAppTemplates lists the app templates: the organization's own, then
// Dock's apps by title, then the databases. Any member may list them.
func (c *Core) ListAppTemplates(ctx context.Context, a Actor) ([]apitypes.AppTemplate, error) {
	cat, err := apptemplates.Catalog()
	if err != nil {
		return nil, err
	}
	out, err := c.orgTemplates(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	own := len(out)
	for _, t := range cat {
		out = append(out, appTemplateInfo(t))
	}
	slices.SortFunc(out[own:], func(x, y apitypes.AppTemplate) int {
		return cmp.Compare(strings.ToLower(x.Title), strings.ToLower(y.Title))
	})
	for _, name := range slices.Sorted(maps.Keys(templates)) {
		out = append(out, appTemplateInfo(dbAppTemplate(templates[name])))
	}
	return out, nil
}

// GetAppTemplate describes one app template.
func (c *Core) GetAppTemplate(ctx context.Context, a Actor, name string) (apitypes.AppTemplate, error) {
	t, org, err := c.appTemplate(ctx, a.OrgID, name)
	if err != nil {
		return apitypes.AppTemplate{}, err
	}
	info := appTemplateInfo(t)
	info.Org = org
	return info, nil
}

// UseAppTemplate creates a template's services in an environment, or with
// req.Plan says what it would create. Needs deploy (read to plan), like
// creating a service. Every service it names must be new: a name that is
// taken is refused with the --name that avoids it, and a hostname
// another service has with the --domain that does. After the services
// exist, a domain or first deploy that fails is listed in Failed rather
// than undoing them; they deploy like any other service once it's fixed.
func (c *Core) UseAppTemplate(ctx context.Context, a Actor, ref EnvRef, name string, req apitypes.UseTemplateRequest) (apitypes.UseTemplateResult, error) {
	out := apitypes.UseTemplateResult{Plan: newPlan(ref.Project), Services: []string{}, Releases: []apitypes.Release{}, Failed: []string{}}
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeDeploy, "using a template")
	if req.Plan {
		es, err = c.envFor(ctx, a, ref, apitypes.ScopeRead, "using a template")
	}
	if err != nil {
		return out, err
	}
	if err := notArchived(es.project); err != nil {
		return out, err
	}
	t, _, err := c.appTemplate(ctx, es.project.OrgID, name)
	if err != nil {
		return out, err
	}
	ex, err := t.Expand(es.env.Name, apptemplates.Use{Name: req.Name, Domain: strings.TrimSpace(req.Domain), Inputs: req.Inputs})
	if err != nil {
		return out, invalid("%v", err)
	}
	fe := apitypes.EnvironmentExport{Name: es.env.Name, Kind: apitypes.EnvironmentKind(es.env.Kind), Services: ex.Services}
	values := map[varKey]string{}
	deps := map[string][]string{}
	for _, v := range ex.Values {
		values[varKey{v.Environment, v.Service, v.Key}] = v.Value
	}
	for _, s := range ex.Services {
		out.Services = append(out.Services, s.Name)
		deps[s.Name] = s.DependsOn
	}
	plan := func(q *store.Queries) (*liveEnv, envPlan, error) {
		le, err := loadLiveEnv(ctx, q, c, es.env)
		if err != nil {
			return nil, envPlan{}, err
		}
		for _, s := range ex.Services {
			if le.svcs[s.Name] != nil || le.archived[s.Name] {
				return nil, envPlan{}, conflict("%s already has a service named %s; use the template under another name (--name)", es, s.Name)
			}
		}
		ep, err := c.planEnv(a, fe, le, values, false)
		// A template only adds: the environment's other services aren't
		// "kept", they are just not its concern.
		ep.kept = nil
		for _, m := range ep.manual {
			// The template's references to its domain would break the
			// first deploy.
			switch {
			case m.Kind == apitypes.PlanDomain && m.Name == "":
				return nil, ep, invalid("%s's server has no base domain to generate a hostname for %s from; give it a hostname (--domain)", es, ex.Main)
			case m.Kind == apitypes.PlanDomain:
				return nil, ep, errf(apitypes.CodeHostnameTaken, "%s; give %s another hostname with --domain", strings.TrimSuffix(m.Note, "; use another hostname"), ex.Main)
			}
		}
		return le, ep, err
	}
	if req.Plan {
		_, ep, err := plan(c.store.Queries)
		if err != nil {
			return out, err
		}
		addPlan(&out.Plan, ep)
		file, err := apitypes.ProjectExport{Version: apitypes.ExportVersion, Project: ref.Project, Environments: []apitypes.EnvironmentExport{fe}}.TOMLWith(
			fmt.Sprintf("The %s template (revision %d) for %s, as a project file.\n", t.Title, t.Revision, es) +
				"Its generated secrets are listed in variables without values: set them before deploying.\n" +
				"Plan it with `dock apply FILE --plan`.\n")
		out.File = string(file)
		return out, err
	}

	var created []store.Service
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		created = nil
		le, ep, err := plan(q)
		if err != nil {
			return err
		}
		out.Plan = newPlan(ref.Project)
		addPlan(&out.Plan, ep)
		if err := c.stageEnv(ctx, q, es, le, ep); err != nil {
			return err
		}
		svcs, err := q.ListServices(ctx, es.env.ID)
		if err != nil {
			return err
		}
		for _, s := range svcs {
			if !slices.Contains(out.Services, s.Name) || s.Staged != "create" {
				continue
			}
			// Live at once, with only its own staged changes.
			if _, err := applyPending(ctx, q, es.env.ID, &s.ID); err != nil {
				return err
			}
			if err := q.SetServiceFromTemplate(ctx, store.SetServiceFromTemplateParams{FromTemplate: t.Name, FromTemplateRevision: int64(t.Revision), ID: s.ID}); err != nil {
				return err
			}
			s.Staged, s.FromTemplate, s.FromTemplateRevision = "", t.Name, int64(t.Revision)
			created = append(created, s)
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	c.audit(ctx, a, "template.use", &es.project, &es.env, es.String()+"/"+ex.Main,
		fmt.Sprintf("created %s from the %s template (revision %d)", strings.Join(out.Services, ", "), t.Title, t.Revision))
	// Jobs are set on live services, so after they exist (jobs_apply.go).
	chs, err := c.applyCreatedJobs(ctx, a, es, fe, false)
	if err != nil {
		out.Failed = append(out.Failed, fmt.Sprintf("couldn't set the scheduled jobs: %v. Set them with `dock job set`", err))
	}
	out.Plan.Changes = append(out.Plan.Changes, chs...)

	domains, err := c.store.ListEnvironmentStagedDomains(ctx, es.env.ID)
	if err != nil {
		return out, err
	}
	ids := map[int64]bool{}
	for _, s := range created {
		ids[s.ID] = true
	}
	domains = slices.DeleteFunc(domains, func(d store.ListEnvironmentStagedDomainsRow) bool { return !ids[d.ServiceID] })
	var applied apitypes.AppliedChanges
	c.applyStagedDomains(ctx, a, es, domains, &applied)
	out.Failed = append(out.Failed, applied.Failed...)

	rels, failed := c.deployInOrder(ctx, a, es, created, deps)
	out.Releases = append(out.Releases, rels...)
	out.Failed = append(out.Failed, failed...)
	return out, nil
}

// deployInOrder starts the first releases of a template's new services,
// each once the services it depends on finished theirs: an app whose
// database doesn't accept connections yet often exits at start, and its
// deploy's health check fails on that. One whose dependency failed still
// starts, and its release says why. The sequence runs on the daemon's
// context; the caller waits for it unless its own context ends first.
func (c *Core) deployInOrder(ctx context.Context, a Actor, es envScope, svcs []store.Service, deps map[string][]string) ([]apitypes.Release, []string) {
	type result struct {
		rels   []apitypes.Release
		failed []string
	}
	res := make(chan result, 1)
	c.wg.Go(func() {
		var r result
		defer func() { res <- r }()
		// finished holds each started service's outcome, put back after
		// every read so later dependents read it too.
		finished := map[string]chan bool{}
		for _, s := range firstDeployOrder(svcs, deps) {
			if s.Image == "" && s.Template == "" {
				continue
			}
			for _, d := range deps[s.Name] {
				if ch := finished[d]; ch != nil {
					select {
					case ok := <-ch:
						ch <- ok
					case <-c.ctx.Done():
						return
					}
				}
			}
			ss := svcScope{es, s}
			ch := make(chan bool, 1)
			rel, err := c.startRelease(c.ctx, a, ss, releaseSpec{source: apitypes.SourceImage, done: func(ok bool) { ch <- ok }})
			if err != nil {
				r.failed = append(r.failed, fmt.Sprintf("couldn't deploy %s: %v. Deploy it once that's fixed: dock deploy -s %s -e %s", s.Name, err, s.Name, es.env.Name))
				continue
			}
			finished[s.Name] = ch
			c.audit(c.ctx, a, "release.deploy", &ss.project, &ss.env, ss.String(), fmt.Sprintf("release %d from image %s", rel.Number, rel.ImageRef))
			if rr, err := c.toRelease(c.ctx, ss, rel, false); err == nil {
				r.rels = append(r.rels, rr)
			}
		}
	})
	select {
	case r := <-res:
		return r.rels, r.failed
	case <-ctx.Done():
		return nil, []string{"stopped waiting while the services deploy in order; follow them with `dock status -e " + es.env.Name + "`"}
	}
}
