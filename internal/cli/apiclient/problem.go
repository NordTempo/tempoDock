package apiclient

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Problem is an RFC 7807 error body (application/problem+json), as the API
// returns for every non-2xx response. Code and Errors are extensions.
type Problem struct {
	Type     string `json:"type,omitempty"`
	Title    string `json:"title,omitempty"`
	Status   int    `json:"status,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	// Code is a stable machine-readable reason, e.g. "health_check_failed".
	// Some codes decide the exit code; see codeExits.
	Code string `json:"code,omitempty"`
	// Errors lists individual validation failures.
	Errors []ProblemDetail `json:"errors,omitempty"`
	// Request is the access request an environment_locked error filed.
	Request int64 `json:"request,omitempty"`
	// Release is the release a deploy_in_progress error is about: the one
	// deploying. Older servers don't send it.
	Release int64 `json:"release,omitempty"`
	// Refusal explains a forbidden error: why, and what would allow it
	// (ADR 0080). Older servers don't send it.
	Refusal *apitypes.Refusal `json:"refusal,omitempty"`
}

// ProblemDetail is one validation failure.
type ProblemDetail struct {
	Message  string `json:"message"`
	Location string `json:"location,omitempty"` // e.g. "body.port"
}

func (p *Problem) Error() string {
	msg := p.Detail
	if p.Refusal != nil && p.Refusal.Reason != "" {
		// The fix is the hint (problemExit), not part of the message.
		msg = p.Refusal.Reason
	}
	if msg == "" {
		msg = p.Title
	}
	if msg == "" {
		msg = http.StatusText(p.Status)
	}
	for _, d := range p.Errors {
		if d.Location != "" {
			msg += fmt.Sprintf("\n  %s: %s", d.Location, d.Message)
		} else {
			msg += "\n  " + d.Message
		}
	}
	return msg
}

// codeExits maps problem codes to exit codes where the HTTP status alone is
// too coarse. The server documents these codes; keep docs/cli.md in sync.
var codeExits = map[string]output.ExitCode{
	"build_failed":         output.ExitDeployFailed,
	"deploy_failed":        output.ExitDeployFailed,
	"health_check_failed":  output.ExitDeployFailed,
	"version_incompatible": output.ExitIncompatible,
}

// statusExit maps an HTTP status to an exit code.
func statusExit(status int) output.ExitCode {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return output.ExitUsage
	case http.StatusUnauthorized, http.StatusForbidden:
		return output.ExitAuth
	case http.StatusNotFound, http.StatusGone:
		return output.ExitNotFound
	case http.StatusConflict, http.StatusPreconditionFailed, http.StatusLocked:
		return output.ExitConflict
	case http.StatusUpgradeRequired:
		return output.ExitIncompatible
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return output.ExitUnreachable
	default:
		return output.ExitError
	}
}

// Default hints per exit code, used when the server gives none.
var exitHints = map[output.ExitCode]string{
	output.ExitAuth:         "Check which token is in use with `dock context current`, or log in again with `dock login <server-url>`.",
	output.ExitUnreachable:  "The server or a proxy in front of it is not answering. Check `dock context current` and try again.",
	output.ExitIncompatible: "Run `dock self-update` to install the CLI that matches the server, or `dock version` for the install command.",
}

// codeHints replace the exit code's hint for codes where it would mislead:
// a server_offline 503 comes from a control plane that is answering.
var codeHints = map[string]string{
	"environment_locked": "Run `dock request wait <id> --timeout 2m` next, again while it exits 5 (still waiting): it waits for an admin's decision in the UI (the Inbox) and exits 0 once the change ran as you, 3 if rejected (the admin's comment says why). Don't retry or work around it. Commands that stage a change take --deploy, so one request covers staging and applying.",
	// A dock login token counts as two-factor only when the browser
	// sign-in that approved it used a second factor (ADR 0057).
	"two_factor_required": "Add a passkey or an authenticator app on your account page in the browser, then run `dock login` again: its new token counts as two-factor.",
	// A dock login token is bound to the organization whose single
	// sign-on the approving browser sign-in went through (ADR 0058).
	"sso_required": "Sign in through your identity provider again in the browser, then retry.",
	// Step-ups only ask browser sessions; tokens skip them.
	"reauth_required": "Confirm it's you in the browser, where the action asks, then try again.",
	"server_offline":  "The control plane is fine; the server the environment runs on isn't connected, and its apps keep running. Check `dock server list`, and the node there (`journalctl -u dock-node`).",
}

// problemError turns a non-2xx response into an *output.Error wrapping a
// *Problem. Bodies that aren't problem+json (say, a proxy's HTML page) still
// yield a Problem built from the status.
func problemError(resp *http.Response) error {
	p := &Problem{Status: resp.StatusCode}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mt == "application/problem+json" || mt == "application/json" {
		_ = json.Unmarshal(body, p)
	} else if s := strings.TrimSpace(string(body)); s != "" && len(s) < 200 && !strings.HasPrefix(s, "<") {
		p.Detail = s
	}
	if p.Status == 0 {
		p.Status = resp.StatusCode
	}
	return problemExit(p, resp.StatusCode)
}

// problemExit wraps p in an *output.Error whose exit code follows status and
// p.Code.
func problemExit(p *Problem, status int) error {
	exit := statusExit(status)
	if e, ok := codeExits[p.Code]; ok {
		exit = e
	}
	hint, ok := codeHints[p.Code]
	if !ok {
		hint = exitHints[exit]
	}
	if r := p.Refusal; r != nil && r.Fix != "" {
		hint = refusalHint(r)
	}
	return &output.Error{Exit: exit, Code: p.Code, Hint: hint, Err: p}
}

// refusalHint is what to do about a refusal: its fix, and whom to ask
// when the server named them. An agent reads it as "stop and report".
func refusalHint(r *apitypes.Refusal) string {
	hint := upperFirst(r.Fix) + "."
	if len(r.Ask) > 0 {
		hint += " Ask " + strings.Join(r.Ask, " or ") + "."
	}
	return hint
}

// upperFirst capitalizes s's first letter (ASCII), for a sentence.
func upperFirst(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}
