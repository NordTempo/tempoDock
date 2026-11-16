package output

import (
	"errors"
)

// ExitCode is the process exit status. The values are a stable contract for
// scripts and agents; see docs/cli.md. Never renumber them.
type ExitCode int

const (
	ExitOK           ExitCode = 0
	ExitError        ExitCode = 1  // anything not covered below
	ExitUsage        ExitCode = 2  // bad flags, arguments or input; nothing was done
	ExitAuth         ExitCode = 3  // no context or token, or the token lacks permission
	ExitNotFound     ExitCode = 4  // the project, service, context, link... doesn't exist
	ExitConflict     ExitCode = 5  // already exists, or another operation holds the lock
	ExitDeployFailed ExitCode = 6  // build or health check failed; traffic was not switched
	ExitUnreachable  ExitCode = 7  // could not reach the server
	ExitIncompatible ExitCode = 8  // CLI and server versions don't work together
	ExitMoved        ExitCode = 9  // dock serve: this control plane moved to another server (ADR 0021); systemd doesn't restart it
	ExitCanceled     ExitCode = 10 // the deploy was canceled before it went live (ADR 0086); traffic was not switched
)

var exitNames = map[ExitCode]string{
	ExitOK:           "ok",
	ExitError:        "error",
	ExitUsage:        "usage",
	ExitAuth:         "auth",
	ExitNotFound:     "not_found",
	ExitConflict:     "conflict",
	ExitDeployFailed: "deploy_failed",
	ExitUnreachable:  "unreachable",
	ExitIncompatible: "incompatible",
	ExitMoved:        "moved",
	ExitCanceled:     "deploy_canceled",
}

// String is the code's stable machine name, e.g. "not_found".
func (c ExitCode) String() string {
	if s, ok := exitNames[c]; ok {
		return s
	}
	return "error"
}

// Error is an error that knows its exit code and what the user should do
// next. Commands return it (directly or wrapped); Execute prints it and exits
// with Exit.
type Error struct {
	Exit ExitCode
	// Code is an optional machine-readable reason from the server, such as
	// "health_check_failed". Defaults to Exit's name in JSON output.
	Code string
	// Message says what happened, Hint what to do about it. Both are full
	// sentences or short phrases; commands in backticks.
	Message string
	Hint    string
	// Err is the underlying cause, if any.
	Err error
}

func (e *Error) Error() string {
	switch {
	case e.Message != "":
		return e.Message
	case e.Err != nil:
		return e.Err.Error()
	default:
		return e.Exit.String()
	}
}

func (e *Error) Unwrap() error { return e.Err }

// ExitCodeOf maps any error to an exit code: an *Error anywhere in the
// chain decides, everything else is ExitError.
func ExitCodeOf(err error) ExitCode {
	if err == nil {
		return ExitOK
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Exit
	}
	return ExitError
}
