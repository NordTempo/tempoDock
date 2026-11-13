package core

import (
	"cmp"
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Two-factor sign-in (ADR 0057). A user with a passkey or an authenticator
// app signs in with their password, then the second factor (or a recovery
// code): Login leaves a pending session that authenticates nothing, and
// LoginSecondFactor replaces it with a real one that remembers the factor
// (sessions.factor). A passkey alone (PasskeyLogin) also signs in, as two
// factors. Sensitive
// actions need the session to have proved who it is within ReauthWindow
// (requireRecent); Reauth renews that. Orgs and the platform can require
// a second factor of every member or operator (gate).

const (
	// ReauthWindow is how long after signing in or a step-up a UI session
	// may do sensitive things (reveal secrets, create tokens, change
	// members) without confirming again.
	ReauthWindow = 15 * time.Minute
	// pendingTTL is how long a password sign-in waits for its second
	// factor.
	pendingTTL = 5 * time.Minute
	// maxFactorAttempts is how many wrong second factors a pending
	// sign-in survives; then it starts over with the password.
	maxFactorAttempts = 5
	// recoveryCodeCount codes of recoveryCodeLen characters (80 bits).
	recoveryCodeCount = 10
	recoveryCodeLen   = 16
)

// Factor kinds (user_factors.kind, sessions.factor).
const (
	factorPasskey  = "passkey"
	factorTOTP     = "totp"
	factorRecovery = "recovery"
	// factorLogin marks a `dock login` token approved from a session with a
	// second factor.
	factorLogin = "login"
	// factorSSO is a sign-in through one of the platform's identity
	// providers (ADR 0065). It counts as two-factor: enforce MFA at the
	// provider.
	factorSSO = "sso"
)

var errTooManyFactors = errf(apitypes.CodeRateLimited, "too many second factor attempts; wait a minute and try again")

// LoginResult is a sign-in's outcome: a session (Pending false), or, when
// the password was right and a second factor is needed, a pending one
// whose secret goes in its own short-lived cookie.
type LoginResult struct {
	Secret  string
	Expires time.Time
	Pending bool
	// Factors are what the pending sign-in can finish with.
	Factors []string
	Actor   Actor
}

// signInVia is how a sign-in proved who it is besides (or instead of) a
// password: the second factor ("" for none), and for a single sign-on
// (ADR 0065) the identity provider that vouched: its ID, name and label.
type signInVia struct {
	factor        string
	ssoProviderID int64
	ssoProvider   string
	ssoLabel      string
}

// ssoProviderIDPtr is ssoProviderID for the store, nil for none.
func (v signInVia) ssoProviderIDPtr() *int64 {
	if v.ssoProviderID == 0 {
		return nil
	}
	return &v.ssoProviderID
}

// signIn starts u's session, signed in via, and audits it.
func (c *Core) signIn(ctx context.Context, u store.User, via signInVia, clientIP string) (LoginResult, error) {
	secret, expires, a, err := c.newSession(ctx, u, via)
	if err != nil {
		return LoginResult{}, err
	}
	// A factor added since the password was checked: this session must
	// not outlive it (addFactor ended the others).
	if via.factor == "" {
		n, err := c.store.CountUserFactors(ctx, u.ID)
		if err != nil {
			return LoginResult{}, err
		}
		if n > 0 {
			if err := c.store.DeleteSessionByID(ctx, a.SessionID); err != nil {
				return LoginResult{}, err
			}
			return c.startSecondFactor(ctx, u, via)
		}
	}
	summary := "logged in from " + clientIP
	switch {
	case via.ssoProvider != "":
		summary += fmt.Sprintf(" with %s (single sign-on, %s)", via.ssoLabel, via.ssoProvider)
	case via.factor != "":
		summary += " with " + factorName(via.factor)
	}
	// Accounts are the platform's, so logins are platform events.
	c.audit(ctx, userActor(u), "user.login", nil, nil, u.Email, summary)
	a, err = c.actIn(ctx, a, "", apitypes.ScopeAdmin)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Secret: secret, Expires: expires, Actor: a}, nil
}

// factorName is a factor kind in words.
func factorName(kind string) string {
	switch kind {
	case factorPasskey:
		return "a passkey"
	case factorTOTP:
		return "an authenticator app"
	case factorRecovery:
		return "a recovery code"
	case factorSSO:
		return "single sign-on"
	}
	return kind
}

// startSecondFactor leaves a pending session for u, whose password was
// right (or whose single sign-on in via vouched for them), and says which
// factors can finish it.
func (c *Core) startSecondFactor(ctx context.Context, u store.User, via signInVia) (LoginResult, error) {
	rows, err := c.store.ListUserFactors(ctx, u.ID)
	if err != nil {
		return LoginResult{}, err
	}
	codes, err := c.store.CountRecoveryCodes(ctx, u.ID)
	if err != nil {
		return LoginResult{}, err
	}
	var kinds []string
	for _, k := range []string{factorPasskey, factorTOTP} {
		if slices.ContainsFunc(rows, func(f store.UserFactor) bool { return f.Kind == k }) {
			kinds = append(kinds, k)
		}
	}
	if codes > 0 {
		kinds = append(kinds, factorRecovery)
	}
	secret, hash := newSecret(sessionPrefix)
	expires := now().Add(pendingTTL)
	if _, err := c.store.CreateSession(ctx, store.CreateSessionParams{UserID: u.ID, Hash: hash, ExpiresAt: expires, Pending: true, SsoProviderID: via.ssoProviderIDPtr()}); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Secret: secret, Expires: expires, Pending: true, Factors: kinds}, nil
}

// pendingSession loads the live pending sign-in with secret.
func (c *Core) pendingSession(ctx context.Context, secret string) (store.GetSessionUserRow, error) {
	expired := errf(apitypes.CodeExpired, "this sign-in expired; enter your password again")
	if !strings.HasPrefix(secret, sessionPrefix) {
		return store.GetSessionUserRow{}, expired
	}
	row, err := c.store.GetSessionUser(ctx, hashSecret(secret))
	if store.IsNotFound(err) || err == nil && (!row.Session.Pending || now().After(row.Session.ExpiresAt) || row.User.DisabledAt != nil) {
		return row, expired
	}
	return row, err
}

// LoginSecondFactor finishes a password sign-in pending in pendingSecret
// with a passkey, an authenticator app code or a recovery code. Wrong
// factors count per user (a few a minute) and per sign-in
// (maxFactorAttempts, then the password again); each is audited.
func (c *Core) LoginSecondFactor(ctx context.Context, pendingSecret string, v apitypes.Verification, clientIP string) (LoginResult, error) {
	row, err := c.pendingSession(ctx, pendingSecret)
	if err != nil {
		return LoginResult{}, err
	}
	u, s := row.User, row.Session
	// The password's limiter: per email and IP, so guessing codes can't
	// lock the owner out from their own address.
	if !c.loginLimit.Allow(u.Email, clientIP, now()) {
		return LoginResult{}, errTooManyFactors
	}
	if v.Password != "" {
		return LoginResult{}, invalid("the password was checked already; use your second factor")
	}
	kind, err := c.checkFactor(ctx, ceremonySecond, u, s.ID, v, true)
	if CodeOf(err) == apitypes.CodeUnauthorized || CodeOf(err) == apitypes.CodeValidation {
		c.log.Warn("login: wrong second factor", "email", u.Email, "ip", clientIP)
		c.audit(ctx, userActor(u), "user.login_failed", nil, nil, u.Email, fmt.Sprintf("wrong second factor from %s: %v", clientIP, err))
		n, ferr := c.store.FailSecondFactor(ctx, s.ID)
		if ferr == nil && n >= maxFactorAttempts {
			_ = c.store.DeleteSessionByID(ctx, s.ID)
			return LoginResult{}, errf(apitypes.CodeExpired, "too many wrong second factors; enter your password again")
		}
	}
	if err != nil {
		return LoginResult{}, err
	}
	if err := c.store.DeleteSessionByID(ctx, s.ID); err != nil {
		return LoginResult{}, err
	}
	via := signInVia{factor: kind}
	if s.SsoProviderID != nil && row.SsoName != nil && row.SsoLabel != nil {
		via.ssoProviderID, via.ssoProvider, via.ssoLabel = *s.SsoProviderID, *row.SsoName, *row.SsoLabel
	}
	return c.signIn(ctx, u, via, clientIP)
}

// checkFactor verifies v as u's second factor, answering a passkey
// ceremony of purpose started in session sessionID, and returns its kind.
// Recovery codes count only with recovery (signing in, not step-ups).
// Codes spend u's failure budget (factorWait) whichever path they come
// through; a right one clears it. Passkeys can't be guessed and don't.
func (c *Core) checkFactor(ctx context.Context, purpose string, u store.User, sessionID int64, v apitypes.Verification, recovery bool) (string, error) {
	if n := countTrue(v.Passkey != nil, v.Code != "", v.RecoveryCode != ""); n != 1 {
		return "", invalid("give one of a passkey, an authenticator app code or a recovery code")
	}
	if !recovery && v.RecoveryCode != "" {
		return "", invalid("recovery codes are for signing in; confirm with your passkey or authenticator app")
	}
	if v.Passkey != nil {
		return factorPasskey, c.checkPasskey(ctx, purpose, u, sessionID, v.Passkey)
	}
	if wait, locked := factorWait(u.FactorFailures, u.FactorFailedAt, now()); locked {
		return "", errf(apitypes.CodeForbidden, "two-factor sign-in is locked after %d wrong codes; an operator runs `dock admin reset-2fa %s` on the server, or resets your password", u.FactorFailures, u.Email)
	} else if wait > 0 {
		return "", errf(apitypes.CodeRateLimited, "too many wrong codes; wait %s and try again", wait.Round(time.Second))
	}
	kind, err := factorTOTP, error(nil)
	if v.Code != "" {
		err = c.checkTOTP(ctx, u, v.Code)
	} else {
		kind, err = factorRecovery, c.useRecoveryCode(ctx, u, v.RecoveryCode)
	}
	switch {
	case CodeOf(err) == apitypes.CodeUnauthorized:
		if _, ferr := c.store.FailFactor(ctx, store.FailFactorParams{Now: ptr(now()), ID: u.ID}); ferr != nil {
			return "", ferr
		}
	case err == nil:
		if cerr := c.store.ClearFactorFailures(ctx, u.ID); cerr != nil {
			return "", cerr
		}
	}
	return kind, err
}

// Wrong codes in a row a user may make before each next try waits
// (doubling from a second up to maxFactorWait), and after which codes are
// refused until `dock admin reset-2fa` or a password reset. With a stolen
// password, guessing a code then takes years instead of days.
const (
	factorBackoffAfter = 10
	factorLockAt       = 100
	maxFactorWait      = time.Hour
)

// factorWait is how long a user with failures wrong codes in a row, the
// last at last, waits before the next try at t, or locked.
func factorWait(failures int64, last *time.Time, t time.Time) (time.Duration, bool) {
	if failures >= factorLockAt {
		return 0, true
	}
	if failures < factorBackoffAfter || last == nil {
		return 0, false
	}
	d := min(time.Second<<min(failures-factorBackoffAfter, 12), maxFactorWait)
	return max(0, last.Add(d).Sub(t)), false
}

// countTrue counts the true bs.
func countTrue(bs ...bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}

// checkTOTP accepts an authenticator app code of u's, once.
func (c *Core) checkTOTP(ctx context.Context, u store.User, code string) error {
	rows, err := c.store.ListUserFactors(ctx, u.ID)
	if err != nil {
		return err
	}
	for _, f := range rows {
		if f.Kind != factorTOTP {
			continue
		}
		key, err := c.box.Open(f.Secret, factorAAD(f.UserID, f.Kind, nil))
		if err != nil {
			return fmt.Errorf("open authenticator key %d: %w", f.ID, err)
		}
		step, ok := matchTOTP(key, code, now())
		if !ok {
			break
		}
		n, err := c.store.UseTOTPStep(ctx, store.UseTOTPStepParams{Step: step, Now: ptr(now()), ID: f.ID})
		if err != nil {
			return err
		}
		if n == 0 {
			return errf(apitypes.CodeUnauthorized, "this code was used already; wait for the next one")
		}
		return nil
	}
	return errf(apitypes.CodeUnauthorized, "wrong code; check your authenticator app and try the current one")
}

// useRecoveryCode spends one of u's recovery codes and audits it.
func (c *Core) useRecoveryCode(ctx context.Context, u store.User, code string) error {
	n, err := c.store.UseRecoveryCode(ctx, store.UseRecoveryCodeParams{Now: ptr(now()), UserID: u.ID, Hash: hashSecret(normalizeRecoveryCode(code))})
	if err != nil {
		return err
	}
	if n == 0 {
		return errf(apitypes.CodeUnauthorized, "wrong or already used recovery code")
	}
	left, _ := c.store.CountRecoveryCodes(ctx, u.ID)
	c.audit(ctx, userActor(u), "user.recovery_code_used", nil, nil, u.Email, fmt.Sprintf("signed in with a recovery code; %d left", left))
	c.securityNotice(ctx, u, "A recovery code signed in to your Tempo Dock account", "")
	return nil
}

// recoveryAlphabet is lowercase base32 without look-alikes (0, 1, l, o).
const recoveryAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

// newRecoveryCodes returns recoveryCodeCount codes as shown
// ("abcd-efgh-ijkm-npqr") and their hashes.
func newRecoveryCodes() (codes, hashes []string) {
	for range recoveryCodeCount {
		b := make([]byte, recoveryCodeLen)
		_, _ = rand.Read(b)
		var s strings.Builder
		for i, x := range b {
			if i > 0 && i%4 == 0 {
				s.WriteByte('-')
			}
			s.WriteByte(recoveryAlphabet[x%32]) // 256 is a multiple of 32: no bias
		}
		codes = append(codes, s.String())
		hashes = append(hashes, hashSecret(normalizeRecoveryCode(s.String())))
	}
	return codes, hashes
}

// normalizeRecoveryCode accepts a code as typed: any case, dashes and
// spaces anywhere.
func normalizeRecoveryCode(s string) string {
	return strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(s)))
}

// requireRecent checks a UI session proved who it is within ReauthWindow,
// for sensitive actions; the UI asks (POST /me/reauth) and retries. Tokens
// are credentials of their own and pass, as does the system.
func (a Actor) requireRecent(action string) error {
	if a.SessionID == 0 || now().Sub(a.VerifiedAt) <= ReauthWindow {
		return nil
	}
	return errf(apitypes.CodeReauthRequired, "%s needs you to confirm it's you; you signed in or confirmed more than %d minutes ago", action, int(ReauthWindow.Minutes()))
}

// sessionUser loads the user of a UI session for managing their own
// second factors, which needs a browser and a recent confirmation.
func (c *Core) sessionUser(ctx context.Context, a Actor, action string) (store.User, error) {
	if a.SessionID == 0 {
		return store.User{}, errf(apitypes.CodeForbidden, "%s is done in the browser, on your account page", action)
	}
	if err := a.requireRecent(action); err != nil {
		return store.User{}, err
	}
	return c.store.GetUser(ctx, a.UserID)
}

// ReauthOptions starts a passkey step-up for the caller's session, which
// Reauth finishes.
func (c *Core) ReauthOptions(ctx context.Context, a Actor, origin string) (apitypes.PasskeyChallenge, error) {
	if a.SessionID == 0 {
		return apitypes.PasskeyChallenge{}, invalid("API tokens don't need to confirm; only browser sessions do")
	}
	u, err := c.store.GetUser(ctx, a.UserID)
	if err != nil {
		return apitypes.PasskeyChallenge{}, err
	}
	return c.beginPasskey(ctx, ceremonyReauth, origin, &u, a.SessionID)
}

// Reauth confirms the caller is still the person who signed in, so the
// session may do sensitive things for ReauthWindow: with a second factor
// when they have one, else their password. A single sign-on session
// without one confirms by signing in through it again.
func (c *Core) Reauth(ctx context.Context, a Actor, v apitypes.Verification, clientIP string) error {
	if a.SessionID == 0 {
		return invalid("API tokens don't need to confirm; only browser sessions do")
	}
	if !c.loginLimit.Allow(a.Email, clientIP, now()) {
		return errTooManyFactors
	}
	u, err := c.store.GetUser(ctx, a.UserID)
	if err != nil {
		return err
	}
	n, err := c.store.CountUserFactors(ctx, u.ID)
	if err != nil {
		return err
	}
	factor := a.Factor
	switch {
	case n > 0 && v.Password != "":
		err = invalid("confirm with your passkey or authenticator app")
	case n > 0:
		factor, err = c.checkFactor(ctx, ceremonyReauth, u, a.SessionID, v, false)
	case a.SSOProviderID != 0:
		err = errf(apitypes.CodeSSORequired, "you signed in with %s: confirm by signing in with it again", a.SSOLabel)
	case v.Password == "":
		err = invalid("confirm with your password")
	default:
		_, err = c.checkPassword(ctx, u.Email, v.Password)
	}
	if CodeOf(err) == apitypes.CodeUnauthorized {
		c.audit(ctx, a.platform(), "user.reauth_failed", nil, nil, u.Email, fmt.Sprintf("failed to confirm from %s: %v", clientIP, err))
		// A stolen session cookie gets a few tries, then the session ends.
		if n, ferr := c.store.FailSecondFactor(ctx, a.SessionID); ferr == nil && n >= maxFactorAttempts {
			_ = c.store.DeleteSessionByID(ctx, a.SessionID)
			return errf(apitypes.CodeUnauthorized, "signed out after too many failed confirmations; sign in again")
		}
	}
	if err != nil {
		return err
	}
	return c.store.SetSessionFactor(ctx, store.SetSessionFactorParams{Factor: cmp.Or(a.Factor, factor), VerifiedAt: ptr(now()), ID: a.SessionID})
}

// TwoFactor returns the caller's second factors.
func (c *Core) TwoFactor(ctx context.Context, a Actor) (apitypes.TwoFactor, error) {
	if a.UserID == 0 {
		return apitypes.TwoFactor{}, errf(apitypes.CodeForbidden, "%s is a token; only users have second factors", a)
	}
	rows, err := c.store.ListUserFactors(ctx, a.UserID)
	if err != nil {
		return apitypes.TwoFactor{}, err
	}
	codes, err := c.store.CountRecoveryCodes(ctx, a.UserID)
	if err != nil {
		return apitypes.TwoFactor{}, err
	}
	out := apitypes.TwoFactor{Factors: make([]apitypes.Factor, len(rows)), RecoveryCodes: int(codes)}
	for i, f := range rows {
		out.Factors[i] = toFactor(f)
	}
	if until := a.VerifiedAt.Add(ReauthWindow); a.SessionID != 0 && now().Before(until) {
		out.ReauthUntil = &until
	}
	return out, nil
}

func toFactor(f store.UserFactor) apitypes.Factor {
	return apitypes.Factor{ID: f.ID, Kind: f.Kind, Name: f.Name, CreatedAt: f.CreatedAt, LastUsedAt: f.LastUsedAt}
}

// StartTOTP makes a new authenticator app key for the caller; AddTOTP
// keeps it once the app shows a matching code.
func (c *Core) StartTOTP(ctx context.Context, a Actor) (apitypes.TOTPSetup, error) {
	u, err := c.sessionUser(ctx, a, "adding an authenticator app")
	if err != nil {
		return apitypes.TOTPSetup{}, err
	}
	rows, err := c.store.ListUserFactors(ctx, u.ID)
	if err != nil {
		return apitypes.TOTPSetup{}, err
	}
	for _, f := range rows {
		if f.Kind == factorTOTP {
			return apitypes.TOTPSetup{}, conflict("you have an authenticator app already; remove it first to set up another")
		}
	}
	key := newTOTPKey()
	id, err := c.ceremonies.put(&ceremony{purpose: ceremonyTOTP, userID: u.ID, sessionID: a.SessionID, totpKey: key})
	if err != nil {
		return apitypes.TOTPSetup{}, err
	}
	issuer := cmp.Or(c.publicHost(), "Dock")
	return apitypes.TOTPSetup{Ceremony: id, Secret: totpEncoding.EncodeToString(key), URI: totpURI(key, issuer, u.Email)}, nil
}

// AddTOTP confirms the authenticator app StartTOTP made a key for with a
// code from it. A wrong code may be retried a few times.
func (c *Core) AddTOTP(ctx context.Context, a Actor, req apitypes.AddTOTPRequest) (apitypes.FactorAdded, error) {
	u, err := c.sessionUser(ctx, a, "adding an authenticator app")
	if err != nil {
		return apitypes.FactorAdded{}, err
	}
	cer, err := c.ceremonies.take(req.Ceremony, ceremonyTOTP, u.ID, a.SessionID)
	if err != nil {
		return apitypes.FactorAdded{}, errf(apitypes.CodeExpired, "this setup expired; start again")
	}
	step, ok := matchTOTP(cer.totpKey, req.Code, now())
	if !ok {
		if cer.attempts++; cer.attempts < maxFactorAttempts {
			c.ceremonies.retry(req.Ceremony, cer)
		}
		return apitypes.FactorAdded{}, invalid("that code doesn't match; check your phone's clock and enter the current code")
	}
	sealed, err := c.box.Seal(cer.totpKey, factorAAD(u.ID, factorTOTP, nil))
	if err != nil {
		return apitypes.FactorAdded{}, err
	}
	return c.addFactor(ctx, a, u, store.CreateUserFactorParams{
		UserID: u.ID, Kind: factorTOTP, Name: "Authenticator app", Secret: sealed, CreatedAt: now(),
	}, step)
}

// addFactor stores a new second factor of u's (a TOTP one with its first
// accepted step) and makes the caller's session count as two-factor. The
// first factor also creates recovery codes, returned once, and ends u's
// other sessions, which never proved it.
func (c *Core) addFactor(ctx context.Context, a Actor, u store.User, p store.CreateUserFactorParams, step int64) (apitypes.FactorAdded, error) {
	var out apitypes.FactorAdded
	var codes []string
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		n, err := q.CountUserFactors(ctx, u.ID)
		if err != nil {
			return err
		}
		f, err := q.CreateUserFactor(ctx, p)
		if err != nil {
			return err
		}
		if step > 0 {
			if _, err := q.UseTOTPStep(ctx, store.UseTOTPStepParams{Step: step, Now: ptr(now()), ID: f.ID}); err != nil {
				return err
			}
		}
		out.Factor = toFactor(f)
		if err := q.SetSessionFactor(ctx, store.SetSessionFactorParams{Factor: cmp.Or(a.Factor, p.Kind), VerifiedAt: ptr(now()), ID: a.SessionID}); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		codes, err = replaceRecoveryCodes(ctx, q, u.ID)
		if err != nil {
			return err
		}
		return q.DeleteOtherSessions(ctx, store.DeleteOtherSessionsParams{UserID: u.ID, Keep: a.SessionID})
	})
	if store.IsUnique(err) {
		if p.Kind == factorTOTP {
			return out, conflict("you have an authenticator app already; remove it first to set up another")
		}
		return out, conflict("this passkey is on an account already")
	}
	if err != nil {
		return out, err
	}
	out.RecoveryCodes = codes
	summary := fmt.Sprintf("added %s %q", factorName(p.Kind), p.Name)
	if codes != nil {
		summary += "; two-factor sign-in is on, recovery codes created and other sessions ended"
	}
	c.audit(ctx, a.platform(), "user.2fa_add", nil, nil, u.Email, summary)
	c.noticeFor(ctx, a, u, "A second factor was added to your Tempo Dock account")
	return out, nil
}

// replaceRecoveryCodes replaces a user's recovery codes with new ones and
// returns them.
func replaceRecoveryCodes(ctx context.Context, q *store.Queries, userID int64) ([]string, error) {
	if err := q.DeleteRecoveryCodes(ctx, userID); err != nil {
		return nil, err
	}
	codes, hashes := newRecoveryCodes()
	for _, h := range hashes {
		if err := q.CreateRecoveryCode(ctx, store.CreateRecoveryCodeParams{UserID: userID, Hash: h}); err != nil {
			return nil, err
		}
	}
	return codes, nil
}

// RemoveFactor removes one of the caller's second factors. Removing the
// last turns two-factor sign-in off and spends the recovery codes; it is
// refused while an org of theirs, or the platform for an operator,
// requires a second factor.
func (c *Core) RemoveFactor(ctx context.Context, a Actor, id int64) error {
	u, err := c.sessionUser(ctx, a, "removing a second factor")
	if err != nil {
		return err
	}
	var f store.UserFactor
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		if f, err = q.DeleteUserFactor(ctx, store.DeleteUserFactorParams{ID: id, UserID: u.ID}); err != nil {
			return err
		}
		n, err := q.CountUserFactors(ctx, u.ID)
		if err != nil || n > 0 {
			return err
		}
		if why, err := c.twoFactorRequiredOf(ctx, q, u); err != nil || why != "" {
			return cmpErrs(err, conflict("%s requires two-factor sign-in; remove it only after the requirement is lifted, or reset it from the server with `dock admin reset-2fa`", why))
		}
		return q.DeleteRecoveryCodes(ctx, u.ID)
	})
	if store.IsNotFound(err) {
		return notFound("second factor %d not found on your account", id)
	}
	if err != nil {
		return err
	}
	c.audit(ctx, a.platform(), "user.2fa_remove", nil, nil, u.Email, fmt.Sprintf("removed %s %q", factorName(f.Kind), f.Name))
	c.noticeFor(ctx, a, u, "A second factor was removed from your Tempo Dock account")
	return nil
}

func cmpErrs(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

// twoFactorRequiredOf names what requires u to have a second factor: one
// of their orgs, or the platform when they are an operator. "" for
// nothing.
func (c *Core) twoFactorRequiredOf(ctx context.Context, q *store.Queries, u store.User) (string, error) {
	if u.Operator && c.operators2FA.Load() {
		return "the platform", nil
	}
	orgs, err := q.ListUserOrgs(ctx, u.ID)
	if err != nil {
		return "", err
	}
	for _, o := range orgs {
		if o.Org.RequireTwoFactor {
			return "organization " + o.Org.Slug, nil
		}
	}
	return "", nil
}

// NewRecoveryCodes replaces the caller's recovery codes and returns the
// new ones, once.
func (c *Core) NewRecoveryCodes(ctx context.Context, a Actor) (apitypes.RecoveryCodes, error) {
	u, err := c.sessionUser(ctx, a, "creating recovery codes")
	if err != nil {
		return apitypes.RecoveryCodes{}, err
	}
	var codes []string
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		n, err := q.CountUserFactors(ctx, u.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			return invalid("recovery codes come with a second factor; add a passkey or an authenticator app first")
		}
		codes, err = replaceRecoveryCodes(ctx, q, u.ID)
		return err
	})
	if err != nil {
		return apitypes.RecoveryCodes{}, err
	}
	c.audit(ctx, a.platform(), "user.recovery_codes", nil, nil, u.Email, "created new recovery codes; the old ones stopped working")
	c.noticeFor(ctx, a, u, "New recovery codes were made for your Tempo Dock account")
	return apitypes.RecoveryCodes{Codes: codes}, nil
}

// ResetTwoFactor removes a user's second factors and recovery codes and
// ends their sessions and `dock login` tokens, for someone who lost
// theirs. Operators only; `dock admin reset-2fa` on the server.
func (c *Core) ResetTwoFactor(ctx context.Context, a Actor, email string) error {
	if err := a.requireOperator("resetting a user's two-factor sign-in"); err != nil {
		return err
	}
	if err := a.requireRecent("resetting a user's two-factor sign-in"); err != nil {
		return err
	}
	u, err := c.store.GetUserByEmail(ctx, email)
	if store.IsNotFound(err) {
		return notFound("no user with email %s", email)
	}
	if err != nil {
		return err
	}
	var n, revoked int64
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		if n, err = q.DeleteUserFactors(ctx, u.ID); err != nil {
			return err
		}
		if err := q.DeleteRecoveryCodes(ctx, u.ID); err != nil {
			return err
		}
		if err := q.ClearFactorFailures(ctx, u.ID); err != nil {
			return err
		}
		revoked, err = endUserAccess(ctx, q, u.ID, 0)
		return err
	})
	if err != nil {
		return err
	}
	c.audit(ctx, a.platform(), "user.2fa_reset", nil, nil, u.Email,
		fmt.Sprintf("reset the two-factor sign-in of %s (%d factors removed, recovery codes spent) and ended their sessions%s", u.Email, n, revokedNote(revoked)))
	c.noticeFor(ctx, a, u, "Your Tempo Dock two-factor sign-in was reset")
	return nil
}

// gateOperator takes operating the platform away from a user's session or
// `dock login` token that used no second factor while the platform
// requires one of operators. A sign-in through an identity provider
// counts as one (ADR 0065).
func (c *Core) gateOperator(a Actor) Actor {
	if a.Operator && a.Factor == "" && c.operators2FA.Load() {
		a.Operator, a.operatorNeeds2FA = false, true
	}
	return a
}

// SetOrgRequireTwoFactor turns on or off the org's requirement that
// members sign in with a second factor. Turning it on needs the caller's
// own session to have used one, so they don't lock themselves out.
func (c *Core) SetOrgRequireTwoFactor(ctx context.Context, a Actor, req apitypes.RequireTwoFactorRequest) (apitypes.Org, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "changing the organization's sign-in policy"); err != nil {
		return apitypes.Org{}, err
	}
	if err := a.requireRecent("changing the organization's sign-in policy"); err != nil {
		return apitypes.Org{}, err
	}
	if req.Require && a.UserID != 0 && a.Factor == "" {
		return apitypes.Org{}, invalid("sign in with a second factor yourself first: add a passkey or an authenticator app on your account page")
	}
	o, err := c.store.SetOrgRequireTwoFactor(ctx, store.SetOrgRequireTwoFactorParams{RequireTwoFactor: req.Require, ID: a.OrgID})
	if err != nil {
		return apitypes.Org{}, err
	}
	summary := "stopped requiring two-factor sign-in of members"
	if req.Require {
		summary = "required two-factor sign-in of every member"
	}
	c.audit(ctx, a, "org.require_two_factor", nil, nil, o.Slug, summary)
	return toOrg(o, a.Scope), nil
}

// PlatformSecurity returns the platform's sign-in policy. Operators only.
func (c *Core) PlatformSecurity(_ context.Context, a Actor) (apitypes.PlatformSecurity, error) {
	if err := a.requireOperator("reading the platform's sign-in policy"); err != nil {
		return apitypes.PlatformSecurity{}, err
	}
	return apitypes.PlatformSecurity{OperatorsTwoFactor: c.operators2FA.Load()}, nil
}

// SetPlatformSecurity changes the platform's sign-in policy. Requiring a
// second factor of operators needs the caller's own session to have used
// one.
func (c *Core) SetPlatformSecurity(ctx context.Context, a Actor, req apitypes.PlatformSecurity) (apitypes.PlatformSecurity, error) {
	if err := a.requireOperator("changing the platform's sign-in policy"); err != nil {
		return apitypes.PlatformSecurity{}, err
	}
	if err := a.requireRecent("changing the platform's sign-in policy"); err != nil {
		return apitypes.PlatformSecurity{}, err
	}
	if req.OperatorsTwoFactor && a.UserID != 0 && a.Factor == "" {
		return apitypes.PlatformSecurity{}, invalid("sign in with a second factor yourself first: add a passkey or an authenticator app on your account page")
	}
	if err := c.store.SetOperatorsTwoFactor(ctx, store.SetOperatorsTwoFactorParams{OperatorsTwoFactor: req.OperatorsTwoFactor, UpdatedBy: a.Name, UpdatedAt: now()}); err != nil {
		return apitypes.PlatformSecurity{}, err
	}
	c.operators2FA.Store(req.OperatorsTwoFactor)
	summary := "stopped requiring two-factor sign-in of operators"
	if req.OperatorsTwoFactor {
		summary = "required two-factor sign-in of operators"
	}
	c.audit(ctx, a.platform(), "platform.operators_two_factor", nil, nil, "sign-in policy", summary)
	return req, nil
}

// loadPlatformSecurity reads the platform's sign-in policy into memory,
// where every authentication reads it.
func (c *Core) loadPlatformSecurity(ctx context.Context) error {
	on, err := c.store.GetOperatorsTwoFactor(ctx)
	if store.IsNotFound(err) {
		on, err = false, nil
	}
	if err != nil {
		return fmt.Errorf("load sign-in policy: %w", err)
	}
	c.operators2FA.Store(on)
	return nil
}
