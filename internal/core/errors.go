package core

import (
	"errors"
	"fmt"

	"github.com/NordTempo/platform/internal/apitypes"
)

// Error is a domain error with a machine-readable code. The API maps codes
// to HTTP statuses; messages are written for humans and say what to do next.
type Error struct {
	Code apitypes.ErrorCode
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func errf(code apitypes.ErrorCode, format string, args ...any) error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

func invalid(format string, args ...any) error {
	return errf(apitypes.CodeValidation, format, args...)
}

func notFound(format string, args ...any) error {
	return errf(apitypes.CodeNotFound, format, args...)
}

func conflict(format string, args ...any) error {
	return errf(apitypes.CodeConflict, format, args...)
}

// CodeOf returns err's code, or CodeInternal for unexpected errors.
func CodeOf(err error) apitypes.ErrorCode {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return apitypes.CodeInternal
}
