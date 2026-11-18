package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// errDryRun rolls back a dry run's transaction.
var errDryRun = errors.New("dry run")

// SwitchReferences rewrites every ${{from.KEY}} reference in the
// environment's variables (shared and every service's but from's and
// to's own) to ${{to.KEY}} and applies it, so the services using from
// get config releases (ADR 0061): "use the restored database", and the
// same to switch back. It stages and applies like `dock env set --apply`,
// so it refuses while the environment has other staged changes, which
// would go live with it. to must be a live service of the environment
// with every key referenced, and not one whose restore is still running
// or failed (restoredTarget): that one doesn't hold the data. With DryRun nothing changes and Services
// says who would. Scope: deploy.
func (c *Core) SwitchReferences(ctx context.Context, a Actor, ref ServiceRef, req apitypes.SwitchReferencesRequest) (apitypes.SwitchReferencesResult, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "switching references")
	if err != nil {
		return apitypes.SwitchReferencesResult{}, err
	}
	if err := notArchived(ss.project); err != nil {
		return apitypes.SwitchReferencesResult{}, err
	}
	if err := c.notMoving(ctx, ss.envScope, "switch references"); err != nil {
		return apitypes.SwitchReferencesResult{}, err
	}
	toName := strings.TrimSpace(req.To)
	if toName == ss.svc.Name {
		return apitypes.SwitchReferencesResult{}, invalid("the references already point at %s; give another service to switch to", ss.svc.Name)
	}
	to, err := c.loadService(ctx, a.OrgID, ServiceRef{ref.Project, ref.Environment, toName})
	if err != nil {
		return apitypes.SwitchReferencesResult{}, err
	}
	// Before the archived check: a failed restore's target is archived,
	// and this says why.
	if err := c.restoredTarget(ctx, to); err != nil {
		return apitypes.SwitchReferencesResult{}, err
	}
	if to.svc.ArchivedAt != nil || to.svc.Staged != "" {
		return apitypes.SwitchReferencesResult{}, invalid("service %s isn't live; switch to a running service of %s", to, ss.envScope)
	}
	out := apitypes.SwitchReferencesResult{From: ss.svc.Name, To: toName, Services: []string{}, Releases: []apitypes.ServiceRelease{}}
	changed := 0
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		staged, err := c.pendingChanges(ctx, q, ss.envScope, false)
		if err != nil {
			return err
		}
		if staged.Count > 0 {
			return conflict("%s has %d staged changes, which would go live with the switch; apply or discard them first (`dock apply`, `dock discard`)", ss.envScope, staged.Count)
		}
		svcs, err := q.ListServices(ctx, ss.env.ID)
		if err != nil {
			return err
		}
		vars, err := q.ListVariables(ctx, ss.env.ID)
		if err != nil {
			return err
		}
		live := map[int64]bool{}
		for _, s := range svcs {
			live[s.ID] = s.ArchivedAt == nil && s.ID != ss.svc.ID && s.ID != to.svc.ID
		}
		has := map[string]bool{}
		for _, v := range vars {
			if v.ServiceID != nil && *v.ServiceID == to.svc.ID && v.Value != nil {
				has[v.Key] = true
			}
		}
		var missing []string
		for _, v := range vars {
			if v.Value == nil || (v.ServiceID != nil && !live[*v.ServiceID]) {
				continue
			}
			val, err := c.openValue(v.Value, variableAAD(v.EnvironmentID, v.ServiceID, v.Key), v.Key)
			if err != nil {
				return err
			}
			if val == nil {
				continue
			}
			hit := false
			next := refRe.ReplaceAllStringFunc(*val, func(m string) string {
				sub := refRe.FindStringSubmatch(m)
				if sub[1] != ss.svc.Name {
					return m
				}
				hit = true
				if !has[sub[2]] && !slices.Contains(missing, sub[2]) {
					missing = append(missing, sub[2])
				}
				return "${{" + toName + "." + sub[2] + "}}"
			})
			if !hit {
				continue
			}
			if err := c.stageSet(ctx, q, ss.env.ID, v.ServiceID, v.Key, next); err != nil {
				return err
			}
			changed++
		}
		if len(missing) > 0 {
			slices.Sort(missing)
			return invalid("%s has no variable %s, which the environment references on %s; set it on %s first, or switch to another service",
				to, strings.Join(missing, ", "), ss.svc.Name, toName)
		}
		if changed == 0 {
			return nil
		}
		after, err := c.pendingChanges(ctx, q, ss.envScope, false)
		if err != nil {
			return err
		}
		out.Services = append(out.Services, after.Affected...)
		if req.DryRun {
			return errDryRun
		}
		return nil
	})
	if errors.Is(err, errDryRun) || (err == nil && (req.DryRun || changed == 0)) {
		return out, nil
	}
	if err != nil {
		return apitypes.SwitchReferencesResult{}, err
	}
	c.audit(ctx, a, "service.switch_references", &ss.project, &ss.env, ss.String(),
		fmt.Sprintf("switched %d references from %s to %s (%s)", changed, ss.svc.Name, toName, strings.Join(out.Services, ", ")))
	applied, err := c.applyEnv(ctx, a, ss.envScope, nil)
	if err != nil {
		return out, err
	}
	for _, r := range applied.Releases {
		out.Releases = append(out.Releases, apitypes.ServiceRelease{Service: r.Service, Number: r.Number})
	}
	return out, nil
}
