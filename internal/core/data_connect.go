package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// `dock connect` tunnels (ADR 0023): a client's TCP connection carried to
// a service's private port over node.Runtime.Dial, the path the database
// panel uses. A tunnel is raw access (no masking, no changesets), so it
// needs data access level write with sensitive columns visible in the
// environment, and production asks for the environment's name. Each
// tunnel connection is audited when it opens and closes, never its bytes.
// An open tunnel costs its two copy goroutines and nothing else: no
// timers, no idle timeout.

// tunnelDialTimeout bounds connecting to the service.
const tunnelDialTimeout = 10 * time.Second

// ConnectRequest is how a caller asks about a tunnel: Confirm is the
// environment's name (needed on production), Reveal adds the connection
// URL with its credentials.
type ConnectRequest struct {
	Confirm string
	Reveal  bool
}

// TunnelRequest opens one tunnel connection: Confirm as in ConnectRequest,
// From is the caller's address for the audit log.
type TunnelRequest struct {
	Confirm string
	From    string
}

// tunnelTarget is the running container a tunnel reaches and, for
// databases, what a client logs in with.
type tunnelTarget struct {
	serverID    int64
	containerID string
	port        int
	engine      apitypes.DataEngine
	user        string
	database    string
	// url is the connection URL with credentials; set only when asked
	// for, "" when none is known.
	url string
}

// connectAccess checks the actor may open tunnels to a service: data
// access level write with sensitive visible there, and on production the
// environment's name as confirm.
func (c *Core) connectAccess(ctx context.Context, a Actor, ref ServiceRef, confirm string) (svcScope, apitypes.DataPermission, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "connecting to a service")
	if err != nil {
		return ss, apitypes.DataPermission{}, err
	}
	perm, err := c.dataPermission(ctx, a, ss.envScope)
	if err != nil {
		return ss, perm, err
	}
	if !perm.Level.Includes(apitypes.LevelWrite) || perm.Sensitive != apitypes.SensitiveVisible {
		return ss, perm, errf(apitypes.CodeForbidden,
			"a tunnel is raw access to %s, so it needs data access level write with sensitive visible; you have %s, sensitive %s (%s)",
			ss, perm.Level, perm.Sensitive, datapanel.Why(perm))
	}
	if ss.env.Kind == string(apitypes.KindProduction) && confirm != ss.env.Name {
		return ss, perm, invalid("%s is production: confirm with the environment's name (%s) to connect", ss.envScope, ss.env.Name)
	}
	return ss, perm, nil
}

// tunnelTarget resolves the service's running container and the port to
// reach: a database's connection port (ADR 0016 section 1), else the
// active release's port. withURL also derives the connection URL.
func (c *Core) tunnelTarget(ctx context.Context, ss svcScope, withURL bool) (tunnelTarget, error) {
	notRunning := errf(apitypes.CodeConflict, "%s is not running; deploy or restart it to connect", ss)
	if ss.svc.StoppedAt != nil {
		notRunning = errf(apitypes.CodeConflict, "%s is stopped; start it (`dock service start`) to connect", ss)
	}
	if engine, _ := detectDatabase(ss.svc); engine == apitypes.EnginePostgres {
		conn, err := c.dbConnection(ctx, ss)
		if errors.Is(err, errNotRunning) {
			return tunnelTarget{}, notRunning
		}
		if err != nil {
			return tunnelTarget{}, err
		}
		t := tunnelTarget{serverID: conn.serverID, containerID: conn.containerID, port: conn.port, engine: engine, user: conn.owner.User, database: conn.database}
		if withURL {
			u := url.URL{Scheme: "postgres", User: url.UserPassword(conn.owner.User, conn.owner.Password),
				Host: net.JoinHostPort(ss.svc.Name, strconv.Itoa(conn.port)), Path: "/" + conn.database}
			t.url = u.String()
		}
		return t, nil
	}

	rel, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
	if store.IsNotFound(err) || err == nil && rel.ContainerID == "" || err == nil && c.getStatus(ss.svc.ID).State != apitypes.ServiceRunning {
		return tunnelTarget{}, notRunning
	}
	if err != nil {
		return tunnelTarget{}, err
	}
	cfg, err := c.openConfig(rel)
	if err != nil {
		return tunnelTarget{}, fmt.Errorf("read release %d config: %w", rel.Number, err)
	}
	t := tunnelTarget{serverID: ss.env.ServerID, containerID: rel.ContainerID, port: cfg.Port}
	if t.port == 0 {
		t.port = cfg.HealthPort // template databases publish no port
	}
	if t.port == 0 {
		return tunnelTarget{}, invalid("%s has no port to connect to; set one with `dock service update --port` and deploy", ss)
	}
	if withURL {
		switch v := ss.svc.DatabaseUrlVariable; {
		case v != "":
			if t.url, err = c.variableValue(ctx, ss, cfg.Env, v); err != nil {
				return tunnelTarget{}, err
			}
		case cfg.Env["DATABASE_URL"] != "":
			t.url = cfg.Env["DATABASE_URL"]
		default:
			t.url = cfg.Env["REDIS_URL"]
		}
	}
	return t, nil
}

// ConnectInfo checks the actor may open tunnels to a service and says
// what they reach (GET .../connect, what `dock connect` asks first).
// req.Reveal adds the connection URL with its password, audited as
// data.reveal.
func (c *Core) ConnectInfo(ctx context.Context, a Actor, ref ServiceRef, req ConnectRequest) (apitypes.ConnectInfo, error) {
	ss, perm, err := c.connectAccess(ctx, a, ref, req.Confirm)
	if err != nil {
		return apitypes.ConnectInfo{}, err
	}
	if req.Reveal {
		if err := a.requireRecent("revealing a connection URL"); err != nil {
			return apitypes.ConnectInfo{}, err
		}
	}
	t, err := c.tunnelTarget(ctx, ss, req.Reveal)
	if err != nil {
		return apitypes.ConnectInfo{}, err
	}
	info := apitypes.ConnectInfo{
		Service: ss.String(), Port: t.port, Engine: t.engine, User: t.user, Database: t.database,
		Confirm: ss.env.Kind == string(apitypes.KindProduction), Access: perm,
	}
	if req.Reveal {
		if t.url == "" {
			return apitypes.ConnectInfo{}, invalid("%s has no connection URL (DATABASE_URL, REDIS_URL or the database_url_variable setting); connect without --show-url and log in with your own credentials", ss)
		}
		info.URL = t.url
		c.audit(ctx, a, apitypes.AuditDataReveal, &ss.project, &ss.env, ss.String(), "revealed the connection URL for `dock connect`")
	}
	return info, nil
}

// OpenTunnel checks access, connects to the service and audits the open
// (data.connect). The caller must end the tunnel with Pipe, or Close if
// it never pipes. At most TunnelsPerActor are open per actor (429).
func (c *Core) OpenTunnel(ctx context.Context, a Actor, ref ServiceRef, req TunnelRequest) (*Tunnel, error) {
	ss, _, err := c.connectAccess(ctx, a, ref, req.Confirm)
	if err != nil {
		return nil, err
	}
	target, err := c.tunnelTarget(ctx, ss, false)
	if err != nil {
		return nil, err
	}
	t := &Tunnel{c: c, a: a, ss: ss, key: datapanel.ActorKey(panelActor(a)), port: target.port, from: req.From}
	if err := c.tunnels.reserve(t.key); err != nil {
		return nil, err
	}
	conn, err := c.dialService(ctx, ss, target)
	if err != nil {
		c.tunnels.release(t.key)
		return nil, err
	}
	t.conn, t.opened = conn, time.Now()
	if !c.tunnels.track(t) {
		_ = conn.Close()
		c.tunnels.release(t.key)
		return nil, errf(apitypes.CodeServerOffline, "the daemon is shutting down; try again in a moment")
	}
	c.audit(ctx, a, apitypes.AuditDataConnect, &ss.project, &ss.env, ss.String(), fmt.Sprintf("opened a tunnel to port %d from %s", t.port, t.from))
	return t, nil
}

// dialService connects to the target's container on its server.
func (c *Core) dialService(ctx context.Context, ss svcScope, t tunnelTarget) (net.Conn, error) {
	rt, err := c.runtimeFor(t.serverID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, tunnelDialTimeout)
	defer cancel()
	conn, err := rt.Dial(ctx, node.DialRequest{ContainerID: t.containerID, Port: t.port})
	if err = c.nodeErr(t.serverID, err); err != nil {
		if _, ok := errors.AsType[*Error](err); ok {
			return nil, err
		}
		return nil, errf(apitypes.CodeConflict, "can't reach %s on port %d: %v; check that it is running and listens there", ss, t.port, err)
	}
	return conn, nil
}

// Tunnel is one open tunnel connection: the actor's client on one side,
// the service on the other.
type Tunnel struct {
	c      *Core
	a      Actor
	ss     svcScope
	key    string
	port   int
	from   string
	conn   net.Conn // to the service
	opened time.Time
	once   sync.Once
}

// Pipe copies between client and the service until either side ends or
// the daemon shuts down, then closes both and audits the close
// (data.disconnect) with the duration and bytes each way. head is what
// the client sent before the copy started (read ahead with the request).
// It blocks; the open tunnel costs this goroutine and one more.
func (t *Tunnel) Pipe(client net.Conn, head []byte) {
	var up, down int64
	closeBoth := func() { _, _ = client.Close(), t.conn.Close() }
	done := make(chan struct{})
	go func() {
		defer close(done)
		if len(head) > 0 {
			n, err := t.conn.Write(head)
			up += int64(n)
			if err != nil {
				closeBoth()
				return
			}
		}
		// TCP to TCP (a service on this server) splices in the kernel.
		n, _ := io.Copy(t.conn, client)
		up += n
		closeBoth()
	}()
	down, _ = io.Copy(client, t.conn)
	closeBoth()
	<-done
	t.end(up, down)
}

// Close ends a tunnel that never piped.
func (t *Tunnel) Close() {
	_ = t.conn.Close()
	t.end(0, 0)
}

// end audits the close and frees the actor's slot, once.
func (t *Tunnel) end(up, down int64) {
	t.once.Do(func() {
		t.c.tunnels.untrack(t)
		took := time.Since(t.opened).Round(time.Second)
		t.c.audit(context.Background(), t.a, apitypes.AuditDataDisconnect, &t.ss.project, &t.ss.env, t.ss.String(),
			fmt.Sprintf("closed a tunnel to port %d from %s after %s: %s sent, %s received", t.port, t.from, took, humanBytes(up), humanBytes(down)))
		t.c.tunnels.release(t.key)
	})
}

// tunnelSet counts open tunnels per actor and closes them all when the
// daemon stops. A slot is reserved before dialing and released after the
// close is audited; Close waits for that.
type tunnelSet struct {
	mu       sync.Mutex
	closed   bool
	perActor map[string]int
	open     map[*Tunnel]struct{}
	wg       sync.WaitGroup
}

// reserve takes one of the actor's slots.
func (s *tunnelSet) reserve(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errf(apitypes.CodeServerOffline, "the daemon is shutting down; try again in a moment")
	}
	if s.perActor[key] >= apitypes.TunnelsPerActor {
		return errf(apitypes.CodeRateLimited, "you have %d tunnel connections open, the most at once; close some first", apitypes.TunnelsPerActor)
	}
	if s.perActor == nil {
		s.perActor = map[string]int{}
	}
	s.perActor[key]++
	s.wg.Add(1)
	return nil
}

// release gives a slot back.
func (s *tunnelSet) release(key string) {
	s.mu.Lock()
	if s.perActor[key]--; s.perActor[key] <= 0 {
		delete(s.perActor, key)
	}
	s.mu.Unlock()
	s.wg.Done()
}

// track registers an open tunnel so closeAll can end it; false once the
// daemon is stopping.
func (s *tunnelSet) track(t *Tunnel) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if s.open == nil {
		s.open = map[*Tunnel]struct{}{}
	}
	s.open[t] = struct{}{}
	return true
}

func (s *tunnelSet) untrack(t *Tunnel) {
	s.mu.Lock()
	delete(s.open, t)
	s.mu.Unlock()
}

// closeAll closes every tunnel's service connection, which ends its Pipe,
// and waits until each close is audited. No tunnel opens after it.
func (s *tunnelSet) closeAll() {
	s.mu.Lock()
	s.closed = true
	for t := range s.open {
		_ = t.conn.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
