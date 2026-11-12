package core

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Deploy diagnoses (ADR 0071): when a release fails, deployment.fail
// captures what explains it (captureContainer before its container goes,
// then recordDiagnosis) and stores it as one bounded bundle, with the
// likely cause from the rules in diagnose.go. Nothing runs while nothing
// fails: no timer, goroutine or cache. Log lines are stored as written
// and masked when read, as logs are (logRedactor).

const (
	// diagContainerLines, diagBuildLines and diagPreDeployLines are the
	// tails a bundle keeps; diagLineBytes cuts a longer line.
	diagContainerLines = 60
	diagBuildLines     = 40
	diagPreDeployLines = 40
	diagLineBytes      = 400
	// diagMaxBytes caps a bundle's JSON: older log lines go first.
	diagMaxBytes = 48 << 10
	// diagKeep is how many diagnoses a service keeps.
	diagKeep = 20
	// diagReadBudget bounds the node calls made before the container is
	// removed, so a failure is never held up long.
	diagReadBudget = 5 * time.Second
	// diagErrorsWindow is how long before the release started an error
	// issue counts as around the failure; diagErrorsMax how many a bundle
	// lists. diagStatsWindow is the stats it keeps.
	diagErrorsWindow = 10 * time.Minute
	diagErrorsMax    = 5
	diagStatsWindow  = 15 * time.Minute
)

// containerCapture is what was read from a release's container: a failed
// one's before it was removed, or a running one's.
type containerCapture struct {
	exit  *apitypes.DiagnosisExit
	lines []string
	// healthOutput is what its ongoing health check's last failed probe
	// said (ADR 0043).
	healthOutput string
}

// captureContainer reads the failed release's container while it still
// exists.
func (d *deployment) captureContainer(ctx context.Context) containerCapture {
	return readContainer(ctx, d.rt, d.rel.ContainerID, d.cfg.MemoryMB)
}

// readContainer reads a container's state and last lines within
// diagReadBudget. One that can't be read (an offline server) leaves
// them empty.
func readContainer(ctx context.Context, rt node.Runtime, id string, memoryMB int) containerCapture {
	var out containerCapture
	if id == "" {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, diagReadBudget)
	defer cancel()
	if ctr, err := rt.Inspect(ctx, id); err == nil {
		out.exit = &apitypes.DiagnosisExit{
			Running: ctr.State == node.StateRunning, OOMKilled: ctr.OOMKilled,
			MemoryLimitMB: memoryMB, Restarts: ctr.RestartCount,
		}
		if !out.exit.Running {
			out.exit.ExitCode = ptr(ctr.ExitCode)
		}
		out.healthOutput = ctr.HealthOutput
	}
	lines, err := rt.Logs(ctx, id, node.LogOptions{Tail: diagContainerLines})
	if err != nil {
		return out
	}
	for l := range lines {
		out.lines = append(out.lines, diagLogLine(l))
	}
	if n := len(out.lines); n > diagContainerLines {
		out.lines = out.lines[n-diagContainerLines:]
	}
	return out
}

// diagLogLine is a container line as logs show it to agents: its time,
// [stderr] for stderr, the text, cut at diagLineBytes.
func diagLogLine(l node.LogLine) string {
	s := l.Time.UTC().Format(time.RFC3339) + " "
	if l.Stream == "stderr" {
		s += "[stderr] "
	}
	return cutLine(s + l.Text)
}

// cutLine cuts s at diagLineBytes on a rune boundary.
func cutLine(s string) string {
	if len(s) <= diagLineBytes {
		return s
	}
	i := diagLineBytes
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + "…"
}

// recordDiagnosis assembles and stores the diagnosis of a release that
// just failed with code and msg. Best effort: the failure is recorded
// whatever happens here.
func (d *deployment) recordDiagnosis(ctx context.Context, code apitypes.ErrorCode, msg string, offline bool, cc containerCapture) {
	c := d.c
	if c.ctx.Err() != nil {
		return
	}
	bundle, cause, err := d.assemble(ctx, code, msg, offline, cc)
	if err == nil {
		_, err = c.storeDiagnosis(ctx, d.ss.svc.ID, d.rel.ID, bundle, cause)
	}
	if err != nil {
		c.log.Warn("record deploy diagnosis", "service", d.ss.String(), "release", d.rel.Number, "err", err)
	}
}

// assemble gathers a failed release's signals, runs the rules and builds
// its bundle.
func (d *deployment) assemble(ctx context.Context, code apitypes.ErrorCode, msg string, offline bool, cc containerCapture) (apitypes.Diagnosis, apitypes.DiagnosisCause, error) {
	c := d.c
	at := now()
	b := apitypes.Diagnosis{
		Kind: apitypes.DiagnosisRelease, Release: d.rel.Number, CapturedAt: at,
		Failure: apitypes.DiagnosisFailure{
			Step: apitypes.ReleaseStatus(d.rel.Status), ErrorCode: code, Error: msg,
			DurationMS: at.Sub(d.rel.CreatedAt).Milliseconds(), Exit: cc.exit,
		},
		AlsoMatched: []string{}, Checked: []apitypes.DiagnosisCheck{},
		ContainerTail: []apitypes.DiagnosisLine{}, BuildTail: []apitypes.DiagnosisLine{}, Errors: []apitypes.DiagnosisError{},
		PreDeploy: d.pre,
	}
	in := diagInput{
		kind: apitypes.DiagnosisRelease, service: d.ss.svc.Name, environment: d.ss.env.Name,
		step: b.Failure.Step, code: code, err: msg, offline: offline,
		pulled: apitypes.ReleaseSource(d.rel.Source) != apitypes.SourceUpload,
		cfg:    d.cfg, exit: cc.exit, pre: d.pre, container: cc.lines,
		build: tailFile(c.buildLogPath(d.rel.ID), diagBuildLines),
	}
	if in.inHealthCheck() {
		in.health = node.ParseHealthFailure(msg)
		b.Health = healthOf(d.cfg, in.health)
		if in.health.Kind == node.HealthFailOOM && b.Failure.Exit != nil {
			b.Failure.Exit.OOMKilled = true // Docker clears it on restart
		}
	}
	last, err := c.lastGoodRelease(ctx, d.ss.svc.ID, d.rel.Number)
	if err != nil {
		return b, apitypes.DiagnosisCause{}, err
	}
	if last != nil {
		lastCfg, err := c.openConfig(*last)
		if err != nil {
			return b, apitypes.DiagnosisCause{}, err
		}
		in.last = &lastCfg
		b.LastGoodRelease = last.Number
		b.Changes = releaseChanges(*last, lastCfg, d.rel, d.cfg)
	}

	fillDiagnosis(&b, diagnose(&in), cc.lines, in.build)
	if b.Errors, err = c.errorsAround(ctx, d.ss.svc.ID, d.rel.CreatedAt); err != nil {
		return b, b.Cause, err
	}
	if b.Resources, err = c.resourcesBefore(ctx, d.ss.svc.ID, at, d.old); err != nil {
		return b, b.Cause, err
	}
	return b, b.Cause, nil
}

// fillDiagnosis puts the rules' answer and the log tails in b, the
// container lines the cause matched marked.
func fillDiagnosis(b *apitypes.Diagnosis, res diagResult, container, build []string) {
	b.Cause, b.AlsoMatched, b.Checked = res.cause, append(b.AlsoMatched, res.also...), append(b.Checked, res.checked...)
	for i, l := range container {
		b.ContainerTail = append(b.ContainerTail, apitypes.DiagnosisLine{Text: l, Matched: slices.Contains(res.matched, i)})
	}
	for _, l := range build {
		b.BuildTail = append(b.BuildTail, apitypes.DiagnosisLine{Text: l})
	}
}

// newDiagnosis is an empty bundle of kind for release, its lists empty
// rather than null.
func newDiagnosis(kind apitypes.DiagnosisKind, release int64, at time.Time) apitypes.Diagnosis {
	return apitypes.Diagnosis{
		Kind: kind, Release: release, CapturedAt: at,
		AlsoMatched: []string{}, Checked: []apitypes.DiagnosisCheck{},
		ContainerTail: []apitypes.DiagnosisLine{}, BuildTail: []apitypes.DiagnosisLine{}, Errors: []apitypes.DiagnosisError{},
	}
}

// diagnoseRunning captures the diagnosis of a service whose active
// release crash loops or stays unhealthy after a restart (kind), when
// its alert opens or turns critical, and stores it with that release.
// It reads the running container like a failed deploy's; the exit code
// and OOM kill come from the service status, since Docker resets them on
// each restart. nil when there is nothing to diagnose (no active
// release) or storing failed.
func (c *Core) diagnoseRunning(ctx context.Context, ss svcScope, kind apitypes.DiagnosisKind) *apitypes.DiagnosisRef {
	rel, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
	if err != nil {
		return nil
	}
	cfg, err := c.openConfig(rel)
	if err != nil {
		c.log.Warn("diagnose running service", "service", ss.String(), "err", err)
		return nil
	}
	cc := readContainer(ctx, c.runtime(ss.env.ServerID), rel.ContainerID, cfg.MemoryMB)
	st := c.getStatus(ss.svc.ID)
	at := now()
	b := newDiagnosis(kind, rel.Number, at)
	b.Failure.Step = apitypes.ReleaseActive
	if rel.FinishedAt != nil {
		b.Failure.DurationMS = at.Sub(*rel.FinishedAt).Milliseconds()
	}
	exit := cc.exit
	if exit == nil {
		exit = &apitypes.DiagnosisExit{MemoryLimitMB: cfg.MemoryMB}
	}
	exit.OOMKilled = exit.OOMKilled || st.OOMKilled
	exit.Restarts = max(exit.Restarts, st.RestartCount)
	in := diagInput{kind: kind, service: ss.svc.Name, environment: ss.env.Name, step: apitypes.ReleaseActive, cfg: cfg, exit: exit, container: cc.lines}
	switch kind {
	case apitypes.DiagnosisCrashLoop:
		exit.ExitCode = ptr(st.ExitCode) // the last crash's: inspect shows the run Docker restarted
		b.Failure.Error = fmt.Sprintf("restarted %d times; last exit code %d", exit.Restarts, st.ExitCode)
	case apitypes.DiagnosisUnhealthy:
		b.Failure.Error = "its health check keeps failing"
		if cc.healthOutput != "" {
			b.Failure.Error += ": " + cc.healthOutput
		}
		in.health = node.ParseHealthFailure(cc.healthOutput)
		b.Health = &apitypes.DiagnosisHealth{Kind: "tcp", Port: in.port(), TimeoutSeconds: cfg.HealthCheckProbeTimeout,
			Result: string(in.health.Kind), Status: in.health.Status}
		if cfg.HealthCheckPath != "" {
			b.Health.Kind, b.Health.Path = "http", cfg.HealthCheckPath
		}
	}
	in.err = b.Failure.Error
	b.Failure.Exit = exit
	if last, err := c.lastGoodRelease(ctx, ss.svc.ID, rel.Number); err == nil && last != nil {
		if lastCfg, err := c.openConfig(*last); err == nil {
			in.last = &lastCfg
			b.LastGoodRelease = last.Number
			b.Changes = releaseChanges(*last, lastCfg, rel, cfg)
		}
	}
	fillDiagnosis(&b, diagnose(&in), cc.lines, nil)
	if b.Errors, err = c.errorsAround(ctx, ss.svc.ID, at); err == nil {
		b.Resources, err = c.resourcesBefore(ctx, ss.svc.ID, at, &rel)
	}
	var id int64
	if err == nil {
		id, err = c.storeDiagnosis(ctx, ss.svc.ID, rel.ID, b, b.Cause)
	}
	if err != nil {
		c.log.Warn("record diagnosis", "service", ss.String(), "kind", kind, "err", err)
		return nil
	}
	// No release event: the alert engine would read the active release as
	// newly live. Open pages learn from the alert that names it.
	return &apitypes.DiagnosisRef{ID: id, Kind: kind, Rule: b.Cause.Rule, Summary: b.Cause.Summary}
}

// healthOf is what the deploy's health check probed, with its answer.
func healthOf(cfg releaseConfig, f node.HealthFailure) *apitypes.DiagnosisHealth {
	h := &apitypes.DiagnosisHealth{TimeoutSeconds: cfg.HealthCheckTimeout, Result: string(f.Kind), Status: f.Status}
	switch {
	case cfg.HealthCheckPath != "" && cfg.Published:
		h.Kind, h.Path, h.Port = "http", cfg.HealthCheckPath, cfg.Port
	case cfg.Published:
		h.Kind, h.Port = "tcp", cfg.Port
	case cfg.HealthPort != 0:
		h.Kind, h.Port = "tcp", cfg.HealthPort
	default:
		h.Kind = "running"
	}
	return h
}

// lastGoodRelease is the newest release before number that served, or
// nil: what a failed release is compared with.
func (c *Core) lastGoodRelease(ctx context.Context, serviceID, number int64) (*store.Release, error) {
	rows, err := c.store.ListReleases(ctx, store.ListReleasesParams{ServiceID: serviceID, Limit: 50})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.Number < number && (r.Status == string(apitypes.ReleaseActive) || r.Status == string(apitypes.ReleaseSuperseded)) {
			return &r, nil
		}
	}
	return nil, nil
}

// releaseChanges is what changed from the last good release to the failed
// one: the image, variable names, settings. Values are compared, never
// returned.
func releaseChanges(from store.Release, fromCfg releaseConfig, to store.Release, toCfg releaseConfig) *apitypes.DiagnosisChanges {
	out := &apitypes.DiagnosisChanges{
		Variables: apitypes.DiagnosisVariables{Added: []string{}, Removed: []string{}, Changed: []string{}},
		Settings:  []apitypes.DiagnosisSetting{},
	}
	if from.ImageID != to.ImageID || from.ImageRef != to.ImageRef {
		out.Image = &apitypes.DiagnosisImage{From: imageName(from), To: imageName(to), Commit: commitOf(to)}
	}
	for _, k := range slices.Sorted(maps.Keys(toCfg.Env)) {
		old, had := fromCfg.Env[k]
		switch {
		case !had:
			out.Variables.Added = append(out.Variables.Added, k)
		case old != toCfg.Env[k] && !platformVar(k):
			out.Variables.Changed = append(out.Variables.Changed, k)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(fromCfg.Env)) {
		if _, has := toCfg.Env[k]; !has {
			out.Variables.Removed = append(out.Variables.Removed, k)
		}
	}
	set := func(name, a, b string) {
		if a != b {
			out.Settings = append(out.Settings, apitypes.DiagnosisSetting{Name: name, From: a, To: b})
		}
	}
	set("port", strconv.Itoa(fromCfg.Port), strconv.Itoa(toCfg.Port))
	set("start_command", cmp.Or(fromCfg.Cmd, "image default"), cmp.Or(toCfg.Cmd, "image default"))
	set("health_check_path", cmp.Or(fromCfg.HealthCheckPath, "TCP"), cmp.Or(toCfg.HealthCheckPath, "TCP"))
	set("health_check_timeout", fmt.Sprintf("%ds", fromCfg.HealthCheckTimeout), fmt.Sprintf("%ds", toCfg.HealthCheckTimeout))
	set("memory", fmt.Sprintf("%d MB", fromCfg.MemoryMB), fmt.Sprintf("%d MB", toCfg.MemoryMB))
	set("cpus", strconv.FormatFloat(fromCfg.CPUs, 'g', -1, 64), strconv.FormatFloat(toCfg.CPUs, 'g', -1, 64))
	set("pre_deploy_command", cmp.Or(fromCfg.PreDeploy, "none"), cmp.Or(toCfg.PreDeploy, "none"))
	set("volumes", mountsText(fromCfg.Mounts), mountsText(toCfg.Mounts))
	return out
}

// platformVar: a variable the platform sets for every release (PORT,
// DOCK_*), whose value may move without anyone changing it.
func platformVar(k string) bool { return k == "PORT" || strings.HasPrefix(k, "DOCK_") }

// imageName names a release's image: its reference, with a short image ID
// when the reference doesn't change (a moved tag).
func imageName(r store.Release) string {
	id := strings.TrimPrefix(r.ImageID, "sha256:")
	if len(id) > 12 {
		id = id[:12]
	}
	if id == "" {
		return r.ImageRef
	}
	return r.ImageRef + " (" + id + ")"
}

// mountsText lists mounts as "volume at path".
func mountsText(ms []configMount) string {
	if len(ms) == 0 {
		return "none"
	}
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = m.Volume + " at " + m.Path
	}
	return strings.Join(parts, ", ")
}

// tailFile is the last n lines of a file, cut at diagLineBytes, read
// through with n lines in memory; nil when it doesn't exist.
func tailFile(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	ring := make([]string, 0, n)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(ring) == n {
			copy(ring, ring[1:])
			ring = ring[:n-1]
		}
		ring = append(ring, cutLine(line))
	}
	return ring
}

// tailLines are the last n lines of s, each cut at diagLineBytes.
func tailLines(s string, n int) []string {
	t := lastLines(s, n)
	if t == "" {
		return []string{}
	}
	out := strings.Split(t, "\n")
	for i, l := range out {
		out[i] = cutLine(strings.TrimRight(l, "\r"))
	}
	return out
}

// errorsAround are the service's error issues seen since diagErrorsWindow
// before the release started, newest first.
func (c *Core) errorsAround(ctx context.Context, serviceID int64, started time.Time) ([]apitypes.DiagnosisError, error) {
	rows, err := c.store.ListServiceErrorIssues(ctx, store.ListServiceErrorIssuesParams{ServiceID: serviceID, State: 0, MaxRows: diagErrorsMax})
	if err != nil {
		return nil, err
	}
	since := started.Add(-diagErrorsWindow)
	out := []apitypes.DiagnosisError{}
	for _, r := range rows {
		if r.LastSeen.Before(since) {
			break // newest first
		}
		out = append(out, apitypes.DiagnosisError{ID: r.ID, Title: cutLine(r.Title), Count: r.Count, FirstRelease: r.FirstRelease})
	}
	return out, nil
}

// resourcesBefore are the service's 1-minute samples in the
// diagStatsWindow before at (those of serving, the release that served
// meanwhile), or nil without any.
func (c *Core) resourcesBefore(ctx context.Context, serviceID int64, at time.Time, serving *store.Release) (*apitypes.DiagnosisResources, error) {
	rows, err := c.store.ListServiceStats(ctx, store.ListServiceStatsParams{Res: statsMinute, At: at.Add(-diagStatsWindow).Unix(), ServiceID: serviceID})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	out := &apitypes.DiagnosisResources{MemoryMB: make([]int, len(rows)), CPUPercent: make([]float64, len(rows))}
	for i, p := range rows {
		mb := int(p.Memory >> 20)
		out.MemoryMB[i], out.CPUPercent[i] = mb, float64(p.Cpu)/100
		out.PeakMemoryMB = max(out.PeakMemoryMB, mb)
	}
	if serving != nil {
		out.OfRelease = serving.Number
		out.MemoryLimitMB = c.memoryLimitMB(*serving)
	}
	return out, nil
}

// storeDiagnosis writes a bundle, cut to diagMaxBytes, trims the service
// to its newest diagKeep, and returns the new diagnosis's ID.
func (c *Core) storeDiagnosis(ctx context.Context, serviceID, releaseID int64, b apitypes.Diagnosis, cause apitypes.DiagnosisCause) (int64, error) {
	body, err := fitDiagnosis(&b)
	if err != nil {
		return 0, err
	}
	var id int64
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if id, err = q.AddDiagnosis(ctx, store.AddDiagnosisParams{
			ServiceID: serviceID, ReleaseID: releaseID, Kind: string(b.Kind), Rule: cause.Rule,
			Summary: cause.Summary, Bundle: string(body), CreatedAt: b.CapturedAt.Unix(),
		}); err != nil {
			return err
		}
		return q.TrimDiagnoses(ctx, store.TrimDiagnosesParams{ServiceID: serviceID, Keep: diagKeep})
	})
	return id, err
}

// fitDiagnosis encodes b, dropping its oldest log lines (container output
// first, then the build's, then the pre-deploy command's) until it fits
// diagMaxBytes.
func fitDiagnosis(b *apitypes.Diagnosis) ([]byte, error) {
	for {
		body, err := json.Marshal(b)
		if err != nil || len(body) <= diagMaxBytes {
			return body, err
		}
		b.Truncated = true
		switch {
		case len(b.ContainerTail) > 10:
			b.ContainerTail = b.ContainerTail[len(b.ContainerTail)/2:]
		case len(b.BuildTail) > 0:
			b.BuildTail = b.BuildTail[len(b.BuildTail)/2:]
		case b.PreDeploy != nil && len(b.PreDeploy.Tail) > 0:
			b.PreDeploy.Tail = b.PreDeploy.Tail[len(b.PreDeploy.Tail)/2:]
		case len(b.ContainerTail) > 0:
			b.ContainerTail = b.ContainerTail[1:]
		default:
			return body, nil // the rest is bounded by its own caps
		}
	}
}

// ListDiagnoses lists a service's diagnoses, newest first, without their
// bundles. release > 0 keeps that release's. Scope: read.
func (c *Core) ListDiagnoses(ctx context.Context, a Actor, ref ServiceRef, release int64, limit int) ([]apitypes.DiagnosisSummary, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading diagnoses")
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > diagKeep {
		limit = diagKeep
	}
	rows, err := c.store.ListDiagnoses(ctx, store.ListDiagnosesParams{ServiceID: ss.svc.ID, MaxRows: diagKeep})
	if err != nil {
		return nil, err
	}
	out := []apitypes.DiagnosisSummary{}
	for _, r := range rows {
		if release > 0 && r.ReleaseNumber != release {
			continue
		}
		out = append(out, apitypes.DiagnosisSummary{
			DiagnosisRef: apitypes.DiagnosisRef{ID: r.ID, Kind: apitypes.DiagnosisKind(r.Kind), Rule: r.Rule, Summary: r.Summary},
			Release:      r.ReleaseNumber, CapturedAt: time.Unix(r.CreatedAt, 0).UTC(),
		})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// GetDiagnosis returns one diagnosis of a service, its log lines masked
// for whoever can't reveal secret values. Scope: read.
func (c *Core) GetDiagnosis(ctx context.Context, a Actor, ref ServiceRef, id int64) (apitypes.Diagnosis, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading diagnoses")
	if err != nil {
		return apitypes.Diagnosis{}, err
	}
	row, err := c.store.GetDiagnosis(ctx, store.GetDiagnosisParams{ID: id, ServiceID: ss.svc.ID})
	if store.IsNotFound(err) {
		return apitypes.Diagnosis{}, notFound("diagnosis %d of %s not found; `dock diagnose` lists the service's newest", id, ss)
	}
	if err != nil {
		return apitypes.Diagnosis{}, err
	}
	var b apitypes.Diagnosis
	if err := json.Unmarshal([]byte(row.Bundle), &b); err != nil {
		return b, fmt.Errorf("diagnosis %d: %w", id, err)
	}
	b.ID, b.Kind, b.Release = id, apitypes.DiagnosisKind(row.Kind), row.ReleaseNumber
	b.Project, b.Environment, b.Service = ss.project.Name, ss.env.Name, ss.svc.Name
	rel, err := c.store.GetRelease(ctx, row.ReleaseID)
	if err != nil {
		return b, err
	}
	red, err := c.logRedactor(a, rel)
	if err != nil {
		return b, err
	}
	b.Trace = c.failedTrace(ctx, ss, rel, b.CapturedAt)
	maskDiagnosis(&b, red)
	return b, nil
}

// failedTrace is rel's newest failed trace from 10 minutes before at to 2
// after, or nil: tracing off, none, or the trace store not answering (a
// diagnosis reads without it).
func (c *Core) failedTrace(ctx context.Context, ss svcScope, rel store.Release, at time.Time) *apitypes.DiagnosisTrace {
	cl, _ := c.tracesClient()
	if cl == nil {
		return nil
	}
	res, err := cl.Traces(ctx, analytics.TracesQuery{
		Org: ss.project.OrgID, Services: []int64{ss.svc.ID}, Release: rel.ID, Status: analytics.TraceError,
		From: at.Add(-10 * time.Minute).UnixMicro(), To: at.Add(2 * time.Minute).UnixMicro(), Limit: 1,
	})
	if err != nil || len(res.Traces) == 0 {
		return nil
	}
	t := res.Traces[0]
	name := t.Name
	if t.Route != "" {
		name = strings.TrimSpace(t.Method + " " + t.Route)
	}
	return &apitypes.DiagnosisTrace{ID: t.Trace.String(), At: time.UnixMicro(t.Start).UTC(), Name: name, HTTPStatus: t.HTTPStatus, DurationMS: ms(t.Duration)}
}

// maskDiagnosis masks secret values in what a bundle quotes from logs.
// The summary is made from the rule's facts and quotes no log text.
func maskDiagnosis(b *apitypes.Diagnosis, red *redactor) {
	if red == nil {
		return
	}
	mask := func(s *string) { *s, _ = red.line(*s) }
	lines := func(ls []apitypes.DiagnosisLine) {
		for i := range ls {
			mask(&ls[i].Text)
		}
	}
	mask(&b.Failure.Error)
	mask(&b.Cause.Detail)
	if b.Trace != nil {
		mask(&b.Trace.Name) // the app's route may hold a value
	}
	lines(b.ContainerTail)
	lines(b.BuildTail)
	if b.PreDeploy != nil {
		lines(b.PreDeploy.Tail)
	}
}

// diagnosisRefs are a service's release diagnoses by release ID, the
// newest per release: one query for a release list.
func (c *Core) diagnosisRefs(ctx context.Context, serviceID int64) (map[int64]*apitypes.DiagnosisRef, error) {
	rows, err := c.store.ListServiceDiagnosisRefs(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*apitypes.DiagnosisRef, len(rows))
	for _, r := range rows {
		out[r.ReleaseID] = &apitypes.DiagnosisRef{ID: r.ID, Kind: apitypes.DiagnosisKind(r.Kind), Rule: r.Rule, Summary: r.Summary}
	}
	return out, nil
}

// diagnosisRef is a release's newest diagnosis, or nil.
func (c *Core) diagnosisRef(ctx context.Context, releaseID int64) (*apitypes.DiagnosisRef, error) {
	r, err := c.store.GetReleaseDiagnosisRef(ctx, releaseID)
	if store.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &apitypes.DiagnosisRef{ID: r.ID, Kind: apitypes.DiagnosisKind(r.Kind), Rule: r.Rule, Summary: r.Summary}, nil
}

// pruneDiagnoses deletes diagnoses older than build_log_days (the
// retention pass, ADR 0020).
func (c *Core) pruneDiagnoses(ctx context.Context, t time.Time) (int64, error) {
	return c.store.PruneDiagnoses(ctx, t.Add(-days(c.cfg.Retention.BuildLogDays)).Unix())
}
