package tempobase

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbstore"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// Invites and password resets are links an admin copies to the person
// (there is no email): <base>/link#<code>, single use. The fragment keeps
// the code out of proxy logs.
const (
	InviteTTL = 7 * 24 * time.Hour
	ResetTTL  = 24 * time.Hour
)

// LinkURL is where a link's code is used.
func LinkURL(base, code string) string { return strings.TrimSuffix(base, "/") + "/link#" + code }

// createLink stores a link in q and returns it with its URL; base is the
// server's origin.
func (a *App) createLink(ctx context.Context, q *tbstore.Queries, act Actor, kind tbtypes.LinkKind, email string, role tbtypes.Role, userID *int64, base string) (tbtypes.Link, error) {
	code, hash := newSecret(linkPrefix)
	n := a.now()
	ttl := InviteTTL
	var rolep *string
	if kind == tbtypes.LinkReset {
		ttl = ResetTTL
	} else {
		rolep = ptr(string(role))
	}
	l, err := q.CreateLink(ctx, tbstore.CreateLinkParams{
		Kind: string(kind), Email: email, Role: rolep, UserID: userID, SecretHash: hash,
		CreatedBy: act.Name, CreatedAt: n, ExpiresAt: n.Add(ttl),
	})
	if err != nil {
		return tbtypes.Link{}, err
	}
	out := toLink(l)
	out.URL = LinkURL(base, code)
	return out, nil
}

// CreateInvite invites a person: they open the link, choose a name and
// password, and are signed in with role. Admin only.
func (a *App) CreateInvite(ctx context.Context, act Actor, req tbtypes.CreateInviteRequest, base string) (tbtypes.Link, error) {
	if err := act.requireAdmin("inviting people"); err != nil {
		return tbtypes.Link{}, err
	}
	email := strings.TrimSpace(req.Email)
	switch {
	case !validEmail(email):
		return tbtypes.Link{}, invalid("email %q is not valid", req.Email)
	case !validRole(req.Role):
		return tbtypes.Link{}, invalid("role must be viewer, editor or admin")
	}
	if _, err := a.store.GetUserByEmail(ctx, email); err == nil {
		return tbtypes.Link{}, conflict("%s already has an account; send a reset link if they lost their password", email)
	} else if !tbstore.IsNotFound(err) {
		return tbtypes.Link{}, err
	}
	l, err := a.createLink(ctx, a.store.Queries, act, tbtypes.LinkInvite, email, req.Role, nil, base)
	if err != nil {
		return tbtypes.Link{}, err
	}
	a.audit(ctx, act, "link.create", nil, email, "invited "+email+" as "+string(req.Role))
	return l, nil
}

// ListInvites returns the invites not used yet, newest first, without
// their URLs. Admin only.
func (a *App) ListInvites(ctx context.Context, act Actor) ([]tbtypes.Link, error) {
	if err := act.requireAdmin("managing people"); err != nil {
		return nil, err
	}
	rows, err := a.store.ListOpenInvites(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tbtypes.Link, len(rows))
	for i, l := range rows {
		out[i] = toLink(l)
	}
	return out, nil
}

// DeleteInvite withdraws an invite not used yet. Admin only.
func (a *App) DeleteInvite(ctx context.Context, act Actor, id int64) error {
	if err := act.requireAdmin("managing people"); err != nil {
		return err
	}
	n, err := a.store.DeleteInvite(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("open invite %d not found", id)
	}
	a.audit(ctx, act, "link.delete", nil, "invite", "withdrew invite "+strconv.FormatInt(id, 10))
	return nil
}

// ResetLink creates a password reset link for a person, replacing their
// open ones. Admin only.
func (a *App) ResetLink(ctx context.Context, act Actor, userID int64, base string) (tbtypes.Link, error) {
	if err := act.requireAdmin("resetting passwords"); err != nil {
		return tbtypes.Link{}, err
	}
	u, err := a.getUser(ctx, userID)
	if err != nil {
		return tbtypes.Link{}, err
	}
	return a.resetLink(ctx, act, u, base)
}

// ResetLinkByEmail is ResetLink for `tempobase admin reset-link`.
func (a *App) ResetLinkByEmail(ctx context.Context, act Actor, email, base string) (tbtypes.Link, error) {
	if err := act.requireAdmin("resetting passwords"); err != nil {
		return tbtypes.Link{}, err
	}
	u, err := a.store.GetUserByEmail(ctx, strings.TrimSpace(email))
	if tbstore.IsNotFound(err) {
		return tbtypes.Link{}, notFound("nobody has the email %s", email)
	}
	if err != nil {
		return tbtypes.Link{}, err
	}
	return a.resetLink(ctx, act, u, base)
}

func (a *App) resetLink(ctx context.Context, act Actor, u tbstore.User, base string) (tbtypes.Link, error) {
	var l tbtypes.Link
	err := a.store.Tx(ctx, func(q *tbstore.Queries) error {
		if err := q.DeleteUserResetLinks(ctx, &u.ID); err != nil {
			return err
		}
		var err error
		l, err = a.createLink(ctx, q, act, tbtypes.LinkReset, u.Email, "", &u.ID, base)
		return err
	})
	if err != nil {
		return tbtypes.Link{}, err
	}
	a.audit(ctx, act, "link.create", nil, u.Email, "created a password reset link for "+u.Email)
	return l, nil
}

// openLink loads a link by its code, refusing used and expired ones.
func (a *App) openLink(ctx context.Context, code string) (tbstore.Link, error) {
	if !strings.HasPrefix(code, linkPrefix) {
		return tbstore.Link{}, notFound("this link is not valid; ask an admin for a new one")
	}
	l, err := a.store.GetLinkByHash(ctx, hashSecret(code))
	if tbstore.IsNotFound(err) {
		return l, notFound("this link is not valid; ask an admin for a new one")
	}
	if err != nil {
		return l, err
	}
	if l.UsedAt != nil || !a.now().Before(l.ExpiresAt) {
		return l, errf(apitypes.CodeExpired, "this link was used or has expired; ask an admin for a new one")
	}
	return l, nil
}

// LinkInfo is what a link's page shows before it is used.
func (a *App) LinkInfo(ctx context.Context, code string) (tbtypes.LinkInfo, error) {
	l, err := a.openLink(ctx, code)
	if err != nil {
		return tbtypes.LinkInfo{}, err
	}
	return tbtypes.LinkInfo{Kind: tbtypes.LinkKind(l.Kind), Email: l.Email, ExpiresAt: l.ExpiresAt}, nil
}

// UseLink accepts an invite (creating the person) or sets a new password
// from a reset link (ending the person's sessions), and signs them in.
func (a *App) UseLink(ctx context.Context, code string, req tbtypes.UseLinkRequest, clientIP string) (secret string, expires time.Time, act Actor, err error) {
	if err := a.allow("", clientIP); err != nil {
		return "", time.Time{}, Actor{}, err
	}
	l, err := a.openLink(ctx, code)
	if err != nil {
		return "", time.Time{}, Actor{}, err
	}
	// use spends the link in the same transaction as what it does.
	use := func(q *tbstore.Queries) error {
		n, err := q.UseLink(ctx, tbstore.UseLinkParams{Now: ptr(a.now()), ID: l.ID})
		if err == nil && n == 0 {
			err = errf(apitypes.CodeExpired, "this link was used or has expired; ask an admin for a new one")
		}
		return err
	}
	var u tbstore.User
	switch tbtypes.LinkKind(l.Kind) {
	case tbtypes.LinkInvite:
		role := tbtypes.RoleViewer
		if l.Role != nil {
			role = tbtypes.Role(*l.Role)
		}
		u, err = a.createUser(ctx, l.Email, req.Name, role, req.Password, func(q *tbstore.Queries, u tbstore.User) error {
			if err := use(q); err != nil {
				return err
			}
			secret, expires, err = a.newSession(ctx, q, u.ID)
			return err
		})
	default:
		var hash string
		if hash, err = a.hashPassword(ctx, req.Password); err != nil {
			break
		}
		err = a.store.Tx(ctx, func(q *tbstore.Queries) error {
			if l.UserID == nil {
				return notFound("this link's person no longer exists")
			}
			var err error
			if u, err = q.GetUser(ctx, *l.UserID); err != nil {
				return err
			}
			if u.DisabledAt != nil {
				return errf(apitypes.CodeUnauthorized, "this account is disabled; ask an admin")
			}
			if err := use(q); err != nil {
				return err
			}
			if err := q.SetUserPassword(ctx, tbstore.SetUserPasswordParams{PasswordHash: hash, ID: u.ID}); err != nil {
				return err
			}
			if err := q.DeleteUserSessions(ctx, u.ID); err != nil {
				return err
			}
			secret, expires, err = a.newSession(ctx, q, u.ID)
			return err
		})
	}
	if err != nil {
		return "", time.Time{}, Actor{}, err
	}
	act = userActor(u)
	summary := "accepted an invite as " + u.Role
	if l.Kind == string(tbtypes.LinkReset) {
		summary = "set a new password with a reset link; other sessions ended"
	}
	a.audit(ctx, act, "link.use", nil, u.Email, summary)
	return secret, expires, act, nil
}

func toLink(l tbstore.Link) tbtypes.Link {
	out := tbtypes.Link{ID: l.ID, Kind: tbtypes.LinkKind(l.Kind), Email: l.Email, CreatedBy: l.CreatedBy, CreatedAt: l.CreatedAt, ExpiresAt: l.ExpiresAt}
	if l.Role != nil {
		out.Role = tbtypes.Role(*l.Role)
	}
	return out
}
