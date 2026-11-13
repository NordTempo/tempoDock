package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/mail"
	"github.com/NordTempo/tempoDock/internal/store"
)

// The outbox (ADR 0079). A mail is a row in email_outbox, inserted with
// what caused it (an invite and its mail commit together), so the request
// never waits on SMTP. The sender is one goroutine that runs only while
// mail is due: an insert kicks it, it sends what is due over one
// connection, and with only retries left it waits on one timer for the
// earliest; with nothing queued it exits. Queued mail survives restarts:
// Start kicks it once.

// emailBackoff is how long a mail waits before each retry; after the
// last it is given up on (about 2 h 45 min after it was queued).
var emailBackoff = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, 2 * time.Hour}

const (
	// emailMaxQueued and emailMaxQueuedOrg bound the outbox: past them
	// new mail is refused (the caller says so, and an invite's link is
	// still shown).
	emailMaxQueued    = 1000
	emailMaxQueuedOrg = 100
	// emailPerAddress mails an hour go to one address at most, so a
	// looping script can't bury someone's inbox.
	emailPerAddress = 10
	// emailPerMinute sends a minute at most, across the platform.
	emailPerMinute = 60
	// emailBatch mails go over one connection at most.
	emailBatch = 20
	// emailTimeout bounds one connection's conversation.
	emailTimeout = time.Minute
	// emailKeep is how long done mails stay listed.
	emailKeep = 30 * 24 * time.Hour
)

// emailState is the sender's state.
type emailState struct {
	mu      sync.Mutex
	sending bool
	// started is set by Start: only the daemon sends. A command that runs
	// core on its own (`dock admin reset-2fa`) queues mail for the
	// daemon's next pass instead of racing it.
	started bool
	kick    chan struct{}
	// recent are the send times of the last minute, for emailPerMinute;
	// only the sender goroutine touches it.
	recent []time.Time
	// guard is where the SMTP server may be. It is the operator's, like
	// the platform's alert channels: private addresses are fine (a LAN
	// relay), and so is this server itself (a local Postfix); link-local
	// and cloud metadata addresses never.
	guard *egressGuard
	dial  mail.Dialer
}

func newEmailState() emailState {
	g := &egressGuard{what: "email"}
	g.loopback.Store(true)
	return emailState{
		kick:  make(chan struct{}, 1),
		guard: g,
		dial:  (&net.Dialer{Control: g.control, Timeout: 15 * time.Second}).DialContext,
	}
}

// outgoing is a mail to queue.
type outgoing struct {
	kind    apitypes.EmailKind
	orgID   int64 // 0: the platform's
	ref     string
	to      string // a bare, checked address
	content mail.Content
}

// queueEmail adds m to the outbox through q (the caller's transaction),
// unless the outbox is full or m's address had too many mails in the last
// hour (errEmailRefused; not for security notices and alerts). The caller kicks the sender and publishes the
// row once q's transaction commits (emailQueued).
func (c *Core) queueEmail(ctx context.Context, q *store.Queries, m outgoing) (store.EmailOutbox, error) {
	n := now()
	counts, err := q.CountQueuedEmail(ctx, orgRef(m.orgID))
	if err != nil {
		return store.EmailOutbox{}, err
	}
	if counts.Total >= emailMaxQueued || (m.orgID != 0 && counts.Org >= emailMaxQueuedOrg) {
		return store.EmailOutbox{}, errEmailRefused{"too many emails are waiting to be sent; send the link yourself, and ask an operator to check the console's Email page"}
	}
	// Security notices and alerts aren't held back: they follow what
	// already happened, and an alert storm is bounded by its channel.
	limited := m.kind != apitypes.EmailNotice && m.kind != apitypes.EmailAlert
	sent, err := q.CountEmailTo(ctx, store.CountEmailToParams{ToAddr: m.to, Since: n.Add(-time.Hour).Unix()})
	if err != nil {
		return store.EmailOutbox{}, err
	}
	if limited && sent >= emailPerAddress {
		return store.EmailOutbox{}, errEmailRefused{m.to + " got " + strconv.Itoa(emailPerAddress) + " emails from Dock in the last hour; send the link yourself, or try again later"}
	}
	body, err := json.Marshal(m.content)
	if err != nil {
		return store.EmailOutbox{}, err
	}
	nonce := newNonce()
	sealed, err := c.box.Seal(body, sealedEmailBody.AAD(nonce))
	if err != nil {
		return store.EmailOutbox{}, err
	}
	return q.AddEmail(ctx, store.AddEmailParams{
		Kind: string(m.kind), OrgID: orgRef(m.orgID), Ref: m.ref, ToAddr: m.to, Subject: m.content.Subject,
		Nonce: nonce, Body: sealed, Status: string(apitypes.EmailQueued), NextAt: n.Unix(), CreatedAt: n.Unix(),
	})
}

// emailQueued starts sending a mail queueEmail added, once its
// transaction committed.
func (c *Core) emailQueued(ctx context.Context, row store.EmailOutbox) {
	c.publishEmail(ctx, row)
	c.kickEmail()
}

// newNonce is a random key for a sealed body's AAD.
func newNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// kickEmail wakes the sender, starting it if it isn't running. A kick
// before Start or after Close does nothing.
func (c *Core) kickEmail() {
	c.email.mu.Lock()
	defer c.email.mu.Unlock()
	switch {
	case !c.email.started:
	case c.email.sending:
		select {
		case c.email.kick <- struct{}{}:
		default:
		}
	case c.ctx.Err() == nil:
		c.email.sending = true
		c.wg.Add(1)
		go c.sendEmails()
	}
}

// sendEmails is the sender: a pass sends what is due; then it waits for
// the next due mail (one timer) or a kick, and exits with nothing queued.
func (c *Core) sendEmails() {
	defer c.wg.Done()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		next, err := c.emailPass(c.ctx)
		if c.ctx.Err() != nil {
			return
		}
		if err != nil {
			c.log.Error("email: send", "err", err)
			next = now().Add(time.Minute)
		}
		if next.IsZero() {
			if c.stopEmail() {
				return
			}
			continue
		}
		if wait := time.Until(next); wait > 0 {
			timer.Reset(wait)
			select {
			case <-c.ctx.Done():
				timer.Stop()
				return
			case <-c.email.kick:
				timer.Stop()
			case <-timer.C:
			}
		}
	}
}

// stopEmail marks the sender stopped unless a kick came in since its
// pass; then it takes the kick and the sender passes again.
func (c *Core) stopEmail() bool {
	c.email.mu.Lock()
	defer c.email.mu.Unlock()
	select {
	case <-c.email.kick:
		return false
	default:
		c.email.sending = false
		return true
	}
}

// emailPass sends the mails due now, at most emailBatch over one
// connection and emailPerMinute a minute, and returns when to pass
// again: now when it sent some (more may be due), the next due mail's
// time, or zero with none queued.
func (c *Core) emailPass(ctx context.Context) (time.Time, error) {
	n := now()
	room := c.email.room(n)
	if room == 0 {
		return c.email.recent[0].Add(time.Minute), nil
	}
	rows, err := c.store.ListDueEmail(ctx, store.ListDueEmailParams{Now: n.Unix(), Lim: int64(min(room, emailBatch))})
	if err != nil {
		return time.Time{}, err
	}
	if len(rows) == 0 {
		next, err := c.store.NextEmailAt(ctx)
		if err != nil || next == 0 {
			return time.Time{}, err
		}
		return time.Unix(next, 0), nil
	}
	srv, ok, err := c.loadEmailServer(ctx)
	if err != nil {
		return time.Time{}, err
	}
	if !ok {
		for _, r := range rows {
			c.emailDone(ctx, r, apitypes.EmailFailed, "email isn't set up on this Dock any more")
		}
		return n, nil
	}
	msgs := make([]mail.Message, 0, len(rows))
	sending := make([]store.EmailOutbox, 0, len(rows))
	for _, r := range rows {
		var content mail.Content
		plain, err := c.box.Open(r.Body, sealedEmailBody.AAD(r.Nonce))
		if err == nil {
			err = json.Unmarshal(plain, &content)
		}
		if err != nil {
			c.emailDone(ctx, r, apitypes.EmailFailed, "its body doesn't open: "+err.Error())
			continue
		}
		msgs = append(msgs, mail.Message{From: srv.from, ReplyTo: srv.replyTo, To: []mail.Address{{Address: r.ToAddr}}, Content: content})
		sending = append(sending, r)
	}
	if len(msgs) == 0 {
		return n, nil
	}
	sctx, cancel := context.WithTimeout(ctx, emailTimeout)
	defer cancel()
	tried, ok := 0, false
	connErr := mail.Send(sctx, c.email.dial, srv.server, msgs, func(i int, err error) {
		tried = i + 1
		c.email.recent = append(c.email.recent, now())
		if !ok {
			// The server took the conversation this far: it works,
			// recorded before the mail's outcome is published.
			ok = true
			c.emailServerOK(ctx)
		}
		c.emailOutcome(ctx, sending[i], err)
	})
	if connErr != nil {
		c.emailServerFailed(ctx, connErr)
		for _, r := range sending[tried:] {
			c.emailOutcome(ctx, r, connErr)
		}
	}
	return n, nil
}

// room is how many mails may go now under emailPerMinute, dropping send
// times older than a minute.
func (e *emailState) room(n time.Time) int {
	i := 0
	for i < len(e.recent) && n.Sub(e.recent[i]) >= time.Minute {
		i++
	}
	e.recent = e.recent[i:]
	return max(emailPerMinute-len(e.recent), 0)
}

// emailOutcome records an attempt: sent; failed for good (a 5xx answer,
// or the last retry); or due again after its backoff.
func (c *Core) emailOutcome(ctx context.Context, r store.EmailOutbox, err error) {
	if err == nil {
		c.emailDone(ctx, r, apitypes.EmailSent, "")
		return
	}
	msg := oneLine(err.Error())
	if mail.Permanent(err) || int(r.Attempts) >= len(emailBackoff) {
		c.emailDone(ctx, r, apitypes.EmailFailed, msg)
		return
	}
	row, rerr := c.store.RetryEmail(ctx, store.RetryEmailParams{Error: msg, NextAt: now().Add(emailBackoff[r.Attempts]).Unix(), ID: r.ID})
	if rerr != nil {
		if !store.IsNotFound(rerr) {
			c.log.Error("email: record a retry", "id", r.ID, "err", rerr)
		}
		return
	}
	c.publishEmail(ctx, row)
}

// emailDone records a mail as sent or failed and drops its body.
func (c *Core) emailDone(ctx context.Context, r store.EmailOutbox, status apitypes.EmailState, msg string) {
	row, err := c.store.FinishEmail(ctx, store.FinishEmailParams{Status: string(status), Error: msg, DoneAt: ptr(now().Unix()), ID: r.ID})
	if err != nil {
		if !store.IsNotFound(err) {
			c.log.Error("email: record the outcome", "id", r.ID, "err", err)
		}
		return
	}
	if status == apitypes.EmailFailed {
		c.log.Warn("email: gave up", "id", r.ID, "kind", r.Kind, "err", msg)
	}
	// An alert channel's mail is its delivery: its status is the mail's.
	if id, ok := strings.CutPrefix(r.Ref, "channel:"); ok {
		if ch, err := strconv.ParseInt(id, 10, 64); err == nil {
			if status == apitypes.EmailSent {
				c.channelSent(ctx, ch)
			} else {
				c.channelFailed(ctx, ch, errors.New(msg))
			}
		}
	}
	c.publishEmail(ctx, row)
}

// publishEmail sends a mail's new state live: to its org's admins (an
// invite's section and row) and to operators (the console's list).
func (c *Core) publishEmail(ctx context.Context, r store.EmailOutbox) {
	var org string
	if r.OrgID != nil {
		if o, err := c.store.GetOrg(ctx, *r.OrgID); err == nil {
			org = o.Slug
		}
	}
	d := toDelivery(r, org)
	ev := apitypes.Event{Type: apitypes.EventEmail, Email: &d}
	if r.OrgID != nil {
		c.bus.publish(busEvent{orgID: *r.OrgID, adminsOnly: true, ev: ev})
	}
	c.bus.publish(busEvent{serverStats: true, ev: ev})
}

// toDelivery is a mail as the API shows it.
func toDelivery(r store.EmailOutbox, org string) apitypes.EmailDelivery {
	d := apitypes.EmailDelivery{
		ID: r.ID, Kind: apitypes.EmailKind(r.Kind), To: r.ToAddr, Subject: r.Subject, Org: org,
		Status: apitypes.EmailState(r.Status), Attempts: int(r.Attempts), Error: r.Error, CreatedAt: time.Unix(r.CreatedAt, 0).UTC(),
	}
	if id, ok := strings.CutPrefix(r.Ref, "invite:"); ok {
		d.Invite, _ = strconv.ParseInt(id, 10, 64)
	}
	if r.Status == string(apitypes.EmailQueued) {
		d.NextAt = ptr(time.Unix(r.NextAt, 0).UTC())
	}
	if r.DoneAt != nil {
		d.DoneAt = ptr(time.Unix(*r.DoneAt, 0).UTC())
	}
	return d
}

// pruneEmail deletes mails done longer than emailKeep ago (retention).
func (c *Core) pruneEmail(ctx context.Context, n time.Time) (int64, error) {
	return c.store.DeleteOldEmail(ctx, ptr(n.Add(-emailKeep).Unix()))
}
