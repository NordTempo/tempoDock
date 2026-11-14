package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/mail"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Dock's own email (ADR 0079). Operators set the platform's SMTP server
// once (here, `dock email set`, the console's Email page); every mail
// Dock sends goes through it, by the outbox (email_send.go). Orgs don't
// set a sender of their own.

// defaultFromName is the From name when the operator sets none.
const defaultFromName = "Tempo Dock"

// emailServer is the platform's SMTP settings as the sender uses them.
type emailServer struct {
	server  mail.Server
	from    mail.Address
	replyTo *mail.Address
}

// loadEmailServer reads and opens the platform's SMTP settings; ok is
// false while email isn't set up.
func (c *Core) loadEmailServer(ctx context.Context) (s emailServer, ok bool, err error) {
	st, err := c.store.GetEmailSettings(ctx)
	if store.IsNotFound(err) {
		return s, false, nil
	}
	if err != nil {
		return s, false, err
	}
	s.server = mail.Server{Host: st.Host, Port: int(st.Port), Security: mail.Security(st.Security), Username: st.Username}
	if len(st.Password) > 0 {
		pw, err := c.box.Open(st.Password, emailPasswordAAD)
		if err != nil {
			return s, false, fmt.Errorf("open the SMTP password: %w; set it again with `dock email set`", err)
		}
		s.server.Password = string(pw)
	}
	s.from = mail.Address{Name: cmp.Or(st.FromName, defaultFromName), Address: st.FromAddr}
	if st.ReplyTo != "" {
		if s.replyTo, err = mail.ParseAddress(st.ReplyTo); err != nil {
			return s, false, err
		}
	}
	return s, true, nil
}

// EmailSettings is the platform's SMTP server and how sending goes.
// Operators only.
func (c *Core) EmailSettings(ctx context.Context, a Actor) (apitypes.EmailSettings, error) {
	if err := a.requirePlatform(PlatformView, "reading the email settings"); err != nil {
		return apitypes.EmailSettings{}, err
	}
	return c.emailSettings(ctx)
}

// emailSettings reads the settings and the outbox's counts: three
// queries.
func (c *Core) emailSettings(ctx context.Context) (apitypes.EmailSettings, error) {
	out := apitypes.EmailSettings{LinksURL: c.publicURL()}
	st, err := c.store.GetEmailSettings(ctx)
	switch {
	case store.IsNotFound(err):
	case err != nil:
		return out, err
	default:
		out.Configured = true
		out.Host, out.Port, out.Security, out.Username = st.Host, int(st.Port), apitypes.EmailSecurity(st.Security), st.Username
		out.PasswordSet = len(st.Password) > 0
		out.From, out.FromName, out.ReplyTo = st.FromAddr, cmp.Or(st.FromName, defaultFromName), st.ReplyTo
		out.UpdatedBy, out.UpdatedAt = st.UpdatedBy, &st.UpdatedAt
		out.Status.LastOKAt, out.Status.LastError, out.Status.LastErrorAt = st.LastOkAt, st.LastError, st.LastErrorAt
	}
	n, err := c.store.EmailCounts(ctx, ptr(now().Add(-24*time.Hour).Unix()))
	if err != nil {
		return out, err
	}
	out.Status.Queued, out.Status.FailedDay = int(n.Queued), int(n.Failed)
	if n.Queued > 0 {
		next, err := c.store.NextEmailAt(ctx)
		if err != nil {
			return out, err
		}
		out.Status.NextAt = ptr(time.Unix(next, 0).UTC())
	}
	return out, nil
}

// SetEmail sets the platform's SMTP server. Operators only, recently
// confirmed (ADR 0057): it decides where every invite link goes. Mail
// waiting for a retry is tried again at once, as the fix may be this.
func (c *Core) SetEmail(ctx context.Context, a Actor, req apitypes.SetEmailRequest) (apitypes.EmailSettings, error) {
	if err := a.requirePlatform(PlatformSettings, "setting up email"); err != nil {
		return apitypes.EmailSettings{}, err
	}
	if err := a.requireRecent("setting up email"); err != nil {
		return apitypes.EmailSettings{}, err
	}
	host := strings.TrimSpace(req.Host)
	if host == "" || strings.ContainsAny(host, " \t\r\n/:@") && net.ParseIP(host) == nil {
		return apitypes.EmailSettings{}, invalid("SMTP host %q isn't a hostname or an IP address", req.Host)
	}
	if err := c.email.guard.hostErr(host); err != nil {
		return apitypes.EmailSettings{}, invalid("%s", err.Error())
	}
	sec := req.Security
	switch {
	case sec == "" && req.Port == 465:
		sec = apitypes.EmailTLS
	case sec == "":
		sec = apitypes.EmailStartTLS
	case sec != apitypes.EmailTLS && sec != apitypes.EmailStartTLS:
		return apitypes.EmailSettings{}, invalid("security must be starttls or tls")
	}
	port := req.Port
	if port == 0 {
		port = 587
		if sec == apitypes.EmailTLS {
			port = 465
		}
	}
	from, err := mail.ParseAddress(req.From)
	if err != nil {
		return apitypes.EmailSettings{}, invalid("the From address %q isn't an email address", req.From)
	}
	fromName := strings.TrimSpace(req.FromName)
	if fromName == "" {
		fromName = from.Name
	}
	if strings.ContainsAny(fromName, "\r\n") || len(fromName) > 64 {
		return apitypes.EmailSettings{}, invalid("the From name must be one line of at most 64 characters")
	}
	var replyTo string
	if r := strings.TrimSpace(req.ReplyTo); r != "" {
		addr, err := mail.ParseAddress(r)
		if err != nil {
			return apitypes.EmailSettings{}, invalid("the Reply-To address %q isn't an email address", req.ReplyTo)
		}
		replyTo = addr.Address
	}
	username := strings.TrimSpace(req.Username)
	if strings.ContainsAny(username, "\r\n") {
		return apitypes.EmailSettings{}, invalid("the username must be one line")
	}
	// nil keeps the stored password, empty removes it.
	var password []byte
	switch {
	case username == "" || (req.Password != nil && *req.Password == ""):
		password = []byte{}
	case req.Password != nil:
		if password, err = c.box.Seal([]byte(*req.Password), emailPasswordAAD); err != nil {
			return apitypes.EmailSettings{}, err
		}
	}
	n := now()
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.SetEmailSettings(ctx, store.SetEmailSettingsParams{
			Host: host, Port: int64(port), Security: string(sec), Username: username, Password: password,
			FromAddr: from.Address, FromName: fromName, ReplyTo: replyTo, UpdatedBy: a.Name, UpdatedAt: n,
		}); err != nil {
			return err
		}
		return q.RetryEmailNow(ctx, store.RetryEmailNowParams{NextAt: n.Unix(), Now: n.Unix()})
	})
	if err != nil {
		return apitypes.EmailSettings{}, err
	}
	c.audit(ctx, a.platform(), "platform.email_set", nil, nil, "email", fmt.Sprintf("set Dock's email to send through %s (%s) from %s", net.JoinHostPort(host, fmt.Sprint(port)), sec, from.Address))
	c.kickEmail()
	return c.emailSettings(ctx)
}

// RemoveEmail turns Dock's email off: the settings go, and mail still
// queued is given up on. Operators only, recently confirmed.
func (c *Core) RemoveEmail(ctx context.Context, a Actor) (apitypes.EmailSettings, error) {
	if err := a.requirePlatform(PlatformSettings, "turning email off"); err != nil {
		return apitypes.EmailSettings{}, err
	}
	if err := a.requireRecent("turning email off"); err != nil {
		return apitypes.EmailSettings{}, err
	}
	n, err := c.store.DeleteEmailSettings(ctx)
	if err != nil {
		return apitypes.EmailSettings{}, err
	}
	if n > 0 {
		c.audit(ctx, a.platform(), "platform.email_clear", nil, nil, "email", "turned Dock's email off")
		c.kickEmail()
	}
	return c.emailSettings(ctx)
}

// TestEmail sends a test mail now, to req.To or the operator's own
// address, and says how it went; the outcome is the server's status too.
// It skips the outbox's queue and its limits, but is listed with the
// rest. Operators only.
func (c *Core) TestEmail(ctx context.Context, a Actor, req apitypes.TestEmailRequest) (apitypes.EmailTest, error) {
	if err := a.requirePlatform(PlatformSettings, "sending a test email"); err != nil {
		return apitypes.EmailTest{}, err
	}
	to := cmp.Or(strings.TrimSpace(req.To), a.Email)
	if to == "" {
		return apitypes.EmailTest{}, invalid("say who to send the test to (`dock email test --to ADDRESS`)")
	}
	addr, err := mail.ParseAddress(to)
	if err != nil {
		return apitypes.EmailTest{}, invalid("%q isn't an email address", to)
	}
	st, err := c.store.GetEmailSettings(ctx)
	if store.IsNotFound(err) {
		return apitypes.EmailTest{}, invalid("email isn't set up; set a server first (`dock email set`, or the console's Email page)")
	}
	if err != nil {
		return apitypes.EmailTest{}, err
	}
	content, err := mail.TestMail(mail.Test{SentBy: cmp.Or(a.DisplayName, a.Name), Server: net.JoinHostPort(st.Host, strconv.FormatInt(st.Port, 10)), Site: c.siteName()})
	if err != nil {
		return apitypes.EmailTest{}, err
	}
	sendErr := c.sendNow(ctx, []mail.Address{{Address: addr.Address}}, content)
	out := apitypes.EmailTest{To: addr.Address, OK: sendErr == nil}
	status, summary := apitypes.EmailSent, "sent a test email to "+addr.Address
	if sendErr != nil {
		out.Error, status, summary = sendErr.Error(), apitypes.EmailFailed, fmt.Sprintf("sent a test email to %s; it failed: %s", addr.Address, oneLine(sendErr.Error()))
	}
	n := now().Unix()
	if row, err := c.store.AddEmail(ctx, store.AddEmailParams{
		Kind: string(apitypes.EmailTestKind), ToAddr: addr.Address, Subject: content.Subject, Nonce: newNonce(),
		Status: string(status), NextAt: n, Error: out.Error, CreatedAt: n, DoneAt: &n,
	}); err == nil {
		c.publishEmail(ctx, row)
	} else {
		c.log.Error("email: record a test", "err", err)
	}
	c.audit(ctx, a.platform(), "platform.email_test", nil, nil, addr.Address, summary)
	if out.Settings, err = c.emailSettings(ctx); err != nil {
		return out, err
	}
	return out, nil
}

// EmailLog is the latest mails, newest first. Operators only.
func (c *Core) EmailLog(ctx context.Context, a Actor, limit int) (apitypes.EmailLog, error) {
	if err := a.requirePlatform(PlatformView, "reading the mail Dock sent"); err != nil {
		return apitypes.EmailLog{}, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := c.store.ListEmail(ctx, int64(limit))
	if err != nil {
		return apitypes.EmailLog{}, err
	}
	out := apitypes.EmailLog{Deliveries: make([]apitypes.EmailDelivery, len(rows))}
	for i, r := range rows {
		out.Deliveries[i] = toDelivery(store.EmailOutbox{
			ID: r.ID, Kind: r.Kind, Ref: r.Ref, ToAddr: r.ToAddr, Subject: r.Subject, Status: r.Status,
			Attempts: r.Attempts, NextAt: r.NextAt, Error: r.Error, CreatedAt: r.CreatedAt, DoneAt: r.DoneAt,
		}, r.Org)
	}
	return out, nil
}

// siteName is Dock's public host, as mail's footer names it.
func (c *Core) siteName() string {
	return cmp.Or(c.publicHost(), "this server")
}

// emailServerOK and emailServerFailed record the latest attempt to reach
// the SMTP server.
func (c *Core) emailServerOK(ctx context.Context) {
	if err := c.store.SetEmailOK(ctx, ptr(now())); err != nil {
		c.log.Error("email: record the server's status", "err", err)
	}
}

func (c *Core) emailServerFailed(ctx context.Context, cause error) {
	if err := c.store.SetEmailError(ctx, store.SetEmailErrorParams{LastError: oneLine(cause.Error()), LastErrorAt: ptr(now())}); err != nil {
		c.log.Error("email: record the server's status", "err", err)
	}
}

// errEmailRefused is why the outbox took no mail: it is full, or the
// address got too many.
type errEmailRefused struct{ why string }

func (e errEmailRefused) Error() string { return e.why }

// isRefused reports whether err is the outbox refusing a mail.
func isRefused(err error) bool {
	_, ok := errors.AsType[errEmailRefused](err)
	return ok
}
