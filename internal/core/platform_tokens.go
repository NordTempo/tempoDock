package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Platform tokens (ADR 0080): credentials that run the platform with a
// platform role and never act in an org. Operators make them in the
// console; `dock login --platform` makes one for its user. Tokens from
// before ADR 0080 that are both an org's and the platform's (operator
// tokens, operators' `dock login` tokens) keep working as legacy tokens
// until an operator splits them or they expire.

// platformTokenPrefix starts a platform token's secret, so a person (and
// the CLI) can tell it from an org's token at a glance.
const platformTokenPrefix = tokenPrefix + "p_"

// CreatePlatformToken makes a platform token with req.Role. A person with
// accounts.manage, recently signed in: a platform credential is as
// sensitive as an operator account.
func (c *Core) CreatePlatformToken(ctx context.Context, a Actor, req apitypes.CreatePlatformTokenRequest) (apitypes.CreatedToken, error) {
	const action = "creating platform tokens"
	if err := a.requireAccountManager(action); err != nil {
		return apitypes.CreatedToken{}, err
	}
	if err := a.requireRecent(action); err != nil {
		return apitypes.CreatedToken{}, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return apitypes.CreatedToken{}, invalid("a platform token needs a name")
	}
	if !validPlatformRole(req.Role) {
		return apitypes.CreatedToken{}, invalid("platform role %q isn't one; use operator or viewer", req.Role)
	}
	role := string(req.Role)
	params := store.CreateTokenParams{Name: name, Scope: string(apitypes.ScopeRead), Platform: &role, CreatedBy: a.Name}
	if a.UserID != 0 {
		params.UserID = &a.UserID
	}
	if req.ExpiresInDays > 0 {
		params.ExpiresAt = ptr(now().Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour))
	}
	secret, hash := newSecret(platformTokenPrefix)
	params.Hash = hash
	t, err := c.store.CreateToken(ctx, params)
	if err != nil {
		return apitypes.CreatedToken{}, err
	}
	c.audit(ctx, a.platform(), "token.create", nil, nil, "token "+t.Name,
		fmt.Sprintf("created platform token %q (id %d) with platform role %s", t.Name, t.ID, role))
	return apitypes.CreatedToken{Token: toToken(ctx, newNames(c.store), t), Secret: secret}, nil
}

// ListPlatformTokens lists the platform's tokens, platform logins and the
// legacy tokens that are both an org's and the platform's, newest first.
func (c *Core) ListPlatformTokens(ctx context.Context, a Actor) ([]apitypes.Token, error) {
	if err := a.requirePlatform(PlatformView, "listing platform tokens"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListPlatformTokens(ctx)
	if err != nil {
		return nil, err
	}
	names := newNames(c.store)
	out := make([]apitypes.Token, len(rows))
	for i, r := range rows {
		out[i] = toToken(ctx, names, r.Token)
		out[i].Org, out[i].User = deref(r.OrgSlug), deref(r.UserEmail)
	}
	return out, nil
}

// RevokePlatformToken revokes a platform token, platform login or legacy
// token: accounts.manage, as a person. A platform login's owner revokes
// their own with RevokeToken, like any of their tokens.
func (c *Core) RevokePlatformToken(ctx context.Context, a Actor, id int64) error {
	const action = "revoking platform tokens"
	if err := a.requireAccountManager(action); err != nil {
		return err
	}
	t, err := c.platformToken(ctx, id)
	if err != nil {
		return err
	}
	n := now()
	if err := c.store.RevokeToken(ctx, store.RevokeTokenParams{RevokedAt: &n, ID: id}); err != nil {
		return err
	}
	c.endOrphanedRequests(ctx)
	c.audit(ctx, a.platform(), "token.revoke", nil, nil, "token "+t.Name, fmt.Sprintf("revoked platform token %q (id %d)", t.Name, t.ID))
	return nil
}

// SplitToken splits a legacy org token that also runs the platform: it
// stays the org's token (with its scope and limits) and stops running
// the platform, and a new platform token with the operator role takes
// that over. The new one's secret is shown once. A legacy `dock login`
// has no such split: its person runs `dock login --platform`, and it
// stops running the platform when it is split (or expires).
func (c *Core) SplitToken(ctx context.Context, a Actor, id int64) (apitypes.SplitTokenResult, error) {
	const action = "splitting legacy tokens"
	if err := a.requireAccountManager(action); err != nil {
		return apitypes.SplitTokenResult{}, err
	}
	if err := a.requireRecent(action); err != nil {
		return apitypes.SplitTokenResult{}, err
	}
	t, err := c.platformToken(ctx, id)
	if err != nil {
		return apitypes.SplitTokenResult{}, err
	}
	if !t.Operator {
		return apitypes.SplitTokenResult{}, invalid("token %q isn't a legacy token: only tokens that are both an org's and the platform's are split", t.Name)
	}
	var out apitypes.SplitTokenResult
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.SetTokenOperator(ctx, store.SetTokenOperatorParams{Operator: false, ID: t.ID}); err != nil {
			return err
		}
		t.Operator = false
		out.Token = toToken(ctx, newNames(c.store), t)
		if t.Origin == "cli_login" {
			return nil // its person logs in to the platform themselves
		}
		role := string(apitypes.PlatformOperator)
		secret, hash := newSecret(platformTokenPrefix)
		nt, err := q.CreateToken(ctx, store.CreateTokenParams{Name: t.Name + "-platform", Hash: hash, Scope: string(apitypes.ScopeRead),
			Platform: &role, CreatedBy: a.Name, UserID: t.UserID, ExpiresAt: t.ExpiresAt})
		if err != nil {
			return err
		}
		out.Platform = apitypes.CreatedToken{Token: toToken(ctx, newNames(c.store), nt), Secret: secret}
		return nil
	})
	if err != nil {
		return apitypes.SplitTokenResult{}, err
	}
	summary := fmt.Sprintf("split legacy token %q (id %d): it stays the org's, and no longer runs the platform", t.Name, t.ID)
	if out.Platform.Secret != "" {
		summary += fmt.Sprintf("; platform token %q (id %d) does", out.Platform.Token.Name, out.Platform.Token.ID)
	}
	c.audit(ctx, a.platform(), "token.split", nil, nil, "token "+t.Name, summary)
	return out, nil
}

// platformToken loads a token the platform's token list shows: a
// platform token or a legacy one, not revoked.
func (c *Core) platformToken(ctx context.Context, id int64) (store.Token, error) {
	t, err := c.store.GetToken(ctx, id)
	if store.IsNotFound(err) || err == nil && (t.RevokedAt != nil || t.Platform == nil && !t.Operator) {
		return store.Token{}, notFound("platform token %d not found; `dock platform token list` lists them", id)
	}
	return t, err
}
