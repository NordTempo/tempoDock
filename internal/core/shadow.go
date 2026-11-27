package core

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The shadow (ADR 0080, phase 3): today's checks decide, and beside each
// the permission model computes its own answer from the same state:
// today's role, scope and limits as the grants the migration will make
// (a person's role as their seeded group at the org, a token's scope as
// its group's permissions at its limit's place). Every disagreement is
// counted and logged once, so a release can show the model agrees with
// what decides today before it decides (phase 4). Tests fail on any.

// actionKey names a check: the scope a call site asks for and its action.
// Until checks take a Perm, actionPerms says which one each one means.
type actionKey struct {
	need   apitypes.Scope
	action string
}

// actionPerms is the permission each of today's checks means. A check
// missing from it counts as a disagreement ("unmapped"), and
// TestEveryCheckHasAPermission fails for a literal one.
var actionPerms = map[actionKey]Perm{
	{apitypes.ScopeAdmin, "reading groups"}:                                              PermMembersRead,
	{apitypes.ScopeAdmin, "managing groups"}:                                             PermMembersManage,
	{apitypes.ScopeRead, "reading your access"}:                                          PermEnvView,
	{apitypes.ScopeAdmin, "removing alert channels"}:                                     PermAlertsManage,
	{apitypes.ScopeAdmin, "testing alert channels"}:                                      PermAlertsManage,
	{apitypes.ScopeDeploy, "editing an incident update"}:                                 PermStatusPagesPost,
	{apitypes.ScopeRead, "reading a site"}:                                               PermAnalyticsRead,
	{apitypes.ScopeRead, "applying a project file to a new project"}:                     PermEnvView,
	{apitypes.ScopeAdmin, "adding Cloudflare connections"}:                               PermConnectionsManage,
	{apitypes.ScopeDeploy, "adding a funnel"}:                                            PermAnalyticsWrite,
	{apitypes.ScopeRead, "adding a funnel"}:                                              PermAnalyticsRead,
	{apitypes.ScopeDeploy, "adding a goal"}:                                              PermAnalyticsWrite,
	{apitypes.ScopeRead, "adding a goal"}:                                                PermAnalyticsRead,
	{apitypes.ScopeAdmin, "adding alert channels"}:                                       PermAlertsManage,
	{apitypes.ScopeAdmin, "adding redirects"}:                                            PermConnectionsManage,
	{apitypes.ScopeAdmin, "changing Cloudflare connections"}:                             PermConnectionsManage,
	{apitypes.ScopeAdmin, "managing agent rules"}:                                        PermAgentsManage,
	{apitypes.ScopeAdmin, "reading agent rules"}:                                         PermMembersRead,
	{apitypes.ScopeAdmin, "managing roles"}:                                              PermRolesManage,
	{apitypes.ScopeAdmin, "reading roles"}:                                               PermMembersRead,
	{apitypes.ScopeAdmin, "changing redirects"}:                                          PermConnectionsManage,
	{apitypes.ScopeAdmin, "retrying redirects"}:                                          PermConnectionsManage,
	{apitypes.ScopeAdmin, "removing an ignore in the whole organization"}:                PermOrgSettings,
	{apitypes.ScopeDeploy, "applying a project file to a new project"}:                   PermProjectsCreate,
	{apitypes.ScopeAdmin, "approving access requests"}:                                   PermApprove,
	{apitypes.ScopeDeploy, "changing a site"}:                                            PermAnalyticsWrite,
	{apitypes.ScopeRead, "changing a site"}:                                              PermAnalyticsRead,
	{apitypes.ScopeDeploy, "changing an incident"}:                                       PermStatusPagesPost,
	{apitypes.ScopeAdmin, "changing roles"}:                                              PermMembersManage,
	{apitypes.ScopeAdmin, "deleting a project"}:                                          PermProjectManage,
	{apitypes.ScopeAdmin, "deleting an environment"}:                                     PermProjectManage,
	{apitypes.ScopeDeploy, "ignoring a vulnerability"}:                                   PermConfigWrite,
	{apitypes.ScopeRead, "ignoring a vulnerability"}:                                     PermEnvView,
	{apitypes.ScopeAdmin, "ignoring a vulnerability in the whole organization"}:          PermOrgSettings,
	{apitypes.ScopeAdmin, "installing the GitHub App"}:                                   PermConnectionsManage,
	{apitypes.ScopeAdmin, "creating a GitHub App"}:                                       PermConnectionsManage,
	{apitypes.ScopeAdmin, "removing a GitHub App"}:                                       PermConnectionsManage,
	{apitypes.ScopeAdmin, "setting a GitHub App's client secret"}:                        PermConnectionsManage,
	{apitypes.ScopeAdmin, "listing Cloudflare connections"}:                              PermConnectionsManage,
	{apitypes.ScopeAdmin, "listing redirects"}:                                           PermConnectionsManage,
	{apitypes.ScopeAdmin, "making a status page"}:                                        PermStatusPagesManage,
	{apitypes.ScopeDeploy, "opening an incident"}:                                        PermStatusPagesPost,
	{apitypes.ScopeDeploy, "posting an incident update"}:                                 PermStatusPagesPost,
	{apitypes.ScopeRead, "reading a site's analytics"}:                                   PermAnalyticsRead,
	{apitypes.ScopeRead, "reading a site's funnels"}:                                     PermAnalyticsRead,
	{apitypes.ScopeRead, "reading a site's goals"}:                                       PermAnalyticsRead,
	{apitypes.ScopeRead, "reading a site's tracking"}:                                    PermAnalyticsRead,
	{apitypes.ScopeAdmin, "reading alert channels"}:                                      PermAlertsManage,
	{apitypes.ScopeRead, "reading alerts"}:                                               PermAlertsRead,
	{apitypes.ScopeRead, "reading incidents"}:                                            PermStatusPagesRead,
	{apitypes.ScopeAdmin, "refreshing Cloudflare connections"}:                           PermConnectionsManage,
	{apitypes.ScopeAdmin, "rejecting access requests"}:                                   PermApprove,
	{apitypes.ScopeAdmin, "removing Cloudflare connections"}:                             PermConnectionsManage,
	{apitypes.ScopeDeploy, "removing a funnel"}:                                          PermAnalyticsWrite,
	{apitypes.ScopeRead, "removing a funnel"}:                                            PermAnalyticsRead,
	{apitypes.ScopeDeploy, "removing a goal"}:                                            PermAnalyticsWrite,
	{apitypes.ScopeRead, "removing a goal"}:                                              PermAnalyticsRead,
	{apitypes.ScopeDeploy, "removing an ignore"}:                                         PermConfigWrite,
	{apitypes.ScopeRead, "removing an ignore"}:                                           PermEnvView,
	{apitypes.ScopeAdmin, "removing an incident"}:                                        PermStatusPagesManage,
	{apitypes.ScopeAdmin, "removing members"}:                                            PermMembersManage,
	{apitypes.ScopeAdmin, "removing redirects"}:                                          PermConnectionsManage,
	{apitypes.ScopeDeploy, "resolving errors"}:                                           PermErrorsManage,
	{apitypes.ScopeDeploy, "scheduling maintenance"}:                                     PermStatusPagesPost,
	{apitypes.ScopeDeploy, "turning analytics on for a service"}:                         PermAnalyticsWrite,
	{apitypes.ScopeDeploy, "moving a site"}:                                              PermAnalyticsWrite,
	{apitypes.ScopeAdmin, "unassigning a GitHub App installation"}:                       PermConnectionsManage,
	{apitypes.ScopeRead, "watching a site's replays"}:                                    PermAnalyticsReplays,
	{apitypes.ScopeRead, "reading a site's replays"}:                                     PermAnalyticsReplays,
	{apitypes.ScopeRead, "watching a site live"}:                                         PermAnalyticsRead,
	{apitypes.ScopeRead, "checking a site's tracking"}:                                   PermAnalyticsRead,
	{apitypes.ScopeRead, "reading a site's public dashboard"}:                            PermAnalyticsRead,
	{apitypes.ScopeDeploy, "adding a domain"}:                                            PermDomainsWrite,
	{apitypes.ScopeAdmin, "adding a status page hook"}:                                   PermStatusPagesManage,
	{apitypes.ScopeDeploy, "adding a volume"}:                                            PermConfigWrite,
	{apitypes.ScopeAdmin, "adding an uptime check"}:                                      PermUptimeManage,
	{apitypes.ScopeAdmin, "adding log drains"}:                                           PermEnvManage,
	{apitypes.ScopeAdmin, "adding uptime checks"}:                                        PermUptimeManage,
	{apitypes.ScopeDeploy, "applying a project file"}:                                    PermConfigWrite,
	{apitypes.ScopeRead, "applying a project file"}:                                      PermEnvView,
	{apitypes.ScopeDeploy, "applying staged changes"}:                                    PermConfigWrite,
	{apitypes.ScopeAdmin, "applying staged changes that archive a service"}:              PermEnvManage,
	{apitypes.ScopeAdmin, "archiving a project"}:                                         PermProjectManage,
	{apitypes.ScopeAdmin, "archiving a service"}:                                         PermEnvManage,
	{apitypes.ScopeAdmin, "archiving services a project file leaves out (--prune)"}:      PermEnvManage,
	{apitypes.ScopeAdmin, "changing a status page"}:                                      PermStatusPagesManage,
	{apitypes.ScopeAdmin, "changing a status page's components"}:                         PermStatusPagesManage,
	{apitypes.ScopeAdmin, "changing alert settings"}:                                     PermAlertsManage,
	{apitypes.ScopeAdmin, "changing an uptime check"}:                                    PermUptimeManage,
	{apitypes.ScopeDeploy, "changing database settings"}:                                 PermConfigWrite,
	{apitypes.ScopeDeploy, "changing service settings"}:                                  PermConfigWrite,
	{apitypes.ScopeAdmin, "changing the analytics defaults"}:                             PermAnalyticsManage,
	{apitypes.ScopeAdmin, "changing the data access policy"}:                             PermMembersManage,
	{apitypes.ScopeAdmin, "changing the organization's lock settings"}:                   PermOrgSettings,
	{apitypes.ScopeAdmin, "changing the organization's sign-in policy"}:                  PermOrgSettings,
	{apitypes.ScopeAdmin, "changing when an environment sleeps"}:                         PermEnvManage,
	{apitypes.ScopeDeploy, "changing who can open a service"}:                            PermConfigWrite,
	{apitypes.ScopeDeploy, "checking a domain"}:                                          PermDomainsWrite,
	{apitypes.ScopeAdmin, "checking a status page's hostname"}:                           PermStatusPagesManage,
	{apitypes.ScopeAdmin, "checking access requests"}:                                    PermApprove,
	{apitypes.ScopeDeploy, "cloning an environment"}:                                     PermEnvironmentsCreate,
	{apitypes.ScopeRead, "cloning an environment"}:                                       PermEnvView,
	{apitypes.ScopeDeploy, "closing a preview"}:                                          PermDeploy,
	{apitypes.ScopeDeploy, "refreshing a preview's data"}:                                PermDeploy,
	{apitypes.ScopeDeploy, "connecting a GitHub repository"}:                             PermConfigWrite,
	{apitypes.ScopeDeploy, "following a Compose file"}:                                   PermConfigWrite,
	{apitypes.ScopeAdmin, "staging archives for services a followed Compose file drops"}: PermEnvManage,
	{apitypes.ScopeRead, "connecting to a service"}:                                      PermEnvView,
	{apitypes.ScopeAdmin, "copying values into a clone"}:                                 PermVariablesReadSecret,
	{apitypes.ScopeDeploy, "creating a project"}:                                         PermProjectsCreate,
	{apitypes.ScopeDeploy, "creating a service"}:                                         PermConfigWrite,
	{apitypes.ScopeDeploy, "using a template"}:                                           PermConfigWrite,
	{apitypes.ScopeAdmin, "saving an organization template"}:                             PermOrgSettings,
	{apitypes.ScopeRead, "saving an organization template"}:                              PermEnvView,
	{apitypes.ScopeAdmin, "removing an organization template"}:                           PermOrgSettings,
	{apitypes.ScopeRead, "using a template"}:                                             PermEnvView,
	{apitypes.ScopeRead, "reading templates"}:                                            PermEnvView,
	{apitypes.ScopeDeploy, "creating an environment"}:                                    PermEnvironmentsCreate,
	{apitypes.ScopeDeploy, "deleting a snapshot"}:                                        PermConfigWrite,
	{apitypes.ScopeAdmin, "deleting the organization"}:                                   PermOrgSettings,
	{apitypes.ScopeDeploy, "canceling a deploy"}:                                         PermDeploy,
	{apitypes.ScopeDeploy, "deploying"}:                                                  PermDeploy,
	{apitypes.ScopeDeploy, "discarding staged changes"}:                                  PermConfigWrite,
	{apitypes.ScopeDeploy, "disconnecting a GitHub repository"}:                          PermConfigWrite,
	{apitypes.ScopeRead, "exporting a project"}:                                          PermEnvView,
	{apitypes.ScopeRead, "following events"}:                                             PermEnvView,
	{apitypes.ScopeAdmin, "ignoring a vulnerability for good"}:                           PermOrgSettings,
	{apitypes.ScopeAdmin, "inviting people"}:                                             PermMembersManage,
	{apitypes.ScopeRead, "leaving the organization"}:                                     PermEnvView,
	{apitypes.ScopeDeploy, "listing Cloudflare zones"}:                                   PermDomainsWrite,
	{apitypes.ScopeDeploy, "listing GitHub repositories"}:                                PermConfigWrite,
	{apitypes.ScopeDeploy, "listing a repository's branches"}:                            PermConfigWrite,
	{apitypes.ScopeDeploy, "listing a repository's directories"}:                         PermConfigWrite,
	{apitypes.ScopeRead, "listing a service's jobs"}:                                     PermEnvView,
	{apitypes.ScopeRead, "reading the deploy queue"}:                                     PermEnvView,
	{apitypes.ScopeRead, "listing a service's runs"}:                                     PermEnvView,
	{apitypes.ScopeRead, "listing access requests"}:                                      PermRequestsRead,
	{apitypes.ScopeRead, "listing backups"}:                                              PermEnvView,
	{apitypes.ScopeRead, "listing domains"}:                                              PermDomainsRead,
	{apitypes.ScopeRead, "listing environments"}:                                         PermEnvView,
	{apitypes.ScopeAdmin, "listing invites"}:                                             PermMembersManage,
	{apitypes.ScopeRead, "listing log drains"}:                                           PermEnvView,
	{apitypes.ScopeRead, "listing organization variables"}:                               PermVariablesRead,
	{apitypes.ScopeRead, "listing projects"}:                                             PermEnvView,
	{apitypes.ScopeAdmin, "listing registry credentials"}:                                PermConnectionsManage,
	{apitypes.ScopeRead, "listing releases"}:                                             PermEnvView,
	{apitypes.ScopeRead, "listing services"}:                                             PermEnvView,
	{apitypes.ScopeRead, "listing sites"}:                                                PermAnalyticsRead,
	{apitypes.ScopeRead, "listing snapshots"}:                                            PermEnvView,
	{apitypes.ScopeRead, "listing uptime checks"}:                                        PermEnvView,
	{apitypes.ScopeRead, "listing variables"}:                                            PermVariablesRead,
	{apitypes.ScopeRead, "listing volumes"}:                                              PermEnvView,
	{apitypes.ScopeAdmin, "locking an environment"}:                                      PermEnvManage,
	{apitypes.ScopeAdmin, "managing tokens"}:                                             PermAgentsManage,
	{apitypes.ScopeAdmin, "managing users"}:                                              PermMembersManage,
	{apitypes.ScopeRead, "moving a site"}:                                                PermAnalyticsRead,
	// "opening <host>" (access protection) is matched by actionPrefixes.
	{apitypes.ScopeRead, "opening "}:                                             PermEnvView,
	{apitypes.ScopeRead, "opening an incident"}:                                  PermStatusPagesRead,
	{apitypes.ScopeDeploy, "pinning charts"}:                                     PermConfigWrite,
	{apitypes.ScopeDeploy, "promoting"}:                                          PermDeploy,
	{apitypes.ScopeRead, "promoting"}:                                            PermEnvView,
	{apitypes.ScopeDeploy, "putting an environment to sleep"}:                    PermServiceOperate,
	{apitypes.ScopeRead, "reading SBOMs"}:                                        PermEnvView,
	{apitypes.ScopeDeploy, "reading a backup's log"}:                             PermConfigWrite,
	{apitypes.ScopeRead, "reading a job's runs"}:                                 PermEnvView,
	{apitypes.ScopeRead, "reading a move"}:                                       PermEnvView,
	{apitypes.ScopeRead, "reading a preview"}:                                    PermEnvView,
	{apitypes.ScopeRead, "reading a project"}:                                    PermEnvView,
	{apitypes.ScopeDeploy, "reading a run's output"}:                             PermRun,
	{apitypes.ScopeRead, "reading a service"}:                                    PermEnvView,
	{apitypes.ScopeDeploy, "reading a snapshot's log"}:                           PermConfigWrite,
	{apitypes.ScopeAdmin, "reading a status page's hooks"}:                       PermStatusPagesManage,
	{apitypes.ScopeRead, "reading alert settings"}:                               PermAlertsRead,
	{apitypes.ScopeRead, "reading an access request"}:                            PermRequestsRead,
	{apitypes.ScopeRead, "reading an environment"}:                               PermEnvView,
	{apitypes.ScopeRead, "reading app metrics"}:                                  PermEnvView,
	{apitypes.ScopeRead, "reading backup settings"}:                              PermEnvView,
	{apitypes.ScopeRead, "reading build logs"}:                                   PermLogsRead,
	{apitypes.ScopeRead, "reading data access"}:                                  PermEnvView,
	{apitypes.ScopeRead, "reading diagnoses"}:                                    PermLogsRead,
	{apitypes.ScopeRead, "reading errors"}:                                       PermLogsRead,
	{apitypes.ScopeRead, "reading ignores"}:                                      PermEnvView,
	{apitypes.ScopeRead, "reading images"}:                                       PermEnvView,
	{apitypes.ScopeRead, "reading logs"}:                                         PermLogsRead,
	{apitypes.ScopeRead, "reading preview settings"}:                             PermEnvView,
	{apitypes.ScopeRead, "reading previews"}:                                     PermEnvView,
	{apitypes.ScopeRead, "reading releases"}:                                     PermEnvView,
	{apitypes.ScopeRead, "reading service statistics"}:                           PermEnvView,
	{apitypes.ScopeRead, "reading service traffic"}:                              PermEnvView,
	{apitypes.ScopeRead, "reading status pages"}:                                 PermStatusPagesRead,
	{apitypes.ScopeRead, "reading the GitHub source"}:                            PermEnvView,
	{apitypes.ScopeRead, "reading the analytics defaults"}:                       PermAnalyticsRead,
	{apitypes.ScopeRead, "reading the audit log"}:                                PermActivityRead,
	{apitypes.ScopeRead, "reading the data access policy"}:                       PermEnvView,
	{apitypes.ScopeRead, "reading the organization"}:                             PermEnvView,
	{apitypes.ScopeAdmin, "reading the organization's GitHub App installations"}: PermConnectionsManage,
	{apitypes.ScopeRead, "reading the organization's lock settings"}:             PermEnvView,
	{apitypes.ScopeRead, "reading traces"}:                                       PermLogsRead,
	{apitypes.ScopeRead, "reading vulnerabilities"}:                              PermEnvView,
	{apitypes.ScopeRead, "reading who can open a service"}:                       PermEnvView,
	{apitypes.ScopeDeploy, "redeploying"}:                                        PermDeploy,
	{apitypes.ScopeDeploy, "removing a domain"}:                                  PermDomainsWrite,
	{apitypes.ScopeDeploy, "removing a job"}:                                     PermConfigWrite,
	{apitypes.ScopeAdmin, "removing a status page"}:                              PermStatusPagesManage,
	{apitypes.ScopeAdmin, "removing a status page hook"}:                         PermStatusPagesManage,
	{apitypes.ScopeAdmin, "removing an uptime check"}:                            PermUptimeManage,
	{apitypes.ScopeAdmin, "removing log drains"}:                                 PermEnvManage,
	{apitypes.ScopeAdmin, "removing organization variables"}:                     PermOrgVariables,
	{apitypes.ScopeAdmin, "removing registry credentials"}:                       PermConnectionsManage,
	{apitypes.ScopeAdmin, "renaming a project"}:                                  PermProjectManage,
	{apitypes.ScopeAdmin, "renaming the organization"}:                           PermOrgSettings,
	{apitypes.ScopeDeploy, "resolving alerts"}:                                   PermAlertsResolve,
	{apitypes.ScopeDeploy, "restarting"}:                                         PermServiceOperate,
	{apitypes.ScopeDeploy, "restoring a backup"}:                                 PermConfigWrite,
	{apitypes.ScopeDeploy, "restoring a snapshot"}:                               PermConfigWrite,
	{apitypes.ScopeAdmin, "revealing secret values"}:                             PermVariablesReadSecret,
	{apitypes.ScopeAdmin, "revoking invites"}:                                    PermMembersManage,
	{apitypes.ScopeDeploy, "rolling back"}:                                       PermServiceOperate,
	{apitypes.ScopeDeploy, "running a backup"}:                                   PermConfigWrite,
	{apitypes.ScopeDeploy, "running a command"}:                                  PermRun,
	{apitypes.ScopeAdmin, actionShell}:                                           PermShell,
	{apitypes.ScopeDeploy, "running a job"}:                                      PermRun,
	{apitypes.ScopeDeploy, "scanning images"}:                                    PermDeploy,
	{apitypes.ScopeDeploy, "checking for a newer image"}:                         PermDeploy,
	{apitypes.ScopeDeploy, "changing image updates"}:                             PermDeploy,
	{apitypes.ScopeRead, "scheduling maintenance"}:                               PermStatusPagesRead,
	{apitypes.ScopeDeploy, "setting a job"}:                                      PermConfigWrite,
	{apitypes.ScopeAdmin, "setting organization variables"}:                      PermOrgVariables,
	{apitypes.ScopeAdmin, "setting registry credentials"}:                        PermConnectionsManage,
	{apitypes.ScopeAdmin, "setting up previews"}:                                 PermProjectManage,
	{apitypes.ScopeDeploy, "setting variables"}:                                  PermVariablesWrite,
	{apitypes.ScopeDeploy, "starting a service"}:                                 PermServiceOperate,
	{apitypes.ScopeDeploy, "stopping a service"}:                                 PermServiceOperate,
	{apitypes.ScopeDeploy, "switching references"}:                               PermVariablesWrite,
	{apitypes.ScopeDeploy, "taking a snapshot"}:                                  PermConfigWrite,
	{apitypes.ScopeAdmin, "testing a status page hook"}:                          PermStatusPagesManage,
	{apitypes.ScopeAdmin, "testing log drains"}:                                  PermEnvManage,
	{apitypes.ScopeDeploy, "turning a job on or off"}:                            PermConfigWrite,
	{apitypes.ScopeDeploy, "turning analytics off for a service"}:                PermAnalyticsWrite,
	{apitypes.ScopeRead, "unlocking an environment"}:                             PermEnvView,
	{apitypes.ScopeRead, "using the database panel"}:                             PermEnvView,
	{apitypes.ScopeRead, "viewing staged changes"}:                               PermEnvView,
	{apitypes.ScopeRead, "waiting for an access request"}:                        PermRequestsRead,
	{apitypes.ScopeRead, "waking an environment"}:                                PermEnvView,
}

// actionPrefixes name the checks whose action ends in what they act on
// ("opening shop.example.com").
var actionPrefixes = []struct {
	need   apitypes.Scope
	prefix string
	perm   Perm
}{
	{apitypes.ScopeRead, "opening ", PermEnvView},
}

// permFor is the permission a check means; ok is false for one with
// none.
func permFor(key actionKey) (Perm, bool) {
	if p, ok := actionPerms[key]; ok {
		return p, true
	}
	for _, x := range actionPrefixes {
		if x.need == key.need && strings.HasPrefix(key.action, x.prefix) {
			return x.perm, true
		}
	}
	return 0, false
}

// shadowRecord is the disagreements seen since start, by kind of actor,
// check and the two answers, for operators (ShadowReport) and tests.
var shadowRecord struct {
	mu     sync.Mutex
	counts map[string]int64
}

// shadowLog is where the first of each disagreement is logged (Core sets
// its logger).
var shadowLog atomic.Pointer[slog.Logger]

// recordShadow counts one disagreement, logging the first of its kind.
func recordShadow(a Actor, key actionKey, place string, old, model bool, why string) {
	k := fmt.Sprintf("%s %s: %q at %s: today %s, the model %s%s", a.Kind, key.need, key.action, place, verdict(old), verdict(model), why)
	shadowRecord.mu.Lock()
	if shadowRecord.counts == nil {
		shadowRecord.counts = map[string]int64{}
	}
	shadowRecord.counts[k]++
	first := shadowRecord.counts[k] == 1
	shadowRecord.mu.Unlock()
	if l := shadowLog.Load(); first && l != nil {
		l.Warn("access model disagrees with today's check (ADR 0080 shadow)", "check", k)
	}
}

// permOf is the permission a check names. A check that names none is a
// bug in Dock (TestEveryCheckHasAPermission keeps them out): it is
// counted where operators and the test suites' gates see it, logged as an
// error the first time, and people's grants refuse it rather than guess.
func permOf(a Actor, key actionKey, place string) (Perm, bool) {
	if p, ok := permFor(key); ok {
		return p, true
	}
	k := fmt.Sprintf("%s %s: %q at %s: no permission names this check (unmapped; people are refused)", a.Kind, key.need, key.action, place)
	shadowRecord.mu.Lock()
	if shadowRecord.counts == nil {
		shadowRecord.counts = map[string]int64{}
	}
	shadowRecord.counts[k]++
	first := shadowRecord.counts[k] == 1
	shadowRecord.mu.Unlock()
	if l := shadowLog.Load(); first && l != nil {
		l.Error("a permission check names no permission, so people are refused it: add it to actionPerms (ADR 0080)", "check", k)
	}
	return 0, false
}

func verdict(ok bool) string {
	if ok {
		return "allows"
	}
	return "refuses"
}

// ShadowReport is every disagreement seen since the daemon started, most
// frequent first: empty means the model agreed with every check.
func ShadowReport() []apitypes.AccessDisagreement {
	shadowRecord.mu.Lock()
	defer shadowRecord.mu.Unlock()
	out := make([]apitypes.AccessDisagreement, 0, len(shadowRecord.counts))
	for k, n := range shadowRecord.counts {
		out = append(out, apitypes.AccessDisagreement{Check: k, Count: n})
	}
	slices.SortFunc(out, func(x, y apitypes.AccessDisagreement) int {
		if x.Count != y.Count {
			return int(y.Count - x.Count)
		}
		if x.Check < y.Check {
			return -1
		}
		return 1
	})
	return out
}

// grantEntry is one place's permissions in an actor's access: project and
// env 0 mean every one.
type grantEntry struct {
	project, env int64
	// kinds are the environment kinds it covers (kindProduction, ...).
	kinds uint8
	perms Perms
}

// seededPerms is a scope's seeded group outside the Data panel.
func seededPerms(s apitypes.Scope) Perms {
	switch s {
	case apitypes.ScopeAdmin:
		return seedAdmins &^ dataPerms
	case apitypes.ScopeDeploy:
		return seedDevelopers &^ dataPerms
	case apitypes.ScopeRead:
		return seedViewers &^ dataPerms
	}
	return 0
}

// modelAccess is the actor's access as the migration will make it, from
// today's state: its role (a person, a `dock login`) or scope (a token)
// as its seeded group's permissions, at its limit's place. It needs no
// query: everything is on the actor.
func (a Actor) modelAccess() grantEntry {
	return grantEntry{project: a.ProjectID, env: a.EnvironmentID, kinds: uint8(allKinds), perms: seededPerms(a.Scope)}
}

// shadowed reports whether the actor's checks are compared: people and
// tokens acting in an org. The system and platform credentials have no
// org access to model.
func (a Actor) shadowed() bool {
	return a.Kind != "system" && !a.platformOnly && a.OrgID != 0 && !a.hasGrants
}

// modelAllows is the model's answer for perm: at the org (project and env
// 0), in a project (env 0) or in one environment. An org or project
// permission asked of the org needs a grant on the whole org; a project
// one in a project a grant on the whole project. An environment
// permission asked of the org is held anywhere (a list filters what it
// shows); asked of a project, one that only reads is held anywhere in it,
// and one that changes needs the whole project.
// A grant for some environment kinds only (kinds) covers environments of
// those kinds; org and project permissions, and changes to a project,
// need one for every kind.
func modelAllows(g grantEntry, perm Perm, project, env int64, kind uint8) bool {
	if !g.perms.has(perm) {
		return false
	}
	info := permTable[perm]
	inProject := g.project == 0 || g.project == project
	every := g.kinds == uint8(allKinds)
	switch {
	case info.level == PermOrg || info.level == PermProject && project == 0:
		return g.project == 0 && g.env == 0 && every
	case info.level == PermProject:
		return inProject && g.env == 0 && every
	case project == 0:
		return true
	case env == 0:
		return inProject && (info.read || g.env == 0 && every)
	}
	return inProject && (g.env == 0 || g.env == env) && g.kinds&kind != 0
}

// placeOf is the kind of place a check asks about, as disagreements name
// it.
func placeOf(project, env int64) string {
	switch {
	case env != 0:
		return "an environment"
	case project != 0:
		return "a project"
	}
	return "the org"
}

// shadow compares today's answer to a check with the model's.
// A person's model answer comes from their real groups and grants while
// the model only watches (shadow mode); a token's, which has none yet,
// from today's scope and limit as the migration will make them. kind is
// the environment's (envKind), 0 at the org or a project.
func (a Actor) shadow(need apitypes.Scope, action string, project, env int64, kind uint8, old bool) {
	if !a.shadowed() {
		return
	}
	key := actionKey{need, action}
	place := placeOf(project, env)
	perm, ok := permOf(a, key, place)
	if !ok {
		return
	}
	model := false
	if a.watchGrants {
		for _, g := range a.grants {
			if modelAllows(g, perm, project, env, kind) {
				model = true
				break
			}
		}
	} else {
		model = modelAllows(a.modelAccess(), perm, project, env, kindOther)
	}
	if model != old {
		recordShadow(a, key, place, old, model, " ("+perm.String()+")")
	}
}

// shadowData compares a person's data access in an environment (a user,
// or a `dock login` acting for one) with the model's: their seeded group
// holds, per environment kind, what the org's default rule for their role
// gives (else the platform's default, else Dock's built-in), and a
// project's or environment's rule that gives more is one more grant
// there. Grants only add, so where today a more specific rule gives less,
// the two disagree: that rule can't move into the model as it is.
// Agents' data access is their tokens' own, compared once phase 4 moves
// it into their grants.
func shadowData(in permissionInput, got apitypes.DataPermission) {
	a := in.actor
	// People's data access stays on the data policy until it moves into
	// grants; this keeps finding the rules that can't.
	if a.Kind == "system" || a.platformOnly || a.OrgID == 0 || isAgent(a) || rank(a.dataScope()) >= rank(apitypes.ScopeAdmin) {
		return
	}
	subject := subjectOf(a.dataScope())
	model := in.platform.builtIn(subject, in.kind)
	for _, r := range in.rules.scoped() {
		if r.Subject != subject || r.Kind != in.kind {
			continue
		}
		if r.From == apitypes.PolicyDefault {
			// The org's default replaces the built-in one.
			model.Level, model.Sensitive = r.Level, r.Sensitive
		}
	}
	for _, r := range in.rules.scoped() {
		if r.Subject != subject || r.Kind != in.kind || r.From == apitypes.PolicyDefault {
			continue
		}
		if !model.Level.Includes(r.Level) {
			model.Level = r.Level
		}
		if r.Sensitive == apitypes.SensitiveVisible {
			model.Sensitive = apitypes.SensitiveVisible
		}
	}
	capped := in.platform.capPermission(apitypes.DataPermission{Level: model.Level, Sensitive: model.Sensitive}, false, subject, in.kind)
	if capped.Level != got.Level || capped.Sensitive != got.Sensitive {
		key := actionKey{a.Scope, "data access in a " + string(in.kind) + " environment"}
		why := fmt.Sprintf(" (today %s, sensitive %s; the model %s, sensitive %s", got.Level, got.Sensitive, capped.Level, capped.Sensitive)
		if capped.Level.Includes(got.Level) {
			// More in the model: a specific rule gives less than a wider
			// one, which grants can't say.
			why += "; " + loweringRule
		}
		recordShadow(a, key, in.project+"/"+in.env, true, false, why+")")
	}
}

// loweringRule marks a disagreement from a data policy rule that gives
// less than a wider one: the migration's preflight stops on these, so
// they are reported, not a fault of the model (TestMain skips them).
const loweringRule = "a lowering rule: the migration's preflight stops on it"

// AccessDisagreements is ShadowReport for an operator (platform.view):
// whether the permission model agreed with every check since the daemon
// started, before it decides (ADR 0080, phase 3).
func (c *Core) AccessDisagreements(_ context.Context, a Actor) ([]apitypes.AccessDisagreement, error) {
	if err := a.requirePlatform(PlatformView, "reading the permission model's disagreements"); err != nil {
		return nil, err
	}
	return ShadowReport(), nil
}
