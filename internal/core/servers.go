package core

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/update"
	"github.com/NordTempo/tempoDock/internal/version"
)

// ListServers returns every server with its link status, facts,
// placement settings, load and latest resource sample (from memory),
// those still joining too (an unused join token, listed with ID 0).
// Operators only: servers are the platform's (seesServers), and org
// members get their environment's server status on the environment.
func (c *Core) ListServers(ctx context.Context, a Actor) ([]apitypes.Server, error) {
	if err := a.requirePlatform(PlatformView, "listing servers"); err != nil {
		return nil, err
	}
	return c.listServers(ctx, true)
}

// listServers loads every server, pending join token and managed tunnel
// in three queries.
func (c *Core) listServers(ctx context.Context, usage bool) ([]apitypes.Server, error) {
	rows, err := c.store.ListServerLoads(ctx, int64(c.cfg.Defaults.MemoryMB))
	if err != nil {
		return nil, err
	}
	pending, err := c.store.ListPendingJoinTokens(ctx, now())
	if err != nil {
		return nil, err
	}
	tunnels, err := c.serverTunnels(ctx)
	if err != nil {
		return nil, err
	}
	drains, err := c.drainProgress(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.Server, 0, len(rows)+len(pending))
	for _, r := range rows {
		s := c.toServer(r, usage, tunnels)
		s.Drain = drains[s.ID]
		out = append(out, s)
	}
	// Tokens expire in order, so the last one of a name is the newest.
	for _, t := range pending {
		i := slices.IndexFunc(out, func(s apitypes.Server) bool { return s.Name == t.ServerName })
		switch {
		case i < 0:
			out = append(out, joiningServer(t))
		case out[i].Status == apitypes.ServerRevoked || out[i].Status == apitypes.ServerJoining:
			// A revoked server re-joins under its name and keeps its row.
			out[i].Status, out[i].JoinExpiresAt = apitypes.ServerJoining, &t.ExpiresAt
		}
	}
	return out, nil
}

// joiningServer is a server that has a join token but hasn't joined.
func joiningServer(t store.JoinToken) apitypes.Server {
	return apitypes.Server{
		Name: t.ServerName, Status: apitypes.ServerJoining, JoinExpiresAt: &t.ExpiresAt, Builder: t.Builder,
		BuildsProduction: t.AcceptsProduction, BuildsNonProduction: true,
		AcceptsProduction: t.AcceptsProduction, AcceptsNonProduction: t.AcceptsNonProduction,
		Ingress: apitypes.IngressDirect, PublicIPs: []string{},
	}
}

// serverByID returns one server as ListServers shows it (without usage).
func (c *Core) serverByID(ctx context.Context, id int64) (apitypes.Server, bool, error) {
	all, err := c.listServers(ctx, false)
	if err != nil {
		return apitypes.Server{}, false, err
	}
	for _, s := range all {
		if s.ID == id {
			return s, true, nil
		}
	}
	return apitypes.Server{}, false, nil
}

// GetServer returns one server with its latest resource sample, what is
// placed on it and its services with their usage. A joining server has
// nothing placed yet.
func (c *Core) GetServer(ctx context.Context, a Actor, name string) (apitypes.ServerDetail, error) {
	if err := a.requirePlatform(ServersManage, "managing servers"); err != nil {
		return apitypes.ServerDetail{}, err
	}
	all, err := c.listServers(ctx, true)
	if err != nil {
		return apitypes.ServerDetail{}, err
	}
	i := slices.IndexFunc(all, func(s apitypes.Server) bool { return s.Name == name })
	if i < 0 {
		return apitypes.ServerDetail{}, notFound("server %q not found", name)
	}
	d := apitypes.ServerDetail{Server: all[i], Placements: []apitypes.ServerPlacement{}, Services: []apitypes.ServerService{}}
	srv := all[i]
	if srv.ID == 0 {
		return d, nil
	}
	if d.Server.Tunnel != nil && d.Server.Tunnel.Status == apitypes.TunnelReady {
		// Asked of Cloudflare only while someone looks at the server.
		d.Server.Tunnel.Connectors = c.tunnelConnectors(ctx, srv.ID)
	}
	if d.Services, err = c.serverServices(ctx, srv.ID, serverLink{name: srv.Name, status: srv.Status}); err != nil {
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

// serverServices lists a server's services with status (marked with its
// link) and usage from memory, in one query, the biggest memory users
// first.
func (c *Core) serverServices(ctx context.Context, serverID int64, link serverLink) ([]apitypes.ServerService, error) {
	rows, err := c.store.ListServerServices(ctx, serverID)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.ServerService, len(rows))
	for i, r := range rows {
		s := apitypes.ServerService{
			Project: r.ProjectName, Environment: r.EnvironmentName, Name: r.Name,
			ActiveRelease: r.ActiveRelease, Status: link.mark(c.getStatus(r.ID)), EffectiveLimits: c.limitsOf(r.MemoryMb, r.Cpus),
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

// UpdateServer renames a server, or changes which environment kinds it
// accepts, whether it takes new placements, whether it builds (and for
// which kinds, apart from what it accepts) and its base domain. Environments already there stay; existing generated domains keep
// their hostnames. A rename changes only the name: everything refers to a
// server by ID. The local server's builder setting follows [buildkit]
// enabled on every start, and its base domain falls back to the config's
// base_domain while unset.
func (c *Core) UpdateServer(ctx context.Context, a Actor, name string, req apitypes.UpdateServerRequest) (apitypes.Server, error) {
	if err := a.requirePlatform(ServersManage, "managing servers"); err != nil {
		return apitypes.Server{}, err
	}
	srv, err := c.loadServer(ctx, name)
	if err != nil {
		return apitypes.Server{}, err
	}
	rename := req.Name != nil && *req.Name != srv.Name
	if rename {
		if err := validName("server", *req.Name); err != nil {
			return apitypes.Server{}, err
		}
	}
	settings := store.UpdateServerSettingsParams{
		BaseDomain: srv.BaseDomain, Builder: srv.Builder, ThroughTunnel: srv.ThroughTunnel,
		BuildsProduction: srv.BuildsProduction, BuildsNonProduction: srv.BuildsNonProduction, ID: srv.ID,
	}
	if req.BaseDomain != nil {
		base := strings.TrimPrefix(strings.TrimSpace(*req.BaseDomain), "*.")
		if base != "" {
			if base, err = normalizeHostname(base); err != nil {
				return apitypes.Server{}, err
			}
		}
		settings.BaseDomain = base
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
	placement := len(changes) > 0
	set(&settings.Builder, req.Builder, "builds", "no longer builds")
	set(&settings.BuildsProduction, req.BuildsProduction, "builds production", "no longer builds production")
	set(&settings.BuildsNonProduction, req.BuildsNonProduction, "builds non-production", "no longer builds non-production")
	if settings.Builder && !settings.BuildsProduction && !settings.BuildsNonProduction {
		return apitypes.Server{}, invalid("a builder builds production, non-production or both; to build nothing here, turn it off with `dock server update %s --no-builder`", srv.Name)
	}
	if settings.BaseDomain != srv.BaseDomain {
		changes = append(changes, fmt.Sprintf("base domain %q", settings.BaseDomain))
	}
	if req.Tunnel != nil && !*req.Tunnel {
		if err := c.checkThroughTunnel(ctx, srv, false); err != nil {
			return apitypes.Server{}, err
		}
	}
	if req.Tunnel != nil && c.installedIngress(srv).mode == apitypes.IngressDirect { // else a tunnel server already
		set(&settings.ThroughTunnel, req.Tunnel, "goes through its Cloudflare tunnel",
			"no longer sends new generated hostnames through its Cloudflare tunnel (ones with records stay there)")
	}
	// next is the server as it will be. Sent through its tunnel, its base
	// domain (the config's on the local server when unset) must be able
	// to have records to the tunnel.
	next := srv
	next.BaseDomain, next.ThroughTunnel = settings.BaseDomain, settings.ThroughTunnel
	baseChanged := settings.BaseDomain != srv.BaseDomain
	routeGenerated := next.ThroughTunnel && (req.Tunnel != nil || baseChanged)
	if routeGenerated {
		if err := c.checkThroughTunnel(ctx, next, true); err != nil {
			return apitypes.Server{}, err
		}
	}
	if len(changes) > 0 || rename {
		err := c.store.Tx(ctx, func(q *store.Queries) error {
			if rename {
				if err := renameServer(ctx, q, srv, *req.Name); err != nil {
					return err
				}
			}
			if placement {
				if _, err := q.UpdateServerPlacement(ctx, params); err != nil {
					return err
				}
			}
			_, err := q.UpdateServerSettings(ctx, settings)
			return err
		})
		if err != nil {
			return apitypes.Server{}, err
		}
		if rename {
			c.audit(ctx, a.platform(), "server.rename", nil, nil, "server "+*req.Name, fmt.Sprintf("renamed server %s to %s", srv.Name, *req.Name))
			if srv.Local {
				c.localNow.Store(req.Name)
			}
			if c.nodes != nil {
				c.nodes.Rename(srv.ID, *req.Name)
			}
			srv.Name = *req.Name
		}
		if len(changes) > 0 {
			c.audit(ctx, a.platform(), "server.update", nil, nil, "server "+srv.Name, fmt.Sprintf("server %s %s", srv.Name, strings.Join(changes, ", ")))
		}
		c.publishServer(ctx, srv.ID)
		if baseChanged && c.online(srv.ID) {
			// Its routes carry the new base domain's wildcard, if it has
			// one, and no longer the old one's (ADR 0090).
			if err := c.applyRoutes(ctx, srv.ID); err != nil {
				c.log.Warn("apply routes for a new base domain", "server", srv.Name, "err", err)
			}
		}
		if settings.BaseDomain != srv.BaseDomain {
			c.kickWildcards() // and the worker gets a wildcard for it
		}
		if settings.Builder && !srv.Builder {
			c.warmBuilder(ctx, srv.ID)
		}
		if settings.Builder != srv.Builder || settings.BuildsProduction != srv.BuildsProduction || settings.BuildsNonProduction != srv.BuildsNonProduction {
			c.kickScans() // queued scans may have a scanner now, or another one
		}
		if srv.Local && settings.BaseDomain != srv.BaseDomain {
			c.publicSitesChanged(ctx) // stats-<site>.<base domain> moved
			c.statusPagesChanged(ctx) // and status pages' addresses
		}
	}
	if routeGenerated {
		// Its generated hostnames go through the tunnel from now on. Run
		// on every such request, changed or not, so asking again finishes
		// what a failure here left. Each is served to the tunnel only once
		// its record is ready (tunnelOnlyHosts).
		if _, err := c.routeGeneratedDomains(ctx, next); err != nil {
			return apitypes.Server{}, err
		}
		if srv.Local {
			c.syncStatusRecords() // and the status pages' addresses
		}
	}
	return c.serverDetail(ctx, srv.ID)
}

// renameServer renames srv in q's transaction. It refuses a name another
// server has, or one still joining has; and a server with a join token
// waiting to re-join it keeps its name until it has, as the token joins
// by name. Reading the tokens in the transaction orders it with a token
// minted at the same time.
func renameServer(ctx context.Context, q *store.Queries, srv store.Server, to string) error {
	pending, err := q.ListPendingJoinTokens(ctx, now())
	if err != nil {
		return err
	}
	for _, t := range pending {
		switch t.ServerName {
		case to:
			return conflict("server %s is joining; pick another name, or remove it with `dock server remove %s` first", to, to)
		case srv.Name:
			return conflict("server %s has a join token waiting to re-join it; rename it once it has joined", srv.Name)
		}
	}
	if _, err := q.RenameServer(ctx, store.RenameServerParams{Name: to, ID: srv.ID}); store.IsUnique(err) {
		return conflict("server %s already exists; pick another name", to)
	} else if err != nil {
		return err
	}
	return q.RenameServerTunnel(ctx, store.RenameServerTunnelParams{Name: to, ServerID: &srv.ID})
}

// serverDetail returns one server with its usage.
func (c *Core) serverDetail(ctx context.Context, id int64) (apitypes.Server, error) {
	rows, err := c.store.ListServerLoads(ctx, int64(c.cfg.Defaults.MemoryMB))
	if err != nil {
		return apitypes.Server{}, err
	}
	tunnels, err := c.serverTunnels(ctx)
	if err != nil {
		return apitypes.Server{}, err
	}
	drains, err := c.drainProgress(ctx)
	if err != nil {
		return apitypes.Server{}, err
	}
	for _, r := range rows {
		if r.Server.ID == id {
			s := c.toServer(r, true, tunnels)
			s.Drain = drains[id]
			return s, nil
		}
	}
	return apitypes.Server{}, notFound("server %d not found", id)
}

// AddServer starts adding a server (`dock server add`): it mints a
// one-time join token for it, valid for remote.JoinTokenTTL, and returns
// the token (shown only this once) and the install command to run on the
// new server. The server shows as joining until it joins. A revoked
// server joins again under its name and keeps its environments. With
// Tunnel, it installs in tunnel mode and Dock creates its Cloudflare
// tunnel once it joins (ADR 0036). Admin only.
func (c *Core) AddServer(ctx context.Context, a Actor, req apitypes.AddServerRequest) (apitypes.AddedServer, error) {
	if err := a.requirePlatform(ServersManage, "adding a server"); err != nil {
		return apitypes.AddedServer{}, err
	}
	if err := validName("server", req.Name); err != nil {
		return apitypes.AddedServer{}, err
	}
	if c.nodes == nil {
		return apitypes.AddedServer{}, invalid("this control plane doesn't accept other servers; set [node] listen in its config.toml (e.g. its Tailscale IP and port 7443) and restart dock")
	}
	if srv, err := c.store.GetServerByName(ctx, req.Name); err == nil && srv.RevokedAt == nil {
		return apitypes.AddedServer{}, conflict("server %s already exists; pick another name, or revoke it first to join it again", req.Name)
	} else if err != nil && !store.IsNotFound(err) {
		return apitypes.AddedServer{}, err
	}
	var cloudflareID *int64
	if req.Tunnel {
		conn, err := c.pickCloudflare(ctx, req.Cloudflare)
		if err != nil {
			return apitypes.AddedServer{}, err
		}
		cloudflareID = &conn.ID
	}
	tok, err := c.nodes.MintJoinToken()
	if err != nil {
		return apitypes.AddedServer{}, invalid("%v", err)
	}
	t := now()
	row, err := c.store.CreateJoinToken(ctx, store.CreateJoinTokenParams{
		Hash: tok.SecretHash(), ServerName: req.Name, AcceptsProduction: req.AcceptsProduction,
		AcceptsNonProduction: req.AcceptsNonProduction, Builder: req.Builder, CloudflareID: cloudflareID, CreatedBy: a.Name,
		CreatedAt: t, ExpiresAt: t.Add(remote.JoinTokenTTL),
	})
	if err != nil {
		return apitypes.AddedServer{}, err
	}
	c.audit(ctx, a.platform(), "server.add", nil, nil, "server "+req.Name, fmt.Sprintf("created a join token for server %s, valid until %s", req.Name, row.ExpiresAt.Format(time.RFC3339)))
	srv := joiningServer(row)
	if existing, ok, err := c.serverByName(ctx, req.Name); err == nil && ok {
		srv = existing
	}
	c.bus.publish(busEvent{ev: apitypes.Event{Type: apitypes.EventServer, Server: &srv}})
	token := tok.String()
	ingress := ""
	if req.Tunnel {
		// Dock creates the tunnel once the server joins (ADR 0036).
		ingress = "INGRESS=cloudflare_tunnel "
	}
	return apitypes.AddedServer{
		Server: srv, Token: token, ExpiresAt: row.ExpiresAt, Addrs: tok.Addrs,
		Command: nodeInstallCommand(token, ingress, version.Version),
	}, nil
}

// nodeInstallCommand is the one line a new server runs to join: the hosted
// bootstrap downloads and verifies the release, installs Docker when it is
// missing, and installs a node (NODE_JOIN_TOKEN implies --node). The node
// gets the control plane's own release; a dev build, which has none, gets
// the latest. The token is set by the shell and passed through sudo's
// environment: on sudo's command line every user of that server could read
// it in the process list.
func nodeInstallCommand(token, ingress, running string) string {
	pin := ""
	if update.IsRelease(running) {
		pin = "DOCK_VERSION=" + running + " "
	}
	return "export NODE_JOIN_TOKEN=" + token + " && curl -fsSL https://dock.nordtempo.com/install.sh | sudo --preserve-env=NODE_JOIN_TOKEN " + pin + ingress + "bash -s -- --yes"
}

// cancelJoins deletes the unused join tokens for a server name.
func (c *Core) cancelJoins(ctx context.Context, name string) (int, error) {
	pending, err := c.store.ListPendingJoinTokens(ctx, now())
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range pending {
		if t.ServerName == name {
			if _, err := c.store.DeleteJoinToken(ctx, t.ID); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// serverByName returns one server as ListServers shows it (without usage).
func (c *Core) serverByName(ctx context.Context, name string) (apitypes.Server, bool, error) {
	all, err := c.listServers(ctx, false)
	if err != nil {
		return apitypes.Server{}, false, err
	}
	i := slices.IndexFunc(all, func(s apitypes.Server) bool { return s.Name == name })
	if i < 0 {
		return apitypes.Server{}, false, nil
	}
	return all[i], true, nil
}

// remoteServer loads a server that admins manage over the link: any but
// the local one.
func (c *Core) remoteServer(ctx context.Context, name, action string) (store.Server, error) {
	srv, err := c.loadServer(ctx, name)
	if err != nil {
		return srv, err
	}
	if srv.Local {
		return srv, invalid("server %s runs the control plane; %s only applies to other servers", srv.Name, action)
	}
	return srv, nil
}

// RevokeServer forgets a remote server's key and drops its connection:
// it can't connect again until it re-joins with a new token (AddServer
// under the same name). Its environments stay placed there, and their
// apps keep running on that machine. Operators only.
func (c *Core) RevokeServer(ctx context.Context, a Actor, name string) (apitypes.Server, error) {
	if err := a.requirePlatform(ServersManage, "revoking a server"); err != nil {
		return apitypes.Server{}, err
	}
	srv, err := c.remoteServer(ctx, name, "revoking")
	if err != nil {
		return apitypes.Server{}, err
	}
	if srv.RevokedAt == nil {
		// Before the key goes: from here on no call reaches the server.
		c.setRevoked(srv.ID, true)
		t := now()
		if _, err := c.store.RevokeServer(ctx, store.RevokeServerParams{Now: &t, ID: srv.ID}); err != nil {
			c.setRevoked(srv.ID, false)
			return apitypes.Server{}, err
		}
		if c.nodes != nil {
			c.nodes.Disconnect(srv.ID)
		}
		c.audit(ctx, a.platform(), "server.revoke", nil, nil, "server "+srv.Name, fmt.Sprintf("revoked server %s; its apps keep running there until it joins again", srv.Name))
		c.publishServer(ctx, srv.ID)
	}
	return c.serverDetail(ctx, srv.ID)
}

// RotateServerKey has an online remote server switch to a new link key
// (ADR 0014 section 2); it reconnects with it at once. Operators only.
func (c *Core) RotateServerKey(ctx context.Context, a Actor, name string) (apitypes.Server, error) {
	if err := a.requirePlatform(ServersManage, "rotating a server's key"); err != nil {
		return apitypes.Server{}, err
	}
	srv, err := c.remoteServer(ctx, name, "rotating keys")
	if err != nil {
		return apitypes.Server{}, err
	}
	if _, err := c.runtimeFor(srv.ID); err != nil {
		return apitypes.Server{}, err
	}
	fp, err := c.nodes.RotateKey(ctx, srv.ID)
	if err != nil {
		return apitypes.Server{}, c.nodeErr(srv.ID, err)
	}
	c.audit(ctx, a.platform(), "server.rotate_key", nil, nil, "server "+srv.Name, fmt.Sprintf("server %s switches to key %s", srv.Name, fp))
	return c.serverDetail(ctx, srv.ID)
}

// RemoveServer removes a remote server no live project has environments
// on, or cancels the join tokens of a server that hasn't joined yet. Its
// project pins go with it, and archived projects' environments move to
// the local server (nothing of theirs runs; containers an unreachable
// server still had are left to it). Dock deletes the Cloudflare tunnel it
// made for the server. Operators only.
func (c *Core) RemoveServer(ctx context.Context, a Actor, name string) error {
	if err := a.requirePlatform(ServersManage, "removing a server"); err != nil {
		return err
	}
	srv, err := c.loadServer(ctx, name)
	if CodeOf(err) == apitypes.CodeNotFound {
		cancelled, err := c.cancelJoins(ctx, name)
		if err != nil {
			return err
		}
		if cancelled == 0 {
			return notFound("server %q not found", name)
		}
		c.audit(ctx, a.platform(), "server.remove", nil, nil, "server "+name, fmt.Sprintf("cancelled the join of server %s", name))
		c.bus.publish(busEvent{ev: apitypes.Event{Type: apitypes.EventServer, Server: &apitypes.Server{Name: name, Status: apitypes.ServerJoining}, Message: "removed"}})
		return nil
	}
	if err != nil {
		return err
	}
	if srv.Local {
		return invalid("server %s runs the control plane and can't be removed", srv.Name)
	}
	if st, err := c.store.GetAnalyticsSettings(ctx); err == nil && st.ServerID != nil && *st.ServerID == srv.ID {
		return conflict("server %s runs the analytics store; move it first with `dock analytics server <another server>`", srv.Name)
	}
	if open, err := c.store.ListOpenEnvironmentMoves(ctx); err != nil {
		return err
	} else if slices.ContainsFunc(open, func(m store.EnvironmentMove) bool { return m.FromServerID == srv.ID || m.ToServerID == srv.ID }) {
		return conflict("an environment is moving to or from server %s; remove it once the move ended", srv.Name)
	}
	if d, err := c.store.GetOpenServerDrain(ctx, srv.ID); err == nil && d.Status == string(apitypes.DrainRunning) {
		return conflict("server %s is draining; remove it once the drain ended (`dock server drain %s --status`)", srv.Name, srv.Name)
	}
	// The services it forwards lose their generated domains under its base
	// domain once it is gone (pruneGeneratedDomains).
	forwarded, err := c.store.ListServerForwards(ctx, srv.ID)
	if err != nil {
		return err
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		// Dock deletes the tunnel it made for it (ADR 0036); the row
		// outlives the server until then. Its domains went with their
		// archived services.
		if err := q.SetCloudflareTunnelAbsent(ctx, store.SetCloudflareTunnelAbsentParams{UpdatedAt: now(), ServerID: &srv.ID}); err != nil {
			return err
		}
		if err := q.DeleteServerPlacements(ctx, srv.ID); err != nil {
			return err
		}
		if err := q.ClearArchivedHostPorts(ctx, srv.ID); err != nil {
			return err
		}
		if err := q.MoveArchivedEnvironments(ctx, store.MoveArchivedEnvironmentsParams{LocalServerID: c.localID, ServerID: srv.ID}); err != nil {
			return err
		}
		// Its forwards end with it, and the history that names it goes:
		// drains, moves to and from it, and snapshots whose files were
		// on it (ADR 0063). The audit log keeps the record.
		if err := q.DeleteServerForwards(ctx, srv.ID); err != nil {
			return err
		}
		if err := q.DeleteServerDrainItems(ctx, srv.ID); err != nil {
			return err
		}
		if err := q.UnlinkServerMovesFromDrains(ctx, srv.ID); err != nil {
			return err
		}
		if err := q.DeleteServerMoveServices(ctx, srv.ID); err != nil {
			return err
		}
		if err := q.DeleteServerMoves(ctx, srv.ID); err != nil {
			return err
		}
		if err := q.DeleteServerDrains(ctx, srv.ID); err != nil {
			return err
		}
		if err := q.DeleteServerSnapshots(ctx, srv.ID); err != nil {
			return err
		}
		removed, err := q.DeleteServer(ctx, srv.ID)
		if err == nil && removed == 0 {
			return conflict("server %s still has environments; archive their projects first; `dock server show %s` lists them", srv.Name, srv.Name)
		}
		return err
	})
	if err != nil {
		return err
	}
	c.closeForwards(srv.ID)
	for _, f := range forwarded {
		svc, err := c.store.GetService(ctx, f.ServiceID)
		if err != nil {
			c.log.Warn("prune generated domains of a removed server", "service", f.ServiceID, "err", err)
			continue
		}
		es, err := c.envByID(ctx, svc.EnvironmentID)
		if err == nil {
			_, err = c.pruneGeneratedDomains(ctx, es, svc)
		}
		if err != nil {
			c.log.Warn("prune generated domains of a removed server", "service", f.ServiceID, "err", err)
		}
	}
	if _, err := c.cancelJoins(ctx, name); err != nil {
		c.log.Warn("cancel join tokens of removed server", "server", name, "err", err)
	}
	if rt := c.connected(srv.ID); rt != nil && c.managedTunnel(srv.ID) != "" {
		// Its tunnel is about to be deleted: stop running it there.
		if err := rt.EnsureTunnel(ctx, nil); err != nil {
			c.log.Warn("stop the Cloudflare tunnel of a removed server", "server", name, "err", err)
		}
	}
	c.kickCloudflare()
	c.kickWildcards() // its base domain's may be unused now
	if c.nodes != nil {
		c.nodes.Disconnect(srv.ID)
	}
	c.setManagedTunnel(srv.ID, "") // its row lost the server ID; a new server may get it
	c.forgetServer(srv.ID)
	c.audit(ctx, a.platform(), "server.remove", nil, nil, "server "+srv.Name, "removed server "+srv.Name)
	gone := apitypes.Server{ID: srv.ID, Name: srv.Name}
	c.bus.publish(busEvent{ev: apitypes.Event{Type: apitypes.EventServer, Server: &gone, Message: "removed"}})
	return nil
}

// toServer converts a server row; usage adds its latest sample, for
// callers allowed to read server statistics. tunnels are the tunnels Dock
// manages, by server ID (serverTunnels).
func (c *Core) toServer(r store.ListServerLoadsRow, usage bool, tunnels map[int64]apitypes.ServerTunnel) apitypes.Server {
	s := r.Server
	ing := c.ingressOf(s)
	out := apitypes.Server{
		ID: s.ID, Name: s.Name, Builder: s.Builder, Local: s.Local, Status: c.serverStatus(s), Version: s.Version,
		LastSeenAt: s.LastSeenAt, ConnectedAt: s.ConnectedAt, DisconnectReason: s.DisconnectReason, RevokedAt: s.RevokedAt,
		KeyFingerprint: deref(s.KeyFingerprint), Ingress: ing.mode, PublicIPs: ing.publicIPs, TunnelHostname: ing.tunnel,
		DockerVersion: s.DockerVersion, BaseDomain: ing.baseDomain,
		AcceptsProduction: s.AcceptsProduction, AcceptsNonProduction: s.AcceptsNonProduction,
		BuildsProduction: s.BuildsProduction, BuildsNonProduction: s.BuildsNonProduction,
		DisabledForPlacement: s.DisabledForPlacement, MemoryBytes: s.MemoryBytes, ReservedMemoryBytes: r.ReservedMb << 20,
		Projects: int(r.Projects), Environments: int(r.Environments), ThroughTunnel: ing.listener,
	}
	if usage {
		if u, ok := c.stats.serverUsage(s.ID); ok {
			out.Usage = &u
		}
	}
	if t, ok := tunnels[s.ID]; ok {
		out.Tunnel = &t
	}
	out.TunnelPort = c.tunnelPort(s)
	if !s.Local && c.nodes != nil && (out.Status == apitypes.ServerOnline || out.Status == apitypes.ServerIncompatible) {
		out.LinkVia = string(c.nodes.State(s.ID).Via)
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
	// plainHTTP: the server serves apps over http:// only ([caddy]
	// disable_tls in direct mode): its own config on the local server, a
	// hello fact on a remote one.
	plainHTTP bool
	// listener: a direct server the control plane sends through its
	// tunnel (servers.through_tunnel). It counts as a tunnel server, but
	// its Caddy is still the direct one: what comes through the tunnel
	// arrives on its tunnel listener, and domains whose DNS Dock doesn't
	// manage are still served on its public IP.
	listener bool
}

// ingressOf returns a server's ingress. Remote servers report it in
// hello. The local server reports nothing, so its facts come from the
// config; its base domain and public IP are the config's until set on
// the server (the database wins once set). A tunnel Dock runs for the
// server is its tunnel, whatever the server reports, and a direct server
// sent through it (`dock server update --tunnel`) is a tunnel server
// while Dock runs it.
func (c *Core) ingressOf(s store.Server) serverIngress {
	ing := c.installedIngress(s)
	if s.ThroughTunnel && ing.mode == apitypes.IngressDirect && c.managedTunnel(s.ID) != "" {
		ing.mode, ing.listener, ing.publicIPs, ing.plainHTTP = apitypes.IngressCloudflareTunnel, true, []string{}, false
	}
	return ing
}

// installedIngress is a server's ingress as it was installed: what it
// reports in hello, or the local server's config.
func (c *Core) installedIngress(s store.Server) serverIngress {
	ing := serverIngress{mode: apitypes.Ingress(s.Ingress), tunnel: s.TunnelHostname, baseDomain: s.BaseDomain, publicIPs: []string{}}
	_ = json.Unmarshal([]byte(s.PublicIps), &ing.publicIPs)
	if t := c.managedTunnel(s.ID); t != "" {
		ing.tunnel = t // the tunnel Dock runs for it (ADR 0036)
	}
	if !s.Local {
		ing.plainHTTP = ing.mode == apitypes.IngressDirect && s.PlainHttp
		return ing
	}
	ing.mode = apitypes.Ingress(c.cfg.Ingress.Mode)
	if ing.mode == "" {
		ing.mode = apitypes.IngressDirect
	}
	if t := strings.ToLower(c.cfg.Ingress.TunnelHostname); ing.tunnel == "" && ing.mode == apitypes.IngressCloudflareTunnel && config.ValidTunnelHostname(t) {
		ing.tunnel = t // only what DNS can point at, as for remote servers' hello
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

// checkThroughTunnel refuses sending srv through its tunnel (on) or back
// to its public IP when it can't be: a server installed in tunnel mode
// has no public IP to go back to; going through the tunnel needs one
// Dock runs, ready, and generated hostnames under its base domain (as
// srv will have it) need a platform Cloudflare zone in the tunnel's
// account for their records.
func (c *Core) checkThroughTunnel(ctx context.Context, srv store.Server, on bool) error {
	if c.installedIngress(srv).mode == apitypes.IngressCloudflareTunnel {
		if on {
			return nil // it is a tunnel server already
		}
		return invalid("server %s was installed in tunnel mode and has no public IP to serve on; re-run install.sh on it with INGRESS=direct", srv.Name)
	}
	if !on {
		return nil
	}
	t, err := c.store.GetCloudflareTunnelByServer(ctx, &srv.ID)
	switch {
	case store.IsNotFound(err) || err == nil && t.Want == cfAbsent:
		return invalid("Dock runs no Cloudflare tunnel for server %s; create one with `dock server tunnel create %s` and switch once it is ready", srv.Name, srv.Name)
	case err != nil:
		return err
	case c.managedTunnel(srv.ID) == "":
		return invalid("server %s's tunnel isn't ready yet (%s); switch once it is (`dock server show %s`)", srv.Name, t.Status, srv.Name)
	}
	base := c.installedIngress(srv).baseDomain
	if base == "" {
		return nil
	}
	z, ok := c.zoneFor(0, base)
	if !ok {
		return invalid("%s isn't in a Cloudflare zone of a platform connection, so its generated hostnames couldn't get records to the tunnel; add the zone to the connection's token (`dock cloudflare set --platform`), or change the base domain", base)
	}
	zconn, err := c.store.GetCloudflareConnectionByID(ctx, z.ConnectionID)
	if err != nil {
		return err
	}
	tconn, err := c.store.GetCloudflareConnectionByID(ctx, t.ConnectionID)
	if err != nil {
		return err
	}
	if zconn.AccountID != tconn.AccountID {
		return invalid("server %s's tunnel is in Cloudflare account %s and %s in %s; a hostname can only go through a tunnel in its zone's account", srv.Name, tconn.AccountName, base, zconn.AccountName)
	}
	return nil
}

// routeGeneratedDomains gives the generated hostnames of a tunnel
// server's services that have no Cloudflare record one to its tunnel,
// as GenerateDomain does for new ones: after a direct server is sent
// through its tunnel, and after an environment moved onto a tunnel
// server under a base domain it shares. Hostnames outside the platform's
// zones are left as they are. It reports whether it saved any. Routes
// change once a record is ready (routeReadyRecords).
func (c *Core) routeGeneratedDomains(ctx context.Context, srv store.Server) (bool, error) {
	ing := c.ingressOf(srv)
	if ing.mode != apitypes.IngressCloudflareTunnel || ing.baseDomain == "" {
		return false, nil
	}
	doms, err := c.store.ListUnroutedGeneratedDomains(ctx, srv.ID)
	if err != nil {
		return false, err
	}
	saved := false
	for _, d := range doms {
		if !underBase(d.Hostname, ing.baseDomain) {
			continue // another server's base: that one forwards it
		}
		z, ok := c.zoneFor(0, d.Hostname)
		if !ok {
			continue
		}
		n, err := c.store.SaveCloudflareRecord(ctx, store.SaveCloudflareRecordParams{
			Hostname: d.Hostname, DomainID: &d.ID, ServerID: &srv.ID,
			ConnectionID: z.ConnectionID, ZoneID: z.ZoneID, UpdatedAt: now(),
		})
		if err != nil {
			return saved, err
		}
		saved = saved || n > 0
	}
	if saved {
		c.kickCloudflare()
	}
	return saved, nil
}
