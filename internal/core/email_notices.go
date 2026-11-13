package core

import (
	"cmp"
	"context"
	"errors"
	"strconv"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/mail"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Mail that follows something already done (ADR 0079): security notices
// to an account's own address, and access requests to the admins who
// decide them. Each goes through the outbox, outside the transaction of
// what caused it, and is skipped quietly while email isn't set up: the
// action stands either way.

// errEmailNotSetUp is mail that can't go: the platform has no SMTP
// server.
var errEmailNotSetUp = errors.New("email isn't set up on this Dock; an operator sets it up with `dock email set` or the console's Email page")

// queueNow queues m on its own and starts sending it. errEmailNotSetUp
// while email isn't set up.
func (c *Core) queueNow(ctx context.Context, m outgoing) (store.EmailOutbox, error) {
	if _, err := c.store.GetEmailSettings(ctx); store.IsNotFound(err) {
		return store.EmailOutbox{}, errEmailNotSetUp
	} else if err != nil {
		return store.EmailOutbox{}, err
	}
	row, err := c.queueEmail(ctx, c.store.Queries, m)
	if err != nil {
		return row, err
	}
	c.emailQueued(ctx, row)
	return row, nil
}

// securityNotice tells u their sign-in changed: what (a one-line
// sentence) and who did it when it wasn't them (by). Notices can't be
// turned off; they aren't held back by the per-address limit.
func (c *Core) securityNotice(ctx context.Context, u store.User, what, by string) {
	var account string
	if base := c.publicURL(); base != "" {
		account = base + "/account"
	}
	content, err := mail.NoticeMail(mail.Notice{What: what, By: by, When: now(), Email: u.Email, Account: account, Site: c.siteName()})
	if err == nil {
		_, err = c.queueNow(ctx, outgoing{kind: apitypes.EmailNotice, ref: userRef(u.ID), to: u.Email, content: content})
	}
	if err != nil && !errors.Is(err, errEmailNotSetUp) {
		c.log.Warn("email: security notice not queued", "user", u.Email, "err", err)
	}
}

// noticeFor sends u a security notice after an action by a: in their own
// name when they did it, else naming who did.
func (c *Core) noticeFor(ctx context.Context, a Actor, u store.User, what string) {
	var by string
	if a.UserID != u.ID {
		by = cmp.Or(a.DisplayName, a.Name)
	}
	c.securityNotice(ctx, u, what, by)
}

// requestMail tells the admins who may decide a new access request about
// it, each unless they turned it off. It needs Dock's public address for
// the Inbox link; without one, the Inbox and the org's alert channels
// carry it alone.
func (c *Core) requestMail(ctx context.Context, r store.AccessRequest, le *LockedError) {
	base := c.publicURL()
	if base == "" {
		return
	}
	if _, err := c.store.GetEmailSettings(ctx); err != nil {
		return
	}
	admins, err := c.store.ListRequestDeciders(ctx, r.OrgID)
	if err != nil || len(admins) == 0 {
		return
	}
	o, err := c.store.GetOrg(ctx, r.OrgID)
	if err != nil {
		return
	}
	content, err := mail.RequestMail(mail.Request{
		ID: r.ID, Title: r.Title, Org: cmp.Or(o.Name, o.Slug), Project: le.Project, Environment: le.Environment,
		RequestedBy: r.RequestedBy, Reason: r.Reason, Expires: r.ExpiresAt,
		URL: base + "/" + o.Slug + "/inbox", Account: base + "/account", Site: c.siteName(),
	})
	if err != nil {
		c.log.Error("email: render an access request", "err", err)
		return
	}
	for _, u := range admins {
		if _, err := c.queueNow(ctx, outgoing{kind: apitypes.EmailAccessRequest, orgID: r.OrgID, ref: "request:" + strconv.FormatInt(r.ID, 10), to: u.Email, content: content}); err != nil {
			c.log.Warn("email: access request not queued", "to", u.Email, "err", err)
		}
	}
}

// sendNow sends one mail now through the platform's server, skipping the
// outbox (tests of the settings and of alert channels), and records the
// server's status.
func (c *Core) sendNow(ctx context.Context, to []mail.Address, content mail.Content) error {
	srv, ok, err := c.loadEmailServer(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return errEmailNotSetUp
	}
	ctx, cancel := context.WithTimeout(ctx, emailTimeout)
	defer cancel()
	var sendErr error
	msg := mail.Message{From: srv.from, ReplyTo: srv.replyTo, To: to, Content: content}
	if err := mail.Send(ctx, c.email.dial, srv.server, []mail.Message{msg}, func(_ int, err error) { sendErr = err }); err != nil {
		c.emailServerFailed(ctx, err)
		return err
	}
	c.emailServerOK(ctx)
	return sendErr
}

// MyEmail is which optional mail the calling user gets. Users only.
func (c *Core) MyEmail(ctx context.Context, a Actor) (apitypes.MyEmail, error) {
	if a.UserID == 0 {
		return apitypes.MyEmail{}, invalid("only users get email; API tokens have no account")
	}
	u, err := c.store.GetUser(ctx, a.UserID)
	if err != nil {
		return apitypes.MyEmail{}, err
	}
	return apitypes.MyEmail{AccessRequests: u.EmailRequests}, nil
}

// SetMyEmail turns the calling user's optional mail on or off. Users
// only, from a session or a `dock login` token.
func (c *Core) SetMyEmail(ctx context.Context, a Actor, req apitypes.MyEmail) (apitypes.MyEmail, error) {
	if a.UserID == 0 {
		return apitypes.MyEmail{}, invalid("only users get email; API tokens have no account")
	}
	if err := c.store.SetUserEmailRequests(ctx, store.SetUserEmailRequestsParams{EmailRequests: req.AccessRequests, ID: a.UserID}); err != nil {
		return apitypes.MyEmail{}, err
	}
	state := "off"
	if req.AccessRequests {
		state = "on"
	}
	c.audit(ctx, a.platform(), "user.email_requests", nil, nil, a.Email, "turned access request emails "+state)
	return req, nil
}
