package core

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/httpkit"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Browser login for the CLI (`dock login`), in the style of the OAuth
// device authorization grant (RFC 8628), so the CLI and the browser can be
// on different machines:
//
//  1. The CLI starts a login (StartCLILogin) and gets a secret device code
//     plus a short user code and the page to approve it on.
//  2. A signed-in user opens the page, types the user code from their
//     terminal (CLILogin) and approves or denies it (DecideCLILogin). The
//     page link never carries the code: anyone who can reach the API can
//     start a login, and a link that only needs a click to approve would
//     let them phish a signed-in admin for a token.
//  3. The CLI long-polls WaitCLILogin with its device code. Approval wakes
//     it at once; it then gets a new API token of the user's, exactly once.
//     The token stays tied to that user (tokens.user_id): see
//     AuthenticateToken.
//
// Logins live in the cli_logins table for at most cliLoginTTL. Only the
// device code's hash is stored, and no token exists until the CLI collects
// it, so no secret is ever at rest.

const (
	cliLoginTTL = 10 * time.Minute
	// cliTokenTTL is how long a token from a browser login lasts.
	cliTokenTTL = 90 * 24 * time.Hour
	// cliWait is how long one wait request is held while still pending.
	cliWait = 30 * time.Second
	// maxPendingCLILogins bounds what unauthenticated callers can store.
	maxPendingCLILogins = 500
	deviceCodePrefix    = "dockdev_"
	// userCodeAlphabet has no vowels (no words) and no look-alikes
	// (0/O, 1/I/L, 5/S, 2/Z are all absent or only one of a pair).
	userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"
	userCodeLen      = 8
)

// cliLogins is Core's in-memory side of browser logins: rate limits and
// the channels that wake waiting CLIs.
type cliLogins struct {
	// starts limits logins started per client IP.
	starts *httpkit.Limiter
	// fails counts wrong user codes per user and per IP; once spent, code
	// lookups are refused until it refills (a lockout of minutes).
	fails *httpkit.Limiter
	// wait is cliWait; tests shorten it.
	wait time.Duration

	mu      sync.Mutex
	waiters map[int64]*cliWaiter // by cli_logins.id
}

// cliWaiter is closed when its login is decided. n counts the requests
// waiting on it.
type cliWaiter struct {
	ch chan struct{}
	n  int
}

func newCLILogins() *cliLogins {
	return &cliLogins{
		starts:  httpkit.NewLimiter(10, 30*time.Second),
		fails:   httpkit.NewLimiter(5, 2*time.Minute),
		wait:    cliWait,
		waiters: map[int64]*cliWaiter{},
	}
}

// watch returns a channel closed when login id is decided, and a func to
// call when done waiting.
func (w *cliLogins) watch(id int64) (decided <-chan struct{}, done func(), ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	cw := w.waiters[id]
	if cw == nil {
		cw = &cliWaiter{ch: make(chan struct{})}
		w.waiters[id] = cw
	}
	if cw.n >= maxCLIWaiters {
		return nil, nil, false
	}
	cw.n++
	return cw.ch, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if cw.n--; cw.n == 0 && w.waiters[id] == cw {
			delete(w.waiters, id)
		}
	}, true
}

// maxCLIWaiters is how many requests may wait on one login at once. A
// CLI waits with one; waiting needs no sign-in, only the login's device
// code, so without a bound whoever started a login could park any number
// of requests on it, each held for cliWait.
const maxCLIWaiters = 3

// notify wakes everyone waiting on login id.
func (w *cliLogins) notify(id int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if cw := w.waiters[id]; cw != nil {
		close(cw.ch)
		delete(w.waiters, id)
	}
}

var (
	errCLILoginExpired = errf(apitypes.CodeExpired, "this login expired or was already used; run `dock login` again")
	errCLILoginDenied  = errf(apitypes.CodeDenied, "the login was denied in the browser")
)

// StartCLILogin creates a pending browser login. Unauthenticated, so it is
// rate limited per client IP. VerificationURL is left for the API to fill
// in, since only it knows the server's address.
func (c *Core) StartCLILogin(ctx context.Context, req apitypes.CLILoginStartRequest, clientIP string) (apitypes.CLILoginStart, error) {
	t := now()
	if !c.cliLogins.starts.Allow("ip:"+httpkit.ClientKey(clientIP), t) {
		return apitypes.CLILoginStart{}, errf(apitypes.CodeRateLimited, "too many logins started from %s; wait a minute and try again", clientIP)
	}
	if err := c.store.DeleteExpiredCLILogins(ctx, t); err != nil {
		return apitypes.CLILoginStart{}, err
	}
	n, err := c.store.CountPendingCLILogins(ctx, t)
	if err != nil {
		return apitypes.CLILoginStart{}, err
	}
	if n >= maxPendingCLILogins {
		c.log.Warn("cli login: too many pending logins", "ip", clientIP)
		return apitypes.CLILoginStart{}, errf(apitypes.CodeRateLimited, "too many logins are waiting for approval; try again in a few minutes")
	}
	device, hash := newSecret(deviceCodePrefix)
	params := store.CreateCLILoginParams{
		DeviceHash: hash,
		ClientName: clean(req.ClientName, 64, "unknown client"),
		Hostname:   clean(req.Hostname, 255, "unknown host"),
		Os:         clean(req.OS, 64, "unknown"),
		Ip:         clientIP,
		CreatedAt:  t,
		ExpiresAt:  t.Add(cliLoginTTL),
		Platform:   req.Platform,
	}
	for range 3 { // 20^8 codes: a clash with a live one is very unlikely
		params.UserCode = newUserCode()
		_, err := c.store.CreateCLILogin(ctx, params)
		if store.IsUnique(err) {
			continue
		}
		if err != nil {
			return apitypes.CLILoginStart{}, err
		}
		return apitypes.CLILoginStart{UserCode: formatUserCode(params.UserCode), DeviceCode: device, ExpiresAt: params.ExpiresAt}, nil
	}
	return apitypes.CLILoginStart{}, fmt.Errorf("cli login: no free user code")
}

// WaitCLILogin waits up to cliWait for the login to be decided. It returns
// status "pending" if it wasn't (the CLI asks again), the new token once
// approved, CodeDenied or CodeExpired. Approval wakes it immediately; it
// never polls.
func (c *Core) WaitCLILogin(ctx context.Context, deviceCode string) (apitypes.CLILoginWait, error) {
	hash := hashSecret(deviceCode)
	l, err := c.store.GetCLILoginByDeviceHash(ctx, hash)
	if store.IsNotFound(err) {
		return apitypes.CLILoginWait{}, errCLILoginExpired
	}
	if err != nil {
		return apitypes.CLILoginWait{}, err
	}
	decided, done, ok := c.cliLogins.watch(l.ID)
	if !ok {
		return apitypes.CLILoginWait{}, errf(apitypes.CodeRateLimited, "%d requests already wait on this login; one is enough", maxCLIWaiters)
	}
	defer done()
	timer := time.NewTimer(min(c.cliLogins.wait, max(0, time.Until(l.ExpiresAt))))
	defer timer.Stop()
	timedOut := false
	for {
		// Read after watching, so a decision in between still counts.
		l, err := c.store.GetCLILoginByDeviceHash(ctx, hash)
		if res, ok, err := c.settleCLILogin(ctx, l, err); ok || err != nil {
			return res, err
		}
		if timedOut {
			return apitypes.CLILoginWait{Status: "pending"}, nil
		}
		select {
		case <-decided:
			decided = nil // closed: read the decision, then wait no more on it
		case <-timer.C:
			timedOut = true
		case <-c.ctx.Done():
			timedOut = true
		case <-ctx.Done():
			return apitypes.CLILoginWait{}, ctx.Err()
		}
	}
}

// settleCLILogin turns a decided or expired login into WaitCLILogin's
// answer (ok), consuming it. Pending logins return ok false.
func (c *Core) settleCLILogin(ctx context.Context, l store.CliLogin, err error) (apitypes.CLILoginWait, bool, error) {
	switch {
	case store.IsNotFound(err):
		return apitypes.CLILoginWait{}, true, errCLILoginExpired
	case err != nil:
		return apitypes.CLILoginWait{}, true, err
	case l.Status == "approved":
		res, err := c.issueCLIToken(ctx, l)
		return res, true, err
	case l.Status == "denied":
		_, err := c.store.DeleteCLILogin(ctx, store.DeleteCLILoginParams{ID: l.ID, Status: l.Status})
		if err == nil {
			err = errCLILoginDenied
		}
		return apitypes.CLILoginWait{}, true, err
	case now().After(l.ExpiresAt):
		_, err := c.store.DeleteCLILogin(ctx, store.DeleteCLILoginParams{ID: l.ID, Status: l.Status})
		if err == nil {
			err = errCLILoginExpired
		}
		return apitypes.CLILoginWait{}, true, err
	}
	return apitypes.CLILoginWait{}, false, nil
}

// issueCLIToken consumes an approved login and creates the approving
// user's token: unlimited, expiring in cliTokenTTL, with scope admin that
// their role caps in each org they act in.
func (c *Core) issueCLIToken(ctx context.Context, l store.CliLogin) (apitypes.CLILoginWait, error) {
	if l.UserID == nil { // approving always sets it
		return apitypes.CLILoginWait{}, errCLILoginExpired
	}
	u, err := c.store.GetUser(ctx, *l.UserID)
	if store.IsNotFound(err) || err == nil && u.DisabledAt != nil {
		// Approved, then the user was disabled before the CLI collected.
		_, err = c.store.DeleteCLILogin(ctx, store.DeleteCLILoginParams{ID: l.ID, Status: l.Status})
		if err == nil {
			err = errCLILoginDenied
		}
		return apitypes.CLILoginWait{}, err
	}
	if err != nil {
		return apitypes.CLILoginWait{}, err
	}
	// A platform login (ADR 0080) runs the platform with the user's
	// platform role and acts in no org; an org login acts in their orgs.
	prefix, name, platform := tokenPrefix, cliTokenName(l.Hostname), (*string)(nil)
	if l.Platform {
		prefix, name, platform = platformTokenPrefix, name+" (platform)", ptr("login")
	}
	secret, hash := newSecret(prefix)
	var tok store.Token
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		// Deleting first makes the token single use: a concurrent wait
		// finds nothing to delete.
		n, err := q.DeleteCLILogin(ctx, store.DeleteCLILoginParams{ID: l.ID, Status: "approved"})
		if err != nil {
			return err
		}
		if n == 0 {
			return errCLILoginExpired
		}
		tok, err = q.CreateCLILoginToken(ctx, store.CreateCLILoginTokenParams{
			Name: name, Hash: hash, Scope: string(apitypes.ScopeAdmin), CreatedBy: u.Email, UserID: &u.ID, ExpiresAt: ptr(now().Add(cliTokenTTL)),
			TwoFactor: l.TwoFactor, Platform: platform,
		})
		return err
	})
	if err != nil {
		return apitypes.CLILoginWait{}, err
	}
	c.audit(ctx, userActor(u), "token.cli_login", nil, nil, "token "+tok.Name,
		fmt.Sprintf("approved dock login from %s (%s, %s); created token %q (id %d), acting with their role", l.Ip, l.Hostname, l.Os, tok.Name, tok.ID))
	out := apitypes.CLILoginWait{Status: "approved", Token: secret, User: u.Email}
	if l.Platform {
		out.Platform = apitypes.PlatformRole(deref(u.PlatformRole))
		return out, nil
	}
	ua := userActor(u)
	if l.TwoFactor {
		ua.Factor = factorLogin
	}
	if a, err := c.actIn(ctx, ua, "", apitypes.ScopeAdmin); err == nil {
		out.Scope = a.Scope // their role, if they have one org
	}
	return out, nil
}

// CLILogin shows a pending login to the user about to approve it.
func (c *Core) CLILogin(ctx context.Context, a Actor, userCode, clientIP string) (apitypes.CLILogin, error) {
	l, err := c.cliLoginFor(ctx, a, userCode, clientIP)
	if err != nil {
		return apitypes.CLILogin{}, err
	}
	return apitypes.CLILogin{
		UserCode: formatUserCode(l.UserCode), ClientName: l.ClientName, Hostname: l.Hostname, OS: l.Os, IP: l.Ip,
		CreatedAt: l.CreatedAt, ExpiresAt: l.ExpiresAt, Status: l.Status,
		Scope: a.Scope, TokenName: cliTokenName(l.Hostname), TokenDays: int(cliTokenTTL / (24 * time.Hour)),
		Platform: l.Platform,
	}, nil
}

// DecideCLILogin approves or denies a pending login as the signed-in user.
// Approving binds it to them: the CLI gets their token, with their role.
func (c *Core) DecideCLILogin(ctx context.Context, a Actor, userCode, clientIP string, approve bool) error {
	l, err := c.cliLoginFor(ctx, a, userCode, clientIP)
	if err != nil {
		return err
	}
	if l.Status != "pending" {
		return conflict("this login was already %s", l.Status)
	}
	status := "denied"
	if approve {
		// Approving makes a token: a step-up, like creating one.
		if err := a.requireRecent("approving a CLI login"); err != nil {
			return err
		}
		if l.Platform {
			// A platform login runs the platform: the approver needs a
			// platform role (ADR 0080), and their sign-in must satisfy
			// the platform's two-factor rule (gateOperator took it away
			// otherwise).
			if err := a.requirePlatform(PlatformView, "approving a platform login"); err != nil {
				return err
			}
		}
		status = "approved"
	}
	// The token carries what the approving sign-in proved: a second factor
	// (an identity provider's sign-in counts as one).
	n, err := c.store.DecideCLILogin(ctx, store.DecideCLILoginParams{Status: status, UserID: &a.UserID, TwoFactor: a.Factor != "", ID: l.ID, ExpiresAt: now()})
	if err != nil {
		return err
	}
	if n == 0 {
		return conflict("this login was decided or expired meanwhile")
	}
	c.cliLogins.notify(l.ID)
	if !approve {
		c.audit(ctx, a, "token.cli_login_denied", nil, nil, "token "+cliTokenName(l.Hostname),
			fmt.Sprintf("denied dock login from %s (%s, %s)", l.Ip, l.Hostname, l.Os))
	}
	return nil
}

// cliLoginFor finds a live login by user code for a signed-in user. Wrong
// codes count against the user and their IP; a few in a row lock both out
// of code lookups for minutes, which makes guessing hopeless (20^8 codes,
// each live for 10 minutes).
func (c *Core) cliLoginFor(ctx context.Context, a Actor, userCode, clientIP string) (store.CliLogin, error) {
	if a.Kind != "user" {
		return store.CliLogin{}, errf(apitypes.CodeForbidden, "only a signed-in user can approve a CLI login, not %s", a)
	}
	t := now()
	keys := []string{"user:" + strconv.FormatInt(a.UserID, 10), "ip:" + httpkit.ClientKey(clientIP)}
	for _, k := range keys {
		if c.cliLogins.fails.Exhausted(k, t) {
			return store.CliLogin{}, errf(apitypes.CodeRateLimited, "too many wrong login codes; wait a few minutes and try again")
		}
	}
	code, ok := parseUserCode(userCode)
	var l store.CliLogin
	var err error
	if ok {
		l, err = c.store.GetCLILoginByUserCode(ctx, code)
	}
	if !ok || store.IsNotFound(err) {
		for _, k := range keys {
			c.cliLogins.fails.Allow(k, t)
		}
		c.log.Warn("cli login: wrong code", "user", a.Email, "ip", clientIP)
		return store.CliLogin{}, notFound("no login with this code; check it against your terminal, or run `dock login` again")
	}
	if err != nil {
		return store.CliLogin{}, err
	}
	if t.After(l.ExpiresAt) {
		return store.CliLogin{}, errCLILoginExpired
	}
	return l, nil
}

// RevokeOwnToken revokes the calling token, for `dock logout`.
func (c *Core) RevokeOwnToken(ctx context.Context, a Actor) error {
	if a.Kind != "token" {
		return invalid("only an API token can revoke itself; sign out of the UI instead")
	}
	if err := c.store.RevokeToken(ctx, store.RevokeTokenParams{RevokedAt: ptr(now()), ID: a.TokenID}); err != nil {
		return err
	}
	c.audit(ctx, a, "token.revoke", nil, nil, "token "+a.Name, fmt.Sprintf("token %q (id %d) revoked itself", a.Name, a.TokenID))
	return nil
}

func cliTokenName(hostname string) string { return "cli: " + hostname }

func newUserCode() string {
	b := make([]byte, 0, userCodeLen)
	buf := make([]byte, 16)
	for len(b) < userCodeLen {
		_, _ = rand.Read(buf)
		for _, x := range buf {
			// 240 is the largest multiple of 20 below 256: no modulo bias.
			if x < 240 && len(b) < userCodeLen {
				b = append(b, userCodeAlphabet[x%byte(len(userCodeAlphabet))])
			}
		}
	}
	return string(b)
}

// formatUserCode is "BCDF-GHJK".
func formatUserCode(code string) string { return code[:4] + "-" + code[4:] }

// parseUserCode accepts a code as typed: any case, with or without the
// dash or spaces.
func parseUserCode(s string) (string, bool) {
	s = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(s))
	if len(s) != userCodeLen || strings.Trim(s, userCodeAlphabet) != "" {
		return "", false
	}
	return s, true
}

// clean makes client-supplied text safe to show: printable, one line,
// at most n bytes.
func clean(s string, n int, fallback string) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s))
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "")
	}
	if s == "" {
		return fallback
	}
	return s
}
