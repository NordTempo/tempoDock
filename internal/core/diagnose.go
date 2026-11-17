package core

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/errparse"
	"github.com/NordTempo/tempoDock/internal/node"
)

// The rules of a deploy diagnosis (ADR 0071): a small, ordered set, each
// matching on facts the platform has (an exit code, an OOM kill, the
// health check's answer, the release's variables) and, where it reads
// the app's output, a log line together with such a fact, never a log
// line alone. The first rule that matches is the likely cause; every
// rule that applies to the step the release failed in is listed with its
// verdict, so a reader sees what was ruled out. No rule matching is an
// answer too. diagnose is pure: capture.go gathers its input.

// diagInput is what the rules read: one failed release, or a running one
// that crash loops or turned unhealthy, and what was captured then.
type diagInput struct {
	kind                 apitypes.DiagnosisKind
	service, environment string
	step                 apitypes.ReleaseStatus
	code                 apitypes.ErrorCode
	err                  string
	// offline: the server's link dropped during the deploy.
	offline bool
	// pulled: the image was pulled, not built (an image or promote
	// release, or one whose image had to come back).
	pulled bool
	cfg    releaseConfig
	// last is the last good release's config, nil when none served.
	last   *releaseConfig
	health node.HealthFailure
	exit   *apitypes.DiagnosisExit
	pre    *apitypes.DiagnosisPreDeploy
	// stalled is a step that ran out of its deadline (deploy_phases.go).
	stalled *stalledPhase
	// container and build are the last lines of the container's output
	// and of the build or pull, as captured.
	container []string
	build     []string
	// listen caches listening.
	listen *listenAt
}

// diagVerdict is one rule's outcome. A rule that doesn't apply to the
// failure (skip) is left out of the checked list.
type diagVerdict struct {
	skip    bool
	matched bool
	why     string
	summary string
	detail  string
	fix     *apitypes.DiagnosisFix
	// lines are the indexes of the container lines it matched.
	lines []int
}

// diagRule is one rule: its ID and its check.
type diagRule struct {
	id    string
	check func(*diagInput) diagVerdict
}

// diagResult is the rules' answer.
type diagResult struct {
	cause   apitypes.DiagnosisCause
	also    []string
	checked []apitypes.DiagnosisCheck
	// matched are the container lines the cause matched.
	matched []int
}

// diagRun is the rules' kind for a failed one-off command or job run
// (runCause): not a stored diagnosis, only its cause is kept.
const diagRun apitypes.DiagnosisKind = "run"

// runCause is the likely cause of a run of cmd in a release with cfg
// that exited with code after printing output, or "" when no rule
// matched: the cause line of a failed run or job (ADR 0071, phase 3).
// That it exited is not a cause, so exited never is one here.
func runCause(cmd string, cfg releaseConfig, code int, output string) string {
	cfg.Cmd = cmd
	in := diagInput{kind: diagRun, cfg: cfg, container: tailLines(output, diagContainerLines),
		exit: &apitypes.DiagnosisExit{ExitCode: &code, MemoryLimitMB: cfg.MemoryMB}}
	if res := diagnose(&in); res.cause.Rule != "" {
		return res.cause.Summary
	}
	return ""
}

// diagRules in order: the first that matches is the cause.
var diagRules = []diagRule{
	{"server_offline", ruleServerOffline},
	{"phase_timeout", rulePhaseTimeout},
	{"push_failed", rulePushFailed},
	{"build_failed", ruleBuildFailed},
	{"pull_failed", rulePullFailed},
	{"pre_deploy_failed", rulePreDeployFailed},
	{"oom_killed", ruleOOMKilled},
	{"missing_binary", ruleMissingBinary},
	{"wrong_arch", ruleWrongArch},
	{"missing_variable", ruleMissingVariable},
	{"port_mismatch", rulePortMismatch},
	{"loopback_only", ruleLoopbackOnly},
	{"health_path", ruleHealthPath},
	{"not_listening", ruleNotListening},
	{"exited", ruleExited},
}

// diagnose runs the rules over in.
func diagnose(in *diagInput) diagResult {
	var out diagResult
	found := false
	for _, r := range diagRules {
		v := r.check(in)
		if v.skip {
			continue
		}
		out.checked = append(out.checked, apitypes.DiagnosisCheck{Rule: r.id, Matched: v.matched, Why: v.why})
		if !v.matched {
			continue
		}
		if found {
			out.also = append(out.also, r.id)
			continue
		}
		found = true
		out.cause = apitypes.DiagnosisCause{Rule: r.id, Summary: v.summary, Detail: v.detail, Fix: v.fix}
		out.matched = v.lines
		if r.id == "server_offline" {
			break // nothing else was read
		}
	}
	if !found {
		out.cause = apitypes.DiagnosisCause{Summary: "No rule matched", Detail: in.err}
	}
	return out
}

// flags is the CLI's service and environment flags for a fix.
func (in *diagInput) flags() string { return fmt.Sprintf("-s %s -e %s", in.service, in.environment) }

// inHealthCheck: the release failed in its deploy's health check, where
// the container ran and the check's answer is known.
func (in *diagInput) inHealthCheck() bool {
	return in.step == apitypes.ReleaseHealthChecking && in.code == apitypes.CodeHealthCheckFailed
}

// running: a live release that crash loops or turned unhealthy.
func (in *diagInput) running() bool {
	return in.kind == apitypes.DiagnosisCrashLoop || in.kind == apitypes.DiagnosisUnhealthy
}

// checksHealth: a health check's answer is known, the deploy's or the
// ongoing one's.
func (in *diagInput) checksHealth() bool {
	return in.inHealthCheck() || in.kind == apitypes.DiagnosisUnhealthy
}

// exitCode is the code the container last exited with, from the health
// check's answer or a running service's status; -1 when it didn't exit.
func (in *diagInput) exitCode() int {
	switch {
	case in.health.Kind == node.HealthFailExited || in.health.Kind == node.HealthFailKilled:
		return in.health.ExitCode
	case (in.kind == apitypes.DiagnosisCrashLoop || in.kind == diagRun) && in.exit != nil && in.exit.ExitCode != nil:
		return *in.exit.ExitCode
	}
	return -1
}

// started: a container was started (or tried to), so its exit and
// output may tell.
func (in *diagInput) started() bool {
	return in.step == apitypes.ReleaseStarting || in.inHealthCheck() || in.running() || in.kind == diagRun
}

func ruleServerOffline(in *diagInput) diagVerdict {
	if in.running() || in.kind == diagRun {
		return diagVerdict{skip: true}
	}
	if !in.offline {
		return diagVerdict{why: "the server stayed online"}
	}
	return diagVerdict{matched: true, why: "the server's link dropped",
		summary: fmt.Sprintf("The server went offline during %s; the previous release kept serving", strings.ReplaceAll(string(in.step), "_", " "))}
}

// rulePushFailed: the image built, but storing it in the platform
// registry failed (the builder's "push <ref>: ..." error). The build's
// log ends at "Pushing", so without this the cause read "The build
// failed" while every build step had passed.
func rulePushFailed(in *diagInput) diagVerdict {
	if in.step != apitypes.ReleaseBuilding || in.pulled {
		return diagVerdict{skip: true}
	}
	if in.code != apitypes.CodeBuildFailed || !pushFailed(in.err) {
		return diagVerdict{why: "no push to the registry failed"}
	}
	v := diagVerdict{matched: true, why: "the image built; storing it in the platform registry failed", detail: in.err,
		summary: "The image built, but the platform registry didn't take it; deploy again, and if it keeps failing an operator checks `dock registry` and the registry's storage"}
	if e := strings.ToLower(in.err); strings.Contains(e, "timeout") || strings.Contains(e, "deadline exceeded") {
		v.summary = "The image built, but the platform registry didn't answer in time; deploy again, and if it keeps failing an operator checks `dock registry` and the registry's storage"
	}
	if _, why, ok := strings.Cut(in.err, registryStorageSays); ok {
		// The gateway knew why (ADR 0084): say that instead.
		v.summary = "The image built, but the platform registry didn't take it: " + strings.TrimSuffix(why, ")")
	}
	return v
}

// pushFailed reports whether a build's error is its push to the
// registry failing: deploy.go's "build failed: " around the builder's
// "push <ref>: ..." (internal/node/build/push.go).
func pushFailed(err string) bool {
	_, rest, ok := strings.Cut(err, "build failed: push ")
	return ok && strings.Contains(rest, ": ")
}

func ruleBuildFailed(in *diagInput) diagVerdict {
	if in.step != apitypes.ReleaseBuilding || in.pulled {
		return diagVerdict{skip: true}
	}
	if in.code != apitypes.CodeBuildFailed {
		return diagVerdict{why: "the build passed"}
	}
	if pushFailed(in.err) {
		return diagVerdict{why: "every build step passed"}
	}
	v := diagVerdict{matched: true, why: "the build failed", summary: "The build failed"}
	if line, ok := firstBuildError(in.build); ok {
		v.summary = "The build failed at a step of its Dockerfile or build plan"
		v.detail = line
	}
	return v
}

// firstBuildError is the build output's first error line: BuildKit's
// "ERROR:" or "error:".
func firstBuildError(lines []string) (string, bool) {
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "ERROR") || strings.HasPrefix(t, "error:") || strings.Contains(t, " ERROR: ") || strings.Contains(t, "failed to solve") {
			return t, true
		}
	}
	return "", false
}

// rulePhaseTimeout: a step of the deploy ran out of its deadline, which
// says more than any of its output (a stalled step usually has none).
func rulePhaseTimeout(in *diagInput) diagVerdict {
	s := in.stalled
	if s == nil {
		return diagVerdict{why: "every step ended in time"}
	}
	what := strings.ToUpper(s.what[:1]) + s.what[1:]
	return diagVerdict{matched: true, why: "it ran out of time",
		summary: fmt.Sprintf("%s didn't finish in %s; deploy again", what, humanDuration(s.limit)),
		detail:  s.hint}
}

func rulePullFailed(in *diagInput) diagVerdict {
	if in.step != apitypes.ReleaseBuilding || !in.pulled {
		return diagVerdict{skip: true}
	}
	if in.code != apitypes.CodeBuildFailed {
		return diagVerdict{why: "the image was pulled"}
	}
	e := strings.ToLower(in.err)
	switch {
	case pullRefused(e):
		return diagVerdict{matched: true, why: "the registry refused access",
			summary: "The image's registry refused access; add or fix its registry credentials",
			fix:     &apitypes.DiagnosisFix{Setting: "image", CLI: "dock registry login HOST --username NAME"}}
	case strings.Contains(e, "manifest unknown") || strings.Contains(e, "not found") || strings.Contains(e, "no such"):
		return diagVerdict{matched: true, why: "the registry has no such image",
			summary: "The image or its tag doesn't exist in the registry",
			fix:     &apitypes.DiagnosisFix{Setting: "image", CLI: "dock service update --image REF " + in.flags()}}
	}
	return diagVerdict{why: "the pull failed for another reason"}
}

func rulePreDeployFailed(in *diagInput) diagVerdict {
	if in.pre == nil {
		return diagVerdict{skip: true}
	}
	p := in.pre
	switch {
	case p.TimedOut:
		return diagVerdict{matched: true, why: "it timed out",
			summary: fmt.Sprintf("The pre-deploy command ran out of time (%s); what it changed is not rolled back", humanMS(p.DurationMS)),
			fix:     &apitypes.DiagnosisFix{Setting: "pre_deploy_command", CLI: "dock service update --pre-deploy-timeout 30m " + in.flags()}}
	case p.ExitCode == nil:
		return diagVerdict{matched: true, why: "it couldn't run", summary: "The pre-deploy command couldn't run",
			fix: &apitypes.DiagnosisFix{Setting: "pre_deploy_command"}}
	case *p.ExitCode != 0:
		v := diagVerdict{matched: true, why: fmt.Sprintf("exit %d", *p.ExitCode),
			summary: fmt.Sprintf("The pre-deploy command exited with code %d; what it changed before is not rolled back", *p.ExitCode),
			fix:     &apitypes.DiagnosisFix{Setting: "pre_deploy_command"}}
		if line, ok := firstErrorLine(p.Tail); ok {
			v.detail = line
		}
		return v
	}
	return diagVerdict{why: "exit 0"}
}

func ruleOOMKilled(in *diagInput) diagVerdict {
	if !in.started() {
		return diagVerdict{skip: true}
	}
	limit := in.cfg.MemoryMB
	if in.health.LimitMB > 0 {
		limit = in.health.LimitMB
	}
	fix := &apitypes.DiagnosisFix{Setting: "memory", Suggested: strconv.Itoa(max(limit*2, 256)),
		CLI: fmt.Sprintf("dock service update --memory %d %s", max(limit*2, 256), in.flags())}
	oom := in.health.Kind == node.HealthFailOOM || in.exit != nil && in.exit.OOMKilled
	switch {
	case oom && limit > 0:
		return diagVerdict{matched: true, why: "the kernel's OOM kill", fix: fix,
			summary: fmt.Sprintf("Killed for using more than its %d MB memory limit", limit)}
	case oom:
		return diagVerdict{matched: true, why: "the kernel's OOM kill", fix: fix, summary: "Killed for running out of memory"}
	case in.health.Kind == node.HealthFailKilled || (in.kind == apitypes.DiagnosisCrashLoop || in.kind == diagRun) && in.exitCode() == 137 && limit > 0:
		return diagVerdict{matched: true, why: "exit 137 with a memory limit", fix: fix,
			summary: fmt.Sprintf("Killed (exit code 137), most likely for using more than its %d MB memory limit", limit)}
	}
	return diagVerdict{why: "no OOM kill"}
}

// notFoundLine matches a shell's or the container runtime's word that a
// command isn't there.
var notFoundLine = regexp.MustCompile(`executable file not found|: not found$|: command not found|no such file or directory`)

func ruleMissingBinary(in *diagInput) diagVerdict {
	if !in.started() {
		return diagVerdict{skip: true}
	}
	exit127 := in.exitCode() == 127
	errLine := in.step == apitypes.ReleaseStarting && strings.Contains(in.err, "executable file not found")
	if !exit127 && !errLine {
		if code := in.exitCode(); code >= 0 {
			return diagVerdict{why: fmt.Sprintf("exit %d, not 127", code)}
		}
		return diagVerdict{why: "no exit 127"}
	}
	idx := matchLines(in.container, func(l string) bool {
		return (strings.Contains(l, "not found") || strings.Contains(l, "no such")) && notFoundLine.MatchString(l)
	})
	what := "The start command"
	switch {
	case in.kind == diagRun:
		what = fmt.Sprintf("The command's program (%s)", firstWord(in.cfg.Cmd))
	case in.cfg.Cmd != "":
		what = fmt.Sprintf("The start command's program (%s)", firstWord(in.cfg.Cmd))
	}
	return diagVerdict{matched: true, why: "exit 127, command not found", lines: idx,
		summary: what + " isn't in the image",
		detail:  lineAt(in.container, idx),
		fix:     &apitypes.DiagnosisFix{Setting: "start_command", CLI: "dock service update --start-command CMD " + in.flags()}}
}

func ruleWrongArch(in *diagInput) diagVerdict {
	if !in.started() {
		return diagVerdict{skip: true}
	}
	idx := matchLines(in.container, func(l string) bool { return strings.Contains(l, "exec format error") })
	if len(idx) == 0 && !strings.Contains(in.err, "exec format error") {
		return diagVerdict{why: "no exec format error"}
	}
	return diagVerdict{matched: true, why: "exec format error", lines: idx,
		summary: "The image was built for another CPU architecture than the server's",
		fix:     &apitypes.DiagnosisFix{Setting: "image"}}
}

var (
	// varCue: a line saying something is missing.
	varCue = regexp.MustCompile(`(?i)\b(not set|missing|required|undefined|must be set|is not defined|keyerror)\b`)
	// varCueWords prefilter varCue's lines without a regexp.
	varCueWords = []string{"set", "issing", "equired", "ndefined", "efined", "eyError"}
	// varName: an environment variable's name: capitals with an
	// underscore (DATABASE_URL), the shape names in logs take; a bare
	// capitalized word (ERROR) isn't one.
	varName = regexp.MustCompile(`\b[A-Z][A-Z0-9]*_[A-Z0-9_]*[A-Z0-9]\b`)
)

func ruleMissingVariable(in *diagInput) diagVerdict {
	if !in.inHealthCheck() && in.pre == nil && in.kind != apitypes.DiagnosisCrashLoop && in.kind != diagRun {
		return diagVerdict{skip: true}
	}
	lines, fromPre := in.container, false
	if in.pre != nil && in.pre.ExitCode != nil && *in.pre.ExitCode != 0 {
		lines, fromPre = diagTexts(in.pre.Tail), true
	}
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if !anyOf(l, varCueWords) || !varCue.MatchString(l) {
			continue
		}
		for _, name := range varName.FindAllString(l, -1) {
			if _, set := in.cfg.Env[name]; set || strings.HasPrefix(name, "DOCK_") || strings.HasPrefix(name, "ERR_") {
				continue
			}
			summary := name + " isn't set"
			if in.last != nil {
				if _, had := in.last.Env[name]; had {
					summary += " (removed since the last good release)"
				}
			}
			v := diagVerdict{matched: true, why: name + " is named as missing and not set", summary: summary, detail: l,
				fix: &apitypes.DiagnosisFix{Setting: "variables", Suggested: name, CLI: fmt.Sprintf("dock env set %s=... %s", name, in.flags())}}
			if !fromPre {
				v.lines = []int{i}
			}
			return v
		}
	}
	return diagVerdict{why: "no unset variable named in the output"}
}

// listenLine reads the address an app says it listens on: "listening on
// port 3000", "Server listening at http://0.0.0.0:3000", "Uvicorn
// running on http://127.0.0.1:8000", "started server on 0.0.0.0:3000".
var listenLine = regexp.MustCompile(`(?i)\b(?:listen|listening|running|started|serving|bound|server)\b.{0,60}?(?:(?:https?://)?(\[[0-9a-f:]*\]|[0-9a-z][0-9a-z.\-]*)?:(\d{2,5})\b|\bport\s*:?\s*(\d{2,5})\b)`)

// listenWords prefilter listenLine's lines without a regexp: its
// keywords without their first letter, which may be a capital.
var listenWords = []string{"isten", "unning", "tarted", "erving", "ound", "erver"}

// listening is the last address the app said it listens on: its host
// (empty when not said) and port, and the line's index (-1: none). It is
// read once per diagnosis.
func (in *diagInput) listening() (host string, port int, at int) {
	if in.listen == nil {
		h, p, i := lastListen(in.container)
		in.listen = &listenAt{h, p, i}
	}
	return in.listen.host, in.listen.port, in.listen.at
}

// listenAt is a listening result.
type listenAt struct {
	host     string
	port, at int
}

// lastListen finds the last line saying where the app listens.
func lastListen(lines []string) (host string, port int, at int) {
	for i := len(lines) - 1; i >= 0; i-- {
		if !anyOf(lines[i], listenWords) {
			continue
		}
		m := listenLine.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		p := m[2]
		if p == "" {
			p = m[3]
		}
		n, err := strconv.Atoi(p)
		if err != nil || n == 0 || n > 65535 {
			continue
		}
		return strings.ToLower(m[1]), n, i
	}
	return "", 0, -1
}

// port is the container port the deploy's health check probed: the
// published port, or a template database's on its network; 0 when it
// only had to keep running.
func (in *diagInput) port() int {
	if in.kind == apitypes.DiagnosisUnhealthy && in.cfg.HealthPort == 0 {
		return in.cfg.Port // probed inside the container, published or not
	}
	if in.cfg.Published {
		return in.cfg.Port
	}
	return in.cfg.HealthPort
}

// unreachable: the health check found nothing answering on the port.
func (in *diagInput) unreachable() bool {
	return in.health.Kind == node.HealthFailNotListening || in.health.Kind == node.HealthFailNoAnswer
}

// reachedWhy says why the port rules are ruled out when the check didn't
// find the port unreachable: it answered, or the app ended first.
func (in *diagInput) reachedWhy() string {
	switch in.health.Kind {
	case node.HealthFailExited:
		return "it exited before the port could answer"
	case node.HealthFailKilled, node.HealthFailOOM:
		return "it was killed before the port could answer"
	}
	return "the port answered"
}

func rulePortMismatch(in *diagInput) diagVerdict {
	if !in.checksHealth() || in.port() == 0 {
		return diagVerdict{skip: true}
	}
	if !in.unreachable() {
		return diagVerdict{why: in.reachedWhy()}
	}
	_, port, at := in.listening()
	switch {
	case at < 0:
		return diagVerdict{why: "the output names no port"}
	case port == in.port():
		return diagVerdict{why: fmt.Sprintf("the app listens on :%d too", port)}
	}
	return diagVerdict{matched: true, why: fmt.Sprintf("checked :%d, the app listens on :%d", in.port(), port), lines: []int{at},
		summary: fmt.Sprintf("The app listens on :%d, Dock checks :%d", port, in.port()),
		detail:  in.container[at],
		fix:     &apitypes.DiagnosisFix{Setting: "port", Suggested: strconv.Itoa(port), CLI: fmt.Sprintf("dock service update --port %d %s", port, in.flags())}}
}

func ruleLoopbackOnly(in *diagInput) diagVerdict {
	if !in.checksHealth() || in.port() == 0 {
		return diagVerdict{skip: true}
	}
	if !in.unreachable() {
		return diagVerdict{why: in.reachedWhy()}
	}
	host, port, at := in.listening()
	if at < 0 || port != in.port() {
		return diagVerdict{why: "the output names no loopback address on the port"}
	}
	if host != "127.0.0.1" && host != "localhost" && host != "[::1]" {
		return diagVerdict{why: "the app listens on all addresses"}
	}
	return diagVerdict{matched: true, why: "the app listens on " + host, lines: []int{at},
		summary: fmt.Sprintf("The app listens only on %s inside its container; make it listen on 0.0.0.0:%d", host, port),
		detail:  in.container[at]}
}

func ruleHealthPath(in *diagInput) diagVerdict {
	if !in.checksHealth() || in.cfg.HealthCheckPath == "" {
		return diagVerdict{skip: true}
	}
	if in.health.Kind != node.HealthFailStatus {
		return diagVerdict{why: "no HTTP answer to judge"}
	}
	path := in.cfg.HealthCheckPath
	fix := &apitypes.DiagnosisFix{Setting: "health_check_path", CLI: "dock service update --health-check-path PATH " + in.flags()}
	switch s := in.health.Status; {
	case s == 404:
		return diagVerdict{matched: true, why: "it answered 404", fix: fix,
			summary: fmt.Sprintf("GET %s answered 404: the app has no such path", path)}
	case s >= 500:
		// The path exists and the app failed on it: its output and what
		// changed say why, not the setting.
		v := diagVerdict{matched: true, why: fmt.Sprintf("it answered %d", s),
			summary: fmt.Sprintf("GET %s answered %d: the app failed to serve it", path, s)}
		if line, ok := firstErrorLine(diagLines(in.container)); ok {
			v.detail = line
		}
		return v
	default:
		return diagVerdict{matched: true, why: fmt.Sprintf("it answered %d", s), fix: fix,
			summary: fmt.Sprintf("GET %s answered %d, not 2xx or 3xx", path, s)}
	}
}

func ruleNotListening(in *diagInput) diagVerdict {
	if !in.checksHealth() || in.port() == 0 {
		return diagVerdict{skip: true}
	}
	timeout := in.cfg.HealthCheckTimeout
	switch in.health.Kind {
	case node.HealthFailNotListening:
		summary := fmt.Sprintf("Nothing answered on :%d within %ds", in.port(), timeout)
		if in.running() {
			summary = fmt.Sprintf("The app stopped answering on :%d", in.port())
		}
		return diagVerdict{matched: true, why: fmt.Sprintf("nothing accepted connections on :%d", in.port()),
			summary: summary,
			fix:     &apitypes.DiagnosisFix{Setting: "port", CLI: "dock service update --port PORT " + in.flags()}}
	case node.HealthFailNoAnswer:
		summary := fmt.Sprintf("The app accepted connections on :%d but didn't answer within %ds", in.port(), timeout)
		if in.running() {
			summary = fmt.Sprintf("The app accepts connections on :%d but stopped answering: it may hang", in.port())
		}
		return diagVerdict{matched: true, why: "connected, no answer",
			summary: summary,
			fix:     &apitypes.DiagnosisFix{Setting: "health_check_timeout", CLI: "dock service update --health-check-timeout 120s " + in.flags()}}
	}
	return diagVerdict{why: in.reachedWhy()}
}

func ruleExited(in *diagInput) diagVerdict {
	if !in.inHealthCheck() && in.kind != apitypes.DiagnosisCrashLoop {
		return diagVerdict{skip: true}
	}
	code := in.exitCode()
	if code < 0 || in.health.Kind == node.HealthFailKilled || in.health.Kind == node.HealthFailOOM || in.exit != nil && in.exit.OOMKilled {
		return diagVerdict{why: "it kept running"}
	}
	v := diagVerdict{matched: true, why: fmt.Sprintf("exit %d", code),
		summary: fmt.Sprintf("The app exited with code %d while starting", code)}
	if in.kind == apitypes.DiagnosisCrashLoop {
		v.summary = fmt.Sprintf("The app keeps exiting with code %d", code)
	}
	if i := lastErrorIndex(in.container); i >= 0 {
		v.detail, v.lines = in.container[i], []int{i}
	}
	return v
}

// anyOf reports whether s contains one of words.
func anyOf(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// matchLines are the indexes of the lines ok matches.
func matchLines(lines []string, ok func(string) bool) []int {
	var out []int
	for i, l := range lines {
		if ok(l) {
			out = append(out, i)
		}
	}
	return out
}

// lineAt is the first indexed line, or "".
func lineAt(lines []string, idx []int) string {
	if len(idx) == 0 {
		return ""
	}
	return lines[idx[0]]
}

// firstErrorLine is the first line that reads as an error
// (errparse.LineLevel, as the Errors tab judges lines).
func firstErrorLine(lines []apitypes.DiagnosisLine) (string, bool) {
	for _, l := range lines {
		if errparse.LineLevel(l.Text) == errparse.LevelError {
			return l.Text, true
		}
	}
	return "", false
}

// lastErrorIndex is the index of the last line that reads as an error,
// or -1: what an app that exited said last about why.
func lastErrorIndex(lines []string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if errparse.LineLevel(lines[i]) == errparse.LevelError {
			return i
		}
	}
	return -1
}

// diagLines wraps texts as lines.
func diagLines(texts []string) []apitypes.DiagnosisLine {
	out := make([]apitypes.DiagnosisLine, len(texts))
	for i, t := range texts {
		out[i].Text = t
	}
	return out
}

// diagTexts is the text of lines.
func diagTexts(lines []apitypes.DiagnosisLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Text
	}
	return out
}

// firstWord is a command's program.
func firstWord(cmd string) string {
	if f := strings.Fields(cmd); len(f) > 0 {
		return f[0]
	}
	return cmd
}

// humanMS says a duration in milliseconds the way people read it.
func humanMS(ms int64) string {
	switch {
	case ms < 1000:
		return fmt.Sprintf("%dms", ms)
	case ms < 60_000:
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%dm%02ds", ms/60_000, ms/1000%60)
}
