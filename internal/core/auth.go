package core

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Secret prefixes make leaked credentials easy to recognise and scan for.
const (
	tokenPrefix   = "dock_"
	sessionPrefix = "ps_"
)

// SessionTTL is how long a UI login lasts.
const SessionTTL = 30 * 24 * time.Hour

// tokenTouchInterval throttles last_used_at writes.
const tokenTouchInterval = time.Minute

// dummyHash keeps failed logins for unknown emails as slow as wrong
// passwords. Lazy: hashing at init would slow every CLI start by ~40ms.
var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("platform-dummy-password"), bcrypt.DefaultCost)
	return h
})

func newSecret(prefix string) (secret, hash string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	secret = prefix + base64.RawURLEncoding.EncodeToString(b)
	return secret, hashSecret(secret)
}

func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

var errUnauthenticated = errf(apitypes.CodeUnauthorized, "invalid, expired or revoked credentials; log in again or use a valid token")

// AuthenticateToken resolves an API token to an actor in org (a slug; ""
// for the token's own org, or a `dock login` user's only one).
func (c *Core) AuthenticateToken(ctx context.Context, secret, org string) (Actor, error) {
	if !strings.HasPrefix(secret, tokenPrefix) {
		return Actor{}, errUnauthenticated
	}
	row, err := c.store.GetTokenByHash(ctx, hashSecret(secret))
	if store.IsNotFound(err) {
		return Actor{}, errUnauthenticated
	}
	if err != nil {
		return Actor{}, err
	}
	a, err := c.tokenActor(ctx, store.GetTokenByIDWithOwnerRow(row), org)
	if err != nil {
		return a, err
	}
	if t, n := row.Token, now(); t.LastUsedAt == nil || n.Sub(*t.LastUsedAt) > tokenTouchInterval {
		if err := c.store.TouchToken(ctx, store.TouchTokenParams{LastUsedAt: &n, ID: t.ID}); err != nil {
			c.log.Warn("touch token", "err", err)
		}
	}
	return a, nil
}

// tokenActor is a token as an actor in org (see AuthenticateToken), as it
// is now: revoked, expired, or for a `dock login` token its user gone or
// disabled, it is unauthenticated. An approved access request acts as
// the token that asked through it.
func (c *Core) tokenActor(ctx context.Context, row store.GetTokenByIDWithOwnerRow, org string) (Actor, error) {
	t, n := row.Token, now()
	if t.RevokedAt != nil || (t.ExpiresAt != nil && n.After(*t.ExpiresAt)) {
		return Actor{}, errUnauthenticated
	}
	a := Actor{Kind: "token", Name: t.Name, TokenID: t.ID, Scope: apitypes.Scope(t.Scope), Operator: t.Operator}
	// A `dock login` token acts for the user who approved it, so it is only
	// as good as that user now: gone or disabled refuses it, and in each
	// org their role caps it. Acting for them lets them change their
	// password from the CLI.
	if t.Origin == "cli_login" {
		if row.UserEmail == nil || row.UserOperator == nil || row.UserDisabledAt != nil || t.UserID == nil {
			return Actor{}, errUnauthenticated
		}
		a.Email, a.UserID, a.Operator = *row.UserEmail, *t.UserID, *row.UserOperator
		if t.TwoFactor {
			a.Factor = factorLogin
		}
		return c.actIn(ctx, c.gateOperator(a), org, apitypes.Scope(t.Scope))
	}
	if t.OrgID == nil || row.OrgSlug == nil { // manual tokens always have one
		return Actor{}, errUnauthenticated
	}
	if org != "" && org != *row.OrgSlug {
		return Actor{}, errf(apitypes.CodeForbidden, "token %q belongs to organization %s; it can't act in %s", t.Name, *row.OrgSlug, org)
	}
	a.OrgID, a.OrgSlug, a.orgs = *t.OrgID, *row.OrgSlug, 1
	if t.ProjectID != nil {
		p, err := c.store.GetProject(ctx, *t.ProjectID)
		if err != nil {
			return Actor{}, err
		}
		a.ProjectID, a.ProjectName = p.ID, p.Name
	}
	if t.EnvironmentID != nil {
		e, err := c.store.GetEnvironment(ctx, *t.EnvironmentID)
		if err != nil {
			return Actor{}, err
		}
		a.EnvironmentID, a.EnvironmentName = e.ID, e.Name
	}
	return a, nil
}

// AuthenticateSession resolves a UI session cookie to an actor in org (a
// slug; "" for the user's only org).
func (c *Core) AuthenticateSession(ctx context.Context, secret, org string) (Actor, error) {
	if !strings.HasPrefix(secret, sessionPrefix) {
		return Actor{}, errUnauthenticated
	}
	row, err := c.store.GetSessionUser(ctx, hashSecret(secret))
	if store.IsNotFound(err) {
		return Actor{}, errUnauthenticated
	}
	if err != nil {
		return Actor{}, err
	}
	s := row.Session
	if s.Pending || now().After(s.ExpiresAt) || row.User.DisabledAt != nil {
		return Actor{}, errUnauthenticated
	}
	a := userActor(row.User)
	a.SessionID, a.Factor = s.ID, s.Factor
	if s.VerifiedAt != nil {
		a.VerifiedAt = *s.VerifiedAt
	}
	return c.actIn(ctx, c.gateOperator(a), org, apitypes.ScopeAdmin)
}

// userActor is a user in no org yet; actIn picks one.
func userActor(u store.User) Actor {
	return Actor{Kind: "user", Name: u.Email, Email: u.Email, UserID: u.ID, Operator: u.Operator}
}

// actIn puts a user's actor (a session or a `dock login` token) in the org
// named by slug, or with none in the user's only org; a user with several
// and no slug gets no org, and org-scoped operations say to choose one.
// The actor's scope is the user's role there, capped at most. An org that
// requires a second factor the actor didn't use leaves it in none, and
// org-scoped operations say so (ADR 0050).
func (c *Core) actIn(ctx context.Context, a Actor, slug string, most apitypes.Scope) (Actor, error) {
	if slug != "" {
		m, err := c.store.GetMembership(ctx, store.GetMembershipParams{UserID: a.UserID, Slug: slug})
		if store.IsNotFound(err) {
			return Actor{}, notFound("organization %q not found among yours; see `dock org list`", slug)
		}
		if err != nil {
			return Actor{}, err
		}
		return a.gatedMember(m.Org, m.Role, most), nil
	}
	orgs, err := c.store.ListUserOrgs(ctx, a.UserID)
	if err != nil {
		return Actor{}, err
	}
	if len(orgs) == 1 {
		return a.gatedMember(orgs[0].Org, orgs[0].Role, most), nil
	}
	a.orgs = len(orgs)
	return a, nil
}

// gatedMember is member, unless org requires a second factor the actor
// didn't use: then it acts in no org and says why.
func (a Actor) gatedMember(org store.Org, role string, most apitypes.Scope) Actor {
	if org.RequireTwoFactor && a.Factor == "" {
		a.needs2FAOrg, a.orgs = org.Slug, 1
		return a
	}
	return a.member(org, role, most)
}

// member is the actor acting in org with role, capped at most.
func (a Actor) member(org store.Org, role string, most apitypes.Scope) Actor {
	a.OrgID, a.OrgSlug, a.orgs = org.ID, org.Slug, 1
	a.Scope = apitypes.Scope(role)
	if rank(most) < rank(a.Scope) {
		a.Scope = most
	}
	return a
}

var errWrongPassword = errf(apitypes.CodeUnauthorized, "wrong email or password")

// Login checks a user's password and starts a session, or, when they
// have a second factor, a pending sign-in for LoginSecondFactor to finish.
// Attempts are rate limited per client IP and email
// (httpkit.LoginLimiter), so a guesser can't lock the owner out; failures
// are logged and audited (never the password).
func (c *Core) Login(ctx context.Context, email, password, clientIP string) (LoginResult, error) {
	t := now()
	if !c.loginLimit.Allow(email, clientIP, t) {
		c.log.Warn("login rate limited", "email", email, "ip", clientIP)
		return LoginResult{}, errf(apitypes.CodeRateLimited, "too many login attempts; wait a minute and try again")
	}
	u, err := c.checkPassword(ctx, email, password)
	if err == nil && u.DisabledAt != nil {
		err = errf(apitypes.CodeUnauthorized, "this account is disabled")
	}
	if CodeOf(err) == apitypes.CodeUnauthorized {
		c.log.Warn("login failed", "email", email, "ip", clientIP, "reason", err.Error())
		c.audit(ctx, Actor{Kind: "user", Name: email}, "user.login_failed", nil, nil, email, fmt.Sprintf("failed login from %s: %v", clientIP, err))
	}
	if err != nil {
		return LoginResult{}, err
	}
	n, err := c.store.CountUserFactors(ctx, u.ID)
	if err != nil {
		return LoginResult{}, err
	}
	if n > 0 {
		return c.startSecondFactor(ctx, u)
	}
	return c.signIn(ctx, u, "", clientIP)
}

// checkPassword returns the user if password is theirs. At most a few
// checks run at once, and unknown emails cost as much as wrong passwords.
func (c *Core) checkPassword(ctx context.Context, email, password string) (store.User, error) {
	select {
	case c.bcryptSem <- struct{}{}:
	case <-ctx.Done():
		return store.User{}, ctx.Err()
	}
	defer func() { <-c.bcryptSem }()
	u, err := c.store.GetUserByEmail(ctx, email)
	if store.IsNotFound(err) {
		_ = bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
		return store.User{}, errWrongPassword
	}
	if err != nil {
		return store.User{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return store.User{}, errWrongPassword
	}
	return u, nil
}

// newSession starts a UI session for u, whose sign-in used factor ("" for
// a password alone), verified now. It returns the session's secret and
// the actor it authenticates, in no org yet.
func (c *Core) newSession(ctx context.Context, u store.User, factor string) (secret string, expires time.Time, a Actor, err error) {
	secret, hash := newSecret(sessionPrefix)
	t := now()
	expires = t.Add(SessionTTL)
	id, err := c.store.CreateSession(ctx, store.CreateSessionParams{UserID: u.ID, Hash: hash, ExpiresAt: expires, Factor: factor, VerifiedAt: &t})
	if err != nil {
		return "", time.Time{}, Actor{}, err
	}
	_ = c.store.DeleteExpiredSessions(ctx, now())
	a = userActor(u)
	a.SessionID, a.Factor, a.VerifiedAt = id, factor, t
	return secret, expires, c.gateOperator(a), nil
}

// ChangePassword changes the calling user's own password, from a UI
// session or a `dock login` token. Every session and `dock login` token of
// theirs ends, except the caller's: a session caller gets the returned
// session in its place, a token caller keeps its token (it just proved the
// password).
func (c *Core) ChangePassword(ctx context.Context, a Actor, req apitypes.ChangePasswordRequest, clientIP string) (secret string, expires time.Time, err error) {
	if a.Email == "" {
		return "", time.Time{}, invalid("only users have passwords; API tokens can't change them. Log in with `dock login` to change yours from the CLI")
	}
	if !c.loginLimit.Allow(a.Email, clientIP, now()) {
		return "", time.Time{}, errf(apitypes.CodeRateLimited, "too many password attempts; wait a minute and try again")
	}
	if len(req.NewPassword) < 12 {
		return "", time.Time{}, invalid("password must be at least 12 characters")
	}
	u, err := c.checkPassword(ctx, a.Email, req.CurrentPassword)
	if CodeOf(err) == apitypes.CodeUnauthorized {
		c.audit(ctx, a.platform(), "user.password_failed", nil, nil, a.Email, "wrong current password from "+clientIP)
		return "", time.Time{}, invalid("current password is wrong")
	}
	if err != nil {
		return "", time.Time{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return "", time.Time{}, err
	}
	var revoked int64
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.SetUserPassword(ctx, store.SetUserPasswordParams{PasswordHash: string(hash), ID: u.ID}); err != nil {
			return err
		}
		revoked, err = endUserAccess(ctx, q, u.ID, a.TokenID)
		return err
	})
	if err != nil {
		return "", time.Time{}, err
	}
	if a.Kind == "user" {
		if secret, expires, _, err = c.newSession(ctx, u, a.Factor); err != nil {
			return "", time.Time{}, err
		}
	}
	c.audit(ctx, a.platform(), "user.password", nil, nil, u.Email, "changed own password; other sessions ended"+revokedNote(revoked))
	return secret, expires, nil
}

// SetUserDisabled disables a user, ending their sessions and revoking
// their `dock login` tokens, or re-enables them. Tokens they created by
// hand are separate credentials and keep working until revoked.
func (c *Core) SetUserDisabled(ctx context.Context, a Actor, id int64, disabled bool) (apitypes.User, error) {
	var at *time.Time
	verb, summary := "enable", "enabled user %s"
	if disabled {
		at, verb, summary = ptr(now()), "disable", "disabled user %s and ended their sessions"
	}
	u, err := c.otherUser(ctx, a, id, verb)
	if err != nil {
		return apitypes.User{}, err
	}
	var revoked int64
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.SetUserDisabled(ctx, store.SetUserDisabledParams{DisabledAt: at, ID: u.ID}); err != nil || !disabled {
			return err
		}
		revoked, err = endUserAccess(ctx, q, u.ID, 0)
		return err
	})
	if err != nil {
		return apitypes.User{}, err
	}
	u.DisabledAt = at
	c.audit(ctx, a.platform(), "user."+verb, nil, nil, u.Email, fmt.Sprintf(summary, u.Email)+revokedNote(revoked))
	return c.userIn(ctx, a, u), nil
}

// DeleteUser deletes a user and their sessions, and revokes their `dock
// login` tokens. Their audit history and the tokens they created by hand
// stay.
func (c *Core) DeleteUser(ctx context.Context, a Actor, id int64) error {
	u, err := c.otherUser(ctx, a, id, "delete")
	if err != nil {
		return err
	}
	var revoked int64
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		if revoked, err = endUserAccess(ctx, q, u.ID, 0); err != nil {
			return err
		}
		return q.DeleteUser(ctx, u.ID)
	})
	if err != nil {
		return err
	}
	c.audit(ctx, a.platform(), "user.delete", nil, nil, u.Email, "deleted user "+u.Email+revokedNote(revoked))
	return nil
}

// otherUser loads a user an operator manages; nobody can lock themselves
// out.
func (c *Core) otherUser(ctx context.Context, a Actor, id int64, verb string) (store.User, error) {
	if err := a.requireOperator("managing user accounts"); err != nil {
		return store.User{}, err
	}
	if err := a.requireRecent("managing user accounts"); err != nil {
		return store.User{}, err
	}
	if a.UserID == id {
		return store.User{}, invalid("you can't %s your own account", verb)
	}
	u, err := c.store.GetUser(ctx, id)
	if store.IsNotFound(err) {
		return u, notFound("user %d not found", id)
	}
	return u, err
}

// endUserAccess ends a user's sessions and revokes their `dock login`
// tokens except token keep (0: none), in transaction q. It returns how many
// tokens it revoked.
func endUserAccess(ctx context.Context, q *store.Queries, userID, keep int64) (int64, error) {
	if err := q.DeleteUserSessions(ctx, userID); err != nil {
		return 0, err
	}
	return q.RevokeUserLoginTokens(ctx, store.RevokeUserLoginTokensParams{Now: ptr(now()), UserID: &userID, Keep: keep})
}

// revokedNote tells the audit log how many `dock login` tokens a user
// change revoked.
func revokedNote(n int64) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "; revoked their dock login token"
	}
	return fmt.Sprintf("; revoked their %d dock login tokens", n)
}

// Logout ends a session. Unknown sessions are ignored.
func (c *Core) Logout(ctx context.Context, secret string) error {
	return c.store.DeleteSession(ctx, hashSecret(secret))
}

// ListTokens returns the org's tokens and its members' `dock login`
// tokens (never their secrets).
func (c *Core) ListTokens(ctx context.Context, a Actor) ([]apitypes.Token, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "managing tokens"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListOrgTokens(ctx, &a.OrgID)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(rows, func(x, y store.Token) int { return cmp.Compare(x.ID, y.ID) })
	names := newNames(c.store)
	out := make([]apitypes.Token, 0, len(rows))
	for _, t := range rows {
		out = append(out, toToken(ctx, names, t))
	}
	return out, nil
}

// CreateToken creates a token and returns its secret, shown only once.
func (c *Core) CreateToken(ctx context.Context, a Actor, req apitypes.CreateTokenRequest) (apitypes.CreatedToken, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "managing tokens"); err != nil {
		return apitypes.CreatedToken{}, err
	}
	if err := a.requireRecent("creating a token"); err != nil {
		return apitypes.CreatedToken{}, err
	}
	return c.createToken(ctx, a, req)
}

func (c *Core) createToken(ctx context.Context, a Actor, req apitypes.CreateTokenRequest) (apitypes.CreatedToken, error) {
	if strings.TrimSpace(req.Name) == "" {
		return apitypes.CreatedToken{}, invalid("token name is required")
	}
	if !validScope(req.Scope) {
		return apitypes.CreatedToken{}, invalid("scope must be read, deploy or admin")
	}
	dataAccess, err := tokenDataParams(0, req.Data)
	if err != nil {
		return apitypes.CreatedToken{}, err
	}
	if req.Operator {
		if err := a.requireOperator("creating an operator token"); err != nil {
			return apitypes.CreatedToken{}, err
		}
		if req.Scope != apitypes.ScopeAdmin || req.Project != "" {
			return apitypes.CreatedToken{}, invalid("an operator token has scope admin and no project or environment limit")
		}
	}
	params := store.CreateTokenParams{Name: req.Name, Scope: string(req.Scope), OrgID: &a.OrgID, Operator: req.Operator, CreatedBy: a.Name}
	if a.UserID != 0 { // the creator lists and revokes it on their account
		params.UserID = &a.UserID
	}
	var p *store.Project
	var e *store.Environment
	switch {
	case req.Environment != "" && req.Project == "":
		return apitypes.CreatedToken{}, invalid("environment limit needs a project too")
	case req.Environment != "":
		es, err := c.loadEnv(ctx, a.OrgID, EnvRef{req.Project, req.Environment})
		if err != nil {
			return apitypes.CreatedToken{}, err
		}
		p, e = &es.project, &es.env
		params.ProjectID, params.EnvironmentID = &es.project.ID, &es.env.ID
	case req.Project != "":
		proj, err := c.loadProject(ctx, a.OrgID, req.Project)
		if err != nil {
			return apitypes.CreatedToken{}, err
		}
		p = &proj
		params.ProjectID = &proj.ID
	}
	if req.ExpiresInDays > 0 {
		exp := now().Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)
		params.ExpiresAt = &exp
	}
	secret, hash := newSecret(tokenPrefix)
	params.Hash = hash
	var t store.Token
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		if t, err = q.CreateToken(ctx, params); err != nil || req.Data == nil {
			return err
		}
		dataAccess.ID = t.ID
		t.DataProduction, t.DataNonProduction, t.DataSensitive = dataAccess.DataProduction, dataAccess.DataNonProduction, dataAccess.DataSensitive
		_, err = q.SetTokenDataAccess(ctx, dataAccess)
		return err
	})
	if err != nil {
		return apitypes.CreatedToken{}, err
	}
	out := toToken(ctx, newNames(c.store), t)
	summary := fmt.Sprintf("created %s token %q (id %d)", t.Scope, t.Name, t.ID)
	if req.Data != nil {
		summary += "; " + datapanel.DescribeTokenAccess(req.Data)
	}
	c.audit(ctx, a, "token.create", p, e, "token "+t.Name, summary)
	return apitypes.CreatedToken{Token: out, Secret: secret}, nil
}

// ListOwnTokens returns the caller's own tokens: their `dock login` tokens
// and those they created in the org. Any user may list theirs.
func (c *Core) ListOwnTokens(ctx context.Context, a Actor) ([]apitypes.Token, error) {
	if a.UserID == 0 {
		return nil, errf(apitypes.CodeForbidden, "%s is a token; only users have tokens of their own", a)
	}
	rows, err := c.store.ListUserTokens(ctx, store.ListUserTokensParams{UserID: &a.UserID, OrgID: &a.OrgID})
	if err != nil {
		return nil, err
	}
	names := newNames(c.store)
	out := make([]apitypes.Token, 0, len(rows))
	for _, t := range rows {
		out = append(out, toToken(ctx, names, t))
	}
	return out, nil
}

// RevokeToken revokes a token by ID: an admin any of the org's tokens or a
// member's `dock login` token, anyone else their own (see ListOwnTokens).
func (c *Core) RevokeToken(ctx context.Context, a Actor, id int64) error {
	t, err := c.revocableToken(ctx, a, id)
	if err != nil {
		return err
	}
	n := now()
	if err := c.store.RevokeToken(ctx, store.RevokeTokenParams{RevokedAt: &n, ID: id}); err != nil {
		return err
	}
	c.audit(ctx, a, "token.revoke", nil, nil, "token "+t.Name, fmt.Sprintf("revoked token %q (id %d)", t.Name, t.ID))
	return nil
}

// revocableToken loads a token the actor may revoke: as an unlimited
// admin through orgToken, else one of their own.
func (c *Core) revocableToken(ctx context.Context, a Actor, id int64) (store.Token, error) {
	admin := a.requireUnlimited(apitypes.ScopeAdmin, "managing tokens")
	if admin == nil {
		return c.orgToken(ctx, a, id)
	}
	if a.UserID == 0 {
		return store.Token{}, admin
	}
	t, err := c.store.GetToken(ctx, id)
	if store.IsNotFound(err) || err == nil && !ownToken(a, t) {
		return t, admin
	}
	return t, err
}

// ownToken reports whether t is one of a's own tokens, as ListUserTokens
// selects them.
func ownToken(a Actor, t store.Token) bool {
	if t.UserID == nil || *t.UserID != a.UserID {
		return false
	}
	return t.Origin == "cli_login" || t.OrgID != nil && *t.OrgID == a.OrgID
}

// orgToken loads a token the actor's org manages: one of its own, or a
// member's `dock login` token. Others are not found.
func (c *Core) orgToken(ctx context.Context, a Actor, id int64) (store.Token, error) {
	t, err := c.store.GetToken(ctx, id)
	if err == nil && (t.OrgID == nil || *t.OrgID != a.OrgID) {
		err = sql.ErrNoRows
		if t.Origin == "cli_login" && t.UserID != nil {
			_, err = c.store.GetMemberRole(ctx, store.GetMemberRoleParams{OrgID: a.OrgID, UserID: *t.UserID})
		}
	}
	if store.IsNotFound(err) {
		return t, notFound("token %d not found", id)
	}
	return t, err
}

// ListUsers returns the org's members with their role, live sessions and
// usable tokens.
func (c *Core) ListUsers(ctx context.Context, a Actor) ([]apitypes.User, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "managing users"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListOrgMembers(ctx, store.ListOrgMembersParams{Now: now(), OrgID: a.OrgID})
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.User, 0, len(rows))
	for _, r := range rows {
		u := toUser(r.User, r.Role)
		u.Sessions, u.Tokens, u.TwoFactor = int(r.Sessions), int(r.Tokens), r.TwoFactor
		out = append(out, u)
	}
	return out, nil
}

// ResetPassword sets another user's password, generating one when the
// request has none, ends their sessions and revokes their `dock login`
// tokens. Admins change their own
// password with ChangePassword, which asks for the current one.
func (c *Core) ResetPassword(ctx context.Context, a Actor, id int64, req apitypes.ResetPasswordRequest) (apitypes.ResetPasswordResult, error) {
	u, err := c.otherUser(ctx, a, id, "reset the password of")
	if err != nil {
		return apitypes.ResetPasswordResult{}, err
	}
	var out apitypes.ResetPasswordResult
	pw := req.Password
	if pw == "" {
		pw = generatePassword()
		out.Password = pw
	}
	if len(pw) < 12 {
		return out, invalid("password must be at least 12 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return out, err
	}
	var revoked int64
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.SetUserPassword(ctx, store.SetUserPasswordParams{PasswordHash: string(hash), ID: u.ID}); err != nil {
			return err
		}
		revoked, err = endUserAccess(ctx, q, u.ID, 0)
		return err
	})
	if err != nil {
		return out, err
	}
	c.audit(ctx, a.platform(), "user.reset_password", nil, nil, u.Email, "reset the password of "+u.Email+" and ended their sessions"+revokedNote(revoked))
	out.User = c.userIn(ctx, a, u)
	return out, nil
}

// generatePassword returns a random 24-character password.
func generatePassword() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// CreateUser creates a user account and adds it to the actor's org.
// Accounts are the platform's, so only operators create them.
func (c *Core) CreateUser(ctx context.Context, a Actor, req apitypes.CreateUserRequest) (apitypes.User, error) {
	if err := a.requireOperator("creating user accounts"); err != nil {
		return apitypes.User{}, err
	}
	if err := a.requireRecent("creating user accounts"); err != nil {
		return apitypes.User{}, err
	}
	if a.OrgID == 0 {
		return apitypes.User{}, a.noOrg("creating a user")
	}
	u, err := c.createUser(ctx, req, a.OrgID)
	if err != nil {
		return apitypes.User{}, err
	}
	kind := "user"
	if u.Operator {
		kind = "operator"
	}
	c.audit(ctx, a.platform(), "user.create", nil, nil, u.Email, fmt.Sprintf("created %s %s with role %s in %s", kind, u.Email, req.Role, a.OrgSlug))
	return toUser(u, string(req.Role)), nil
}

// createUser creates a user and makes them a member of org with req.Role.
func (c *Core) createUser(ctx context.Context, req apitypes.CreateUserRequest, orgID int64) (store.User, error) {
	if !strings.Contains(req.Email, "@") {
		return store.User{}, invalid("email %q is not valid", req.Email)
	}
	if len(req.Password) < 12 {
		return store.User{}, invalid("password must be at least 12 characters")
	}
	if !validScope(req.Role) {
		return store.User{}, invalid("role must be read, deploy or admin")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return store.User{}, err
	}
	var u store.User
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		if u, err = q.CreateUser(ctx, store.CreateUserParams{Email: req.Email, Name: req.Name, PasswordHash: string(hash), Operator: req.Operator}); err != nil {
			return err
		}
		return q.AddOrgMember(ctx, store.AddOrgMemberParams{OrgID: orgID, UserID: u.ID, Role: string(req.Role)})
	})
	if store.IsUnique(err) {
		return store.User{}, conflict("a user with email %s already exists", req.Email)
	}
	return u, err
}

// Bootstrapped is the first admin's credentials, shown once.
type Bootstrapped struct {
	Email    string
	Password string
	Token    string
}

// Bootstrap creates the first user, an operator and the admin of the
// install's first org, "default" (created now, or the one an older
// install's data was moved into), and an operator token. It returns
// ok=false (and no error) when already bootstrapped.
func (c *Core) Bootstrap(ctx context.Context, email string) (b Bootstrapped, ok bool, err error) {
	n, err := c.store.CountUsers(ctx)
	if err != nil || n > 0 {
		return b, false, err
	}
	org, err := c.store.GetOrg(ctx, 1)
	if store.IsNotFound(err) {
		org, err = createOrgTx(ctx, c.store.Queries, "default", "Default")
	}
	if err != nil {
		return b, false, err
	}
	b = Bootstrapped{Email: email, Password: generatePassword()}
	u, err := c.createUser(ctx, apitypes.CreateUserRequest{Email: email, Name: "admin", Password: b.Password, Role: apitypes.ScopeAdmin, Operator: true}, org.ID)
	if err != nil {
		return b, false, err
	}
	a := userActor(u).member(org, string(apitypes.ScopeAdmin), apitypes.ScopeAdmin)
	created, err := c.createToken(ctx, a, apitypes.CreateTokenRequest{Name: "bootstrap", Scope: apitypes.ScopeAdmin, Operator: true})
	if err != nil {
		return b, false, err
	}
	b.Token = created.Secret
	c.audit(ctx, System.InOrg(org.ID), "user.bootstrap", nil, nil, u.Email, "created the first user, an operator and admin of "+org.Slug+", and an operator token")
	return b, true, nil
}

// userIn converts a user for the API with their role in the actor's org
// ("" when they aren't a member).
func (c *Core) userIn(ctx context.Context, a Actor, u store.User) apitypes.User {
	role, _ := c.store.GetMemberRole(ctx, store.GetMemberRoleParams{OrgID: a.OrgID, UserID: u.ID})
	return toUser(u, role)
}

func toUser(u store.User, role string) apitypes.User {
	return apitypes.User{ID: u.ID, Email: u.Email, Name: u.Name, Role: apitypes.Scope(role), Operator: u.Operator, CreatedAt: u.CreatedAt, DisabledAt: u.DisabledAt}
}

func toToken(ctx context.Context, names *names, t store.Token) apitypes.Token {
	out := apitypes.Token{
		ID: t.ID, Name: t.Name, Scope: apitypes.Scope(t.Scope), CreatedBy: t.CreatedBy, CreatedAt: t.CreatedAt,
		ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt, RevokedAt: t.RevokedAt, Origin: t.Origin,
		Operator: t.Operator, Data: tokenData(t),
	}
	if t.ProjectID != nil {
		out.Project = names.project(ctx, *t.ProjectID)
	}
	if t.EnvironmentID != nil {
		out.Environment = names.environment(ctx, *t.EnvironmentID)
	}
	return out
}

// names caches project and environment names by ID for list conversions.
type names struct {
	q        *store.Store
	projects map[int64]string
	envs     map[int64]string
}

func newNames(q *store.Store) *names {
	return &names{q: q, projects: map[int64]string{}, envs: map[int64]string{}}
}

func (n *names) project(ctx context.Context, id int64) string {
	if s, ok := n.projects[id]; ok {
		return s
	}
	p, err := n.q.GetProject(ctx, id)
	if err == nil {
		n.projects[id] = p.Name
	}
	return p.Name
}

func (n *names) environment(ctx context.Context, id int64) string {
	if s, ok := n.envs[id]; ok {
		return s
	}
	e, err := n.q.GetEnvironment(ctx, id)
	if err == nil {
		n.envs[id] = e.Name
	}
	return e.Name
}
