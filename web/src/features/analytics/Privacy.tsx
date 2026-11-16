import { useState } from "react";
import type { components } from "../../api/schema";
import { Act, CopyButton, ErrorText } from "../../ui/kit";
import { type Site, useUpdateSite } from "./data";
import { type PolicyLanguage, policyLanguages, policyText } from "./policy";

// What a site's analytics settings mean for cookies and consent (ADR 0085,
// Saile's pick A): the site's status, what it collects with a consent
// label each, the snippet a consent banner calls, and privacy-policy text
// to copy. The status and labels come from the API (Site.privacy), so the
// UI, CLI and MCP say the same. Not legal advice, and the page says so.

type Feature = components["schemas"]["PrivacyFeature"];

/** A feature's consent label as the table shows it. */
const labels: Record<Feature["consent"], { words: string; tone: string }> = {
  not_needed: { words: "Not needed", tone: "text-ok" },
  after_consent: { words: "After consent", tone: "text-warn" },
  needs_consent: { words: "Needs consent", tone: "text-fail" },
};

/** The site's status in a line, and what it leaves out. */
export function PrivacyStatus({ site }: { site: Site }) {
  const p = site.privacy;
  return (
    <span className="flex flex-col gap-0.5">
      <span
        className={`text-[15px] font-medium ${p.needs_consent ? "text-fail" : ""}`}
      >
        {p.summary}
      </span>
      {p.detail && <span className="text-mut">{p.detail}</span>}
    </span>
  );
}

/** What the site collects, with each feature's consent label and why. */
export function PrivacyTable({ site }: { site: Site }) {
  return (
    <table className="max-w-[760px] text-sm">
      <thead>
        <tr>
          <th>What the site collects</th>
          <th>Consent</th>
          <th>Privacy policy</th>
          <th>Why</th>
        </tr>
      </thead>
      <tbody>
        {(site.privacy.features ?? []).map((f) => (
          <tr key={f.name}>
            <td>{f.name}</td>
            <td className={labels[f.consent].tone}>
              {labels[f.consent].words}
            </td>
            <td>Mention</td>
            <td className="whitespace-normal text-mut">{f.why}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** What a consent banner adds to the page, for the site's consent mode. */
export function ConsentSnippet({ site }: { site: Site }) {
  if (site.settings.consent === "none")
    return (
      <span className="text-mut">
        Everything runs at load. Add the script tag only once your banner has
        the visitor's consent, or let replay wait for it.
      </span>
    );
  const code = `<script src="/_dock/a/s.js" defer></script>
// in your consent banner
dock.consent(true)   // accepted
dock.consent(false)  // declined or withdrawn`;
  return (
    <span className="flex max-w-[760px] flex-col gap-1.5">
      <span className="flex items-start gap-3">
        <pre className="m-0 flex-1 overflow-x-auto rounded-ctl border border-line bg-panel px-3 py-2 font-mono text-xs text-sec">
          {code}
        </pre>
        <CopyButton text={code} />
      </span>
      <span className="text-xs text-faint">
        Before the script loads, set{" "}
        <code>window.dock = {"{ consent: true }"}</code>, or add{" "}
        <code>data-consent="granted"</code> to the script tag. Dock stores
        nothing; your banner keeps the choice.
      </span>
    </span>
  );
}

/** The site's privacy-policy text, in English or Norwegian, to copy. */
export function PolicyText({ site }: { site: Site }) {
  const [lang, setLang] = useState<PolicyLanguage>("en");
  const paragraphs = policyText(site, lang);
  return (
    <span className="flex max-w-[760px] flex-col gap-2">
      <span className="flex items-center gap-3">
        <span className="seg flex-1 self-start">
          {policyLanguages.map(([value, words]) => (
            <button
              key={value}
              type="button"
              aria-pressed={lang === value}
              onClick={() => setLang(value)}
            >
              {words}
            </button>
          ))}
        </span>
        <CopyButton text={paragraphs.join("\n\n")} />
      </span>
      <span className="flex flex-col gap-1.5 rounded-ctl border border-line bg-panel px-3 py-2 text-sec">
        {paragraphs.map((p) => (
          <span key={p}>{p}</span>
        ))}
      </span>
    </span>
  );
}

/**
 * The warning across the settings of a site that records sessions without
 * waiting for consent, with the one change that fixes it.
 */
export function ConsentWarning({
  project,
  site,
  cmd,
  canEdit,
}: {
  project: string;
  site: Site;
  cmd: string;
  canEdit: boolean;
}) {
  const update = useUpdateSite();
  if (!site.privacy.needs_consent) return null;
  return (
    <div className="flex flex-col gap-1 rounded-ctl border border-warn-edge bg-stage-bg px-3 py-2 text-warn-text">
      <span className="flex flex-wrap items-baseline gap-x-4 gap-y-2">
        <span className="flex-1">
          Replay records visitors without asking. Let it wait for consent, or
          load the script only after your banner's consent.
        </span>
        {canEdit && (
          <Act
            small
            cli={`${cmd} --consent replay`}
            disabled={update.isPending}
            onClick={() =>
              update.mutate({
                project,
                site: site.name,
                change: { settings: { consent: "replay" } },
              })
            }
          >
            Wait for consent
          </Act>
        )}
      </span>
      <ErrorText error={update.error} />
    </div>
  );
}
