package apitypes

import "time"

// Groups and grants (ADR 0080): a person's access in an org is the role
// grants of their groups and their own one-off grants.

// Group is an org's job function: its members and what it gives.
type Group struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Seeded is one of the groups every org starts with: admins,
	// developers, viewers, approvers.
	Seeded  string        `json:"seeded,omitempty" enum:"admins,developers,viewers,approvers"`
	Members []GroupMember `json:"members"`
	Grants  []Grant       `json:"grants"`
}

// GroupMember is a person in a group.
type GroupMember struct {
	UserID int64  `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
}

// Grant is a built-in role at a place: the org (no project), a project, or
// one environment, for some environment kinds.
type Grant struct {
	ID int64 `json:"id"`
	// Role is an area role at a level ("deploy:edit"), "approver" or "full".
	Role        string `json:"role" example:"deploy:edit"`
	Project     string `json:"project,omitempty"`
	Environment string `json:"environment,omitempty"`
	// Kinds are the environment kinds it covers; all three when empty.
	Kinds     []string   `json:"kinds,omitempty" enum:"production,other,previews"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Via says why it exists when no one gave it directly: operator (a
	// joined operator's), role (a role whose group was removed).
	Via string `json:"via,omitempty"`
	// User is the person a one-off grant is for (members' list).
	User string `json:"user,omitempty"`
}

// CreateGroupRequest makes a group, or renames one (PATCH).
type CreateGroupRequest struct {
	Name string `json:"name" minLength:"1" maxLength:"64"`
}

// GrantRequest gives a role at a place: a group's grant, or one person's.
type GrantRequest struct {
	Role        string   `json:"role" minLength:"1" example:"deploy:edit"`
	Project     string   `json:"project,omitempty"`
	Environment string   `json:"environment,omitempty" doc:"Requires project"`
	Kinds       []string `json:"kinds,omitempty" enum:"production,other,previews" doc:"Environment kinds it covers; all when empty. Only for a grant on the org or a project"`
	// ForHours ends it by itself after that long; 0 never.
	ForHours int `json:"for_hours,omitempty" minimum:"0" maximum:"8760"`
}

// AreaRole is a built-in role: one area of Dock with its levels, each
// adding permissions to the one before.
type AreaRole struct {
	Key    string      `json:"key" example:"deploy"`
	Name   string      `json:"name" example:"Deploy"`
	Levels []RoleLevel `json:"levels"`
}

// RoleLevel is one level of an area role; Role is how grants name it.
type RoleLevel struct {
	Name        string   `json:"name,omitempty" example:"edit"`
	Role        string   `json:"role" example:"deploy:edit"`
	Permissions []string `json:"permissions"`
}

// EffectiveAccess is what a person can do, worked out from their groups
// and one-off grants: per area role, its level in each environment and
// on the org, and the grants that give it.
type EffectiveAccess struct {
	User   string   `json:"user"`
	Groups []string `json:"groups"`
	// Org is what they hold on the org as a whole (org permissions).
	Org []RoleAt `json:"org"`
	// Environments are every environment they see, with their levels.
	Environments []EnvironmentAccess `json:"environments"`
}

// EnvironmentAccess is a person's levels in one environment.
type EnvironmentAccess struct {
	Project     string   `json:"project"`
	Environment string   `json:"environment"`
	Kind        string   `json:"kind" enum:"production,other,previews"`
	Roles       []RoleAt `json:"roles"`
}

// RoleAt is an area role's level somewhere, and what gives it.
type RoleAt struct {
	Role  string `json:"role" example:"deploy"`
	Level string `json:"level" example:"edit"`
	// From are the grants that give it: "Developers: deploy:edit on the org".
	From []string `json:"from"`
}
