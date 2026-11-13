package core

import (
	"errors"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/httpkit"
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

// errorLine is an error's message for a one-line event (a push not
// deployed, a preview's note, a check's step): a coded error's own words,
// a network failure in plain words (httpkit.NetErrorText) or an egress
// refusal as it is; anything else is logged and summarized, since its
// text can carry internals.
func (c *Core) errorLine(err error) string {
	if r, ok := errors.AsType[egressRefused](err); ok {
		return string(r)
	}
	if CodeOf(err) != apitypes.CodeInternal {
		return err.Error()
	}
	if text := httpkit.NetErrorText(err, 0); text != "" {
		return text
	}
	c.log.Error("summarized as an internal error", "err", err)
	return "internal error; see the daemon log"
}
