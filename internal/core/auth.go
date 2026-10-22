package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/NordTempo/tempoDock/internal/apitypes"
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

// AuthenticateToken resolves an API token to an actor.
func (c *Core) AuthenticateToken(ctx context.Context, secret string) (Actor, error) {
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
	t, n := row.Token, now()
	if t.RevokedAt != nil || (t.ExpiresAt != nil && n.After(*t.ExpiresAt)) {
		return Actor{}, errUnauthenticated
	}
	a := Actor{Kind: "token", Name: t.Name, TokenID: t.ID, Scope: apitypes.Scope(t.Scope)}
	// A `dock login` token acts for the user who approved it, so it is only
	// as good as that user now: gone or disabled refuses it, and a lowered
	// role lowers it. Acting for them lets them change their password from
	// the CLI.
	if t.Origin == "cli_login" {
		if row.UserEmail == nil || row.UserRole == nil || row.UserDisabledAt != nil {
			return Actor{}, errUnauthenticated
		}
		a.Email = *row.UserEmail
		if role := apitypes.Scope(*row.UserRole); rank(role) < rank(a.Scope) {
			a.Scope = role
		}
	}
	if t.LastUsedAt == nil || n.Sub(*t.LastUsedAt) > tokenTouchInterval {
		if err := c.store.TouchToken(ctx, store.TouchTokenParams{LastUsedAt: &n, ID: t.ID}); err != nil {
			c.log.Warn("touch token", "err", err)
		}
	}
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

// AuthenticateSession resolves a UI session cookie to an actor.
func (c *Core) AuthenticateSession(ctx context.Context, secret string) (Actor, error) {
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
	if now().After(row.SessionExpiresAt) || row.User.DisabledAt != nil {
		return Actor{}, errUnauthenticated
	}
	return userActor(row.User), nil
}

func userActor(u store.User) Actor {
	return Actor{Kind: "user", Name: u.Email, Email: u.Email, UserID: u.ID, Scope: apitypes.Scope(u.Role)}
}

var errWrongPassword = errf(apitypes.CodeUnauthorized, "wrong email or password")

// Login checks a user's password and starts a session. It returns the
// session secret for the cookie. Attempts are rate limited per client IP
// and per email; failures are logged and audited (never the password).
func (c *Core) Login(ctx context.Context, email, password, clientIP string) (secret string, expires time.Time, a Actor, err error) {
	t := now()
	if !c.loginLimit.allow("ip:"+clientIP, t) || !c.loginLimit.allow("email:"+strings.ToLower(email), t) {
		c.log.Warn("login rate limited", "email", email, "ip", clientIP)
		return "", time.Time{}, Actor{}, errf(apitypes.CodeRateLimited, "too many login attempts; wait a minute and try again")
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
		return "", time.Time{}, Actor{}, err
	}
	secret, expires, err = c.newSession(ctx, u)
	if err != nil {
		return "", time.Time{}, Actor{}, err
	}
	a = userActor(u)
	c.audit(ctx, a, "user.login", nil, nil, u.Email, "logged in from "+clientIP)
	return secret, expires, a, nil
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

func (c *Core) newSession(ctx context.Context, u store.User) (secret string, expires time.Time, err error) {
	secret, hash := newSecret(sessionPrefix)
	expires = now().Add(SessionTTL)
	if err := c.store.CreateSession(ctx, store.CreateSessionParams{UserID: u.ID, Hash: hash, ExpiresAt: expires}); err != nil {
		return "", time.Time{}, err
	}
	_ = c.store.DeleteExpiredSessions(ctx, now())
	return secret, expires, nil
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
	if !c.loginLimit.allow("email:"+strings.ToLower(a.Email), now()) {
		return "", time.Time{}, errf(apitypes.CodeRateLimited, "too many password attempts; wait a minute and try again")
	}
	if len(req.NewPassword) < 12 {
		return "", time.Time{}, invalid("password must be at least 12 characters")
	}
	u, err := c.checkPassword(ctx, a.Email, req.CurrentPassword)
	if CodeOf(err) == apitypes.CodeUnauthorized {
		c.audit(ctx, a, "user.password_failed", nil, nil, a.Email, "wrong current password from "+clientIP)
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
		if secret, expires, err = c.newSession(ctx, u); err != nil {
			return "", time.Time{}, err
		}
	}
	c.audit(ctx, a, "user.password", nil, nil, u.Email, "changed own password; other sessions ended"+revokedNote(revoked))
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
	c.audit(ctx, a, "user."+verb, nil, nil, u.Email, fmt.Sprintf(summary, u.Email)+revokedNote(revoked))
	return toUser(u), nil
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
	c.audit(ctx, a, "user.delete", nil, nil, u.Email, "deleted user "+u.Email+revokedNote(revoked))
	return nil
}

// otherUser loads a user an admin manages; nobody can lock themselves out.
func (c *Core) otherUser(ctx context.Context, a Actor, id int64, verb string) (store.User, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "managing users"); err != nil {
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

// ListTokens returns all tokens (never their secrets).
func (c *Core) ListTokens(ctx context.Context, a Actor) ([]apitypes.Token, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "managing tokens"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListTokens(ctx)
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

// CreateToken creates a token and returns its secret, shown only once.
func (c *Core) CreateToken(ctx context.Context, a Actor, req apitypes.CreateTokenRequest) (apitypes.CreatedToken, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "managing tokens"); err != nil {
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
	params := store.CreateTokenParams{Name: req.Name, Scope: string(req.Scope), CreatedBy: a.Name}
	var p *store.Project
	var e *store.Environment
	switch {
	case req.Environment != "" && req.Project == "":
		return apitypes.CreatedToken{}, invalid("environment limit needs a project too")
	case req.Environment != "":
		es, err := c.loadEnv(ctx, EnvRef{req.Project, req.Environment})
		if err != nil {
			return apitypes.CreatedToken{}, err
		}
		p, e = &es.project, &es.env
		params.ProjectID, params.EnvironmentID = &es.project.ID, &es.env.ID
	case req.Project != "":
		proj, err := c.loadProject(ctx, req.Project)
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
		summary += "; " + describeTokenData(req.Data)
	}
	c.audit(ctx, a, "token.create", p, e, "token "+t.Name, summary)
	return apitypes.CreatedToken{Token: out, Secret: secret}, nil
}

// RevokeToken revokes a token by ID.
func (c *Core) RevokeToken(ctx context.Context, a Actor, id int64) error {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "managing tokens"); err != nil {
		return err
	}
	t, err := c.store.GetToken(ctx, id)
	if store.IsNotFound(err) {
		return notFound("token %d not found", id)
	}
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

// ListUsers returns all users with their live sessions and usable tokens.
func (c *Core) ListUsers(ctx context.Context, a Actor) ([]apitypes.User, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "managing users"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListUsersWithCounts(ctx, now())
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.User, 0, len(rows))
	for _, r := range rows {
		u := toUser(r.User)
		u.Sessions, u.Tokens = int(r.Sessions), int(r.Tokens)
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
	c.audit(ctx, a, "user.reset_password", nil, nil, u.Email, "reset the password of "+u.Email+" and ended their sessions"+revokedNote(revoked))
	out.User = toUser(u)
	return out, nil
}

// generatePassword returns a random 24-character password.
func generatePassword() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// CreateUser creates a user who can log in to the UI.
func (c *Core) CreateUser(ctx context.Context, a Actor, req apitypes.CreateUserRequest) (apitypes.User, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "managing users"); err != nil {
		return apitypes.User{}, err
	}
	u, err := c.createUser(ctx, req)
	if err != nil {
		return apitypes.User{}, err
	}
	c.audit(ctx, a, "user.create", nil, nil, u.Email, fmt.Sprintf("created %s user %s", u.Role, u.Email))
	return toUser(u), nil
}

func (c *Core) createUser(ctx context.Context, req apitypes.CreateUserRequest) (store.User, error) {
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
	u, err := c.store.CreateUser(ctx, store.CreateUserParams{Email: req.Email, Name: req.Name, PasswordHash: string(hash), Role: string(req.Role)})
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

// Bootstrap creates the first admin user and an admin token if no users
// exist yet. It returns ok=false (and no error) when already bootstrapped.
func (c *Core) Bootstrap(ctx context.Context, email string) (b Bootstrapped, ok bool, err error) {
	n, err := c.store.CountUsers(ctx)
	if err != nil || n > 0 {
		return b, false, err
	}
	b = Bootstrapped{Email: email, Password: generatePassword()}
	u, err := c.createUser(ctx, apitypes.CreateUserRequest{Email: email, Name: "admin", Password: b.Password, Role: apitypes.ScopeAdmin})
	if err != nil {
		return b, false, err
	}
	created, err := c.createToken(ctx, userActor(u), apitypes.CreateTokenRequest{Name: "bootstrap", Scope: apitypes.ScopeAdmin})
	if err != nil {
		return b, false, err
	}
	b.Token = created.Secret
	c.audit(ctx, System, "user.bootstrap", nil, nil, u.Email, "created the first admin user and token")
	return b, true, nil
}

func toUser(u store.User) apitypes.User {
	return apitypes.User{ID: u.ID, Email: u.Email, Name: u.Name, Role: apitypes.Scope(u.Role), CreatedAt: u.CreatedAt, DisabledAt: u.DisabledAt}
}

func toToken(ctx context.Context, names *names, t store.Token) apitypes.Token {
	out := apitypes.Token{
		ID: t.ID, Name: t.Name, Scope: apitypes.Scope(t.Scope), CreatedBy: t.CreatedBy, CreatedAt: t.CreatedAt,
		ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt, RevokedAt: t.RevokedAt, Origin: t.Origin,
		Data: tokenData(t),
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
