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
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sync/singleflight"

	"github.com/NordTempo/tempoDock/internal/acmedns"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/cpbackup"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/mysql"
	"github.com/NordTempo/tempoDock/internal/data/pg"
	"github.com/NordTempo/tempoDock/internal/data/redis"
	"github.com/NordTempo/tempoDock/internal/httpkit"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/update"
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
	// DataEngines are the database panel's engines (ADR 0016), one per
	// kind; nil uses Postgres, MySQL and Redis (internal/data/pg,
	// data/mysql, data/redis).
	DataEngines []data.Engine
	// CutoverDrain overrides DefaultCutoverDrain when positive (tests use
	// a tiny value).
	CutoverDrain time.Duration
	// UptimeSecond, when positive, is how long a second of an uptime
	// check's interval lasts (ADR 0055), so tests probe every few
	// milliseconds.
	UptimeSecond time.Duration
	// ScanQuiet, when positive, replaces [scan] quiet_seconds: how long
	// nothing may be busy before an image scan starts (ADR 0074), so
	// tests don't wait a minute.
	ScanQuiet time.Duration
	// PasswordCost, when positive, is the bcrypt cost of new password
	// hashes instead of bcrypt.DefaultCost; tests use bcrypt.MinCost so
	// creating and logging in users doesn't take ~50 ms each.
	PasswordCost int
	// GitHubAPI overrides the GitHub REST API's base URL
	// (https://api.github.com) for push-to-deploy; tests point it at a
	// fake.
	GitHubAPI string
	// CloudflareAPI overrides Cloudflare's API base URL (ADR 0036); tests
	// point it at a fake.
	CloudflareAPI string
	// FirewallApplied reports whether the app firewall's rules are in on
	// the control plane's own server (firewall.Applied); Start asks once
	// and raises an alert when they aren't. Nil where no firewall is
	// expected (the fake runtime, a daemon outside systemd): no alert.
	FirewallApplied func() (bool, error)
	// ConfigPath is the daemon config file, archived by the control
	// plane's own backup (ADR 0021); empty leaves it out.
	ConfigPath string
	// Recovery puts the control plane in recovery mode (ADR 0098): the
	// data dir's recovery file, read by `dock serve`. Nil is the normal
	// mode.
	Recovery *cpbackup.Recovery
	// Updates is the release source (ADR 0028); nil uses [updates] in
	// Config. UpdaterUnit overrides update.PathUnit, whose existence says
	// the updater is installed. Tests set both.
	Updates     update.Source
	UpdaterUnit string
	// ObtainCertificate gets a base domain's wildcard certificate (ADR
	// 0090) and WaitTXT waits for its challenge records to be visible;
	// nil uses acmedns. ACMEClient talks to the CA; nil is
	// http.DefaultClient. Tests stand in for the CA.
	ObtainCertificate func(context.Context, acmedns.Request) (acmedns.Certificate, error)
	WaitTXT           func(ctx context.Context, zone, name string, values []string) error
	ACMEClient        *http.Client
	// LookupTXT looks up a name's TXT records, for custom domain
	// verification (ADR 0042); nil uses the system resolver. Tests answer
	// from a map.
	LookupTXT func(ctx context.Context, name string) ([]string, error)
	// LookupIP resolves a hostname, to tell whether a new domain is
	// public before Dock adds an uptime check for it (ADR 0059) and
	// whether servers may pull from a registry (checkRegistry); nil uses
	// the system resolver.
	LookupIP func(ctx context.Context, host string) ([]netip.Addr, error)
}

// Core is the control plane.
type Core struct {
	// replay runs approved access requests' calls (requests.go).
	replays replayers
	cfg     config.Config
	// configPath is Options.ConfigPath.
	configPath string
	// firewallApplied is Options.FirewallApplied.
	firewallApplied func() (bool, error)
	store           *store.Store
	box             *secrets.Box
	rt              node.Runtime
	nodes           Nodes // nil: no remote servers
	reg             Registry
	log             *slog.Logger
	bus             *bus
	// renameMu keeps committed renames and their live events in order.
	renameMu sync.Mutex
	// setupMu makes ensureDataAccess one at a time.
	setupMu sync.Mutex
	// localID is the control plane's own server. Its name is read with
	// localName (a rename changes it), anything else from the store.
	localID  int64
	localNow atomic.Pointer[string]
	// platformPolicy is the platform's data access default and ceiling
	// (platform_policy.go), loaded on start and after each change.
	platformPolicy atomic.Pointer[platformPolicy]
	drain          time.Duration
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
	// checking holds, by domain ID, the check of a domain running now,
	// closed when it ends (holdDomainCheck); guarded by checksMu.
	checking map[int64]chan struct{}
	// lookupTXT is Options.LookupTXT, or the system resolver's.
	lookupTXT func(ctx context.Context, name string) ([]string, error)
	// lookupIP is Options.LookupIP, or the system resolver's.
	lookupIP func(ctx context.Context, host string) ([]netip.Addr, error)
	// pulls refuses registries servers may not pull from (checkRegistry).
	pulls egressGuard

	// routesMu serialises render-and-apply, so a slow render can't
	// overwrite a newer routing table.
	routesMu sync.Mutex

	statusMu sync.Mutex
	status   map[int64]storedStatus // by service ID
	// statusSets counts setStatus calls, for container events read
	// before a service's last one (changeStatusSince).
	statusSets uint64
	// removing holds the IDs of containers the daemon itself is stopping
	// and removing, so their die and destroy events aren't taken for a
	// crash or drift. An ID leaves the set on its destroy event, or when
	// its removal fails; its channel is closed then, for whoever waits on
	// the removal (removalOf).
	removing map[string]chan struct{}
	// health follows the services' ongoing health checks (health.go).
	health healthWatch

	// loginLimit throttles password attempts per client IP, per (email,
	// IP) and, more loosely, per email (httpkit.LoginLimiter); bcryptSem
	// caps concurrent password checks (each is ~50ms of CPU) and
	// bcryptWaiting counts the callers waiting for one of its slots
	// (bcryptSlot).
	loginLimit    *httpkit.LoginLimiter
	bcryptSem     chan struct{}
	bcryptWaiting atomic.Int32
	// passwordCost is the bcrypt cost of new password hashes
	// (Options.PasswordCost).
	passwordCost int

	// stats holds the latest resource samples and the live samplers.
	stats *statsState
	// alerts is the alert engine (alerts.go); retention the state of
	// retention passes and disk prunes (retention.go).
	alerts    *alertEngine
	retention *retention
	// logDrains are the log drains and the log followers
	// (drains_deliver.go).
	logDrains *drainManager
	// issues is the error sink the statistics tick writes as error
	// issues (errors_ingest.go).
	issues errorSink
	// stops is when failed releases' containers began to stop (errors_ingest.go).
	stops releaseStops
	// cliLogins wakes CLIs waiting on browser logins (cli_login.go).
	cliLogins *cliLogins
	// streams counts the event streams open (streams.go).
	streams streamCounts
	// access is what actors hold open, ended when their access changes
	// (access_watch.go).
	access accessWatch
	// ceremonies are the passkey challenges and authenticator app setups
	// in flight; operators2FA is the platform's requirement of a second
	// factor of operators (twofactor.go, passkeys.go). Second factors
	// spend from loginLimit like passwords.
	ceremonies   *ceremonies
	operators2FA atomic.Bool
	// sso is single sign-on's flows in flight and the HTTP client that
	// talks to identity providers (sso_signin.go).
	sso ssoState

	// data are the database panel's engines and panel the shared panel
	// over them, with Dock as its host (data_host.go).
	data  []data.Engine
	panel *dataPanel
	// tunnels are the open `dock connect` tunnels (data_connect.go).
	tunnels tunnelSet

	// backups is the backup scheduler's kick and the runs in flight
	// (backups.go).
	backups backupState
	// recovery is set while the control plane is in recovery mode
	// (recovery_mode.go).
	recovery atomic.Pointer[cpbackup.Recovery]
	// recoveryRun is the recovery's run and its report (recovery_run.go).
	recoveryRun recoveryRun
	pitr        pitrState
	// volumes keeps restores apart from the other writers of a service's
	// volumes (volume_writers.go).
	volumes volumeWriters
	// runs counts one-off commands in flight (runs.go).
	runs runState
	// deploys holds the deploys running here, to cancel one (deploy_cancel.go).
	deploys deployRuns
	// uptime is the uptime checks' scheduler kick and state (uptime.go).
	uptime uptimeState
	// sleep holds the environments that sleep (sleep.go).
	sleep sleepState
	// jobs is the job scheduler's kick and the job runs in flight
	// (jobs.go, jobs_schedule.go).
	jobs jobState
	// scans is the image scanner's state (scans.go, ADR 0074).
	scans scanState
	// enforceGrants: people's groups and grants decide their checks (ADR
	// 0080's enforce); false, their roles do and the model watches.
	enforceGrants atomic.Bool
	// agentRules caches each org's agent rules (orgRules), which every
	// agent's request needs and admins rarely change: one entry per org.
	// rulesGen counts changes, so a read that raced one isn't kept.
	agentRules sync.Map
	rulesGen   atomic.Int64
	// email is the outbox's sender (email_send.go, ADR 0079).
	email emailState
	// moves holds the environment moves being driven (ADR 0053).
	moves moveState
	// fwd holds the listeners of moved services' forwards (ADR 0063).
	fwd forwardState
	// drains are the server drains being driven (ADR 0063).
	drains serverDrainState

	// uploads holds the services with a source upload streaming to disk,
	// each with its organization.
	uploadsMu sync.Mutex
	uploads   map[int64]int64

	// git is GitHub push-to-deploy's state (git.go).
	git gitState
	// previews is the state of preview environments (previews.go).
	previews previewState
	// tracking holds the newest checks of sites' pages (site_tracking.go).
	tracking *siteChecks
	// analytics is Dock Analytics' state (analytics.go).
	analytics analyticsState
	// traces is the trace store (traces.go).
	traces tracesState
	// statusPages is which hostname is which status page (status.go).
	statusPages statusState
	// maint is the status pages' maintenance windows (ADR 0073).
	maint maintState
	// updates is updating Dock (update.go).
	updates updates
	// address is Dock's public address, "" for none (address.go).
	address atomic.Pointer[string]
	// cf is the Cloudflare integration (cloudflare.go).
	cf cfState
	// wild is the base domains' wildcard certificates (wildcards.go).
	wild wildState
	// obtain gets a wildcard certificate, waitTXT waits for its
	// challenge records and acmeHTTP talks to the CA: Options'.
	obtain   func(context.Context, acmedns.Request) (acmedns.Certificate, error)
	waitTXT  func(ctx context.Context, zone, name string, values []string) error
	acmeHTTP *http.Client

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// diskReads shares one disk read (ServerDisk) per server between
	// concurrent callers; disk keeps each server's last reading.
	diskReads singleflight.Group
	disk      diskState
}

// checkRuntime refuses a runtime that would panic on its first call
// instead of failing: a nil pointer in the interface (rt != nil, yet
// every method dereferences nil) or a node.Local missing a part. Without
// a runtime at all (admin commands) Core does no runtime work; Start
// refuses it.
func checkRuntime(rt node.Runtime) error {
	if rt == nil {
		return nil
	}
	if v := reflect.ValueOf(rt); v.Kind() == reflect.Pointer && v.IsNil() {
		return fmt.Errorf("core: the runtime is a nil %T", rt)
	}
	if l, ok := rt.(node.Local); ok {
		if m := l.Missing(); len(m) > 0 {
			return fmt.Errorf("core: the local runtime has no %s", strings.Join(m, ", "))
		}
	}
	return nil
}

// New creates the control plane and makes sure the local server is
// registered. Call Start to recover state and begin background work.
func New(ctx context.Context, o Options) (*Core, error) {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if err := checkRuntime(o.Runtime); err != nil {
		return nil, err
	}
	shadowLog.Store(o.Logger)
	for _, dir := range []string{"builds", "uploads", "requests", "sboms"} {
		if err := os.MkdirAll(filepath.Join(o.Config.DataDir, dir), 0o700); err != nil {
			return nil, err
		}
	}
	c := &Core{
		cfg:             o.Config,
		configPath:      o.ConfigPath,
		firewallApplied: o.FirewallApplied,
		store:           o.Store,
		box:             o.Secrets,
		rt:              o.Runtime,
		reg:             o.Registry,
		stats:           newStatsState(),
		alerts:          newAlertEngine(o.Config.Network.AllowPrivate),
		logDrains:       newDrainManager(o.Config.Network.AllowPrivate),
		retention:       newRetention(),
		log:             o.Logger,
		bus:             newBus(),
		servers:         map[int64]*serverState{},
		domainKick:      make(chan struct{}, 1),
		checks:          map[int64]domainCheck{},
		checking:        map[int64]chan struct{}{},
		lookupTXT:       o.LookupTXT,
		lookupIP:        o.LookupIP,
		status:          map[int64]storedStatus{},
		removing:        map[string]chan struct{}{},
		health:          newHealthWatch(),
		uploads:         map[int64]int64{},
		loginLimit:      httpkit.NewLoginLimiter(),
		bcryptSem:       make(chan struct{}, 2),
		cliLogins:       newCLILogins(),
		ceremonies:      newCeremonies(),
		sso:             newSSOState(),
		backups:         newBackupState(),
		volumes:         newVolumeWriters(),
		uptime:          newUptimeState(),
		runs:            runState{running: map[int64]int{}, ended: map[int64]chan struct{}{}},
		jobs:            newJobState(),
		scans:           newScanState(),
		email:           newEmailState(),
		moves:           newMoveState(),
		fwd:             newForwardState(),
		drains:          newServerDrainState(),
		drain:           DefaultCutoverDrain,
		data:            o.DataEngines,
		git:             newGitState(o.GitHubAPI),
		previews:        newPreviewState(),
		tracking:        newSiteChecks(o.Config.Network.AllowPrivate),
		updates:         newUpdates(o),
		cf:              newCFState(o.CloudflareAPI),
		wild:            newWildState(),
		obtain:          o.ObtainCertificate,
		waitTXT:         o.WaitTXT,
		acmeHTTP:        o.ACMEClient,
	}
	if c.obtain == nil {
		c.obtain = acmedns.Obtain
	}
	if c.waitTXT == nil {
		c.waitTXT = acmedns.WaitTXT
	}
	if c.data == nil {
		c.data = []data.Engine{pg.New(pg.Options{}), mysql.New(mysql.Options{}), redis.New(redis.Options{})}
	}
	if c.lookupTXT == nil {
		c.lookupTXT = net.DefaultResolver.LookupTXT
	}
	c.pulls.what, c.pulls.private, c.pulls.allow = "image pulls", true, o.Config.Network.AllowPrivate
	if c.lookupIP == nil {
		c.lookupIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	c.panel = newDataPanel(c)
	if o.CutoverDrain > 0 {
		c.drain = o.CutoverDrain
	}
	c.scans.quiet = o.ScanQuiet
	if o.UptimeSecond > 0 {
		c.uptime.second = o.UptimeSecond
	}
	c.passwordCost = bcrypt.DefaultCost
	if o.PasswordCost > 0 {
		c.passwordCost = o.PasswordCost
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	var err error
	if c.install, err = c.store.GetInstallID(ctx); err != nil {
		return nil, fmt.Errorf("read install ID: %w", err)
	}
	if err := c.resealSecrets(ctx); err != nil {
		return nil, fmt.Errorf("bind sealed values to where they are stored (ADR 0041): %w", err)
	}
	if err := c.resealSSOSecrets(ctx); err != nil {
		return nil, fmt.Errorf("reseal identity providers' client secrets (ADR 0065): %w", err)
	}
	if err := c.registerLocalServer(ctx); err != nil {
		return nil, err
	}
	if err := c.loadPlatformSecurity(ctx); err != nil {
		return nil, err
	}
	if err := c.loadPublicHost(ctx); err != nil {
		return nil, err
	}
	if err := c.loadCloudflare(ctx); err != nil {
		return nil, fmt.Errorf("load Cloudflare state: %w", err)
	}
	if err := c.loadWildcards(ctx); err != nil {
		return nil, err
	}
	// Held deploys run again through core itself (held.go).
	c.SetReplayer("source", c.replaySource)
	c.SetReplayer("git", c.replayGit)
	c.recovery.Store(o.Recovery)
	return c, nil
}

func (c *Core) registerLocalServer(ctx context.Context) error {
	now := time.Now().UTC()
	srv, err := c.store.GetLocalServer(ctx)
	if store.IsNotFound(err) {
		srv, err = c.createLocalServer(ctx, now)
	} else if err == nil {
		srv, err = c.store.TouchLocalServer(ctx, store.TouchLocalServerParams{
			Builder: c.cfg.BuildKit.Enabled, LastSeenAt: &now, Version: version.Version, ID: srv.ID,
		})
	}
	if err != nil {
		return fmt.Errorf("register local server: %w", err)
	}
	c.localID = srv.ID
	c.localNow.Store(&srv.Name)
	return nil
}

// localName is the control plane's own server's current name.
func (c *Core) localName() string { return *c.localNow.Load() }

// createLocalServer registers this server, named after its host. The
// first server takes every kind of environment; once a second one joins,
// an admin decides which kinds go where. A control plane restored next to
// its old server (ADR 0021) can find the name taken by that server, now
// waiting to join as a node: then it is "<host>-2", and so on.
func (c *Core) createLocalServer(ctx context.Context, now time.Time) (store.Server, error) {
	name, _ := os.Hostname()
	if name == "" {
		name = "local"
	}
	params := store.CreateServerParams{
		Name: name, AcceptsProduction: true, AcceptsNonProduction: true,
		Builder: c.cfg.BuildKit.Enabled, Local: true, LastSeenAt: &now, Version: version.Version,
	}
	for i := 2; ; i++ {
		srv, err := c.store.CreateServer(ctx, params)
		if !store.IsUnique(err) || i > 9 {
			return srv, err
		}
		params.Name = fmt.Sprintf("%s-%d", name, i)
	}
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
	if err := c.recoverRequests(ctx); err != nil {
		return fmt.Errorf("recover access requests: %w", err)
	}
	if err := c.failInterruptedBackups(ctx); err != nil {
		return fmt.Errorf("recover backups: %w", err)
	}
	if err := c.failInterruptedSnapshots(ctx); err != nil {
		return fmt.Errorf("recover snapshots: %w", err)
	}
	if err := c.interruptRuns(ctx); err != nil {
		return fmt.Errorf("recover runs: %w", err)
	}
	// Orgs from before ADR 0080's groups get theirs before anyone acts.
	if err := c.seedAccess(ctx); err != nil {
		return fmt.Errorf("seed groups: %w", err)
	}
	// Org tokens from before org agents get their grants (ADR 0080, 6b).
	if err := c.grantTokens(ctx); err != nil {
		return fmt.Errorf("grant tokens: %w", err)
	}
	c.loadAccessModel(ctx)
	c.applyRegistryStorage(ctx)
	if err := c.loadPITR(ctx); err != nil {
		return fmt.Errorf("point-in-time restore: %w", err)
	}
	if err := c.convertBackupRepository(ctx); err != nil {
		// Backups keep using the URL as before; the next start tries again.
		c.log.Error("turn the backup repository into a storage connection", "err", err)
	}
	if err := c.reconcileServer(ctx, c.localID, c.rt, !c.Recovering()); err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	if err := c.startSleep(ctx); err != nil {
		return fmt.Errorf("sleeping environments: %w", err)
	}
	// Status pages' addresses, before the routes that serve them.
	if hosts, err := c.loadStatusHosts(ctx); err == nil {
		c.statusPages.hosts.Store(&hosts)
	} else {
		c.log.Error("status pages: read the pages; they aren't served until one changes", "err", err)
	}
	if err := c.applyRoutes(ctx, c.localID); err != nil {
		// Caddy keeps its previous config; deploys will retry.
		c.log.Error("apply routes on start", "err", err)
	}
	events, err := c.rt.Events(c.ctx)
	if err != nil {
		return fmt.Errorf("runtime events: %w", err)
	}
	if err := c.loadPlatformPolicy(ctx); err != nil {
		return err
	}
	// Maintenance windows before the alerts, which they may hold.
	if err := c.loadMaintenance(ctx); err != nil {
		c.log.Error("maintenance: read the windows; none is applied until one changes", "err", err)
	}
	if err := c.startAlerts(ctx); err != nil {
		return err
	}
	// Alerts a window held while Dock was down, if it has ended.
	c.releaseHeld(ctx, time.Now())
	c.signalRecovery(ctx)
	if err := c.startLogDrains(ctx); err != nil {
		return err
	}
	c.loadLastUpdate()
	c.UpdateFinished(ctx)
	if err := c.startAnalytics(ctx); err != nil {
		// Apps are served either way; only analytics waits.
		c.log.Error("start analytics", "err", err)
	} else if cl, _ := c.tracesClient(); cl != nil {
		// The routes went out above, before tracing's key was read: again,
		// with their edge traces (ADR 0075).
		if err := c.applyRoutes(ctx, c.localID); err != nil {
			c.log.Error("apply routes with edge traces on start", "err", err)
		}
	}
	c.wg.Add(8)
	c.goCaught("watching containers", func() { c.watchRuntime(events) })
	c.goLoop("checking domains", c.checkDomains)
	c.goCaught("collecting statistics", func() { c.collectStats() })
	c.goCaught("scheduling backups", func() { c.scheduleBackups() })
	c.goLoop("scheduling uptime checks", c.scheduleUptime)
	// Jobs left due by a stop run now; without jobs it exits at once.
	c.kickJobs()
	// Scans wait for a quiet minute; with nothing queued it exits.
	c.startScans(ctx)
	// A new builder, or one updated to other Railpack images, fetches
	// them now rather than in its first build.
	c.warmBuilder(ctx, c.localID)
	c.checkLocalFirewall(ctx)
	// Mail queued before a stop goes now; with none the sender exits.
	c.email.mu.Lock()
	c.email.started = true
	c.email.mu.Unlock()
	c.kickEmail()
	c.goCaught("working on Cloudflare", func() { c.runCloudflare() })
	c.goCaught("keeping wildcard certificates", func() { c.runWildcards() })
	// In the background: running the tunnel may pull an image.
	c.goCaught("running the control plane's tunnel", func() {
		defer c.wg.Done()
		c.runServerTunnel(c.ctx, c.localID, c.rt)
		if srv, err := c.store.GetServer(c.ctx, c.localID); err == nil {
			c.syncServerRecords(c.ctx, srv)
		}
	})
	if err := c.resumeGit(ctx); err != nil {
		return fmt.Errorf("resume queued pushes: %w", err)
	}
	if err := c.resumePreviews(ctx); err != nil {
		return fmt.Errorf("archive closed previews: %w", err)
	}
	// Preview copies a restart cut short end empty (ADR 0092); in the
	// background, as they run jobs.
	c.wg.Go(func() {
		if err := c.resumePreviewData(c.ctx); err != nil {
			c.log.Error("end preview copies cut short", "err", err)
		}
	})
	// A move a restart cut short rolls back, or finishes its clean-up.
	c.resumeMoves(ctx, true)
	// This server's forwards of services that moved away listen again,
	// and drains go on (ADR 0063).
	c.ensureForwards(ctx, c.localID)
	c.resumeServerDrains(ctx)
	// Remote servers are reconciled as they connect (NodeStatus); ones a
	// hub that already serves has connected are reconciled now.
	if c.nodes != nil {
		servers, err := c.store.ListServers(ctx)
		if err != nil {
			return err
		}
		for _, s := range servers {
			if s.RevokedAt != nil {
				c.setRevoked(s.ID, true)
			}
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
// fails abandoned releases and removes their containers. Open `dock
// connect` tunnels and database panel connections are closed.
func (c *Core) Close() {
	c.tunnels.closeAll()
	c.closeStats()
	// Under serversMu, git.mu, the live lock and the log drains' lock, so
	// kickServer, kickGit, reloadSite and followContainer can't start a
	// goroutine after this.
	c.serversMu.Lock()
	c.git.mu.Lock()
	c.analytics.live.mu.Lock()
	c.logDrains.mu.Lock()
	c.cancel()
	c.logDrains.mu.Unlock()
	c.analytics.live.mu.Unlock()
	c.git.mu.Unlock()
	c.serversMu.Unlock()
	c.wg.Wait()
	c.panel.Close()
}

func now() time.Time { return time.Now().UTC() }

// audit records a mutating action and publishes it. Failures are logged,
// never returned: the action already happened. It happened in the
// project's org, else in the actor's; the system's actions outside a
// project are the platform's.
func (c *Core) audit(ctx context.Context, a Actor, action string, p *store.Project, e *store.Environment, target, summary string) {
	if a.approval != nil {
		// The call an approved access request runs (ADR 0044).
		summary += fmt.Sprintf(" (access request #%d, approved by %s)", a.approval.id, a.approval.by)
	}
	params := store.AddAuditEventParams{
		Actor: a.Name, ActorKind: a.Kind, Action: action, Target: target, Summary: summary, CreatedAt: now(),
	}
	oid := a.OrgID
	if p != nil {
		oid = p.OrgID
	}
	if oid != 0 {
		params.OrgID = &oid
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
	var kind uint8
	if e != nil {
		ae.Environment, kind = e.Name, envKind(*e)
	}
	c.bus.publish(busEvent{orgID: oid, projectID: pid, envID: eid, envKind: kind, ev: apitypes.Event{
		Type: apitypes.EventAudit, Project: ae.Project, Environment: ae.Environment, Audit: &ae,
	}})
}
