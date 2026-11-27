package core

import (
	"cmp"
	"context"
	"fmt"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/compose"
)

// maxComposeSize caps a Compose file and each file it reads, like a
// project file.
const maxComposeSize = 1 << 20

// ImportCompose converts a Docker Compose file into a project file and
// plans it against project (ADR 0049): its services go in req.Environment
// (default production), so a project made from Compose is updated from
// its next version the same way. With req.Apply it stages the file with
// the detected secrets' values and, unless req.StageOnly, applies it, as
// ApplyProject with deploy. req.Prune archives the services the file
// leaves out, nothing else (composePruning). The secrets' values come from
// the Compose file unless req.Values replaces them, and are never
// returned. Planning needs read; applying what ApplyProject needs
// (archiving admin).
func (c *Core) ImportCompose(ctx context.Context, a Actor, project string, req apitypes.ComposeImportRequest) (apitypes.ComposeImport, error) {
	out, ar, err := convertCompose(project, req)
	if err != nil {
		return out, err
	}
	pr := composePruning(req.Prune)
	if !req.Apply {
		out.Plan, err = c.planProject(ctx, a, project, ar, pr)
		return out, err
	}
	ar.Deploy = !req.StageOnly
	applied, err := c.applyProject(ctx, a, project, ar, pr)
	if err != nil {
		return out, err
	}
	out.Plan, out.Result = applied.Plan, &applied
	return out, nil
}

// convertCompose converts req's Compose file into project's project file
// (out.File, with its warnings, secrets and builds; no plan yet) and the
// request that plans or stages it in req.Environment, with the secrets'
// values. It reads nothing: ImportCompose and Compose syncs plan it.
func convertCompose(project string, req apitypes.ComposeImportRequest) (apitypes.ComposeImport, apitypes.ApplyRequest, error) {
	var ar apitypes.ApplyRequest
	out := apitypes.ComposeImport{Warnings: []apitypes.ComposeWarning{}, Secrets: []apitypes.ComposeSecret{}, Builds: []apitypes.ComposeBuild{}, MissingFiles: []string{}, Plan: newPlan(project)}
	if len(req.Compose) > maxComposeSize {
		return out, ar, errf(apitypes.CodeTooLarge, "the Compose file is larger than %d KB", maxComposeSize>>10)
	}
	for name, f := range req.EnvFiles {
		if len(f) > maxComposeSize {
			return out, ar, errf(apitypes.CodeTooLarge, "%s is larger than %d KB", name, maxComposeSize>>10)
		}
	}
	res, err := compose.Convert(compose.Input{Compose: []byte(req.Compose), Project: project, EnvFiles: req.EnvFiles})
	if err != nil {
		return out, ar, invalid("%v", err)
	}
	out.MissingFiles = append(out.MissingFiles, res.MissingFiles...)
	for _, w := range res.Warnings {
		out.Warnings = append(out.Warnings, apitypes.ComposeWarning{Service: w.Service, Key: w.Key, Message: w.Message})
	}
	for _, bd := range res.Builds {
		out.Builds = append(out.Builds, apitypes.ComposeBuild{Service: bd.Service, Context: bd.Context, Dockerfile: bd.Dockerfile})
	}

	if len(res.File.Environments) != 1 {
		return out, ar, fmt.Errorf("a converted Compose file has %d environments, not one", len(res.File.Environments))
	}
	// A Compose file has no say in an environment's kind: an existing one
	// keeps its own, a new one gets its name's default. So the file names
	// none, whichever environment it goes in.
	env := cmp.Or(req.Environment, res.File.Environments[0].Name)
	res.File.Environments[0].Name, res.File.Environments[0].Kind = env, ""
	b, err := res.File.TOMLWith("Tempo Dock project " + project + ", converted from a Docker Compose file with `dock import compose`.\n" +
		"Secrets are names only (variables): their values are never written here.\n" +
		"Apply it with `dock apply FILE --plan`, then without --plan to stage it.\n")
	if err != nil {
		return out, ar, err
	}
	out.File = string(b)

	// The secrets' values: the Compose file's, replaced or given by req.
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
	ar = apitypes.ApplyRequest{File: out.File, Environment: env}
	for k, v := range values {
		ar.Values = append(ar.Values, apitypes.VariableValue{Environment: k.env, Service: k.service, Key: k.key, Value: v})
	}
	return out, ar, nil
}
