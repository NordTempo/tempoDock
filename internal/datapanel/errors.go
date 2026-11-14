package datapanel

import (
	"errors"
	"fmt"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Error is an error a caller sees, with a machine-readable code that the
// API maps to an HTTP status and the MCP endpoint to a tool error.
// Messages say what to do next. Dock's core.Error is an alias of it, so
// both hosts and the Panel raise and match one type.
type Error struct {
	Code apitypes.ErrorCode
	Msg  string
	// Release is the release a deploy_in_progress error is about (the
	// one deploying), so callers can wait for it; 0 when unknown.
	Release int64
	// Refusal explains a forbidden error: why, and how it could be
	// allowed (ADR 0080). Only Dock's core sets it.
	Refusal *apitypes.Refusal
}

func (e *Error) Error() string { return e.Msg }

// Errorf returns an *Error with a formatted message.
func Errorf(code apitypes.ErrorCode, format string, args ...any) error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// CodeOf returns err's code, or CodeInternal for unexpected errors.
func CodeOf(err error) apitypes.ErrorCode {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Code
	}
	return apitypes.CodeInternal
}

// Host contract errors. Hosts wrap them with a message; the Panel matches
// them with errors.Is.
var (
	// ErrNotRunning: Host.Connect has nothing to dial (Dock: the service
	// has no running container).
	ErrNotRunning = errors.New("not running")
	// ErrNotFound: a store lookup matched nothing.
	ErrNotFound = errors.New("not found")
	// ErrDuplicate: a store write broke a uniqueness rule.
	ErrDuplicate = errors.New("duplicate")
)

func invalid(format string, args ...any) error {
	return Errorf(apitypes.CodeValidation, format, args...)
}

func notFound(format string, args ...any) error {
	return Errorf(apitypes.CodeNotFound, format, args...)
}

func conflict(format string, args ...any) error {
	return Errorf(apitypes.CodeConflict, format, args...)
}

func forbidden(format string, args ...any) error {
	return Errorf(apitypes.CodeForbidden, format, args...)
}
