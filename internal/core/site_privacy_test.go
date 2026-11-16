package core

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A site's consent mode decides what its script holds back, and its
// privacy status says what that means: replay is the only feature that
// needs consent, and the only one that waits for it by default.
func TestSiteConsent(t *testing.T) {
	h := newHarness(t)
	site, _, _ := countingSite(h)
	update := func(set apitypes.SiteSettings) apitypes.Site {
		t.Helper()
		s, err := h.c.UpdateSite(h.ctx, testSystem, "shop", "shop.example.com", apitypes.UpdateSiteRequest{Settings: set})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	// replay is the replay row's label: the last of the four.
	replay := func(p apitypes.SitePrivacy) string {
		if len(p.Features) != 4 {
			return ""
		}
		return p.Features[3].Consent
	}
	on, none, all, bad := true, apitypes.ConsentNone, apitypes.ConsentAll, "maybe"

	s := update(apitypes.SiteSettings{})
	if p := s.Privacy; *s.Settings.Consent != apitypes.ConsentReplay || p.NeedsConsent ||
		p.Summary != "No cookies. Most sites run this without a banner." || len(p.Features) != 3 {
		t.Errorf("default: %+v", p)
	}

	s = update(apitypes.SiteSettings{Replay: &on})
	if p := s.Privacy; p.NeedsConsent || !strings.Contains(p.Summary, "Replay waits") ||
		replay(p) != apitypes.PrivacyAfterConsent {
		t.Errorf("replay waits: %+v", p)
	}
	eventually(t, "the ingest to hold replay back", func() bool { return h.rt.AnalyticsSiteRules()[site].Consent == "replay" })

	s = update(apitypes.SiteSettings{Consent: &none})
	if p := s.Privacy; !p.NeedsConsent || p.Summary != "Needs consent for: session replay." ||
		replay(p) != apitypes.PrivacyNeedsConsent {
		t.Errorf("replay at load: %+v", p)
	}
	eventually(t, "the ingest to record at load", func() bool { return h.rt.AnalyticsSiteRules()[site].Consent == "" })

	s = update(apitypes.SiteSettings{Consent: &all})
	for _, f := range s.Privacy.Features {
		if f.Consent != apitypes.PrivacyAfterConsent {
			t.Errorf("everything waits, but %s is %s", f.Name, f.Consent)
		}
	}
	eventually(t, "the ingest to hold everything back", func() bool { return h.rt.AnalyticsSiteRules()[site].Consent == "all" })

	if _, err := h.c.UpdateSite(h.ctx, testSystem, "shop", "shop.example.com", apitypes.UpdateSiteRequest{
		Settings: apitypes.SiteSettings{Consent: &bad}}); err == nil {
		t.Error("consent maybe was accepted")
	}
}
