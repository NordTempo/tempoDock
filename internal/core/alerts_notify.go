package core

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/mail"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/version"
)

// Notification channels (ADR 0020). An org's admins add channels for its
// alerts, operators channels for the platform's (ADR 0062): a generic webhook
// (JSON, HMAC-signed), a Slack-compatible incoming webhook, a Discord
// webhook, Pushover, or email over SMTP. A channel gets alerts at or above its minimum severity when they
// open, escalate, repeat (ADR 0097) and resolve, and a reminder every
// renotify_minutes while one stays open. Each channel's notifications are
// sent in order by a goroutine that runs while it has some queued. It
// first waits groupWindow, then sends what is queued as one group (ADR
// 0097): a Slack, Discord or Pushover channel gets one message listing
// every alert (grouped), a webhook one POST per notification as before. Each delivery is retried with
// backoff (deliveryBackoff, longer when the receiver asks with
// Retry-After; never after an answer retrying can't change,
// deliveryError.final); the outcome is kept on the channel, so failures
// show in `dock alerts channel list` and the UI. Queues live in memory
// only: a daemon that stops gives what a channel still has queued one
// last attempt (flushGroup), so a restart or an update inside the grouping
// window or a retry's wait loses nothing; a daemon that is killed drops
// it. Email channels queue a mail per notification in Dock's outbox at
// once (email_channels.go): they aren't grouped.

// deliveryBackoff is how long each delivery attempt waits before it runs:
// the first right away, the last about 20 minutes after the alert.
var deliveryBackoff = []time.Duration{0, 10 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute}

// maxRetryAfter caps how long a receiver's Retry-After makes the next
// attempt wait: the longest backoff step.
const maxRetryAfter = 15 * time.Minute

// groupWindow is how long a channel's sender waits after the first
// notification arrives before it sends what is queued as one group (ADR
// 0097): a server going offline with its checks is then one Slack message
// instead of one per alert. A one-shot timer that exists only while a
// channel has something queued (docs/performance.md); tests set it to 0.
var groupWindow = 10 * time.Second

// closeFlush bounds the last attempt a stopping daemon makes at what a
// channel still has queued (flushGroup): well inside the service's stop
// timeout (deploy/systemd/dock.service).
const closeFlush = 5 * time.Second

const (
	// maxQueued caps the notifications waiting for one channel (or status
	// page hook); more are dropped and counted as failures, except an
	// alert's resolution, which is always queued (deliver).
	maxQueued = 32
	// deliveryTimeout bounds one attempt.
	deliveryTimeout = 15 * time.Second
	// defaultRenotify is how often an open alert is sent again unless the
	// channel says otherwise.
	defaultRenotify = 240
	// SignatureHeader carries a webhook delivery's signature:
	// t=<unix seconds>,v1=<hex HMAC-SHA256 of "<t>.<body>">.
	SignatureHeader = "Dock-Signature"
)

// channelConfig is a channel's sealed settings. An email channel holds
// only its recipients (To); Dock's email server sends (ADR 0079). SMTP is
// an email channel's own server from before, moved on start
// (moveEmailChannels).
type channelConfig struct {
	URL    string      `json:"url,omitempty"`
	Secret string      `json:"secret,omitempty"`
	To     []string    `json:"to,omitempty"`
	SMTP   *legacySMTP `json:"smtp,omitempty"`
	// User and Token are a Pushover channel's user (or group) key and
	// application token.
	User  string `json:"user,omitempty"`
	Token string `json:"token,omitempty"`
}

// legacySMTP is an email channel's own SMTP server, as channels stored it
// before Dock had one (ADR 0020).
type legacySMTP struct {
	Host     string   `json:"host"`
	Port     int      `json:"port,omitempty"`
	Username string   `json:"username,omitempty"`
	Password string   `json:"password,omitempty"`
	From     string   `json:"from"`
	To       []string `json:"to"`
}

// notify sends what happened to an alert (opened, escalated, repeated,
// resolved) to the channels that should hear it: for opened, escalated
// and repeated its org's (or the platform's) whose minimum severity it
// meets, for resolved those that were told about it.
func (c *Core) notify(ctx context.Context, a store.Alert, event string, now time.Time) {
	var to []store.AlertChannel
	if event == "resolved" {
		told, err := c.store.ListNoticedChannels(ctx, a.ID)
		if err != nil {
			c.log.Error("alerts: list notices", "err", err)
			return
		}
		if len(told) > 0 {
			if err := c.store.DeleteAlertNotices(ctx, a.ID); err != nil {
				c.log.Error("alerts: delete notices", "err", err)
			}
		}
		to = told
	} else {
		channels, err := c.store.ListAlertChannels(ctx, alertOrgID(a))
		if err != nil {
			c.log.Error("alerts: list channels", "err", err)
			return
		}
		for _, ch := range channels {
			if severityRank(apitypes.AlertSeverity(a.Severity)) < severityRank(apitypes.AlertSeverity(ch.MinSeverity)) {
				continue
			}
			if err := c.store.AddAlertNotice(ctx, store.AddAlertNoticeParams{AlertID: a.ID, ChannelID: ch.ID, NotifiedAt: now.Unix()}); err != nil {
				c.log.Error("alerts: record notice", "err", err)
			}
			to = append(to, ch)
		}
	}
	n := apitypes.AlertNotification{Event: event, Alert: toAlert(a), SentAt: now.UTC()}
	for _, ch := range to {
		c.deliver(ch, n)
	}
}

// remind sends open alerts again to channels whose renotify interval has
// passed since they were last told. With no open alert it does nothing,
// not even a query.
func (c *Core) remind(ctx context.Context, now time.Time) {
	c.alerts.mu.Lock()
	byID := make(map[int64]store.Alert, len(c.alerts.open))
	for _, a := range c.alerts.open {
		byID[a.ID] = a
	}
	c.alerts.mu.Unlock()
	if len(byID) == 0 {
		return
	}
	due, err := c.store.ListDueReminders(ctx, now.Unix())
	if err != nil {
		return
	}
	for _, d := range due {
		a, ok := byID[d.AlertID]
		if !ok || a.Kind == string(apitypes.AlertNewVulnerability) {
			// Vulnerabilities aren't reminded of: what stands is on the
			// Images page (ADR 0074).
			continue
		}
		ch := d.AlertChannel
		if err := c.store.AddAlertNotice(ctx, store.AddAlertNoticeParams{AlertID: a.ID, ChannelID: ch.ID, NotifiedAt: now.Unix()}); err != nil {
			c.log.Error("alerts: record notice", "err", err)
			continue
		}
		c.deliver(ch, apitypes.AlertNotification{Event: "reminder", Alert: toAlert(a), SentAt: now.UTC()})
	}
}

// deliveryQueue queues what is sent to each target (an alert channel,
// a status page hook) by its ID. Each target with something queued has
// one goroutine sending its queue in order (an alert's resolution never
// overtakes its opening), which exits once the queue is empty.
type deliveryQueue[T any] struct {
	mu     sync.Mutex
	queues map[int64][]T
}

// outbox is the alert channels' queue.
type outbox = deliveryQueue[apitypes.AlertNotification]

// add queues item for target id. start says the caller must start the
// target's sender (sendAll or sendGroups); full that maxQueued items wait
// already, so item was dropped. An item with keep is queued past the cap:
// for what must arrive and is bounded some other way (resolutions).
func (q *deliveryQueue[T]) add(id int64, item T, keep bool) (start, full bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.queues == nil {
		q.queues = map[int64][]T{}
	}
	cur, running := q.queues[id]
	if len(cur) >= maxQueued && !keep {
		return false, true
	}
	q.queues[id] = append(cur, item)
	return !running, false
}

// sendAll sends target id's queue in order with send until it is empty
// or ctx ends. The sender add asked for runs it, counted in c.wg.
func (q *deliveryQueue[T]) sendAll(ctx context.Context, id int64, send func(T)) {
	for {
		q.mu.Lock()
		cur := q.queues[id]
		if len(cur) == 0 || ctx.Err() != nil {
			delete(q.queues, id)
			q.mu.Unlock()
			return
		}
		item := cur[0]
		q.mu.Unlock()
		send(item)
		q.mu.Lock()
		q.queues[id] = q.queues[id][1:]
		q.mu.Unlock()
	}
}

// sendGroups is sendAll for a target that takes several items at once:
// each round hands send everything queued, in order, and removes as many
// as send says it is done with (sent, or failed for good). What arrives
// meanwhile is the next round's. When ctx ends it returns what is still
// queued, the items send wasn't done with first, for the caller's last
// attempt.
func (q *deliveryQueue[T]) sendGroups(ctx context.Context, id int64, send func([]T) (done int)) (left []T) {
	for {
		q.mu.Lock()
		cur := q.queues[id]
		if len(cur) == 0 || ctx.Err() != nil {
			delete(q.queues, id)
			q.mu.Unlock()
			return cur
		}
		group := append([]T(nil), cur...)
		q.mu.Unlock()
		done := send(group)
		q.mu.Lock()
		q.queues[id] = q.queues[id][done:]
		q.mu.Unlock()
	}
}

// errGone stops a delivery's retries: what it was for was removed.
var errGone = errors.New("removed meanwhile")

// withRetries makes up to len(deliveryBackoff) attempts, waiting between
// them, until one succeeds, returns errGone or a final deliveryError
// (a 404 from a removed Slack webhook), or the daemon closes. A
// deliveryError's Retry-After (Slack's and Discord's rate limits) makes
// the next wait at least that long, up to maxRetryAfter. It returns the
// last attempt's error.
func (c *Core) withRetries(attempt func() error) error {
	var err error
	var after time.Duration
	for _, wait := range deliveryBackoff {
		if wait > 0 {
			wait = max(wait, after)
			t := time.NewTimer(wait)
			select {
			case <-c.ctx.Done():
				t.Stop()
				return c.ctx.Err()
			case <-t.C:
			}
		}
		if err = attempt(); err == nil || errors.Is(err, errGone) || c.ctx.Err() != nil {
			return err
		}
		after = 0
		if de, ok := errors.AsType[*deliveryError](err); ok {
			if de.final() {
				return err
			}
			after = min(de.retryAfter, maxRetryAfter)
		}
	}
	return err
}

// deliveryError is a channel's or hook's answer other than 2xx.
type deliveryError struct {
	status int
	// retryAfter is the answer's Retry-After in seconds, 0 without one.
	retryAfter time.Duration
	// answer is the start of the body, msg the whole error.
	answer, msg string
}

func (e *deliveryError) Error() string { return e.msg }

// final reports whether sending the same body again can't succeed: a
// 4xx other than 408 (timeout) and 429 (rate limited). Slack answers 404
// no_service for a removed webhook and 410 channel_is_archived, and
// retrying those for 20 minutes only holds up the channel's queue.
func (e *deliveryError) final() bool {
	return e.status/100 == 4 && e.status != http.StatusRequestTimeout && e.status != http.StatusTooManyRequests
}

// deliver queues n for ch and starts the channel's sender if it isn't
// running. A channel with maxQueued notifications waiting (it has been
// failing for a while) drops new ones and records that, except a
// resolution: the channel would show the alert open for good, and
// resolutions are bounded by the alerts it was told about (ADR 0097).
// The sender waits groupWindow before its first send, so alerts raised
// together go out as one group (sendGroup). When the daemon stops, what
// is still queued (held by the window, or between retries) gets one last
// attempt (flushGroup). Email goes to Dock's outbox at once, ungrouped.
func (c *Core) deliver(ch store.AlertChannel, n apitypes.AlertNotification) {
	if apitypes.AlertChannelKind(ch.Kind) == apitypes.ChannelEmail {
		c.mailChannel(c.ctx, ch, n)
		return
	}
	start, full := c.alerts.outbox.add(ch.ID, n, n.Event == "resolved")
	if full {
		c.channelFailed(c.ctx, ch.ID, errors.New("too many notifications waiting to be sent; this one was dropped"))
		return
	}
	if !start {
		return
	}
	c.wg.Add(1)
	c.goCaught("sending alert notifications", func() {
		defer c.wg.Done()
		if groupWindow > 0 {
			// One shot per burst, never a ticker: it runs only while the
			// channel has something queued.
			t := time.NewTimer(groupWindow)
			select {
			case <-c.ctx.Done():
				t.Stop()
			case <-t.C:
			}
		}
		// With the daemon closing, sendGroups only hands back the queue.
		left := c.alerts.outbox.sendGroups(c.ctx, ch.ID, func(group []apitypes.AlertNotification) int {
			// A panic drops this group; the queue goes on.
			done := len(group)
			c.safely("sending alert notifications", func() { done = c.sendGroup(ch, group) })
			return done
		})
		c.safely("sending alert notifications", func() { c.flushGroup(ch, left) })
	})
}

// flushGroup is a stopping daemon's last attempt at what channel ch still
// has queued: once, without retries, within closeFlush, to a grouped
// channel (Slack, Discord, Pushover) as one message and to a webhook one
// POST each. Without it a restart inside the
// grouping window lost every alert of the group, and the channels heard
// of them again only with a reminder. What can't be sent is logged and
// noted on the channel.
func (c *Core) flushGroup(ch store.AlertChannel, left []apitypes.AlertNotification) {
	if len(left) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), closeFlush)
	defer cancel()
	cur, err := c.store.GetAlertChannelByID(ctx, ch.ID)
	if store.IsNotFound(err) {
		return // removed meanwhile
	}
	sent := 0
	switch {
	case err != nil:
	case grouped(apitypes.AlertChannelKind(cur.Kind)):
		var cfg channelConfig
		if cfg, err = c.openChannel(cur); err == nil {
			if err = c.postGroup(ctx, cur, cfg, left); err == nil {
				sent = len(left)
			}
		}
	default:
		for _, n := range left {
			if err = c.send(ctx, cur, n); err != nil {
				break
			}
			sent++
		}
	}
	if err == nil {
		return
	}
	c.log.Warn("alerts: not sent before stopping", "channel", ch.Name, "notifications", len(left)-sent, "err", err)
	// As text, not wrapped: this isn't the receiver's final answer, which
	// channelFailed would open the channel's broken alert for.
	c.channelFailed(ctx, ch.ID, fmt.Errorf("%d notifications weren't sent, because Dock stopped: %s", len(left)-sent, err.Error()))
}

// sendGroup sends a channel's queued notifications: to a grouped channel
// (Slack, Discord, Pushover) as one message, retried as one delivery; to
// a webhook one POST each, in order,
// each with its own retries, since receivers parse one alert per body.
// It returns how many of the group it is done with: all of them, unless
// the daemon stopped before a delivery came through (unsent).
func (c *Core) sendGroup(ch store.AlertChannel, group []apitypes.AlertNotification) (done int) {
	if !grouped(apitypes.AlertChannelKind(ch.Kind)) {
		for i, n := range group {
			if c.unsent(c.sendWithRetries(ch.ID, n)) {
				return i
			}
		}
		return len(group)
	}
	err := c.withRetries(func() error {
		cur, err := c.store.GetAlertChannelByID(c.ctx, ch.ID)
		if err != nil {
			return errGone // removed, or the daemon is closing
		}
		cfg, err := c.openChannel(cur)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(c.ctx, deliveryTimeout)
		defer cancel()
		return c.postGroup(ctx, cur, cfg, group)
	})
	c.recordDelivery(ch.ID, group[0].Alert.Title, err)
	if c.unsent(err) {
		return 0
	}
	return len(group)
}

// unsent reports whether a delivery that ended with err (withRetries')
// was cut short by the daemon stopping, not sent and not failed for
// good: it stays queued for flushGroup.
func (c *Core) unsent(err error) bool { return err != nil && c.ctx.Err() != nil }

// sendWithRetries makes up to len(deliveryBackoff) attempts at one
// notification, records the outcome on the channel and returns it. A
// channel removed meanwhile isn't sent to.
func (c *Core) sendWithRetries(id int64, n apitypes.AlertNotification) error {
	err := c.withRetries(func() error {
		ch, err := c.store.GetAlertChannelByID(c.ctx, id)
		if err != nil {
			return errGone // removed, or the daemon is closing
		}
		return c.send(c.ctx, ch, n)
	})
	c.recordDelivery(id, n.Alert.Title, err)
	return err
}

// recordDelivery records a delivery's outcome (withRetries' error) on
// channel id; title names the alert, or a group's first, in the log.
func (c *Core) recordDelivery(id int64, title string, err error) {
	switch {
	case err == nil:
		c.channelSent(c.ctx, id)
	case errors.Is(err, errGone) || c.ctx.Err() != nil:
	default:
		c.log.Warn("alerts: delivery failed", "channel", id, "alert", title, "err", err)
		c.channelFailed(c.ctx, id, err)
	}
}

// channelSent records a delivery that worked, which also resolves the
// channel's broken alert (channelFailed); without one that is a map
// lookup.
func (c *Core) channelSent(ctx context.Context, id int64) {
	if err := c.store.SetAlertChannelSent(ctx, store.SetAlertChannelSentParams{LastSentAt: ptr(now()), ID: id}); err != nil {
		c.log.Error("alerts: record delivery", "err", err)
	}
	c.connectionWorks(ctx, channelAlertKey(id))
}

// channelFailed records a delivery that failed on the channel. One that
// got a final answer (deliveryError.final: Slack's 404 for a removed
// webhook, 410 for an archived channel) marks the channel broken: its
// last error says so and what to do (brokenText), and an alert of the
// channel's org (or the platform's) stays open until a delivery or a test
// works or the channel is removed. The channel itself is kept.
func (c *Core) channelFailed(ctx context.Context, id int64, cause error) {
	msg := cause.Error()
	de, final := errors.AsType[*deliveryError](cause)
	final = final && de.final()
	var ch store.AlertChannel
	if final {
		var err error
		if ch, err = c.store.GetAlertChannelByID(ctx, id); err != nil {
			return // removed meanwhile
		}
		msg = brokenText(apitypes.AlertChannelKind(ch.Kind), de)
	}
	if err := c.store.SetAlertChannelFailed(ctx, store.SetAlertChannelFailedParams{LastError: clip(msg, 500), LastErrorAt: ptr(now()), ID: id}); err != nil {
		c.log.Error("alerts: record delivery", "err", err)
	}
	if !final {
		return
	}
	command := "dock alerts channel test " + ch.Name
	if ch.OrgID == nil {
		command += " --platform"
	}
	c.connectionFailedIn(ctx, channelOrg(ch), channelAlertKey(id), "alert_channel:"+ch.Name,
		"Alert channel "+ch.Name+" is broken", msg, command)
	// A removal between the read above and the alert resolved nothing
	// yet open: the alert goes now (RemoveAlertChannel deletes, then
	// resolves, so one of the two sees the other).
	if _, err := c.store.GetAlertChannelByID(ctx, id); store.IsNotFound(err) {
		c.connectionWorks(ctx, channelAlertKey(id))
	}
}

// channelAlertKey names a broken channel's alert.
func channelAlertKey(id int64) string { return "alert_channel:" + strconv.FormatInt(id, 10) }

// channelOrg is a channel's org, 0 for the platform's.
func channelOrg(ch store.AlertChannel) int64 {
	if ch.OrgID == nil {
		return 0
	}
	return *ch.OrgID
}

// brokenText says why a channel that got a final answer is broken and
// what to do about it, in the receiver's words for Slack, Discord and
// Pushover.
func brokenText(kind apitypes.AlertChannelKind, de *deliveryError) string {
	switch {
	case kind == apitypes.ChannelDiscord && de.status == http.StatusNotFound:
		return fmt.Sprintf("Discord says this webhook no longer exists (404 %s). Nothing reaches the channel: remove it and add it again with a new webhook URL.", de.answer)
	case kind == apitypes.ChannelPushover:
		return fmt.Sprintf("Pushover refused the channel's keys (%d %s). Nothing reaches the channel: remove it and add it again with the right user key and application token.", de.status, de.answer)
	case kind == apitypes.ChannelSlack && de.status == http.StatusNotFound:
		return fmt.Sprintf("Slack says this webhook no longer exists (404 %s). Nothing reaches the channel: remove it and add it again with a new webhook URL.", de.answer)
	case kind == apitypes.ChannelSlack && de.status == http.StatusGone:
		return fmt.Sprintf("Slack says the webhook's Slack channel is archived (410 %s). Nothing reaches the channel: unarchive it in Slack, or remove the channel and add it again with a new webhook URL.", de.answer)
	}
	return fmt.Sprintf("The channel's URL answered %d %s: %s. Sending again can't change that, so nothing reaches the channel: fix the receiver, or remove the channel and add it again with the right URL.",
		de.status, http.StatusText(de.status), de.answer)
}

// send makes one delivery attempt.
func (c *Core) send(ctx context.Context, ch store.AlertChannel, n apitypes.AlertNotification) error {
	cfg, err := c.openChannel(ch)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	switch apitypes.AlertChannelKind(ch.Kind) {
	case apitypes.ChannelWebhook:
		body, err := json.Marshal(n)
		if err != nil {
			return err
		}
		// Signed at the attempt, not at sent_at: receivers reject old
		// timestamps, and a retry goes out up to 20 minutes later.
		return c.alerts.post(ctx, ch.OrgID != nil, cfg.URL, body, map[string]string{
			SignatureHeader: signWebhook(cfg.Secret, time.Now(), body), "Dock-Event": n.Event,
		})
	case apitypes.ChannelSlack, apitypes.ChannelDiscord, apitypes.ChannelPushover:
		return c.postGroup(ctx, ch, cfg, []apitypes.AlertNotification{n})
	case apitypes.ChannelEmail:
		subject, text := emailText(n)
		return c.sendNow(ctx, mailAddresses(cfg.To), mail.Content{Subject: subject, Text: text})
	}
	return fmt.Errorf("unknown channel kind %q", ch.Kind)
}

// grouped reports whether a channel of kind gets the notifications queued
// together as one message (sendGroup) rather than one delivery each.
func grouped(kind apitypes.AlertChannelKind) bool {
	return kind == apitypes.ChannelSlack || kind == apitypes.ChannelDiscord || kind == apitypes.ChannelPushover
}

// postGroup sends notifications to a grouped channel as one message.
func (c *Core) postGroup(ctx context.Context, ch store.AlertChannel, cfg channelConfig, group []apitypes.AlertNotification) error {
	switch apitypes.AlertChannelKind(ch.Kind) {
	case apitypes.ChannelDiscord:
		return c.postDiscord(ctx, ch, cfg, group)
	case apitypes.ChannelPushover:
		return c.postPushover(ctx, ch, cfg, group)
	}
	return c.postSlack(ctx, ch, cfg, group)
}

// postSlack posts notifications to a Slack channel as one message: each
// alert's text (slackText), a blank line between them, under a first line
// saying how many when there are several.
func (c *Core) postSlack(ctx context.Context, ch store.AlertChannel, cfg channelConfig, group []apitypes.AlertNotification) error {
	texts := make([]string, 0, len(group)+1)
	if len(group) > 1 {
		texts = append(texts, strconv.Itoa(len(group))+" alerts")
	}
	link := c.alertLinks(ctx, ch.OrgID)
	for _, n := range group {
		texts = append(texts, slackText(n, link(n.Alert)))
	}
	body, err := json.Marshal(map[string]string{"text": strings.Join(texts, "\n\n")})
	if err != nil {
		return err
	}
	return c.alerts.post(ctx, ch.OrgID != nil, cfg.URL, body, nil)
}

// discordLimit is the most characters a Discord message holds.
const discordLimit = 2000

// postDiscord posts notifications to a Discord webhook as one message,
// laid out like Slack's (postSlack) and cut to fit discordLimit. Mentions
// are off (allowed_mentions), so an alert quoting @everyone pings nobody.
func (c *Core) postDiscord(ctx context.Context, ch store.AlertChannel, cfg channelConfig, group []apitypes.AlertNotification) error {
	link := c.alertLinks(ctx, ch.OrgID)
	texts := make([]string, len(group))
	for i, n := range group {
		texts[i] = discordText(n, link(n.Alert))
	}
	var msg discordMessage
	msg.Content = joinFitting(texts, "\n\n", discordLimit)
	msg.AllowedMentions.Parse = []string{}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.alerts.post(ctx, ch.OrgID != nil, discordWait(cfg.URL), body, nil)
}

// discordWait asks Discord to answer only once the message is saved
// (wait=true), keeping the URL's other parameters (thread_id): without
// it, Discord may answer 204 for a message it then drops, and the
// delivery isn't retried.
func discordWait(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw // validated when added; post reports it
	}
	q := u.Query()
	q.Set("wait", "true")
	u.RawQuery = q.Encode()
	return u.String()
}

// discordMessage is the body of a Discord webhook's execute call.
type discordMessage struct {
	Content         string `json:"content"`
	AllowedMentions struct {
		Parse []string `json:"parse"` // none: nobody is pinged
	} `json:"allowed_mentions"`
}

// discordText is a Discord message, escaped: alert messages quote release
// errors and build output, which must not turn into markdown, masked
// links or mentions. The command is a code span and the link Dock's own
// [Open in Dock](<url>), without an embed.
func discordText(n apitypes.AlertNotification, link string) string {
	text := headline(n)
	if n.Alert.Message != "" && n.Event != "resolved" {
		text += "\n" + n.Alert.Message
	}
	text = discordEscaper.Replace(text)
	if n.Alert.Command != "" && n.Event != "resolved" {
		text += "\n`" + strings.ReplaceAll(n.Alert.Command, "`", "'") + "`"
	}
	if link != "" {
		text += "\n[Open in Dock](<" + link + ">)"
	}
	return text
}

// discordEscaper escapes Discord's markdown and mention characters.
var discordEscaper = strings.NewReplacer(
	`\`, `\\`, "*", `\*`, "_", `\_`, "~", `\~`, "`", "\\`", "|", `\|`,
	">", `\>`, "<", `\<`, "[", `\[`, "]", `\]`, "#", `\#`, "@", "@\u200b",
)

// defaultPushoverURL is Pushover's messages API.
const defaultPushoverURL = "https://api.pushover.net/1/messages.json"

// Pushover's limits on a message's parts.
const (
	pushoverTitleLimit   = 250
	pushoverMessageLimit = 1024
)

// pushoverMessage is the body of a Pushover messages call.
type pushoverMessage struct {
	Token    string `json:"token"`
	User     string `json:"user"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	Priority int    `json:"priority"`
	URL      string `json:"url,omitempty"`
	URLTitle string `json:"url_title,omitempty"`
}

// postPushover sends notifications as one Pushover push. One alert's
// headline is the title and its details the message; a group's title
// says how many and its message lists their headlines. A push about a
// critical alert that is still open has high priority (1, past quiet
// hours); the rest normal (0). The link opens the alert in Dock.
func (c *Core) postPushover(ctx context.Context, ch store.AlertChannel, cfg channelConfig, group []apitypes.AlertNotification) error {
	msg := pushoverMessage{Token: cfg.Token, User: cfg.User}
	var lines []string
	first := group[0]
	if len(group) == 1 {
		msg.Title = clip(headline(first), pushoverTitleLimit-1)
		if first.Alert.Message != "" && first.Event != "resolved" {
			lines = append(lines, first.Alert.Message)
		}
		if first.Alert.Command != "" && first.Event != "resolved" {
			lines = append(lines, first.Alert.Command)
		}
		if len(lines) == 0 {
			lines = append(lines, first.Alert.Title) // Pushover needs a message
		}
	} else {
		msg.Title = strconv.Itoa(len(group)) + " alerts"
		for _, n := range group {
			lines = append(lines, headline(n))
		}
		first.Alert = apitypes.Alert{} // the link goes to the alerts page
	}
	msg.Message = joinFitting(lines, "\n", pushoverMessageLimit)
	for _, n := range group {
		if n.Alert.Severity == apitypes.SeverityCritical && n.Event != "resolved" && n.Event != "test" {
			msg.Priority = 1
		}
	}
	if link := c.alertLinks(ctx, ch.OrgID)(first.Alert); link != "" {
		msg.URL, msg.URLTitle = link, "Open in Dock"
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.alerts.post(ctx, ch.OrgID != nil, c.alerts.pushoverURL, body, nil)
}

// joinFitting joins texts with sep, keeping as many whole texts as fit in
// limit characters and saying how many were left out; a first text too
// long on its own is cut, leaving room to say how many follow it.
func joinFitting(texts []string, sep string, limit int) string {
	if all := strings.Join(texts, sep); utf8.RuneCountInString(all) <= limit {
		return all
	}
	more := func(kept int) string { return sep + "and " + strconv.Itoa(len(texts)-kept) + " more" }
	for kept := len(texts) - 1; kept > 0; kept-- {
		out := strings.Join(texts[:kept], sep) + more(kept)
		if utf8.RuneCountInString(out) <= limit {
			return out
		}
	}
	if len(texts) == 1 {
		return clip(texts[0], limit-1) // clip adds an ellipsis
	}
	tail := more(1)
	return clip(texts[0], limit-1-utf8.RuneCountInString(tail)) + tail
}

// signWebhook signs a webhook body: receivers recompute the HMAC-SHA256
// of "<t>.<body>" with the channel's secret, compare it in constant time
// and reject old timestamps, which stops replays.
func signWebhook(secret string, at time.Time, body []byte) string {
	t := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(t))
	mac.Write([]byte("."))
	mac.Write(body)
	return "t=" + t + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// newChannelClient is an HTTP client webhook, Slack, Discord and
// Pushover channels post with. It never follows redirects: a channel's URL is where alerts go,
// nowhere else. It never uses a proxy (that would connect to the proxy,
// not the channel, past the guard) and never connects to addresses g
// refuses (checked after DNS), and keeps no idle connections: alerts are
// rare.
func newChannelClient(g *egressGuard) *http.Client {
	return &http.Client{
		Timeout:       deliveryTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: deliveryTimeout, Control: g.control}).DialContext,
			TLSHandshakeTimeout: deliveryTimeout,
			DisableKeepAlives:   true,
		},
	}
}

// AllowLoopbackAlertChannels lets alert channels deliver to this server's
// own addresses, which they never may in production (egressGuard.addrErr).
// For tests whose receivers listen on 127.0.0.1.
func (c *Core) AllowLoopbackAlertChannels(allow bool) {
	c.alerts.guard.loopback.Store(allow)
	c.alerts.orgGuard.loopback.Store(allow)
}

// guardFor is the guard of an org's channels or hooks (org) or the
// platform's: only the operator's may reach private addresses, where a
// homelab's ntfy lives (ADR 0020); an org admin's would reach the LAN and
// other orgs' containers past the app firewall.
func (e *alertEngine) guardFor(org bool) (*egressGuard, *http.Client) {
	if org {
		return &e.orgGuard, e.orgClient
	}
	return &e.guard, e.client
}

// post sends a JSON body, with the org's guard (org) or the platform's
// (guardFor); anything but a 2xx answer is a *deliveryError.
func (e *alertEngine) post(ctx context.Context, org bool, target string, body []byte, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "dock/"+version.Version)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	_, client := e.guardFor(org)
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL may hold a token; don't repeat it
		}
		return fmt.Errorf("POST to the channel's URL: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		answer := strings.TrimSpace(string(snippet))
		de := &deliveryError{status: resp.StatusCode, answer: answer, msg: fmt.Sprintf("the channel's URL answered %s: %s", resp.Status, answer)}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			de.retryAfter = time.Duration(s) * time.Second
		}
		return de
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return nil
}

// headline is a notification's one-line summary. An alert whose
// condition occurred more than once says so (ADR 0097).
func headline(n apitypes.AlertNotification) string {
	a := n.Alert
	times := ""
	if a.Count > 1 {
		times = " (" + strconv.FormatInt(a.Count, 10) + " times)"
	}
	switch n.Event {
	case "resolved":
		if a.Count > 1 {
			times = " (happened " + strconv.FormatInt(a.Count, 10) + " times)"
		}
		return "[resolved] " + a.Title + times
	case "reminder":
		return "[" + string(a.Severity) + ", still open] " + a.Title + times
	case "repeated":
		return "[" + string(a.Severity) + ", again] " + a.Title + times
	case "test":
		return "[test] " + a.Title
	}
	return "[" + string(a.Severity) + "] " + a.Title + times
}

// slackText is a Slack message, escaped: alert messages quote release
// errors and build output, which must not turn into @channel mentions or
// disguised links (<https://...|text>). With a link (alertLink) it ends
// with Slack's <url|Open in Dock>, the one link Dock itself makes.
func slackText(n apitypes.AlertNotification, link string) string {
	text := headline(n)
	if n.Alert.Message != "" && n.Event != "resolved" {
		text += "\n" + n.Alert.Message
	}
	if n.Alert.Command != "" && n.Event != "resolved" {
		text += "\n`" + n.Alert.Command + "`"
	}
	text = slackEscaper.Replace(text)
	if link != "" {
		text += "\n<" + slackEscaper.Replace(link) + "|Open in Dock>"
	}
	return text
}

// alertLinks makes the links to alerts' UI pages for a channel of org
// (nil: the platform's): the release an alert names, else its service,
// else its server in the console, else the alerts page; "" when Dock has
// no UI URL (uiURL). Made once per message, so a group's links cost one
// indexed read of the org, not one per alert.
func (c *Core) alertLinks(ctx context.Context, org *int64) func(apitypes.Alert) string {
	ui := c.uiURL()
	switch {
	case ui == "":
		return func(apitypes.Alert) string { return "" }
	case org == nil:
		return func(a apitypes.Alert) string {
			if a.Server != "" {
				return ui + "/console/servers/" + url.PathEscape(a.Server)
			}
			return ui + "/console/alerts"
		}
	}
	o, err := c.store.GetOrg(ctx, *org)
	if err != nil {
		return func(apitypes.Alert) string { return "" }
	}
	return func(a apitypes.Alert) string { return orgAlertLink(ui+"/"+url.PathEscape(o.Slug), a) }
}

// orgAlertLink is the UI page of an org's alert a under base, the org's
// address: its release, else its service, else the org's alerts page.
func orgAlertLink(base string, a apitypes.Alert) string {
	if a.Project == "" || a.Environment == "" || a.Service == "" {
		return base + "/alerts"
	}
	base += "/" + url.PathEscape(a.Project) + "/" + url.PathEscape(a.Environment) + "/" + url.PathEscape(a.Service)
	if a.Target != nil && a.Target.Release != 0 {
		return base + "/releases/" + strconv.FormatInt(a.Target.Release, 10)
	}
	return base
}

// slackEscaper escapes Slack's control characters.
var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// emailText is a notification as a mail: the headline as its subject,
// the alert's details, and how often it happened when more than once.
func emailText(n apitypes.AlertNotification) (subject, text string) {
	a := n.Alert
	var b strings.Builder
	b.WriteString(a.Title + "\n\n")
	if a.Message != "" {
		b.WriteString(a.Message + "\n\n")
	}
	if a.Command != "" && a.ResolvedAt == nil {
		b.WriteString("More: " + a.Command + "\n\n")
	}
	for _, kv := range [][2]string{{"Project", a.Project}, {"Environment", a.Environment}, {"Service", a.Service}, {"Server", a.Server}} {
		if kv[1] != "" {
			b.WriteString(kv[0] + ": " + kv[1] + "\n")
		}
	}
	if a.Count > 1 {
		b.WriteString("Happened: " + strconv.FormatInt(a.Count, 10) + " times\n")
	}
	b.WriteString("Opened: " + a.OpenedAt.UTC().Format(time.RFC1123) + "\n")
	if a.ResolvedAt != nil {
		b.WriteString("Resolved: " + a.ResolvedAt.UTC().Format(time.RFC1123) + "\n")
	}
	b.WriteString("\nSee every open alert with `dock alerts`.\n")
	return "dock " + headline(n), b.String()
}

// oneLine keeps a header value on one line.
func oneLine(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }

func (c *Core) openChannel(ch store.AlertChannel) (channelConfig, error) {
	var cfg channelConfig
	plain, err := c.box.Open(ch.Config, channelAAD(ch.OrgID, ch.Name))
	if err != nil {
		return cfg, fmt.Errorf("open channel settings: %w", err)
	}
	return cfg, json.Unmarshal(plain, &cfg)
}

func toChannel(ch store.AlertChannel) apitypes.AlertChannel {
	out := apitypes.AlertChannel{
		Name: ch.Name, Kind: apitypes.AlertChannelKind(ch.Kind), Target: ch.Target,
		MinSeverity: apitypes.AlertSeverity(ch.MinSeverity), RenotifyMinutes: int(ch.RenotifyMinutes),
		CreatedAt: ch.CreatedAt, LastSentAt: ch.LastSentAt, LastErrorAt: ch.LastErrorAt, Failures: int(ch.Failures),
	}
	if ch.LastErrorAt != nil {
		out.LastError = ch.LastError
	}
	return out
}

// channelsIn is the org whose channels the actor manages: with platform
// the platform's (0), for operators; otherwise the actor's org, for its
// unlimited admins.
func channelsIn(a Actor, platform bool, action string) (int64, error) {
	if platform {
		return 0, a.requirePlatform(PlatformAlerts, action)
	}
	return a.OrgID, a.requireUnlimited(apitypes.ScopeAdmin, action)
}

// channelAudit is who a channel change is audited as: the actor in its
// org's log, or for a platform channel in the platform's.
func channelAudit(a Actor, org int64) Actor {
	if org == 0 {
		return a.platform()
	}
	return a
}

// ListAlertChannels lists the org's channels (with platform, the
// platform's), without their secrets.
func (c *Core) ListAlertChannels(ctx context.Context, a Actor, platform bool) ([]apitypes.AlertChannel, error) {
	org, err := channelsIn(a, platform, "reading alert channels")
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListAlertChannels(ctx, org)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.AlertChannel, len(rows))
	for i, r := range rows {
		out[i] = toChannel(r)
	}
	return out, nil
}

// CreateAlertChannel adds a channel to the org (with platform, to the
// platform). A webhook without a secret gets a generated one, returned
// only here.
func (c *Core) CreateAlertChannel(ctx context.Context, a Actor, platform bool, req apitypes.CreateAlertChannelRequest) (apitypes.CreatedAlertChannel, error) {
	org, err := channelsIn(a, platform, "adding alert channels")
	if err != nil {
		return apitypes.CreatedAlertChannel{}, err
	}
	if err := validName("channel", req.Name); err != nil {
		return apitypes.CreatedAlertChannel{}, err
	}
	cfg, target, generated, err := c.alerts.channelSettings(req, !platform)
	if err != nil {
		return apitypes.CreatedAlertChannel{}, err
	}
	sev := req.MinSeverity
	if sev == "" {
		sev = apitypes.SeverityWarning
	}
	if sev != apitypes.SeverityWarning && sev != apitypes.SeverityCritical {
		return apitypes.CreatedAlertChannel{}, invalid("min severity must be warning or critical")
	}
	renotify := defaultRenotify
	if req.RenotifyMinutes != nil {
		renotify = *req.RenotifyMinutes
	}
	if renotify < 0 || renotify > 7*24*60 {
		return apitypes.CreatedAlertChannel{}, invalid("renotify minutes must be 0 (never) to 10080 (a week)")
	}
	plain, err := json.Marshal(cfg)
	if err != nil {
		return apitypes.CreatedAlertChannel{}, err
	}
	orgID := orgRef(org)
	sealed, err := c.box.Seal(plain, channelAAD(orgID, req.Name))
	if err != nil {
		return apitypes.CreatedAlertChannel{}, err
	}
	row, err := c.store.CreateAlertChannel(ctx, store.CreateAlertChannelParams{
		OrgID: orgID, Name: req.Name, Kind: string(req.Kind), Target: target, Config: sealed, MinSeverity: string(sev),
		RenotifyMinutes: int64(renotify), CreatedAt: now(),
	})
	if store.IsUnique(err) {
		return apitypes.CreatedAlertChannel{}, conflict("alert channel %q exists; remove it first to change it", req.Name)
	}
	if err != nil {
		return apitypes.CreatedAlertChannel{}, err
	}
	c.audit(ctx, channelAudit(a, org), "alert.channel_add", nil, nil, "alert channel "+req.Name,
		fmt.Sprintf("added %s alert channel %s to %s (%s and up)", req.Kind, req.Name, target, sev))
	out := apitypes.CreatedAlertChannel{Channel: toChannel(row), Secret: generated}
	if req.Kind == apitypes.ChannelEmail {
		if _, err := c.store.GetEmailSettings(ctx); store.IsNotFound(err) {
			out.Warning = emailNotSetUpWarning
		}
	}
	return out, nil
}

// emailNotSetUpWarning is what adding or testing an email channel says
// while Dock has no email server: nothing reaches it until an operator
// sets one up, and the channel itself needs no change.
const emailNotSetUpWarning = "Email isn't set up on this Dock, so nothing reaches this channel yet. An operator sets it up with `dock email set` or the console's Email page; the channel then works as it is."

// channelSettings validates a new channel's settings and returns what is
// sealed, what lists show, and a generated webhook secret.
func (e *alertEngine) channelSettings(req apitypes.CreateAlertChannelRequest, org bool) (cfg channelConfig, target, generated string, err error) {
	guard, _ := e.guardFor(org)
	if req.Kind != apitypes.ChannelPushover && (req.User != "" || req.Token != "") {
		return cfg, "", "", invalid("a user key and token are for pushover channels")
	}
	switch req.Kind {
	case apitypes.ChannelWebhook, apitypes.ChannelSlack, apitypes.ChannelDiscord:
		u, perr := url.Parse(req.URL)
		if req.URL == "" || perr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return cfg, "", "", invalid("a %s channel needs --url, an http:// or https:// URL", req.Kind)
		}
		// A Discord webhook's URL is its credential, and Discord serves
		// only https.
		if req.Kind == apitypes.ChannelDiscord && u.Scheme != "https" {
			return cfg, "", "", invalid("a discord channel needs the webhook's https:// URL")
		}
		if len(req.To) > 0 {
			return cfg, "", "", invalid("recipients are for email channels")
		}
		// Named hosts are checked when delivering (newChannelClient), after DNS.
		if err := guard.hostErr(u.Hostname()); err != nil {
			return cfg, "", "", invalid("%v", err)
		}
		cfg.URL, target = req.URL, u.Scheme+"://"+u.Host
		if req.Kind == apitypes.ChannelWebhook {
			cfg.Secret = req.Secret
			if cfg.Secret == "" {
				b := make([]byte, 32)
				_, _ = rand.Read(b)
				cfg.Secret = hex.EncodeToString(b)
				generated = cfg.Secret
			}
		} else if req.Secret != "" {
			return cfg, "", "", invalid("only webhook channels are signed; %s channels take no secret", req.Kind)
		}
	case apitypes.ChannelPushover:
		if !pushoverKey.MatchString(req.User) || !pushoverKey.MatchString(req.Token) {
			return cfg, "", "", invalid("a pushover channel needs a user (or group) key and an application token, 30 letters and digits each, from pushover.net")
		}
		if req.URL != "" || req.Secret != "" || len(req.To) > 0 {
			return cfg, "", "", invalid("pushover channels take a user key and token, not a URL, secret or recipients")
		}
		cfg.User, cfg.Token = req.User, req.Token
		target = "pushover.net"
	case apitypes.ChannelEmail:
		if len(req.To) == 0 {
			return cfg, "", "", invalid("an email channel needs at least one recipient")
		}
		if len(req.To) > maxChannelRecipients {
			return cfg, "", "", invalid("an email channel takes at most %d recipients; send to a list's address for more", maxChannelRecipients)
		}
		if req.URL != "" || req.Secret != "" {
			return cfg, "", "", invalid("email channels take recipients, not a URL or secret")
		}
		for _, addr := range req.To {
			a, perr := mail.ParseAddress(addr)
			if perr != nil {
				return cfg, "", "", invalid("%q is not an email address", addr)
			}
			cfg.To = append(cfg.To, a.Address)
		}
		target = strings.Join(cfg.To, ", ")
	default:
		return cfg, "", "", invalid("channel kind must be webhook, slack, discord, pushover or email")
	}
	return cfg, target, generated, nil
}

// RemoveAlertChannel deletes one of the org's channels (with platform, the
// platform's); notifications it is retrying stop with the next attempt.
func (c *Core) RemoveAlertChannel(ctx context.Context, a Actor, platform bool, name string) error {
	org, err := channelsIn(a, platform, "removing alert channels")
	if err != nil {
		return err
	}
	ch, err := c.channel(ctx, org, name)
	if err != nil {
		return err
	}
	if err := c.store.DeleteAlertChannel(ctx, ch.ID); err != nil {
		return err
	}
	c.connectionWorks(ctx, channelAlertKey(ch.ID)) // a broken channel's alert goes with it
	c.audit(ctx, channelAudit(a, org), "alert.channel_remove", nil, nil, "alert channel "+name, "removed alert channel "+name)
	return nil
}

// pushoverKey is the form of Pushover's user, group and application keys.
var pushoverKey = regexp.MustCompile(`^[A-Za-z0-9]{30}$`)

// maxChannelRecipients caps an email channel's recipients, in core as in
// the API's schema: every alert and every test mails each of them.
const maxChannelRecipients = 20

// emailTestEvery is how often an organization may test an email channel.
const emailTestEvery = time.Minute

// TestAlertChannel sends a test notification to one of the org's channels
// (with platform, the platform's) once, without retries, and reports how
// it went; the outcome is recorded on the channel like any delivery.
func (c *Core) TestAlertChannel(ctx context.Context, a Actor, platform bool, name string) (apitypes.AlertChannelTest, error) {
	org, err := channelsIn(a, platform, "testing alert channels")
	if err != nil {
		return apitypes.AlertChannelTest{}, err
	}
	ch, err := c.channel(ctx, org, name)
	if err != nil {
		return apitypes.AlertChannelTest{}, err
	}
	t := now()
	if apitypes.AlertChannelKind(ch.Kind) == apitypes.ChannelEmail {
		// A test mails the channel's recipients at once, past the outbox
		// and its limits, to addresses nobody confirmed: one a minute an
		// organization, so testing can't be used to send mail in bulk.
		c.alerts.mu.Lock()
		if c.alerts.mailTested == nil {
			c.alerts.mailTested = map[int64]time.Time{}
		}
		last, tested := c.alerts.mailTested[org]
		wait := emailTestEvery - t.Sub(last)
		if !tested || wait <= 0 {
			c.alerts.mailTested[org] = t
		}
		c.alerts.mu.Unlock()
		if tested && wait > 0 {
			return apitypes.AlertChannelTest{}, errf(apitypes.CodeRateLimited, "an email channel was tested less than a minute ago; try again in %d seconds", int(wait.Seconds())+1)
		}
	}
	c.audit(ctx, channelAudit(a, org), "alert.channel_test", nil, nil, "alert channel "+name, "sent a test alert to channel "+name)
	n := apitypes.AlertNotification{Event: "test", SentAt: t, Alert: apitypes.Alert{
		Kind: "test", Severity: apitypes.SeverityWarning, Title: "Test alert from dock",
		Message: "Channel " + name + " works. Alerts at " + ch.MinSeverity + " and up come here.", OpenedAt: t, UpdatedAt: t,
	}}
	out := apitypes.AlertChannelTest{OK: true}
	if err := c.send(ctx, ch, n); err != nil {
		out.OK, out.Error = false, err.Error()
		c.channelFailed(ctx, ch.ID, err)
	} else {
		c.channelSent(ctx, ch.ID)
	}
	if ch, err = c.store.GetAlertChannelByID(ctx, ch.ID); err != nil {
		return out, err
	}
	out.Channel = toChannel(ch)
	return out, nil
}

// channel is the org's channel (org 0: the platform's) with name.
func (c *Core) channel(ctx context.Context, org int64, name string) (store.AlertChannel, error) {
	ch, err := c.store.GetAlertChannel(ctx, store.GetAlertChannelParams{Org: org, Name: name})
	if store.IsNotFound(err) {
		list := "`dock alerts channel list`"
		if org == 0 {
			list = "`dock alerts channel list --platform`"
		}
		return ch, notFound("alert channel %q not found; %s lists them", name, list)
	}
	return ch, err
}

// orgRef is an org ID as a nullable column: nil for 0, the platform.
func orgRef(org int64) *int64 {
	if org == 0 {
		return nil
	}
	return &org
}

// channelAAD binds a channel's sealed settings to its row: a platform
// channel's by its name, as before channels had orgs; an org's by
// "<org ID>/<name>" (names have no slash), matching
// sealedAlertChannel.Key.
func channelAAD(org *int64, name string) secrets.AAD {
	if org == nil {
		return sealedAlertChannel.AAD(name)
	}
	return sealedAlertChannel.AAD(strconv.FormatInt(*org, 10) + "/" + name)
}

// copyPlatformChannels makes the copies of platform channels that
// migration 0070 asked for: each into an org with an operator among its
// admins, so those orgs keep hearing about their services now that
// platform channels receive only platform alerts (ADR 0062). Each copy is
// sealed for its own row, written with its request's removal, and audited
// in the org's log. Start runs it; with nothing asked for it is one
// query.
func (c *Core) copyPlatformChannels(ctx context.Context) error {
	rows, err := c.store.ListAlertChannelCopies(ctx)
	if err != nil {
		return fmt.Errorf("list alert channel copies: %w", err)
	}
	for _, r := range rows {
		ch, org := r.AlertChannel, r.CopyOrgID
		plain, err := c.box.Open(ch.Config, channelAAD(nil, ch.Name))
		if err != nil {
			c.log.Error("alerts: copy a platform channel into an org", "channel", ch.Name, "org", org, "err", err)
			continue
		}
		sealed, err := c.box.Seal(plain, channelAAD(&org, ch.Name))
		if err != nil {
			return err
		}
		err = c.store.Tx(ctx, func(q *store.Queries) error {
			_, err := q.CreateAlertChannel(ctx, store.CreateAlertChannelParams{
				OrgID: &org, Name: ch.Name, Kind: ch.Kind, Target: ch.Target, Config: sealed, MinSeverity: ch.MinSeverity,
				RenotifyMinutes: ch.RenotifyMinutes, CreatedAt: now(),
			})
			if err != nil && !store.IsUnique(err) {
				return err
			}
			return q.DeleteAlertChannelCopy(ctx, store.DeleteAlertChannelCopyParams{ChannelID: ch.ID, OrgID: org})
		})
		if err != nil {
			return fmt.Errorf("copy alert channel %s: %w", ch.Name, err)
		}
		c.audit(ctx, System.InOrg(org), "alert.channel_add", nil, nil, "alert channel "+ch.Name,
			fmt.Sprintf("copied platform alert channel %s (%s to %s) into the organization: platform channels now get only platform alerts", ch.Name, ch.Kind, ch.Target))
	}
	return nil
}
