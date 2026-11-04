package core

import (
	"context"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/compose"
)

// maxComposeSize caps a Compose file and each file it reads, like a
// project file.
const maxComposeSize = 1 << 20

// ImportCompose converts a Docker Compose file into a project file and
// plans it against project (ADR 0049); with req.Apply it stages it with
// the detected secrets' values and applies each environment, as
// ApplyProject with deploy. The secrets' values come from the Compose file
// unless req.Values replaces them, and are never returned. Planning needs
// read; applying what ApplyProject needs.
func (c *Core) ImportCompose(ctx context.Context, a Actor, project string, req apitypes.ComposeImportRequest) (apitypes.ComposeImport, error) {
	out := apitypes.ComposeImport{Warnings: []apitypes.ComposeWarning{}, Secrets: []apitypes.ComposeSecret{}, Builds: []apitypes.ComposeBuild{}, MissingFiles: []string{}, Plan: newPlan(project)}
	if len(req.Compose) > maxComposeSize {
		return out, errf(apitypes.CodeTooLarge, "the Compose file is larger than %d KB", maxComposeSize>>10)
	}
	for name, f := range req.EnvFiles {
		if len(f) > maxComposeSize {
			return out, errf(apitypes.CodeTooLarge, "%s is larger than %d KB", name, maxComposeSize>>10)
		}
	}
	res, err := compose.Convert(compose.Input{Compose: []byte(req.Compose), Project: project, EnvFiles: req.EnvFiles})
	if err != nil {
		return out, invalid("%v", err)
	}
	b, err := res.File.TOMLWith("Tempo Dock project " + project + ", converted from a Docker Compose file with `dock import compose`.\n" +
		"Secrets are names only (variables): their values are never written here.\n" +
		"Apply it with `dock apply FILE --plan`, then without --plan to stage it.\n")
	if err != nil {
		return out, err
	}
	out.File = string(b)
	out.MissingFiles = append(out.MissingFiles, res.MissingFiles...)
	for _, w := range res.Warnings {
		out.Warnings = append(out.Warnings, apitypes.ComposeWarning{Service: w.Service, Key: w.Key, Message: w.Message})
	}
	for _, bd := range res.Builds {
		out.Builds = append(out.Builds, apitypes.ComposeBuild{Service: bd.Service, Context: bd.Context, Dockerfile: bd.Dockerfile})
	}

	// The secrets' values: the Compose file's, replaced or given by req.
	env := "production"
	if len(res.File.Environments) > 0 {
		env = res.File.Environments[0].Name
	}
	values := map[varKey]string{}
	for _, s := range res.Secrets {
		out.Secrets = append(out.Secrets, apitypes.ComposeSecret{Environment: env, Service: s.Service, Key: s.Key, HasValue: s.Value != ""})
		if s.Value != "" {
			values[varKey{env, s.Service, s.Key}] = s.Value
		}
	}
	for _, v := range req.Values {
		values[varKey{v.Environment, v.Service, v.Key}] = v.Value
	}
	ar := apitypes.ApplyRequest{File: out.File}
	for k, v := range values {
		ar.Values = append(ar.Values, apitypes.VariableValue{Environment: k.env, Service: k.service, Key: k.key, Value: v})
	}
	if !req.Apply {
		out.Plan, err = c.PlanProject(ctx, a, project, ar)
		return out, err
	}
	ar.Deploy = true
	applied, err := c.ApplyProject(ctx, a, project, ar)
	if err != nil {
		return out, err
	}
	out.Plan, out.Result = applied.Plan, &applied
	return out, nil
}
