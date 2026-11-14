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

// App templates (ADR 0082). A template expands into the services of one
// environment as a project file describes them; using it plans that with
// planEnv, stages it with stageEnv and makes only its services live at
// once, the way a direct `dock service create` is live. It never touches
// other staged changes in the environment. Domains are added before the
// first deploys, so DOCK_PUBLIC_DOMAIN is set in the first release.

// appTemplate returns a template by name: the catalog's, or one made from
// a database template.
func (c *Core) appTemplate(name string) (apptemplates.Template, error) {
	cat, err := apptemplates.Catalog()
	if err != nil {
		return apptemplates.Template{}, err
	}
	if t, ok := cat[name]; ok {
		return t, nil
	}
	if t, ok := templates[apitypes.Template(name)]; ok {
		return dbAppTemplate(t), nil
	}
	return apptemplates.Template{}, notFound("there's no template %q; list them with `dock template list`", name)
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

// ListAppTemplates lists the app templates, apps by title and then the
// databases. They are the same for everyone, so any caller may list them.
func (c *Core) ListAppTemplates(context.Context, Actor) ([]apitypes.AppTemplate, error) {
	cat, err := apptemplates.Catalog()
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.AppTemplate, 0, len(cat)+len(templates))
	for _, t := range cat {
		out = append(out, appTemplateInfo(t))
	}
	slices.SortFunc(out, func(x, y apitypes.AppTemplate) int {
		return cmp.Compare(strings.ToLower(x.Title), strings.ToLower(y.Title))
	})
	for _, name := range slices.Sorted(maps.Keys(templates)) {
		out = append(out, appTemplateInfo(dbAppTemplate(templates[name])))
	}
	return out, nil
}

// GetAppTemplate describes one app template.
func (c *Core) GetAppTemplate(_ context.Context, _ Actor, name string) (apitypes.AppTemplate, error) {
	t, err := c.appTemplate(name)
	if err != nil {
		return apitypes.AppTemplate{}, err
	}
	return appTemplateInfo(t), nil
}

// UseAppTemplate creates a template's services in an environment, or with
// req.Plan says what it would create. Needs deploy (read to plan), like
// creating a service. Every service it names must be new: a name that is
// taken is refused with the --name that avoids it. After the services
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
	t, err := c.appTemplate(name)
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
			if m.Kind == apitypes.PlanDomain && m.Name == "" {
				// The template's references to its domain would break the
				// first deploy.
				return nil, ep, invalid("%s's server has no base domain to generate a hostname for %s from; give it a hostname (--domain)", es, ex.Main)
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

	for _, s := range firstDeployOrder(created, deps) {
		if s.Image == "" && s.Template == "" {
			continue
		}
		ss := svcScope{es, s}
		rel, err := c.startRelease(ctx, a, ss, releaseSpec{source: apitypes.SourceImage})
		if err != nil {
			out.Failed = append(out.Failed, fmt.Sprintf("couldn't deploy %s: %v. Deploy it once that's fixed: dock deploy -s %s -e %s", s.Name, err, s.Name, es.env.Name))
			continue
		}
		c.audit(ctx, a, "release.deploy", &ss.project, &ss.env, ss.String(), fmt.Sprintf("release %d from image %s", rel.Number, rel.ImageRef))
		r, err := c.toRelease(ctx, ss, rel, false)
		if err != nil {
			return out, err
		}
		out.Releases = append(out.Releases, r)
	}
	return out, nil
}
