package tempobase

import (
	"slices"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// Actor is who calls: a signed-in person, an API token (an agent acting
// in its own right, capped by its owner's role) or the system (the admin
// commands). The HTTP layer authenticates it once per request.
type Actor struct {
	// Kind is "user", "token" or "system".
	Kind string
	// Name is the person's email or the token's name.
	Name string
	// UserID and Email are the person, or the token's owner.
	UserID int64
	Email  string
	// Role is the person's role, or the token owner's current role.
	Role tbtypes.Role
	// TokenID is set for tokens.
	TokenID int64
	// TokenData is a token's own data access; nil follows the tokens rule.
	TokenData *apitypes.DataTokenAccess
	// TokenConnections limits a token to these connection IDs; nil: no
	// limit. TokenConnectionNames are their names, for messages and the
	// MCP tools' default connection.
	TokenConnections     []int64
	TokenConnectionNames []string
}

// System is the actor of `tempobase admin` commands.
var System = Actor{Kind: "system", Name: "system", Role: tbtypes.RoleAdmin}

// IsAdmin reports whether a is an admin person or the system. Tokens are
// never admins.
func (a Actor) IsAdmin() bool { return a.Kind != "token" && a.Role == tbtypes.RoleAdmin }

// isUser reports whether a is a signed-in person.
func (a Actor) isUser() bool { return a.Kind == "user" }

// requireAdmin refuses everyone but admins.
func (a Actor) requireAdmin(action string) error {
	if a.IsAdmin() {
		return nil
	}
	return forbidden("%s needs an admin; ask one", action)
}

// mayUse reports whether a token's connection limit allows connection id.
func (a Actor) mayUse(id int64) bool {
	return a.TokenConnections == nil || slices.Contains(a.TokenConnections, id)
}

// panelActor is a as the data panel sees it. Tokens are agents: their
// queries, history and changesets are their own, not their owner's.
func (a Actor) panelActor() datapanel.Actor {
	pa := datapanel.Actor{Kind: a.Kind, Name: a.Name, TokenID: a.TokenID}
	if a.isUser() {
		pa.UserID = a.UserID
	}
	return pa
}

// auditIDs are the user and token IDs an audit event records for a.
func (a Actor) auditIDs() (user, token *int64) {
	switch a.Kind {
	case "user":
		return ptr(a.UserID), nil
	case "token":
		return nil, ptr(a.TokenID)
	}
	return nil, nil
}

// validRole reports whether r is a tempoBase role.
func validRole(r tbtypes.Role) bool {
	return r == tbtypes.RoleViewer || r == tbtypes.RoleEditor || r == tbtypes.RoleAdmin
}
