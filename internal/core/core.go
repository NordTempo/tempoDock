// Package core is the platform's domain logic: projects, environments,
// services, variables, releases and the deploy state machine, domains, auth
// and audit. It talks to SQLite through internal/store and to servers only
// through node.Runtime; it knows nothing about HTTP or Docker.
//
// Every exported operation takes the calling Actor and checks permissions
// itself, so the API (and anything else) can't forget to.
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/pg"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/version"
)

// stopTimeout is how long a replaced container gets to exit gracefully.
const stopTimeout = 30 * time.Second

// DefaultCutoverDrain is how long the old container keeps running after
// routes switch to a new release. Existing proxy connections (HTTP/2
// keep-alive) can use the old upstream until Caddy's 10s grace period
// closes them.
const DefaultCutoverDrain = 12 * time.Second

// Options are Core's dependencies.
type Options struct {
	Config  config.Config
	Store   *store.Store
	Secrets *secrets.Box
	// Runtime is the local server's node runtime. It may be nil for
	// commands that only touch the database (e.g. admin bootstrap).
	Runtime node.Runtime
	// Registry is the platform registry; nil means none (see Registry).
	// Remote servers are plugged in with SetNodes.
	Registry Registry
	Logger   *slog.Logger
	// Data is the database panel engine (ADR 0016); nil uses Postgres
	// (internal/data/pg).
	Data data.Engine
	// CutoverDrain overrides DefaultCutoverDrain when positive (tests use
	// a tiny value).
	CutoverDrain time.Duration
}

// Core is the control plane.
type Core struct {
	cfg   config.Config
	store *store.Store
	box   *secrets.Box
	rt    node.Runtime
	nodes Nodes // nil: no remote servers
	reg   Registry
	log   *slog.Logger
	bus   *bus
	local store.Server
	drain time.Duration
	// install is this control plane's install ID, the dock.control_plane
	// label on everything it creates (see labels and foreign).
	install string

	// servers is the per-server state of remote servers and builders
	// (servers.go), by server ID.
	serversMu sync.Mutex
	servers   map[int64]*serverState
	// images is registry and image upkeep (images.go).
	images imageUpkeep
	// domainKick wakes the domain checker.
	domainKick chan struct{}
	// checks holds each domain's latest DNS check, by domain ID. It is
	// only in memory: pending domains are checked again soon after a
	// restart, active ones no longer matter.
	checksMu sync.Mutex
	checks   map[int64]domainCheck

	// routesMu serialises render-and-apply, so a slow render can't
	// overwrite a newer routing table.
	routesMu sync.Mutex

	statusMu sync.Mutex
	status   map[int64]apitypes.ServiceStatus // by service ID
	// removing holds the IDs of containers the daemon itself is stopping
	// and removing, so their die and destroy events aren't taken for a
	// crash or drift. An ID leaves the set on its destroy event.
	removing map[string]bool

	// loginLimit throttles password attempts per client IP and per email;
	// bcryptSem caps concurrent password checks (each is ~50ms of CPU).
	loginLimit *limiter
	bcryptSem  chan struct{}

	// stats holds the latest resource samples and the live samplers.
	stats *statsState
	// cliLogins wakes CLIs waiting on browser logins (cli_login.go).
	cliLogins *cliLogins

	// data is the database panel engine; browse throttles data.browse
	// audit events.
	data     data.Engine
	browse   browseLog
	deciding decisions

	// uploads holds the services with a source upload streaming to disk.
	uploadsMu sync.Mutex
	uploads   map[int64]bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates the control plane and makes sure the local server is
// registered. Call Start to recover state and begin background work.
func New(ctx context.Context, o Options) (*Core, error) {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	for _, dir := range []string{"builds", "uploads"} {
		if err := os.MkdirAll(filepath.Join(o.Config.DataDir, dir), 0o700); err != nil {
			return nil, err
		}
	}
	c := &Core{
		cfg:        o.Config,
		store:      o.Store,
		box:        o.Secrets,
		rt:         o.Runtime,
		reg:        o.Registry,
		stats:      newStatsState(),
		log:        o.Logger,
		bus:        newBus(),
		servers:    map[int64]*serverState{},
		domainKick: make(chan struct{}, 1),
		checks:     map[int64]domainCheck{},
		status:     map[int64]apitypes.ServiceStatus{},
		removing:   map[string]bool{},
		uploads:    map[int64]bool{},
		loginLimit: newLimiter(5, 10*time.Second),
		bcryptSem:  make(chan struct{}, 2),
		cliLogins:  newCLILogins(),
		drain:      DefaultCutoverDrain,
		data:       o.Data,
	}
	if c.data == nil {
		c.data = pg.New()
	}
	if o.CutoverDrain > 0 {
		c.drain = o.CutoverDrain
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	var err error
	if c.install, err = c.store.GetInstallID(ctx); err != nil {
		return nil, fmt.Errorf("read install ID: %w", err)
	}
	if err := c.registerLocalServer(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Core) registerLocalServer(ctx context.Context) error {
	now := time.Now().UTC()
	srv, err := c.store.GetLocalServer(ctx)
	if store.IsNotFound(err) {
		name, _ := os.Hostname()
		if name == "" {
			name = "local"
		}
		// The first server takes every kind of environment. Once a
		// second one joins, an admin decides which kinds go where.
		srv, err = c.store.CreateServer(ctx, store.CreateServerParams{
			Name: name, AcceptsProduction: true, AcceptsNonProduction: true,
			Builder: c.cfg.BuildKit.Enabled, Local: true, LastSeenAt: &now, Version: version.Version,
		})
	} else if err == nil {
		srv, err = c.store.TouchLocalServer(ctx, store.TouchLocalServerParams{
			Builder: c.cfg.BuildKit.Enabled, LastSeenAt: &now, Version: version.Version, ID: srv.ID,
		})
	}
	if err != nil {
		return fmt.Errorf("register local server: %w", err)
	}
	c.local = srv
	return nil
}

// Start recovers from a crash or restart (failing interrupted deploys),
// reconciles the DB against the runtime, re-applies routes and starts the
// runtime event watcher and the domain checker.
func (c *Core) Start(ctx context.Context) error {
	if c.rt == nil {
		return errors.New("core: no runtime")
	}
	c.clearTempFiles()
	if err := c.recover(ctx); err != nil {
		return fmt.Errorf("recover: %w", err)
	}
	if err := c.reconcileServer(ctx, c.local.ID, c.rt, true); err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	if err := c.applyRoutes(ctx, c.local.ID); err != nil {
		// Caddy keeps its previous config; deploys will retry.
		c.log.Error("apply routes on start", "err", err)
	}
	events, err := c.rt.Events(c.ctx)
	if err != nil {
		return fmt.Errorf("runtime events: %w", err)
	}
	c.wg.Add(3)
	go c.watchRuntime(events)
	go c.checkDomains()
	go c.collectStats()
	// Remote servers are reconciled as they connect (NodeStatus); ones a
	// hub that already serves has connected are reconciled now.
	if c.nodes != nil {
		servers, err := c.store.ListServers(ctx)
		if err != nil {
			return err
		}
		for _, s := range servers {
			if !s.Local && c.online(s.ID) {
				c.kickServer(s.ID)
			}
		}
	}
	return nil
}

// clearTempFiles removes source uploads and unpacked build contexts left by
// deploys that a crash or restart cut short; only the deploy that created
// them deletes them otherwise. Nothing can be using them yet: Start runs
// before the API serves (and not in admin commands, which use New alone).
// builds/work is where the local builder unpacks contexts (internal/cli).
func (c *Core) clearTempFiles() {
	for _, pattern := range []string{
		filepath.Join(c.cfg.DataDir, "uploads", "*"),
		filepath.Join(c.cfg.DataDir, "builds", "work", "build-*"),
	} {
		paths, _ := filepath.Glob(pattern)
		for _, p := range paths {
			if err := os.RemoveAll(p); err != nil {
				c.log.Warn("remove temp file", "path", p, "err", err)
			}
		}
	}
}

// Close abandons in-flight deploys and waits for background work to finish.
// It never stops or waits on app containers: the next Start's recovery
// fails abandoned releases and removes their containers. Database panel
// connections are closed.
func (c *Core) Close() {
	c.closeStats()
	// Under serversMu, so kickServer can't start a watcher after this.
	c.serversMu.Lock()
	c.cancel()
	c.serversMu.Unlock()
	c.wg.Wait()
	c.data.Close()
}

func now() time.Time { return time.Now().UTC() }

// audit records a mutating action and publishes it. Failures are logged,
// never returned: the action already happened.
func (c *Core) audit(ctx context.Context, a Actor, action string, p *store.Project, e *store.Environment, target, summary string) {
	params := store.AddAuditEventParams{
		Actor: a.Name, ActorKind: a.Kind, Action: action, Target: target, Summary: summary, CreatedAt: now(),
	}
	if a.UserID != 0 {
		params.UserID = &a.UserID
	}
	if a.TokenID != 0 {
		params.TokenID = &a.TokenID
	}
	var pid, eid int64
	if p != nil {
		pid = p.ID
		params.ProjectID = &pid
	}
	if e != nil {
		eid = e.ID
		params.EnvironmentID = &eid
	}
	row, err := c.store.AddAuditEvent(ctx, params)
	if err != nil {
		c.log.Error("write audit event", "action", action, "err", err)
		return
	}
	ae := apitypes.AuditEvent{
		ID: row.ID, Actor: row.Actor, ActorKind: row.ActorKind, Action: row.Action,
		Target: row.Target, Summary: row.Summary, CreatedAt: row.CreatedAt,
	}
	if p != nil {
		ae.Project = p.Name
	}
	if e != nil {
		ae.Environment = e.Name
	}
	c.bus.publish(busEvent{projectID: pid, envID: eid, ev: apitypes.Event{
		Type: apitypes.EventAudit, Project: ae.Project, Environment: ae.Environment, Audit: &ae,
	}})
}
