# 0085: Analytics consent

Status: accepted (asked by Saile, 2026-11-16; research and plan in
Postplan `ftricw5aj9cy`, the UI is mock A of `wr6xtozudxgj` with B's
site-bar chip and warning line). Amends [0037](0037-analytics.md).

## Context

ADR 0037, the guide and the help topic said Dock Analytics needs "no
banner" because it sets no cookies. That holds for the cookieless basics
in common practice, not for every setting, and not with certainty in
Norway:

- Storing or reading information on the visitor's device needs consent
  unless it is strictly necessary (ePrivacy Directive Art. 5(3);
  ekomloven § 3-15 in Norway since 2025-01-01, with consent to the GDPR's
  standard). The rule covers any technique, not only cookies.
- The EDPB counts a script that tells the browser to send information as
  "gaining access" (Guidelines 2/2023 ¶33), so a cookieless script is in
  scope. Exemptions are left to national authorities.
- Norway has no exemption for statistics, and Nkom and Datatilsynet have
  not ruled on cookieless analytics. France's CNIL exempts audience
  measurement that yields anonymous statistics (salted hashes with a site
  and day component, truncated IPs, no replay). Plausible, Fathom and
  Pirsch sites run without a banner on that common reading.
- Session replay needs consent everywhere (CNIL's 2026 recommendation,
  even for debugging). No source argues otherwise.
- Every feature belongs in the site's privacy policy (GDPR Arts 13, 21).

## Decision

| Topic | Decision |
|---|---|
| Consent mode | A site setting, `consent`: `replay` (the default) runs the basics at load and holds session replay back until the page calls `dock.consent(true)`; `all` sends nothing before it; `none` runs everything at load, for sites that add the script only after their banner's consent. In the settings JSON, so no migration; sites that recorded at load before move to `replay` |
| The script | `dock.consent(true)` gives consent, `dock.consent(false)` withdraws it: the recorder stops and drops what it buffered, and with `all` nothing more is sent. Consent before the script loads: `window.dock = { consent: true }`, or `data-consent="granted"` on the script tag. Dock stores nothing; the site's banner keeps the choice. The mode is the script's config (`c.c`), a variant like replay's; the node rules carry it as an optional field, which an older node ignores and records at load |
| Status | Core computes `Site.privacy` from the settings in effect: a line for the site, and each thing it collects with a label (not needed, after consent, needs consent) and why. The UI, CLI (`site show`) and MCP (`analytics` sites) show the same words. The basics' words are Saile's: "No cookies. Most sites run this without a banner. Norwegian law has no explicit exemption for statistics." Not legal advice, and the UI says so |
| UI | A Privacy section near the top of a site's settings: the status, what it collects with consent and privacy-policy columns, the consent mode, the snippet for a banner and privacy-policy text to copy (English, Norwegian bokmål), built in the browser from the settings. A site that records without waiting gets a warning across its settings, with one button to let replay wait, and a "needs consent" chip in the site bar. The Replays view says when recording waits for consent and how to record at load instead |
| Less data | Outbound clicks keep the link without its query or fragment, as page views keep the path without its query. Replay chunks already kept only the path |
| Not now | A script-free mode counting document requests at Caddy, the only design without a script reading the device (and even then the EDPB says tracking by IP "could" be in scope). It loses single-page apps, screen size, errors and Web Vitals. The next step if Nkom ever rules against cookieless scripts |

## Consequences

- Sites with replay on stop recording on pages that don't call
  `dock.consent(true)`, from the update on (or within the hour a cached
  script lives). The release notes say so, and the Replays view and site
  settings say how to resume: call it from the banner, or set the mode to
  `none` if the script loads only after consent.
- Every site answers "do I need a banner?" in one line, in the same
  words everywhere, and the guide no longer promises more than the law
  gives.
- `s.js` grows by about 150 bytes gzipped; still under its 2 KB budget.
- If the EU adopts the Digital Omnibus's audience-measurement exemption
  (proposed Art. 88a(3)(c)), or Nkom rules, the words in
  `internal/core/site_privacy.go` change in one place.
