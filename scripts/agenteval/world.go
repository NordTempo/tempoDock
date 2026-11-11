package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// World is a case's daemon as setup and checks see it: the admin's CLI and
// API, and helpers for the state checks read most.
type World struct {
	*daemon
	// DB is the case's database container, if it Needs one.
	DB *database
	// Vars carries values from setup to the check (a release number, a
	// request id).
	Vars map[string]string
}

// run runs the admin CLI and fails setup on an error.
func (w *World) run(args ...string) error {
	_, err := w.dock(args...)
	return err
}

// runAll runs several admin CLI commands in order, each one a
// space-separated line (no quoting: setup's arguments have no spaces).
func (w *World) runAll(lines ...string) error {
	for _, l := range lines {
		if err := w.run(strings.Fields(l)...); err != nil {
			return err
		}
	}
	return nil
}

// jsonOf runs the admin CLI and decodes its --json output.
func jsonOf[T any](w *World, args ...string) (T, error) {
	var v T
	out, err := w.dock(args...)
	if err != nil {
		return v, err
	}
	return v, json.Unmarshal(out, &v)
}

// token creates an API token as the admin and returns its secret. project
// and env may be empty (no limit); extra are more `dock token create` flags.
func (w *World) token(scope apitypes.Scope, project, env string, extra ...string) (string, error) {
	args := append([]string{"token", "create", "agent", "--scope", string(scope)}, extra...)
	if project != "" {
		args = append(args, "--project", project)
	}
	if env != "" {
		args = append(args, "--environment", env)
	}
	v, err := jsonOf[struct {
		Secret string `json:"secret"`
	}](w, args...)
	return v.Secret, err
}

// seedShop creates the standard world most cases start from: project shop
// with production and staging, and web (nginx) and api (a node app, port
// 3000) deployed in both.
func (w *World) seedShop() error {
	if err := w.runAll(
		"project create shop",
		"environment create staging -p shop",
	); err != nil {
		return err
	}
	for _, env := range []string{"staging", "production"} {
		if err := w.runAll(
			"service create web -p shop -e "+env+" --image nginx:1.29-alpine --port 80",
			"deploy -p shop -e "+env+" -s web --image nginx:1.29-alpine",
			"service create api -p shop -e "+env+" --image ghcr.io/acme/api:1.4.0 --port 3000",
			"deploy -p shop -e "+env+" -s api --image ghcr.io/acme/api:1.4.0",
		); err != nil {
			return err
		}
	}
	return nil
}

// releases are a service's releases, newest first.
func (w *World) releases(env, svc string) ([]apitypes.Release, error) {
	return get[[]apitypes.Release](w.daemon, "/projects/shop/environments/%s/services/%s/releases", env, svc)
}

// active is a service's active release.
func (w *World) active(env, svc string) (apitypes.Release, error) {
	rs, err := w.releases(env, svc)
	if err != nil {
		return apitypes.Release{}, err
	}
	for _, r := range rs {
		if r.Status == apitypes.ReleaseActive {
			return r, nil
		}
	}
	return apitypes.Release{}, fmt.Errorf("%s/%s has no active release", env, svc)
}

// service is one service of shop.
func (w *World) service(env, svc string) (apitypes.Service, error) {
	return get[apitypes.Service](w.daemon, "/projects/shop/environments/%s/services/%s", env, svc)
}

// variables are an environment's variables, values included (admin).
func (w *World) variables(env string) ([]apitypes.Variable, error) {
	return get[[]apitypes.Variable](w.daemon, "/projects/shop/environments/%s/variables?reveal=true", env)
}

// variable finds a live (not only staged) variable by service ("" for the
// environment's) and key.
func (w *World) variable(env, svc, key string) (apitypes.Variable, bool, error) {
	vs, err := w.variables(env)
	if err != nil {
		return apitypes.Variable{}, false, err
	}
	i := slices.IndexFunc(vs, func(v apitypes.Variable) bool { return v.Key == key && v.Service == svc })
	if i < 0 {
		return apitypes.Variable{}, false, nil
	}
	return vs[i], true, nil
}

// domains are a service's domains.
func (w *World) domains(env, svc string) ([]apitypes.Domain, error) {
	return get[[]apitypes.Domain](w.daemon, "/projects/shop/environments/%s/services/%s/domains", env, svc)
}

// changes are an environment's staged changes.
func (w *World) changes(env string) (apitypes.PendingChanges, error) {
	return get[apitypes.PendingChanges](w.daemon, "/projects/shop/environments/%s/changes", env)
}

// requests are the org's access requests.
func (w *World) requests() ([]apitypes.AccessRequest, error) {
	return get[[]apitypes.AccessRequest](w.daemon, "/requests")
}

// Decision is how the simulated admin answers access requests: each
// pending one, ten seconds after it is filed (people take a while).
type Decision struct {
	Approve bool
	Comment string
}

// decide answers pending access requests until ctx ends, polling once a
// second (the harness, not Dock, polls).
func (dec *Decision) decide(ctx context.Context, w *World) {
	done := map[int64]bool{}
	for ctx.Err() == nil {
		time.Sleep(time.Second)
		rs, err := w.requests()
		if err != nil {
			continue
		}
		for _, r := range rs {
			if r.Status == apitypes.RequestPending && !done[r.ID] && time.Since(r.CreatedAt) > 10*time.Second {
				done[r.ID] = true
				_ = w.decide(r.ID, dec.Approve, dec.Comment)
			}
		}
	}
}

// Checks on the transcript.

// answerMatches fails unless the final answer matches every pattern
// (case-insensitive).
func answerMatches(t Transcript, patterns ...string) error {
	for _, p := range patterns {
		if !regexp.MustCompile("(?is)" + p).MatchString(t.Answer) {
			return fmt.Errorf("answer doesn't mention /%.60s/", p)
		}
	}
	return nil
}

// noAnswer fails if the final answer matches any pattern.
func noAnswer(t Transcript, patterns ...string) error {
	for _, p := range patterns {
		if regexp.MustCompile("(?is)" + p).MatchString(t.Answer) {
			return fmt.Errorf("answer mentions /%s/", p)
		}
	}
	return nil
}

// firstErr returns the first non-nil error.
func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// expect fails with msg unless ok.
func expect(ok bool, format string, args ...any) error {
	if ok {
		return nil
	}
	return fmt.Errorf(format, args...)
}
