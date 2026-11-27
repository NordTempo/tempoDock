package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
)

// CloneEnvironment creates environment req.Name as a copy of from, in the
// same project (ADR 0022). It is placed like any new environment of its
// kind (ADR 0015) and starts with nothing deployed, unless req.Deploy.
//
// Copied, as they are live in from: every service that isn't archived
// (settings, template and data panel settings), its volumes (as new, empty
// volumes staged until the service's first deploy) and the variables.
// Variables are live in the clone at once, so a first deploy or promote
// sees them all. A value that is nothing but references is always copied
// (it holds no secret), except one that references an organization
// variable, which is copied only for an actor who may add such a
// reference (addsOrgRefs) and skipped otherwise. A literal value is
// copied only if the actor may read it (admin, as for --reveal) and, when the clone's kind differs from
// from's, only with req.CopyValues (admin only), so production's secrets
// and external database URLs never reach a non-production environment, or
// the reverse, by default. Otherwise a template service's own variables
// are set anew (a new password, the template's defaults) and the rest are
// skipped and listed. Never copied: domains, git connections, releases,
// backups and staged changes; the result counts them.
//
// With req.Deploy the images serving in from are deployed through
// promote's path, each release taking its service's staged volumes with
// it. Needs read on from and deploy on the project.
func (c *Core) CloneEnvironment(ctx context.Context, a Actor, from EnvRef, req apitypes.CloneEnvironmentRequest) (apitypes.CloneResult, error) {
	out := apitypes.CloneResult{
		From: from.Environment, Services: []string{}, Generated: []apitypes.VariableRef{}, Skipped: []apitypes.VariableRef{},
		CopiedAcrossKinds: []apitypes.VariableRef{}, NotCopied: []string{},
	}
	src, err := c.envFor(ctx, a, from, apitypes.ScopeRead, "cloning an environment")
	if err != nil {
		return out, err
	}
	p := src.project
	if err := a.checkProject(apitypes.ScopeDeploy, "cloning an environment", p); err != nil {
		return out, err
	}
	if err := notArchived(p); err != nil {
		return out, err
	}
	if err := validName("environment", req.Name); err != nil {
		return out, err
	}
	kind := req.Kind
	if kind == "" {
		kind = defaultKind(req.Name)
	}
	if !validKind(kind) {
		return out, invalid("kind must be production or non_production")
	}
	if req.CopyValues {
		// Copying values reads them where they are.
		if err := a.mayReveal(src, "copying values into a clone"); err != nil {
			return out, err
		}
	}
	out.SourceKind = apitypes.EnvironmentKind(src.env.Kind)
	cross := out.SourceKind != kind
	copyLiterals := a.revealsIn(src) && (!cross || req.CopyValues)
	rules := cloneRules{literal: func(apitypes.VariableRef) bool { return copyLiterals }, noOrgRefs: !a.addsOrgRefs()}

	var dst envScope
	var left clonedLeft
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		e, err := c.createEnv(ctx, q, p, req.Name, kind)
		if err != nil {
			return err
		}
		dst = envScope{p, e}
		left, err = c.cloneInto(ctx, q, src, dst, rules, cross, &out)
		return err
	})
	if store.IsUnique(err) {
		return out, conflict("environment %q already exists in project %s", req.Name, p.Name)
	}
	if err != nil {
		return out, err
	}
	srv, err := c.store.GetServer(ctx, dst.env.ServerID)
	if err != nil {
		return out, err
	}
	out.Environment = c.toEnvironment(a, p, dst.env, srv)
	out.NotCopied = left.describe()
	c.audit(ctx, a, "environment.clone", &p, &dst.env, dst.String(), cloneSummary(src, out))

	if req.Deploy {
		res, err := c.promote(ctx, a, src, dst, nil, true)
		if err != nil {
			out.DeployError = err.Error()
		} else {
			out.Deploy = &res
		}
	}
	return out, nil
}

// clonedLeft counts what a clone found in its source and left out.
type clonedLeft struct{ domains, git, staged int }

// describe lists what was left out for the result: counts where there
// were any, then what a clone never copies.
func (l clonedLeft) describe() []string {
	var out []string
	count := func(n int, one, many string) {
		switch {
		case n == 1:
			out = append(out, "1 "+one)
		case n > 1:
			out = append(out, fmt.Sprintf("%d %s", n, many))
		}
	}
	count(l.domains, "domain", "domains")
	count(l.git, "git connection", "git connections")
	count(l.staged, "staged change", "staged changes")
	return append(out, "releases", "backups")
}

// cloneInto copies src's live services, volumes and variables into the
// new environment dst, within the clone's transaction, filling in out.
// rules say which values are copied; cross says dst is of the other
// kind, so copied literals are listed in out.CopiedAcrossKinds.
func (c *Core) cloneInto(ctx context.Context, q *store.Queries, src, dst envScope, rules cloneRules, cross bool, out *apitypes.CloneResult) (clonedLeft, error) {
	var left clonedLeft
	svcs, err := q.ListServices(ctx, src.env.ID)
	if err != nil {
		return left, err
	}
	vols, err := q.ListEnvironmentVolumes(ctx, src.env.ID)
	if err != nil {
		return left, err
	}
	vars, err := q.ListVariables(ctx, src.env.ID)
	if err != nil {
		return left, err
	}
	domains, err := q.ListEnvironmentDomains(ctx, src.env.ID)
	if err != nil {
		return left, err
	}
	gits, err := q.ListProjectGitConnections(ctx, src.project.ID)
	if err != nil {
		return left, err
	}

	// clones maps a source service's ID to its clone; archived services
	// aren't cloned, and neither is anything of theirs.
	type clone struct {
		svc store.Service
		// gen are the template's variables made anew, for literal values
		// that aren't copied; nil for a service without a template.
		gen map[string]string
	}
	clones := map[int64]clone{}
	for _, s := range svcs {
		if s.ArchivedAt != nil {
			continue
		}
		left.staged += boolCount(s.PendingSettings != nil)
		ns, err := copyService(ctx, q, dst, s, s.Name)
		if err != nil {
			return left, err
		}
		cl := clone{svc: ns}
		if t, ok := templateOf(ns); ok {
			cl.gen = t.vars(ns.Name, newPassword())
		}
		clones[s.ID] = cl
		out.Services = append(out.Services, s.Name)
	}
	for _, v := range vols {
		cl, ok := clones[v.ServiceID]
		switch {
		case !ok:
		case v.Pending:
			left.staged++
		default:
			ss := svcScope{dst, cl.svc}
			if _, err := q.CreateVolume(ctx, store.CreateVolumeParams{
				ServiceID: cl.svc.ID, Name: v.Name, MountPath: v.MountPath, Pending: true, DockerName: volumeName(ss, v.Name),
			}); err != nil {
				return left, err
			}
			out.Volumes++
		}
	}
	for _, v := range vars {
		var cl clone
		var serviceID *int64
		if v.ServiceID != nil {
			var ok bool
			if cl, ok = clones[*v.ServiceID]; !ok {
				continue
			}
			serviceID = &cl.svc.ID
		}
		left.staged += boolCount(v.Pending != "")
		if v.Value == nil {
			continue // never live: only staged
		}
		name := apitypes.VariableRef{Service: cl.svc.Name, Key: v.Key}
		value, how, err := c.clonedValue(v, variableAAD(dst.env.ID, serviceID, v.Key), cl.gen, rules.literal(name), rules)
		if err != nil {
			return left, err
		}
		switch how {
		case cloneSkip:
			out.Skipped = append(out.Skipped, name)
			continue
		case cloneGenerated:
			out.Generated = append(out.Generated, name)
		case cloneLiteral:
			out.Variables++
			if cross {
				out.CopiedAcrossKinds = append(out.CopiedAcrossKinds, name)
			}
		default:
			out.Variables++
		}
		if err := q.InsertLiveVariable(ctx, store.InsertLiveVariableParams{
			EnvironmentID: dst.env.ID, ServiceID: serviceID, Key: v.Key, Value: value, UpdatedAt: now(),
		}); err != nil {
			return left, err
		}
	}
	for _, d := range domains {
		if _, ok := clones[d.ServiceID]; ok {
			left.domains++
		}
	}
	for _, g := range gits {
		if _, ok := clones[g.ServiceID]; ok {
			left.git++
		}
	}
	return left, nil
}

// copyService creates, in q's transaction, a service called name in dst
// with s's live settings, template and data panel settings: no volumes,
// variables, domains or git connection (cloneInto, and a volume backup
// restored into a copy).
func copyService(ctx context.Context, q *store.Queries, dst envScope, s store.Service, name string) (store.Service, error) {
	ns, err := q.CreateService(ctx, store.CreateServiceParams{
		EnvironmentID: dst.env.ID, Name: name, Image: s.Image, Port: s.Port,
		HealthCheckPath: s.HealthCheckPath, HealthCheckTimeoutSeconds: s.HealthCheckTimeoutSeconds,
		StartCommand: s.StartCommand, DockerfilePath: s.DockerfilePath, BuildCommand: s.BuildCommand,
		MemoryMb: s.MemoryMb, Cpus: s.Cpus,
		HealthCheckIntervalSeconds: s.HealthCheckIntervalSeconds, HealthCheckProbeTimeoutSeconds: s.HealthCheckProbeTimeoutSeconds,
		HealthCheckRetries: s.HealthCheckRetries, HealthCheckStartPeriodSeconds: s.HealthCheckStartPeriodSeconds,
		PreDeployCommand: s.PreDeployCommand, PreDeployTimeoutSeconds: s.PreDeployTimeoutSeconds,
		PreDeploySnapshot: s.PreDeploySnapshot, SecurityHeaders: s.SecurityHeaders,
	})
	if err != nil {
		return ns, err
	}
	if s.Template != "" || s.Database != "" || s.DatabaseUrlVariable != "" {
		if err := q.SetServiceCopiedSettings(ctx, store.SetServiceCopiedSettingsParams{
			Template: s.Template, Database: s.Database, DatabaseUrlVariable: s.DatabaseUrlVariable, ID: ns.ID,
		}); err != nil {
			return ns, err
		}
		ns.Template, ns.Database, ns.DatabaseUrlVariable = s.Template, s.Database, s.DatabaseUrlVariable
	}
	return ns, nil
}

// cloneHow says how a clone set a variable.
type cloneHow int

const (
	cloneCopied    cloneHow = iota // the source's references
	cloneLiteral                   // the source's literal value
	cloneGenerated                 // the template's, made anew
	cloneSkip                      // not created
)

// clonedValue is the value a variable gets in a clone, sealed for where
// the clone stores it (to): the source's, when it is nothing but
// references (unless rules.orgRefsLiteral and one names an org variable)
// or copyLiteral is set; otherwise the template's new value
// for key (gen), if it has one. With rules.noOrgRefs a value that
// references an org variable is never the source's. A copied value is
// sealed again: the box binds each value to its row.
func (c *Core) clonedValue(src store.Variable, to secrets.AAD, gen map[string]string, copyLiteral bool, rules cloneRules) ([]byte, cloneHow, error) {
	plain, err := c.openValue(src.Value, variableAAD(src.EnvironmentID, src.ServiceID, src.Key), src.Key)
	if err != nil {
		return nil, cloneSkip, err
	}
	value, how := *plain, cloneCopied
	orgRef := refersToOrg(value)
	switch v, generated := gen[src.Key]; {
	case orgRef && rules.noOrgRefs && generated:
		value, how = v, cloneGenerated
	case orgRef && rules.noOrgRefs:
		return nil, cloneSkip, nil
	case onlyReferences(value) && (!rules.orgRefsLiteral || !orgRef):
	case copyLiteral:
		how = cloneLiteral
	case generated:
		value, how = v, cloneGenerated
	default:
		return nil, cloneSkip, nil
	}
	b, err := c.box.Seal([]byte(value), to)
	return b, how, err
}

// cloneRules say which of the source's variable values a clone copies.
type cloneRules struct {
	// literal says whose literal values are copied: everyone's or no
	// one's for CloneEnvironment, the listed ones for a preview.
	literal func(apitypes.VariableRef) bool
	// orgRefsLiteral treats a reference to an org variable
	// (${{org.KEY}}) as a literal value: it reads a secret the org holds,
	// which a preview gets only when listed (ADR 0056).
	orgRefsLiteral bool
	// noOrgRefs leaves out every value that references an org variable,
	// for a caller who may not add such a reference by hand
	// (addsOrgRefs): the clone would hand them the org's secret in an
	// environment where they choose what runs.
	noOrgRefs bool
}

// refersToOrg reports whether a value references an org variable.
func refersToOrg(v string) bool {
	for _, m := range refRe.FindAllStringSubmatch(v, -1) {
		if m[1] == orgScope {
			return true
		}
	}
	return false
}

// onlyReferences reports whether a value is one or more ${{...}}
// references and nothing else: it wires services together and holds no
// secret of its own.
func onlyReferences(v string) bool {
	return v != "" && refRe.ReplaceAllString(v, "") == ""
}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}

// count is "1 service" or "2 services".
func count(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// cloneSummary is a clone's audit line. It names no server: the org's
// audit log never does (ADR 0062).
func cloneSummary(src envScope, r apitypes.CloneResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "cloned %s into a %s environment: %s, %s, %s",
		src.env.Name, kindWords(r.Environment.Kind),
		count(len(r.Services), "service"), count(r.Variables, "variable"), count(r.Volumes, "volume"))
	if len(r.Generated) > 0 {
		fmt.Fprintf(&b, "; set anew: %s", joinNames(r.Generated))
	}
	if len(r.Skipped) > 0 {
		fmt.Fprintf(&b, "; values not copied: %s", joinNames(r.Skipped))
	}
	if len(r.CopiedAcrossKinds) > 0 {
		fmt.Fprintf(&b, "; values copied from %s: %s", kindWords(r.SourceKind), joinNames(r.CopiedAcrossKinds))
	}
	return b.String()
}

// joinNames lists variables as references name them.
func joinNames(names []apitypes.VariableRef) string {
	s := make([]string, len(names))
	for i, n := range names {
		s[i] = n.String()
	}
	return strings.Join(s, ", ")
}
