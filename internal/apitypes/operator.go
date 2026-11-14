package apitypes

import "time"

// The platform console's Organizations page (ADR 0062): every org with
// its size and use, and what an operator may do to one without being a
// member. An org's content (variables, logs, data, code) is not here.

// OrgStatus is how an org stands, for the operator.
type OrgStatus string

// Org statuses.
const (
	OrgActive OrgStatus = "active"
	// OrgSuspended: deploys and changes are refused; its apps run on.
	OrgSuspended OrgStatus = "suspended"
	// OrgNoAdmin: no member is an admin, so nobody can manage it.
	OrgNoAdmin OrgStatus = "no_admin"
)

// OperatorOrg is one org as the operator sees it.
type OperatorOrg struct {
	Slug      string    `json:"slug" example:"acme"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	Status    OrgStatus `json:"status" enum:"active,suspended,no_admin"`
	// SuspendedAt and SuspendedBy say when and by whom it was suspended.
	SuspendedAt *time.Time `json:"suspended_at,omitempty"`
	SuspendedBy string     `json:"suspended_by,omitempty"`
	// Owner is its first admin; Admins every admin of its own (not
	// operators who joined for a while), owner first.
	Owner    string   `json:"owner,omitempty"`
	Admins   []string `json:"admins"`
	Members  int      `json:"members"`
	Projects int      `json:"projects"`
	Services int      `json:"services"`
	Usage    OrgUsage `json:"usage"`
}

// OrgUsage is what an org's live services use, from the statistics Dock
// keeps anyway.
type OrgUsage struct {
	// CPUPercent is relative to one core, like a service's: 150 is one
	// and a half cores. CPUWeekAgoPercent is the same a week ago (the
	// hour starting seven days back), for growth.
	CPUPercent        float64 `json:"cpu_percent"`
	CPUWeekAgoPercent float64 `json:"cpu_week_ago_percent"`
	MemoryBytes       int64   `json:"memory_bytes" doc:"Working set of its containers now"`
	// VolumeBytes is its volumes, from the servers' last disk reading
	// (ReadAt; none yet: opening the page asks for one).
	VolumeBytes  int64      `json:"volume_bytes"`
	VolumeReadAt *time.Time `json:"volume_read_at,omitempty"`
	BackupBytes  int64      `json:"backup_bytes" doc:"Its services' newest backups that worked"`
	Images       int        `json:"images" doc:"Images the registry keeps for its services' rollback windows"`
	// BandwidthBytes is what its services served in the last 30 days.
	BandwidthBytes int64 `json:"bandwidth_bytes"`
}

// OperatorMemberRequest adds an existing user to an org, or changes the
// role of one in it.
type OperatorMemberRequest struct {
	Email string `json:"email,omitempty" doc:"The user to add (with add); their account must exist"`
	Role  Scope  `json:"role" enum:"read,deploy,admin"`
}

// JoinOrgRequest is "Join as operator": membership as an admin for a
// while.
type JoinOrgRequest struct {
	Hours int `json:"hours,omitempty" minimum:"1" maximum:"24" doc:"How long, 1 when unset"`
}

// OrgMembershipGrant is the membership Join as operator made.
type OrgMembershipGrant struct {
	Org       string    `json:"org"`
	Role      Scope     `json:"role"`
	ExpiresAt time.Time `json:"expires_at"`
}

// StoppedOrgServices is what stopping an org's services did.
type StoppedOrgServices struct {
	Stopped int `json:"stopped"`
	// Failed lists the services that didn't stop, with why.
	Failed []string `json:"failed,omitempty"`
}

// Account is a user account as the platform console's Users page shows
// it: the account itself (the platform's) and the orgs it belongs to
// (each org's own business, shown here so operators see who owns what).
type Account struct {
	ID       int64  `json:"id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Operator bool   `json:"operator"`
	// PlatformRole is the account's platform role (ADR 0080); Operator
	// is true for operator.
	PlatformRole PlatformRole `json:"platform_role,omitempty" enum:"operator,viewer"`
	TwoFactor    bool         `json:"two_factor"`
	CreatedAt    time.Time    `json:"created_at"`
	DisabledAt   *time.Time   `json:"disabled_at,omitempty"`
	Orgs         []AccountOrg `json:"orgs"`
}

// AccountOrg is one org an account belongs to.
type AccountOrg struct {
	Org  string `json:"org"`
	Role Scope  `json:"role" enum:"read,deploy,admin"`
	// Owner: the org's first admin of its own.
	Owner bool `json:"owner,omitempty"`
	// ExpiresAt: an operator's membership from Join as operator.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// SetOperatorRequest makes a user a platform operator, or not.
type SetOperatorRequest struct {
	Operator bool `json:"operator"`
}
