package tempobase

import (
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbstore"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// Secret prefixes make leaked credentials easy to recognise and scan for.
const (
	sessionPrefix = "tbs_"
	tokenPrefix   = "tbt_"
	linkPrefix    = "tbl_"
)

// SessionTTL is how long a sign-in lasts; it doesn't slide, so requests
// never write.
const SessionTTL = 30 * 24 * time.Hour

// minPassword is the shortest password accepted.
const minPassword = 12

// bcryptCost is the cost of password hashes.
const bcryptCost = 10

// dummyHash keeps sign-ins for unknown emails as slow as wrong passwords.
// Lazy, so commands that never check a password don't pay for it.
var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("tempobase-dummy-password"), bcryptCost)
	return h
})

// newSecret returns a random secret with prefix and its stored hash.
func newSecret(prefix string) (secret, hash string) {
	secret = prefix + randomCode(32)
	return secret, hashSecret(secret)
}

// hashSecret is how sessions, tokens and links are stored: sha256, hex.
func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

var errUnauthenticated = errf(apitypes.CodeUnauthorized, "invalid, expired or revoked credentials; sign in again or use a valid token")

// AuthenticateSession resolves a session cookie to its user, with one
// query. An expired session is deleted when presented: nothing sweeps
// them on a timer.
func (a *App) AuthenticateSession(ctx context.Context, secret string) (Actor, error) {
	if !strings.HasPrefix(secret, sessionPrefix) {
		return Actor{}, errUnauthenticated
	}
	row, err := a.store.GetSessionUser(ctx, hashSecret(secret))
	if tbstore.IsNotFound(err) {
		return Actor{}, errUnauthenticated
	}
	if err != nil {
		return Actor{}, err
	}
	if a.now().After(row.SessionExpiresAt) {
		if err := a.store.DeleteSessionByID(ctx, row.SessionID); err != nil {
			a.log.Warn("delete expired session", "err", err)
		}
		return Actor{}, errUnauthenticated
	}
	if row.User.DisabledAt != nil {
		return Actor{}, errUnauthenticated
	}
	return userActor(row.User), nil
}

// AuthenticateToken resolves an API token to its actor, with one query
// that also checks its owner: a disabled or deleted owner ends it.
func (a *App) AuthenticateToken(ctx context.Context, secret string) (Actor, error) {
	if !strings.HasPrefix(secret, tokenPrefix) {
		return Actor{}, errUnauthenticated
	}
	row, err := a.store.GetTokenAuth(ctx, hashSecret(secret))
	if tbstore.IsNotFound(err) {
		return Actor{}, errUnauthenticated
	}
	if err != nil {
		return Actor{}, err
	}
	t := row.Token
	if t.RevokedAt != nil || (t.ExpiresAt != nil && a.now().After(*t.ExpiresAt)) || row.UserDisabledAt != nil {
		return Actor{}, errUnauthenticated
	}
	act := Actor{
		Kind: "token", Name: t.Name, UserID: t.UserID, Email: row.UserEmail, Role: tbtypes.Role(row.UserRole),
		TokenID: t.ID, TokenData: tokenData(t), TokenLimited: t.ConnectionsLimited != 0,
	}
	// A limited token whose connections were all removed keeps its limit
	// with nothing in it.
	if act.TokenLimited && row.ConnectionIds != "" {
		for s := range strings.SplitSeq(row.ConnectionIds, ",") {
			id, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return Actor{}, fmt.Errorf("token %d connection limit %q: %w", t.ID, row.ConnectionIds, err)
			}
			act.TokenConnections = append(act.TokenConnections, id)
		}
		act.TokenConnectionNames = strings.Split(row.ConnectionNames, ",")
	}
	return act, nil
}

func userActor(u tbstore.User) Actor {
	return Actor{Kind: "user", Name: u.Email, UserID: u.ID, Email: u.Email, Role: tbtypes.Role(u.Role)}
}

// Me describes the caller.
func (a *App) Me(ctx context.Context, act Actor) (tbtypes.Me, error) {
	me := tbtypes.Me{Kind: act.Kind, Name: act.Name, Email: act.Email, Role: act.Role, TokenLimited: act.TokenLimited, TokenConnections: []string{}}
	// A person's actor name is their email (audit uses it); show the name
	// they set, if any.
	if act.Kind == "user" {
		if u, err := a.store.GetUser(ctx, act.UserID); err == nil && u.Name != "" {
			me.Name = u.Name
		}
	}
	if act.TokenConnectionNames != nil {
		me.TokenConnections = slices.Sorted(slices.Values(act.TokenConnectionNames))
	}
	return me, nil
}

var errWrongPassword = errf(apitypes.CodeUnauthorized, "wrong email or password")

// allow spends a sign-in attempt for each key, refusing when one has none
// left.
func (a *App) allow(keys ...string) error {
	t := a.now()
	for _, k := range keys {
		if !a.loginLimit.Allow(k, t) {
			return errf(apitypes.CodeRateLimited, "too many attempts; wait a minute and try again")
		}
	}
	return nil
}

// Login checks a person's password and starts a session; it returns the
// session secret for the cookie. Attempts are rate limited per client IP
// and per email; failures are logged and audited (never the password).
func (a *App) Login(ctx context.Context, email, password, clientIP string) (secret string, expires time.Time, act Actor, err error) {
	if err := a.allow("ip:"+clientIP, "email:"+strings.ToLower(email)); err != nil {
		a.log.Warn("sign-in rate limited", "email", email, "ip", clientIP)
		return "", time.Time{}, Actor{}, err
	}
	u, err := a.checkPassword(ctx, email, password)
	if err == nil && u.DisabledAt != nil {
		err = errf(apitypes.CodeUnauthorized, "this account is disabled; ask an admin")
	}
	if codeOf(err) == apitypes.CodeUnauthorized {
		a.log.Warn("sign-in failed", "email", email, "ip", clientIP, "reason", err.Error())
		a.audit(ctx, Actor{Kind: "user", Name: email}, "user.login_failed", nil, email, fmt.Sprintf("failed sign-in from %s: %v", clientIP, err))
	}
	if err != nil {
		return "", time.Time{}, Actor{}, err
	}
	secret, expires, err = a.newSession(ctx, a.store.Queries, u.ID)
	if err != nil {
		return "", time.Time{}, Actor{}, err
	}
	act = userActor(u)
	a.audit(ctx, act, "user.login", nil, u.Email, "signed in from "+clientIP)
	return secret, expires, act, nil
}

// checkPassword returns the user if password is theirs. At most two
// checks run at once, and unknown emails cost as much as wrong passwords.
func (a *App) checkPassword(ctx context.Context, email, password string) (tbstore.User, error) {
	release, err := a.hashSlot(ctx)
	if err != nil {
		return tbstore.User{}, err
	}
	defer release()
	u, err := a.store.GetUserByEmail(ctx, email)
	if err != nil && !tbstore.IsNotFound(err) {
		return tbstore.User{}, err
	}
	// Unknown emails and people whose link hasn't set a password yet.
	if err != nil || u.PasswordHash == "" {
		_ = bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
		return tbstore.User{}, errWrongPassword
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return tbstore.User{}, errWrongPassword
	}
	return u, nil
}

// hashSlot waits for one of the bcrypt slots.
func (a *App) hashSlot(ctx context.Context) (release func(), err error) {
	select {
	case a.bcrypt <- struct{}{}:
		return func() { <-a.bcrypt }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// hashPassword checks a new password's length and hashes it.
func (a *App) hashPassword(ctx context.Context, password string) (string, error) {
	if len(password) < minPassword {
		return "", invalid("the password must be at least %d characters", minPassword)
	}
	if len(password) > 72 {
		return "", invalid("the password must be at most 72 bytes")
	}
	release, err := a.hashSlot(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	return string(h), err
}

// newSession starts a session for a user in q (a transaction or the
// store), and deletes expired sessions while at it.
func (a *App) newSession(ctx context.Context, q *tbstore.Queries, userID int64) (secret string, expires time.Time, err error) {
	secret, hash := newSecret(sessionPrefix)
	n := a.now()
	expires = n.Add(SessionTTL)
	if err := q.CreateSession(ctx, tbstore.CreateSessionParams{UserID: userID, SecretHash: hash, CreatedAt: n, ExpiresAt: expires}); err != nil {
		return "", time.Time{}, err
	}
	if err := q.DeleteExpiredSessions(ctx, n); err != nil {
		return "", time.Time{}, err
	}
	return secret, expires, nil
}

// Logout ends a session. Unknown sessions are ignored.
func (a *App) Logout(ctx context.Context, secret string) error {
	return a.store.DeleteSession(ctx, hashSecret(secret))
}

// ChangePassword changes the signed-in person's own password. Every
// session of theirs ends; the caller gets the returned session in its
// place. Tokens have no password.
func (a *App) ChangePassword(ctx context.Context, act Actor, req apitypes.ChangePasswordRequest, clientIP string) (secret string, expires time.Time, err error) {
	if !act.isUser() {
		return "", time.Time{}, invalid("only people have passwords; sign in to change yours")
	}
	if err := a.allow("ip:"+clientIP, "email:"+strings.ToLower(act.Email)); err != nil {
		return "", time.Time{}, err
	}
	u, err := a.checkPassword(ctx, act.Email, req.CurrentPassword)
	if codeOf(err) == apitypes.CodeUnauthorized {
		a.audit(ctx, act, "user.password_failed", nil, act.Email, "wrong current password from "+clientIP)
		return "", time.Time{}, invalid("the current password is wrong")
	}
	if err != nil {
		return "", time.Time{}, err
	}
	hash, err := a.hashPassword(ctx, req.NewPassword)
	if err != nil {
		return "", time.Time{}, err
	}
	err = a.store.Tx(ctx, func(q *tbstore.Queries) error {
		if err := q.SetUserPassword(ctx, tbstore.SetUserPasswordParams{PasswordHash: hash, ID: u.ID}); err != nil {
			return err
		}
		if err := q.DeleteUserSessions(ctx, u.ID); err != nil {
			return err
		}
		secret, expires, err = a.newSession(ctx, q, u.ID)
		return err
	})
	if err != nil {
		return "", time.Time{}, err
	}
	a.audit(ctx, act, "user.update", nil, u.Email, "changed own password; other sessions ended")
	return secret, expires, nil
}

var errSetUp = conflict("tempoBase is already set up; sign in, or ask an admin for an invite")

// SetupNeeded reports whether the first admin has yet to be created.
func (a *App) SetupNeeded(ctx context.Context) (bool, error) {
	n, err := a.store.CountUsers(ctx)
	return n == 0, err
}

// StartSetup makes the one-time setup code when there are no users yet,
// for serve to log; ok is false (no code) when someone exists. The code
// lives in memory only: a restart makes a new one.
func (a *App) StartSetup(ctx context.Context) (code string, ok bool, err error) {
	needed, err := a.SetupNeeded(ctx)
	if err != nil || !needed {
		return "", false, err
	}
	a.setupMu.Lock()
	defer a.setupMu.Unlock()
	a.setupCode = randomCode(16)
	return a.setupCode, true, nil
}

// Setup creates the first admin with the setup code and signs them in.
// The code is spent on success.
func (a *App) Setup(ctx context.Context, req tbtypes.SetupRequest, clientIP string) (secret string, expires time.Time, act Actor, err error) {
	if err := a.allow("ip:" + clientIP); err != nil {
		return "", time.Time{}, Actor{}, err
	}
	a.setupMu.Lock()
	defer a.setupMu.Unlock()
	if a.setupCode == "" {
		return "", time.Time{}, Actor{}, errSetUp
	}
	if subtle.ConstantTimeCompare([]byte(req.Code), []byte(a.setupCode)) != 1 {
		a.log.Warn("setup with a wrong code", "ip", clientIP)
		return "", time.Time{}, Actor{}, errf(apitypes.CodeUnauthorized, "wrong setup code; use the link the server logged at start")
	}
	u, err := a.createUser(ctx, req.Email, req.Name, tbtypes.RoleAdmin, req.Password, func(q *tbstore.Queries, u tbstore.User) error {
		// `tempobase admin create-user` may have run since start.
		if n, err := q.CountUsers(ctx); err != nil || n > 1 {
			return cmp.Or(err, errSetUp)
		}
		secret, expires, err = a.newSession(ctx, q, u.ID)
		return err
	})
	if errors.Is(err, errSetUp) {
		a.setupCode = ""
	}
	if err != nil {
		return "", time.Time{}, Actor{}, err
	}
	a.setupCode = ""
	act = userActor(u)
	a.audit(ctx, act, "user.create", nil, u.Email, "created the first admin "+u.Email+" with the setup code")
	return secret, expires, act, nil
}

// createUser validates and creates a person (password "" leaves it unset
// for a link to set), running then in the same transaction, if set.
func (a *App) createUser(ctx context.Context, email, name string, role tbtypes.Role, password string, then func(*tbstore.Queries, tbstore.User) error) (tbstore.User, error) {
	email, name = strings.TrimSpace(email), strings.TrimSpace(name)
	switch {
	case !validEmail(email):
		return tbstore.User{}, invalid("email %q is not valid", email)
	case name == "" || len(name) > 100:
		return tbstore.User{}, invalid("a name of 1-100 characters is required")
	case !validRole(role):
		return tbstore.User{}, invalid("role must be viewer, editor or admin")
	}
	var hash string
	if password != "" {
		var err error
		if hash, err = a.hashPassword(ctx, password); err != nil {
			return tbstore.User{}, err
		}
	}
	var u tbstore.User
	err := a.store.Tx(ctx, func(q *tbstore.Queries) error {
		var err error
		u, err = q.CreateUser(ctx, tbstore.CreateUserParams{Email: email, Name: name, Role: string(role), PasswordHash: hash, CreatedAt: a.now()})
		if err != nil || then == nil {
			return err
		}
		return then(q, u)
	})
	if tbstore.IsUnique(err) {
		return u, conflict("a person with email %s already exists", email)
	}
	return u, err
}

// validEmail is a sanity check, not RFC 5322: something@something.
func validEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	return at > 0 && at < len(s)-1 && len(s) <= 254 && !strings.ContainsAny(s, " \t\r\n")
}
