package tempobase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbstore"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// API tokens are agents (ADR 0024): each person creates their own, shown
// once and stored hashed. A token follows the policy's tokens rule or its
// own data access, capped by its owner's current role, and may be limited
// to some connections. It never manages anything.

// tokenPrefixLen is how much of a secret is kept for display.
const tokenPrefixLen = len(tokenPrefix) + 6

// ListTokens returns the caller's tokens, every token for admins, or only
// itself for a token.
func (a *App) ListTokens(ctx context.Context, act Actor) ([]tbtypes.Token, error) {
	type row struct {
		t     tbstore.Token
		email string
		conns string
	}
	var rows []row
	switch {
	case act.Kind == "token":
		r, err := a.store.GetToken(ctx, act.TokenID)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row{r.Token, r.UserEmail, r.ConnectionNames})
	case act.IsAdmin():
		rs, err := a.store.ListTokens(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			rows = append(rows, row{r.Token, r.UserEmail, r.ConnectionNames})
		}
	default:
		rs, err := a.store.ListUserTokens(ctx, act.UserID)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			rows = append(rows, row{r.Token, r.UserEmail, r.ConnectionNames})
		}
	}
	out := make([]tbtypes.Token, len(rows))
	for i, r := range rows {
		out[i] = toToken(r.t, r.email, r.conns)
	}
	return out, nil
}

// CreateToken creates a token for the signed-in person and returns its
// secret, shown only once. Its connection limit may only name connections
// the person sees.
func (a *App) CreateToken(ctx context.Context, act Actor, req tbtypes.CreateTokenRequest) (tbtypes.CreatedToken, error) {
	if !act.isUser() {
		return tbtypes.CreatedToken{}, forbidden("tokens are created by people; sign in to create one")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 100 {
		return tbtypes.CreatedToken{}, invalid("a token name of 1-100 characters is required")
	}
	if err := datapanel.ValidateTokenAccess(req.Data); err != nil {
		return tbtypes.CreatedToken{}, err
	}
	if req.ExpiresInDays < 0 || req.ExpiresInDays > 3650 {
		return tbtypes.CreatedToken{}, invalid("expires_in_days must be 0 (never) to 3650")
	}
	ids, err := a.visibleConnectionIDs(ctx, act, req.Connections)
	if err != nil {
		return tbtypes.CreatedToken{}, err
	}
	secret, hash := newSecret(tokenPrefix)
	n := a.now()
	params := tbstore.CreateTokenParams{Name: name, Prefix: secret[:tokenPrefixLen], SecretHash: hash, UserID: act.UserID, CreatedAt: n}
	params.DataProduction, params.DataNonProduction, params.DataSensitive = tokenDataColumns(req.Data)
	if req.ExpiresInDays > 0 {
		params.ExpiresAt = ptr(n.Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour))
	}
	var t tbstore.Token
	err = a.store.Tx(ctx, func(q *tbstore.Queries) error {
		var err error
		if t, err = q.CreateToken(ctx, params); err != nil {
			return err
		}
		for _, id := range ids {
			if err := q.AddTokenConnection(ctx, tbstore.AddTokenConnectionParams{TokenID: t.ID, ConnectionID: id}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return tbtypes.CreatedToken{}, err
	}
	limit := "every connection the policy allows"
	if len(req.Connections) > 0 {
		limit = strings.Join(req.Connections, ", ")
	}
	a.audit(ctx, act, "token.create", nil, "token "+t.Name,
		fmt.Sprintf("created token %q (id %d) for %s; %s", t.Name, t.ID, limit, datapanel.DescribeTokenAccess(req.Data)))
	return tbtypes.CreatedToken{Token: toToken(t, act.Email, strings.Join(req.Connections, ",")), Secret: secret}, nil
}

// visibleConnectionIDs resolves connection names to IDs, refusing names
// act can't see (one query for the connections, one for the rules).
func (a *App) visibleConnectionIDs(ctx context.Context, act Actor, names []string) ([]int64, error) {
	if len(names) == 0 {
		return nil, nil
	}
	conns, err := a.store.ListConnections(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := a.store.ListAllPolicyRules(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]tbstore.Connection, len(conns))
	for _, c := range conns {
		byName[c.Name] = c
	}
	ids := make([]int64, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		c, ok := byName[n]
		if !ok || !visible(act, c, permission(act, c, rulesFor(rules, c.ID))) {
			return nil, notFound("connection %q not found", n)
		}
		if !seen[n] {
			seen[n] = true
			ids = append(ids, c.ID)
		}
	}
	return ids, nil
}

// UpdateToken changes a token's own data access; null follows the
// policy's tokens rule again. Its owner or an admin.
func (a *App) UpdateToken(ctx context.Context, act Actor, id int64, req apitypes.UpdateTokenRequest) (tbtypes.Token, error) {
	r, err := a.manageableToken(ctx, act, id)
	if err != nil {
		return tbtypes.Token{}, err
	}
	if r.Token.RevokedAt != nil {
		return tbtypes.Token{}, invalid("token %q is revoked; create a new one", r.Token.Name)
	}
	if err := datapanel.ValidateTokenAccess(req.Data); err != nil {
		return tbtypes.Token{}, err
	}
	t := r.Token
	t.DataProduction, t.DataNonProduction, t.DataSensitive = tokenDataColumns(req.Data)
	if err := a.store.SetTokenData(ctx, tbstore.SetTokenDataParams{
		DataProduction: t.DataProduction, DataNonProduction: t.DataNonProduction, DataSensitive: t.DataSensitive, ID: t.ID,
	}); err != nil {
		return tbtypes.Token{}, err
	}
	a.audit(ctx, act, apitypes.AuditDataPolicy, nil, "token "+t.Name, fmt.Sprintf("token %q (id %d) %s", t.Name, t.ID, datapanel.DescribeTokenAccess(req.Data)))
	return toToken(t, r.UserEmail, r.ConnectionNames), nil
}

// RevokeToken revokes a token at once. Its owner or an admin.
func (a *App) RevokeToken(ctx context.Context, act Actor, id int64) error {
	r, err := a.manageableToken(ctx, act, id)
	if err != nil {
		return err
	}
	if _, err := a.store.RevokeToken(ctx, tbstore.RevokeTokenParams{RevokedAt: ptr(a.now()), ID: id}); err != nil {
		return err
	}
	a.audit(ctx, act, "token.revoke", nil, "token "+r.Token.Name, fmt.Sprintf("revoked token %q (id %d) of %s", r.Token.Name, id, r.UserEmail))
	return nil
}

// manageableToken loads a token its owner or an admin manages; to anyone
// else it doesn't exist. Tokens manage nothing.
func (a *App) manageableToken(ctx context.Context, act Actor, id int64) (tbstore.GetTokenRow, error) {
	if !act.isUser() && !act.IsAdmin() {
		return tbstore.GetTokenRow{}, forbidden("tokens can't manage tokens; sign in")
	}
	r, err := a.store.GetToken(ctx, id)
	if tbstore.IsNotFound(err) || err == nil && !act.IsAdmin() && r.Token.UserID != act.UserID {
		return r, notFound("token %d not found", id)
	}
	return r, err
}

// tokenData is a token's own data access, nil when it follows the
// policy.
func tokenData(t tbstore.Token) *apitypes.DataTokenAccess {
	if t.DataProduction == nil || t.DataNonProduction == nil || t.DataSensitive == nil {
		return nil
	}
	return &apitypes.DataTokenAccess{
		Production: apitypes.DataLevel(*t.DataProduction), NonProduction: apitypes.DataLevel(*t.DataNonProduction),
		Sensitive: apitypes.DataSensitivity(*t.DataSensitive),
	}
}

// tokenDataColumns are the stored columns of a token's own data access.
func tokenDataColumns(d *apitypes.DataTokenAccess) (prod, nonProd, sensitive *string) {
	if d == nil {
		return nil, nil, nil
	}
	return ptr(string(d.Production)), ptr(string(d.NonProduction)), ptr(string(d.Sensitive))
}

// toToken converts a token with its owner's email and its connection
// limit (comma-separated names).
func toToken(t tbstore.Token, email, conns string) tbtypes.Token {
	out := tbtypes.Token{
		ID: t.ID, Name: t.Name, Prefix: t.Prefix, User: email, Connections: []string{}, Data: tokenData(t),
		CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt, RevokedAt: t.RevokedAt,
	}
	if conns != "" {
		out.Connections = strings.Split(conns, ",")
	}
	return out
}
