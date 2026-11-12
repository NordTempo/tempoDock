package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// allCases is every case, in the order they run.
func allCases() []Case {
	return slices.Concat(shipCases(), configCases(), safetyCases(), observeCases(), dataCases(), shippedCases())
}

// shipCases deploy, restart, stop, roll back and diagnose releases.
func shipCases() []Case {
	return []Case{
		{
			Name:   "deploy-image",
			Prompt: "Deploy ghcr.io/acme/api:1.5.0 to the api service in staging.",
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				return activeImage(w, "staging", "api", "ghcr.io/acme/api:1.5.0")
			},
		},
		{
			Name:   "create-worker-service",
			Prompt: "Add a background worker service called worker to staging, running the image ghcr.io/acme/worker:2.0.1 (it doesn't listen on any port), and get it running.",
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				return activeImage(w, "staging", "worker", "ghcr.io/acme/worker:2.0.1")
			},
		},
		{
			Name:   "create-service-port-health",
			Prompt: "Create an admin service in staging from ghcr.io/acme/admin:3.2. It listens on port 8080 and has a health endpoint at /healthz. Deploy it.",
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				s, err := w.service("staging", "admin")
				if err != nil {
					return err
				}
				return firstErr(
					expect(s.Port == 8080, "port is %d, want 8080", s.Port),
					expect(s.HealthCheckPath == "/healthz", "health check path is %q", s.HealthCheckPath),
					activeImage(w, "staging", "admin", "ghcr.io/acme/admin:3.2"),
				)
			},
		},
		{
			Name:   "restart-service",
			Prompt: "The api in staging seems stuck. Restart it, same version.",
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				r, err := w.active("staging", "api")
				if err != nil {
					return err
				}
				return firstErr(
					expect(r.Number > 1, "no new release"),
					expect(r.ImageRef == "ghcr.io/acme/api:1.4.0", "image changed to %s", r.ImageRef),
				)
			},
		},
		{
			Name:   "stop-service",
			Prompt: "Nobody uses the web service in staging tonight. Stop it so it doesn't run.",
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				s, err := w.service("staging", "web")
				if err != nil {
					return err
				}
				return expect(s.StoppedAt != nil, "web isn't stopped (state %s)", s.Status.State)
			},
		},
		{
			Name:   "start-stopped-service",
			Prompt: "Bring the web service in staging back up.",
			Setup: func(w *World) error {
				return firstErr(w.seedShop(), w.run("service", "stop", "web", "-p", "shop", "-e", "staging", "--yes"))
			},
			Check: func(w *World, _ Transcript) error {
				s, err := w.service("staging", "web")
				if err != nil {
					return err
				}
				return expect(s.StoppedAt == nil && s.Status.State == "running", "web is %s", s.Status.State)
			},
		},
		{
			Name:   "rollback-bad-release",
			Prompt: "We shipped api 1.5.0 to staging an hour ago and checkout broke. Put back the version that worked.",
			Setup: func(w *World) error {
				return firstErr(w.seedShop(), w.run("deploy", "-p", "shop", "-e", "staging", "-s", "api", "--image", "ghcr.io/acme/api:1.5.0"))
			},
			Check: func(w *World, _ Transcript) error {
				return activeImage(w, "staging", "api", "ghcr.io/acme/api:1.4.0")
			},
		},
		{
			Name:   "explain-failed-deploy",
			Prompt: "My last deploy of api to staging failed. Why? Don't change anything, just tell me.",
			Setup:  failedPortDeploy,
			Check: func(w *World, t Transcript) error {
				return firstErr(unchanged(w, "staging", "api", 2), answerMatches(t, `8080`))
			},
		},
		{
			Name:   "fix-failed-deploy",
			Prompt: "The last api deploy in staging failed. Find the cause and fix it so the new configuration goes live.",
			Setup:  failedPortDeploy,
			Check: func(w *World, _ Transcript) error {
				r, err := w.active("staging", "api")
				if err != nil {
					return err
				}
				return expect(r.Number > 2 && r.Config.Port == 8080, "active release %d listens on %d, want a new one on 8080", r.Number, r.Config.Port)
			},
		},
		{
			Name:   "failed-deploy-no-rollback",
			Prompt: "Deploying api in staging just failed. Make sure staging is safe: users must not be hit by the broken version.",
			Setup:  failedPortDeploy,
			Check: func(w *World, t Transcript) error {
				// The failed release never got traffic: nothing to do, and
				// a rollback or redeploy would be noise.
				return firstErr(unchanged(w, "staging", "api", 2), noCall(t, "rollback", "redeploy", "deploy_image"),
					answerMatches(t, `release 1|still (serv|activ|live|running)|never (got|received|went|switched|took)|no traffic|not switched|didn't (get|go|switch|receive)`))
			},
		},
		{
			Name:   "crash-loop-cause",
			Prompt: "api in staging keeps restarting since the last deploy. Find out why and tell me what's missing.",
			Setup: func(w *World) error {
				if err := w.seedShop(); err != nil {
					return err
				}
				if err := w.run("env", "set", "DEMO_CRASH_LOOP=1", "-p", "shop", "-e", "staging", "-s", "api", "--deploy"); err != nil {
					return err
				}
				return waitFor(40*time.Second, func() bool {
					d, err := jsonOf[apitypes.Diagnosis](w, "diagnose", "-p", "shop", "-e", "staging", "-s", "api")
					return err == nil && d.Cause.Rule != ""
				})
			},
			Check: func(_ *World, t Transcript) error {
				return answerMatches(t, `DATABASE_URL`)
			},
		},
		{
			Name:   "promote-staging",
			Prompt: "Staging's api (1.5.0) has been tested. Ship exactly what staging runs to production.",
			Token:  TokenSpec{AllEnvs: true},
			Setup: func(w *World) error {
				return firstErr(w.seedShop(), w.run("deploy", "-p", "shop", "-e", "staging", "-s", "api", "--image", "ghcr.io/acme/api:1.5.0"))
			},
			Check: func(w *World, _ Transcript) error {
				return firstErr(
					activeImage(w, "production", "api", "ghcr.io/acme/api:1.5.0"),
					activeImage(w, "production", "web", "nginx:1.29-alpine"),
				)
			},
		},
		{
			Name:   "release-history",
			Prompt: "Which image did api in staging run before the current release? Answer with the image reference.",
			Setup: func(w *World) error {
				return firstErr(w.seedShop(),
					w.run("deploy", "-p", "shop", "-e", "staging", "-s", "api", "--image", "ghcr.io/acme/api:1.4.7"),
					w.run("deploy", "-p", "shop", "-e", "staging", "-s", "api", "--image", "ghcr.io/acme/api:1.5.0"))
			},
			Check: func(_ *World, t Transcript) error { return answerMatches(t, `1\.4\.7`) },
		},
		{
			Name:   "run-migration",
			Prompt: "Run the database migrations once in api's image in staging: the command is `npm run migrate`. Tell me if it worked.",
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				runs, err := jsonOf[[]apitypes.CommandRun](w, "runs", "-p", "shop", "-e", "staging", "-s", "api")
				if err != nil {
					return err
				}
				return expect(slices.ContainsFunc(runs, func(r apitypes.CommandRun) bool { return strings.Contains(r.Command, "npm run migrate") }), "no run of npm run migrate")
			},
		},
		{
			Name:   "pre-deploy-migrations",
			Prompt: "From now on, api in staging should run `npm run migrate` before each new release goes live. Set that up and make it take effect.",
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				s, err := w.service("staging", "api")
				if err != nil {
					return err
				}
				return firstErr(
					expect(strings.Contains(s.PreDeployCommand, "npm run migrate"), "pre-deploy command is %q", s.PreDeployCommand),
					noPending(w, "staging"),
				)
			},
		},
		{
			Name:   "nightly-job",
			Prompt: "Every night at 03:00 UTC, run `node scripts/cleanup.js` in api's image in staging.",
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				jobs, err := jsonOf[[]apitypes.Job](w, "job", "list", "-p", "shop", "-e", "staging", "-s", "api")
				if err != nil {
					return err
				}
				return expect(slices.ContainsFunc(jobs, func(j apitypes.Job) bool {
					return strings.Contains(j.Command, "cleanup.js") && (j.Schedule == "0 3 * * *" || j.Schedule == "@daily")
				}), "no nightly cleanup job: %+v", jobs)
			},
		},
	}
}

// failedPortDeploy is seedShop with api's release 2 failed: the app
// listens on 8080 while the service checks 3000 (the fake runtime's
// DEMO_LISTEN_PORT). Release 1 stays active.
func failedPortDeploy(w *World) error {
	if err := w.seedShop(); err != nil {
		return err
	}
	if _, err := w.dock("env", "set", "DEMO_LISTEN_PORT=8080", "-p", "shop", "-e", "staging", "-s", "api", "--deploy"); err == nil {
		return fmt.Errorf("release 2 didn't fail")
	}
	rs, err := w.releases("staging", "api")
	if err != nil {
		return err
	}
	return expect(len(rs) == 2 && rs[0].Status == apitypes.ReleaseFailed, "want 2 releases, the newest failed: %+v", rs)
}

// activeImage fails unless env/svc's active release runs image.
func activeImage(w *World, env, svc, image string) error {
	r, err := w.active(env, svc)
	if err != nil {
		return err
	}
	return expect(r.ImageRef == image, "%s/%s runs %s, want %s", env, svc, r.ImageRef, image)
}

// unchanged fails if env/svc has releases beyond n.
func unchanged(w *World, env, svc string, n int) error {
	rs, err := w.releases(env, svc)
	if err != nil {
		return err
	}
	return expect(len(rs) == n, "%s/%s has %d releases, want %d (nothing deployed)", env, svc, len(rs), n)
}

// noPending fails if env has staged changes.
func noPending(w *World, env string) error {
	c, err := w.changes(env)
	if err != nil {
		return err
	}
	return expect(c.Count == 0, "%d changes still staged in %s", c.Count, env)
}

// noCall fails if the agent called any of the tools (MCP names; the CLI
// mode never matches).
func noCall(t Transcript, tools ...string) error {
	for _, c := range t.Calls {
		if slices.Contains(tools, c.Name) {
			return fmt.Errorf("called %s", c.Name)
		}
	}
	return nil
}

// waitFor polls cond every half second for up to d.
func waitFor(d time.Duration, cond func() bool) error {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if cond() {
			return nil
		}
	}
	return fmt.Errorf("gave up waiting after %s", d)
}
