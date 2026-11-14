package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cloudflare"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

// The Cloudflare worker (ADR 0036): it works through the pending rows of
// cloudflare_tunnels, cloudflare_records and cloudflare_redirects (ADR
// 0068), one call at a time, until
// none is due, then sleeps until kicked or until the earliest held-back
// retry. Idle (nothing pending) it has no timer.

const (
	cfPresent = "present"
	cfAbsent  = "absent"

	// cfCallTimeout bounds one step's Cloudflare calls.
	cfCallTimeout = 30 * time.Second
	// cfRetryMin and cfRetryMax bound the backoff of a failed step:
	// 5 s, 10 s, 20 s … 5 min.
	cfRetryMin = 5 * time.Second
	cfRetryMax = 5 * time.Minute
	// cfRateLimitPause is how long the worker waits after a 429 without
	// a Retry-After: Cloudflare blocks for five minutes.
	cfRateLimitPause = 5 * time.Minute
	// cfBatch is how many records one query of a pass loads.
	cfBatch = 50
	// cfMaxBatches bounds one pass, so a row whose status can't be saved
	// can't keep the worker busy.
	cfMaxBatches = 20
	// cfConnectorsTimeout bounds reading a tunnel's connectors for a
	// server page.
	cfConnectorsTimeout = 5 * time.Second
	// ensureTunnelTimeout bounds having a server run its tunnel, which
	// may pull the cloudflared image first.
	ensureTunnelTimeout = 5 * time.Minute
)

// errRateLimited stops a pass: Cloudflare is rate limiting Dock.
var errRateLimited = errors.New("rate limited by Cloudflare")

// runCloudflare is the worker. It runs from Start until Close.
//
// Its timer (a doc comment is required for every timer, see
// docs/performance.md) only exists while a row waits for a retry: it
// fires at the earliest retry_at, or when a rate limit ends.
func (c *Core) runCloudflare() {
	defer c.wg.Done()
	var timer *time.Timer
	for {
		next := c.cloudflarePass(c.ctx)
		if timer != nil {
			timer.Stop()
		}
		var wake <-chan time.Time
		if !next.IsZero() {
			timer = time.NewTimer(max(time.Until(next), 100*time.Millisecond))
			wake = timer.C
		}
		select {
		case <-c.ctx.Done():
			return
		case <-c.cf.kick:
		case <-wake:
		}
	}
}

// cloudflarePass works through what is due and returns when the next
// retry is (zero for none).
func (c *Core) cloudflarePass(ctx context.Context) time.Time {
	c.cf.mu.Lock()
	pause := c.cf.pauseUntil
	c.cf.mu.Unlock()
	if time.Now().Before(pause) {
		return pause
	}
	conns := map[int64]*cfConn{}
	err := c.cloudflareTunnels(ctx, conns)
	if err == nil {
		err = c.cloudflareRecords(ctx, conns)
	}
	if err == nil {
		err = c.cloudflareRedirects(ctx, conns)
	}
	if errors.Is(err, errRateLimited) {
		c.cf.mu.Lock()
		defer c.cf.mu.Unlock()
		return c.cf.pauseUntil
	}
	if err != nil && ctx.Err() == nil {
		c.log.Error("Cloudflare worker", "err", err)
	}
	return c.nextCloudflareRetry(ctx)
}

// nextCloudflareRetry is the earliest held-back retry, zero for none.
func (c *Core) nextCloudflareRetry(ctx context.Context) time.Time {
	var next time.Time
	for _, q := range []func(context.Context) ([]*time.Time, error){c.store.NextCloudflareTunnelRetry, c.store.NextCloudflareRecordRetry, c.store.NextCloudflareRedirectRetry} {
		at, err := q(ctx)
		if err != nil || len(at) == 0 || at[0] == nil {
			continue
		}
		if next.IsZero() || at[0].Before(next) {
			next = *at[0]
		}
	}
	return next
}

// cfConn is a connection and its client, loaded once per pass.
type cfConn struct {
	row    store.CloudflareConnection
	client *cloudflare.Client
}

// conn loads a connection for a pass.
func (c *Core) conn(ctx context.Context, conns map[int64]*cfConn, id int64) (*cfConn, error) {
	if cc, ok := conns[id]; ok {
		return cc, nil
	}
	row, err := c.store.GetCloudflareConnectionByID(ctx, id)
	if err != nil {
		return nil, err
	}
	cl, err := c.cfClient(row)
	if err != nil {
		return nil, err
	}
	cc := &cfConn{row: row, client: cl}
	conns[id] = cc
	return cc, nil
}

// cfOutcome is what a failed step means for its row.
type cfOutcome struct {
	// status is the row's new status: pending (retry at retryAt, or when
	// its connection works again if retryAt is nil) or error.
	status  string
	reason  string
	retryAt *time.Time
	// attempts is the row's new attempt count.
	attempts int64
}

// failed decides what a step's error means. A refused token is checked:
// if Cloudflare refuses the token itself, the connection is marked
// invalid and its rows wait for a new token; otherwise the token lacks
// the permission perm names. Rate limits pause the whole worker.
func (c *Core) failed(ctx context.Context, cc *cfConn, attempts int64, err error, perm string) (cfOutcome, error) {
	e, ok := cloudflare.AsError(err)
	if !ok && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		// The step's own deadline: Cloudflare was too slow this time.
		e, ok = &cloudflare.Error{Op: "call Cloudflare", Message: "no answer in time"}, true
	}
	switch {
	case ctx.Err() != nil:
		return cfOutcome{}, ctx.Err()
	case !ok:
		return cfOutcome{status: "error", reason: err.Error()}, nil
	case e.Status == 429:
		wait := e.RetryAfter
		if wait <= 0 {
			wait = cfRateLimitPause
		}
		until := time.Now().Add(wait)
		c.cf.mu.Lock()
		c.cf.pauseUntil = until
		c.cf.mu.Unlock()
		at := until.UTC()
		return cfOutcome{status: "pending", reason: fmt.Sprintf("Cloudflare is rate limiting Dock; trying again at %s", at.Format("15:04 MST")), retryAt: &at, attempts: attempts}, errRateLimited
	case e.Temporary():
		d := cfRetryMin << min(attempts, 10)
		at := now().Add(min(d, cfRetryMax))
		return cfOutcome{status: "pending", reason: e.Error() + "; retrying", retryAt: &at, attempts: attempts + 1}, nil
	case e.Denied():
		vctx, cancel := context.WithTimeout(ctx, cfCallTimeout)
		verr := cc.client.VerifyToken(vctx, cc.row.AccountID)
		cancel()
		ve, ok := cloudflare.AsError(verr)
		if verr != nil && (!ok || ve.Temporary()) {
			// Can't tell a refused token from a missing permission now.
			return c.failed(ctx, cc, attempts, verr, perm)
		}
		if ok && ve.Denied() {
			set := "dock cloudflare set " + cc.row.Name + cfFlag(cc.row.OrgID) + " --token-stdin"
			reason := fmt.Sprintf("Cloudflare refused the token (%s); give the connection a new one with `%s`", ve.Message, set)
			if err := c.store.SetCloudflareConnectionStatus(ctx, store.SetCloudflareConnectionStatusParams{
				Status: string(apitypes.CloudflareInvalid), StatusReason: reason, ID: cc.row.ID,
			}); err != nil {
				return cfOutcome{}, err
			}
			cc.row.Status = string(apitypes.CloudflareInvalid)
			c.log.Warn("Cloudflare refused a connection's token", "connection", cc.row.Name)
			c.cloudflareFailed(ctx, cc.row, reason+". Its tunnels and records wait until then.", set)
			return cfOutcome{status: "pending", reason: "waiting for a working token for Cloudflare connection " + cc.row.Name, attempts: attempts}, nil
		}
		// An org's admins can't retry everything, as operators do; a
		// refresh retries their connection's failed domains.
		retry := "dock cloudflare retry"
		if cc.row.OrgID != nil {
			retry = "dock cloudflare refresh " + cc.row.Name
		}
		reason := fmt.Sprintf("Cloudflare refused (%s); the token of connection %s needs %s, then `%s`", e.Message, cc.row.Name, perm, retry)
		c.cloudflareFailed(ctx, cc.row, reason+".", retry)
		return cfOutcome{status: "error", reason: reason}, nil
	}
	return cfOutcome{status: "error", reason: e.Error()}, nil
}

// cloudflareTunnels works through the due tunnels.
func (c *Core) cloudflareTunnels(ctx context.Context, conns map[int64]*cfConn) error {
	due, err := c.store.ListDueCloudflareTunnels(ctx, ptr(now()))
	if err != nil {
		return err
	}
	for _, t := range due {
		cc, err := c.conn(ctx, conns, t.ConnectionID)
		if err != nil {
			return err
		}
		if cc.row.Status != string(apitypes.CloudflareOK) {
			continue // turned invalid in this pass
		}
		var stepErr error
		if t.Want == cfAbsent {
			stepErr = c.deleteTunnel(ctx, cc, t)
		} else {
			stepErr = c.makeTunnel(ctx, cc, t)
		}
		if stepErr == nil {
			c.connectionWorks(ctx, cloudflareAlertKey(cc.row.ID))
			continue
		}
		out, err := c.failed(ctx, cc, t.Attempts, stepErr, "Account › Cloudflare Tunnel › Edit")
		if out.status != "" {
			if _, uerr := c.store.UpdateCloudflareTunnel(ctx, store.UpdateCloudflareTunnelParams{
				Token: t.Token, Status: out.status, Reason: "tunnel for " + t.ServerName + ": " + out.reason,
				Attempts: out.attempts, RetryAt: out.retryAt, UpdatedAt: now(), ID: t.ID, Gen: t.Gen,
			}); uerr != nil {
				return uerr
			}
			if t.ServerID != nil {
				c.publishServer(ctx, *t.ServerID)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// tunnelName is the name of the tunnel Dock makes for tunnel row rowID of
// a server: an interrupted step finds its own tunnel by it, and never the
// one of a server by that name that was removed and is still being
// deleted.
func (c *Core) tunnelName(server string, rowID int64) string {
	return "dock-" + c.install + "-" + server + "-" + strconv.FormatInt(rowID, 10)
}

// makeTunnel creates a server's tunnel (or finds the one an interrupted
// step created), routes everything to the server's Caddy, saves its
// token sealed, and has the server run it.
func (c *Core) makeTunnel(ctx context.Context, cc *cfConn, t store.CloudflareTunnel) error {
	if t.ServerID == nil {
		return c.store.DeleteCloudflareTunnel(ctx, store.DeleteCloudflareTunnelParams{ID: t.ID, Gen: t.Gen})
	}
	srv, err := c.store.GetServer(ctx, *t.ServerID)
	if err != nil {
		return err
	}
	port := c.tunnelPort(srv)
	if port == 0 {
		_, err := c.store.UpdateCloudflareTunnel(ctx, store.UpdateCloudflareTunnelParams{
			Token: t.Token, Status: "error", UpdatedAt: now(), ID: t.ID, Gen: t.Gen,
			Reason: c.noTunnelListener(srv) + "; it is retried when it reconnects",
		})
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, cfCallTimeout)
	defer cancel()
	acct := cc.row.AccountID
	if t.TunnelID == "" {
		// The row's name, not the server's: a rename leaves a tunnel
		// without an ID its first name, so a retry finds what it made.
		name := c.tunnelName(t.ServerName, t.ID)
		found, err := cc.client.TunnelNamed(ctx, acct, name)
		if err != nil {
			return err
		}
		if found == nil {
			made, err := cc.client.CreateTunnel(ctx, acct, name)
			if err != nil {
				return err
			}
			found = &made
		}
		t.TunnelID = found.ID
		// Saved at once, whatever else changed: a crash, or the server's
		// removal, finds it by ID.
		if err := c.store.SetCloudflareTunnelID(ctx, store.SetCloudflareTunnelIDParams{TunnelID: t.TunnelID, ID: t.ID}); err != nil {
			return err
		}
	}
	if !t.Adopted {
		if err := cc.client.SetTunnelConfig(ctx, acct, t.TunnelID, catchAll(port)); err != nil {
			return err
		}
	}
	token, err := cc.client.TunnelToken(ctx, acct, t.TunnelID)
	if err != nil {
		return err
	}
	sealed, err := c.box.Seal([]byte(token), sealedTunnelToken.AAD(t.TunnelID))
	if err != nil {
		return err
	}
	saved, err := c.store.UpdateCloudflareTunnel(ctx, store.UpdateCloudflareTunnelParams{
		Token: sealed, Status: "ok", UpdatedAt: now(), ID: t.ID, Gen: t.Gen,
	})
	if err != nil || saved == 0 {
		return err // saved == 0: asked to go meanwhile; the next step deletes it
	}
	c.tunnelReady(ctx, srv, t.ID, t.TunnelID, token)
	return nil
}

// tunnelReady follows tunnel row rowID becoming ready: ingressOf names
// the tunnel, the domains waiting for it follow, and the server runs it,
// in the background (it may pull the cloudflared image first; offline, it
// does when it reconnects; a failure is the row's reason until then).
func (c *Core) tunnelReady(ctx context.Context, srv store.Server, rowID int64, tunnelID, token string) {
	hostname := cloudflare.Tunnel{ID: tunnelID}.Hostname()
	c.setManagedTunnel(srv.ID, hostname)
	c.syncServerRecords(ctx, srv)
	c.publishServer(ctx, srv.ID)
	rt := c.connected(srv.ID)
	if rt == nil {
		return
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ctx, cancel := context.WithTimeout(c.ctx, ensureTunnelTimeout)
		defer cancel()
		err := rt.EnsureTunnel(ctx, &node.TunnelSpec{Token: token, Hostname: hostname})
		if err == nil || c.ctx.Err() != nil {
			return
		}
		err = c.nodeErr(srv.ID, err)
		c.log.Warn("run the Cloudflare tunnel", "server", srv.Name, "err", err)
		reason := fmt.Sprintf("server %s couldn't run it (%v); it tries again when it reconnects", srv.Name, err)
		if err := c.store.SetCloudflareTunnelReason(c.ctx, store.SetCloudflareTunnelReasonParams{Reason: reason, ID: rowID}); err != nil {
			c.log.Error("save a tunnel's reason", "server", srv.Name, "err", err)
		}
		c.publishServer(c.ctx, srv.ID)
	}()
}

// deleteTunnel deletes the tunnel of a server that went, unless Dock
// adopted it (someone else made it), then forgets it.
func (c *Core) deleteTunnel(ctx context.Context, cc *cfConn, t store.CloudflareTunnel) error {
	if t.TunnelID != "" && !t.Adopted {
		ctx, cancel := context.WithTimeout(ctx, cfCallTimeout)
		defer cancel()
		if err := cc.client.DeleteTunnel(ctx, cc.row.AccountID, t.TunnelID); err != nil {
			return err
		}
	}
	if t.ServerID != nil {
		c.setManagedTunnel(*t.ServerID, "")
	}
	return c.store.DeleteCloudflareTunnel(ctx, store.DeleteCloudflareTunnelParams{ID: t.ID, Gen: t.Gen})
}

// catchAll is the tunnel configuration Dock sets: everything to the
// server's Caddy, which routes by hostname.
func catchAll(port int) cloudflare.TunnelConfig {
	return cloudflare.TunnelConfig{Ingress: []cloudflare.IngressRule{{Service: "http://127.0.0.1:" + strconv.Itoa(port)}}}
}

// tunnelPort is the port on 127.0.0.1 a server's cloudflared forwards to:
// its Caddy's plain-HTTP port in tunnel mode, its tunnel listener on a
// direct server (ADR 0036). 0: a direct server without one (turned off,
// or a node from before protocol 9), which can't take a tunnel.
func (c *Core) tunnelPort(srv store.Server) int {
	if srv.Local {
		return c.cfg.CloudflaredPort()
	}
	tunnelMode := c.ingressOf(srv).mode == apitypes.IngressCloudflareTunnel
	var facts remote.Facts
	if c.nodes != nil {
		facts = c.nodes.State(srv.ID).Hello.Facts
	}
	switch {
	case facts.TunnelPort > 0:
		return facts.TunnelPort
	case !tunnelMode:
		return 0
	case facts.HTTPPort > 0:
		return facts.HTTPPort
	}
	return 80
}

// noTunnelListener explains why a server can't take a tunnel now: it is
// offline (its hello facts aren't known), or a direct server without a
// tunnel listener.
func (c *Core) noTunnelListener(srv store.Server) string {
	switch {
	case srv.Local:
		return fmt.Sprintf("server %s has no tunnel listener; set [caddy] tunnel_port in its config.toml and restart dock", srv.Name)
	case c.connected(srv.ID) == nil:
		return fmt.Sprintf("server %s is offline; try again once it is connected", srv.Name)
	}
	return fmt.Sprintf("server %s has no tunnel listener: upgrade dock on it (or set [caddy] tunnel_port in its config.toml)", srv.Name)
}

// runServerTunnel has a server that (re)connected run the tunnel Dock
// manages for it, if any: one indexed read, and the server only acts when
// something changed.
func (c *Core) runServerTunnel(ctx context.Context, serverID int64, rt node.Runtime) {
	t, err := c.store.GetCloudflareTunnelByServer(ctx, &serverID)
	if err != nil || t.Status != "ok" || t.Want != cfPresent || len(t.Token) == 0 {
		return
	}
	token, err := c.box.Open(t.Token, sealedTunnelToken.AAD(t.TunnelID))
	if err != nil {
		c.log.Error("open a tunnel token", "server", t.ServerName, "err", err)
		return
	}
	hostname := cloudflare.Tunnel{ID: t.TunnelID}.Hostname()
	ctx, cancel := context.WithTimeout(ctx, ensureTunnelTimeout)
	defer cancel()
	if err := rt.EnsureTunnel(ctx, &node.TunnelSpec{Token: string(token), Hostname: hostname}); err != nil {
		c.log.Warn("run the Cloudflare tunnel", "server", t.ServerName, "err", c.nodeErr(serverID, err))
	}
}

// SetServerTunnel has Dock run a tunnel server's Cloudflare tunnel:
// create one (in the background; the server shows it pending) or adopt
// the one the server runs already (at once, so a problem shows here).
// Operators only.
func (c *Core) SetServerTunnel(ctx context.Context, a Actor, name string, req apitypes.ServerTunnelRequest) (apitypes.Server, error) {
	if err := a.requirePlatform(ServersManage, "managing server tunnels"); err != nil {
		return apitypes.Server{}, err
	}
	srv, err := c.loadServer(ctx, name)
	if err != nil {
		return apitypes.Server{}, err
	}
	if _, err := c.store.GetCloudflareTunnelByServer(ctx, &srv.ID); err == nil {
		return apitypes.Server{}, conflict("Dock already runs server %s's tunnel", srv.Name)
	} else if !store.IsNotFound(err) {
		return apitypes.Server{}, err
	}
	ing := c.ingressOf(srv)
	if c.tunnelPort(srv) == 0 {
		return apitypes.Server{}, invalid("%s", c.noTunnelListener(srv))
	}
	conn, err := c.pickCloudflare(ctx, req.Cloudflare)
	if err != nil {
		return apitypes.Server{}, err
	}
	switch req.Action {
	case "create":
		if ing.tunnel != "" {
			return apitypes.Server{}, conflict("server %s already runs tunnel %s; have Dock run it with `dock server tunnel adopt %s`", srv.Name, ing.tunnel, srv.Name)
		}
		err = c.createTunnel(ctx, a, srv, conn)
	case "adopt":
		err = c.adoptTunnel(ctx, a, srv, ing, conn)
	default:
		err = invalid("action must be create or adopt")
	}
	if err != nil {
		return apitypes.Server{}, err
	}
	c.publishServer(ctx, srv.ID)
	return c.serverDetail(ctx, srv.ID)
}

// serverTunnelFor returns the tunnel row Dock runs for a tunnel server,
// so a domain from conn's zones can go through it: the one it has, the
// one the server runs already (adopted), or a new one (created in the
// background). The tunnel and the zone must be in one Cloudflare
// account: a CNAME can't point at another account's tunnel. Tunnels are
// the platform's, so for a zone of an org's own connection a new one is
// made with a platform connection in the same account.
func (c *Core) serverTunnelFor(ctx context.Context, a Actor, srv store.Server, conn store.CloudflareConnection) error {
	t, err := c.store.GetCloudflareTunnelByServer(ctx, &srv.ID)
	switch {
	case err == nil:
		tconn, err := c.store.GetCloudflareConnectionByID(ctx, t.ConnectionID)
		if err != nil {
			return err
		}
		if tconn.AccountID != conn.AccountID {
			return invalid("server %s's tunnel is in Cloudflare account %s, and the zone in %s; a domain can only go through a tunnel in its zone's account", srv.Name, tconn.AccountName, conn.AccountName)
		}
		if t.Want == cfAbsent {
			return invalid("server %s's tunnel is being deleted", srv.Name)
		}
		return nil
	case !store.IsNotFound(err):
		return err
	}
	if conn.OrgID != nil {
		if conn, err = c.platformConnectionIn(ctx, srv, conn.AccountID, conn.AccountName); err != nil {
			return err
		}
	}
	ing := c.ingressOf(srv)
	if ing.tunnel != "" {
		return c.adoptTunnel(ctx, a, srv, ing, conn)
	}
	return c.createTunnel(ctx, a, srv, conn)
}

// platformConnectionIn is a working platform connection in Cloudflare
// account accountID, to make srv's tunnel with for a domain in an org's
// own zone there.
func (c *Core) platformConnectionIn(ctx context.Context, srv store.Server, accountID, accountName string) (store.CloudflareConnection, error) {
	rows, err := c.store.ListCloudflareConnectionsOf(ctx, 0)
	if err != nil {
		return store.CloudflareConnection{}, err
	}
	for _, r := range rows {
		if r.AccountID == accountID && r.Status == string(apitypes.CloudflareOK) {
			return r, nil
		}
	}
	return store.CloudflareConnection{}, invalid("server %s has no Cloudflare tunnel, and the platform has no connection in account %s to make one with; a domain can only go through a tunnel in its zone's account. Ask the platform's operator, or use a zone the platform shares", srv.Name, accountName)
}

// createTunnel has Dock create a tunnel for a server that can take one, in
// the background.
func (c *Core) createTunnel(ctx context.Context, a Actor, srv store.Server, conn store.CloudflareConnection) error {
	if _, err := c.store.CreateCloudflareTunnel(ctx, store.CreateCloudflareTunnelParams{
		ServerID: &srv.ID, ServerName: srv.Name, ConnectionID: conn.ID, Status: "pending", UpdatedAt: now(),
	}); err != nil {
		return err
	}
	c.audit(ctx, a.platform(), "server.tunnel_create", nil, nil, "server "+srv.Name, fmt.Sprintf("Dock creates a Cloudflare tunnel for server %s with connection %s", srv.Name, conn.Name))
	c.kickCloudflare()
	return nil
}

// adoptTunnel takes over the tunnel a server already runs (made in the
// dashboard and installed with its token): its token is saved, and the
// server runs it in Dock's container as another connector next to the
// old one, so traffic doesn't drop. Its routes stay; domains Dock adds get
// their own when its catch-all doesn't go to Caddy.
func (c *Core) adoptTunnel(ctx context.Context, a Actor, srv store.Server, ing serverIngress, conn store.CloudflareConnection) error {
	id, ok := strings.CutSuffix(ing.tunnel, ".cfargotunnel.com")
	if !ok || id == "" {
		return invalid("server %s doesn't report the tunnel it runs; create one with `dock server tunnel create %s`", srv.Name, srv.Name)
	}
	cl, err := c.cfClient(conn)
	if err != nil {
		return err
	}
	ctx2, cancel := context.WithTimeout(ctx, cfCallTimeout)
	defer cancel()
	t, err := cl.Tunnel(ctx2, conn.AccountID, id)
	if e, ok := cloudflare.AsError(err); ok && (e.NotFound() || e.Denied()) {
		return invalid("server %s's tunnel %s isn't in account %s of Cloudflare connection %s; use the connection of the account that has it", srv.Name, id, conn.AccountName, conn.Name)
	}
	if err != nil {
		return cfCheckErr(err, "read tunnel "+id)
	}
	if t.ConfigSrc != "cloudflare" {
		return invalid("server %s's tunnel %s is configured by a file next to cloudflared; migrate it to the dashboard first (Zero Trust › Networks › Tunnels › %s › Migrate)", srv.Name, t.Name, t.Name)
	}
	token, err := cl.TunnelToken(ctx2, conn.AccountID, id)
	if err != nil {
		return cfCheckErr(err, "read the tunnel's token")
	}
	sealed, err := c.box.Seal([]byte(token), sealedTunnelToken.AAD(id))
	if err != nil {
		return err
	}
	row, err := c.store.CreateCloudflareTunnel(ctx, store.CreateCloudflareTunnelParams{
		ServerID: &srv.ID, ServerName: srv.Name, ConnectionID: conn.ID, TunnelID: id, Token: sealed, Adopted: true,
		Status: "ok", UpdatedAt: now(),
	})
	if err != nil {
		return err
	}
	c.audit(ctx, a.platform(), "server.tunnel_adopt", nil, nil, "server "+srv.Name, fmt.Sprintf("Dock runs server %s's tunnel %s (connection %s)", srv.Name, t.Name, conn.Name))
	c.tunnelReady(ctx, srv, row.ID, id, token)
	return nil
}

// serverTunnels returns the tunnel Dock manages for each server, by
// server ID, in one query.
func (c *Core) serverTunnels(ctx context.Context) (map[int64]apitypes.ServerTunnel, error) {
	rows, err := c.store.ListCloudflareTunnels(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]apitypes.ServerTunnel, len(rows))
	for _, r := range rows {
		t := r.CloudflareTunnel
		if t.ServerID == nil {
			continue
		}
		st := apitypes.ServerTunnel{Connection: r.ConnectionName, TunnelID: t.TunnelID, Adopted: t.Adopted, Reason: t.Reason}
		if t.TunnelID != "" {
			st.Hostname = cloudflare.Tunnel{ID: t.TunnelID}.Hostname()
		}
		switch {
		case t.Want == cfAbsent:
			st.Status = apitypes.TunnelDeleting
		case t.Status == "ok":
			st.Status = apitypes.TunnelReady
		case t.Status == "error":
			st.Status = apitypes.TunnelError
		default:
			st.Status = apitypes.TunnelPending
		}
		out[*t.ServerID] = st
	}
	return out, nil
}

// tunnelConnectors reads a ready tunnel's connectors from Cloudflare, for
// one server's page. Nil when it can't.
func (c *Core) tunnelConnectors(ctx context.Context, serverID int64) []apitypes.TunnelConnector {
	t, err := c.store.GetCloudflareTunnelByServer(ctx, &serverID)
	if err != nil || t.TunnelID == "" || t.Want != cfPresent {
		return nil
	}
	conn, err := c.store.GetCloudflareConnectionByID(ctx, t.ConnectionID)
	if err != nil || conn.Status != string(apitypes.CloudflareOK) {
		return nil
	}
	cl, err := c.cfClient(conn)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, cfConnectorsTimeout)
	defer cancel()
	list, err := cl.Connectors(ctx, conn.AccountID, t.TunnelID)
	if err != nil {
		return nil
	}
	out := make([]apitypes.TunnelConnector, len(list))
	for i, k := range list {
		tc := apitypes.TunnelConnector{ID: k.ID, Version: k.Version, Since: k.RunAt, Edge: []string{}}
		for _, cn := range k.Conns {
			if !slices.Contains(tc.Edge, cn.ColoName) {
				tc.Edge = append(tc.Edge, cn.ColoName)
			}
		}
		out[i] = tc
	}
	return out
}

// RetryCloudflare has Dock try every failed record, tunnel and redirect
// again. Operators only.
func (c *Core) RetryCloudflare(ctx context.Context, a Actor) (apitypes.CloudflareRetried, error) {
	if err := a.requirePlatform(PlatformSettings, "retrying Cloudflare changes"); err != nil {
		return apitypes.CloudflareRetried{}, err
	}
	t := now()
	records, err := c.store.RetryFailedCloudflare(ctx, t)
	if err != nil {
		return apitypes.CloudflareRetried{}, err
	}
	tunnels, err := c.store.RetryFailedCloudflareTunnels(ctx, t)
	if err != nil {
		return apitypes.CloudflareRetried{}, err
	}
	redirects, err := c.store.RetryFailedCloudflareRedirects(ctx, t)
	if err != nil {
		return apitypes.CloudflareRetried{}, err
	}
	c.kickCloudflare()
	return apitypes.CloudflareRetried{Retried: int(records + tunnels + redirects)}, nil
}
