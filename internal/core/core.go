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

	"github.com/NordTempo/platform/internal/apitypes"
	"github.com/NordTempo/platform/internal/config"
	"github.com/NordTempo/platform/internal/node"
	"github.com/NordTempo/platform/internal/secrets"
	"github.com/NordTempo/platform/internal/store"
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
	Logger  *slog.Logger
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
	log   *slog.Logger
	bus   *bus
	local store.Server
	drain time.Duration

	// buildSem allows one build at a time on this server.
	buildSem chan struct{}
	// domainKick wakes the domain checker.
	domainKick chan struct{}

	// routesMu serialises render-and-apply, so a slow render can't
	// overwrite a newer routing table.
	routesMu sync.Mutex

	statusMu sync.Mutex
	status   map[int64]apitypes.ServiceStatus // by service ID

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
		log:        o.Logger,
		bus:        newBus(),
		buildSem:   make(chan struct{}, 1),
		domainKick: make(chan struct{}, 1),
		status:     map[int64]apitypes.ServiceStatus{},
		drain:      DefaultCutoverDrain,
	}
	if o.CutoverDrain > 0 {
		c.drain = o.CutoverDrain
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
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
		// The control plane lives on the production server (ADR 0010).
		srv, err = c.store.CreateServer(ctx, store.CreateServerParams{
			Name: name, Role: "production", Builder: c.cfg.BuildKit.Enabled, Local: true, LastSeenAt: &now,
		})
	} else if err == nil {
		srv, err = c.store.TouchLocalServer(ctx, store.TouchLocalServerParams{Builder: c.cfg.BuildKit.Enabled, LastSeenAt: &now, ID: srv.ID})
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
	if err := c.recover(ctx); err != nil {
		return fmt.Errorf("recover: %w", err)
	}
	if err := c.reconcile(ctx); err != nil {
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
	c.wg.Add(2)
	go c.watchRuntime(events)
	go c.checkDomains()
	return nil
}

// Close abandons in-flight deploys and waits for background work to finish.
// It never stops or waits on app containers: the next Start's recovery
// fails abandoned releases and removes their containers.
func (c *Core) Close() {
	c.cancel()
	c.wg.Wait()
}

// runtime returns the node runtime for a server. Only the local server
// exists until remote nodes land (M3).
func (c *Core) runtime(int64) node.Runtime { return c.rt }

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
