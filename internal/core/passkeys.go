package core

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Passkeys (WebAuthn, ADR 0057) through go-webauthn. Dock is the relying
// party on the host the browser uses, which must be Dock's public address
// or localhost: a passkey is bound to that host, and browsers only offer
// WebAuthn on https:// or localhost. Every challenge is a ceremony
// (ceremonies.go), answered once, by the user and session that asked.

// relyingParty is Dock as the WebAuthn relying party for a browser at
// origin: its host is the RP ID. Only Dock's public address (the exact
// origin) and localhost are accepted, never an IP, so a passkey is never
// bound to a host Dock doesn't own.
func (c *Core) relyingParty(origin string) (*webauthn.WebAuthn, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.Path != "" || u.RawQuery != "" || u.User != nil {
		return nil, invalid("passkeys need the browser's origin; open Dock in a browser")
	}
	host := u.Hostname()
	switch {
	case net.ParseIP(host) != nil:
	case host == "localhost" || strings.HasSuffix(host, ".localhost"):
		return c.webauthnFor(host, origin)
	case host == c.publicHost() && origin == c.publicURL():
		return c.webauthnFor(host, origin)
	}
	if pub := c.publicURL(); pub != "" {
		return nil, invalid("passkeys work on Dock's public address; open %s", pub)
	}
	return nil, invalid("passkeys work on Dock's public address or on localhost; an operator can set the address in the console (`dock address set`)")
}

// webauthnFor is go-webauthn for RP ID rpID, accepting answers from origin
// only.
func (c *Core) webauthnFor(rpID, origin string) (*webauthn.WebAuthn, error) {
	w, err := webauthn.New(&webauthn.Config{RPID: rpID, RPDisplayName: "Dock", RPOrigins: []string{origin}})
	if err != nil {
		return nil, fmt.Errorf("passkeys for %s: %w", origin, err)
	}
	return w, nil
}

// passkeyUser is a user with their passkeys, as go-webauthn sees them.
type passkeyUser struct {
	u     store.User
	creds []webauthn.Credential
	// factors are the user_factors rows of creds, in the same order.
	factors []store.UserFactor
}

func (p *passkeyUser) WebAuthnID() []byte                         { return userHandle(p.u.ID) }
func (p *passkeyUser) WebAuthnName() string                       { return p.u.Email }
func (p *passkeyUser) WebAuthnDisplayName() string                { return cmp.Or(p.u.Name, p.u.Email) }
func (p *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return p.creds }

// userHandle is the WebAuthn user handle of user id: the ID as 8 bytes,
// no personal information.
func userHandle(id int64) []byte { return binary.BigEndian.AppendUint64(nil, uint64(id)) }

// passkeyUser loads u's passkeys.
func (c *Core) passkeyUser(ctx context.Context, u store.User) (*passkeyUser, error) {
	rows, err := c.store.ListUserFactors(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	p := &passkeyUser{u: u}
	for _, f := range rows {
		if f.Kind != factorPasskey {
			continue
		}
		var cred webauthn.Credential
		b, err := c.box.Open(f.Secret, factorAAD(f.UserID, f.Kind, f.CredentialID))
		if err == nil {
			err = json.Unmarshal(b, &cred)
		}
		if err != nil {
			return nil, fmt.Errorf("open passkey %d: %w", f.ID, err)
		}
		p.creds, p.factors = append(p.creds, cred), append(p.factors, f)
	}
	return p, nil
}

// beginPasskey starts an assertion (second factor or step-up) for u in
// session sessionID, or a passwordless sign-in for whoever answers when u
// is nil.
func (c *Core) beginPasskey(ctx context.Context, purpose, origin string, u *store.User, sessionID int64) (apitypes.PasskeyChallenge, error) {
	rp, err := c.relyingParty(origin)
	if err != nil {
		return apitypes.PasskeyChallenge{}, err
	}
	cer := &ceremony{purpose: purpose, sessionID: sessionID, origin: origin}
	var opts *protocol.CredentialAssertion
	var data *webauthn.SessionData
	if u == nil {
		// A passkey alone is both factors: something you have, unlocked by
		// something you know or are.
		opts, data, err = rp.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	} else {
		var pu *passkeyUser
		if pu, err = c.passkeyUser(ctx, *u); err != nil {
			return apitypes.PasskeyChallenge{}, err
		}
		if len(pu.creds) == 0 {
			return apitypes.PasskeyChallenge{}, invalid("you have no passkey; use your authenticator app")
		}
		cer.userID = u.ID
		opts, data, err = rp.BeginLogin(pu, webauthn.WithUserVerification(protocol.VerificationPreferred))
	}
	if err != nil {
		return apitypes.PasskeyChallenge{}, fmt.Errorf("begin passkey %s: %w", purpose, err)
	}
	cer.webauthn = *data
	return c.challenge(cer, opts.Response)
}

// challenge stores cer and returns it with options for the browser.
func (c *Core) challenge(cer *ceremony, options any) (apitypes.PasskeyChallenge, error) {
	b, err := json.Marshal(options)
	if err != nil {
		return apitypes.PasskeyChallenge{}, err
	}
	id, err := c.ceremonies.put(cer)
	if err != nil {
		return apitypes.PasskeyChallenge{}, err
	}
	return apitypes.PasskeyChallenge{Ceremony: id, Options: b}, nil
}

var errPasskeyRejected = errf(apitypes.CodeUnauthorized, "the passkey was not accepted; try again or use another factor")

// checkPasskey verifies an assertion answering ceremony purpose for u in
// session sessionID, and stores the passkey's new sign count.
func (c *Core) checkPasskey(ctx context.Context, purpose string, u store.User, sessionID int64, res *apitypes.PasskeyResponse) error {
	cer, err := c.ceremonies.take(res.Ceremony, purpose, u.ID, sessionID)
	if err != nil {
		return err
	}
	rp, err := c.relyingParty(cer.origin)
	if err != nil {
		return err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(res.Credential)
	if err != nil {
		c.log.Warn("passkey: malformed answer", "user", u.Email, "err", describeWebAuthn(err))
		return errPasskeyRejected
	}
	pu, err := c.passkeyUser(ctx, u)
	if err != nil {
		return err
	}
	cred, err := rp.ValidateLogin(pu, cer.webauthn, parsed)
	if err != nil {
		c.log.Warn("passkey: rejected", "user", u.Email, "err", describeWebAuthn(err))
		return errPasskeyRejected
	}
	return c.usePasskey(ctx, pu, cred)
}

// usePasskey stores cred after a sign-in with it, unless its sign count
// went backwards (a cloned authenticator: refused and audited) or another
// sign-in stored a newer one since pu was read.
func (c *Core) usePasskey(ctx context.Context, pu *passkeyUser, cred *webauthn.Credential) error {
	if cred.Authenticator.CloneWarning {
		c.log.Warn("passkey: sign count went backwards; refusing a possibly cloned authenticator", "user", pu.u.Email)
		c.audit(ctx, userActor(pu.u), "user.passkey_cloned", nil, nil, pu.u.Email, "refused a passkey whose sign count went backwards (a cloned authenticator?)")
		return errPasskeyRejected
	}
	i := -1
	for j, x := range pu.creds {
		if bytes.Equal(x.ID, cred.ID) {
			i = j
		}
	}
	if i < 0 {
		return errPasskeyRejected
	}
	b, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	sealed, err := c.box.Seal(b, factorAAD(pu.u.ID, factorPasskey, cred.ID))
	if err != nil {
		return err
	}
	f := pu.factors[i]
	n, err := c.store.UsePasskey(ctx, store.UsePasskeyParams{Secret: sealed, Now: ptr(now()), ID: f.ID, Old: f.Secret})
	if err != nil {
		return err
	}
	if n == 0 { // another sign-in with this passkey stored its count first
		return errPasskeyRejected
	}
	return nil
}

// describeWebAuthn is a go-webauthn error with its details, for the log.
func describeWebAuthn(err error) string {
	if pe, ok := errors.AsType[*protocol.Error](err); ok {
		return fmt.Sprintf("%s: %s", pe.Details, pe.DevInfo)
	}
	return err.Error()
}

// PasskeyOptions starts a passkey sign-in: purpose "login" signs in with a
// passkey alone, "second" finishes the password sign-in pending in
// pendingSecret. Unauthenticated, so it spends from the client IP's
// sign-in budget.
func (c *Core) PasskeyOptions(ctx context.Context, req apitypes.PasskeyOptionsRequest, pendingSecret, origin, clientIP string) (apitypes.PasskeyChallenge, error) {
	if !c.loginLimit.AllowIP(clientIP, now()) {
		return apitypes.PasskeyChallenge{}, errf(apitypes.CodeRateLimited, "too many sign-ins started from %s; wait a minute and try again", clientIP)
	}
	switch req.Purpose {
	case ceremonyLogin:
		return c.beginPasskey(ctx, ceremonyLogin, origin, nil, 0)
	case ceremonySecond:
		s, err := c.pendingSession(ctx, pendingSecret)
		if err != nil {
			return apitypes.PasskeyChallenge{}, err
		}
		return c.beginPasskey(ctx, ceremonySecond, origin, &s.User, s.Session.ID)
	}
	return apitypes.PasskeyChallenge{}, invalid("purpose must be login or second")
}

// PasskeyLogin signs in with a passkey alone: a discoverable credential
// with user verification (something the person has, plus their PIN or
// biometrics), whose user handle names the passkey's own user. The
// session counts as two-factor.
func (c *Core) PasskeyLogin(ctx context.Context, res apitypes.PasskeyResponse, clientIP string) (LoginResult, error) {
	cer, err := c.ceremonies.take(res.Ceremony, ceremonyLogin, 0, 0)
	if err != nil {
		return LoginResult{}, err
	}
	rp, err := c.relyingParty(cer.origin)
	if err != nil {
		return LoginResult{}, err
	}
	var pu *passkeyUser
	parsed, err := protocol.ParseCredentialRequestResponseBytes(res.Credential)
	if err == nil {
		var cred *webauthn.Credential
		cred, err = rp.ValidateDiscoverableLogin(func(rawID, handle []byte) (webauthn.User, error) {
			row, err := c.store.GetPasskeyByCredentialID(ctx, rawID)
			if err != nil {
				return nil, fmt.Errorf("unknown passkey: %w", err)
			}
			if !bytes.Equal(handle, userHandle(row.User.ID)) {
				return nil, errors.New("the user handle doesn't match the passkey's user")
			}
			if pu, err = c.passkeyUser(ctx, row.User); err != nil {
				return nil, err
			}
			return pu, nil
		}, cer.webauthn, parsed)
		if err == nil {
			return c.finishPasskeyLogin(ctx, pu, cred, clientIP)
		}
	}
	c.log.Warn("passkey sign-in rejected", "ip", clientIP, "err", describeWebAuthn(err))
	c.audit(ctx, Actor{Kind: "user", Name: "unknown"}, "user.login_failed", nil, nil, "passkey", "rejected a passkey sign-in from "+clientIP)
	return LoginResult{}, errPasskeyRejected
}

// finishPasskeyLogin signs in pu with the passkey cred they answered with,
// spending from their sign-in budget like a password.
func (c *Core) finishPasskeyLogin(ctx context.Context, pu *passkeyUser, cred *webauthn.Credential, clientIP string) (LoginResult, error) {
	u := pu.u
	if !c.loginLimit.Allow(u.Email, clientIP, now()) {
		return LoginResult{}, errTooManyFactors
	}
	if u.DisabledAt != nil {
		return LoginResult{}, errf(apitypes.CodeUnauthorized, "this account is disabled")
	}
	if err := c.usePasskey(ctx, pu, cred); err != nil {
		return LoginResult{}, err
	}
	return c.signIn(ctx, u, factorPasskey, clientIP)
}

// PasskeyRegistration starts adding a passkey to the caller's account;
// AddPasskey keeps the browser's answer.
func (c *Core) PasskeyRegistration(ctx context.Context, a Actor, origin string) (apitypes.PasskeyChallenge, error) {
	u, err := c.sessionUser(ctx, a, "adding a passkey")
	if err != nil {
		return apitypes.PasskeyChallenge{}, err
	}
	rp, err := c.relyingParty(origin)
	if err != nil {
		return apitypes.PasskeyChallenge{}, err
	}
	pu, err := c.passkeyUser(ctx, u)
	if err != nil {
		return apitypes.PasskeyChallenge{}, err
	}
	exclude := make([]protocol.CredentialDescriptor, len(pu.creds))
	for i, cr := range pu.creds {
		exclude[i] = cr.Descriptor()
	}
	opts, data, err := rp.BeginRegistration(pu,
		webauthn.WithExclusions(exclude),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementPreferred, UserVerification: protocol.VerificationPreferred,
		}),
	)
	if err != nil {
		return apitypes.PasskeyChallenge{}, fmt.Errorf("begin passkey registration: %w", err)
	}
	return c.challenge(&ceremony{purpose: ceremonyRegister, userID: u.ID, sessionID: a.SessionID, origin: origin, webauthn: *data}, opts.Response)
}

// AddPasskey registers the passkey answering a PasskeyRegistration.
func (c *Core) AddPasskey(ctx context.Context, a Actor, req apitypes.AddPasskeyRequest) (apitypes.FactorAdded, error) {
	u, err := c.sessionUser(ctx, a, "adding a passkey")
	if err != nil {
		return apitypes.FactorAdded{}, err
	}
	cer, err := c.ceremonies.take(req.Passkey.Ceremony, ceremonyRegister, u.ID, a.SessionID)
	if err != nil {
		return apitypes.FactorAdded{}, err
	}
	rp, err := c.relyingParty(cer.origin)
	if err != nil {
		return apitypes.FactorAdded{}, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(req.Passkey.Credential)
	if err != nil {
		return apitypes.FactorAdded{}, invalid("the browser's passkey answer is malformed; try again")
	}
	pu, err := c.passkeyUser(ctx, u)
	if err != nil {
		return apitypes.FactorAdded{}, err
	}
	cred, err := rp.CreateCredential(pu, cer.webauthn, parsed)
	if err != nil {
		c.log.Warn("passkey registration rejected", "user", u.Email, "err", describeWebAuthn(err))
		return apitypes.FactorAdded{}, invalid("the passkey was not accepted; try again")
	}
	b, err := json.Marshal(cred)
	if err != nil {
		return apitypes.FactorAdded{}, err
	}
	sealed, err := c.box.Seal(b, factorAAD(u.ID, factorPasskey, cred.ID))
	if err != nil {
		return apitypes.FactorAdded{}, err
	}
	return c.addFactor(ctx, a, u, store.CreateUserFactorParams{
		UserID: u.ID, Kind: factorPasskey, Name: clean(req.Name, 64, "Passkey"), CredentialID: cred.ID, Secret: sealed, CreatedAt: now(),
	}, 0)
}
