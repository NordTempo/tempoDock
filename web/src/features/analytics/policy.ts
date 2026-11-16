import type { Site } from "./data";

// Privacy-policy text for a site, built from its settings (ADR 0085): what
// Dock Analytics collects, for how long, and on what basis, in English or
// Norwegian bokmål. A starting point to copy into the site's own policy;
// the placeholders in brackets are the site owner's to fill in.

export const policyLanguages = [
  ["en", "English"],
  ["nb", "Norsk"],
] as const;

export type PolicyLanguage = (typeof policyLanguages)[number][0];

/** The words each language needs, given the site's settings. */
const words = {
  en: {
    count: (host: string, consent: boolean) =>
      `${consent ? "With your consent, we" : "We"} count visits to ${host} with Dock Analytics, which runs on our own servers. It sets no cookies and stores nothing on your device. To count a visitor once a day, your IP address and browser are combined with a random value that changes every day and is then deleted. Your IP address itself is not stored, only the city it belongs to.`,
    collect:
      "We collect the page you visit, the site you came from, campaign tags in the link, the links you follow to other sites, your browser, operating system, device, screen size and language, and your approximate location.",
    errors: " We also collect errors on our pages and how fast they load.",
    replay: (maskText: boolean) =>
      `With your consent, we record a share of visits to see how the site is used. What you type is masked${maskText ? ", and so is all text on the page" : ""}.`,
    kept: (events: number, errors?: number, replays?: number) =>
      `Visits are kept for ${events} days, then only as daily totals${errors ? `; errors for ${errors} days` : ""}${replays ? `; recordings for ${replays} days` : ""}. No one else receives the data.`,
    consentBasis:
      "We do this with your consent, which you can withdraw at any time [where visitors change their choice].",
    interest: (replay: boolean) =>
      `Our legal basis is our legitimate interest in understanding and improving the site (GDPR Article 6(1)(f))${replay ? ", and your consent for recordings" : ""}. You can object at any time by contacting us at [your contact].`,
  },
  nb: {
    count: (host: string, consent: boolean) =>
      `${consent ? "Med ditt samtykke teller vi" : "Vi teller"} besøk på ${host} med Dock Analytics, som kjører på våre egne servere. Den setter ingen informasjonskapsler og lagrer ingenting på enheten din. For å telle en besøkende én gang per dag kombineres IP-adressen og nettleseren din med en tilfeldig verdi som byttes hver dag og deretter slettes. Selve IP-adressen lagres ikke, bare byen den hører til.`,
    collect:
      "Vi samler inn siden du besøker, nettstedet du kom fra, kampanjemerker i lenken, lenkene du følger til andre nettsteder, nettleser, operativsystem, enhet, skjermstørrelse og språk, og omtrent hvor du er.",
    errors: " Vi samler også inn feil på sidene våre og hvor raskt de lastes.",
    replay: (maskText: boolean) =>
      `Med ditt samtykke tar vi opp en andel av besøkene for å se hvordan nettstedet brukes. Det du skriver, maskeres${maskText ? ", og det gjør all tekst på siden også" : ""}.`,
    kept: (events: number, errors?: number, replays?: number) =>
      `Besøk lagres i ${events} dager, deretter bare som daglige totaler${errors ? `; feil i ${errors} dager` : ""}${replays ? `; opptak i ${replays} dager` : ""}. Ingen andre får dataene.`,
    consentBasis:
      "Vi gjør dette med ditt samtykke, som du når som helst kan trekke tilbake [der besøkende endrer valget sitt].",
    interest: (replay: boolean) =>
      `Behandlingsgrunnlaget er vår berettigede interesse i å forstå og forbedre nettstedet (personvernforordningen artikkel 6 nr. 1 bokstav f)${replay ? ", og ditt samtykke for opptak" : ""}. Du kan når som helst protestere ved å kontakte oss på [kontaktinformasjon].`,
  },
} satisfies Record<PolicyLanguage, unknown>;

/** The site's privacy-policy text in a language, as paragraphs. */
export function policyText(site: Site, lang: PolicyLanguage): string[] {
  const w = words[lang];
  const s = site.settings;
  const all = s.consent === "all";
  const errors = !!s.errors_and_vitals;
  const replay = !!s.replay;
  const host = site.hostnames?.[0]?.hostname ?? site.name;
  return [
    w.count(host, all),
    w.collect + (errors ? w.errors : ""),
    ...(replay ? [w.replay(!!s.mask_all_text)] : []),
    w.kept(
      s.event_retention_days ?? 0,
      errors ? s.error_retention_days : undefined,
      replay ? s.replay_retention_days : undefined,
    ),
    all ? w.consentBasis : w.interest(replay),
  ];
}
