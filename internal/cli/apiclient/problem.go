package apiclient

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/NordTempo/platform/internal/cli/output"
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
}

// ProblemDetail is one validation failure.
type ProblemDetail struct {
	Message  string `json:"message"`
	Location string `json:"location,omitempty"` // e.g. "body.port"
}

func (p *Problem) Error() string {
	msg := p.Detail
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
	output.ExitAuth:         "Check the token for this context (`platform context current`), or add a new one with `platform context add`.",
	output.ExitUnreachable:  "The server or a proxy in front of it is not answering. Check `platform context current` and try again.",
	output.ExitIncompatible: "Update the CLI to match the server's version.",
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

	exit := statusExit(resp.StatusCode)
	if e, ok := codeExits[p.Code]; ok {
		exit = e
	}
	return &output.Error{Exit: exit, Code: p.Code, Hint: exitHints[exit], Err: p}
}
