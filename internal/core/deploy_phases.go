package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Deadlines for the deploy steps that wait on something outside the
// release (the build's own is buildTimeout, the pre-deploy command's and
// the health check's are the service's settings). Every step a deploy
// takes ends, so no deploy holds its service, its builder's slot or the
// registry forever. A var only so tests can shorten them.
var (
	// registryWait bounds waiting for the platform registry while a
	// migration or a storage change holds it; a prune yields within
	// seconds (registryGate).
	registryWait = 10 * time.Minute
	// pullWait bounds pulling an image onto the release's server.
	pullWait = 30 * time.Minute
	// startWait bounds creating and starting the release's container.
	startWait = 5 * time.Minute
)

// stalledPhase is a deploy step that ran out of its deadline: the
// release fails with code, and its diagnosis says which step and for how
// long (rulePhaseTimeout).
type stalledPhase struct {
	// what is the step as a phrase: "pulling the image".
	what  string
	limit time.Duration
	code  apitypes.ErrorCode
	// hint is what to do besides deploying again, or "".
	hint string
}

// error is the release's error for the stalled step.
func (s *stalledPhase) error() error {
	msg := fmt.Sprintf("%s didn't finish in %s", s.what, humanDuration(s.limit))
	if s.hint != "" {
		msg += "; " + s.hint
	}
	return step(s.code, "%s; deploy again", msg)
}

// within runs one step of the deploy under its deadline. If the deadline
// ends it (not a cancel or the daemon stopping), the deploy fails with
// phase's error and remembers it for the diagnosis.
func (d *deployment) within(ctx context.Context, phase stalledPhase, fn func(context.Context) error) error {
	pctx, cancel := context.WithTimeout(ctx, phase.limit)
	defer cancel()
	err := fn(pctx)
	if err != nil && ctx.Err() == nil && errors.Is(pctx.Err(), context.DeadlineExceeded) {
		d.stalled = &phase
		return phase.error()
	}
	return err
}

// registry takes the platform registry for the build's push or a pull,
// under registryWait. While a prune or migration holds it the release
// says so: the step would otherwise show nothing at all.
func (d *deployment) registry(ctx context.Context) (release func(), err error) {
	gate := &d.c.images.gate
	if gate.busy() {
		d.event(ctx, "Waiting for the platform registry: it is being pruned or its images are moving (`dock registry`)")
	}
	err = d.within(ctx, stalledPhase{what: "waiting for the platform registry", limit: registryWait, code: apitypes.CodeBuildFailed,
		hint: "it was busy (its images moving to other storage, or a storage change); `dock registry` says what it does"},
		func(ctx context.Context) error {
			var err error
			release, err = gate.use(ctx)
			return err
		})
	return release, err
}

// humanDuration is "10 minutes", "1 hour", "45s".
func humanDuration(d time.Duration) string {
	unit, n := "", 0
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		unit, n = "hour", int(d/time.Hour)
	case d >= time.Minute && d%time.Minute == 0:
		unit, n = "minute", int(d/time.Minute)
	case d < time.Second:
		return d.String()
	default:
		return d.Round(time.Second).String()
	}
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
