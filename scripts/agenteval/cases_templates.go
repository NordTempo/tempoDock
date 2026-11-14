package main

import (
	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// templateCases set up apps from templates (ADR 0081): find one, fill its
// inputs, and use it instead of building the services by hand.
func templateCases() []Case {
	return []Case{
		{
			Name:   "template-umami",
			Prompt: "Set up Umami web analytics in staging.",
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				return firstErr(fromTemplate(w, "staging", "umami", "umami"), fromTemplate(w, "staging", "umami-db", "umami"))
			},
		},
		{
			Name:   "template-input-and-domain",
			Prompt: "Add Grafana to staging at grafana.shop.example.com. Its admin password should be Tr0ub4dor-grafana.",
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				v, ok, err := w.variable("staging", "grafana", "GF_SECURITY_ADMIN_PASSWORD")
				ds, derr := w.domains("staging", "grafana")
				return firstErr(err, derr, fromTemplate(w, "staging", "grafana", "grafana"),
					expect(ok && v.Value != nil && *v.Value == "Tr0ub4dor-grafana", "admin password not set as asked"),
					expect(len(ds) == 1 && ds[0].Hostname == "grafana.shop.example.com", "domains %+v", ds))
			},
		},
		{
			// The input has a name the prompt doesn't give: the agent
			// reads the template's inputs instead of inventing a variable.
			Name:   "template-choice-input",
			Prompt: "Set up Vaultwarden in staging, with new sign-ups turned off.",
			Setup:  (*World).seedShop,
			Check: func(w *World, _ Transcript) error {
				v, ok, err := w.variable("staging", "vaultwarden", "SIGNUPS_ALLOWED")
				return firstErr(err, fromTemplate(w, "staging", "vaultwarden", "vaultwarden"),
					expect(ok && v.Value != nil && *v.Value == "false", "SIGNUPS_ALLOWED isn't false"))
			},
		},
	}
}

// fromTemplate fails unless env/svc was created from template and its
// first release is active.
func fromTemplate(w *World, env, svc, template string) error {
	s, err := w.service(env, svc)
	if err != nil {
		return err
	}
	r, err := w.active(env, svc)
	return firstErr(err, expect(s.FromTemplate != nil && s.FromTemplate.Name == template, "%s/%s wasn't created from the %s template", env, svc, template),
		expect(r.Status == apitypes.ReleaseActive, "%s/%s isn't live", env, svc))
}
