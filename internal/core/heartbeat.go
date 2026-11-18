package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/version"
)

// The outside heartbeat (ADR 0097): while the control plane runs, it
// sends a GET to one URL every few minutes, so a monitor outside Dock
// raises the alarm when the pings stop. It rides the statistics tick and
// has no timer of its own; the setting is kept in memory, so a tick
// without a ping due is a mutex and a time comparison.

const (
	// heartbeatDefaultMinutes is the interval when none is given, and
	// heartbeatMaxMinutes the longest one.
	heartbeatDefaultMinutes = 5
	heartbeatMaxMinutes     = 60
	// heartbeatSlack is how much early a ping may go: ticks are a minute
	// apart, and one that runs a little early must not skip its ping.
	heartbeatSlack = 30 * time.Second
	// heartbeatTimeout bounds one ping. There are no retries: the next
	// interval is the retry.
	heartbeatTimeout = 15 * time.Second
	// heartbeatAlertAfter is how many pings in a row fail before the
	// alert opens.
	heartbeatAlertAfter = 3
	// heartbeatAlertKey names the heartbeat's connection alert.
	heartbeatAlertKey = "heartbeat"
	// maxHeartbeatURL caps the URL's length, in core as in the API's schema.
	maxHeartbeatURL = 2048
)

// heartbeat is the heartbeat setting and how its pings went, in memory:
// loaded on start (loadHeartbeat), replaced by SetHeartbeat and
// RemoveHeartbeat. Outcomes aren't stored: they start over with the
// daemon. Nothing writes to the store or raises an alert holding mu.
type heartbeat struct {
	// changing serializes SetHeartbeat and RemoveHeartbeat, so the store
	// and the memory end up with the same setting. The tick never takes it.
	changing sync.Mutex

	mu sync.Mutex
	// url is empty while no heartbeat is set. It is a secret: it never
	// goes into an error, a log line, the audit log or the API.
	url    string
	target string
	every  time.Duration
	// gen changes with the setting: a ping sent for an earlier setting
	// records nothing.
	gen uint64
	// lastAttempt is when a ping was last started, and sending whether
	// the tick's ping is still on its way.
	lastAttempt time.Time
	sending     bool
	lastOK      time.Time
	lastErr     string
	lastErrAt   time.Time
	failures    int
}

// replace puts a new setting (or with an empty url, none) in place and
// forgets the outcomes of the one before.
func (hb *heartbeat) replace(u, target string, minutes int64) {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	gen := hb.gen + 1
	hb.url, hb.target, hb.every, hb.gen = u, target, time.Duration(minutes)*time.Minute, gen
	hb.lastAttempt, hb.sending, hb.lastOK, hb.lastErr, hb.lastErrAt, hb.failures = time.Time{}, false, time.Time{}, "", time.Time{}, 0
}

// view is the heartbeat for the API, or false when none is set.
func (hb *heartbeat) view() (apitypes.Heartbeat, bool) {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	if hb.url == "" {
		return apitypes.Heartbeat{}, false
	}
	out := apitypes.Heartbeat{Target: hb.target, Minutes: int(hb.every / time.Minute), LastError: hb.lastErr, Failures: hb.failures}
	if !hb.lastOK.IsZero() {
		out.LastSentAt = ptr(hb.lastOK.UTC())
	}
	if !hb.lastErrAt.IsZero() {
		out.LastErrorAt = ptr(hb.lastErrAt.UTC())
	}
	return out, true
}

// record notes how a ping of setting gen went and returns the failures
// in a row since; ok is false when the setting changed meanwhile, and
// the outcome is nobody's.
func (hb *heartbeat) record(gen uint64, tick bool, err error, at time.Time) (failures int, ok bool) {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	if gen != hb.gen {
		return 0, false
	}
	if tick {
		hb.sending = false
	}
	if err != nil {
		hb.failures++
		hb.lastErr, hb.lastErrAt = clip(err.Error(), 500), at
	} else {
		hb.failures, hb.lastErr, hb.lastErrAt, hb.lastOK = 0, "", time.Time{}, at
	}
	return hb.failures, true
}

// loadHeartbeat reads the heartbeat into memory on start: the one query
// the heartbeat makes until it is changed.
func (c *Core) loadHeartbeat(ctx context.Context) error {
	row, err := c.store.GetHeartbeat(ctx)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the heartbeat: %w", err)
	}
	u, err := c.box.Open(row.Url, heartbeatURLAAD)
	if err != nil {
		return fmt.Errorf("open the heartbeat's URL: %w", err)
	}
	c.heartbeat.replace(string(u), row.Target, row.Minutes)
	return nil
}

// heartbeatTick sends the heartbeat's ping when one is due, in its own
// goroutine so a slow receiver never holds the statistics tick. It runs
// at the end of a tick that read the database, so a daemon whose
// database is stuck stops pinging. With no heartbeat or none due it is a
// mutex and a time comparison.
func (c *Core) heartbeatTick(_ context.Context, now time.Time) {
	hb := &c.heartbeat
	hb.mu.Lock()
	due := hb.url != "" && !hb.sending && (hb.lastAttempt.IsZero() || now.Sub(hb.lastAttempt) >= hb.every-heartbeatSlack)
	if !due {
		hb.mu.Unlock()
		return
	}
	hb.sending, hb.lastAttempt = true, now
	u, gen := hb.url, hb.gen
	hb.mu.Unlock()
	c.wg.Go(func() {
		// Under the daemon's context: the tick's ends with the tick.
		_ = c.pingHeartbeat(c.ctx, u, gen, true)
	})
}

// pingHeartbeat sends one ping of setting gen to u and records how it
// went: the alert opens after heartbeatAlertAfter failures in a row and
// resolves with a ping that works. tick says it is the tick's ping, the
// one heartbeat.sending guards.
func (c *Core) pingHeartbeat(ctx context.Context, u string, gen uint64, tick bool) error {
	err := c.alerts.get(ctx, u)
	if ctx.Err() != nil {
		return err // shutting down, or the test's caller gave up: not the receiver's fault
	}
	failures, ok := c.heartbeat.record(gen, tick, err, now())
	switch {
	case !ok:
	case err == nil:
		// Also with no failures counted: an alert from before a restart
		// resolves here. Without an open alert it is a map lookup.
		c.connectionWorks(ctx, heartbeatAlertKey)
	case failures >= heartbeatAlertAfter:
		c.connectionFailed(ctx, heartbeatAlertKey, "heartbeat", "The outside heartbeat is failing",
			fmt.Sprintf("The last %d pings failed: %v. Whoever watches the heartbeat will think Dock is down. Check that the URL still exists; `dock alerts heartbeat set` replaces it.", failures, err),
			"dock alerts heartbeat test")
		// A change between the record above and the alert resolved
		// nothing yet open: the alert goes now (as channelFailed does).
		c.heartbeat.mu.Lock()
		changed := c.heartbeat.gen != gen
		c.heartbeat.mu.Unlock()
		if changed {
			c.connectionWorks(ctx, heartbeatAlertKey)
		}
	}
	return err
}

// get sends the heartbeat's GET with the platform's egress guard (private
// addresses allowed, Dock's own and link-local not, no redirects); anything
// but a 2xx answer is an error. The URL is a secret, so errors never
// repeat it.
func (e *alertEngine) get(ctx context.Context, target string) error {
	ctx, cancel := context.WithTimeout(ctx, heartbeatTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return errors.New("the heartbeat URL is not a valid URL")
	}
	req.Header.Set("User-Agent", "dock/"+version.Version)
	_, client := e.guardFor(false)
	resp, err := client.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err // the URL holds the check's ID; don't repeat it
		}
		return fmt.Errorf("GET the heartbeat URL: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("the heartbeat URL answered %s", resp.Status)
	}
	return nil
}

// errNoHeartbeat is what reading, removing or testing the heartbeat
// answers while none is set.
func errNoHeartbeat() error {
	return notFound("no heartbeat is set; `dock alerts heartbeat set <url>` sets one")
}

// GetHeartbeat returns the heartbeat and how its last pings went, without
// its URL. Operators with platform.alerts.
func (c *Core) GetHeartbeat(_ context.Context, a Actor) (apitypes.Heartbeat, error) {
	if err := a.requirePlatform(PlatformAlerts, "reading the heartbeat"); err != nil {
		return apitypes.Heartbeat{}, err
	}
	out, ok := c.heartbeat.view()
	if !ok {
		return out, errNoHeartbeat()
	}
	return out, nil
}

// SetHeartbeat sets or replaces the heartbeat: the first ping goes with
// the next statistics tick. Operators with platform.alerts.
func (c *Core) SetHeartbeat(ctx context.Context, a Actor, req apitypes.SetHeartbeatRequest) (apitypes.Heartbeat, error) {
	if err := a.requirePlatform(PlatformAlerts, "setting the heartbeat"); err != nil {
		return apitypes.Heartbeat{}, err
	}
	u, perr := url.Parse(req.URL)
	if req.URL == "" || len(req.URL) > maxHeartbeatURL || perr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return apitypes.Heartbeat{}, invalid("the heartbeat needs an http:// or https:// URL of at most %d characters", maxHeartbeatURL)
	}
	// Named hosts are checked when sending (newChannelClient), after DNS.
	if err := c.alerts.guard.hostErr(u.Hostname()); err != nil {
		return apitypes.Heartbeat{}, invalid("%v", err)
	}
	minutes := int64(req.Minutes)
	if minutes == 0 {
		minutes = heartbeatDefaultMinutes
	}
	if minutes < 1 || minutes > heartbeatMaxMinutes {
		return apitypes.Heartbeat{}, invalid("the heartbeat is sent every 1 to %d minutes; pass --every with a number in that range", heartbeatMaxMinutes)
	}
	sealed, err := c.box.Seal([]byte(req.URL), heartbeatURLAAD)
	if err != nil {
		return apitypes.Heartbeat{}, err
	}
	target := u.Scheme + "://" + u.Host
	hb := &c.heartbeat
	hb.changing.Lock()
	err = c.store.SetHeartbeat(ctx, store.SetHeartbeatParams{Url: sealed, Target: target, Minutes: minutes, UpdatedBy: a.Name, UpdatedAt: now()})
	if err == nil {
		hb.replace(req.URL, target, minutes)
	}
	hb.changing.Unlock()
	if err != nil {
		return apitypes.Heartbeat{}, err
	}
	// The alert was about the URL before this one.
	c.connectionWorks(ctx, heartbeatAlertKey)
	c.audit(ctx, a.platform(), "heartbeat.set", nil, nil, "heartbeat", fmt.Sprintf("set the outside heartbeat: %s every %d min", target, minutes))
	out, _ := hb.view()
	return out, nil
}

// RemoveHeartbeat removes the heartbeat and resolves its alert: pings
// stop, so whoever watched them will raise the alarm unless the check is
// removed there too. Operators with platform.alerts.
func (c *Core) RemoveHeartbeat(ctx context.Context, a Actor) error {
	if err := a.requirePlatform(PlatformAlerts, "removing the heartbeat"); err != nil {
		return err
	}
	hb := &c.heartbeat
	hb.changing.Lock()
	_, set := hb.view()
	var err error
	if set {
		if err = c.store.DeleteHeartbeat(ctx); err == nil {
			hb.replace("", "", 0)
		}
	}
	hb.changing.Unlock()
	if !set {
		return errNoHeartbeat()
	}
	if err != nil {
		return err
	}
	c.connectionWorks(ctx, heartbeatAlertKey)
	c.audit(ctx, a.platform(), "heartbeat.remove", nil, nil, "heartbeat", "removed the outside heartbeat")
	return nil
}

// TestHeartbeat sends one ping now and reports how it went; the outcome
// is recorded like the tick's. Operators with platform.alerts.
func (c *Core) TestHeartbeat(ctx context.Context, a Actor) (apitypes.HeartbeatTest, error) {
	if err := a.requirePlatform(PlatformAlerts, "testing the heartbeat"); err != nil {
		return apitypes.HeartbeatTest{}, err
	}
	hb := &c.heartbeat
	hb.mu.Lock()
	u, gen := hb.url, hb.gen
	hb.mu.Unlock()
	if u == "" {
		return apitypes.HeartbeatTest{}, errNoHeartbeat()
	}
	c.audit(ctx, a.platform(), "heartbeat.test", nil, nil, "heartbeat", "sent a test ping to the outside heartbeat")
	out := apitypes.HeartbeatTest{OK: true}
	if err := c.pingHeartbeat(ctx, u, gen, false); err != nil {
		out.OK, out.Error = false, err.Error()
	}
	var set bool
	if out.Heartbeat, set = hb.view(); !set {
		return out, errNoHeartbeat() // removed meanwhile
	}
	return out, nil
}
