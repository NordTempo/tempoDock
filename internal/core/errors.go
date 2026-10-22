package core

import (
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
)

// Error is a domain error with a machine-readable code. The API maps codes
// to HTTP statuses; messages are written for humans and say what to do next.
// It is datapanel.Error (ADR 0024), so the data panel's errors and core's
// are one type.
type Error = datapanel.Error

func errf(code apitypes.ErrorCode, format string, args ...any) error {
	return datapanel.Errorf(code, format, args...)
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
func CodeOf(err error) apitypes.ErrorCode { return datapanel.CodeOf(err) }
