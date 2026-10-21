package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// ListServers returns all servers with their placement settings and load.
func (c *Core) ListServers(ctx context.Context, a Actor) ([]apitypes.Server, error) {
	if err := a.require(apitypes.ScopeRead, "listing servers"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListServerLoads(ctx, int64(c.cfg.Defaults.MemoryMB))
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.Server, 0, len(rows))
	for _, r := range rows {
		out = append(out, c.toServer(r))
	}
	return out, nil
}

// GetServer returns one server with what is placed on it and its latest
// resource sample.
func (c *Core) GetServer(ctx context.Context, a Actor, name string) (apitypes.ServerDetail, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "managing servers"); err != nil {
		return apitypes.ServerDetail{}, err
	}
	srv, err := c.loadServer(ctx, name)
	if err != nil {
		return apitypes.ServerDetail{}, err
	}
	rows, err := c.store.ListServerLoads(ctx, int64(c.cfg.Defaults.MemoryMB))
	if err != nil {
		return apitypes.ServerDetail{}, err
	}
	d := apitypes.ServerDetail{Placements: []apitypes.ServerPlacement{}}
	for _, r := range rows {
		if r.Server.ID == srv.ID {
			d.Server = c.toServer(r)
		}
	}
	pls, err := c.store.ListServerPlacements(ctx, srv.ID)
	if err != nil {
		return d, err
	}
	envs, err := c.store.ListServerEnvironments(ctx, srv.ID)
	if err != nil {
		return d, err
	}
	for _, pl := range pls {
		sp := apitypes.ServerPlacement{
			Project: pl.ProjectName, Kind: apitypes.EnvironmentKind(pl.ProjectPlacement.Kind),
			Pinned: pl.ProjectPlacement.Pinned, Environments: []string{},
		}
		for _, e := range envs {
			if e.ProjectID == pl.ProjectPlacement.ProjectID && e.Kind == pl.ProjectPlacement.Kind {
				sp.Environments = append(sp.Environments, e.Name)
			}
		}
		d.Placements = append(d.Placements, sp)
	}
	if u, ok := c.stats.serverUsage(srv.ID); ok {
		d.Usage = &u
	}
	return d, nil
}

// UpdateServer changes which environment kinds a server accepts and
// whether it takes new placements. Environments already there stay.
func (c *Core) UpdateServer(ctx context.Context, a Actor, name string, req apitypes.UpdateServerRequest) (apitypes.Server, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "managing servers"); err != nil {
		return apitypes.Server{}, err
	}
	srv, err := c.loadServer(ctx, name)
	if err != nil {
		return apitypes.Server{}, err
	}
	params := store.UpdateServerPlacementParams{
		AcceptsProduction: srv.AcceptsProduction, AcceptsNonProduction: srv.AcceptsNonProduction,
		DisabledForPlacement: srv.DisabledForPlacement, ID: srv.ID,
	}
	var changes []string
	set := func(field *bool, to *bool, on, off string) {
		if to == nil || *to == *field {
			return
		}
		*field = *to
		if *to {
			changes = append(changes, on)
		} else {
			changes = append(changes, off)
		}
	}
	set(&params.AcceptsProduction, req.AcceptsProduction, "accepts production", "no longer accepts production")
	set(&params.AcceptsNonProduction, req.AcceptsNonProduction, "accepts non-production", "no longer accepts non-production")
	set(&params.DisabledForPlacement, req.DisabledForPlacement, "disabled for new placements", "enabled for new placements")
	if len(changes) > 0 {
		if _, err := c.store.UpdateServerPlacement(ctx, params); err != nil {
			return apitypes.Server{}, err
		}
		c.audit(ctx, a, "server.update", nil, nil, "server "+srv.Name, fmt.Sprintf("server %s %s", srv.Name, strings.Join(changes, ", ")))
	}
	rows, err := c.store.ListServerLoads(ctx, int64(c.cfg.Defaults.MemoryMB))
	if err != nil {
		return apitypes.Server{}, err
	}
	for _, r := range rows {
		if r.Server.ID == srv.ID {
			return c.toServer(r), nil
		}
	}
	return apitypes.Server{}, notFound("server %q not found", name)
}

func (c *Core) toServer(r store.ListServerLoadsRow) apitypes.Server {
	s := r.Server
	status := "offline"
	if c.online(s.ID) {
		status = "online"
	}
	return apitypes.Server{
		ID: s.ID, Name: s.Name, Builder: s.Builder, Local: s.Local, Status: status, Version: s.Version,
		LastSeenAt: s.LastSeenAt, AcceptsProduction: s.AcceptsProduction, AcceptsNonProduction: s.AcceptsNonProduction,
		DisabledForPlacement: s.DisabledForPlacement, MemoryBytes: s.MemoryBytes, ReservedMemoryBytes: r.ReservedMb << 20,
		Projects: int(r.Projects), Environments: int(r.Environments),
	}
}
