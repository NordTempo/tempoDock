package core

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/mail"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Password reset by email (ADR 0079): "Forgot password?" on the sign-in
// page emails a link that sets a new password. The link's code is a
// bearer secret kept only as its SHA-256; it works once, for an hour, and
// a newer one replaces it. Setting the password ends every session and
// `dock login` token, keeps the account's second factors, and sends a
// security notice. It proves the account's email (ssoUser). Offered only while passwords sign in and email can
// carry a link (canEmailLinks).

// PasswordResetTTL is how long a reset link works.
const PasswordResetTTL = time.Hour

// resetPrefix starts a reset link's code.
const resetPrefix = "dpr_"

// RequestPasswordReset emails a reset link to the account with email,
// if there is one, enabled. The answer is the same whether or not there
// is, so the form can't be used to find accounts. Rate limited like
// logins.
func (c *Core) RequestPasswordReset(ctx context.Context, email, clientIP string) error {
	email = strings.TrimSpace(email)
	if !c.loginLimit.Allow(email, clientIP, now()) {
		return errf(apitypes.CodeRateLimited, "too many attempts; wait a minute and try again")
	}
	if err := c.resetsOffered(ctx); err != nil {
		return err
	}
	u, err := c.store.GetUserByEmail(ctx, email)
	if store.IsNotFound(err) || (err == nil && u.DisabledAt != nil) {
		return nil
	}
	if err != nil {
		return err
	}
	code, hash := newSecret(resetPrefix)
	t := now()
	content, err := mail.PasswordResetMail(mail.PasswordReset{
		Email: u.Email, URL: c.publicURL() + "/reset-password/" + code, Expires: t.Add(PasswordResetTTL), Site: c.siteName(),
	})
	if err != nil {
		return err
	}
	var row store.EmailOutbox
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteUnusedPasswordResets(ctx, u.ID); err != nil {
			return err
		}
		if err := q.CreatePasswordReset(ctx, store.CreatePasswordResetParams{UserID: u.ID, Hash: hash, CreatedAt: t, ExpiresAt: t.Add(PasswordResetTTL)}); err != nil {
			return err
		}
		row, err = c.queueEmail(ctx, q, outgoing{kind: apitypes.EmailPasswordReset, ref: userRef(u.ID), to: u.Email, content: content})
		return err
	})
	if isRefused(err) {
		// The same answer: the person waits out the limit.
		c.log.Warn("password reset: not emailed", "email", u.Email, "err", err)
		return nil
	}
	if err != nil {
		return err
	}
	c.emailQueued(ctx, row)
	c.audit(ctx, userActor(u), "user.password_reset_sent", nil, nil, u.Email, "emailed a password reset link, asked for from "+clientIP)
	return nil
}

// PasswordReset shows a reset link that still works: whose password it
// sets. Rate limited per client IP.
func (c *Core) PasswordReset(ctx context.Context, code, clientIP string) (apitypes.PasswordResetInfo, error) {
	if !c.loginLimit.AllowIP(clientIP, now()) {
		return apitypes.PasswordResetInfo{}, errf(apitypes.CodeRateLimited, "too many attempts; wait a minute and try again")
	}
	row, err := c.openReset(ctx, code)
	if err != nil {
		return apitypes.PasswordResetInfo{}, err
	}
	return apitypes.PasswordResetInfo{Email: row.User.Email, ExpiresAt: row.PasswordReset.ExpiresAt}, nil
}

// ResetPasswordWithLink sets a new password with a reset link's code,
// spending it, and ends every session and `dock login` token of the
// account. It doesn't sign in: the person signs in with the new
// password, and their second factor if they have one.
func (c *Core) ResetPasswordWithLink(ctx context.Context, code string, req apitypes.PasswordResetRequest, clientIP string) error {
	if !c.loginLimit.AllowIP(clientIP, now()) {
		return errf(apitypes.CodeRateLimited, "too many attempts; wait a minute and try again")
	}
	if err := checkNewPassword(req.Password); err != nil {
		return err
	}
	set, err := signInSettings(ctx, c.store.Queries)
	if err != nil {
		return err
	}
	if !set.PasswordSignIn {
		return errf(apitypes.CodeForbidden, "password sign-in is off on this Dock; continue with an identity provider or a passkey")
	}
	row, err := c.openReset(ctx, code)
	if err != nil {
		return err
	}
	hash, err := c.hashPassword(ctx, req.Password)
	if err != nil {
		return err
	}
	u := row.User
	var revoked int64
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if _, err := q.UsePasswordReset(ctx, store.UsePasswordResetParams{UsedAt: ptr(now()), ID: row.PasswordReset.ID}); store.IsNotFound(err) {
			return errResetGone
		} else if err != nil {
			return err
		}
		if err := q.SetUserPassword(ctx, store.SetUserPasswordParams{PasswordHash: hash, ID: u.ID}); err != nil {
			return err
		}
		if err := q.DeleteUnusedPasswordResets(ctx, u.ID); err != nil {
			return err
		}
		// The link reached the mailbox: its owner holds the account now.
		if _, err := q.ProveUserEmail(ctx, u.ID); err != nil {
			return err
		}
		revoked, err = endUserAccess(ctx, q, u.ID, 0)
		return err
	})
	if err != nil {
		return err
	}
	c.endOrphanedRequests(ctx)
	c.audit(ctx, userActor(u), "user.password_reset", nil, nil, u.Email, "set a new password with an emailed link from "+clientIP+"; sessions ended"+revokedNote(revoked))
	c.securityNotice(ctx, u, "Your Tempo Dock password was changed", "")
	return nil
}

// errResetGone is a reset link that is unknown, spent, replaced or
// expired, with what to do.
var errResetGone = notFound("this reset link doesn't work any more: it was used, an hour passed, or a newer one replaced it. Ask for a new one on the sign-in page")

// openReset loads a reset link that still works, for an enabled
// account.
func (c *Core) openReset(ctx context.Context, code string) (store.GetPasswordResetRow, error) {
	if !strings.HasPrefix(code, resetPrefix) {
		return store.GetPasswordResetRow{}, errResetGone
	}
	row, err := c.store.GetPasswordReset(ctx, hashSecret(code))
	if store.IsNotFound(err) {
		return row, errResetGone
	}
	if err != nil {
		return row, err
	}
	if row.PasswordReset.UsedAt != nil || !now().Before(row.PasswordReset.ExpiresAt) || row.User.DisabledAt != nil {
		return row, errResetGone
	}
	return row, nil
}

// resetsOffered says why the sign-in page can't email reset links now,
// or nil.
func (c *Core) resetsOffered(ctx context.Context) error {
	set, err := signInSettings(ctx, c.store.Queries)
	if err != nil {
		return err
	}
	if !set.PasswordSignIn {
		return errf(apitypes.CodeForbidden, "password sign-in is off on this Dock; continue with an identity provider or a passkey")
	}
	ok, err := c.canEmailLinks(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return errf(apitypes.CodeForbidden, "this Dock can't email reset links (no email server or public address); ask an operator to reset your password (`dock user reset-password`)")
	}
	return nil
}

// canEmailLinks reports whether Dock can send mail that carries a link:
// email is set up and Dock has a public address.
func (c *Core) canEmailLinks(ctx context.Context) (bool, error) {
	if c.publicURL() == "" {
		return false, nil
	}
	_, err := c.store.GetEmailSettings(ctx)
	if store.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// userRef is the outbox's ref of a mail about a user's account.
func userRef(id int64) string { return "user:" + strconv.FormatInt(id, 10) }

// prunePasswordResets deletes reset links a day past their expiry
// (retention).
func (c *Core) prunePasswordResets(ctx context.Context, n time.Time) (int64, error) {
	return c.store.DeleteOldPasswordResets(ctx, n.Add(-24*time.Hour))
}
