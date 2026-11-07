package core

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cloudflare"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Cloudflare (ADR 0036): named connections, each an API token, with which
// Dock runs a Cloudflare tunnel for tunnel servers and routes domains
// added from the token's zones through them (cloudflare_domains.go).
// Dock never points DNS at an IP. Tokens are sealed with c.box and never
// returned.
//
// A connection is the platform's or an org's own (ADR 0062). Operators
// manage the platform's: they run server tunnels and serve Dock's own
// address, and a shared one's zones are open to every org's domains. An
// org's unlimited admins manage its own, whose zones only its domains
// use. A zone belongs to one connection.
//
// What Dock wants in Cloudflare is rows in cloudflare_tunnels and
// cloudflare_records; a worker (cloudflare_queue.go) makes Cloudflare
// match, so nothing waits on Cloudflare and a failure is retried. Records
// Dock makes carry the comment dock:<install ID>; Dock only ever edits or
// deletes those.

const (
	// cfCheckTimeout bounds checking a token when a connection is saved.
	cfCheckTimeout = 60 * time.Second
	// cfCheckParallel is how many zones are checked at a time then.
	cfCheckParallel = 8
)

// cfState is the Cloudflare integration's state in memory.
type cfState struct {
	// api is Options.CloudflareAPI.
	api string
	// kick wakes the worker.
	kick chan struct{}

	mu sync.Mutex
	// zones are every connection's zones, longest name first, so the
	// first that matches a hostname is its zone. Bounded by the zones the
	// tokens reach.
	zones []cfZone
	// conns are the connections' names, by ID.
	conns map[int64]string
	// tunnels are the hostnames of the tunnels Dock runs, by server ID,
	// for ingressOf. Bounded by servers.
	tunnels map[int64]string
	// pauseUntil holds the worker back after Cloudflare rate limited it.
	pauseUntil time.Time
}

// cfZone is a managed zone with whose connection it is in.
type cfZone struct {
	store.CloudflareZone
	// org is the org whose connection it is in; 0 for the platform's.
	org int64
	// shared: a platform zone every org's domains may use.
	shared bool
}

// usableBy reports whether org's domains may use the zone: the org's
// own, or the platform's shared ones. Org 0 is the platform's own names
// (under a server's base domain), which may use every platform zone.
func (z cfZone) usableBy(org int64) bool {
	if z.org == 0 {
		return org == 0 || z.shared
	}
	return z.org == org
}

func newCFState(api string) cfState {
	return cfState{api: api, kick: make(chan struct{}, 1), tunnels: map[int64]string{}}
}

// kickCloudflare wakes the Cloudflare worker.
func (c *Core) kickCloudflare() {
	select {
	case c.cf.kick <- struct{}{}:
	default:
	}
}

// loadCloudflare reads the zones and the ready tunnels into memory.
func (c *Core) loadCloudflare(ctx context.Context) error {
	if err := c.loadCloudflareZones(ctx); err != nil {
		return err
	}
	rows, err := c.store.ListCloudflareTunnels(ctx)
	if err != nil {
		return err
	}
	c.cf.mu.Lock()
	defer c.cf.mu.Unlock()
	for _, r := range rows {
		t := r.CloudflareTunnel
		if t.ServerID != nil && t.TunnelID != "" && t.Want == cfPresent {
			c.cf.tunnels[*t.ServerID] = cloudflare.Tunnel{ID: t.TunnelID}.Hostname()
		}
	}
	return nil
}

// loadCloudflareZones reads the zones and connection names into memory.
func (c *Core) loadCloudflareZones(ctx context.Context) error {
	rows, err := c.store.ListCloudflareZones(ctx)
	if err != nil {
		return err
	}
	zones := make([]cfZone, len(rows))
	for i, r := range rows {
		zones[i] = cfZone{CloudflareZone: r.CloudflareZone, org: r.OrgID, shared: r.Shared}
	}
	conns, err := c.store.ListCloudflareConnections(ctx)
	if err != nil {
		return err
	}
	names := make(map[int64]string, len(conns))
	for _, r := range conns {
		names[r.ID] = r.Name
	}
	slices.SortFunc(zones, func(a, b cfZone) int { return cmp.Compare(len(b.Name), len(a.Name)) })
	c.cf.mu.Lock()
	c.cf.zones, c.cf.conns = zones, names
	c.cf.mu.Unlock()
	return nil
}

// connectionName names a connection, from memory.
func (c *Core) connectionName(id int64) string {
	c.cf.mu.Lock()
	defer c.cf.mu.Unlock()
	return c.cf.conns[id]
}

// zoneFor returns the managed zone hostname is in that org's domains may
// use: the org's own first, then the platform's shared ones; never
// another org's. Org 0 asks for the platform's own names (usableBy).
func (c *Core) zoneFor(org int64, hostname string) (store.CloudflareZone, bool) {
	c.cf.mu.Lock()
	defer c.cf.mu.Unlock()
	var platform *cfZone
	for i, z := range c.cf.zones {
		if hostname != z.Name && !strings.HasSuffix(hostname, "."+z.Name) || !z.usableBy(org) {
			continue
		}
		if z.org != 0 {
			return z.CloudflareZone, true
		}
		if platform == nil {
			platform = &c.cf.zones[i]
		}
	}
	if platform == nil {
		return store.CloudflareZone{}, false
	}
	return platform.CloudflareZone, true
}

// zoneName returns the name of the managed zone with Cloudflare ID id.
func (c *Core) zoneName(id string) string {
	c.cf.mu.Lock()
	defer c.cf.mu.Unlock()
	for _, z := range c.cf.zones {
		if z.ZoneID == id {
			return z.Name
		}
	}
	return ""
}

// managedTunnel returns the hostname of the tunnel Dock runs for a
// server, "" for none.
func (c *Core) managedTunnel(serverID int64) string {
	c.cf.mu.Lock()
	defer c.cf.mu.Unlock()
	return c.cf.tunnels[serverID]
}

// setManagedTunnel records (or with "" forgets) the hostname of the
// tunnel Dock runs for a server.
func (c *Core) setManagedTunnel(serverID int64, hostname string) {
	c.cf.mu.Lock()
	defer c.cf.mu.Unlock()
	if hostname == "" {
		delete(c.cf.tunnels, serverID)
		return
	}
	c.cf.tunnels[serverID] = hostname
}

// recordComment marks the records this control plane made.
func (c *Core) recordComment() string { return "dock:" + c.install }

// cloudflareAAD binds a connection's sealed token to its row: a platform
// connection's to its name (the AAD it had before orgs), an org's to
// "<org>/<name>".
func cloudflareAAD(org *int64, name string) secrets.AAD {
	if org == nil {
		return sealedCloudflareToken.AAD(name)
	}
	return sealedCloudflareToken.AAD(strconv.FormatInt(*org, 10) + "/" + name)
}

// cfClient is a Cloudflare client with a connection's token.
func (c *Core) cfClient(conn store.CloudflareConnection) (*cloudflare.Client, error) {
	token, err := c.box.Open(conn.Token, cloudflareAAD(conn.OrgID, conn.Name))
	if err != nil {
		return nil, fmt.Errorf("open the token of Cloudflare connection %s: %w", conn.Name, err)
	}
	return &cloudflare.Client{API: c.cf.api, Token: string(token)}, nil
}

// connectionsIn is whose Cloudflare connections an operation acts on:
// with platform the platform's (0), for operators, else the actor's
// org's, for its unlimited admins.
func connectionsIn(a Actor, platform bool, action string) (int64, error) {
	if platform {
		return 0, a.requireOperator(action)
	}
	return a.OrgID, a.requireUnlimited(apitypes.ScopeAdmin, action)
}

// cfAudit is who a connection change is audited as: the actor in its
// org's log, or for a platform connection in the platform's.
func cfAudit(a Actor, org int64) Actor {
	if org == 0 {
		return a.platform()
	}
	return a
}

// cfFlag is what picks a connection's owner on the command line:
// --platform for the platform's.
func cfFlag(org *int64) string {
	if org == nil {
		return " --platform"
	}
	return ""
}

// ListCloudflare returns the org's Cloudflare connections (with platform,
// the platform's), by name.
func (c *Core) ListCloudflare(ctx context.Context, a Actor, platform bool) ([]apitypes.Cloudflare, error) {
	org, err := connectionsIn(a, platform, "listing Cloudflare connections")
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListCloudflareConnectionsOf(ctx, org)
	if err != nil {
		return nil, err
	}
	zones, err := c.store.ListCloudflareZones(ctx)
	if err != nil {
		return nil, err
	}
	var tunnels []store.ListCloudflareTunnelsRow
	if org == 0 { // only the platform's connections run tunnels
		if tunnels, err = c.store.ListCloudflareTunnels(ctx); err != nil {
			return nil, err
		}
	}
	out := make([]apitypes.Cloudflare, len(rows))
	for i, r := range rows {
		out[i] = toCloudflare(r, zones, tunnels)
	}
	return out, nil
}

// cloudflareView returns one of org's connections as ListCloudflare
// shows it.
func (c *Core) cloudflareView(ctx context.Context, org int64, name string) (apitypes.Cloudflare, error) {
	all, err := c.ListCloudflare(ctx, System.InOrg(org), org == 0)
	if err != nil {
		return apitypes.Cloudflare{}, err
	}
	for _, cf := range all {
		if cf.Name == name {
			return cf, nil
		}
	}
	return apitypes.Cloudflare{}, notFound("no Cloudflare connection named %s", name)
}

// AddCloudflare saves a new connection for the org (with platform, the
// platform) after checking its token, and lists the zones domains can be
// added from. Only the platform's may be shared.
func (c *Core) AddCloudflare(ctx context.Context, a Actor, platform bool, req apitypes.CreateCloudflareRequest) (apitypes.Cloudflare, error) {
	org, err := connectionsIn(a, platform, "adding Cloudflare connections")
	if err != nil {
		return apitypes.Cloudflare{}, err
	}
	if req.Shared && org != 0 {
		return apitypes.Cloudflare{}, invalid("only the platform's Cloudflare connections are shared; an organization's own serve its domains only")
	}
	if err := validName("Cloudflare connection", req.Name); err != nil {
		return apitypes.Cloudflare{}, err
	}
	ref := orgRef(org)
	if _, err := c.store.GetCloudflareConnection(ctx, store.GetCloudflareConnectionParams{Org: org, Name: req.Name}); err == nil {
		return apitypes.Cloudflare{}, conflict("a Cloudflare connection named %s exists; pick another name, or give it a new token with `dock cloudflare set %s%s --token-stdin`", req.Name, req.Name, cfFlag(ref))
	} else if !store.IsNotFound(err) {
		return apitypes.Cloudflare{}, err
	}
	token := strings.TrimSpace(req.Token)
	account, zones, err := c.checkCloudflare(ctx, token, strings.TrimSpace(req.AccountID))
	if err != nil {
		return apitypes.Cloudflare{}, err
	}
	zones = zonesFor(org, zones)
	sealed, err := c.box.Seal([]byte(token), cloudflareAAD(ref, req.Name))
	if err != nil {
		return apitypes.Cloudflare{}, err
	}
	t := now()
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		row, err := q.CreateCloudflareConnection(ctx, store.CreateCloudflareConnectionParams{
			OrgID: ref, Name: req.Name, Shared: req.Shared, AccountID: account.ID, AccountName: account.Name, Token: sealed,
			CreatedAt: t, UpdatedAt: t, UpdatedBy: a.Name,
		})
		if err != nil {
			return err
		}
		return saveZones(ctx, q, row, zones)
	})
	if err != nil {
		return apitypes.Cloudflare{}, err
	}
	summary := fmt.Sprintf("added Cloudflare connection %s: account %s, %d zones", req.Name, account.Name, len(zones))
	if req.Shared {
		summary += ", shared with organizations"
	}
	c.audit(ctx, cfAudit(a, org), "cloudflare.connection_add", nil, nil, req.Name, summary)
	c.cloudflareChanged(ctx)
	return c.cloudflareView(ctx, org, req.Name)
}

// zonesFor are the zones of a token a connection of org's may manage: an
// org's own only those Cloudflare serves (active), since a zone added to
// an account but not delegated to it proves nothing about the name.
func zonesFor(org int64, zones []cloudflare.Zone) []cloudflare.Zone {
	if org == 0 {
		return zones
	}
	return slices.DeleteFunc(zones, func(z cloudflare.Zone) bool { return z.Status != "active" })
}

// UpdateCloudflare gives a connection a new token, checked like a new
// one, and lists its zones again: a connection whose token Cloudflare
// refused works again, and what waited for it resumes. A platform
// connection may be shared or stop being shared, with or without a new
// token; domains that use its zones already keep them.
func (c *Core) UpdateCloudflare(ctx context.Context, a Actor, platform bool, name string, req apitypes.UpdateCloudflareRequest) (apitypes.Cloudflare, error) {
	org, err := connectionsIn(a, platform, "changing Cloudflare connections")
	if err != nil {
		return apitypes.Cloudflare{}, err
	}
	row, err := c.cloudflareConnection(ctx, org, name)
	if err != nil {
		return apitypes.Cloudflare{}, err
	}
	token := strings.TrimSpace(req.Token)
	switch {
	case req.Shared != nil && org != 0:
		return apitypes.Cloudflare{}, invalid("only the platform's Cloudflare connections are shared; an organization's own serve its domains only")
	case token == "" && req.AccountID != nil:
		return apitypes.Cloudflare{}, invalid("moving Cloudflare connection %s to another account needs its token too", name)
	case token == "" && req.Shared == nil:
		return apitypes.Cloudflare{}, invalid("give Cloudflare connection %s a new token (`dock cloudflare set %s%s --token-stdin`)", name, name, cfFlag(row.OrgID))
	}
	if token != "" {
		accountID := row.AccountID
		patch(&accountID, req.AccountID)
		if accountID != row.AccountID {
			used, err := c.store.CountConnectionUse(ctx, row.ID)
			if err != nil {
				return apitypes.Cloudflare{}, err
			}
			if used > 0 {
				return apitypes.Cloudflare{}, conflict("Cloudflare connection %s runs tunnels in account %s; it can't move to another account while it does", name, row.AccountName)
			}
		}
		zones, err := c.recheckCloudflare(ctx, a, row, token, accountID)
		if err != nil {
			return apitypes.Cloudflare{}, err
		}
		c.audit(ctx, cfAudit(a, org), "cloudflare.connection_update", nil, nil, name, fmt.Sprintf("gave Cloudflare connection %s a new token (%d zones)", name, zones))
	}
	if req.Shared != nil && *req.Shared != row.Shared {
		if err := c.store.SetCloudflareConnectionShared(ctx, store.SetCloudflareConnectionSharedParams{
			Shared: *req.Shared, UpdatedAt: now(), UpdatedBy: a.Name, ID: row.ID,
		}); err != nil {
			return apitypes.Cloudflare{}, err
		}
		summary := "shared Cloudflare connection " + name + " with organizations: their domains may use its zones"
		if !*req.Shared {
			summary = "stopped sharing Cloudflare connection " + name + " with organizations; their domains in its zones keep working"
		}
		c.audit(ctx, a.platform(), "cloudflare.connection_share", nil, nil, name, summary)
		c.cloudflareChanged(ctx)
	}
	return c.cloudflareView(ctx, org, name)
}

// RefreshCloudflare checks one of the org's connections (with platform,
// the platform's) again and lists its zones again, after zones were
// added to (or taken from) the token in Cloudflare.
func (c *Core) RefreshCloudflare(ctx context.Context, a Actor, platform bool, name string) (apitypes.Cloudflare, error) {
	org, err := connectionsIn(a, platform, "refreshing Cloudflare connections")
	if err != nil {
		return apitypes.Cloudflare{}, err
	}
	row, err := c.cloudflareConnection(ctx, org, name)
	if err != nil {
		return apitypes.Cloudflare{}, err
	}
	cl, err := c.cfClient(row)
	if err != nil {
		return apitypes.Cloudflare{}, err
	}
	if _, err := c.recheckCloudflare(ctx, a, row, cl.Token, row.AccountID); err != nil {
		return apitypes.Cloudflare{}, err
	}
	return c.cloudflareView(ctx, org, name)
}

// recheckCloudflare checks token, then saves it and the zones it reaches
// for connection row, which works again, and has its failed domains
// tried again. It returns the number of zones.
func (c *Core) recheckCloudflare(ctx context.Context, a Actor, row store.CloudflareConnection, token, accountID string) (int, error) {
	account, zones, err := c.checkCloudflare(ctx, token, accountID)
	if err != nil {
		return 0, err
	}
	zones = zonesFor(cfOwner(row), zones)
	sealed, err := c.box.Seal([]byte(token), cloudflareAAD(row.OrgID, row.Name))
	if err != nil {
		return 0, err
	}
	t := now()
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if _, err := q.UpdateCloudflareConnection(ctx, store.UpdateCloudflareConnectionParams{
			AccountID: account.ID, AccountName: account.Name, Token: sealed, UpdatedAt: t, UpdatedBy: a.Name, ID: row.ID,
		}); err != nil {
			return err
		}
		if err := q.RetryConnectionRecords(ctx, store.RetryConnectionRecordsParams{UpdatedAt: t, ConnectionID: row.ID}); err != nil {
			return err
		}
		return saveZones(ctx, q, row, zones)
	})
	if err != nil {
		return 0, err
	}
	c.connectionWorks(ctx, cloudflareAlertKey(row.ID))
	c.cloudflareChanged(ctx)
	return len(zones), nil
}

// RemoveCloudflare deletes one of the org's connections (with platform,
// the platform's) that no tunnel uses. Dock forgets the records it kept
// with it; they stay in Cloudflare.
func (c *Core) RemoveCloudflare(ctx context.Context, a Actor, platform bool, name string) error {
	org, err := connectionsIn(a, platform, "removing Cloudflare connections")
	if err != nil {
		return err
	}
	row, err := c.cloudflareConnection(ctx, org, name)
	if err != nil {
		return err
	}
	used, err := c.store.CountConnectionUse(ctx, row.ID)
	if err != nil {
		return err
	}
	if used > 0 {
		return conflict("Cloudflare connection %s runs server tunnels; remove those servers first (`dock cloudflare --platform` lists them)", name)
	}
	if err := c.store.DeleteCloudflareConnection(ctx, row.ID); err != nil {
		return err
	}
	c.connectionWorks(ctx, cloudflareAlertKey(row.ID))
	c.audit(ctx, cfAudit(a, org), "cloudflare.connection_remove", nil, nil, name, "removed Cloudflare connection "+name+"; the records of its domains stay in Cloudflare")
	c.cloudflareChanged(ctx)
	return nil
}

// cloudflareChanged reloads the zones after a connection changed, and
// wakes the worker for what waited on the connection.
func (c *Core) cloudflareChanged(ctx context.Context) {
	if err := c.loadCloudflareZones(ctx); err != nil {
		c.log.Error("load Cloudflare zones", "err", err)
		return
	}
	c.kickCloudflare()
}

// cloudflareConnection loads one of org's connections (0: the
// platform's) by name.
func (c *Core) cloudflareConnection(ctx context.Context, org int64, name string) (store.CloudflareConnection, error) {
	row, err := c.store.GetCloudflareConnection(ctx, store.GetCloudflareConnectionParams{Org: org, Name: name})
	if store.IsNotFound(err) {
		return row, notFound("no Cloudflare connection named %s; `dock cloudflare%s` lists them", name, cfFlag(orgRef(org)))
	}
	return row, err
}

// cfOwner is the org a connection is in, 0 for the platform's.
func cfOwner(row store.CloudflareConnection) int64 {
	if row.OrgID == nil {
		return 0
	}
	return *row.OrgID
}

// pickCloudflare is the platform connection named name, or the only one
// when name is empty, for a server's tunnel: those are the platform's,
// never an org's. It must work.
func (c *Core) pickCloudflare(ctx context.Context, name string) (store.CloudflareConnection, error) {
	if name != "" {
		row, err := c.cloudflareConnection(ctx, 0, name)
		if err == nil && row.Status != string(apitypes.CloudflareOK) {
			err = invalid("Cloudflare connection %s doesn't work (%s); give it a new token with `dock cloudflare set %s --platform --token-stdin`", name, row.StatusReason, name)
		}
		return row, err
	}
	rows, err := c.store.ListCloudflareConnectionsOf(ctx, 0)
	if err != nil {
		return store.CloudflareConnection{}, err
	}
	switch len(rows) {
	case 0:
		return store.CloudflareConnection{}, invalid("the platform has no Cloudflare connection yet; add one with `dock cloudflare add <name> --platform --token-stdin`")
	case 1:
		return c.pickCloudflare(ctx, rows[0].Name)
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
	}
	return store.CloudflareConnection{}, invalid("the platform has several Cloudflare connections (%s); name one with --cloudflare", strings.Join(names, ", "))
}

// saveZones replaces a connection's zones. A zone another connection of
// the same owner had moves to this one; one another org's or the
// platform's connection has is refused: a zone belongs to one owner.
func saveZones(ctx context.Context, q *store.Queries, conn store.CloudflareConnection, zones []cloudflare.Zone) error {
	held, err := q.ListCloudflareZones(ctx)
	if err != nil {
		return err
	}
	owner := map[string]int64{}
	for _, h := range held {
		owner[h.CloudflareZone.Name] = h.OrgID
	}
	var elsewhere []string
	for _, z := range zones {
		if org, ok := owner[strings.ToLower(z.Name)]; ok && org != cfOwner(conn) {
			elsewhere = append(elsewhere, strings.ToLower(z.Name))
		}
	}
	if len(elsewhere) > 0 {
		return conflict("%s already connected to Dock elsewhere; take %s off this token in Cloudflare, or have whoever connected it remove it first",
			plural(len(elsewhere), "zone "+elsewhere[0]+" is", "zones "+strings.Join(elsewhere, ", ")+" are"), plural(len(elsewhere), "it", "them"))
	}
	if err := q.DeleteCloudflareZones(ctx, conn.ID); err != nil {
		return err
	}
	for _, z := range zones {
		if err := q.SaveCloudflareZone(ctx, store.SaveCloudflareZoneParams{ConnectionID: conn.ID, ZoneID: z.ID, Name: strings.ToLower(z.Name)}); err != nil {
			return err
		}
	}
	return nil
}

// plural picks one or many by n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// checkCloudflare checks a token the way Cloudflare lets it be checked:
// it is active, reaches the account and its tunnels, and lists zones.
// Cloudflare doesn't tell a token its own permissions, so edit
// permissions show on first use. Zones whose DNS records the token can't
// read are left out. accountID picks the account; it may be left out
// when the token reaches one.
func (c *Core) checkCloudflare(ctx context.Context, token, accountID string) (cloudflare.Account, []cloudflare.Zone, error) {
	if token == "" {
		return cloudflare.Account{}, nil, invalid("the Cloudflare API token is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, cfCheckTimeout)
	defer cancel()
	cl := &cloudflare.Client{API: c.cf.api, Token: token}
	var account cloudflare.Account
	accounts, err := cl.Accounts(ctx)
	switch {
	case err != nil && accountID == "":
		return account, nil, cfCheckErr(err, "list the token's accounts")
	case err != nil:
		account.ID = accountID // an account-owned token may not list accounts
	case accountID != "":
		i := slices.IndexFunc(accounts, func(a cloudflare.Account) bool { return a.ID == accountID })
		if i < 0 {
			return account, nil, invalid("the token can't reach account %s; check the account ID, or the token's account resources", accountID)
		}
		account = accounts[i]
	case len(accounts) == 1:
		account = accounts[0]
	case len(accounts) == 0:
		return account, nil, invalid("the token reaches no Cloudflare account; give it Account › Cloudflare Tunnel › Edit on one")
	default:
		ids := make([]string, len(accounts))
		for i, a := range accounts {
			ids[i] = a.ID + " (" + a.Name + ")"
		}
		return account, nil, invalid("the token reaches %d accounts; pick one with --account-id: %s", len(accounts), strings.Join(ids, ", "))
	}
	if err := cl.VerifyToken(ctx, account.ID); err != nil {
		return account, nil, cfCheckErr(err, "verify the token")
	}
	if err := cl.CheckTunnels(ctx, account.ID); err != nil {
		return account, nil, cfCheckErr(err, "list the account's tunnels; give the token Account › Cloudflare Tunnel › Edit")
	}
	zones, err := cl.Zones(ctx, account.ID)
	if err != nil {
		return account, nil, cfCheckErr(err, "list zones; give the token Zone › Zone › Read on the zones Dock may manage")
	}
	// One read per zone, a few at a time: an account may have hundreds.
	readable := make([]bool, len(zones))
	errs := make([]error, len(zones))
	sem := make(chan struct{}, cfCheckParallel)
	var wg sync.WaitGroup
	for i, z := range zones {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			err := cl.CheckDNS(ctx, z.ID)
			if e, ok := cloudflare.AsError(err); ok && e.Denied() {
				return // no DNS permission there: not managed
			}
			readable[i], errs[i] = err == nil, err
		})
	}
	wg.Wait()
	var usable []cloudflare.Zone
	for i, z := range zones {
		if errs[i] != nil {
			return account, nil, cfCheckErr(errs[i], "list DNS records of "+z.Name)
		}
		if readable[i] {
			usable = append(usable, z)
		}
	}
	return account, usable, nil
}

// cfCheckErr explains why checking a token failed; what is what Dock
// tried, with a hint where it helps.
func cfCheckErr(err error, what string) error {
	e, ok := cloudflare.AsError(err)
	switch {
	case !ok:
		return err
	case e.Temporary():
		return errf(apitypes.CodeConflict, "could not %s: %v; try again", what, e)
	case e.Denied():
		return invalid("Cloudflare refused the token when Dock tried to %s (%s); check that the whole token was copied and isn't expired or revoked", what, e.Message)
	}
	return invalid("could not %s: %v", what, e)
}

// toCloudflare is a connection as the API shows it.
func toCloudflare(r store.CloudflareConnection, zones []store.ListCloudflareZonesRow, tunnels []store.ListCloudflareTunnelsRow) apitypes.Cloudflare {
	out := apitypes.Cloudflare{
		Name: r.Name, Shared: r.Shared, AccountID: r.AccountID, AccountName: r.AccountName, Status: apitypes.CloudflareStatus(r.Status),
		StatusReason: r.StatusReason, Zones: []string{}, UsedBy: []string{}, UpdatedAt: r.UpdatedAt, UpdatedBy: r.UpdatedBy,
	}
	for _, z := range zones {
		if z.CloudflareZone.ConnectionID == r.ID {
			out.Zones = append(out.Zones, z.CloudflareZone.Name)
		}
	}
	for _, t := range tunnels {
		if t.CloudflareTunnel.ConnectionID == r.ID && t.CloudflareTunnel.Want == cfPresent {
			out.UsedBy = append(out.UsedBy, t.CloudflareTunnel.ServerName)
		}
	}
	return out
}
