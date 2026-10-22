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
