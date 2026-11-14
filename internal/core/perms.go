package core

// The org's permission model (ADR 0080): a fixed list of permissions,
// built-in area roles that bundle them with levels, and the groups every
// org starts with. Phase 3 only computes it beside today's checks (the
// shadow, shadow.go); phase 4 makes it decide.

// Perm is one permission. Its value is an index into permTable and a bit
// in Perms; names are the stable identifiers stored and shown.
type Perm uint8

// PermLevel is where a permission applies: in an environment (granted at
// the org, a project or one environment, and decided per environment), on
// a whole project, or on the org.
type PermLevel uint8

// Permission levels.
const (
	PermEnv PermLevel = iota
	PermProject
	PermOrg
)

// Permissions. A new feature adds its own here and names it in its
// checks (actionPerms until checks take a Perm).
const (
	// Deploy
	PermEnvView Perm = iota
	PermServiceOperate
	PermDeploy
	PermRun
	PermConfigWrite
	PermEnvironmentsCreate
	PermProjectsCreate
	// Variables and secrets
	PermVariablesRead
	PermVariablesWrite
	PermVariablesReadSecret
	// Logs, errors and traces
	PermLogsRead
	PermErrorsManage
	// Data panel
	PermDataSchema
	PermDataRead
	PermDataPropose
	PermDataWrite
	PermDataSensitive
	PermDataApprove
	// Domains
	PermDomainsRead
	PermDomainsWrite
	// Analytics
	PermAnalyticsRead
	PermAnalyticsWrite
	PermAnalyticsManage
	// Status pages
	PermStatusPagesRead
	PermStatusPagesPost
	PermStatusPagesManage
	// Alerts and Inbox
	PermAlertsRead
	PermAlertsResolve
	PermAlertsManage
	PermUptimeManage
	PermRequestsRead
	PermApprove
	// Projects
	PermProjectManage
	PermEnvManage
	// Org settings
	PermOrgSettings
	PermConnectionsManage
	PermOrgVariables
	// Members and access
	PermMembersRead
	PermMembersManage
	PermRolesManage
	PermAgentsManage
	// Usage, Activity
	PermUsageRead
	PermActivityRead

	permCount
)

// permInfo describes a permission.
type permInfo struct {
	name  string
	level PermLevel
	// secret: holding it reaches the service's secret values (the code
	// it runs or ships reads them), whatever variables.read_secret says.
	secret bool
	// read: it only reads. In a project, one held in any of its
	// environments is enough; a change to a project needs it on the
	// whole project (today's environment limit lets a token read its
	// project, not change it).
	read bool
}

// permTable is every permission, by Perm.
var permTable = [permCount]permInfo{
	PermEnvView:             {"env.view", PermEnv, false, true},
	PermServiceOperate:      {"service.operate", PermEnv, false, false},
	PermDeploy:              {"deploy", PermEnv, true, false},
	PermRun:                 {"run", PermEnv, true, false},
	PermConfigWrite:         {"config.write", PermEnv, false, false},
	PermEnvironmentsCreate:  {"environments.create", PermProject, false, false},
	PermProjectsCreate:      {"projects.create", PermOrg, false, false},
	PermVariablesRead:       {"variables.read", PermEnv, false, true},
	PermVariablesWrite:      {"variables.write", PermEnv, false, false},
	PermVariablesReadSecret: {"variables.read_secret", PermEnv, true, false},
	PermLogsRead:            {"logs.read", PermEnv, false, true},
	PermErrorsManage:        {"errors.manage", PermEnv, false, false},
	PermDataSchema:          {"data.schema", PermEnv, false, true},
	PermDataRead:            {"data.read", PermEnv, false, true},
	PermDataPropose:         {"data.propose", PermEnv, false, false},
	PermDataWrite:           {"data.write", PermEnv, false, false},
	PermDataSensitive:       {"data.sensitive", PermEnv, true, false},
	PermDataApprove:         {"data.approve", PermEnv, false, false},
	PermDomainsRead:         {"domains.read", PermEnv, false, true},
	PermDomainsWrite:        {"domains.write", PermEnv, false, false},
	PermAnalyticsRead:       {"analytics.read", PermEnv, false, true},
	PermAnalyticsWrite:      {"analytics.write", PermEnv, false, false},
	PermAnalyticsManage:     {"analytics.manage", PermOrg, false, false},
	PermStatusPagesRead:     {"status_pages.read", PermEnv, false, true},
	PermStatusPagesPost:     {"status_pages.post", PermEnv, false, false},
	PermStatusPagesManage:   {"status_pages.manage", PermOrg, false, false},
	PermAlertsRead:          {"alerts.read", PermEnv, false, true},
	PermAlertsResolve:       {"alerts.resolve", PermEnv, false, false},
	PermAlertsManage:        {"alerts.manage", PermOrg, false, false},
	PermUptimeManage:        {"uptime.manage", PermEnv, false, false},
	PermRequestsRead:        {"requests.read", PermEnv, false, true},
	PermApprove:             {"approve", PermEnv, false, false},
	PermProjectManage:       {"project.manage", PermProject, false, false},
	PermEnvManage:           {"env.manage", PermEnv, false, false},
	PermOrgSettings:         {"org.settings", PermOrg, false, false},
	PermConnectionsManage:   {"connections.manage", PermOrg, false, false},
	PermOrgVariables:        {"org.variables", PermOrg, false, false},
	PermMembersRead:         {"members.read", PermOrg, false, true},
	PermMembersManage:       {"members.manage", PermOrg, false, false},
	PermRolesManage:         {"roles.manage", PermOrg, false, false},
	PermAgentsManage:        {"agents.manage", PermOrg, false, false},
	PermUsageRead:           {"usage.read", PermOrg, false, true},
	PermActivityRead:        {"activity.read", PermEnv, false, true},
}

// String is the permission's name.
func (p Perm) String() string { return permTable[p].name }

// Perms is a set of permissions.
type Perms uint64

// permsOf is the set of ps.
func permsOf(ps ...Perm) Perms {
	var s Perms
	for _, p := range ps {
		s |= 1 << p
	}
	return s
}

// has reports whether the set holds p.
func (s Perms) has(p Perm) bool { return s&(1<<p) != 0 }

// allPerms is every permission: Full access.
const allPerms = Perms(1<<permCount - 1)

// areaRole is a built-in building block (ADR 0080): one area of Dock, with
// levels that each add to the one before.
type areaRole struct {
	key, name string
	levels    []roleLevel
}

// roleLevel is one level of an area role and what it adds.
type roleLevel struct {
	name string
	adds Perms
}

// areaRoles are the built-in roles, the same in every org.
var areaRoles = []areaRole{
	{"deploy", "Deploy", []roleLevel{
		{"view", permsOf(PermEnvView)},
		{"operate", permsOf(PermServiceOperate)},
		{"edit", permsOf(PermDeploy, PermRun, PermConfigWrite, PermEnvironmentsCreate, PermProjectsCreate)},
	}},
	{"variables", "Variables and secrets", []roleLevel{
		{"view", permsOf(PermVariablesRead)},
		{"edit", permsOf(PermVariablesWrite)},
		{"reveal", permsOf(PermVariablesReadSecret)},
	}},
	{"logs", "Logs, errors and traces", []roleLevel{
		{"view", permsOf(PermLogsRead)},
		{"edit", permsOf(PermErrorsManage)},
	}},
	{"data", "Data panel", []roleLevel{
		{"schema", permsOf(PermDataSchema)},
		{"read", permsOf(PermDataRead)},
		{"propose", permsOf(PermDataPropose)},
		{"write", permsOf(PermDataWrite)},
		{"full", permsOf(PermDataSensitive)},
	}},
	{"domains", "Domains", []roleLevel{
		{"view", permsOf(PermDomainsRead)},
		{"edit", permsOf(PermDomainsWrite)},
	}},
	{"analytics", "Analytics", []roleLevel{
		{"view", permsOf(PermAnalyticsRead)},
		{"edit", permsOf(PermAnalyticsWrite)},
	}},
	{"status_pages", "Status pages", []roleLevel{
		{"view", permsOf(PermStatusPagesRead)},
		{"post", permsOf(PermStatusPagesPost)},
		{"edit", permsOf(PermStatusPagesManage)},
	}},
	{"alerts", "Alerts and Inbox", []roleLevel{
		{"view", permsOf(PermAlertsRead, PermRequestsRead)},
		{"resolve", permsOf(PermAlertsResolve)},
		{"edit", permsOf(PermAlertsManage, PermUptimeManage)},
	}},
	{"projects", "Projects", []roleLevel{
		{"edit", permsOf(PermProjectManage, PermEnvManage)},
	}},
	{"org_settings", "Org settings", []roleLevel{
		{"edit", permsOf(PermOrgSettings, PermConnectionsManage, PermOrgVariables, PermAnalyticsManage)},
	}},
	{"members", "Members and access", []roleLevel{
		{"view", permsOf(PermMembersRead)},
		{"edit", permsOf(PermMembersManage, PermRolesManage, PermAgentsManage)},
	}},
	{"usage", "Usage and billing", []roleLevel{{"view", permsOf(PermUsageRead)}}},
	{"activity", "Activity", []roleLevel{{"view", permsOf(PermActivityRead)}}},
	{"approver", "Approver", []roleLevel{{"", permsOf(PermApprove, PermDataApprove)}}},
	{"full", "Full access", []roleLevel{{"", allPerms}}},
}

// builtinRole is an area role at a level ("deploy:edit", "approver"), the
// name grants store; ok is false for one that doesn't exist.
func builtinRole(key, level string) (Perms, bool) {
	for _, r := range areaRoles {
		if r.key != key {
			continue
		}
		var s Perms
		for _, l := range r.levels {
			s |= l.adds
			if l.name == level {
				return s, true
			}
		}
	}
	return 0, false
}

// mustRole is builtinRole for the seeds below, which name real ones.
func mustRole(key, level string) Perms {
	s, ok := builtinRole(key, level)
	if !ok {
		panic("no built-in role " + key + ":" + level)
	}
	return s
}

// Seeded groups (ADR 0080): what Admins, Developers and Viewers hold
// outside the Data panel, which they hold per environment kind from the
// org's data policy (shadow.go). Developers and Viewers are exactly
// today's deploy and read; the shadow and its tests hold them to that.
var (
	seedAdmins  = mustRole("full", "")
	seedViewers = mustRole("deploy", "view") | mustRole("variables", "view") | mustRole("logs", "view") |
		mustRole("domains", "view") | mustRole("status_pages", "view") | mustRole("alerts", "view") |
		mustRole("analytics", "view") | mustRole("activity", "view")
	seedDevelopers = seedViewers | mustRole("deploy", "edit") | mustRole("variables", "edit") |
		mustRole("domains", "edit") | mustRole("alerts", "resolve") | mustRole("logs", "edit") |
		mustRole("analytics", "edit") | mustRole("status_pages", "post")
)

// dataPerms are the Data panel's permissions, decided per environment kind.
const dataPerms = Perms(1<<PermDataSchema | 1<<PermDataRead | 1<<PermDataPropose | 1<<PermDataWrite |
	1<<PermDataSensitive | 1<<PermDataApprove)
