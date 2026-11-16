package core

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/apptemplates"
	"github.com/NordTempo/tempoDock/internal/store"
)

// An organization's own templates (ADR 0083): files in the catalog's
// format, saved by its admins from a file or from an environment's
// services, listed and used by every member like Dock's own. Their names
// can't be those of Dock's templates.

// orgTemplates describes an organization's templates. One that no longer
// parses (saved by another Dock version) is left out of the list; using
// it says why.
func (c *Core) orgTemplates(ctx context.Context, orgID int64) ([]apitypes.AppTemplate, error) {
	rows, err := c.store.ListOrgTemplates(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.AppTemplate, 0, len(rows))
	for _, r := range rows {
		t, err := apptemplates.Parse([]byte(r.Body))
		if err != nil {
			continue
		}
		info := appTemplateInfo(t)
		info.Org = true
		out = append(out, info)
	}
	return out, nil
}

// SaveAppTemplate saves an organization template from its file, replacing
// one of the same name. Needs admin.
func (c *Core) SaveAppTemplate(ctx context.Context, a Actor, req apitypes.SaveTemplateRequest) (apitypes.AppTemplate, error) {
	if err := a.require(apitypes.ScopeAdmin, "saving an organization template"); err != nil {
		return apitypes.AppTemplate{}, err
	}
	t, err := apptemplates.Parse([]byte(req.File))
	if err != nil {
		return apitypes.AppTemplate{}, invalid("the template file %v", err)
	}
	return c.saveOrgTemplate(ctx, a, t, req.File)
}

// SaveAppTemplateFrom saves services of an environment as an organization
// template: their settings, volumes, jobs and references between them.
// Variables whose values aren't references become inputs the form asks
// for, as secrets: a template never carries a value someone set. Database
// templates stay templates and make their own credentials. Needs admin,
// and read on the environment.
func (c *Core) SaveAppTemplateFrom(ctx context.Context, a Actor, ref EnvRef, req apitypes.SaveTemplateFromRequest) (apitypes.AppTemplate, error) {
	if err := a.require(apitypes.ScopeAdmin, "saving an organization template"); err != nil {
		return apitypes.AppTemplate{}, err
	}
	if _, err := c.envFor(ctx, a, ref, apitypes.ScopeRead, "saving an organization template"); err != nil {
		return apitypes.AppTemplate{}, err
	}
	ex, err := c.ExportProject(ctx, a, ref.Project)
	if err != nil {
		return apitypes.AppTemplate{}, err
	}
	i := slices.IndexFunc(ex.Environments, func(e apitypes.EnvironmentExport) bool { return e.Name == ref.Environment })
	if i < 0 {
		return apitypes.AppTemplate{}, notFound("environment %s has nothing to save", ref.Environment)
	}
	t, err := templateFrom(ex.Environments[i], ref, req)
	if err != nil {
		return apitypes.AppTemplate{}, err
	}
	file, err := t.TOML()
	if err != nil {
		return apitypes.AppTemplate{}, err
	}
	// Parsed back, so it is checked exactly as an uploaded file is.
	if t, err = apptemplates.Parse(file); err != nil {
		return apitypes.AppTemplate{}, invalid("these services don't make a template: %v", err)
	}
	return c.saveOrgTemplate(ctx, a, t, string(file))
}

// templateFrom builds a template from an exported environment's services.
func templateFrom(fe apitypes.EnvironmentExport, ref EnvRef, req apitypes.SaveTemplateFromRequest) (apptemplates.Template, error) {
	t := apptemplates.Template{
		Name: req.Name, Title: cmp.Or(strings.TrimSpace(req.Title), req.Name),
		Summary:  cmp.Or(strings.TrimSpace(req.Summary), fmt.Sprintf("Saved from %s/%s", ref.Project, ref.Environment)),
		Category: "organization", License: "the organization's", Revision: 1,
	}
	pick := req.Services
	if len(pick) == 0 {
		for _, s := range fe.Services {
			if s.Image != "" || s.Template != apitypes.TemplateNone {
				pick = append(pick, s.Name)
			}
		}
	}
	if len(pick) == 0 {
		return t, invalid("%s has no services that run an image or a database template; a template can't build from source", ref)
	}
	for _, name := range pick {
		i := slices.IndexFunc(fe.Services, func(s apitypes.ServiceExport) bool { return s.Name == name })
		if i < 0 {
			return t, notFound("%s has no service %s", ref, name)
		}
		s := fe.Services[i]
		if s.Image == "" && s.Template == apitypes.TemplateNone {
			return t, invalid("%s builds from source, and a template runs images: deploy it from an image, or leave it out (services)", name)
		}
		if len(s.Domains) > 0 && t.Main == "" {
			t.Main = name
		}
		svc := apptemplates.Service{ServiceExport: s}
		svc.Domains, svc.Git, svc.Stopped, svc.Variables = nil, nil, false, nil
		if s.Template == apitypes.TemplateNone {
			for _, k := range s.Variables {
				t.Inputs = append(t.Inputs, apitypes.TemplateInput{Key: k, Service: name, Label: k, Kind: apitypes.InputSecret})
			}
		}
		t.Services = append(t.Services, svc)
	}
	t.Main = cmp.Or(req.Main, t.Main, pick[0])
	if !slices.Contains(pick, t.Main) {
		return t, invalid("main is %s, which isn't one of the services saved", t.Main)
	}
	i := slices.IndexFunc(fe.Services, func(s apitypes.ServiceExport) bool { return s.Name == t.Main })
	t.Domain = len(fe.Services[i].Domains) > 0
	for i, in := range t.Inputs {
		if in.Service == t.Main {
			t.Inputs[i].Service = ""
		}
	}
	return t, nil
}

// saveOrgTemplate stores a checked template under its name, raising the
// revision of one it replaces.
func (c *Core) saveOrgTemplate(ctx context.Context, a Actor, t apptemplates.Template, file string) (apitypes.AppTemplate, error) {
	if _, ok := builtinTemplate(t.Name); ok {
		return apitypes.AppTemplate{}, conflict("Dock has a template named %s; give yours another name", t.Name)
	}
	_, err := c.store.UpsertOrgTemplate(ctx, store.UpsertOrgTemplateParams{OrgID: a.OrgID, Name: t.Name, Body: file, CreatedBy: a.Name, UpdatedAt: now()})
	if err != nil {
		return apitypes.AppTemplate{}, err
	}
	c.audit(ctx, a, "template.save", nil, nil, t.Name, fmt.Sprintf("saved the organization's template %s (%s)", t.Name, count(len(t.Services), "service")))
	info := appTemplateInfo(t)
	info.Org = true
	return info, nil
}

// RemoveAppTemplate removes an organization template. Services made from
// it stay as they are. Needs admin.
func (c *Core) RemoveAppTemplate(ctx context.Context, a Actor, name string) error {
	if err := a.require(apitypes.ScopeAdmin, "removing an organization template"); err != nil {
		return err
	}
	if _, ok := builtinTemplate(name); ok {
		return invalid("%s comes with Dock and can't be removed", name)
	}
	n, err := c.store.DeleteOrgTemplate(ctx, store.DeleteOrgTemplateParams{OrgID: a.OrgID, Name: name})
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("the organization has no template %q", name)
	}
	c.audit(ctx, a, "template.remove", nil, nil, name, "removed the organization's template "+name)
	return nil
}

// AppTemplateFile returns a template's file: an organization's as saved,
// Dock's own as it ships, to copy, edit and save as the organization's.
func (c *Core) AppTemplateFile(ctx context.Context, a Actor, name string) (string, error) {
	if b, ok := apptemplates.File(name); ok {
		return string(b), nil
	}
	if t, ok := builtinTemplate(name); ok {
		b, err := t.TOML()
		return string(b), err
	}
	row, err := c.store.GetOrgTemplate(ctx, store.GetOrgTemplateParams{OrgID: a.OrgID, Name: name})
	if store.IsNotFound(err) {
		return "", notFound("there's no template %q; list them with `dock template list`", name)
	}
	return row.Body, err
}
