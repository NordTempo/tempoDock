// Package tbtypes is the JSON contract of tempoBase's own endpoints (ADR
// 0024 section 3): auth, people, tokens, connections and the audit log.
// The data panel's and the policy's types are apitypes' own, shared with
// Tempo Dock. Like apitypes it has no server dependencies; JSON field
// names are snake_case and stable, and struct tags feed the OpenAPI spec
// (web/tempobase/openapi.json).
package tbtypes

import (
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Role is a person's role in tempoBase.
type Role string

// Roles. Viewers follow the policy's role:read rules, editors its
// role:deploy rules; admins can do everything.
const (
	RoleViewer Role = "viewer"
	RoleEditor Role = "editor"
	RoleAdmin  Role = "admin"
)

// Health reports that the server is up.
type Health struct {
	Status string `json:"status" example:"ok"`
}

// Version identifies the server.
type Version struct {
	Version string `json:"version" example:"v0.1.0"`
}

// SetupStatus says whether the first admin still has to be created.
type SetupStatus struct {
	Needed bool `json:"needed"`
}

// SetupRequest creates the first admin with the one-time setup code the
// server logged at start.
type SetupRequest struct {
	Code     string `json:"code" minLength:"1"`
	Email    string `json:"email" format:"email"`
	Name     string `json:"name" minLength:"1" maxLength:"100"`
	Password string `json:"password" minLength:"12"`
}

// Me describes the caller: a signed-in person or an API token.
type Me struct {
	Kind  string `json:"kind" enum:"user,token"`
	Name  string `json:"name"`
	Email string `json:"email" doc:"The person, or the token's owner"`
	Role  Role   `json:"role" enum:"viewer,editor,admin" doc:"The person's role; for a token its owner's, which caps it"`
	// TokenLimited is set for a token limited to TokenConnections, which
	// is empty once they are all removed: such a token may use none.
	TokenLimited bool `json:"token_limited,omitempty" doc:"The token may use only token_connections, even when that is empty"`
	// TokenConnections is a token's connection limit; empty for people and
	// unlimited tokens, or a limited token whose connections were removed.
	TokenConnections []string `json:"token_connections" doc:"A token's connection limit; empty: no limit, unless token_limited"`
}

// User is a person who can sign in.
type User struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      Role      `json:"role" enum:"viewer,editor,admin"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"created_at"`
}

// UpdateUserRequest changes another person; absent fields stay.
// Disabling ends their sessions and tokens at once.
type UpdateUserRequest struct {
	Name     *string `json:"name,omitempty" minLength:"1" maxLength:"100"`
	Role     *Role   `json:"role,omitempty" enum:"viewer,editor,admin"`
	Disabled *bool   `json:"disabled,omitempty"`
}

// LinkKind is what a link does.
type LinkKind string

// Link kinds.
const (
	LinkInvite LinkKind = "invite"
	LinkReset  LinkKind = "reset"
)

// Link is an invite or a password reset link. URL (with the one-time
// code) is only in the response that creates it.
type Link struct {
	ID        int64     `json:"id"`
	Kind      LinkKind  `json:"kind" enum:"invite,reset"`
	Email     string    `json:"email"`
	Role      Role      `json:"role,omitempty" enum:"viewer,editor,admin" doc:"Invites: the role the person gets"`
	URL       string    `json:"url,omitempty" doc:"Only when created: <base>/link#<code>, to copy to the person"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CreateInviteRequest invites a person by email.
type CreateInviteRequest struct {
	Email string `json:"email" format:"email"`
	Role  Role   `json:"role" enum:"viewer,editor,admin"`
}

// LinkInfo is what a link's page shows before it is used.
type LinkInfo struct {
	Kind      LinkKind  `json:"kind" enum:"invite,reset"`
	Email     string    `json:"email"`
	ExpiresAt time.Time `json:"expires_at"`
}

// UseLinkRequest accepts an invite (name and password) or sets a new
// password from a reset link.
type UseLinkRequest struct {
	Name     string `json:"name,omitempty" maxLength:"100" doc:"Invites only"`
	Password string `json:"password" minLength:"12"`
}

// Token is an API token (for agents and scripts), never its secret.
type Token struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Prefix string `json:"prefix" doc:"The secret's first characters, to recognise it"`
	// User is the email of the person who owns it; the token is capped by
	// their current role.
	User string `json:"user"`
	// Connections limits the token to these connections when Limited;
	// otherwise it may use every connection the policy allows. A limited
	// token whose connections were all removed may use none.
	Connections []string                  `json:"connections"`
	Limited     bool                      `json:"limited,omitempty" doc:"The token may use only connections, even when that is empty"`
	Data        *apitypes.DataTokenAccess `json:"data,omitempty" doc:"The token's own data access; absent: the policy's tokens rule"`
	CreatedAt   time.Time                 `json:"created_at"`
	ExpiresAt   *time.Time                `json:"expires_at,omitempty"`
	RevokedAt   *time.Time                `json:"revoked_at,omitempty"`
}

// CreateTokenRequest creates a token for the caller.
type CreateTokenRequest struct {
	Name          string                    `json:"name" minLength:"1" maxLength:"100"`
	Connections   []string                  `json:"connections" doc:"Connection names to limit the token to; empty: no limit"`
	Data          *apitypes.DataTokenAccess `json:"data,omitempty" doc:"The token's own data access; absent: the policy's tokens rule"`
	ExpiresInDays int                       `json:"expires_in_days,omitempty" minimum:"0" maximum:"3650"`
}

// CreatedToken is returned once, on creation. Secret is never shown again.
type CreatedToken struct {
	Token  Token  `json:"token"`
	Secret string `json:"secret" example:"tbt_3q2..."`
}

// TLSMode is how a connection uses TLS, as libpq's sslmode.
type TLSMode string

// TLS modes.
const (
	TLSDisable    TLSMode = "disable"
	TLSPrefer     TLSMode = "prefer"
	TLSRequire    TLSMode = "require"
	TLSVerifyCA   TLSMode = "verify-ca"
	TLSVerifyFull TLSMode = "verify-full"
)

// Connection is a saved database. Its password never leaves the server.
type Connection struct {
	Name     string              `json:"name"`
	Engine   apitypes.DataEngine `json:"engine" enum:"postgres"`
	Host     string              `json:"host"`
	Port     int                 `json:"port"`
	Database string              `json:"database"`
	User     string              `json:"user"`
	TLS      TLSMode             `json:"tls" enum:"disable,prefer,require,verify-ca,verify-full"`
	// CustomCA says a CA certificate was given for verify-ca or
	// verify-full; otherwise the system roots are used.
	CustomCA   bool      `json:"custom_ca"`
	Production bool      `json:"production"`
	URL        string    `json:"url" doc:"The URL without its password"`
	CreatedAt  time.Time `json:"created_at"`
	CreatedBy  string    `json:"created_by"`
	// Access is the caller's data permission here.
	Access apitypes.DataPermission `json:"access"`
	// Pending counts open changesets; only for admins.
	Pending int `json:"pending" doc:"Open changesets; admins only, else 0"`
}

// CreateConnectionRequest saves a database.
type CreateConnectionRequest struct {
	Name string `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$" doc:"Used in URLs: lowercase letters, digits and dashes"`
	URL  string `json:"url" minLength:"1" maxLength:"4096" doc:"postgres://user:password@host:port/database?sslmode=prefer"`
	// Production connections follow the policy's production rules, and
	// commits on them confirm the connection's name.
	Production bool   `json:"production"`
	CAPEM      string `json:"ca_pem,omitempty" maxLength:"65536" doc:"PEM CA certificate for sslmode verify-ca or verify-full; default: the system roots"`
}

// UpdateConnectionRequest changes a connection; absent fields stay. A new
// URL or production flag applies at once.
type UpdateConnectionRequest struct {
	Name       *string `json:"name,omitempty" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	URL        *string `json:"url,omitempty" minLength:"1" maxLength:"4096"`
	Production *bool   `json:"production,omitempty"`
	CAPEM      *string `json:"ca_pem,omitempty" maxLength:"65536" doc:"Empty: the system roots"`
}

// TestConnectionRequest tries a URL without saving it.
type TestConnectionRequest struct {
	URL   string `json:"url" minLength:"1" maxLength:"4096"`
	CAPEM string `json:"ca_pem,omitempty" maxLength:"65536"`
}

// TestConnectionResult is what a test found.
type TestConnectionResult struct {
	OK            bool   `json:"ok"`
	ServerVersion string `json:"server_version,omitempty" example:"17.2"`
	// CanSetUp says the URL's user may create tempoBase's roles
	// (CREATEROLE or superuser), which setting up data access needs.
	CanSetUp bool   `json:"can_set_up"`
	Error    string `json:"error,omitempty" doc:"What failed, when not ok"`
}

// AuditEvent is one entry of the audit log.
type AuditEvent struct {
	ID         int64  `json:"id"`
	Actor      string `json:"actor"`
	ActorKind  string `json:"actor_kind" enum:"user,token,system"`
	Action     string `json:"action" example:"connection.create"`
	Connection string `json:"connection,omitempty" doc:"The connection's name now; empty if it was removed or the event isn't about one"`
	Target     string `json:"target"`
	Summary    string `json:"summary"`
	// DetailID is set for events with a detail, the SQL of data.query and
	// data.commit events (GET /audit/{detail_id}/detail).
	DetailID  *int64    `json:"detail_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
