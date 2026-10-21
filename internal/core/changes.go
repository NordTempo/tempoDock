package core

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/NordTempo/platform/internal/apitypes"
	"github.com/NordTempo/platform/internal/store"
)

// Staged changes (Railway-style): setting variables, changing service
// settings and adding volumes are stored next to the live values and only go
// live when applied. Applying an environment's changes is one transaction,
// followed by one config release per affected service that is running.
// Deploying a service from source or an image applies that service's own
// staged changes first; environment-level variables stay staged until an
// explicit apply because they affect every service.

// GetChanges returns an environment's staged changes as a diff.
func (c *Core) GetChanges(ctx context.Context, a Actor, ref EnvRef, reveal bool) (apitypes.PendingChanges, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeRead, "viewing staged changes")
	if err != nil {
		return apitypes.PendingChanges{}, err
	}
	if reveal {
		if err := a.require(apitypes.ScopeAdmin, "revealing secret values"); err != nil {
			return apitypes.PendingChanges{}, err
		}
	}
	return c.pendingChanges(ctx, c.store.Queries, es, reveal)
}

// ApplyChanges applies an environment's staged changes and starts config
// releases for the affected running services.
func (c *Core) ApplyChanges(ctx context.Context, a Actor, ref EnvRef) (apitypes.AppliedChanges, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeDeploy, "applying staged changes")
	if err != nil {
		return apitypes.AppliedChanges{}, err
	}
	return c.applyEnv(ctx, a, es)
}

// DiscardChanges drops an environment's staged changes.
func (c *Core) DiscardChanges(ctx context.Context, a Actor, ref EnvRef) (apitypes.PendingChanges, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeDeploy, "discarding staged changes")
	if err != nil {
		return apitypes.PendingChanges{}, err
	}
	var discarded int
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		before, err := c.pendingChanges(ctx, q, es, false)
		if err != nil {
			return err
		}
		discarded = before.Count
		vars, err := q.ListVariables(ctx, es.env.ID)
		if err != nil {
			return err
		}
		for _, v := range vars {
			switch {
			case v.Pending == "":
			case v.Value == nil:
				err = q.DeleteVariable(ctx, v.ID)
			default:
				err = q.SetVariablePending(ctx, store.SetVariablePendingParams{Pending: "", PendingValue: nil, UpdatedAt: now(), ID: v.ID})
			}
			if err != nil {
				return err
			}
		}
		svcs, err := q.ListServices(ctx, es.env.ID)
		if err != nil {
			return err
		}
		for _, s := range svcs {
			if err := q.SetPendingSettings(ctx, store.SetPendingSettingsParams{PendingSettings: nil, ID: s.ID}); err != nil {
				return err
			}
			vols, err := q.ListVolumes(ctx, s.ID)
			if err != nil {
				return err
			}
			for _, v := range vols {
				if v.Pending {
					if err := q.DeleteVolume(ctx, v.ID); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return apitypes.PendingChanges{}, err
	}
	if discarded > 0 {
		c.audit(ctx, a, "changes.discard", &es.project, &es.env, es.String(), fmt.Sprintf("discarded %d staged changes", discarded))
	}
	return c.pendingChanges(ctx, c.store.Queries, es, false)
}

// changeResult finishes a staging endpoint: optionally apply, then report.
func (c *Core) changeResult(ctx context.Context, a Actor, es envScope, apply bool) (apitypes.ChangeResult, error) {
	var out apitypes.ChangeResult
	if apply {
		applied, err := c.applyEnv(ctx, a, es)
		if err != nil {
			return out, err
		}
		out.Applied = &applied
	}
	var err error
	out.Pending, err = c.pendingChanges(ctx, c.store.Queries, es, false)
	return out, err
}

// applied records what applyPending changed, per service ID.
type applied struct {
	count   int
	envVars bool
	// services maps service ID → whether its new image setting must be
	// pulled (true) or the active image reused (false).
	services map[int64]bool
}

// applyPending makes staged changes live. With only set, it applies just
// that service's own changes (variables, settings, volumes).
func applyPending(ctx context.Context, q *store.Queries, envID int64, only *int64) (applied, error) {
	res := applied{services: map[int64]bool{}}
	vars, err := q.ListVariables(ctx, envID)
	if err != nil {
		return res, err
	}
	for _, v := range vars {
		if v.Pending == "" || (only != nil && (v.ServiceID == nil || *v.ServiceID != *only)) {
			continue
		}
		if v.Pending == "unset" {
			err = q.DeleteVariable(ctx, v.ID)
		} else {
			err = q.ApplyVariable(ctx, v.ID)
		}
		if err != nil {
			return res, err
		}
		res.count++
		if v.ServiceID == nil {
			res.envVars = true
		} else {
			res.services[*v.ServiceID] = res.services[*v.ServiceID] || false
		}
	}
	svcs, err := q.ListServices(ctx, envID)
	if err != nil {
		return res, err
	}
	for _, s := range svcs {
		if only != nil && s.ID != *only {
			continue
		}
		next, err := pendingSettings(s)
		if err != nil {
			return res, err
		}
		if next != nil {
			live := settingsOf(s)
			if _, err := q.UpdateService(ctx, store.UpdateServiceParams{
				Image: next.Image, Port: int64(next.Port), HealthCheckPath: next.HealthCheckPath,
				HealthCheckTimeoutSeconds: int64(next.HealthCheckTimeoutSeconds), StartCommand: next.StartCommand,
				DockerfilePath: next.DockerfilePath, BuildCommand: next.BuildCommand,
				MemoryMb: int64(next.MemoryMB), Cpus: next.CPUs, ID: s.ID,
			}); err != nil {
				return res, err
			}
			if err := q.SetPendingSettings(ctx, store.SetPendingSettingsParams{PendingSettings: nil, ID: s.ID}); err != nil {
				return res, err
			}
			res.count += len(settingChanges(s.Name, live, *next))
			// Build-only settings take effect on the next build; no release.
			buildOnly := live
			buildOnly.DockerfilePath, buildOnly.BuildCommand = next.DockerfilePath, next.BuildCommand
			if buildOnly != *next {
				res.services[s.ID] = res.services[s.ID] || (next.Image != live.Image && next.Image != "")
			}
		}
		vols, err := q.ListVolumes(ctx, s.ID)
		if err != nil {
			return res, err
		}
		for _, v := range vols {
			if v.Pending {
				if err := q.ApplyVolume(ctx, v.ID); err != nil {
					return res, err
				}
				res.count++
				res.services[s.ID] = res.services[s.ID] || false
			}
		}
	}
	return res, nil
}

// applyEnv applies all staged changes in an environment atomically, then
// starts a release for every affected service that has an active release.
func (c *Core) applyEnv(ctx context.Context, a Actor, es envScope) (apitypes.AppliedChanges, error) {
	out := apitypes.AppliedChanges{Releases: []apitypes.Release{}, Skipped: []string{}}
	var res applied
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		if res, err = applyPending(ctx, q, es.env.ID, nil); err != nil {
			return err
		}
		// Resolve every affected service now, so a broken reference rolls
		// the whole apply back instead of failing releases one by one.
		svcs, err := q.ListServices(ctx, es.env.ID)
		if err != nil {
			return err
		}
		for _, s := range svcs {
			if _, affected := res.services[s.ID]; s.ArchivedAt == nil && (affected || res.envVars) {
				if _, err := c.resolveConfig(ctx, q, svcScope{es, s}, nil); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil || res.count == 0 {
		return out, err
	}
	svcs, err := c.store.ListServices(ctx, es.env.ID)
	if err != nil {
		return out, err
	}
	for _, s := range svcs {
		pull, affected := res.services[s.ID]
		if s.ArchivedAt != nil || (!affected && !res.envVars) {
			continue
		}
		ss := svcScope{es, s}
		spec := releaseSpec{source: apitypes.SourceConfig}
		if pull {
			spec = releaseSpec{source: apitypes.SourceImage, imageRef: s.Image}
		}
		rel, err := c.startRelease(ctx, a, ss, spec)
		switch {
		case errors.Is(err, errNoActiveRelease):
			continue // gets the new config on its first deploy
		case CodeOf(err) == apitypes.CodeDeployInProgress:
			out.Skipped = append(out.Skipped, s.Name)
			continue
		case err != nil:
			return out, fmt.Errorf("release %s: %w", ss, err)
		}
		r, err := c.toRelease(ctx, ss, rel, false)
		if err != nil {
			return out, err
		}
		out.Releases = append(out.Releases, r)
	}
	c.audit(ctx, a, "changes.apply", &es.project, &es.env, es.String(),
		fmt.Sprintf("applied %d staged changes; started %d releases", res.count, len(out.Releases)))
	return out, nil
}

// pendingChanges builds the diff of an environment's staged changes.
func (c *Core) pendingChanges(ctx context.Context, q *store.Queries, es envScope, reveal bool) (apitypes.PendingChanges, error) {
	out := apitypes.PendingChanges{
		Project: es.project.Name, Environment: es.env.Name,
		Variables: []apitypes.VariableChange{}, Settings: []apitypes.SettingChange{}, Volumes: []apitypes.VolumeAddition{},
	}
	svcs, err := q.ListServices(ctx, es.env.ID)
	if err != nil {
		return out, err
	}
	svcNames := map[int64]string{}
	for _, s := range svcs {
		svcNames[s.ID] = s.Name
		next, err := pendingSettings(s)
		if err != nil {
			return out, err
		}
		if next != nil {
			out.Settings = append(out.Settings, settingChanges(s.Name, settingsOf(s), *next)...)
		}
		vols, err := q.ListVolumes(ctx, s.ID)
		if err != nil {
			return out, err
		}
		for _, v := range vols {
			if v.Pending {
				out.Volumes = append(out.Volumes, apitypes.VolumeAddition{Service: s.Name, Name: v.Name, MountPath: v.MountPath})
			}
		}
	}
	vars, err := q.ListVariables(ctx, es.env.ID)
	if err != nil {
		return out, err
	}
	for _, v := range vars {
		if v.Pending == "" {
			continue
		}
		vc := apitypes.VariableChange{Key: v.Key, Scope: apitypes.ScopeEnvironment, Action: variableAction(v)}
		if v.ServiceID != nil {
			vc.Scope, vc.Service = apitypes.ScopeService, svcNames[*v.ServiceID]
		}
		if reveal {
			if vc.Old, err = c.openValue(v.Value); err != nil {
				return out, err
			}
			if vc.New, err = c.openValue(v.PendingValue); err != nil {
				return out, err
			}
		}
		out.Variables = append(out.Variables, vc)
	}
	out.Count = len(out.Variables) + len(out.Settings) + len(out.Volumes)
	return out, nil
}

func variableAction(v store.Variable) apitypes.ChangeAction {
	switch {
	case v.Pending == "unset":
		return apitypes.ChangeRemove
	case v.Value == nil:
		return apitypes.ChangeAdd
	default:
		return apitypes.ChangeUpdate
	}
}

// openValue decrypts a nullable variable value.
func (c *Core) openValue(sealed []byte) (*string, error) {
	if sealed == nil {
		return nil, nil
	}
	b, err := c.box.Open(sealed)
	if err != nil {
		return nil, err
	}
	s := string(b)
	return &s, nil
}

// settingChanges lists the fields that differ between two settings.
func settingChanges(service string, old, next apitypes.ServiceSettings) []apitypes.SettingChange {
	fields := []struct {
		name     string
		old, new string
	}{
		{"image", old.Image, next.Image},
		{"port", strconv.Itoa(old.Port), strconv.Itoa(next.Port)},
		{"health_check_path", old.HealthCheckPath, next.HealthCheckPath},
		{"health_check_timeout_seconds", strconv.Itoa(old.HealthCheckTimeoutSeconds), strconv.Itoa(next.HealthCheckTimeoutSeconds)},
		{"start_command", old.StartCommand, next.StartCommand},
		{"dockerfile_path", old.DockerfilePath, next.DockerfilePath},
		{"build_command", old.BuildCommand, next.BuildCommand},
		{"memory_mb", strconv.Itoa(old.MemoryMB), strconv.Itoa(next.MemoryMB)},
		{"cpus", strconv.FormatFloat(old.CPUs, 'g', -1, 64), strconv.FormatFloat(next.CPUs, 'g', -1, 64)},
	}
	var out []apitypes.SettingChange
	for _, f := range fields {
		if f.old != f.new {
			out = append(out, apitypes.SettingChange{Service: service, Field: f.name, Old: f.old, New: f.new})
		}
	}
	return out
}

func describeSettings(old, next apitypes.ServiceSettings) string {
	changes := settingChanges("", old, next)
	if len(changes) == 0 {
		return "no changes"
	}
	parts := make([]string, len(changes))
	for i, ch := range changes {
		parts[i] = fmt.Sprintf("%s %q → %q", ch.Field, ch.Old, ch.New)
	}
	return strings.Join(parts, ", ")
}
