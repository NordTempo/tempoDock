package core

import (
	"context"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// The permission model's mode (ADR 0080): in shadow, people's roles
// decide their checks as before groups, and the model answers beside
// each from their real groups and grants, counting where it differs
// (dock platform access-diff); in enforce, their grants decide. Shadow
// until an operator has read a clean diff and switches.

// loadAccessModel reads the mode into memory at start; reading it fails
// to shadow, the old checks.
func (c *Core) loadAccessModel(ctx context.Context) {
	m, err := c.store.GetAccessModel(ctx)
	if err != nil && !store.IsNotFound(err) {
		c.log.Error("read the permission model's mode; the old checks decide", "err", err)
	}
	c.enforceGrants.Store(apitypes.AccessModelMode(m) == apitypes.AccessModelEnforce)
}

// accessMode is the mode in force.
func (c *Core) accessMode() apitypes.AccessModelMode {
	if c.enforceGrants.Load() {
		return apitypes.AccessModelEnforce
	}
	return apitypes.AccessModelShadow
}

// AccessModel is the permission model's mode. Needs platform.view.
func (c *Core) AccessModel(_ context.Context, a Actor) (apitypes.AccessModel, error) {
	if err := a.requirePlatform(PlatformView, "reading the permission model's mode"); err != nil {
		return apitypes.AccessModel{}, err
	}
	return apitypes.AccessModel{Mode: c.accessMode()}, nil
}

// SetAccessModel switches the permission model between shadow and
// enforce. It applies to the next request of each person; the
// disagreements counted so far stay. Needs platform.settings.
func (c *Core) SetAccessModel(ctx context.Context, a Actor, req apitypes.AccessModel) (apitypes.AccessModel, error) {
	if err := a.requirePlatform(PlatformSettings, "switching the permission model"); err != nil {
		return apitypes.AccessModel{}, err
	}
	switch req.Mode {
	case apitypes.AccessModelShadow, apitypes.AccessModelEnforce:
	default:
		return apitypes.AccessModel{}, invalid("the permission model's mode is shadow (roles decide, the model watches) or enforce (groups and grants decide), not %q", req.Mode)
	}
	if err := c.store.SetAccessModel(ctx, store.SetAccessModelParams{AccessModel: string(req.Mode), UpdatedBy: a.String(), UpdatedAt: now()}); err != nil {
		return apitypes.AccessModel{}, err
	}
	was := c.accessMode()
	c.enforceGrants.Store(req.Mode == apitypes.AccessModelEnforce)
	if was != req.Mode {
		c.audit(ctx, a.platform(), "access_model."+string(req.Mode), nil, nil, "", "switched the permission model to "+string(req.Mode))
	}
	return apitypes.AccessModel{Mode: req.Mode}, nil
}
