package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// SignUpWithInvite creates the account an open invite names, accepts the
// invite and signs the new user in, for someone without an account who
// opened the link (ADR 0060). The email is the invite's; the person gives
// a name and a password (checkNewPassword). The answer is a session as
// after a password login: an org that requires a second factor then asks
// for one to be added before acting in it. Refused when the email has an
// account already (sign in and accept instead) and when the org requires
// its single sign-on (sign in through it: the open invite is accepted
// then). Attempts are rate limited like logins: per client IP, and per
// IP and email once the code is known.
func (c *Core) SignUpWithInvite(ctx context.Context, code string, req apitypes.InviteSignUpRequest, clientIP string) (LoginResult, error) {
	t := now()
	row, err := c.openInvite(ctx, code)
	var allowed bool
	if err != nil {
		allowed = c.loginLimit.AllowIP(clientIP, t)
	} else {
		allowed = c.loginLimit.Allow(row.OrgInvite.Email, clientIP, t)
	}
	if !allowed {
		c.log.Warn("invite sign-up rate limited", "ip", clientIP)
		return LoginResult{}, errf(apitypes.CodeRateLimited, "too many attempts; wait a minute and try again")
	}
	if err != nil {
		return LoginResult{}, err
	}
	inv, org := row.OrgInvite, row.Org
	if org.SsoOnly {
		return LoginResult{}, errf(apitypes.CodeForbidden, "%s signs in only through its single sign-on, so Dock makes no password account for it; sign in with SSO as %s and the invite is accepted then", org.Name, inv.Email)
	}
	name, err := displayName(req.Name)
	if err != nil {
		return LoginResult{}, err
	}
	if err := checkNewPassword(req.Password); err != nil {
		return LoginResult{}, err
	}
	errExists := conflict("an account with %s exists already; sign in with it to accept the invite", inv.Email)
	if _, err := c.store.GetUserByEmail(ctx, inv.Email); err == nil {
		return LoginResult{}, errExists
	} else if !store.IsNotFound(err) {
		return LoginResult{}, err
	}
	hash, err := c.hashPassword(ctx, req.Password)
	if err != nil {
		return LoginResult{}, err
	}
	var u store.User
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		if u, err = q.CreateUser(ctx, store.CreateUserParams{Email: inv.Email, Name: name, PasswordHash: hash}); err != nil {
			return err
		}
		return joinTx(ctx, q, org, &inv, u.ID, inv.Role)
	})
	if store.IsUnique(err) {
		return LoginResult{}, errExists
	}
	if err != nil {
		return LoginResult{}, err
	}
	c.audit(ctx, userActor(u), "user.create", nil, nil, u.Email, fmt.Sprintf("created user %s from an invite to %s, from %s", u.Email, org.Slug, clientIP))
	in := userActor(u).member(org, inv.Role, apitypes.ScopeAdmin)
	c.audit(ctx, in, "org.join", nil, nil, u.Email, fmt.Sprintf("%s joined as %s, creating their account from the invite", u.Email, inv.Role))
	return c.signIn(ctx, u, signInVia{}, clientIP)
}

// checkNewPassword is the rule for every password a person sets: at
// least 12 characters, and at most bcrypt's 72 bytes.
func checkNewPassword(p string) error {
	switch {
	case len(p) < 12:
		return invalid("password must be at least 12 characters")
	case len(p) > 72:
		return invalid("password must be at most 72 bytes; use a shorter one")
	}
	return nil
}

// hashPassword hashes a new password, sharing the bcrypt slots login
// attempts use so sign-ups can't starve them.
func (c *Core) hashPassword(ctx context.Context, password string) (string, error) {
	select {
	case c.bcryptSem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-c.bcryptSem }()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if errors.Is(err, bcrypt.ErrPasswordTooLong) {
		return "", invalid("password must be at most 72 bytes; use a shorter one")
	}
	return string(h), err
}

// maxDisplayName is the longest display name, in characters.
const maxDisplayName = 64

// displayName checks a person's display name: trimmed, 1 to 64
// characters, no control characters.
func displayName(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch n := utf8.RuneCountInString(s); {
	case n == 0:
		return "", invalid("enter a name")
	case n > maxDisplayName:
		return "", invalid("a name is at most %d characters; this one has %d", maxDisplayName, n)
	case strings.ContainsFunc(s, unicode.IsControl):
		return "", invalid("a name can't contain control characters like tabs or line breaks")
	}
	return s, nil
}
