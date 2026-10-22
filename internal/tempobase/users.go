package tempobase

import (
	"context"
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/tempobase/tbstore"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// ListUsers returns everyone. Admin only.
func (a *App) ListUsers(ctx context.Context, act Actor) ([]tbtypes.User, error) {
	if err := act.requireAdmin("managing people"); err != nil {
		return nil, err
	}
	rows, err := a.store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tbtypes.User, len(rows))
	for i, u := range rows {
		out[i] = toUser(u)
	}
	return out, nil
}

// CreateUser creates a person directly, for `tempobase admin
// create-user`: with a password, or with password "" and a returned link
// that sets it.
func (a *App) CreateUser(ctx context.Context, act Actor, email, name string, role tbtypes.Role, password string) (tbtypes.User, *tbtypes.Link, error) {
	if err := act.requireAdmin("managing people"); err != nil {
		return tbtypes.User{}, nil, err
	}
	var link *tbtypes.Link
	u, err := a.createUser(ctx, email, name, role, password, func(q *tbstore.Queries, u tbstore.User) error {
		if password != "" {
			return nil
		}
		l, err := a.createLink(ctx, q, act, tbtypes.LinkReset, u.Email, "", &u.ID, "")
		link = &l
		return err
	})
	if err != nil {
		return tbtypes.User{}, nil, err
	}
	a.audit(ctx, act, "user.create", nil, u.Email, fmt.Sprintf("created %s %s", u.Role, u.Email))
	return toUser(u), link, nil
}

// UpdateUser changes another person's name, role or disabled state.
// Disabling ends their sessions; their tokens stop at once (the token
// lookup checks the user). Admins can't change their own role or disable
// themselves, and the last admin stays an admin.
func (a *App) UpdateUser(ctx context.Context, act Actor, id int64, req tbtypes.UpdateUserRequest) (tbtypes.User, error) {
	if err := act.requireAdmin("managing people"); err != nil {
		return tbtypes.User{}, err
	}
	u, err := a.getUser(ctx, id)
	if err != nil {
		return tbtypes.User{}, err
	}
	var changes []string
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || len(name) > 100 {
			return tbtypes.User{}, invalid("a name of 1-100 characters is required")
		}
		if name != u.Name {
			changes = append(changes, fmt.Sprintf("name %q", name))
		}
		u.Name = name
	}
	wasAdmin := activeAdmin(u)
	if req.Role != nil && string(*req.Role) != u.Role {
		if !validRole(*req.Role) {
			return tbtypes.User{}, invalid("role must be viewer, editor or admin")
		}
		u.Role = string(*req.Role)
		changes = append(changes, "role "+u.Role)
	}
	if req.Disabled != nil && *req.Disabled != (u.DisabledAt != nil) {
		if *req.Disabled {
			u.DisabledAt = ptr(a.now())
			changes = append(changes, "disabled; sessions and tokens ended")
		} else {
			u.DisabledAt = nil
			changes = append(changes, "enabled")
		}
	}
	if len(changes) == 0 {
		return toUser(u), nil
	}
	if u.ID == act.UserID && (req.Role != nil || req.Disabled != nil) {
		return tbtypes.User{}, invalid("you can't change your own role or disable yourself; ask another admin")
	}
	err = a.store.Tx(ctx, func(q *tbstore.Queries) error {
		if wasAdmin && !activeAdmin(u) {
			if err := a.keepAnAdmin(ctx, q); err != nil {
				return err
			}
		}
		if _, err := q.UpdateUser(ctx, tbstore.UpdateUserParams{Name: u.Name, Role: u.Role, DisabledAt: u.DisabledAt, ID: u.ID}); err != nil {
			return err
		}
		if u.DisabledAt != nil {
			return q.DeleteUserSessions(ctx, u.ID)
		}
		return nil
	})
	if err != nil {
		return tbtypes.User{}, err
	}
	a.audit(ctx, act, "user.update", nil, u.Email, "changed "+u.Email+": "+strings.Join(changes, ", "))
	return toUser(u), nil
}

// keepAnAdmin refuses a change, in transaction q, that would leave no
// active admin. Called before the change, so one must remain besides the
// one changing.
func (a *App) keepAnAdmin(ctx context.Context, q *tbstore.Queries) error {
	n, err := q.CountActiveAdmins(ctx)
	if err != nil {
		return err
	}
	if n <= 1 {
		return conflict("that would leave no admin; make someone else an admin first")
	}
	return nil
}

// DeleteUser deletes another person with their sessions, tokens, reset
// links and saved queries. Their audit history and changesets stay.
func (a *App) DeleteUser(ctx context.Context, act Actor, id int64) error {
	if err := act.requireAdmin("managing people"); err != nil {
		return err
	}
	if id == act.UserID {
		return invalid("you can't delete yourself; ask another admin")
	}
	u, err := a.getUser(ctx, id)
	if err != nil {
		return err
	}
	err = a.store.Tx(ctx, func(q *tbstore.Queries) error {
		if activeAdmin(u) {
			if err := a.keepAnAdmin(ctx, q); err != nil {
				return err
			}
		}
		return q.DeleteUser(ctx, u.ID)
	})
	if err != nil {
		return err
	}
	a.audit(ctx, act, "user.delete", nil, u.Email, "deleted "+u.Email+" with their sessions and tokens")
	return nil
}

// activeAdmin reports whether u is an admin who can sign in.
func activeAdmin(u tbstore.User) bool {
	return u.Role == string(tbtypes.RoleAdmin) && u.DisabledAt == nil
}

// getUser loads a person by ID.
func (a *App) getUser(ctx context.Context, id int64) (tbstore.User, error) {
	u, err := a.store.GetUser(ctx, id)
	if tbstore.IsNotFound(err) {
		return u, notFound("person %d not found", id)
	}
	return u, err
}

func toUser(u tbstore.User) tbtypes.User {
	return tbtypes.User{ID: u.ID, Email: u.Email, Name: u.Name, Role: tbtypes.Role(u.Role), Disabled: u.DisabledAt != nil, CreatedAt: u.CreatedAt}
}
