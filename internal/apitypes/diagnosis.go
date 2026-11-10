package apitypes

import (
	"fmt"
	"strings"
	"time"
)

// Deploy diagnosis (ADR 0071): when a release fails, Dock keeps what
// explains it in one bundle, captured at the failure (the container, its
// exit and the probe's answer are gone seconds later), and names the
// likely cause with a small set of deterministic rules. The UI's release
// page, `dock diagnose` and MCP `diagnose` show the same bundle.

// DiagnosisKind is what failed.
type DiagnosisKind string

// Diagnosis kinds.
const (
	// DiagnosisRelease: a release failed (build, pull, pre-deploy
	// command, start, health check, routes, or its server went offline).
	DiagnosisRelease DiagnosisKind = "release"
	// DiagnosisCrashLoop: the active release's container kept exiting
	// (its crash loop alert opened).
	DiagnosisCrashLoop DiagnosisKind = "crash_loop"
	// DiagnosisUnhealthy: the active release's ongoing health check kept
	// failing after a restart (its unhealthy alert turned critical).
	DiagnosisUnhealthy DiagnosisKind = "unhealthy"
)

// DiagnosisRef names a release's diagnosis and its cause, for the release
// itself and its live events.
type DiagnosisRef struct {
	ID   int64         `json:"id"`
	Kind DiagnosisKind `json:"kind" enum:"release,crash_loop,unhealthy"`
	// Rule is the matched rule's ID, empty when none matched.
	Rule    string `json:"rule,omitempty" doc:"The matched rule's ID (port_mismatch, oom_killed, ...); empty when no rule matched"`
	Summary string `json:"summary" doc:"The likely cause in one line, or that no rule matched"`
}

// DiagnosisSummary is a diagnosis in a list, without its bundle.
type DiagnosisSummary struct {
	DiagnosisRef
	Release    int64     `json:"release" doc:"The release's number"`
	CapturedAt time.Time `json:"captured_at"`
}

// Diagnosis is one failure's bundle. Shapes only gain fields; the name
// trace is kept for the trace of the failing request, once Dock records
// traces.
type Diagnosis struct {
	ID          int64         `json:"id"`
	Kind        DiagnosisKind `json:"kind" enum:"release,crash_loop,unhealthy"`
	Project     string        `json:"project"`
	Environment string        `json:"environment"`
	Service     string        `json:"service"`
	Release     int64         `json:"release" doc:"The release's number: the one that failed, or the active one that crash looped or turned unhealthy"`
	// LastGoodRelease is the newest earlier release that served (0: none),
	// what Changes compares with.
	LastGoodRelease int64     `json:"last_good_release,omitempty" doc:"The newest earlier release that served, which changes compare with; 0 when none did"`
	CapturedAt      time.Time `json:"captured_at"`

	Cause DiagnosisCause `json:"cause"`
	// AlsoMatched are later rules that matched too.
	AlsoMatched []string `json:"also_matched" doc:"Rules that matched after the cause's"`
	// Checked is every rule that applies to where the release failed,
	// with its verdict: what was ruled out and why.
	Checked []DiagnosisCheck `json:"checked" doc:"Every rule that applies to the failed step, with its verdict"`

	Failure   DiagnosisFailure    `json:"failure"`
	Health    *DiagnosisHealth    `json:"health,omitempty" doc:"The deploy's health check, when the release got that far"`
	Changes   *DiagnosisChanges   `json:"changes,omitempty" doc:"What changed since the last good release; absent when none served before"`
	PreDeploy *DiagnosisPreDeploy `json:"pre_deploy,omitempty" doc:"The pre-deploy command, when one ran"`
	// ContainerTail and BuildTail are the last lines of the container's
	// output and of the build (or image pull), secret values masked for
	// whoever can't reveal them.
	ContainerTail []DiagnosisLine     `json:"container_tail"`
	BuildTail     []DiagnosisLine     `json:"build_tail"`
	Errors        []DiagnosisError    `json:"errors" doc:"Error issues of the service seen around the failure, newest first"`
	Resources     *DiagnosisResources `json:"resources,omitempty" doc:"The service's memory and CPU before the failure, from its 1-minute samples"`
	Truncated     bool                `json:"truncated" doc:"Older log lines were left out to keep the bundle under its size limit"`
}

// Headline says what the diagnosis is about in one line: "Release 62 of
// shop/production/web failed in health checking", "... crash looped
// while live", "... turned unhealthy while live".
func (d Diagnosis) Headline() string {
	what := fmt.Sprintf("Release %d of %s/%s/%s", d.Release, d.Project, d.Environment, d.Service)
	switch d.Kind {
	case DiagnosisCrashLoop:
		return what + " crash looped while live"
	case DiagnosisUnhealthy:
		return what + " turned unhealthy while live"
	}
	return what + " failed in " + strings.ReplaceAll(string(d.Failure.Step), "_", " ")
}

// DiagnosisCause is the likely cause: the first rule that matched.
type DiagnosisCause struct {
	Rule    string `json:"rule,omitempty"`
	Summary string `json:"summary" doc:"One line, made from the rule's facts (a port, a limit, a variable name), never from log text"`
	// Detail says what matched, and may quote a log line.
	Detail string        `json:"detail,omitempty" doc:"What matched; may quote a log line (masked)"`
	Fix    *DiagnosisFix `json:"fix,omitempty"`
}

// DiagnosisFix is what to change, when a setting can fix it.
type DiagnosisFix struct {
	// Setting is the service setting to change: port, health_check_path,
	// memory, start_command, image, variables, pre_deploy_command.
	Setting   string `json:"setting" enum:"port,health_check_path,memory,start_command,image,variables,pre_deploy_command,health_check_timeout"`
	Suggested string `json:"suggested,omitempty" doc:"A value the evidence suggests (the port the app listens on)"`
	CLI       string `json:"cli,omitempty" doc:"The command that makes the change"`
}

// DiagnosisCheck is one rule's verdict.
type DiagnosisCheck struct {
	Rule    string `json:"rule"`
	Matched bool   `json:"matched"`
	Why     string `json:"why" doc:"The fact the verdict rests on"`
}

// DiagnosisFailure is how and where the release failed.
type DiagnosisFailure struct {
	Step       ReleaseStatus  `json:"step" enum:"queued,building,starting,health_checking,active" doc:"The step it failed in; active for a running service"`
	ErrorCode  ErrorCode      `json:"error_code"`
	Error      string         `json:"error"`
	DurationMS int64          `json:"duration_ms" doc:"From the release's start to its failure; for a running service, how long it had been live"`
	Exit       *DiagnosisExit `json:"exit,omitempty" doc:"The container's state, when one was started"`
}

// DiagnosisExit is the container's state when the release failed.
type DiagnosisExit struct {
	Running       bool `json:"running" doc:"Still running when the deploy stopped it"`
	ExitCode      *int `json:"exit_code"`
	OOMKilled     bool `json:"oom_killed"`
	MemoryLimitMB int  `json:"memory_limit_mb"`
	Restarts      int  `json:"restarts"`
}

// DiagnosisHealth is the deploy's health check and its last answer.
type DiagnosisHealth struct {
	Kind           string `json:"kind" enum:"http,tcp,running" doc:"http: GET of the path; tcp: a connection; running: a service without a port has to keep running"`
	Path           string `json:"path,omitempty"`
	Port           int    `json:"port,omitempty" doc:"The container port it checked"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	// Result is what the last probe said: oom, killed, exited, no_port,
	// status, not_listening, no_answer, or empty when unknown.
	Result string `json:"result,omitempty"`
	Status int    `json:"status,omitempty" doc:"The HTTP status the check got"`
}

// DiagnosisChanges is what changed from the last good release.
type DiagnosisChanges struct {
	Image *DiagnosisImage `json:"image,omitempty" doc:"Set when the image changed"`
	// Variables are names: values never leave the sealed configs.
	Variables DiagnosisVariables `json:"variables"`
	Settings  []DiagnosisSetting `json:"settings"`
}

// DiagnosisImage is an image change.
type DiagnosisImage struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	Commit *Commit `json:"commit,omitempty" doc:"The failed release's commit"`
}

// DiagnosisVariables are variable names added, removed or with another
// value.
type DiagnosisVariables struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Changed []string `json:"changed"`
}

// DiagnosisSetting is one changed setting.
type DiagnosisSetting struct {
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

// DiagnosisPreDeploy is the pre-deploy command's run (ADR 0045).
type DiagnosisPreDeploy struct {
	Command    string          `json:"command"`
	ExitCode   *int            `json:"exit_code" doc:"Null when it couldn't run or timed out"`
	TimedOut   bool            `json:"timed_out,omitempty"`
	DurationMS int64           `json:"duration_ms"`
	Tail       []DiagnosisLine `json:"tail"`
}

// DiagnosisLine is one log line; Matched marks the lines a rule matched.
type DiagnosisLine struct {
	Text    string `json:"text"`
	Matched bool   `json:"matched,omitempty"`
}

// DiagnosisError is an error issue seen around the failure.
type DiagnosisError struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	Count        int64  `json:"count"`
	FirstRelease int64  `json:"first_release" doc:"The release it was first seen in"`
}

// DiagnosisResources are the service's 1-minute samples before the
// failure. For a failed release they are the serving release's: the new
// container rarely lives through a sample.
type DiagnosisResources struct {
	OfRelease     int64     `json:"of_release,omitempty" doc:"The release the samples are of"`
	MemoryMB      []int     `json:"memory_mb"`
	CPUPercent    []float64 `json:"cpu_percent"`
	PeakMemoryMB  int       `json:"peak_memory_mb"`
	MemoryLimitMB int       `json:"memory_limit_mb"`
}
