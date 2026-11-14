package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// The platform apart (ADR 0080). Running Dock itself (servers, accounts,
// updates, backups, the registry, the platform's settings, alerts and the
// console's view of orgs) is a platform permission, held through a
// platform role: a user's (users.platform_role) or a platform token's.
// No org role gives one, and a platform credential (a platform token or
// `dock login --platform`) never acts in an org.

// PlatformPerm is one thing the platform's checks ask for. Every
// requirePlatform names one.
type PlatformPerm string

// Platform permissions. A platform viewer has PlatformView only; an
// operator has all of them.
const (
	// PlatformView reads the console: servers and their statistics,
	// orgs and their use, settings, alerts, the platform's audit log.
	PlatformView PlatformPerm = "platform.view"
	// ServersManage adds, removes, drains and places servers, and moves
	// environments between them.
	ServersManage PlatformPerm = "servers.manage"
	// AccountsManage creates and manages user accounts, platform roles
	// and platform tokens.
	AccountsManage PlatformPerm = "accounts.manage"
	// OrgsAdminister administers any org's metadata (ADR 0062), never its
	// content; OrgsJoin joins one for a while to reach its content.
	OrgsAdminister PlatformPerm = "orgs.administer"
	OrgsJoin       PlatformPerm = "orgs.join"
	// BackupsManage configures backups and storage connections and backs
	// up the control plane.
	BackupsManage PlatformPerm = "backups.manage"
	// RegistryManage sets up and prunes the image registry.
	RegistryManage PlatformPerm = "registry.manage"
	// UpdatesApply updates Dock and its servers.
	UpdatesApply PlatformPerm = "updates.apply"
	// PlatformSettings changes the platform's settings: its address,
	// email, sign-in and SSO, analytics, tracing, scanning, the data
	// policy's ceiling, the GitHub App and Cloudflare connections.
	PlatformSettings PlatformPerm = "platform.settings"
	// PlatformAlerts resolves the platform's alerts and manages its
	// alert channels.
	PlatformAlerts PlatformPerm = "platform.alerts"
)

// platformRoles is what each platform role holds.
var platformRoles = map[apitypes.PlatformRole][]PlatformPerm{
	apitypes.PlatformOperator: {PlatformView, ServersManage, AccountsManage, OrgsAdminister, OrgsJoin, BackupsManage,
		RegistryManage, UpdatesApply, PlatformSettings, PlatformAlerts},
	apitypes.PlatformViewer: {PlatformView},
}

// validPlatformRole reports whether r names a platform role.
func validPlatformRole(r apitypes.PlatformRole) bool {
	_, ok := platformRoles[r]
	return ok
}

// platformCan reports whether role holds perm.
func platformCan(role apitypes.PlatformRole, perm PlatformPerm) bool {
	for _, p := range platformRoles[role] {
		if p == perm {
			return true
		}
	}
	return false
}

// withPlatform gives the actor a platform role ("" for none); Operator
// follows it.
func (a Actor) withPlatform(role apitypes.PlatformRole) Actor {
	a.Platform, a.Operator = role, role == apitypes.PlatformOperator
	return a
}

// requirePlatform checks the actor may do action on the platform: its
// platform role holds perm. The system is an operator (System).
func (a Actor) requirePlatform(perm PlatformPerm, action string) error {
	if platformCan(a.Platform, perm) {
		return nil
	}
	if a.operatorNeeds2FA {
		return errf(apitypes.CodeTwoFactorRequired, "%s requires two-factor sign-in: %s", action, a.twoFactorRequired())
	}
	return a.refusePlatform(perm, action)
}

// readsPlatform reports whether the actor may read the console's view of
// the platform (PlatformView): the servers an environment runs on,
// server statistics, platform events.
func (a Actor) readsPlatform() bool {
	return platformCan(a.Platform, PlatformView)
}

// platformIn places a platform credential in the org named by slug (its
// old slug too), or in the install's only org, for platform actions on an
// org's resources: moving its environment, pinning its project to a
// server. It gets no scope there, so every org permission still refuses
// it (requireScope); only the platform's checks let it act. Not found, or
// several orgs and none named, leave it in none.
func (c *Core) platformIn(ctx context.Context, a Actor, slug string) (Actor, error) {
	if slug != "" {
		o, err := c.orgBySlug(ctx, slug)
		if store.IsNotFound(err) || err == nil && o.DeletedAt != nil {
			return a, notFound("organization %q not found; the console's Organizations page lists them", slug)
		}
		if err != nil {
			return a, err
		}
		a.OrgID, a.OrgSlug, a.orgs = o.ID, o.Slug, 1
		return a, nil
	}
	orgs, err := c.store.FirstLiveOrgs(ctx)
	if err != nil {
		return a, err
	}
	a.orgs = len(orgs)
	if len(orgs) == 1 {
		a.OrgID, a.OrgSlug = orgs[0].ID, orgs[0].Slug
	}
	return a, nil
}

// viewing is PlatformView for an action that only reads ("listing ...",
// "reading ..."), else perm: for helpers shared by reads and changes.
func viewing(action string, perm PlatformPerm) PlatformPerm {
	if strings.HasPrefix(action, "listing ") || strings.HasPrefix(action, "reading ") {
		return PlatformView
	}
	return perm
}

// refusePlatform is the refusal when a platform action meets an actor
// whose credential or role doesn't allow it, saying which credential
// would: an org's token never runs the platform, and a person's org
// login doesn't either since ADR 0080 (their platform login does).
func (a Actor) refusePlatform(perm PlatformPerm, action string) error {
	r := apitypes.Refusal{DecidedBy: apitypes.RefusedPlatform, Action: action, Need: string(perm)}
	switch {
	case a.Kind == "token" && a.UserID == 0 && !a.platformOnly:
		r.Reason = fmt.Sprintf("%s is a token of organization %s, not a platform token; %s runs the platform, which org tokens never do", a, a.orgName(), action)
		r.Fix = "platform commands need a platform login (`dock login --platform`) or a platform token (Console › Tokens); `dock whoami` shows what this context holds"
	case a.Kind == "token" && !a.platformOnly && a.userPlatform != "":
		r.Reason = fmt.Sprintf("%s is %s's org login; %s runs the platform, which takes their platform login", a, a.who(), action)
		r.Fix = "run `dock login --platform`; it keeps this org login and adds a platform one beside it"
	case a.Platform == "":
		r.Reason = fmt.Sprintf("%s has no platform role; %s needs %s", a, action, perm)
		r.Fix = "an operator gives platform roles in Console › Users (`dock user platform-role <email> operator`)"
	default:
		r.Reason = fmt.Sprintf("%s has platform role %s; %s needs %s, which operators have", a, a.Platform, action, perm)
		r.Fix = "an operator can make you one in Console › Users (`dock user platform-role <email> operator`)"
	}
	return a.refuse(r)
}
