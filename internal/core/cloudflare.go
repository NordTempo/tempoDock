package core

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cloudflare"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Cloudflare (ADR 0035): named connections, each an API token, with which
// Dock runs a Cloudflare tunnel for tunnel servers and routes domains
// added from the token's zones through them (cloudflare_domains.go).
// Dock never points DNS at an IP. Tokens are sealed with c.box and never
// returned. Operators manage connections.
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
	zones []store.CloudflareZone
	// conns are the connections' names, by ID.
	conns map[int64]string
	// tunnels are the hostnames of the tunnels Dock runs, by server ID,
	// for ingressOf. Bounded by servers.
	tunnels map[int64]string
	// pauseUntil holds the worker back after Cloudflare rate limited it.
	pauseUntil time.Time
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
	zones, err := c.store.ListCloudflareZones(ctx)
	if err != nil {
		return err
	}
	rows, err := c.store.ListCloudflareConnections(ctx)
	if err != nil {
		return err
	}
	conns := make(map[int64]string, len(rows))
	for _, r := range rows {
		conns[r.ID] = r.Name
	}
	slices.SortFunc(zones, func(a, b store.CloudflareZone) int { return cmp.Compare(len(b.Name), len(a.Name)) })
	c.cf.mu.Lock()
	c.cf.zones, c.cf.conns = zones, conns
	c.cf.mu.Unlock()
	return nil
}

// connectionName names a connection, from memory.
func (c *Core) connectionName(id int64) string {
	c.cf.mu.Lock()
	defer c.cf.mu.Unlock()
	return c.cf.conns[id]
}

// zoneFor returns the managed zone hostname is in, if any.
func (c *Core) zoneFor(hostname string) (store.CloudflareZone, bool) {
	c.cf.mu.Lock()
	defer c.cf.mu.Unlock()
	for _, z := range c.cf.zones {
		if hostname == z.Name || strings.HasSuffix(hostname, "."+z.Name) {
			return z, true
		}
	}
	return store.CloudflareZone{}, false
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

// cfClient is a Cloudflare client with a connection's token.
func (c *Core) cfClient(conn store.CloudflareConnection) (*cloudflare.Client, error) {
	token, err := c.box.Open(conn.Token)
	if err != nil {
		return nil, fmt.Errorf("open the token of Cloudflare connection %s: %w", conn.Name, err)
	}
	return &cloudflare.Client{API: c.cf.api, Token: string(token)}, nil
}

// ListCloudflare returns the Cloudflare connections, by name.
func (c *Core) ListCloudflare(ctx context.Context, a Actor) ([]apitypes.Cloudflare, error) {
	if err := a.requireOperator("listing Cloudflare connections"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListCloudflareConnections(ctx)
	if err != nil {
		return nil, err
	}
	zones, err := c.store.ListCloudflareZones(ctx)
	if err != nil {
		return nil, err
	}
	tunnels, err := c.store.ListCloudflareTunnels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.Cloudflare, len(rows))
	for i, r := range rows {
		out[i] = toCloudflare(r, zones, tunnels)
	}
	return out, nil
}

// cloudflareView returns one connection as ListCloudflare shows it.
func (c *Core) cloudflareView(ctx context.Context, name string) (apitypes.Cloudflare, error) {
	all, err := c.ListCloudflare(ctx, System)
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

// AddCloudflare saves a new connection after checking its token, and
// lists the zones domains can be added from.
func (c *Core) AddCloudflare(ctx context.Context, a Actor, req apitypes.CreateCloudflareRequest) (apitypes.Cloudflare, error) {
	if err := a.requireOperator("adding Cloudflare connections"); err != nil {
		return apitypes.Cloudflare{}, err
	}
	if err := validName("Cloudflare connection", req.Name); err != nil {
		return apitypes.Cloudflare{}, err
	}
	if _, err := c.store.GetCloudflareConnection(ctx, req.Name); err == nil {
		return apitypes.Cloudflare{}, conflict("a Cloudflare connection named %s exists; pick another name, or give it a new token with `dock cloudflare set %s --token-stdin`", req.Name, req.Name)
	} else if !store.IsNotFound(err) {
		return apitypes.Cloudflare{}, err
	}
	token := strings.TrimSpace(req.Token)
	account, zones, err := c.checkCloudflare(ctx, token, strings.TrimSpace(req.AccountID))
	if err != nil {
		return apitypes.Cloudflare{}, err
	}
	sealed, err := c.box.Seal([]byte(token))
	if err != nil {
		return apitypes.Cloudflare{}, err
	}
	t := now()
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		row, err := q.CreateCloudflareConnection(ctx, store.CreateCloudflareConnectionParams{
			Name: req.Name, AccountID: account.ID, AccountName: account.Name, Token: sealed,
			CreatedAt: t, UpdatedAt: t, UpdatedBy: a.Name,
		})
		if err != nil {
			return err
		}
		return saveZones(ctx, q, row.ID, zones)
	})
	if err != nil {
		return apitypes.Cloudflare{}, err
	}
	c.audit(ctx, a.platform(), "cloudflare.connection_add", nil, nil, req.Name,
		fmt.Sprintf("added Cloudflare connection %s: account %s, %d zones", req.Name, account.Name, len(zones)))
	c.cloudflareChanged(ctx)
	return c.cloudflareView(ctx, req.Name)
}

// UpdateCloudflare gives a connection a new token, checked like a new
// one, and lists its zones again. A connection whose token Cloudflare
// refused works again, and what waited for it resumes.
func (c *Core) UpdateCloudflare(ctx context.Context, a Actor, name string, req apitypes.UpdateCloudflareRequest) (apitypes.Cloudflare, error) {
	if err := a.requireOperator("changing Cloudflare connections"); err != nil {
		return apitypes.Cloudflare{}, err
	}
	row, err := c.cloudflareConnection(ctx, name)
	if err != nil {
		return apitypes.Cloudflare{}, err
	}
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
	zones, err := c.recheckCloudflare(ctx, a, row, strings.TrimSpace(req.Token), accountID)
	if err != nil {
		return apitypes.Cloudflare{}, err
	}
	c.audit(ctx, a.platform(), "cloudflare.connection_update", nil, nil, name, fmt.Sprintf("gave Cloudflare connection %s a new token (%d zones)", name, zones))
	return c.cloudflareView(ctx, name)
}

// RefreshCloudflare checks a connection's token again and lists its
// zones again, after zones were added to (or taken from) the token in
// Cloudflare.
func (c *Core) RefreshCloudflare(ctx context.Context, a Actor, name string) (apitypes.Cloudflare, error) {
	if err := a.requireOperator("refreshing Cloudflare connections"); err != nil {
		return apitypes.Cloudflare{}, err
	}
	row, err := c.cloudflareConnection(ctx, name)
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
	return c.cloudflareView(ctx, name)
}

// recheckCloudflare checks token, then saves it and the zones it reaches
// for connection row, which works again. It returns the number of zones.
func (c *Core) recheckCloudflare(ctx context.Context, a Actor, row store.CloudflareConnection, token, accountID string) (int, error) {
	account, zones, err := c.checkCloudflare(ctx, token, accountID)
	if err != nil {
		return 0, err
	}
	sealed, err := c.box.Seal([]byte(token))
	if err != nil {
		return 0, err
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if _, err := q.UpdateCloudflareConnection(ctx, store.UpdateCloudflareConnectionParams{
			AccountID: account.ID, AccountName: account.Name, Token: sealed, UpdatedAt: now(), UpdatedBy: a.Name, ID: row.ID,
		}); err != nil {
			return err
		}
		return saveZones(ctx, q, row.ID, zones)
	})
	if err != nil {
		return 0, err
	}
	c.cloudflareChanged(ctx)
	return len(zones), nil
}

// RemoveCloudflare deletes a connection no tunnel uses. Dock forgets the
// records it kept with it; they stay in Cloudflare.
func (c *Core) RemoveCloudflare(ctx context.Context, a Actor, name string) error {
	if err := a.requireOperator("removing Cloudflare connections"); err != nil {
		return err
	}
	row, err := c.cloudflareConnection(ctx, name)
	if err != nil {
		return err
	}
	used, err := c.store.CountConnectionUse(ctx, row.ID)
	if err != nil {
		return err
	}
	if used > 0 {
		return conflict("Cloudflare connection %s runs server tunnels; remove those servers first (`dock cloudflare` lists them)", name)
	}
	if err := c.store.DeleteCloudflareConnection(ctx, row.ID); err != nil {
		return err
	}
	c.audit(ctx, a.platform(), "cloudflare.connection_remove", nil, nil, name, "removed Cloudflare connection "+name+"; the records of its domains stay in Cloudflare")
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

// cloudflareConnection loads a connection by name.
func (c *Core) cloudflareConnection(ctx context.Context, name string) (store.CloudflareConnection, error) {
	row, err := c.store.GetCloudflareConnection(ctx, name)
	if store.IsNotFound(err) {
		return row, notFound("no Cloudflare connection named %s; `dock cloudflare` lists them", name)
	}
	return row, err
}

// pickCloudflare is the connection named name, or the only one when name
// is empty. It must work.
func (c *Core) pickCloudflare(ctx context.Context, name string) (store.CloudflareConnection, error) {
	if name != "" {
		row, err := c.cloudflareConnection(ctx, name)
		if err == nil && row.Status != string(apitypes.CloudflareOK) {
			err = invalid("Cloudflare connection %s doesn't work (%s); give it a new token with `dock cloudflare set %s --token-stdin`", name, row.StatusReason, name)
		}
		return row, err
	}
	rows, err := c.store.ListCloudflareConnections(ctx)
	if err != nil {
		return store.CloudflareConnection{}, err
	}
	switch len(rows) {
	case 0:
		return store.CloudflareConnection{}, invalid("no Cloudflare connection yet; add one with `dock cloudflare add <name> --token-stdin`")
	case 1:
		return c.pickCloudflare(ctx, rows[0].Name)
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
	}
	return store.CloudflareConnection{}, invalid("there are several Cloudflare connections (%s); name one with --cloudflare", strings.Join(names, ", "))
}

// saveZones replaces a connection's zones.
func saveZones(ctx context.Context, q *store.Queries, connID int64, zones []cloudflare.Zone) error {
	if err := q.DeleteCloudflareZones(ctx, connID); err != nil {
		return err
	}
	for _, z := range zones {
		if err := q.SaveCloudflareZone(ctx, store.SaveCloudflareZoneParams{ConnectionID: connID, ZoneID: z.ID, Name: strings.ToLower(z.Name)}); err != nil {
			return err
		}
	}
	return nil
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
func toCloudflare(r store.CloudflareConnection, zones []store.CloudflareZone, tunnels []store.ListCloudflareTunnelsRow) apitypes.Cloudflare {
	out := apitypes.Cloudflare{
		Name: r.Name, AccountID: r.AccountID, AccountName: r.AccountName, Status: apitypes.CloudflareStatus(r.Status),
		StatusReason: r.StatusReason, Zones: []string{}, UsedBy: []string{}, UpdatedAt: r.UpdatedAt, UpdatedBy: r.UpdatedBy,
	}
	for _, z := range zones {
		if z.ConnectionID == r.ID {
			out.Zones = append(out.Zones, z.Name)
		}
	}
	for _, t := range tunnels {
		if t.CloudflareTunnel.ConnectionID == r.ID && t.CloudflareTunnel.Want == cfPresent {
			out.UsedBy = append(out.UsedBy, t.CloudflareTunnel.ServerName)
		}
	}
	return out
}
