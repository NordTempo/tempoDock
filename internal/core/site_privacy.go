package core

import (
	"strconv"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Words shared by every site's privacy status (ADR 0085). The basics'
// line is Saile's wording: honest about Norway, where ekomloven § 3-15
// has no exemption for statistics, and still useful.
const (
	privacyBasics    = "Most sites run this without a banner."
	privacyNorway    = "Norwegian law has no explicit exemption for statistics."
	privacyNoCookies = "No cookies."
)

// sitePrivacy is what settings (all set, as in Site.Settings) mean for
// consent: a line for the site and one for each thing it collects. Every
// feature belongs in the site's privacy policy; only session replay needs
// consent in every reading, and the consent mode says whether it (or
// everything) waits for dock.consent(true).
func sitePrivacy(set apitypes.SiteSettings) apitypes.SitePrivacy {
	mode := *set.Consent
	basics := apitypes.PrivacyNotNeeded
	if mode == apitypes.ConsentAll {
		basics = apitypes.PrivacyAfterConsent
	}
	p := apitypes.SitePrivacy{Features: []apitypes.PrivacyFeature{
		{Name: "Visits, sources, devices, locations", Consent: basics,
			Why: "Nothing is stored on the device; a visitor is a hash that changes daily."},
		{Name: "Custom events", Consent: basics, Why: "As long as dock.track sends no names, emails or IDs."},
	}}
	if *set.ErrorsAndVitals {
		why := "Finding problems and measuring speed. Error messages can hold personal data."
		if *set.Public && *set.PublicErrors {
			why += " The public dashboard shows them."
		}
		p.Features = append(p.Features, apitypes.PrivacyFeature{Name: "Errors and Web Vitals", Consent: basics, Why: why})
	}
	replay := *set.Replay
	if replay {
		f := apitypes.PrivacyFeature{Name: "Session replay, " + strconv.Itoa(*set.ReplaySampleRate) + "%",
			Consent: apitypes.PrivacyAfterConsent, Why: "Recording a visit needs consent everywhere. Inputs are masked."}
		if *set.MaskAllText {
			f.Why = "Recording a visit needs consent everywhere. All text is masked."
		}
		if mode == apitypes.ConsentNone {
			f.Consent = apitypes.PrivacyNeedsConsent
		}
		p.Features = append(p.Features, f)
	}
	switch {
	case replay && mode == apitypes.ConsentNone:
		p.NeedsConsent = true
		p.Summary = "Needs consent for: session replay."
		p.Detail = "Load the script only after your banner's consent, or let replay wait for dock.consent(true)."
	case mode == apitypes.ConsentAll:
		p.Summary = privacyNoCookies + " Nothing runs before consent."
		p.Detail = "Your banner calls dock.consent(true) when the visitor accepts."
	case replay:
		p.Summary = privacyNoCookies + " Replay waits for consent."
		p.Detail = "Most sites run the rest without a banner. " + privacyNorway
	default:
		p.Summary = privacyNoCookies + " " + privacyBasics
		p.Detail = privacyNorway
	}
	return p
}
