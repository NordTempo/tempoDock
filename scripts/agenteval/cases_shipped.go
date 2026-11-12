package main

import (
	"slices"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// shippedCases cover features that shipped without agent coverage:
// maintenance over a locked environment (#258), vulnerability ignores and
// package versions (ADR 0074), traces by release (ADR 0075), a failed
// deploy's diagnosis, multi-key Redis changesets and bare table names
// (#277).
func shippedCases() []Case {
	return []Case{
		{
			Name:   "maintenance-locked",
			Prompt: "Schedule maintenance on the acme status page for the API component tomorrow from 02:00 to 03:00 UTC, for a database upgrade.",
			Token:  TokenSpec{AllEnvs: true},
			Setup: func(w *World) error {
				return firstErr(seedStatusPage(w), w.run("environment", "lock", "production", "-p", "shop", "--reason", "live"))
			},
			Admin: &Decision{Approve: true},
			Check: func(w *World, t Transcript) error {
				in, err := incidents(w)
				if err != nil {
					return err
				}
				rs, err := w.requests()
				if err != nil {
					return err
				}
				return firstErr(
					expect(slices.ContainsFunc(in, func(i apitypes.StatusIncident) bool { return i.Kind == apitypes.KindMaintenance }), "no maintenance window: %+v", in),
					expect(len(rs) == 1 && rs[0].Status == apitypes.RequestDone, "want one approved request: %+v", rs),
					answerMatches(t, `approv`))
			},
		},
		{
			Name:   "maintenance-complete",
			Prompt: "The database upgrade is done. Complete the maintenance that's running on the acme status page.",
			Token:  TokenSpec{AllEnvs: true},
			Setup: func(w *World) error {
				return firstErr(seedStatusPage(w),
					w.run("maintenance", "schedule", "acme", "Database upgrade", "--component", "API", "--for", "2h"),
					w.run("environment", "lock", "production", "-p", "shop", "--reason", "live"))
			},
			Check: func(w *World, _ Transcript) error {
				in, err := incidents(w)
				if err != nil {
					return err
				}
				rs, _ := w.requests()
				return firstErr(
					expect(len(in) == 1 && in[0].State == apitypes.MaintenanceCompleted, "maintenance isn't completed: %+v", in),
					expect(len(rs) == 0, "completing needs no approval, but %d requests were filed", len(rs)))
			},
		},
		{
			Name:   "vuln-ignore",
			Prompt: "CVE-2026-1001 in busybox doesn't affect api in staging: we never call that applet. Ignore it there.",
			Env:    map[string]string{"DOCK_FAKE_FINDINGS": "{dir}/findings.jsonl"},
			Setup:  seedVulns,
			Check: func(w *World, t Transcript) error {
				ig, err := get[apitypes.VulnIgnores](w.daemon, "/org/vulnerabilities/ignores")
				if err != nil {
					return err
				}
				// Over MCP no tool ignores (ADR 0074): an agent asks a person.
				// The CLI lets a deploy token ignore in its own service.
				if len(ig.Ignores) == 0 {
					return answerMatches(t, `person|human|admin|UI|CLI|can't|cannot|no tool`)
				}
				g := ig.Ignores[0]
				return expect(len(ig.Ignores) == 1 && g.Vulnerability == "CVE-2026-1001" && g.Scope == apitypes.IgnoreService &&
					g.Service == "api" && g.Environment == "staging" && g.Reason != "", "wrong ignore: %+v", ig.Ignores)
			},
		},
		{
			Name:   "package-version",
			Prompt: "Which version of openssl is in the image api runs in production?",
			Token:  TokenSpec{Scope: apitypes.ScopeRead, AllEnvs: true},
			Env:    map[string]string{"DOCK_FAKE_FINDINGS": "{dir}/findings.jsonl"},
			Setup:  seedVulns,
			Check:  func(_ *World, t Transcript) error { return answerMatches(t, `3\.0\.13`) },
		},
		{
			Name:   "traces-by-release",
			Prompt: "Which route had the slowest requests served by release 1 of api in staging?",
			Env:    map[string]string{"DOCK_FAKE_SPANS": "{dir}/spans.jsonl"},
			Setup:  seedTraces,
			Check: func(_ *World, t Transcript) error {
				return firstErr(answerMatches(t, `/search`), noAnswer(t, `slowest[^.]*/checkout`))
			},
		},
		{
			Name:   "failed-deploy-running",
			Prompt: "The last api deploy in staging failed. Did the app crash, or was it running? What should I change?",
			Setup:  failedPortDeploy,
			Check: func(w *World, t Transcript) error {
				return firstErr(unchanged(w, "staging", "api", 2), answerMatches(t, `8080`, `running|didn't crash|did not crash|no crash|not crash|never crashed`))
			},
		},
		{
			Name:   "redis-several-keys",
			Prompt: "In the cache service in staging, turn on both flags:checkout and flags:search, and delete the stale config:banner key, as one change.",
			Needs:  "redis",
			Token:  TokenSpec{Data: []string{"--data-non-production", "write"}},
			Setup:  withPolicy(seedRedis, "deploy.non-production=write"),
			Check: func(w *World, _ Transcript) error {
				co, _ := w.DB.redis("GET", "flags:checkout")
				se, _ := w.DB.redis("GET", "flags:search")
				ex, _ := w.DB.redis("EXISTS", "config:banner")
				cs, err := get[[]apitypes.DataChangeset](w.daemon, "/projects/shop/environments/staging/services/cache/data/changesets")
				if err != nil {
					return err
				}
				committed := 0
				for _, c := range cs {
					if c.Status == apitypes.ChangesetCommitted {
						committed++
					}
				}
				return firstErr(
					expect(co == "on" && se == "on" && ex == "0", "flags %q %q, banner exists %s", co, se, ex),
					expect(committed == 1, "%d changesets committed, want one", committed))
			},
		},
		{
			Name:   "data-latest-rows",
			Prompt: "Show me the three newest orders (highest id) in the db database in staging, with their totals.",
			Needs:  "postgres",
			Token:  TokenSpec{Data: []string{"--data-non-production", "read"}},
			Setup:  seedPostgres("staging"),
			Check: func(_ *World, t Transcript) error {
				return answerMatches(t, `\b17\b`, `\b16\b`, `\b15\b`, `170`)
			},
		},
	}
}

// seedSearchSpans are release 1's slow searches, which seedTraces adds:
// the slowest route of release 1, and slower than nothing on release 2.
func seedSearchSpans(span func(trace, id, parent int, rel int64, ms int, name, route string, entry bool), r1 int64) {
	for i := 201; i <= 205; i++ {
		span(i, i*10, 0, r1, 900+i, "GET /search", "/search", true)
	}
}
