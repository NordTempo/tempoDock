package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// observeCases read logs, errors, traces, vulnerabilities and alerts, and
// keep status pages up to date.
func observeCases() []Case {
	return []Case{
		{
			Name:   "logs-find-order",
			Prompt: "A customer says their payment failed. Find the payment error in api's logs in staging and tell me the order number.",
			Env:    map[string]string{"DOCK_FAKE_LOGS": "{dir}/logs.txt"},
			Setup:  withLogs(manyLogLines("E Error: payment declined: card_expired (order 48213)")),
			Check:  func(_ *World, t Transcript) error { return answerMatches(t, `48213`) },
		},
		{
			Name:   "errors-overview",
			Prompt: "What errors is the api service in staging throwing? Give me the most frequent one.",
			Env:    map[string]string{"DOCK_FAKE_LOGS": "{dir}/logs.txt"},
			// A dev daemon's demo lines predate the log followers, so they
			// never become issues: the errors are in the logs only.
			Setup: withLogs(strings.Repeat("E TypeError: Cannot read properties of undefined (reading 'sku')\nE     at addToCart (/app/src/cart.js:41:17)\n", 6) +
				"E Error: connect ECONNREFUSED 10.0.0.7:6379\nGET /api/cart 200 9ms\n"),
			Check: func(_ *World, t Transcript) error { return answerMatches(t, `sku`) },
		},
		{
			Name:   "slowest-traces",
			Prompt: "Checkout feels slow on api in staging since release 2. Which route is slowest, and where does that request spend its time?",
			Token:  TokenSpec{},
			Env:    map[string]string{"DOCK_FAKE_SPANS": "{dir}/spans.jsonl"},
			Setup:  seedTraces,
			Check: func(_ *World, t Transcript) error {
				return answerMatches(t, `/checkout`, `inventory|reserve`)
			},
		},
		{
			Name:   "vulnerable-production",
			Prompt: "Which images live in production have critical vulnerabilities, and what version of the package fixes them?",
			Token:  TokenSpec{Scope: apitypes.ScopeRead, AllEnvs: true},
			Env:    map[string]string{"DOCK_FAKE_FINDINGS": "{dir}/findings.jsonl"},
			Setup:  seedVulns,
			Check: func(_ *World, t Transcript) error {
				return answerMatches(t, `CVE-2026-31337`, `3\.0\.17`)
			},
		},
		{
			Name:   "open-alerts",
			Prompt: "Is anything wrong in staging right now? Check the alerts and tell me which service needs attention.",
			Setup: func(w *World) error {
				return firstErr(failedPortDeploy(w), waitFor(20*time.Second, func() bool {
					as, err := jsonOf[[]json.RawMessage](w, "alerts")
					return err == nil && len(as) > 0
				}))
			},
			Check: func(_ *World, t Transcript) error { return answerMatches(t, `\bapi\b`) },
		},
		{
			Name:   "incident-draft",
			Prompt: "Checkout on the api is slow for some customers. Write a draft incident on the acme status page saying we're investigating slow checkouts; don't publish it, I'll review it.",
			Token:  TokenSpec{AllEnvs: true},
			Setup:  seedStatusPage,
			Check: func(w *World, _ Transcript) error {
				in, err := incidents(w)
				if err != nil {
					return err
				}
				return expect(slices.ContainsFunc(in, func(i apitypes.StatusIncident) bool {
					return i.State == apitypes.IncidentDraft && i.PublishedAt == nil
				}) && !slices.ContainsFunc(in, func(i apitypes.StatusIncident) bool { return i.PublishedAt != nil }), "want one unpublished draft: %+v", in)
			},
		},
		{
			Name:   "incident-resolve",
			Prompt: "The slow checkout incident on the acme status page is fixed. Resolve it with a short note.",
			Token:  TokenSpec{AllEnvs: true},
			Setup: func(w *World) error {
				return firstErr(seedStatusPage(w), w.run("incident", "open", "acme", "Slow checkouts", "--component", "API:degraded", "-m", "We are investigating."))
			},
			Check: func(w *World, _ Transcript) error {
				in, err := incidents(w)
				if err != nil {
					return err
				}
				return expect(len(in) == 1 && in[0].State == apitypes.IncidentResolved, "incident isn't resolved: %+v", in)
			},
		},
		{
			Name:   "schedule-maintenance",
			Prompt: "Schedule maintenance on the acme status page for the API component: tomorrow from 02:00 to 03:00 UTC, for a database upgrade.",
			Token:  TokenSpec{AllEnvs: true},
			Setup:  seedStatusPage,
			Check: func(w *World, _ Transcript) error {
				in, err := incidents(w)
				if err != nil {
					return err
				}
				tomorrow := time.Now().UTC().Add(24 * time.Hour).Format("2006-01-02")
				return expect(slices.ContainsFunc(in, func(i apitypes.StatusIncident) bool {
					return i.Kind == apitypes.KindMaintenance && i.StartedAt.UTC().Format("2006-01-02T15") == tomorrow+"T02" &&
						i.EndsAt != nil && i.EndsAt.UTC().Format("15:04") == "03:00"
				}), "no maintenance tomorrow 02:00-03:00 UTC: %+v", in)
			},
		},
	}
}

// withLogs is seedShop with every container's output set to lines (the
// case's Env points DOCK_FAKE_LOGS at {dir}/logs.txt).
func withLogs(lines string) func(*World) error {
	return func(w *World) error {
		if err := os.WriteFile(filepath.Join(w.Vars["dir"], "logs.txt"), []byte(lines), 0o644); err != nil {
			return err
		}
		return w.seedShop()
	}
}

// manyLogLines buries needle in 300 lines of request logs, so finding it
// takes a filter rather than a tail.
func manyLogLines(needle string) string {
	var b strings.Builder
	for i := range 300 {
		if i == 40 {
			b.WriteString(needle + "\n")
		}
		fmt.Fprintf(&b, "GET /api/products/%d 200 %dms\n", i, 5+i%9)
	}
	return b.String()
}

// seedTraces is seedShop with tracing on and api's spans in
// DOCK_FAKE_SPANS: fast product reads on release 1, and on release 2 a
// /checkout that waits 2.3 s on an inventory reservation.
func seedTraces(w *World) error {
	if err := w.seedShop(); err != nil {
		return err
	}
	if err := w.run("deploy", "-p", "shop", "-e", "staging", "-s", "api", "--image", "ghcr.io/acme/api:1.5.0"); err != nil {
		return err
	}
	svc, err := w.service("staging", "api")
	if err != nil {
		return err
	}
	env, err := get[apitypes.Environment](w.daemon, "/projects/shop/environments/staging")
	if err != nil {
		return err
	}
	rs, err := w.releases("staging", "api")
	if err != nil {
		return err
	}
	o, err := get[apitypes.ProjectOverview](w.daemon, "/projects/shop/overview")
	if err != nil {
		return err
	}
	var lines []string
	now := time.Now().Add(-30 * time.Minute)
	span := func(trace, id, parent int, rel int64, ms int, name, route string, entry bool) {
		s := map[string]any{"org": 1, "project": o.Project.ID, "env": env.ID, "service": svc.ID, "release": rel,
			"trace": fmt.Sprintf("%032x", trace), "span": fmt.Sprintf("%016x", id), "start": now.Add(time.Duration(trace) * time.Second).UnixMicro(),
			"dur": ms * 1000, "name": name}
		if parent != 0 {
			s["parent"] = fmt.Sprintf("%016x", parent)
		}
		if entry {
			s["entry"], s["kind"], s["method"], s["route"], s["http_status"] = true, 2, strings.Fields(name)[0], route, 200
		}
		b, _ := json.Marshal(s)
		lines = append(lines, string(b))
	}
	r1, r2 := rs[1].ID, rs[0].ID
	for i := 1; i <= 10; i++ {
		span(i, i*10, 0, r1, 20+i, "GET /products", "/products", true)
	}
	seedSearchSpans(span, r1)
	for i := 11; i <= 20; i++ {
		span(i, i*10, 0, r2, 30+i, "GET /products", "/products", true)
		span(i+100, (i+100)*10, 0, r2, 2400+i, "POST /checkout", "/checkout", true)
		span(i+100, (i+100)*10+1, (i+100)*10, r2, 2300, "inventory.reserve", "", false)
		span(i+100, (i+100)*10+2, (i+100)*10, r2, 40, "SELECT orders", "", false)
	}
	if err := os.WriteFile(filepath.Join(w.Vars["dir"], "spans.jsonl"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return err
	}
	if err := w.run("tracing", "on"); err != nil {
		return err
	}
	return waitFor(30*time.Second, func() bool {
		l, err := jsonOf[apitypes.TraceList](w, "traces", "-p", "shop", "-e", "staging", "-s", "api")
		return err == nil && len(l.Traces) >= 20
	})
}

// seedVulns is seedShop with scanning on and a critical OpenSSL finding in
// every image.
func seedVulns(w *World) error {
	findings := `{"ID":"CVE-2026-31337","Severity":5,"Package":"openssl","Version":"3.0.13","Type":"apk","FixedIn":"3.0.17"}
{"ID":"CVE-2026-1001","Severity":2,"Package":"busybox","Version":"1.36.1","Type":"apk"}`
	if err := os.WriteFile(filepath.Join(w.Vars["dir"], "findings.jsonl"), []byte(findings), 0o644); err != nil {
		return err
	}
	if err := w.run("scanning", "on"); err != nil {
		return err
	}
	if err := w.seedShop(); err != nil {
		return err
	}
	return waitFor(30*time.Second, func() bool {
		out, err := w.dock("images")
		return err == nil && strings.Count(string(out), `"state": "scanned"`) == 2
	})
}

// seedStatusPage is seedShop with a published status page acme showing
// production's api and web.
func seedStatusPage(w *World) error {
	return firstErr(w.seedShop(), w.runAll(
		"status-page create acme",
		"status-page component add acme API --service shop/production/api",
		"status-page component add acme Website --service shop/production/web",
		"status-page publish acme",
	))
}

// incidents are status page acme's incidents and maintenance, drafts
// included.
func incidents(w *World) ([]apitypes.StatusIncident, error) {
	p, err := get[apitypes.StatusPage](w.daemon, "/status-pages/acme")
	return p.Incidents, err
}
