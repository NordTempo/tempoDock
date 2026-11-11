package main

import (
	"regexp"
	"slices"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// configCases stage and apply variables, settings, domains and volumes,
// and sleep, wake and clone environments.
func configCases() []Case {
	return []Case{
		{
			Name:   "set-variable-live",
			Prompt: "Set LOG_LEVEL=debug on the api service in staging and make it take effect.",
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				return firstErr(liveVar(w, "staging", "api", "LOG_LEVEL", "debug"), noPending(w, "staging"), newRelease(w, "staging", "api", 1))
			},
		},
		{
			Name:   "set-shared-variable",
			Prompt: "Turn on FEATURE_CHECKOUT_V2=true for every service in staging, live.",
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				return firstErr(liveVar(w, "staging", "", "FEATURE_CHECKOUT_V2", "true"), noPending(w, "staging"))
			},
		},
		{
			Name:   "stage-variable-only",
			Prompt: "Stage SENTRY_DSN=https://abc@o1.ingest.sentry.io/42 for api in staging, but don't deploy it: I want to review it first.",
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				vs, err := w.variables("staging")
				if err != nil {
					return err
				}
				staged := slices.ContainsFunc(vs, func(v apitypes.Variable) bool { return v.Key == "SENTRY_DSN" && v.Pending == apitypes.ChangeAdd })
				return firstErr(expect(staged, "SENTRY_DSN isn't staged"), unchanged(w, "staging", "api", 1))
			},
		},
		{
			Name:   "unset-variable",
			Prompt: "Remove the OLD_PAYMENTS_FLAG variable from api in staging; it isn't used anymore. Make it live.",
			Setup: func(w *World) error {
				return firstErr(w.seedShop(), w.run("env", "set", "OLD_PAYMENTS_FLAG=1", "KEEP_ME=1", "-p", "shop", "-e", "staging", "-s", "api", "--deploy"))
			},
			Check: func(w *World, _ Transcript) error {
				_, gone, err := w.variable("staging", "api", "OLD_PAYMENTS_FLAG")
				_, kept, _ := w.variable("staging", "api", "KEEP_ME")
				return firstErr(err, expect(!gone, "OLD_PAYMENTS_FLAG is still set"), expect(kept, "KEEP_ME was removed too"), noPending(w, "staging"))
			},
		},
		{
			Name:   "discard-staged",
			Prompt: "Someone staged changes in staging that we don't want. Throw away everything staged there without deploying anything.",
			Setup: func(w *World) error {
				return firstErr(w.seedShop(),
					w.run("env", "set", "BROKEN=1", "-p", "shop", "-e", "staging", "-s", "api"),
					w.run("service", "update", "web", "-p", "shop", "-e", "staging", "--memory", "2048"))
			},
			Check: func(w *World, _ Transcript) error {
				return firstErr(noPending(w, "staging"), unchanged(w, "staging", "api", 1), unchanged(w, "staging", "web", 1))
			},
		},
		{
			Name:   "review-staged",
			Prompt: "What is staged but not yet live in staging? List each change.",
			Setup: func(w *World) error {
				return firstErr(w.seedShop(),
					w.run("env", "set", "CACHE_TTL=300", "-p", "shop", "-e", "staging", "-s", "api"),
					w.run("service", "update", "web", "-p", "shop", "-e", "staging", "--memory", "768"))
			},
			Check: func(w *World, t Transcript) error {
				c, err := w.changes("staging")
				return firstErr(err, expect(c.Count == 2, "staged changes went from 2 to %d", c.Count), answerMatches(t, `CACHE_TTL`, `768`))
			},
		},
		{
			Name:   "memory-limit",
			Prompt: "api in staging runs out of memory. Give it 1 GB and roll that out.",
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				r, err := w.active("staging", "api")
				if err != nil {
					return err
				}
				return firstErr(expect(r.Config.MemoryMB == 1024, "active release has %d MB", r.Config.MemoryMB), noPending(w, "staging"))
			},
		},
		{
			Name:   "add-domain",
			Prompt: "Serve the web service in staging at staging.shop-example.com.",
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				return hasDomain(w, "staging", "web", "staging.shop-example.com")
			},
		},
		{
			Name:   "generated-domain",
			Prompt: "Give the api service in staging a public URL so I can try it, and tell me the URL.",
			Setup:  (*World).seedShop,
			Check: func(w *World, t Transcript) error {
				ds, err := w.domains("staging", "api")
				if err != nil {
					return err
				}
				if len(ds) == 0 {
					return expect(false, "api has no domain")
				}
				return answerMatches(t, regexp.QuoteMeta(ds[0].Hostname))
			},
		},
		{
			Name:   "domain-dns-records",
			Prompt: "api.shop-example.com on the api service in staging isn't working yet. What DNS record do I need to create?",
			Env:    map[string]string{"DOCK_FAKE_PENDING_DOMAINS": "api.shop-example.com"},
			Setup: func(w *World) error {
				return firstErr(w.seedShop(), w.run("domain", "add", "api.shop-example.com", "-p", "shop", "-e", "staging", "-s", "api", "--detach"))
			},
			Check: func(w *World, t Transcript) error {
				return firstErr(hasDomain(w, "staging", "api", "api.shop-example.com"), answerMatches(t, `\bA\b`, `127\.0\.0\.1`))
			},
		},
		{
			Name:   "remove-domain",
			Prompt: "Remove old.shop-example.com from web in staging. Keep its other domains.",
			Setup: func(w *World) error {
				return firstErr(w.seedShop(),
					w.run("domain", "add", "old.shop-example.com", "-p", "shop", "-e", "staging", "-s", "web", "--detach"),
					w.run("domain", "add", "new.shop-example.com", "-p", "shop", "-e", "staging", "-s", "web", "--detach"))
			},
			Check: func(w *World, _ Transcript) error {
				ds, err := w.domains("staging", "web")
				if err != nil {
					return err
				}
				has := func(h string) bool {
					return slices.ContainsFunc(ds, func(d apitypes.Domain) bool { return d.Hostname == h })
				}
				return firstErr(expect(!has("old.shop-example.com"), "old domain still there"), expect(has("new.shop-example.com"), "new domain removed too"))
			},
		},
		{
			Name:   "add-volume",
			Prompt: "web in staging must keep its uploaded files across deploys: give it persistent storage mounted at /data, live.",
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				vs, err := jsonOf[[]apitypes.Volume](w, "volume", "list", "-p", "shop", "-e", "staging", "-s", "web")
				if err != nil {
					return err
				}
				return firstErr(
					expect(slices.ContainsFunc(vs, func(v apitypes.Volume) bool { return v.MountPath == "/data" }), "no volume at /data: %+v", vs),
					noPending(w, "staging"))
			},
		},
		{
			Name:   "sleep-staging",
			Prompt: "Nobody uses staging overnight. Put the whole environment to sleep.",
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				e, err := get[apitypes.Environment](w.daemon, "/projects/shop/environments/staging")
				if err != nil {
					return err
				}
				return expect(e.Sleep != nil && (e.Sleep.State == apitypes.SleepAsleep || e.Sleep.State == apitypes.SleepGoing), "staging isn't asleep: %+v", e.Sleep)
			},
		},
		{
			Name:   "wake-staging",
			Prompt: "Staging is asleep and QA starts now. Wake it up.",
			Setup: func(w *World) error {
				return firstErr(w.seedShop(), w.run("environment", "sleep", "staging", "-p", "shop"))
			},
			Check: func(w *World, _ Transcript) error {
				e, err := get[apitypes.Environment](w.daemon, "/projects/shop/environments/staging")
				if err != nil {
					return err
				}
				return expect(e.Sleep == nil || e.Sleep.State == apitypes.SleepAwake || e.Sleep.State == apitypes.SleepWaking, "staging is still asleep")
			},
		},
		{
			Name:   "clone-environment",
			Prompt: "Make a qa environment in shop that is a copy of staging.",
			Token:  TokenSpec{AllEnvs: true},
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				o, err := get[apitypes.ProjectOverview](w.daemon, "/projects/shop/overview")
				if err != nil {
					return err
				}
				i := slices.IndexFunc(o.Environments, func(e apitypes.EnvironmentOverview) bool { return e.Environment.Name == "qa" })
				return expect(i >= 0 && len(o.Environments[i].Services) == 2, "no qa environment with web and api")
			},
		},
	}
}

// liveVar fails unless env has the variable live (not only staged) with
// value; svc "" is the environment's shared variable.
func liveVar(w *World, env, svc, key, value string) error {
	v, ok, err := w.variable(env, svc, key)
	if err != nil {
		return err
	}
	if !ok || v.Pending != "" {
		return expect(false, "%s isn't live on %q in %s", key, svc, env)
	}
	return expect(v.Value != nil && *v.Value == value, "%s is %v, want %s", key, v.Value, value)
}

// newRelease fails unless env/svc has more than n releases and the newest
// is active.
func newRelease(w *World, env, svc string, n int) error {
	rs, err := w.releases(env, svc)
	if err != nil {
		return err
	}
	return expect(len(rs) > n && rs[0].Status == apitypes.ReleaseActive, "no new active release of %s/%s", env, svc)
}

// hasDomain fails unless env/svc has hostname.
func hasDomain(w *World, env, svc, hostname string) error {
	ds, err := w.domains(env, svc)
	if err != nil {
		return err
	}
	return expect(slices.ContainsFunc(ds, func(d apitypes.Domain) bool { return d.Hostname == hostname }), "%s/%s has no domain %s", env, svc, hostname)
}
