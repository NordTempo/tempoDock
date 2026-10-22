package core

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

// ListServers returns all servers with their placement settings and load,
// and for unlimited admins their latest resource sample (from memory).
func (c *Core) ListServers(ctx context.Context, a Actor) ([]apitypes.Server, error) {
	if err := a.require(apitypes.ScopeRead, "listing servers"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListServerLoads(ctx, int64(c.cfg.Defaults.MemoryMB))
	if err != nil {
		return nil, err
	}
	usage := a.readsServerStats()
	out := make([]apitypes.Server, 0, len(rows))
	for _, r := range rows {
		out = append(out, c.toServer(r, usage))
	}
	return out, nil
}

// GetServer returns one server with its latest resource sample, what is
// placed on it and its services with their usage.
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
			d.Server = c.toServer(r, true)
		}
	}
	if d.Services, err = c.serverServices(ctx, srv.ID); err != nil {
		return d, err
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
	return d, nil
}

// serverServices lists a server's services with status and usage from
// memory, in one query, the biggest memory users first.
func (c *Core) serverServices(ctx context.Context, serverID int64) ([]apitypes.ServerService, error) {
	rows, err := c.store.ListServerServices(ctx, serverID)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.ServerService, len(rows))
	for i, r := range rows {
		s := apitypes.ServerService{
			Project: r.ProjectName, Environment: r.EnvironmentName, Name: r.Name,
			ActiveRelease: r.ActiveRelease, Status: c.getStatus(r.ID), EffectiveLimits: c.limitsOf(r.MemoryMb, r.Cpus),
		}
		if r.ActiveRelease != nil {
			if u, ok := c.stats.serviceUsage(r.ID); ok {
				s.Usage = &u
			}
		}
		out[i] = s
	}
	memory := func(s apitypes.ServerService) int64 {
		if s.Usage == nil {
			return -1
		}
		return s.Usage.MemoryBytes
	}
	slices.SortStableFunc(out, func(a, b apitypes.ServerService) int { return cmp.Compare(memory(b), memory(a)) })
	return out, nil
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
			return c.toServer(r, true), nil
		}
	}
	return apitypes.Server{}, notFound("server %q not found", name)
}

// toServer converts a server row; usage adds its latest sample, for
// callers allowed to read server statistics.
func (c *Core) toServer(r store.ListServerLoadsRow, usage bool) apitypes.Server {
	s := r.Server
	ing := c.ingressOf(s)
	out := apitypes.Server{
		ID: s.ID, Name: s.Name, Builder: s.Builder, Local: s.Local, Status: c.serverStatus(s), Version: s.Version,
		LastSeenAt: s.LastSeenAt, ConnectedAt: s.ConnectedAt, DisconnectReason: s.DisconnectReason, RevokedAt: s.RevokedAt,
		KeyFingerprint: deref(s.KeyFingerprint), Ingress: ing.mode, PublicIPs: ing.publicIPs, TunnelHostname: ing.tunnel,
		DockerVersion: s.DockerVersion, BaseDomain: ing.baseDomain,
		AcceptsProduction: s.AcceptsProduction, AcceptsNonProduction: s.AcceptsNonProduction,
		DisabledForPlacement: s.DisabledForPlacement, MemoryBytes: s.MemoryBytes, ReservedMemoryBytes: r.ReservedMb << 20,
		Projects: int(r.Projects), Environments: int(r.Environments),
	}
	if usage {
		if u, ok := c.stats.serverUsage(s.ID); ok {
			out.Usage = &u
		}
	}
	return out
}

// serverStatus is a server's link status: the local server is always
// online, a revoked one revoked, others what the hub says.
func (c *Core) serverStatus(s store.Server) apitypes.ServerStatus {
	switch {
	case s.Local:
		return apitypes.ServerOnline
	case s.RevokedAt != nil:
		return apitypes.ServerRevoked
	case c.nodes == nil:
		return apitypes.ServerOffline
	}
	switch c.nodes.State(s.ID).Status {
	case remote.StatusOnline:
		return apitypes.ServerOnline
	case remote.StatusIncompatible:
		return apitypes.ServerIncompatible
	}
	return apitypes.ServerOffline
}

// serverIngress is how the public reaches a server (ADR 0013 and 0014
// section 6).
type serverIngress struct {
	mode       apitypes.Ingress
	publicIPs  []string
	tunnel     string // CNAME target of its Cloudflare tunnel
	baseDomain string
	// plainHTTP: the local server serves apps over http:// only
	// ([caddy] disable_tls in direct mode).
	plainHTTP bool
}

// ingressOf returns a server's ingress. Remote servers report it in
// hello. The local server reports nothing, so its facts come from the
// config; its base domain and public IP are the config's until set on
// the server (the database wins once set).
func (c *Core) ingressOf(s store.Server) serverIngress {
	ing := serverIngress{mode: apitypes.Ingress(s.Ingress), tunnel: s.TunnelHostname, baseDomain: s.BaseDomain, publicIPs: []string{}}
	_ = json.Unmarshal([]byte(s.PublicIps), &ing.publicIPs)
	if !s.Local {
		return ing
	}
	ing.mode = apitypes.Ingress(c.cfg.Ingress.Mode)
	if ing.mode == "" {
		ing.mode = apitypes.IngressDirect
	}
	if ing.tunnel == "" {
		ing.tunnel = c.cfg.Ingress.TunnelHostname
	}
	if ing.baseDomain == "" {
		ing.baseDomain = strings.TrimPrefix(c.cfg.BaseDomain, "*.")
	}
	if len(ing.publicIPs) == 0 && c.cfg.PublicIP != "" && ing.mode == apitypes.IngressDirect {
		ing.publicIPs = []string{c.cfg.PublicIP}
	}
	ing.plainHTTP = ing.mode == apitypes.IngressDirect && c.cfg.Caddy.DisableTLS
	return ing
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
