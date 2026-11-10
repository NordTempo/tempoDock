package core

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Log followers and log drain delivery (ADR 0046, ADR 0062). Each
// running service container has a follower: one runtime Logs stream
// (through the node link for a remote server) that ends with the
// container's output. Followers start from container start events, when
// the daemon starts, when a remote server reconnects or resyncs and when
// an environment gets its first drain. Every line is read for errors
// (errors_logs.go: a byte scan, no allocation for a plain line); in an
// environment with drains it also goes into a bounded queue per drain
// without blocking, and a full queue drops the line and counts it. Each
// drain with lines queued has one sender goroutine that posts them in
// batches and exits once the queue is empty.
//
// A remote server's followers share its node link's HTTP/2 connection,
// one stream each, so at most maxServerFollows containers of a server
// are followed for errors alone; containers of environments with drains
// are always followed.

const (
	// drainQueueLines and drainQueueBytes bound what waits for one drain:
	// past either, new lines are dropped and counted. The byte bound
	// keeps a burst of long lines from costing the daemon tens of MB.
	drainQueueLines = 10_000
	drainQueueBytes = 4 << 20
	// drainBatchLines and drainBatchBytes bound one delivery.
	drainBatchLines = 1000
	drainBatchBytes = 1 << 20
	// drainSendTimeout bounds one delivery attempt.
	drainSendTimeout = 15 * time.Second
	// maxDrainResume bounds the containers whose last read line is
	// remembered (drainManager.last).
	maxDrainResume = 10_000
	// maxServerFollows bounds a server's followers outside environments
	// with drains: each is a stream on the server's node link, whose
	// HTTP/2 connection allows 250 at once.
	maxServerFollows = 150
)

// drainBatchWait is how long a sender waits after the first line of a
// batch for more to arrive, unless the batch fills first. A variable so
// tests can wait less.
var drainBatchWait = time.Second

// drainManager holds the drains and the log followers.
type drainManager struct {
	// guard keeps deliveries off this server and metadata addresses;
	// client is the HTTP client http and Loki drains post with.
	guard  egressGuard
	client *http.Client

	mu sync.Mutex
	// envs holds the environments with a drain or a followed container,
	// by ID; one goes when it has neither.
	envs map[int64]*drainEnv
	// byID holds every drain.
	byID map[int64]*drain
	// follows holds the containers being followed, by container ID.
	follows map[string]*follower
	// perServer counts each server's followers, against maxServerFollows.
	perServer map[int64]int
	// last is when the last line read from a container whose follower
	// ended was written, so a restart of the container resumes after it.
	// An entry goes with the container's destroy event; at most
	// maxDrainResume are kept.
	last map[string]time.Time
	// restart holds the containers that started again while their old
	// follower still ran, by container ID; that follower starts the next
	// when it ends. An entry exists only beside one in follows.
	restart map[string]drainRestart
}

// follower is one container's follower: its environment (whose drains
// get its lines), its server and what it finds errors with.
type follower struct {
	env    *drainEnv
	server int64
	errs   errorFollower
}

// drainRestart is what followContainer needs to follow a container's
// next run.
type drainRestart struct {
	serverID, releaseID int64
	row                 *store.ListDrainContainersRow
	labels              *node.Labels
}

// drainEnv is an environment with drains or followed containers.
type drainEnv struct {
	id int64
	// drains is replaced, never changed in place, so followers read it
	// without a lock for every line.
	drains atomic.Pointer[[]*drain]
	// followers counts its containers' followers (under drainManager.mu).
	followers int
}

// newDrainEnv starts an environment's entry without drains.
func newDrainEnv(id int64) *drainEnv {
	env := &drainEnv{id: id}
	env.drains.Store(&[]*drain{})
	return env
}

// drained reports whether env has a drain.
func (env *drainEnv) drained() bool { return len(*env.drains.Load()) > 0 }

// drain is one drain's settings, queue and delivery status.
type drain struct {
	id, envID int64
	kind      apitypes.LogDrainKind
	name      string
	cfg       drainConfig
	// ctx ends when the drain is removed or the daemon closes.
	ctx    context.Context
	cancel context.CancelFunc
	// kick wakes the sender when a batch fills.
	kick chan struct{}

	mu      sync.Mutex
	queue   []drainRecord
	queued  int // bytes of text in queue
	running bool
	lastErr string
	errAt   *time.Time
	sentAt  *time.Time

	sent, dropped atomic.Int64

	// conn is a syslog drain's open connection (drains_sink.go).
	conn syslogConn
}

// drainRecord is one line on its way to a drain.
type drainRecord struct {
	Time   time.Time
	Stream string
	Text   string
	src    *logSource
}

// logSource is what a container's lines are labelled with, shared by
// all of them. No server: drains are the org's, and only operators see
// servers (seesServers). ndjson is the end of each http drain line,
// encoded once; host is syslog's HOSTNAME, project.environment; red masks
// the release's secret values (nil: it has none).
type logSource struct {
	Project, Environment, Service string
	Release                       int64
	ndjson                        []byte
	host                          string
	red                           *redactor
	// serviceID is the service whose errors the lines show
	// (errors_logs.go).
	serviceID int64
}

// newLogSource labels a container's lines.
func newLogSource(project, env, service string, release int64) *logSource {
	s := &logSource{Project: project, Environment: env, Service: service, Release: release, host: project + "." + env}
	b := append([]byte(nil), `,"project":`...)
	b = appendJSONString(b, project)
	b = append(b, `,"environment":`...)
	b = appendJSONString(b, env)
	b = append(b, `,"service":`...)
	b = appendJSONString(b, service)
	b = append(b, `,"release":`...)
	b = strconv.AppendInt(b, release, 10)
	s.ndjson = append(b, "}\n"...)
	return s
}

// drainLine turns a container's log line into what drains send: the
// line `dock logs` shows (ServiceLogs) to someone who can't reveal
// secrets, its release's secret values masked. A drain has no actor, so
// it always masks.
func drainLine(l node.LogLine, src *logSource) drainRecord {
	text, _ := src.red.line(l.Text)
	return drainRecord{Time: l.Time, Stream: l.Stream, Text: text, src: src}
}

// releaseLogSource labels and masks the lines of rel's container.
func (c *Core) releaseLogSource(rel store.Release, project, env, service string) (*logSource, error) {
	red, err := c.releaseRedactor(rel)
	if err != nil {
		return nil, err
	}
	src := newLogSource(project, env, service, rel.Number)
	src.red, src.serviceID = red, rel.ServiceID
	return src, nil
}

// newDrainManager starts the drains' state; allow is the private CIDRs
// apps may reach, which drains may then reach too (egressGuard.allow).
func newDrainManager(allow []netip.Prefix) *drainManager {
	m := &drainManager{
		envs: map[int64]*drainEnv{}, byID: map[int64]*drain{},
		follows: map[string]*follower{}, perServer: map[int64]int{}, last: map[string]time.Time{},
		restart: map[string]drainRestart{},
	}
	m.guard.what, m.guard.private, m.guard.allow = "logs", true, allow
	m.client = newDrainClient(&m.guard)
	return m
}

// newDrainClient is the HTTP client http and Loki drains post with. It
// never follows redirects, never uses a proxy (that would connect to the
// proxy, not the drain, past the guard) and never connects to addresses
// the guard refuses (checked after DNS). It keeps one idle connection per host,
// closed by the transport 90 s after the last delivery, so a busy drain
// doesn't handshake for every batch and an idle one holds nothing.
func newDrainClient(g *egressGuard) *http.Client {
	return &http.Client{
		Timeout:       drainSendTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: drainSendTimeout, Control: g.control}).DialContext,
			TLSHandshakeTimeout: drainSendTimeout,
			MaxIdleConnsPerHost: 1,
			IdleConnTimeout:     90 * time.Second,
			ForceAttemptHTTP2:   true,
		},
	}
}

// AllowLoopbackLogDrains lets log drains deliver to this server's own
// addresses, which they never may in production (egressGuard.addrErr).
// For tests whose receivers listen on 127.0.0.1.
func (c *Core) AllowLoopbackLogDrains(allow bool) { c.logDrains.guard.loopback.Store(allow) }

// get returns a drain by ID, or nil.
func (m *drainManager) get(id int64) *drain {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byID[id]
}

// addLogDrain starts a drain and reports whether its environment had
// none before (so containers past maxServerFollows need followers now).
func (c *Core) addLogDrain(r store.LogDrain, cfg drainConfig) (first bool) {
	m := c.logDrains
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.ctx.Err() != nil {
		return false
	}
	d := &drain{
		id: r.ID, envID: r.EnvironmentID, kind: apitypes.LogDrainKind(r.Kind), name: r.Name, cfg: cfg,
		kick: make(chan struct{}, 1),
	}
	d.ctx, d.cancel = context.WithCancel(c.ctx)
	context.AfterFunc(d.ctx, d.conn.close)
	m.byID[d.id] = d
	env := m.envs[d.envID]
	if env == nil {
		env = newDrainEnv(d.envID)
		m.envs[d.envID] = env
	}
	first = !env.drained()
	list := append(append([]*drain(nil), *env.drains.Load()...), d)
	env.drains.Store(&list)
	return first
}

// removeLogDrain stops a drain. The environment's followers keep
// reading its containers for errors.
func (c *Core) removeLogDrain(id int64) {
	m := c.logDrains
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.byID[id]
	if d == nil {
		return
	}
	delete(m.byID, id)
	d.cancel()
	env := m.envs[d.envID]
	if env == nil {
		return
	}
	var list []*drain
	for _, o := range *env.drains.Load() {
		if o != d {
			list = append(list, o)
		}
	}
	env.drains.Store(&list)
	if len(list) == 0 && env.followers == 0 {
		delete(m.envs, env.id)
	}
}

// drainContainerEvent starts following a service container when it
// starts, and forgets a destroyed one. Other containers cost nothing.
func (c *Core) drainContainerEvent(serverID int64, ev node.ContainerEvent) {
	if ev.Labels.Service == "" || ev.Labels.Release == "" {
		return // not a service container (jobs and BuildKit never get here)
	}
	switch ev.Action {
	case "destroy":
		m := c.logDrains
		m.mu.Lock()
		delete(m.last, ev.ContainerID)
		m.mu.Unlock()
	case "start", "restart":
		envID, err1 := strconv.ParseInt(ev.Labels.Environment, 10, 64)
		relID, err2 := strconv.ParseInt(ev.Labels.Release, 10, 64)
		if err1 != nil || err2 != nil {
			return
		}
		// A container can write before Docker stamps its start event; a
		// second of slack keeps those lines.
		c.followContainer(serverID, envID, ev.ContainerID, relID, ev.Time.Add(-time.Second), nil, &ev.Labels)
	}
}

// followEnv follows the running service containers of an environment
// that got its first drain, from now on (or after the last line read from
// each): those past maxServerFollows weren't followed before. With
// serverID set, only if the environment runs there. Containers already
// followed are left alone; a server that isn't connected is skipped until
// it is (followServer).
func (c *Core) followEnv(ctx context.Context, envID, serverID int64) {
	rows, err := c.store.ListDrainContainers(ctx, envID)
	if err != nil {
		c.log.Warn("log drains: list containers", "environment", envID, "err", err)
		return
	}
	if len(rows) == 0 || (serverID != 0 && rows[0].ServerID != serverID) {
		return
	}
	if _, err := c.runtimeFor(rows[0].ServerID); err != nil {
		return
	}
	since := time.Now()
	for _, r := range rows {
		c.followContainer(r.ServerID, envID, r.Release.ContainerID, r.Release.ID, since, &r, nil)
	}
}

// followServer follows the running service containers on a server, as
// the daemon starts or the server (re)connects or resyncs: from now on,
// or after the last line read from each. Containers already followed are
// left alone.
func (c *Core) followServer(ctx context.Context, serverID int64) {
	if _, err := c.runtimeFor(serverID); err != nil {
		return
	}
	rows, err := c.store.ListFollowContainers(ctx, serverID)
	if err != nil {
		c.log.Warn("log followers: list containers", "server", serverID, "err", err)
		return
	}
	since := time.Now()
	for _, r := range rows {
		row := store.ListDrainContainersRow{
			Release: r.Release, Service: r.Service, Environment: r.Environment, ServerID: r.ServerID, Project: r.Project,
		}
		c.followContainer(r.ServerID, r.EnvironmentID, r.Release.ContainerID, r.Release.ID, since, &row, nil)
	}
}

// followContainer starts a follower for a service container. If one
// still runs (a quick restart, whose old log stream hasn't ended yet), it
// records the restart and that follower starts the next when it ends.
// Lines are read from since, or just after the last line read from the
// container if a follower ran before. row (from followServer and
// followEnv) has the release and its labels; nil looks them up, and then
// labels (from a container event) must match the release.
func (c *Core) followContainer(serverID, envID int64, id string, releaseID int64, since time.Time, row *store.ListDrainContainersRow, labels *node.Labels) {
	m := c.logDrains
	m.mu.Lock()
	if c.ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	if f := m.follows[id]; f != nil {
		if f.env.id == envID {
			m.restart[id] = drainRestart{serverID: serverID, releaseID: releaseID, row: row, labels: labels}
		}
		m.mu.Unlock()
		return
	}
	env := m.envs[envID]
	if m.perServer[serverID] >= maxServerFollows && (env == nil || !env.drained()) {
		m.mu.Unlock()
		c.log.Debug("log followers: the server has enough followers; this container's errors aren't read", "server", serverID, "container", id)
		return
	}
	if env == nil {
		env = newDrainEnv(envID)
		m.envs[envID] = env
	}
	if at, ok := m.last[id]; ok {
		since = at.Add(time.Nanosecond)
	}
	f := &follower{env: env, server: serverID}
	m.follows[id] = f
	env.followers++
	m.perServer[serverID]++
	c.wg.Add(1)
	m.mu.Unlock()
	go c.follow(f, id, releaseID, since, row, labels)
}

// follow reads a container's output for errors and copies it, masked,
// into its environment's drains until the container's log stream ends or
// the daemon closes. If the container started again meanwhile, it then
// starts the next follower, after the last line it read.
func (c *Core) follow(f *follower, id string, releaseID int64, since time.Time, row *store.ListDrainContainersRow, labels *node.Labels) {
	defer c.wg.Done()
	env, serverID := f.env, f.server
	var last time.Time
	defer func() {
		m := c.logDrains
		m.mu.Lock()
		if m.follows[id] == f {
			delete(m.follows, id)
		}
		env.followers--
		if env.followers == 0 && !env.drained() && m.envs[env.id] == env {
			delete(m.envs, env.id)
		}
		if m.perServer[serverID]--; m.perServer[serverID] <= 0 {
			delete(m.perServer, serverID)
		}
		if _, ok := m.last[id]; !last.IsZero() && (ok || len(m.last) < maxDrainResume) {
			m.last[id] = last
		}
		next, again := m.restart[id]
		delete(m.restart, id)
		m.mu.Unlock()
		if again {
			c.followContainer(next.serverID, env.id, id, next.releaseID, since, next.row, next.labels)
		}
	}()
	var src *logSource
	var err error
	if row != nil {
		src, err = c.releaseLogSource(row.Release, row.Project, row.Environment, row.Service)
	} else {
		src, err = c.logSource(env, serverID, releaseID, labels)
	}
	if err != nil {
		if c.ctx.Err() == nil {
			c.log.Warn("log followers: label container", "container", id, "err", err)
		}
		return
	}
	lines, err := c.runtime(serverID).Logs(c.ctx, id, node.LogOptions{Follow: true, Since: since})
	if err != nil {
		if c.ctx.Err() == nil && !errors.Is(err, node.ErrNotFound) {
			c.log.Warn("log followers: follow container", "container", id, "err", err)
		}
		return
	}
	f.errs.mu.Lock()
	f.errs.src = src
	f.errs.mu.Unlock()
	defer f.errs.end(c)
	for l := range lines {
		f.errs.line(c, l)
		if drains := *env.drains.Load(); len(drains) > 0 {
			r := drainLine(l, src)
			for _, d := range drains {
				c.enqueue(d, r)
			}
		}
		last = l.Time
	}
}

// logSource labels the lines of a release's container, after checking
// that the release is of a service in env on serverID and, for a
// container event, that the container's labels say the same.
func (c *Core) logSource(env *drainEnv, serverID, releaseID int64, labels *node.Labels) (*logSource, error) {
	r, err := c.store.GetLogSource(c.ctx, releaseID)
	if err != nil {
		return nil, err
	}
	if r.EnvironmentID != env.id || r.ServerID != serverID {
		return nil, errors.New("the release runs elsewhere")
	}
	if labels != nil && (labels.Service != strconv.FormatInt(r.Release.ServiceID, 10) || labels.Project != strconv.FormatInt(r.ProjectID, 10)) {
		return nil, errors.New("the container's labels don't match its release")
	}
	return c.releaseLogSource(r.Release, r.Project, r.Environment, r.Service)
}

// enqueue queues a line for d without ever blocking: a full queue drops
// it and counts it. The first line queued starts the drain's sender; a
// full batch wakes it early.
func (c *Core) enqueue(d *drain, r drainRecord) {
	d.mu.Lock()
	if len(d.queue) >= drainQueueLines || d.queued+len(r.Text) > drainQueueBytes || d.ctx.Err() != nil {
		d.mu.Unlock()
		d.dropped.Add(1)
		return
	}
	d.queue = append(d.queue, r)
	d.queued += len(r.Text)
	start, full := !d.running, len(d.queue) == drainBatchLines || d.queued >= drainBatchBytes
	d.running = true
	d.mu.Unlock()
	switch {
	case start:
		// The caller is a follower, counted in c.wg, so Close can't be
		// past its Wait.
		c.wg.Add(1)
		go c.sendDrain(d)
	case full:
		select {
		case d.kick <- struct{}{}:
		default:
		}
	}
}

// sendDrain delivers a drain's queue in batches until it is empty, then
// exits; the next line queued starts it again.
func (c *Core) sendDrain(d *drain) {
	defer c.wg.Done()
	batch := make([]drainRecord, 0, drainBatchLines)
	var buf bytes.Buffer
	for {
		if !d.waitBatch() {
			return
		}
		d.mu.Lock()
		n, size := 0, 0
		for n < len(d.queue) && n < drainBatchLines && size < drainBatchBytes {
			size += len(d.queue[n].Text)
			n++
		}
		if n == 0 {
			d.running = false
			d.queue = nil // a burst's backing array goes with it
			d.mu.Unlock()
			return
		}
		batch = append(batch[:0], d.queue[:n]...)
		rest := copy(d.queue, d.queue[n:])
		clear(d.queue[rest:])
		d.queue, d.queued = d.queue[:rest], d.queued-size
		d.mu.Unlock()
		if !c.deliverBatch(d, batch, &buf) {
			return
		}
		clear(batch)
	}
}

// waitBatch waits until the queue holds a full batch or drainBatchWait
// has passed since the sender started on it, so a trickle of lines goes
// out once a second instead of one request per line. The timer runs only
// while lines are queued. False means the drain stopped.
func (d *drain) waitBatch() bool {
	select {
	case <-d.kick: // a stale wake-up from the batch just taken
	default:
	}
	d.mu.Lock()
	full := len(d.queue) >= drainBatchLines || d.queued >= drainBatchBytes
	d.mu.Unlock()
	if full {
		return d.ctx.Err() == nil
	}
	t := time.NewTimer(drainBatchWait)
	defer t.Stop()
	select {
	case <-t.C:
	case <-d.kick:
	case <-d.ctx.Done():
		return false
	}
	return true
}

// deliverBatch sends one batch until it goes through, backing off from
// drainRetryMin, doubling to drainRetryMax, while the queue keeps
// filling and dropping what doesn't fit. The backoff timer runs only
// while a drain's sink fails. False means the drain stopped.
func (c *Core) deliverBatch(d *drain, batch []drainRecord, buf *bytes.Buffer) bool {
	wait := drainRetryMin
	for {
		err := c.logDrains.deliver(d.ctx, d, batch, buf)
		if err == nil {
			d.delivered(len(batch))
			return true
		}
		if d.ctx.Err() != nil {
			return false
		}
		if d.failed(err) {
			c.log.Warn("log drain failing", "drain", d.name, "environment", d.envID, "err", err)
		}
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-d.ctx.Done():
			t.Stop()
			return false
		}
		wait = min(wait*2, drainRetryMax)
	}
}

// delivered records n lines sent.
func (d *drain) delivered(n int) {
	d.sent.Add(int64(n))
	t := now()
	d.mu.Lock()
	d.sentAt, d.lastErr, d.errAt = &t, "", nil
	d.mu.Unlock()
}

// failed records a failed delivery and reports whether the drain was
// working until now.
func (d *drain) failed(err error) (wasOK bool) {
	msg := err.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	t := now()
	d.mu.Lock()
	wasOK, d.lastErr, d.errAt = d.lastErr == "", msg, &t
	d.mu.Unlock()
	return wasOK
}

// status is the drain's delivery status for the API.
func (d *drain) status() apitypes.LogDrainStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	return apitypes.LogDrainStatus{
		Sent: d.sent.Load(), Dropped: d.dropped.Load(), LastError: d.lastErr, LastErrorAt: d.errAt, LastSentAt: d.sentAt,
	}
}
